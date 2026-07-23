package cli

// gpu.go — the compute-worker machinery behind `hanzo link` (command wiring lives
// in link.go). It authenticates against IAM (reusing the `hanzo login` credential
// store), registers the machine + its CPU/memory/GPU inventory as a heartbeating
// presence record in the org's `fleet` tasks namespace, and runs an OUTBOUND-only
// worker loop that CLAIMS jobs from the org's `gpu-jobs` namespace — the NAT-safe
// primitive (the worker dials out; nothing dials in). The registered node then shows
// up on console.hanzo.ai's Machines + GPUs pages (provider="byo") via cloud's
// /v1/fleet union.
//
// One identity, one way: the same IAM token `hanzo login` mints (org in its `owner`
// claim) authorizes every cloud call; the server derives the tenant from the token,
// so the CLI never sends an org header. Job types are pluggable (jobHandlers); v1
// ships `echo` (trivial, no GPU — smoke/E2E) and `studio.render` (POST to the local
// ComfyUI at 127.0.0.1:8188 and poll history).
//
// --serve-engine adds the `engine.serve` capability: the worker probes a local
// hanzo-engine (the OpenAI + Anthropic model server on :1234), advertises its model
// endpoint in the presence record, and prints (or with --register-provider, POSTs)
// the /v1/add-provider call that routes api.hanzo.ai model traffic to this GPU. One
// fleet, two job types: engine.serve (model serving) alongside studio.render.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	fleetNS       = "fleet"
	defaultJobsNS = "gpu-jobs"
	// gpuQueuePrefix names a per-GPU lane WITHIN the gpu-jobs namespace: a job whose
	// taskQueue is "gpu:<identity>" is claimed ONLY by that node; the shared
	// "gpu-jobs" value stays the any-GPU broadcast. ONE convention (shared with the
	// studio dispatcher + clients/visor), so targeting is the taskQueue VALUE — not a
	// second store, a new namespace, or a new endpoint.
	gpuQueuePrefix = "gpu:"
	// heartbeatEvery keeps the presence record fresh; well under cloud's 90s
	// byoLiveWindow so one missed beat does not flap the machine offline.
	heartbeatEvery = 30 * time.Second
	claimPoll      = 2 * time.Second
	claimLeaseSecs = 120
	// renderWindow matches the dispatch cap (studio gpu_dispatch sets
	// startToCloseTimeout 14400s). The old 10m local poll undercut it and
	// marked live renders failed while they kept sampling (observed 8-70m).
	renderWindow = 4 * time.Hour
	// localComfyUI is the studio render backend the studio.render handler drives.
	localComfyUI = "http://127.0.0.1:8188"
	// localWorkerExecute is the GATED submit seam on the local studio (worker-mode):
	// the render graph is POSTed here with X-Worker-Token so ONLY the fleet worker —
	// not anything else that can reach loopback — can start a render. It replaces the
	// open /prompt POST (the hidden-run hole the studio's --worker-mode closes).
	localWorkerExecute = localComfyUI + "/v1/worker/execute"
	// defaultStudioUploadURL is where finished render outputs are POSTed so they
	// land in the org's gallery (orgs/{org}/output → S3 mirror). The render runs on
	// the LOCAL studio; the deliverable is uploaded to the org's cloud studio,
	// authorized by the user's IAM token — no S3/rclone credentials on the box.
	// Overridable per-job (input.uploadUrl) or per-box (HANZO_STUDIO_UPLOAD_URL).
	defaultStudioUploadURL = "https://studio.hanzo.ai"
	// defaultEngineURL is where --serve-engine probes hanzo-engine on THIS node.
	// hanzo-engine binds its OpenAI + Anthropic HTTP API on 0.0.0.0:1234 by default.
	defaultEngineURL = "http://localhost:1234"
)

// Fleet capability names advertised in the presence record. studioCap is always
// present (the worker claims gpu-jobs); engineCap is added by --serve-engine so the
// gateway can route model calls to a hanzo-engine running on this node.
const (
	studioCap = "studio.render"
	engineCap = "engine.serve"
)

// The node-level command surface — `hanzo link | unlink | status` — is wired in
// link.go. It composes the worker machinery below (runConnect / runDisconnect /
// runFleetStatus) with the fabric verbs (`hanzo node up|stop`).

// ---------------------------------------------------------------------------
// worker — the connect loop's state.
// ---------------------------------------------------------------------------

type worker struct {
	env      *Env
	http     *http.Client
	baseURL  string
	identity string // sanitized hostname == fleet activityId == runId
	hostname string
	jobsNS   string
	gpus     []gpuInfo
	arch     string // CPU arch (`uname -m`), detected once at newWorker
	cpuModel string // CPU model name (/proc/cpuinfo | sysctl), detected once at newWorker
	memory   int64  // total system RAM in bytes, detected once at newWorker
	handlers map[string]jobHandler

	// studioUploadURL is the org studio base that receives finished render outputs
	// (POST /upload/output). Falls back to defaultStudioUploadURL; a job may override
	// it via input.uploadUrl.
	studioUploadURL string

	// policy is this machine's sharing policy (nil = permissive). Advertised in the
	// fleet record and enforced at claim.
	policy *SharePolicy

	// studio.render preflight — a node must be able to SERVE renders before it
	// advertises studioCap or claims render lanes, else it claims jobs the gated
	// /v1/worker/execute will only refuse (poison loop). studioReady = a worker token
	// is present AND a studio is reachable (or we launch one via --studio-dir).
	// Re-evaluated on each heartbeat so a studio dying/recovering flips claiming.
	launchesStudio bool // --studio-dir set: we own the studio lifecycle
	studioReady    bool // current preflight verdict
	studioWarned   bool // loud "won't render" error emitted once per not-ready spell

	// engine.serve — advertise a hanzo-engine model server running on this node.
	serveEngine  bool
	engineURL    string               // local URL probed for /v1/models
	engineAdvURL string               // endpoint advertised for gateway routing
	engine       *engineAdvertisement // latest probe result (nil until probed)
}

// engineAdvertisement describes a hanzo-engine model server on this node.
// hanzo-engine serves the OpenAI AND Anthropic HTTP APIs from ONE axum port
// (0.0.0.0:1234), so the gateway can route model calls here as an OpenAI-compatible
// (Type=Local) provider. This rides in the fleet presence record's Input.
type engineAdvertisement struct {
	URL    string   `json:"url"`              // base the gateway calls (…:1234)
	APIs   []string `json:"apis,omitempty"`   // wire formats served: ["openai","anthropic"]
	Models []string `json:"models,omitempty"` // model ids from GET /v1/models
	Status string   `json:"status"`           // "ready" | "unreachable"
}

type gpuInfo struct {
	Name        string `json:"name"`
	MemoryTotal string `json:"memoryTotal,omitempty"`
	Arch        string `json:"arch,omitempty"`    // native target, e.g. "gfx1151" (AMD)
	Unified     bool   `json:"unified,omitempty"` // memory is a unified CPU/GPU pool (APU / SoC)
}

// registration is the fleet presence activity's Input — the shape cloud's
// clients/visor/fleet.go fleetRegistration decodes. Capabilities + Engine are
// additive (omitempty): an older cloud that does not read them still renders the
// GPU; a newer one advertises the engine endpoint on GET /v1/fleet/workers.
type registration struct {
	Hostname string `json:"hostname"`
	Os       string `json:"os"`
	// Arch/CPUs/Memory are THIS host's static CPU spec, in the SAME convention the
	// fleet already uses for code-linked run-targets: Arch is `uname -m`
	// (aarch64 | x86_64 | arm64), Memory is total system RAM in BYTES. Matching the
	// existing convention matters — evo-2 and spark appear on the board as BOTH a
	// run-target and a linked worker, so both rows must show the SAME arch.
	Arch         string               `json:"arch,omitempty"`
	CPUs         int                  `json:"cpus,omitempty"`
	CPUModel     string               `json:"cpuModel,omitempty"`
	Memory       int64                `json:"memory,omitempty"`
	Version      string               `json:"version"`
	JobQueue     string               `json:"jobQueue"`
	Rocm         string               `json:"rocm,omitempty"` // host ROCm version (AMD only)
	Hip          string               `json:"hip,omitempty"`  // host HIP version (AMD only)
	GPUs         []gpuInfo            `json:"gpus"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Engine       *engineAdvertisement `json:"engine,omitempty"`
	Policy       *SharePolicy         `json:"policy,omitempty"`
}

// SharePolicy is the per-machine sharing policy: which jobs this GPU accepts and for
// whom. ONE object on the machine record (advertised in the fleet registration) and
// enforced ONCE, at claim. A nil/zero policy is fully permissive — an unconfigured
// box behaves exactly as before. A linked GPU can thus be shared to specific orgs /
// projects / job types / models with per-scope limits, edited on the machine record
// (desktop UI / visor) and loaded here via HANZO_GPU_POLICY (inline JSON) or
// HANZO_GPU_POLICY_FILE (path).
type SharePolicy struct {
	AllowedOrgs     []string `json:"allowedOrgs,omitempty"`     // org owners allowed to run here (empty = any)
	AllowedProjects []string `json:"allowedProjects,omitempty"` // project ids allowed (empty = any)
	AllowedJobTypes []string `json:"allowedJobTypes,omitempty"` // e.g. ["studio.render"] (empty = any this worker handles)
	AllowedModels   []string `json:"allowedModels,omitempty"`   // model ids allowed (empty = any)
	MaxConcurrent   int      `json:"maxConcurrent,omitempty"`   // 0 = unbounded (the worker is serial today)
}

// loadSharePolicy reads the machine's sharing policy from HANZO_GPU_POLICY (inline
// JSON) or HANZO_GPU_POLICY_FILE (a path to it). Returns (nil, nil) when unset.
func loadSharePolicy() (*SharePolicy, error) {
	raw := os.Getenv("HANZO_GPU_POLICY")
	if raw == "" {
		if f := os.Getenv("HANZO_GPU_POLICY_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, fmt.Errorf("read HANZO_GPU_POLICY_FILE: %w", err)
			}
			raw = string(b)
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var p SharePolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("parse share policy: %w", err)
	}
	return &p, nil
}

// reject returns a non-empty reason when this machine's policy forbids running the
// claimed job, or "" to allow it. It reads org/project/model from the job input
// (best-effort: an absent field skips its gate). Enforcement point is the claim loop,
// so a declined job is failed back for an eligible worker to take.
func (p *SharePolicy) reject(jobType string, input json.RawMessage) string {
	if p == nil {
		return ""
	}
	if len(p.AllowedJobTypes) > 0 && !slices.Contains(p.AllowedJobTypes, jobType) {
		return fmt.Sprintf("job type %q not allowed", jobType)
	}
	var meta struct {
		Org     string `json:"org"`
		Project string `json:"project"`
		Model   string `json:"model"`
	}
	_ = json.Unmarshal(input, &meta)
	if len(p.AllowedOrgs) > 0 && meta.Org != "" && !slices.Contains(p.AllowedOrgs, meta.Org) {
		return fmt.Sprintf("org %q not allowed", meta.Org)
	}
	if len(p.AllowedProjects) > 0 && meta.Project != "" && !slices.Contains(p.AllowedProjects, meta.Project) {
		return fmt.Sprintf("project %q not allowed", meta.Project)
	}
	if len(p.AllowedModels) > 0 && meta.Model != "" && !slices.Contains(p.AllowedModels, meta.Model) {
		return fmt.Sprintf("model %q not allowed", meta.Model)
	}
	return ""
}

type jobHandler func(ctx context.Context, input json.RawMessage) (any, error)

func newWorker(env *Env, jobsNS string) (*worker, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "worker"
	}
	id := sanitizeID(host)
	w := &worker{
		env:             env,
		http:            &http.Client{Timeout: 60 * time.Second},
		baseURL:         env.CloudURL,
		identity:        id,
		hostname:        host,
		jobsNS:          firstNonEmpty(jobsNS, defaultJobsNS),
		gpus:            detectGPUs(),
		arch:            detectArch(),
		cpuModel:        detectCPUModel(),
		memory:          detectMemTotal(),
		studioUploadURL: firstNonEmpty(os.Getenv("HANZO_STUDIO_UPLOAD_URL"), defaultStudioUploadURL),
	}
	policy, err := loadSharePolicy()
	if err != nil {
		return nil, err
	}
	w.policy = policy
	w.handlers = map[string]jobHandler{
		"echo":          echoHandler,
		"studio.render": w.studioRenderHandler,
	}
	return w, nil
}

var idRE = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitizeID lower-cases and reduces s to the [a-z0-9-] alphabet valid as a
// tasks path segment / activity id, so a hostname is a stable machine key.
func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = idRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "worker"
	}
	return s
}

// detectGPUs reports the machine's accelerators as first-class resources — NVIDIA
// (nvidia-smi), AMD (rocm-smi / kfd topology / vulkaninfo), or the Apple Silicon
// integrated GPU with its unified memory. Each vendor path is tried in turn; the
// first that reports a card wins. Degrades gracefully to an empty list (CPU-only)
// when none is present.
func detectGPUs() []gpuInfo {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return detectAppleGPU()
	}
	if g := detectNvidiaGPUs(); len(g) > 0 {
		return g
	}
	if g := detectAmdGPUs(); len(g) > 0 {
		return g
	}
	return nil
}

// detectNvidiaGPUs reports NVIDIA accelerators via nvidia-smi (name + total VRAM).
func detectNvidiaGPUs() []gpuInfo {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader").Output()
	if err != nil {
		return nil
	}
	var gpus []gpuInfo
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		name, mem, _ := strings.Cut(line, ",")
		gpus = append(gpus, gpuInfo{Name: strings.TrimSpace(name), MemoryTotal: strings.TrimSpace(mem)})
	}
	return gpus
}

// detectAmdGPUs reports AMD accelerators — discrete Radeon cards and gfx APUs alike
// (e.g. evo's gfx1151 Radeon 8060S on the RYZEN AI MAX+ 395). Resolution order:
// rocm-smi (marketing name + gfx target), then the kfd topology under /sys (gfx
// target from gfx_target_version, GPU nodes only), then a vulkaninfo summary. VRAM
// is filled best-effort from the amdgpu sysfs mem_info_vram_total, positionally.
func detectAmdGPUs() []gpuInfo {
	if out, err := exec.Command("rocm-smi", "--showproductname", "--csv").Output(); err == nil {
		if gpus := parseRocmSmiCSV(out); len(gpus) > 0 {
			return fillAmdVRAM(gpus, amdVRAMTotals(sysfsDRM))
		}
	}
	if gpus := parseKfdTopology(sysfsKfdNodes); len(gpus) > 0 {
		return fillAmdVRAM(gpus, amdVRAMTotals(sysfsDRM))
	}
	if out, err := exec.Command("vulkaninfo", "--summary").Output(); err == nil {
		if gpus := parseVulkaninfoSummary(out); len(gpus) > 0 {
			return gpus
		}
	}
	return nil
}

// sysfs roots, indirected so tests can point them at fixtures.
var (
	sysfsKfdNodes = "/sys/class/kfd/kfd/topology/nodes"
	sysfsDRM      = "/sys/class/drm"
)

// parseRocmSmiCSV parses `rocm-smi --showproductname --csv` into one gpuInfo per
// card, naming it "<Card Series> (<GFX Version>)" (e.g. "Radeon 8060S Graphics
// (gfx1151)"). Column order is read from the header so it survives field additions.
func parseRocmSmiCSV(out []byte) []gpuInfo {
	sc := bufio.NewScanner(bytes.NewReader(out))
	var series, gfx = -1, -1
	var gpus []gpuInfo
	for sc.Scan() {
		fields := strings.Split(strings.TrimSpace(sc.Text()), ",")
		if len(fields) < 2 {
			continue
		}
		if series < 0 { // header row
			for i, f := range fields {
				switch strings.TrimSpace(f) {
				case "Card Series":
					series = i
				case "GFX Version":
					gfx = i
				}
			}
			continue
		}
		if series >= len(fields) {
			continue
		}
		name := strings.TrimSpace(fields[series])
		if name == "" {
			continue
		}
		arch := ""
		if gfx >= 0 && gfx < len(fields) {
			if v := strings.TrimSpace(fields[gfx]); v != "" {
				arch = v
				name += " (" + v + ")"
			}
		}
		if fam := amdFamily(arch); fam != "" {
			name = fam + " \u00b7 " + name
		}
		gpus = append(gpus, gpuInfo{Name: name, Arch: arch})
	}
	return gpus
}

// parseKfdTopology reads the amdgpu kfd topology under nodesDir and reports one
// gpuInfo per GPU node — a node with simd_count > 0 (node 0 is the CPU) — naming it
// from the decoded gfx_target_version (110501 → "gfx1151"). Nodes are visited in
// numeric order so the list matches the DRM card order VRAM is read in.
func parseKfdTopology(nodesDir string) []gpuInfo {
	entries, err := os.ReadDir(nodesDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Slice(names, func(i, j int) bool { return atoiSafe(names[i]) < atoiSafe(names[j]) })
	var gpus []gpuInfo
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(nodesDir, n, "properties"))
		if err != nil {
			continue
		}
		simd, gfxVer := kfdNodeProps(b)
		if simd <= 0 || gfxVer <= 0 {
			continue // CPU node or non-GPU
		}
		arch := gfxName(gfxVer)
		name := "AMD GPU (" + arch + ")"
		if fam := amdFamily(arch); fam != "" {
			name = fam + " (" + arch + ")"
		}
		gpus = append(gpus, gpuInfo{Name: name, Arch: arch})
	}
	return gpus
}

// kfdNodeProps extracts simd_count and gfx_target_version from a kfd node's
// properties file (space-separated "key value" lines).
func kfdNodeProps(properties []byte) (simd, gfxVer int) {
	sc := bufio.NewScanner(bytes.NewReader(properties))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		switch k {
		case "simd_count":
			simd = atoiSafe(strings.TrimSpace(v))
		case "gfx_target_version":
			gfxVer = atoiSafe(strings.TrimSpace(v))
		}
	}
	return simd, gfxVer
}

// gfxName decodes a kfd gfx_target_version into the LLVM gfx target string:
// 110501 → "gfx1151" (major=v/10000, minor=(v/100)%100, step=v%100).
func gfxName(v int) string {
	return fmt.Sprintf("gfx%d%d%d", v/10000, (v/100)%100, v%100)
}

// amdFamily maps a gfx target to the APU/SoC family marketing name, so the board
// says what the silicon IS ("AMD Strix Halo"), not just the iGPU series. Discrete
// cards return "" and keep their own name.
func amdFamily(arch string) string {
	switch arch {
	case "gfx1151":
		return "AMD Strix Halo"
	case "gfx1150":
		return "AMD Strix Point"
	case "gfx1103":
		return "AMD Phoenix"
	}
	return ""
}

// amdSoft is the host ROCm/HIP toolchain inventory, detected once: ROCm from
// /opt/rocm/.info/version, HIP from `hipconfig --version` (which exists even on
// TheRock-style installs that lack the .info file). Empty on non-AMD hosts.
type amdSoft struct{ rocm, hip string }

var amdSoftware = sync.OnceValue(func() amdSoft {
	var v amdSoft
	if b, err := os.ReadFile("/opt/rocm/.info/version"); err == nil {
		v.rocm = strings.TrimSpace(string(b))
	}
	if out, err := exec.Command("hipconfig", "--version").Output(); err == nil {
		v.hip = strings.TrimSpace(string(out))
	}
	return v
})

// pickAmdMem chooses the honest memory figure for a card: dedicated VRAM for a
// discrete GPU, the GTT pool for an APU whose "VRAM" is a token carve-out (Strix
// Halo reports 1 GiB VRAM + a ~120 GiB unified GTT pool). Unified when GTT wins.
func pickAmdMem(vramMiB, gttMiB int64) (miB int64, unified bool) {
	if gttMiB > vramMiB*4 {
		return gttMiB, true
	}
	return vramMiB, false
}

// amdMem is one card's memory inventory in MiB: dedicated VRAM plus the GTT
// (system-memory) pool an APU actually computes in.
type amdMem struct{ vramMiB, gttMiB int64 }

// amdVRAMTotals returns each amdgpu card's memory totals in MiB, in DRM card order,
// read from /sys/class/drm/card*/device/mem_info_{vram,gtt}_total (vendor 0x1002).
func amdVRAMTotals(drmDir string) []amdMem {
	entries, err := os.ReadDir(drmDir)
	if err != nil {
		return nil
	}
	cards := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "card") && !strings.Contains(n, "-") {
			cards = append(cards, n)
		}
	}
	sort.Slice(cards, func(i, j int) bool {
		return atoiSafe(strings.TrimPrefix(cards[i], "card")) < atoiSafe(strings.TrimPrefix(cards[j], "card"))
	})
	readMiB := func(path string) int64 {
		b, err := os.ReadFile(path)
		if err != nil {
			return 0
		}
		var n int64
		if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &n); err != nil || n <= 0 {
			return 0
		}
		return n / (1024 * 1024)
	}
	var mems []amdMem
	for _, c := range cards {
		dev := filepath.Join(drmDir, c, "device")
		if vendor, _ := os.ReadFile(filepath.Join(dev, "vendor")); strings.TrimSpace(string(vendor)) != "0x1002" {
			continue
		}
		vram := readMiB(filepath.Join(dev, "mem_info_vram_total"))
		if vram == 0 {
			continue
		}
		mems = append(mems, amdMem{vramMiB: vram, gttMiB: readMiB(filepath.Join(dev, "mem_info_gtt_total"))})
	}
	return mems
}

// fillAmdVRAM attaches memory totals to the GPU list positionally when the counts
// match (the common single-GPU case always does); otherwise the names stand alone.
// An APU reports its unified GTT pool, not the token VRAM carve-out.
func fillAmdVRAM(gpus []gpuInfo, mems []amdMem) []gpuInfo {
	if len(mems) != len(gpus) {
		return gpus
	}
	for i := range gpus {
		miB, unified := pickAmdMem(mems[i].vramMiB, mems[i].gttMiB)
		gpus[i].MemoryTotal = fmt.Sprintf("%d MiB", miB)
		gpus[i].Unified = unified
	}
	return gpus
}

// parseVulkaninfoSummary is the last-resort AMD path: it scrapes GPU device names
// from `vulkaninfo --summary` (the "deviceName = ..." lines), keeping AMD/Radeon
// devices only so it never double-counts an NVIDIA card already handled upstream.
func parseVulkaninfoSummary(out []byte) []gpuInfo {
	sc := bufio.NewScanner(bytes.NewReader(out))
	var gpus []gpuInfo
	for sc.Scan() {
		_, v, ok := strings.Cut(sc.Text(), "deviceName")
		if !ok {
			continue
		}
		name := strings.TrimSpace(strings.TrimLeft(v, " =\t"))
		if name == "" {
			continue
		}
		if l := strings.ToLower(name); strings.Contains(l, "amd") || strings.Contains(l, "radeon") || strings.Contains(l, "gfx") {
			gpus = append(gpus, gpuInfo{Name: name})
		}
	}
	return gpus
}

// atoiSafe parses an int, returning 0 on any error (0 never denotes a GPU node).
func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// detectAppleGPU reports the Apple Silicon chip as one GPU with the machine's
// unified memory (Metal/MPS shares it all), MiB-formatted like nvidia-smi.
func detectAppleGPU() []gpuInfo {
	brand, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err != nil {
		return nil
	}
	name := strings.TrimSpace(string(brand))
	if !strings.HasPrefix(name, "Apple") {
		return nil
	}
	info := gpuInfo{Name: name + " (Metal)"}
	if mem, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
		var b int64
		if _, err := fmt.Sscan(strings.TrimSpace(string(mem)), &b); err == nil && b > 0 {
			info.MemoryTotal = fmt.Sprintf("%d MiB", b/(1024*1024))
		}
	}
	return []gpuInfo{info}
}

// detectArch reports this machine's CPU architecture in the SAME convention the
// fleet already uses for code-linked nodes — `uname -m` (aarch64 | x86_64 on Linux,
// arm64 | x86_64 on Darwin) — so a machine that shows up as both a run-target and a
// linked worker carries ONE arch string on the board. Falls back to the
// compiled runtime.GOARCH only if uname is unavailable; "" is never forced.
func detectArch() string {
	if out, err := exec.Command("uname", "-m").Output(); err == nil {
		if a := strings.TrimSpace(string(out)); a != "" {
			return a
		}
	}
	return runtime.GOARCH
}

// detectCPUModel reports this machine's CPU model name so the node advertises its
// processor, not just its core count — Linux reads /proc/cpuinfo's "model name"
// (x86) or "Model" (arm64 boards like spark's GB10), Darwin reads sysctl
// machdep.cpu.brand_string. Empty when unreadable (reported via omitempty, never
// faked); a bare `uname -m` arch still travels on the record.
func detectCPUModel() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
		return ""
	}
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name", "Model", "cpu model":
			if v := strings.TrimSpace(val); v != "" {
				return v
			}
		}
	}
	return ""
}

// detectMemTotal returns this machine's total physical RAM in bytes, or 0 when it
// cannot be read (reported as "unknown" via omitempty — never faked). Linux reads
// /proc/meminfo's MemTotal (covers evo-2's Strix Halo and spark's GB10, both Linux);
// Darwin reads sysctl hw.memsize. This is the SAME total a code-linked box reports
// as Spec.Memory, so the fleet board describes both kinds of node identically.
func detectMemTotal() int64 {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0
		}
		var b int64
		if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &b); err == nil && b > 0 {
			return b
		}
		return 0
	}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMemTotalKB(b)
}

// parseMemTotalKB extracts MemTotal from /proc/meminfo content (reported in kB) and
// returns it in bytes, or 0 when the line is absent or malformed.
func parseMemTotalKB(meminfo []byte) int64 {
	sc := bufio.NewScanner(bytes.NewReader(meminfo))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		f := strings.Fields(line) // "MemTotal:" <kb> "kB"
		if len(f) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || kb <= 0 {
			return 0
		}
		return kb * 1024
	}
	return 0
}

// sampleProbeTimeout hard-caps the nvidia-smi probe; sampleReportTimeout bounds the
// whole detached report (probe + POST). A hung nvidia-smi (GPU/driver pressure) thus
// self-cancels instead of wedging the worker's select loop.
const (
	sampleProbeTimeout  = 5 * time.Second
	sampleReportTimeout = 20 * time.Second
)

// nvidiaSmi runs the utilization query bounded by ctx (CommandContext kills it when
// ctx fires). A package var so a test can substitute a hung/slow probe and prove it
// can never block the worker loop.
var nvidiaSmi = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=utilization.gpu,memory.used,memory.free",
		"--format=csv,noheader,nounits").Output()
}

// sampleReport is this machine's live GPU utilization for the fleet series.
type sampleReport struct {
	GPUUtil  float64 // mean across cards, 0..1
	GPUs     int
	GPUModel string
	MemUsed  int64 // bytes, summed across cards
	MemFree  int64 // bytes, summed across cards
}

// sampleGPUs reads live utilization from nvidia-smi (utilization.gpu %, memory
// used/free MiB), averaging util to 0..1 and summing memory across cards. A box
// without nvidia-smi (Apple / CPU-only) returns a zero-util report that still
// carries its GPU count + model — a valid liveness point, never faked numbers.
func (w *worker) sampleGPUs(ctx context.Context) sampleReport {
	rep := sampleReport{GPUs: len(w.gpus)}
	if len(w.gpus) > 0 {
		rep.GPUModel = w.gpus[0].Name
	}
	pctx, cancel := context.WithTimeout(ctx, sampleProbeTimeout)
	defer cancel()
	out, err := nvidiaSmi(pctx)
	if err != nil {
		return rep // hung/absent nvidia-smi self-cancels → inventory-only report, never a stall
	}
	var utilSum float64
	var n int
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 3 {
			continue
		}
		if u, e := strconv.ParseFloat(strings.TrimSpace(f[0]), 64); e == nil {
			utilSum += u
			n++
		}
		if used, e := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64); e == nil {
			rep.MemUsed += used << 20 // MiB → bytes
		}
		if free, e := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64); e == nil {
			rep.MemFree += free << 20
		}
	}
	if n > 0 {
		rep.GPUUtil = utilSum / float64(n) / 100.0 // percent → 0..1
	}
	return rep
}

// reportSample posts this machine's live utilization to the org's fleet series
// (POST /v1/fleet/samples) so the console board shows THIS GPU's load beside its
// inventory. DETACHED + bounded: the probe and POST run on their OWN goroutine under
// a hard timeout, so a hung nvidia-smi (GPU/driver pressure) or a slow POST can NEVER
// block the worker's select loop — heartbeats and claims keep flowing. At most one
// report is in flight per ticker site (interval ≫ budget), and it self-cancels on
// worker shutdown (parent ctx). A dropped sample is the server's to log, never the
// worker's to stall on.
func (w *worker) reportSample(parent context.Context) {
	go func() {
		ctx, cancel := context.WithTimeout(parent, sampleReportTimeout)
		defer cancel()
		r := w.sampleGPUs(ctx)
		_, _ = w.call(ctx, http.MethodPost, "/v1/fleet/samples", map[string]any{
			"unit":     w.identity,
			"host":     w.hostname,
			"gpuUtil":  r.GPUUtil,
			"gpus":     r.GPUs,
			"gpuModel": r.GPUModel,
			"memUsed":  r.MemUsed,
			"memFree":  r.MemFree,
		}, nil)
	}()
}

// ---------------------------------------------------------------------------
// connect.
// ---------------------------------------------------------------------------

// connectOpts is the resolved `hanzo link` compute-worker configuration.
type connectOpts struct {
	jobsNS           string
	serveEngine      bool
	engineURL        string // local URL to probe hanzo-engine
	engineEndpoint   string // public URL to advertise (defaults to engineURL)
	registerProvider bool   // auto POST /v1/add-provider for the engine
	studioDir        string // local Studio checkout to launch + supervise on :8188
	studioURL        string // studio base the render mirror uploads finished images to
	mirror           bool   // sweep local renders into the org studio library (default on)
}

func runConnect(cmd *cobra.Command, env *Env, opts connectOpts) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, err := newWorker(env, opts.jobsNS)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// engine.serve: probe the local hanzo-engine once so the first registration
	// carries its live model list + reachability.
	if opts.serveEngine {
		w.serveEngine = true
		w.engineURL = firstNonEmpty(opts.engineURL, defaultEngineURL)
		w.engineAdvURL = firstNonEmpty(opts.engineEndpoint, w.engineURL)
		w.refreshEngine(ctx)
	}

	// studio.render preflight: decide whether this node can SERVE renders BEFORE it
	// advertises studioCap or claims a render lane. A --studio-dir node launches its
	// own studio (assumed reachable once up); a BYO node probes an already-running one.
	// The worker token is required either way — the gated execute seam refuses without it.
	w.launchesStudio = opts.studioDir != ""
	w.refreshStudioReady(ctx)

	if err := w.register(ctx); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	fmt.Fprintf(out, "linked %q into %s as a node (org %s)\n", w.hostname, w.baseURL, orgOf(env))
	fmt.Fprintf(out, "  cpu: %s\n", describeCPU(w))
	fmt.Fprintf(out, "  gpu: %s\n", describeGPUs(w.gpus))
	w.warnIfCannotRender(cmd.ErrOrStderr())

	// studio.render backend: the claim loop drives the LOCAL studio server, so
	// when a checkout is named we own its lifecycle too — no separate watchdog.
	if opts.studioDir != "" {
		go superviseStudio(ctx, opts.studioDir, out)
	}
	fmt.Fprintf(out, "claiming %s jobs; heartbeating every %s. Ctrl-C to stop (the node goes offline after ~90s; `hanzo unlink` removes it).\n", w.jobsNS, heartbeatEvery)

	if w.serveEngine {
		w.printEngineHint(out)
		if opts.registerProvider {
			if err := w.registerProvider(ctx, w.engine); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "register-provider: %v\n", err)
			} else {
				fmt.Fprintf(out, "  → registered org model provider %q on %s\n", "gpu-"+w.identity, w.baseURL)
			}
		}
	}

	hb := time.NewTicker(heartbeatEvery)
	defer hb.Stop()
	poll := time.NewTicker(claimPoll)
	defer poll.Stop()

	// Render mirror — independent of claims by design. It scans the local studio
	// output tree every heartbeatEvery and uploads every image to the org's library
	// (POST /v1/library/upload), so EVERY render lands in studio.hanzo.ai even when
	// it was produced outside the job path — a graph hand-run on this node, or a
	// render that finished after its activity was reaped (the stranded-late-render
	// class). Active only when a studio checkout is named (there is local output to
	// mirror); a nil channel case never fires when it is not.
	w.studioUploadURL = firstNonEmpty(opts.studioURL, w.studioUploadURL)
	mirrorBase := w.studioUploadURL
	mirrorDir := ""
	seen := map[string]int64{}
	var mirC <-chan time.Time
	if opts.studioDir != "" && opts.mirror {
		mirrorDir = filepath.Join(opts.studioDir, "output")
		mir := time.NewTicker(heartbeatEvery)
		defer mir.Stop()
		mirC = mir.C
	}

	// Heartbeat once immediately so the machine reports online without waiting a
	// full interval, and report an initial utilization sample.
	_ = w.heartbeat(ctx)
	w.reportSample(ctx)

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "\nstopping (node will go offline; run `hanzo unlink` to remove it)")
			return nil
		case <-hb.C:
			// Re-probe the engine; if its reachability or model set changed, rewrite
			// the presence record so the fleet advertisement stays honest (e.g. the
			// engine came up after connect, or loaded a new model).
			if w.serveEngine && w.refreshEngine(ctx) {
				if err := w.register(ctx); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "re-advertise engine: %v\n", err)
				} else {
					fmt.Fprintf(out, "engine %s — %s\n", w.engine.URL, describeEngine(w.engine))
				}
			}
			// Re-evaluate render readiness; if it flipped, re-advertise (studioCap on/off)
			// so the fleet never shows a node accepting renders it can't serve.
			if w.refreshStudioReady(ctx) {
				if err := w.register(ctx); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "re-advertise capabilities: %v\n", err)
				}
				if w.studioReady {
					w.studioWarned = false
					fmt.Fprintf(out, "studio ready — now accepting render jobs\n")
				} else {
					w.warnIfCannotRender(cmd.ErrOrStderr())
				}
			}
			if err := w.heartbeat(ctx); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "heartbeat: %v\n", err)
			}
			w.reportSample(ctx)
		case <-poll.C:
			if err := w.claimAndRun(ctx, out); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "claim: %v\n", err)
			}
		case <-mirC:
			w.mirrorRenders(ctx, out, mirrorDir, mirrorBase, seen)
		}
	}
}

// register ensures the fleet + jobs namespaces exist, then writes this machine's
// presence record (activityId==runId==identity, no requestId so a reconnect
// overwrites any prior/terminal record with a fresh online row).
func (w *worker) register(ctx context.Context) error {
	for _, ns := range []string{fleetNS, w.jobsNS} {
		if _, err := w.call(ctx, http.MethodPost, "/v1/tasks/namespaces", map[string]any{
			"namespaceInfo": map[string]any{"name": ns},
		}, nil); err != nil {
			return fmt.Errorf("ensure namespace %q: %w", ns, err)
		}
	}
	_, err := w.call(ctx, http.MethodPost, "/v1/tasks/namespaces/"+fleetNS+"/activities", map[string]any{
		"activityId":       w.identity,
		"runId":            w.identity,
		"activityType":     map[string]any{"name": "fleet.worker"},
		"taskQueue":        fleetNS,
		"heartbeatTimeout": "120s",
		"input":            w.buildRegistration(),
	}, nil)
	return err
}

// buildRegistration is the pure presence-record payload: this machine's inventory,
// its advertised capabilities, and (when serving) its hanzo-engine endpoint.
func (w *worker) buildRegistration() registration {
	return registration{
		Hostname:     w.hostname,
		Os:           runtime.GOOS,
		Arch:         w.arch,
		CPUs:         runtime.NumCPU(),
		CPUModel:     w.cpuModel,
		Memory:       w.memory,
		Version:      Version,
		JobQueue:     w.jobsNS,
		Rocm:         amdSoftware().rocm,
		Hip:          amdSoftware().hip,
		GPUs:         w.gpus,
		Capabilities: w.capabilities(),
		Engine:       w.engine,
		Policy:       w.policy,
	}
}

// capabilities lists what this worker does for the org. studio.render is always
// present (it claims gpu-jobs); engine.serve is added when --serve-engine advertises
// a local hanzo-engine model server.
func (w *worker) capabilities() []string {
	caps := []string{}
	if w.studioReady {
		caps = append(caps, studioCap) // only advertised when this node can actually render
	}
	if w.serveEngine {
		caps = append(caps, engineCap)
	}
	return caps
}

// studioReachable probes the local studio's /queue (bounded), authenticated. A node
// that can answer it is up enough to accept a render.
func (w *worker) studioReachable(ctx context.Context) bool {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, localComfyUI+"/queue", nil)
	if err != nil {
		return false
	}
	authLocal(req)
	resp, err := w.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	return resp.StatusCode/100 == 2
}

// refreshStudioReady recomputes whether this node can serve renders and returns
// whether the verdict changed. Ready ⇔ a worker token is present AND (we launch the
// studio ourselves via --studio-dir, or a studio is reachable now). The token is the
// decisive gate: without it the gated execute seam 403s every job. A missing studio
// on a node that does NOT launch one also blocks — it would only refuse renders.
func (w *worker) refreshStudioReady(ctx context.Context) bool {
	ready := workerToken() != "" && (w.launchesStudio || w.studioReachable(ctx))
	changed := ready != w.studioReady
	w.studioReady = ready
	return changed
}

// warnIfCannotRender emits the loud "this node won't render" message once per
// not-ready spell (latched), and clears the latch when the node recovers so a later
// regression warns again.
func (w *worker) warnIfCannotRender(errw io.Writer) {
	if w.studioReady {
		w.studioWarned = false
		return
	}
	if w.studioWarned {
		return
	}
	w.studioWarned = true
	fmt.Fprintf(errw, "WARNING: this node will NOT accept renders — %s. It heartbeats as present but claims no render jobs.\n", w.studioBlockReason())
}

// studioBlockReason explains why a node won't serve renders, for the loud operator
// warning. Empty when the node IS ready.
func (w *worker) studioBlockReason() string {
	if w.studioReady {
		return ""
	}
	if workerToken() == "" {
		return "STUDIO_WORKER_TOKEN not set (the gated studio seam would refuse every render) — set it from KMS"
	}
	return fmt.Sprintf("no studio reachable on %s (start one, or pass --studio-dir so the worker launches it)", localComfyUI)
}

func (w *worker) heartbeat(ctx context.Context) error {
	path := fmt.Sprintf("/v1/tasks/namespaces/%s/activities/%s/%s/heartbeat", fleetNS, w.identity, w.identity)
	_, err := w.call(ctx, http.MethodPost, path, map[string]any{
		"details": map[string]any{"gpus": len(w.gpus), "ts": time.Now().UTC().Format(time.RFC3339)},
	}, nil)
	return err
}

// gpuQueue is this worker's OWN task-queue lane — the value a job names to target
// THIS machine ("gpu:spark"). Targeted jobs land here; the shared w.jobsNS
// ("gpu-jobs") lane stays the any-GPU broadcast. ONE namespace (w.jobsNS), two
// queue VALUES — targeting is the string, not a second store or endpoint.
func (w *worker) gpuQueue() string { return gpuQueuePrefix + w.identity }

// claimFrom claims the next job on ONE task-queue value within w.jobsNS. Returns
// (act, true, nil) when a job was claimed, (_, false, nil) on an empty queue (204).
func (w *worker) claimFrom(ctx context.Context, taskQueue string) (claimedActivity, bool, error) {
	var act claimedActivity
	code, err := w.call(ctx, http.MethodPost, "/v1/tasks/namespaces/"+w.jobsNS+"/activities/claim", map[string]any{
		"taskQueue":    taskQueue,
		"identity":     w.identity,
		"leaseSeconds": claimLeaseSecs,
	}, &act)
	if err != nil {
		return claimedActivity{}, false, err
	}
	return act, code != http.StatusNoContent, nil
}

// claimAndRun claims the next job — this machine's OWN lane first (gpu:<identity>),
// then the shared any-GPU lane — runs its handler, and reports the terminal result.
// Both lanes empty is a no-op. Targeted-lane-first guarantees a job pinned to THIS
// GPU is never starved by shared work; a job is only ever claimed under an explicit
// queue name, so one worker never steals another GPU's targeted job (an empty
// taskQueue on claim would match any lane and do exactly that).
func (w *worker) claimAndRun(ctx context.Context, out io.Writer) error {
	// Poison-loop guard: a node that can't serve renders must not claim render lanes —
	// it would only fail every claimed job on the gated execute seam. It still
	// heartbeats presence (shows in the fleet), just idle, until it becomes ready.
	if !w.studioReady {
		return nil
	}
	act, claimed, err := w.claimFrom(ctx, w.gpuQueue())
	if err != nil {
		return err
	}
	if !claimed {
		if act, claimed, err = w.claimFrom(ctx, w.jobsNS); err != nil {
			return err
		}
		if !claimed {
			return nil // both lanes empty — nothing to do
		}
	}
	wf, run := act.Execution.WorkflowId, act.Execution.RunId
	fmt.Fprintf(out, "claimed job %s (type %s)\n", wf, act.Type.Name)

	// Sharing policy: decline (fail back) a job this machine isn't allowed to run,
	// so an eligible worker can take it. Enforced once, here, at claim.
	if reason := w.policy.reject(act.Type.Name, act.Input); reason != "" {
		_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "fail"), map[string]any{"cause": "share policy: " + reason, "identity": w.identity}, nil)
		fmt.Fprintf(out, "  → declined (share policy: %s)\n", reason)
		return nil
	}

	h, ok := w.handlers[act.Type.Name]
	if !ok {
		cause := fmt.Sprintf("no handler for job type %q", act.Type.Name)
		_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "fail"), map[string]any{"cause": cause, "identity": w.identity}, nil)
		fmt.Fprintf(out, "  → failed: %s\n", cause)
		return nil
	}
	// Keep BOTH the claimed activity and this machine's fleet presence alive while
	// the handler runs. A render blocks this call for minutes (a cold GB10 reloads
	// ~40GB before sampling); without heartbeats the studio.render activity hits its
	// heartbeatTimeout AND the fleet presence (120s) goes stale, so the machine
	// drops offline mid-render and the next dispatch sees no online GPU. A ticker in
	// a child context heartbeats both every heartbeatEvery until the handler returns.
	hbCtx, stopHB := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "heartbeat"),
					map[string]any{"identity": w.identity}, nil)
				_ = w.heartbeat(ctx) // fleet presence — stays online through the render
				w.reportSample(ctx)  // live util during the render (the board's hottest point)
			}
		}
	}()
	result, herr := h(ctx, act.Input)
	stopHB()
	if herr != nil {
		_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "fail"), map[string]any{"cause": herr.Error(), "identity": w.identity}, nil)
		fmt.Fprintf(out, "  → failed: %v\n", herr)
		return nil
	}
	if _, err := w.call(ctx, http.MethodPost, w.actPath(wf, run, "complete"), map[string]any{"result": result, "identity": w.identity}, nil); err != nil {
		return fmt.Errorf("complete %s: %w", wf, err)
	}
	fmt.Fprintf(out, "  → completed\n")
	return nil
}

func (w *worker) actPath(wf, run, verb string) string {
	return fmt.Sprintf("/v1/tasks/namespaces/%s/activities/%s/%s/%s", w.jobsNS, wf, run, verb)
}

type claimedActivity struct {
	Execution struct {
		WorkflowId string `json:"workflowId"`
		RunId      string `json:"runId"`
	} `json:"execution"`
	Type struct {
		Name string `json:"name"`
	} `json:"type"`
	Input     json.RawMessage `json:"input"`
	TaskQueue string          `json:"taskQueue"`
	Status    string          `json:"status"`
}

// ---------------------------------------------------------------------------
// status / disconnect.
// ---------------------------------------------------------------------------

type fleetWorker struct {
	ID            string               `json:"id"`
	Hostname      string               `json:"hostname"`
	Provider      string               `json:"provider"`
	Location      string               `json:"location"`
	Status        string               `json:"status"`
	Arch          string               `json:"arch,omitempty"`
	CPUs          int                  `json:"cpus,omitempty"`
	CPUModel      string               `json:"cpuModel,omitempty"`
	Memory        int64                `json:"memory,omitempty"`
	GPUs          []gpuInfo            `json:"gpus"`
	LastHeartbeat string               `json:"lastHeartbeat"`
	Capabilities  []string             `json:"capabilities,omitempty"`
	Engine        *engineAdvertisement `json:"engine,omitempty"`
}

// runFleetStatus renders the org's fleet: every node with its CPU inventory and
// each GPU shown distinctly, this box highlighted with `*`. This is the `hanzo
// status` view; --output=json emits the raw worker list.
func runFleetStatus(cmd *cobra.Command, env *Env) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, _ := newWorker(env, "")
	var resp struct {
		Workers []fleetWorker `json:"workers"`
	}
	if _, err := w.call(ctx, http.MethodGet, "/v1/fleet/workers", nil, &resp); err != nil {
		return err
	}
	return env.emit(resp.Workers, func(out io.Writer) {
		if len(resp.Workers) == 0 {
			fmt.Fprintln(out, "no nodes linked. Run `hanzo link` on a machine to bring it into the fleet.")
			return
		}
		online := 0
		for _, fw := range resp.Workers {
			if fw.Status == "online" {
				online++
			}
		}
		fmt.Fprintf(out, "fleet — org %s · %d node(s), %d online\n\n", orgOf(env), len(resp.Workers), online)
		for _, fw := range resp.Workers {
			marker := ""
			if fw.ID == w.identity {
				marker = " *"
			}
			fmt.Fprintf(out, "%s %s%s\n", statusDot(fw.Status), fw.Hostname, marker)
			fmt.Fprintf(out, "    status    %s (%s)\n", fw.Status, dashIfEmpty(fw.Provider))
			fmt.Fprintf(out, "    cpu       %s\n", cpuSummary(fw.Arch, fw.CPUs, fw.CPUModel))
			fmt.Fprintf(out, "    memory    %s\n", humanBytes(fw.Memory))
			if len(fw.GPUs) == 0 {
				fmt.Fprintf(out, "    gpu       (none — CPU-only node)\n")
			}
			for i, g := range fw.GPUs {
				fmt.Fprintf(out, "    gpu[%d]    %s\n", i, describeGPUs([]gpuInfo{g}))
			}
			if fw.Engine != nil {
				fmt.Fprintf(out, "    engine    %s — %s\n", fw.Engine.URL, describeEngine(fw.Engine))
			}
			if fw.LastHeartbeat != "" {
				fmt.Fprintf(out, "    heartbeat %s\n", fw.LastHeartbeat)
			}
		}
	})
}

// statusDot renders a filled/hollow dot for a node's liveness.
func statusDot(status string) string {
	if status == "online" {
		return "●"
	}
	return "○"
}

// cpuSummary renders a node's CPU as "<cores> cores · <arch> · <model>", omitting
// any field the node did not report. "unknown" only when it reported nothing.
func cpuSummary(arch string, cpus int, model string) string {
	var parts []string
	if cpus > 0 {
		parts = append(parts, fmt.Sprintf("%d cores", cpus))
	}
	if arch != "" {
		parts = append(parts, arch)
	}
	if model != "" {
		parts = append(parts, model)
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " · ")
}

// describeCPU renders THIS node's CPU line for the link confirmation.
func describeCPU(w *worker) string {
	return cpuSummary(w.arch, runtime.NumCPU(), w.cpuModel) + " · " + humanBytes(w.memory)
}

// humanBytes renders a byte count as GiB/MiB, "unknown" for 0 (never faked).
func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "unknown"
	case n >= 1<<30:
		return fmt.Sprintf("%.0f GiB", float64(n)/float64(1<<30))
	default:
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(1<<20))
	}
}

func runDisconnect(cmd *cobra.Command, env *Env) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, _ := newWorker(env, "")
	out := cmd.OutOrStdout()
	path := fmt.Sprintf("/v1/tasks/namespaces/%s/activities/%s/%s/complete", fleetNS, w.identity, w.identity)
	code, err := w.call(ctx, http.MethodPost, path, map[string]any{
		"result":   map[string]any{"disconnected": true},
		"identity": w.identity,
	}, nil)
	if err != nil {
		// Idempotent: an already-terminal (409) or absent (404) fleet row is the
		// desired end state, so a repeat `hanzo unlink` no-ops instead of erroring.
		if code == http.StatusConflict || code == http.StatusNotFound {
			fmt.Fprintf(out, "%q already unlinked (no active fleet row)\n", w.hostname)
			return nil
		}
		return fmt.Errorf("deregister: %w", err)
	}
	fmt.Fprintf(out, "unlinked %q from the fleet\n", w.hostname)
	return nil
}

// ---------------------------------------------------------------------------
// HTTP + token.
// ---------------------------------------------------------------------------

// call issues one authed JSON request to the cloud API, decoding a 2xx body into
// out (when non-nil) and returning the status code. A 204 returns (204,nil) with
// out untouched — the empty-queue signal the claim loop reads.
func (w *worker) call(ctx context.Context, method, path string, body, out any) (int, error) {
	tok, err := w.env.ensureToken(ctx)
	if err != nil {
		return 0, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.baseURL+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, serverMessage(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// ensureToken returns a valid IAM bearer token, refreshing it when it is missing
// or within 90s of expiry and a refresh token is available. Refreshed credentials
// are persisted so a long-running connect loop survives token rotation. Returns a
// clear error when no usable credential exists.
func (e *Env) ensureToken(ctx context.Context) (string, error) {
	if t := os.Getenv("HANZO_TOKEN"); t != "" {
		return t, nil
	}
	if e.creds == nil || e.creds.AccessToken == "" {
		return "", fmt.Errorf("not logged in: run `hanzo login`")
	}
	needsRefresh := e.creds.Expiry > 0 && time.Now().Add(90*time.Second).Unix() >= e.creds.Expiry
	if needsRefresh && e.creds.RefreshToken != "" {
		iam := newIAMClient(e.IAMIssuer, e.ClientID)
		tr, err := iam.refreshGrant(ctx, e.creds.RefreshToken)
		if err == nil {
			nc := credsFromToken(tr)
			// Preserve any machine tokens + a refresh token IAM did not re-issue.
			nc.PlatformToken = e.creds.PlatformToken
			nc.BuildToken = e.creds.BuildToken
			if nc.RefreshToken == "" {
				nc.RefreshToken = e.creds.RefreshToken
			}
			*e.creds = *nc
			_ = SaveActive(e.creds) // refresh the active identity in the store + mirror
		}
		// On refresh failure fall through: the current token may still be valid
		// (clock skew) and the server is the authority.
	}
	return e.creds.AccessToken, nil
}

func orgOf(e *Env) string {
	if e.creds != nil && e.creds.Owner != "" {
		return e.creds.Owner
	}
	return "?"
}

func describeGPUs(gpus []gpuInfo) string {
	if len(gpus) == 0 {
		return "CPU-only"
	}
	parts := make([]string, len(gpus))
	for i, g := range gpus {
		if g.MemoryTotal != "" {
			parts[i] = g.Name + " (" + g.MemoryTotal + ")"
		} else {
			parts[i] = g.Name
		}
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Job handlers (pluggable).
// ---------------------------------------------------------------------------

// echoHandler returns its input verbatim — a trivial, GPU-free job for smoke
// tests and E2E, and the reference shape for a job handler.
func echoHandler(_ context.Context, input json.RawMessage) (any, error) {
	var v any
	if len(input) > 0 {
		_ = json.Unmarshal(input, &v)
	}
	return map[string]any{"echo": v}, nil
}

// workerToken is the KMS-sourced STUDIO_WORKER_TOKEN this box holds — the credential
// the local studio's --worker-mode gate checks. Empty on an unconfigured box (the
// render preflight refuses such a node before it ever claims a render lane).
func workerToken() string { return os.Getenv("STUDIO_WORKER_TOKEN") }

// authLocal stamps the worker token on a loopback studio request. The studio's
// --worker-mode gate guards the submit seam today, but sending it on ALL loopback
// calls (execute/history/view/upload/queue) makes the CLI robust if that scope widens
// — one way to talk to the local studio, always authenticated.
func authLocal(req *http.Request) {
	if t := workerToken(); t != "" {
		req.Header.Set("X-Worker-Token", t)
	}
}

// studioRenderHandler POSTs the job payload to the local ComfyUI /prompt endpoint
// and polls /history/{id} until the prompt completes. The GPU work happens entirely
// on the LOCAL studio server (127.0.0.1:8188); this handler drives it, then uploads
// the finished outputs to the org's cloud studio gallery (POST /upload/output),
// authorized by the user's IAM token — so the render lands in orgs/{org}/output
// (S3-mirrored to the gallery) with no S3/rclone credentials ever on this box.
// The payload is the ComfyUI prompt graph (input.prompt) — the same body
// studio.hanzo.ai submits; input.uploadUrl (optional) overrides the gallery target.
func (w *worker) studioRenderHandler(ctx context.Context, input json.RawMessage) (any, error) {
	var req struct {
		Prompt    json.RawMessage `json:"prompt"`
		UploadURL string          `json:"uploadUrl"`
		Org       string          `json:"org"`
		Inputs    []inputImage    `json:"inputs"`
	}
	if err := json.Unmarshal(input, &req); err != nil || len(req.Prompt) == 0 {
		return nil, fmt.Errorf("studio.render: input needs a `prompt` graph")
	}
	cl := &http.Client{Timeout: 60 * time.Second}
	// The claim-to-submit window: hold off the supervisor's recycle, and wait out
	// one if it is already mid-flight — a claimed job must never die on staging
	// because the engine happened to be restarting.
	staging.Add(1)
	defer staging.Add(-1)
	if err := waitEngine(ctx, cl); err != nil {
		return nil, fmt.Errorf("studio.render: %w", err)
	}
	// Materialize any uploaded inputs (they live in orgs/{org}/input on the cloud
	// pod, which this worker cannot read) into the LOCAL studio input dir via its
	// own /upload/image, so LoadImage resolves them before we render.
	if err := w.materializeInputs(ctx, cl, req.Inputs); err != nil {
		return nil, fmt.Errorf("studio.render: staging inputs: %w", err)
	}
	body, _ := json.Marshal(map[string]any{"prompt": req.Prompt})
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, localWorkerExecute, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	post.Header.Set("Content-Type", "application/json")
	authLocal(post) // gated worker-mode submit seam
	resp, err := cl.Do(post)
	if err != nil {
		return nil, fmt.Errorf("studio.render: POST /v1/worker/execute: %w (is the local studio server on %s?)", err, localComfyUI)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("studio.render: /v1/worker/execute HTTP %d: %s", resp.StatusCode, serverMessage(raw))
	}
	var pr struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil || pr.PromptID == "" {
		return nil, fmt.Errorf("studio.render: no prompt_id in /v1/worker/execute response")
	}
	// Poll history until the prompt shows up (completed).
	deadline := time.Now().Add(renderWindow)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, localComfyUI+"/history/"+pr.PromptID, nil)
		authLocal(hreq)
		hresp, err := cl.Do(hreq)
		if err != nil {
			continue
		}
		hraw, _ := io.ReadAll(io.LimitReader(hresp.Body, 8<<20))
		hresp.Body.Close()
		var hist map[string]json.RawMessage
		if err := json.Unmarshal(hraw, &hist); err != nil {
			continue
		}
		if entry, ok := hist[pr.PromptID]; ok {
			outputs := collectOutputs(entry)
			gallery, uerr := w.uploadOutputs(ctx, outputs, req.UploadURL, req.Org)
			if uerr != nil {
				return nil, fmt.Errorf("studio.render: prompt %s rendered but gallery upload failed: %w", pr.PromptID, uerr)
			}
			// The engine leaks ~58GB per render; recycling after each completed
			// render caps it at one render's worth. Boot (~20s) is noise next to
			// 8-70m renders. Never recycle on the timeout path — the engine may
			// still be sampling and the mirror rescues late finishes.
			requestStudioRecycle()
			return map[string]any{"promptId": pr.PromptID, "outputs": outputs, "gallery": gallery}, nil
		}
	}
	return nil, fmt.Errorf("studio.render: prompt %s did not complete within the deadline", pr.PromptID)
}

// uploadOutputs fetches each finished render from the LOCAL studio (GET /view) and
// POSTs it to the org studio's /upload/output with the user's IAM bearer. The remote
// studio writes to orgs/{org}/output for the ACTIVE org — persisted to the gallery.
// org is the job's active org (from the browser's org switcher); it is forwarded as
// the studio_active_org cookie so a worker whose token home-org differs from the
// active org still lands the output in the right gallery. Returns the stored gallery
// paths. base is resolved from (in order) the per-job uploadUrl, then w.studioUploadURL.
func (w *worker) uploadOutputs(ctx context.Context, outputs []string, uploadURL, org string) ([]string, error) {
	if len(outputs) == 0 {
		return nil, nil
	}
	tok, err := w.env.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(firstNonEmpty(uploadURL, w.studioUploadURL), "/")
	if base == "" {
		return nil, fmt.Errorf("no studio upload URL configured")
	}
	stored := make([]string, 0, len(outputs))
	for _, o := range outputs {
		sub, name := filepath.Split(o)
		sub = strings.Trim(sub, "/")
		data, err := w.fetchLocalOutput(ctx, name, sub)
		if err != nil {
			return stored, fmt.Errorf("fetch %s: %w", o, err)
		}
		path, err := w.postGalleryOutput(ctx, base, tok, org, name, sub, data)
		if err != nil {
			return stored, fmt.Errorf("upload %s: %w", o, err)
		}
		stored = append(stored, path)
	}
	return stored, nil
}

// fetchLocalOutput reads one finished output image from the local studio's /view.
func (w *worker) fetchLocalOutput(ctx context.Context, name, subfolder string) ([]byte, error) {
	q := url.Values{"filename": {name}, "type": {"output"}}
	if subfolder != "" {
		q.Set("subfolder", subfolder)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localComfyUI+"/view?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	authLocal(req)
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET /view HTTP %d: %s", resp.StatusCode, serverMessage(body))
	}
	return body, nil
}

// postGalleryOutput multipart-POSTs one output to <base>/upload/output with the
// user's IAM bearer and returns the stored "subfolder/name" gallery path. When org
// is set it rides as the studio_active_org cookie so the studio writes into that
// org's gallery even if the worker's token home-org differs.
func (w *worker) postGalleryOutput(ctx context.Context, base, tok, org, name, subfolder string, data []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", name)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	_ = mw.WriteField("type", "output")
	_ = mw.WriteField("subfolder", subfolder)
	_ = mw.WriteField("overwrite", "true")
	if err := mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/upload/output", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if org != "" {
		req.Header.Set("Cookie", "studio_active_org="+org)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("POST /upload/output HTTP %d: %s", resp.StatusCode, serverMessage(raw))
	}
	var out struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Name == "" {
		// Upload succeeded (2xx) but the body was unexpected; fall back to the sent name.
		return filepath.Join(subfolder, name), nil
	}
	return filepath.Join(out.Subfolder, out.Name), nil
}

// isImageFile reports whether name carries a render image extension the library accepts.
func isImageFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

// mirrorRenders scans dir (the local studio output tree) for image files new or
// changed since the last scan and POSTs each to base/v1/library/upload with the
// worker's bearer, tagged with this node's identity, so EVERY render lands in the
// org's studio library — including ones produced OUTSIDE the job path. seen (rel
// path -> size) skips unchanged files; the endpoint dedupes, so a re-scan after a
// restart is cheap and harmless. One log line per newly stored file; upload
// failures are summarized once per scan and retried next tick (no 5xx log spam).
func (w *worker) mirrorRenders(ctx context.Context, out io.Writer, dir, base string, seen map[string]int64) {
	tok, err := w.env.ensureToken(ctx)
	if err != nil {
		return
	}
	base = strings.TrimRight(base, "/")
	failed := 0
	var firstErr error
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, werr error) error {
		if werr != nil || info == nil || info.IsDir() || !isImageFile(p) {
			return nil
		}
		// Hidden files and AppleDouble forks (`._*`, `.DS_Store`) ride along with
		// mac scp and are not renders — `._foo.png` passes the extension check
		// but is a 4KB resource fork that poisons the library.
		if strings.HasPrefix(filepath.Base(p), ".") {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] == info.Size() {
			return nil
		}
		data, derr := os.ReadFile(p)
		if derr != nil || len(data) == 0 {
			return nil
		}
		sub, name := "", rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			sub, name = rel[:i], rel[i+1:]
		}
		existed, perr := w.postLibraryUpload(ctx, base, tok, sub, name, data)
		if perr != nil {
			failed++
			if firstErr == nil {
				firstErr = perr
			}
			return nil
		}
		seen[rel] = info.Size()
		if !existed {
			fmt.Fprintf(out, "mirrored %s (%d bytes) -> %s\n", rel, len(data), base)
		}
		return nil
	})
	if failed > 0 {
		fmt.Fprintf(out, "mirror: %d file(s) failed to upload, will retry: %v\n", failed, firstErr)
	}
}

// postLibraryUpload multipart-POSTs one image to base/v1/library/upload with the
// worker's IAM bearer, landing it in the org's library (orgs/{org}/output). The
// file's subfolder rides as ?subpath and this node's identity as ?node so the
// render is filterable by its source in Queue & History. Returns whether the
// endpoint already had a byte-identical copy (dedup).
func (w *worker) postLibraryUpload(ctx context.Context, base, tok, sub, name string, data []byte) (bool, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("image", name)
	if err != nil {
		return false, err
	}
	if _, err := part.Write(data); err != nil {
		return false, err
	}
	if err := mw.Close(); err != nil {
		return false, err
	}
	q := url.Values{"node": {w.identity}}
	if sub != "" {
		q.Set("subpath", sub)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/library/upload?"+q.Encode(), &buf)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("POST /v1/library/upload HTTP %d: %s", resp.StatusCode, serverMessage(raw))
	}
	var out struct {
		Existed bool `json:"existed"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Existed, nil
}

// inputImage is one uploaded input shipped with the job: a base64 blob plus the
// input-dir-relative location it must occupy on this worker so LoadImage finds it.
type inputImage struct {
	Name      string `json:"name"`
	Subfolder string `json:"subfolder"`
	Data      string `json:"data"` // base64
}

// materializeInputs writes each shipped input into the LOCAL studio's input dir by
// POSTing it to the studio's own /upload/image (loopback, auth-bypassed). This is
// how an uploaded photo — which lives in orgs/{org}/input on the dispatching pod,
// unreadable by this worker — becomes resolvable by LoadImage before the render.
func (w *worker) materializeInputs(ctx context.Context, cl *http.Client, inputs []inputImage) error {
	for _, in := range inputs {
		if in.Name == "" || in.Data == "" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			return fmt.Errorf("decode input %q: %w", in.Name, err)
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("image", in.Name)
		if err != nil {
			return err
		}
		if _, err := part.Write(data); err != nil {
			return err
		}
		_ = mw.WriteField("type", "input")
		_ = mw.WriteField("subfolder", in.Subfolder)
		_ = mw.WriteField("overwrite", "true")
		if err := mw.Close(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, localComfyUI+"/upload/image", &buf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		authLocal(req)
		resp, err := cl.Do(req)
		if err != nil {
			return fmt.Errorf("stage input %q: %w", in.Name, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("stage input %q: HTTP %d: %s", in.Name, resp.StatusCode, serverMessage(raw))
		}
	}
	return nil
}

// waitEngine blocks until the local engine answers its /queue — up to 90s, which
// outlasts any supervisor recycle (engine restart is seconds, model reload longer).
func waitEngine(ctx context.Context, cl *http.Client) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, localComfyUI+"/queue", nil)
		if err != nil {
			return err
		}
		authLocal(req)
		resp, err := cl.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("engine not up: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// collectOutputs pulls the output file names out of a ComfyUI history entry. Savers
// publish under different keys — SaveImage/SaveVideo under "images", SaveGLB under
// "3d" — so every saver's outputs are gathered, not just images (a 3D mesh would
// otherwise never travel back to the library).
func collectOutputs(entry json.RawMessage) []string {
	type namedFile struct {
		Filename  string `json:"filename"`
		Subfolder string `json:"subfolder"`
	}
	var e struct {
		Outputs map[string]struct {
			Images []namedFile `json:"images"`
			ThreeD []namedFile `json:"3d"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		return nil
	}
	var files []string
	for _, node := range e.Outputs {
		for _, f := range append(append([]namedFile{}, node.Images...), node.ThreeD...) {
			files = append(files, filepath.Join(f.Subfolder, f.Filename))
		}
	}
	return files
}

// ---------------------------------------------------------------------------
// engine.serve — advertise a local hanzo-engine model server.
// ---------------------------------------------------------------------------

// probeEngine GETs {base}/v1/models and returns the served model ids. hanzo-engine
// answers an OpenAI-shaped {"object":"list","data":[{"id":...}]} once it is serving;
// a transport error or non-2xx means it is not up yet.
func probeEngine(ctx context.Context, base string) ([]string, error) {
	url := strings.TrimRight(base, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	var ml struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ml); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	ids := make([]string, 0, len(ml.Data))
	for _, m := range ml.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// refreshEngine re-probes the local engine and updates w.engine, returning true when
// the advertisement materially changed (reachability or model set) so the caller can
// rewrite the presence record.
func (w *worker) refreshEngine(ctx context.Context) bool {
	prev := w.engine
	adv := &engineAdvertisement{URL: w.engineAdvURL, APIs: []string{"openai", "anthropic"}}
	if models, err := probeEngine(ctx, w.engineURL); err != nil {
		adv.Status = "unreachable"
	} else {
		adv.Status = "ready"
		adv.Models = models
	}
	w.engine = adv
	return !sameEngine(prev, adv)
}

func sameEngine(a, b *engineAdvertisement) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.URL != b.URL || a.Status != b.Status || len(a.Models) != len(b.Models) {
		return false
	}
	for i := range a.Models {
		if a.Models[i] != b.Models[i] {
			return false
		}
	}
	return true
}

func describeEngine(adv *engineAdvertisement) string {
	if adv == nil {
		return "no engine"
	}
	if adv.Status != "ready" {
		return adv.Status
	}
	n := len(adv.Models)
	if n == 1 {
		return "ready · 1 model"
	}
	return fmt.Sprintf("ready · %d models", n)
}

// providerBody is the POST /v1/add-provider payload registering this node's engine
// as an org model provider. hanzo-engine is OpenAI-compatible, so Type=Local: the
// gateway speaks the OpenAI wire format to it and auto-appends /v1 to providerUrl.
func (w *worker) providerBody() map[string]any {
	model := "default"
	if w.engine != nil && len(w.engine.Models) > 0 {
		model = w.engine.Models[0]
	}
	url := ""
	if w.engine != nil {
		url = w.engine.URL
	}
	return map[string]any{
		"name":               "gpu-" + w.identity,
		"category":           "Model",
		"type":               "Local",
		"providerUrl":        url,
		"subType":            model,
		"compatibleProvider": model,
	}
}

// printEngineHint prints the ready-to-run registration so an operator can route
// api.hanzo.ai model calls to this GPU (or pass --register-provider to do it here).
func (w *worker) printEngineHint(out io.Writer) {
	adv := w.engine
	if adv == nil {
		return
	}
	fmt.Fprintf(out, "serving hanzo-engine (OpenAI + Anthropic) at %s — %s\n", adv.URL, describeEngine(adv))
	body, _ := json.Marshal(w.providerBody())
	fmt.Fprintln(out, "  route api.hanzo.ai model calls to this GPU by registering it as an org provider:")
	fmt.Fprintf(out, "    curl -sS %s/v1/add-provider -H \"Authorization: Bearer $HANZO_TOKEN\" \\\n", w.baseURL)
	fmt.Fprintf(out, "      -H 'Content-Type: application/json' -d '%s'\n", body)
	fmt.Fprintln(out, "  (or pass --register-provider. The endpoint must be reachable from api.hanzo.ai —")
	fmt.Fprintln(out, "   a cloud GPU is in-cluster; a BYO node needs a public URL/tunnel. add-provider needs a platform-admin token today.)")
}

// registerProvider POSTs /v1/add-provider so the gateway routes model calls to this
// node's engine. Requires the engine to be reachable and (today) a platform-admin
// token; both failures are reported clearly rather than swallowed.
func (w *worker) registerProvider(ctx context.Context, adv *engineAdvertisement) error {
	if adv == nil || adv.Status != "ready" {
		return fmt.Errorf("engine not ready at %s — start hanzo-engine, then retry", w.engineURL)
	}
	code, err := w.call(ctx, http.MethodPost, "/v1/add-provider", w.providerBody(), nil)
	if err != nil {
		if code == http.StatusForbidden {
			return fmt.Errorf("add-provider is gated to a platform-admin token today; register from the console or with an admin token: %w", err)
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Daemon install (systemd --user).
// ---------------------------------------------------------------------------

func installDaemon(cmd *cobra.Command, opts connectOpts) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	// The unit is persistent COMPUTE membership; hanzod has its own supervision, so
	// the worker unit does not also babysit the fabric (--no-fabric).
	args := "link --no-fabric"
	if opts.jobsNS != "" && opts.jobsNS != defaultJobsNS {
		args += " --jobs-namespace " + opts.jobsNS
	}
	if opts.serveEngine {
		args += " --serve-engine"
		if opts.engineURL != "" && opts.engineURL != defaultEngineURL {
			args += " --engine-url " + opts.engineURL
		}
		if opts.engineEndpoint != "" {
			args += " --engine-endpoint " + opts.engineEndpoint
		}
		if opts.registerProvider {
			args += " --register-provider"
		}
	}
	if opts.studioDir != "" {
		args += " --studio-dir " + opts.studioDir
	}
	if !opts.mirror {
		args += " --mirror=false"
	}
	unit := fmt.Sprintf(`[Unit]
Description=Hanzo node (compute worker)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s %s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, exe, args)
	unitPath := filepath.Join(unitDir, "hanzo-node.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "wrote %s\n", unitPath)
	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Fprintln(out, "systemctl not found; enable the unit manually once available.")
		return nil
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "--now", "hanzo-node.service").Run(); err != nil {
		fmt.Fprintf(out, "unit written; enable it with: systemctl --user enable --now hanzo-node.service\n")
		return nil
	}
	fmt.Fprintln(out, "enabled and started hanzo-node.service (Restart=always). Check: systemctl --user status hanzo-node")
	fmt.Fprintln(out, "note: the daemon reuses ~/.hanzo/credentials.json — keep `hanzo login` current, or set HANZO_TOKEN in the unit.")
	return nil
}
