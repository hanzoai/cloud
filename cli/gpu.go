package cli

// gpu.go — the compute-worker machinery behind `hanzo link` (command wiring lives
// in link.go). It authenticates against IAM (reusing the `hanzo login` credential
// store), registers the machine + its CPU/memory/GPU inventory as a heartbeating
// presence record in the org's `fleet` tasks namespace, and runs an OUTBOUND-only
// worker loop that CLAIMS jobs from the org's `gpu-jobs` namespace — the NAT-safe
// primitive (the worker dials out; nothing dials in). The registered node then shows
// up on console.hanzo.ai's Machines + GPUs pages (provider="byo") via cloud's
// /v1/compute/fleet union.
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
// the POST /v1/ai/providers call that routes api.hanzo.ai model traffic to this GPU. One
// fleet, two job types: engine.serve (model serving) alongside studio.render.

import (
	"bufio"
	"bytes"
	"cmp"
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
	"unicode"

	"github.com/spf13/cobra"
)

const (
	// engineAPI is where the durable engine answers, spelled once because every
	// path below is built from it. It is /v1/workflow (manifest/apps.go). The old
	// address, /v1/tasks, is now the plural of the BOARD's /v1/task, and the router
	// rewrites it there before routing (manifest.Normalize) — so a worker asking it
	// reaches an app that serves no namespaces and is answered 404 for every one.
	engineAPI     = "/v1/workflow"
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
	heartbeatEvery        = 30 * time.Second
	claimPoll             = 2 * time.Second
	claimLeaseSecs        = 30
	claimHeartbeatEvery   = 10 * time.Second
	defaultMaxGPUUtil     = 0.80
	sampleCacheTTL        = 10 * time.Second
	releaseRetryDelaySecs = 10
	// maxFetchedInputBytes guards fetchInput's read. Generous next to any real
	// source (a 4K PNG is ~20MB) and finite, so a wrong URL cannot exhaust memory.
	maxFetchedInputBytes = 512 << 20
	// registerAttempts/registerBackoff let the presence write ride out a control
	// plane that is rolling. ~30s of cover, which is longer than a pod replacement
	// and far shorter than a real outage.
	registerAttempts = 6
	registerBackoff  = 5 * time.Second
	// renderWindow matches the dispatch cap (studio gpu_dispatch sets
	// startToCloseTimeout 14400s). The old 10m local poll undercut it and
	// marked live renders failed while they kept sampling (observed 8-70m).
	renderWindow = 4 * time.Hour
	// localComfyUI is the studio render backend the studio.render handler drives.
	localComfyUI = "http://127.0.0.1:8188"
	// localWorkerExecute is the GATED submit client on the local studio (worker-mode):
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
	studioCap       = "studio.render"
	engineCap       = "engine.serve"
	fnCap           = "fn.run"         // ephemeral script execution (uv-run) on this node
	researchCap     = "research.bench" // hardware/model benchmark execution on this node
	researchTickCap = "research.tick"  // scheduled/autonomous research tick on this node
	hanzodRunCap    = "hanzod.run"     // linked desktop / hanzod workload execution on this node
)

// The node-level command surface — `hanzo link | unlink` — is wired in link.go.
// It composes the worker machinery below (runConnect / runDisconnect) with the
// fabric verbs (`hanzo node up|stop`). Reading the fleet back is `hanzo status`,
// which this binary does not serve; the router hands it to the fabric CLI.

// ---------------------------------------------------------------------------
// worker — the connect loop's state.
// ---------------------------------------------------------------------------

type worker struct {
	env         *Env
	http        *http.Client
	baseURL     string
	identity    string // sanitized node id == fleet activityId == runId
	hostname    string
	rawHostname string // OS hostname prior to --name / HANZO_NODE_ID override (for -wsl alias queues)
	jobsNS      string
	gpus        []gpuInfo
	arch        string // CPU arch (`uname -m`), detected once at newWorker
	cpuModel    string // CPU model name (/proc/cpuinfo | sysctl), detected once at newWorker
	memory      int64  // total system RAM in bytes, detected once at newWorker
	handlers    map[string]jobHandler

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

	// lanes/models: what the local studio says it can render (GET /v1/lanes),
	// empty while it cannot render at all.
	lanes  []string
	models []laneModel

	// engine.serve — advertise a hanzo-engine model server running on this node.
	serveEngine  bool
	engineURL    string               // local URL probed for /v1/models
	engineAdvURL string               // endpoint advertised for gateway routing
	engine       *engineAdvertisement // latest probe result (nil until probed)

	// Busy-aware scheduling + backend matching across linked nodes (`hanzo link` / `hanzod`).
	busyLockPaths  []string
	backends       []string
	sampleMu       sync.Mutex
	lastSample     sampleReport
	lastSampleAt   time.Time
	sampleFn       func(context.Context) sampleReport
	registeredOnce bool
	advBusy        bool
	advBusyReason  string

	// Event-driven SSE wakeup (`GET /v1/workflow/events`) + 0ms post-job slot wakeup.
	wakeCh     chan<- struct{}
	sseBackoff time.Duration
}

// engineAdvertisement describes a hanzo-engine model server on this node.
// hanzo-engine serves the OpenAI AND Anthropic HTTP APIs from ONE axum port
// (0.0.0.0:1234), so the gateway can route model calls here on the standard chat-completions
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
// apps/visor/fleet.go fleetRegistration decodes. Capabilities + Engine are
// additive (omitempty): an older cloud that does not read them still renders the
// GPU; a newer one advertises the engine endpoint on GET /v1/compute/fleet/workers.
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
	Rocm         string               `json:"rocm,omitempty"`   // host ROCm version (AMD only)
	Hip          string               `json:"hip,omitempty"`    // host HIP version (AMD only)
	Cuda         string               `json:"cuda,omitempty"`   // host CUDA toolkit version (NVIDIA only)
	Driver       string               `json:"driver,omitempty"` // host NVIDIA driver version
	GPUs         []gpuInfo            `json:"gpus"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Engine       *engineAdvertisement `json:"engine,omitempty"`
	Policy       *SharePolicy         `json:"policy,omitempty"`
	// Lanes are the Studio render lanes the local engine can run right now
	// (image.zen, edit.qwen, video.wan, mesh, ...), and Models the weights behind
	// them with their licences: the engine's GET /v1/lanes, read on every beat.
	// A node advertises a lane only when every file it loads is present, so the
	// home offers a GPU card only where a node can render it.
	Lanes      []string    `json:"lanes,omitempty"`
	Models     []laneModel `json:"models,omitempty"`
	Busy       bool        `json:"busy,omitempty"`
	BusyReason string      `json:"busyReason,omitempty"`
}

// laneModel is one weight set behind a lane, as the engine names it: its id, the
// lane it serves first, and its SPDX licence.
type laneModel struct {
	ID      string `json:"id"`
	Lane    string `json:"lane"`
	Licence string `json:"licence"`
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
	return newNamedWorker(env, jobsNS, os.Getenv("HANZO_NODE_ID"))
}

func newNamedWorker(env *Env, jobsNS, nodeID string) (*worker, error) {
	rawHost, _ := os.Hostname()
	if rawHost == "" {
		rawHost = "worker"
	}
	host := rawHost
	id := sanitizeID(rawHost)
	if trimmed := strings.TrimSpace(nodeID); trimmed != "" {
		id = sanitizeID(trimmed)
		host = id
	}
	w := &worker{
		env:             env,
		http:            &http.Client{Timeout: 60 * time.Second},
		baseURL:         env.CloudURL,
		identity:        id,
		hostname:        host,
		rawHostname:     rawHost,
		jobsNS:          cmp.Or(jobsNS, defaultJobsNS),
		gpus:            detectGPUs(),
		arch:            detectArch(),
		cpuModel:        detectCPUModel(),
		memory:          detectMemTotal(),
		studioUploadURL: cmp.Or(os.Getenv("HANZO_STUDIO_UPLOAD_URL"), defaultStudioUploadURL),
		busyLockPaths:   defaultBusyLockPaths(),
	}
	policy, err := loadSharePolicy()
	if err != nil {
		return nil, err
	}
	w.policy = policy
	w.handlers = map[string]jobHandler{
		"echo":          echoHandler,
		"studio.render": w.studioRenderHandler,
		researchCap:     w.researchBenchHandler,
		researchTickCap: w.researchBenchHandler,
		hanzodRunCap:    w.researchBenchHandler,
	}
	if _, err := resolveExecInPath("uv", researchBenchPath()); err == nil {
		w.handlers[fnCap] = fnRunHandler // functions-runner: needs uv, nothing else
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

// detectNvidiaGPUs reports NVIDIA accelerators via nvidia-smi: name, memory,
// and the sm arch (compute capability). A unified SoC (GB10 Grace-Blackwell)
// reports memory.total as "[N/A]" — there is no dedicated VRAM counter — so it
// reports the MACHINE's RAM snapped to hardware capacity, unified=true, the
// same convention the AMD APU and Apple paths use.
func detectNvidiaGPUs() []gpuInfo {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total,compute_cap", "--format=csv,noheader").Output()
	if err != nil {
		return nil
	}
	return parseNvidiaSmiCSV(out, detectMemTotal()/(1<<20))
}

// parseNvidiaSmiCSV parses `nvidia-smi --query-gpu=name,memory.total,compute_cap`
// output ("NVIDIA GB10, [N/A], 12.1"). Pure — hostMiB is injected for the
// unified-SoC memory figure.
func parseNvidiaSmiCSV(out []byte, hostMiB int64) []gpuInfo {
	var gpus []gpuInfo
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ",")
		if len(fields) < 2 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		g := gpuInfo{Name: strings.TrimSpace(fields[0]), MemoryTotal: strings.TrimSpace(fields[1])}
		var memMiB int64
		if _, err := fmt.Sscanf(g.MemoryTotal, "%d MiB", &memMiB); err != nil || memMiB <= 0 {
			// No dedicated VRAM counter — a unified SoC. The machine's RAM is
			// the GPU's RAM.
			if hostMiB > 0 {
				g.MemoryTotal = fmt.Sprintf("%d MiB", snapUnified(hostMiB))
				g.Unified = true
			} else {
				g.MemoryTotal = ""
			}
		}
		if len(fields) >= 3 {
			if cap := strings.TrimSpace(fields[2]); cap != "" && cap != "[N/A]" {
				g.Arch = "sm_" + strings.ReplaceAll(cap, ".", "")
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// nvidiaSoft is the host CUDA/driver inventory, detected once — the NVIDIA
// mirror of amdSoftware. Empty on non-NVIDIA hosts.
type nvidiaSoft struct{ cuda, driver string }

var nvidiaSoftware = sync.OnceValue(func() nvidiaSoft {
	var v nvidiaSoft
	if out, err := exec.Command("nvidia-smi", "--query-gpu=driver_version", "--format=csv,noheader").Output(); err == nil {
		v.driver = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}
	if b, err := os.ReadFile("/usr/local/cuda/version.json"); err == nil {
		var m struct {
			Cuda struct {
				Version string `json:"version"`
			} `json:"cuda"`
		}
		if json.Unmarshal(b, &m) == nil {
			v.cuda = m.Cuda.Version
		}
	}
	return v
})

// detectAmdGPUs reports AMD accelerators — discrete Radeon cards and gfx APUs alike
// (e.g. evo's gfx1151 Radeon 8060S on the RYZEN AI MAX+ 395). Resolution order:
// rocm-smi (marketing name + gfx target), then the kfd topology under /sys (gfx
// target from gfx_target_version, GPU nodes only), then a vulkaninfo summary. VRAM
// is filled best-effort from the amdgpu sysfs mem_info_vram_total, positionally.
func detectAmdGPUs() []gpuInfo {
	if out, err := exec.Command("rocm-smi", "--showproductname", "--csv").Output(); err == nil {
		if gpus := parseRocmSmiCSV(out); len(gpus) > 0 {
			return apuProcessorNames(fillAmdVRAM(gpus, amdVRAMTotals(sysfsDRM)), detectCPUModel())
		}
	}
	if gpus := parseKfdTopology(sysfsKfdNodes); len(gpus) > 0 {
		return apuProcessorNames(fillAmdVRAM(gpus, amdVRAMTotals(sysfsDRM)), detectCPUModel())
	}
	if out, err := exec.Command("vulkaninfo", "--summary").Output(); err == nil {
		if gpus := parseVulkaninfoSummary(out); len(gpus) > 0 {
			return gpus
		}
	}
	return detectAmdAPUFallback(detectCPUModel(), detectMemTotal())
}

// amdAPUArch maps a normalized AMD Ryzen APU marketing string to its LLVM gfx
// target when sysfs/kfd/rocm-smi are absent (such as inside a WSL2 /dev/dxg guest).
func amdAPUArch(apu string) string {
	l := strings.ToLower(apu)
	switch {
	case strings.Contains(l, "8060s") || strings.Contains(l, "8050s") || strings.Contains(l, "395") || strings.Contains(l, "390") || strings.Contains(l, "385"):
		return "gfx1151"
	case strings.Contains(l, "890m") || strings.Contains(l, "880m"):
		return "gfx1150"
	case strings.Contains(l, "780m") || strings.Contains(l, "760m") || strings.Contains(l, "740m"):
		return "gfx1103"
	}
	return ""
}

// detectAmdAPUFallback identifies unified-memory AMD Ryzen APUs (e.g. Strix Halo
// Radeon 8060S on WSL2 where /dev/dxg is exposed instead of /sys/class/kfd) from
// /proc/cpuinfo + system RAM.
func detectAmdAPUFallback(cpuModel string, memBytes int64) []gpuInfo {
	apu := amdAPUName(cpuModel)
	if apu == "" || !strings.Contains(apu, "Radeon") {
		return nil
	}
	var memTotal string
	if miB := memBytes / (1 << 20); miB > 0 {
		memTotal = fmt.Sprintf("%d MiB", snapUnified(miB))
	}
	return []gpuInfo{{
		Name:        apu,
		Arch:        amdAPUArch(apu),
		MemoryTotal: memTotal,
		Unified:     true,
	}}
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

// apuProcessorNames renders a unified-memory APU as the PROCESSOR it is — the
// cpuinfo marketing name ("AMD Ryzen AI Max+ 395 w/ Radeon 8060S") beats the
// iGPU series, because that is the product the owner bought. Discrete cards and
// non-Ryzen hosts keep their existing names.
func apuProcessorNames(gpus []gpuInfo, cpuModel string) []gpuInfo {
	name := amdAPUName(cpuModel)
	if name == "" {
		return gpus
	}
	for i, g := range gpus {
		if !g.Unified {
			continue
		}
		if g.Arch != "" {
			gpus[i].Name = name + " (" + g.Arch + ")"
		} else {
			gpus[i].Name = name
		}
	}
	return gpus
}

// amdAPUName normalizes an AMD APU cpuinfo model into its marketing name:
// "AMD RYZEN AI MAX+ 395 w/ Radeon 8060S" -> "AMD Ryzen AI Max+ 395 w/ Radeon
// 8060S". BIOS strings shout; the board should not. Non-Ryzen models return "".
func amdAPUName(cpuModel string) string {
	m := strings.Join(strings.Fields(cpuModel), " ")
	if !strings.Contains(strings.ToLower(m), "ryzen") {
		return ""
	}
	words := strings.Split(m, " ")
	for i, w := range words {
		if w == "AMD" || w == "AI" || w != strings.ToUpper(w) {
			continue // brand/initialism stays; mixed-case is already right
		}
		if strings.ContainsFunc(w, unicode.IsDigit) {
			continue // model numbers ("8060S", "395") keep their casing
		}
		if r := []rune(w); len(r) > 1 && strings.ContainsFunc(w, unicode.IsLetter) {
			words[i] = string(r[0]) + strings.ToLower(string(r[1:]))
		}
	}
	return strings.Join(words, " ")
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
// discrete GPU; for an APU whose "VRAM" is a token carve-out (Strix Halo: 1 GiB
// VRAM beside a ~118 GiB GTT pool) the figure is the MACHINE's unified RAM —
// the same convention Apple silicon reports (an M4 Max says 128 GiB, not the
// wired-down remainder) — snapped to hardware capacity. Unified when GTT wins.
func pickAmdMem(vramMiB, gttMiB, hostMiB int64) (miB int64, unified bool) {
	if gttMiB > vramMiB*4 {
		m := max(hostMiB, gttMiB)
		return snapUnified(m), true
	}
	return vramMiB, false
}

// snapUnified rounds a kernel-visible unified-memory figure up to the hardware
// DIMM capacity (the next 16 GiB multiple) when the gap is a plausible firmware
// reservation (≤ 6 GiB): a 128 GiB Strix Halo shows 124.4 GiB to Linux because
// BIOS + the VRAM carve-out are invisible to the OS. A larger gap (say a 16 GiB
// carve-out) is real capacity the pool lost — reported as-is, never invented.
func snapUnified(miB int64) int64 {
	const step = 16 << 10 // 16 GiB in MiB
	next := ((miB + step - 1) / step) * step
	if next-miB <= 8<<10 {
		return next
	}
	return miB
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
	hostMiB := detectMemTotal() / (1 << 20)
	for i := range gpus {
		miB, unified := pickAmdMem(mems[i].vramMiB, mems[i].gttMiB, hostMiB)
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
	w.sampleMu.Lock()
	w.lastSample = rep
	w.lastSampleAt = time.Now()
	w.sampleMu.Unlock()
	return rep
}

func defaultBusyLockPaths() []string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/home/z"
	}
	return []string{
		filepath.Join(os.TempDir(), "hanzo-research-gpu.lock"),
		filepath.Join(home, ".hanzo", "research", "controller", "gpu.lock"),
		filepath.Join(home, ".hanzo", "research", ".gpu.lock"),
	}
}

func maxGPUUtilThreshold() float64 {
	raw := strings.TrimSpace(os.Getenv("HANZO_LINK_MAX_GPU_UTIL"))
	if raw == "" {
		return defaultMaxGPUUtil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return defaultMaxGPUUtil
	}
	if v > 1.0 && v <= 100.0 {
		v = v / 100.0
	}
	if v > 0 && v <= 1.0 {
		return v
	}
	return defaultMaxGPUUtil
}

func (w *worker) cachedOrSampledGPUs(ctx context.Context) sampleReport {
	if w.sampleFn != nil {
		rep := w.sampleFn(ctx)
		w.sampleMu.Lock()
		w.lastSample = rep
		w.lastSampleAt = time.Now()
		w.sampleMu.Unlock()
		return rep
	}
	w.sampleMu.Lock()
	fresh := !w.lastSampleAt.IsZero() && time.Since(w.lastSampleAt) <= sampleCacheTTL
	cached := w.lastSample
	w.sampleMu.Unlock()
	if fresh {
		return cached
	}
	if len(w.gpus) == 0 {
		return sampleReport{}
	}
	return w.sampleGPUs(ctx)
}

// isNodeBusy reports whether this linked machine is currently occupied by an
// active local GPU flock or high GPU utilization, returning (busy, reason).
func (w *worker) isNodeBusy(ctx context.Context) (bool, string) {
	if len(w.busyLockPaths) > 0 && isAnyGPULockHeld(w.busyLockPaths...) {
		return true, "local_gpu_lock_held"
	}
	if len(w.gpus) > 0 || w.sampleFn != nil {
		rep := w.cachedOrSampledGPUs(ctx)
		if rep.GPUUtil > maxGPUUtilThreshold() {
			return true, "gpu_utilization_high"
		}
	}
	return false, ""
}

// nodeBackends enumerates the execution backends this machine can satisfy for
// `input.required_backend` / `input.requiredBackend` matching.
func (w *worker) nodeBackends() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add("cpu")
	add(runtime.GOOS)
	add(w.arch)
	for _, b := range w.backends {
		add(b)
	}
	for _, b := range strings.Split(os.Getenv("HANZO_LINK_BACKENDS"), ",") {
		add(b)
	}
	for _, g := range w.gpus {
		ln := strings.ToLower(g.Name)
		la := strings.ToLower(g.Arch)
		switch {
		case strings.Contains(ln, "apple") || strings.Contains(ln, "metal"):
			add("metal")
			add("mps")
			add("mlx")
			add("apple")
		case strings.Contains(ln, "amd") || strings.Contains(ln, "radeon") || strings.Contains(ln, "instinct") || strings.HasPrefix(la, "gfx"):
			add("rocm")
			add("hip")
			add("amd")
			add("vulkan")
		default:
			add("cuda")
			add("nvidia")
			add("vulkan")
		}
	}
	if w.serveEngine && w.engine != nil && w.engine.Status == "ready" {
		add("engine")
		add("hanzo-engine")
	}
	return out
}

func (w *worker) supportsBackend(required string) bool {
	req := strings.ToLower(strings.TrimSpace(required))
	if req == "" || req == "any" {
		return true
	}
	return slices.Contains(w.nodeBackends(), req)
}

// reportSample posts this machine's live utilization to the org's fleet series
// (POST /v1/compute/fleet/samples) so the console board shows THIS GPU's load beside its
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
		_, _ = w.call(ctx, http.MethodPost, "/v1/compute/fleet/samples", map[string]any{
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
	nodeID           string // explicit --id / --name / HANZO_NODE_ID override
	jobsNS           string
	serveEngine      bool
	engineURL        string // local URL to probe hanzo-engine
	engineEndpoint   string // public URL to advertise (defaults to engineURL)
	registerProvider bool   // auto POST /v1/ai/providers for the engine
	studioDir        string // local Studio checkout to launch + supervise on :8188
	studioURL        string // studio base the render mirror uploads finished images to
	mirror           bool   // sweep local renders into the org studio library (default on)
}

func signalWake(wakeCh chan<- struct{}) {
	if wakeCh == nil {
		return
	}
	select {
	case wakeCh <- struct{}{}:
	default:
	}
}

func shouldWakeOnSSE(eventHeader, dataLine, jobsNS string) bool {
	dataLine = strings.TrimSpace(dataLine)
	if dataLine == "" && eventHeader == "" {
		return false
	}
	var ev struct {
		Kind      string `json:"kind"`
		Type      string `json:"type"`
		Event     string `json:"event"`
		Namespace string `json:"namespace"`
	}
	if dataLine != "" && json.Unmarshal([]byte(dataLine), &ev) == nil {
		ns := strings.TrimSpace(ev.Namespace)
		if ns != "" && jobsNS != "" && ns != jobsNS {
			return false
		}
		kind := strings.ToLower(strings.TrimSpace(cmp.Or(ev.Kind, ev.Type, ev.Event, eventHeader)))
		switch kind {
		case "activity.scheduled", "activity.deferred", "activity.timedout", "activity.released", "activity.requeued", "activity.created", "job.queued":
			return true
		}
		return false
	}
	lower := strings.ToLower(eventHeader + " " + dataLine)
	for _, kw := range []string{"activity.scheduled", "activity.deferred", "activity.timedout", "activity.released"} {
		if strings.Contains(lower, kw) {
			if jobsNS == "" || strings.Contains(lower, strings.ToLower(jobsNS)) || !strings.Contains(lower, "namespace") {
				return true
			}
		}
	}
	return false
}

// watchEvents maintains a streaming SSE subscription to GET {baseURL}/v1/workflow/events,
// signaling wakeCh within milliseconds whenever an activity in w.jobsNS is scheduled,
// deferred, or timed out — eliminating the 2s claim-polling loop. Reconnects with
// exponential backoff on stream drop until ctx is canceled.
func (w *worker) watchEvents(ctx context.Context, wakeCh chan<- struct{}) {
	initBackoff := cmp.Or(w.sseBackoff, time.Second)
	maxBackoff := 30 * time.Second
	backoff := initBackoff

	var tr http.RoundTripper
	if w.http != nil {
		tr = w.http.Transport
	}
	streamClient := &http.Client{Transport: tr} // Timeout=0 so long-lived SSE streams stay open

	for {
		if ctx.Err() != nil {
			return
		}
		connected, err := w.streamEventsOnce(ctx, streamClient, wakeCh)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = initBackoff
		}
		_ = err
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (w *worker) streamEventsOnce(ctx context.Context, cl *http.Client, wakeCh chan<- struct{}) (bool, error) {
	var tok string
	var err error
	if w.env != nil {
		tok, err = w.env.ensureToken(ctx)
	} else {
		tok = os.Getenv("HANZO_TOKEN")
	}
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(w.baseURL, "/")+engineAPI+"/events", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)

	resp, err := cl.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return false, fmt.Errorf("SSE %s: HTTP %d", engineAPI+"/events", resp.StatusCode)
	}

	// Connected: sweep once immediately in case a job arrived during reconnect.
	signalWake(wakeCh)

	rdr := bufio.NewReader(resp.Body)
	var eventHeader string
	var dataLines []string
	flush := func() {
		if len(dataLines) > 0 || eventHeader != "" {
			if shouldWakeOnSSE(eventHeader, strings.Join(dataLines, "\n"), w.jobsNS) {
				signalWake(wakeCh)
			}
			eventHeader = ""
			dataLines = dataLines[:0]
		}
	}
	for {
		line, rerr := rdr.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				flush()
			case strings.HasPrefix(trimmed, ":"):
				// SSE comment / keepalive
			case strings.HasPrefix(trimmed, "event:"):
				eventHeader = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			case strings.HasPrefix(trimmed, "data:"):
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				dataLines = append(dataLines, payload)
				// Also evaluate single-line JSON frames eagerly in case server omits trailing blank line before flush.
				if shouldWakeOnSSE(eventHeader, payload, w.jobsNS) {
					signalWake(wakeCh)
					eventHeader = ""
					dataLines = dataLines[:0]
				}
			}
		}
		if rerr != nil {
			flush()
			return true, rerr
		}
	}
}

func runConnect(cmd *cobra.Command, env *Env, opts connectOpts) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, err := newNamedWorker(env, opts.jobsNS, cmp.Or(opts.nodeID, os.Getenv("HANZO_NODE_ID")))
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// engine.serve: probe the local hanzo-engine once so the first registration
	// carries its live model list + reachability.
	if opts.serveEngine {
		w.serveEngine = true
		w.engineURL = cmp.Or(opts.engineURL, defaultEngineURL)
		w.engineAdvURL = cmp.Or(opts.engineEndpoint, w.engineURL)
		w.refreshEngine(ctx)
	}

	// studio.render preflight: decide whether this node can SERVE renders BEFORE it
	// advertises studioCap or claims a render lane. Every node probes, including one
	// that launches its own studio below — at this point that studio has not been
	// started yet, so a node claiming work here would be claiming it cold.
	// The worker token is required either way — the gated execute client refuses without it.
	w.launchesStudio = opts.studioDir != ""
	w.refreshStudioReady(ctx)
	w.refreshLanes(ctx)

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
	fmt.Fprintf(out, "claiming %s jobs (SSE /v1/workflow/events + %s safety sweep); heartbeating every %s. Ctrl-C to stop (the node goes offline after ~90s; `hanzo unlink` removes it).\n", w.jobsNS, heartbeatEvery, heartbeatEvery)

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
	wakeCh := make(chan struct{}, 1)
	w.wakeCh = wakeCh
	go w.watchEvents(ctx, wakeCh)

	// Render mirror — independent of claims by design. It scans the local studio
	// output tree every heartbeatEvery and uploads every image to the org's library
	// (POST /v1/library/upload), so EVERY render lands in studio.hanzo.ai even when
	// it was produced outside the job path — a graph hand-run on this node, or a
	// render that finished after its activity was reaped (the stranded-late-render
	// class). Active only when a studio checkout is named (there is local output to
	// mirror); a nil channel case never fires when it is not.
	w.studioUploadURL = cmp.Or(opts.studioURL, w.studioUploadURL)
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
	// full interval, report an initial utilization sample, and trigger an initial claim check.
	_ = w.heartbeat(ctx)
	w.reportSample(ctx)
	signalWake(wakeCh)

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
			// Re-evaluate render readiness and the lanes behind it; if either moved,
			// re-advertise (studioCap on/off, the lane list) so the fleet never shows a
			// node accepting renders it can't serve.
			ready := w.refreshStudioReady(ctx)
			lanes := w.refreshLanes(ctx)
			if ready || lanes {
				if err := w.register(ctx); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "re-advertise capabilities: %v\n", err)
				}
			}
			if lanes {
				fmt.Fprintf(out, "lanes: %s\n", cmp.Or(strings.Join(w.lanes, ", "), "none"))
			}
			if ready {
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
			// Slow 30s safety sweep fallback alongside realtime SSE wakeup.
			signalWake(wakeCh)
		case <-wakeCh:
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
	// Ensuring the namespaces is BEST-EFFORT: they already exist in any live org, so a
	// transient API blip here must not kill the worker. It used to be fatal, and with
	// systemd Restart=always/RestartSec=5 one 503 turned into an infinite crash-loop
	// (observed at 141 restarts, machine offline the whole time). If a namespace truly
	// is missing, the presence write below fails and surfaces the real error.
	for _, ns := range []string{fleetNS, w.jobsNS} {
		if _, err := w.call(ctx, http.MethodPost, engineAPI+"/namespaces", map[string]any{
			"namespaceInfo": map[string]any{"name": ns},
		}, nil); err != nil {
			fmt.Fprintf(os.Stderr, "ensure namespace %q (continuing): %v\n", ns, err)
		}
	}
	// The presence write RIDES OUT a rolling control plane. It is the call whose
	// error ends the process, and systemd restarts us — which kills the studio this
	// worker supervises, and with it whatever render was sampling at the time. So a
	// cloud deploy destroyed renders on every BYO box: api.hanzo.ai answered
	// "503 no available server" for the few seconds its pod was replaced, three
	// restarts landed in that window, and a compose that had been running for
	// minutes was gone.
	//
	// A blip is not an outage. Retry with backoff and only then report; a genuinely
	// missing namespace or a real rejection still surfaces, just later.
	//
	// But only a blip. A REFUSAL is not one: 401/403 says this node's credential is
	// not accepted, and no amount of waiting changes that. Retrying it burned 30s per
	// boot inside a systemd restart loop — seen at restart counter 10 on a node whose
	// token had expired — and buried the one line that said what was wrong under five
	// that said "retrying". Fail fast, and say what to do about it.
	var err error
	for attempt := range registerAttempts {
		reg := w.buildRegistration()
		var code int
		if code, err = w.call(ctx, http.MethodPost, engineAPI+"/namespaces/"+fleetNS+"/activities", map[string]any{
			"activityId":       w.identity,
			"runId":            w.identity,
			"activityType":     map[string]any{"name": "fleet.worker"},
			"taskQueue":        fleetNS,
			"heartbeatTimeout": "120s",
			"input":            reg,
		}, nil); err == nil {
			w.registeredOnce = true
			w.advBusy = reg.Busy
			w.advBusyReason = reg.BusyReason
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			return fmt.Errorf("%w — this node's credential was refused; run `hanzo login` on it", err)
		}
		if attempt < registerAttempts-1 {
			fmt.Fprintf(os.Stderr, "register (attempt %d/%d, retrying in %s): %v\n",
				attempt+1, registerAttempts, registerBackoff, err)
			select {
			case <-ctx.Done():
				return err
			case <-time.After(registerBackoff):
			}
		}
	}
	return err
}

// buildRegistration is the pure presence-record payload: this machine's inventory,
// its advertised capabilities, and (when serving) its hanzo-engine endpoint.
func (w *worker) buildRegistration() registration {
	busy, busyReason := w.isNodeBusy(context.Background())
	if !busy {
		busyReason = ""
	}
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
		Cuda:         nvidiaSoftware().cuda,
		Driver:       nvidiaSoftware().driver,
		GPUs:         w.gpus,
		Capabilities: w.capabilities(),
		Engine:       w.engine,
		Policy:       w.policy,
		Lanes:        w.lanes,
		Models:       w.models,
		Busy:         busy,
		BusyReason:   busyReason,
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
	if _, ok := w.handlers[fnCap]; ok {
		caps = append(caps, fnCap)
	}
	if _, ok := w.handlers[researchCap]; ok {
		caps = append(caps, researchCap)
	}
	if _, ok := w.handlers[researchTickCap]; ok {
		caps = append(caps, researchTickCap)
	}
	if _, ok := w.handlers[hanzodRunCap]; ok {
		caps = append(caps, hanzodRunCap)
	}
	return caps
}

// hasNonRenderLane reports whether this worker serves any lane beyond the render
// path (echo is a smoke type, not a lane).
func (w *worker) hasNonRenderLane() bool {
	for name := range w.handlers {
		if name != "echo" && name != studioCap {
			return true
		}
	}
	return false
}

// studioReachable probes the local studio's /prompt (bounded), authenticated. A
// node that can answer it is up enough to accept a render.
//
// /prompt, NOT /queue. A Studio holds no queue — a box renders one prompt at a
// time and nothing waits behind it — so the queue route was removed and GET
// /prompt reports the one slot instead ({"exec_info":{"rendering":N}}). Probing
// the route that no longer exists answered 404 on every pass, which pinned
// studioReady false forever: the node claimed jobs off gpu-jobs and then
// declined every one of them as "studio not ready", and the work sat until its
// lease lapsed. Measured on the GB10: /queue 404, /prompt 200.
func (w *worker) studioReachable(ctx context.Context) bool {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, localComfyUI+"/prompt", nil)
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
// whether the verdict changed. Ready ⇔ a worker token is present AND a studio
// ANSWERS right now. The token is the decisive gate: without it the gated execute
// client 403s every job.
//
// Reachability is MEASURED even when we launch the studio ourselves. Launching one
// does not make it serve — it makes it serve eventually, after it binds the port and
// loads its models, which on a cold box is minutes. Treating "--studio-dir was
// passed" as readiness advertised studio.render and claimed render jobs from the
// first instant of the process, before superviseStudio had even been started (it is
// launched after this runs), so a cold node won every job it could reach and failed
// each one on arrival. Nothing deadlocks by probing instead: the studio launch does
// not depend on this verdict, and the heartbeat loop re-evaluates it every 30s and
// flips claiming on the way up — the same path that already recovers a studio that
// died under a node that had been ready.
func (w *worker) refreshStudioReady(ctx context.Context) bool {
	ready := workerToken() != "" && w.studioReachable(ctx)
	changed := ready != w.studioReady
	w.studioReady = ready
	return changed
}

// refreshLanes asks the local studio which render lanes it can run (GET /v1/lanes)
// and returns whether the answer changed. A studio that cannot render advertises
// none; an unreadable answer keeps the last one rather than flapping the record.
func (w *worker) refreshLanes(ctx context.Context) bool {
	lanes, models := []string(nil), []laneModel(nil)
	if w.studioReady {
		got, ok := w.readLanes(ctx)
		if !ok {
			return false
		}
		lanes, models = got.Lanes, got.Models
	}
	changed := !slices.Equal(lanes, w.lanes) || !slices.Equal(models, w.models)
	w.lanes, w.models = lanes, models
	return changed
}

type laneReport struct {
	Lanes  []string    `json:"lanes"`
	Models []laneModel `json:"models"`
}

func (w *worker) readLanes(ctx context.Context) (laneReport, bool) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out laneReport
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, localComfyUI+"/v1/lanes", nil)
	if err != nil {
		return out, false
	}
	authLocal(req)
	resp, err := w.http.Do(req)
	if err != nil {
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil {
		return out, false
	}
	return out, true
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
		return "STUDIO_WORKER_TOKEN not set (the gated studio client would refuse every render) — set it from KMS"
	}
	if w.launchesStudio {
		return fmt.Sprintf("the studio we launched is not serving on %s yet (still starting, or it failed) — this clears by itself once it answers", localComfyUI)
	}
	return fmt.Sprintf("no studio reachable on %s (start one, or pass --studio-dir so the worker launches it)", localComfyUI)
}

func (w *worker) heartbeat(ctx context.Context) error {
	busy, busyReason := w.isNodeBusy(ctx)
	details := map[string]any{
		"gpus": len(w.gpus),
		"ts":   time.Now().UTC().Format(time.RFC3339),
		"busy": busy,
	}
	if busy && busyReason != "" {
		details["busyReason"] = busyReason
	} else {
		busyReason = ""
	}
	path := fmt.Sprintf("%s/namespaces/%s/activities/%s/%s/heartbeat", engineAPI, fleetNS, w.identity, w.identity)
	_, err := w.call(ctx, http.MethodPost, path, map[string]any{
		"details": details,
	}, nil)
	if err == nil && w.registeredOnce && (busy != w.advBusy || busyReason != w.advBusyReason) {
		reg := w.buildRegistration()
		reg.Busy = busy
		reg.BusyReason = busyReason
		if _, rerr := w.call(ctx, http.MethodPost, engineAPI+"/namespaces/"+fleetNS+"/activities", map[string]any{
			"activityId":       w.identity,
			"runId":            w.identity,
			"activityType":     map[string]any{"name": "fleet.worker"},
			"taskQueue":        fleetNS,
			"heartbeatTimeout": "120s",
			"input":            reg,
		}, nil); rerr == nil {
			w.advBusy = busy
			w.advBusyReason = busyReason
		}
	}
	return err
}

type jobScheduleHints struct {
	WhenIdleOnly    bool
	RequiredBackend string
}

func parseScheduleHints(input json.RawMessage) jobScheduleHints {
	h := jobScheduleHints{WhenIdleOnly: true}
	if len(input) == 0 {
		return h
	}
	var raw struct {
		AllowWhenBusy        *bool  `json:"allowWhenBusy,omitempty"`
		AllowWhenBusySnake   *bool  `json:"allow_when_busy,omitempty"`
		WhenIdleOnly         *bool  `json:"whenIdleOnly,omitempty"`
		WhenIdleOnlySnake    *bool  `json:"when_idle_only,omitempty"`
		RequiredBackend      string `json:"requiredBackend,omitempty"`
		RequiredBackendSnake string `json:"required_backend,omitempty"`
	}
	if json.Unmarshal(input, &raw) != nil {
		return h
	}
	if (raw.AllowWhenBusy != nil && *raw.AllowWhenBusy) || (raw.AllowWhenBusySnake != nil && *raw.AllowWhenBusySnake) {
		h.WhenIdleOnly = false
	} else if raw.WhenIdleOnly != nil {
		h.WhenIdleOnly = *raw.WhenIdleOnly
	} else if raw.WhenIdleOnlySnake != nil {
		h.WhenIdleOnly = *raw.WhenIdleOnlySnake
	}
	h.RequiredBackend = strings.ToLower(strings.TrimSpace(cmp.Or(raw.RequiredBackend, raw.RequiredBackendSnake)))
	return h
}

// deferClaimedJob returns a claimed job back to the queue without failing it:
// prefers `POST .../release` (`delaySeconds: 10`), and on pre-release servers
// (404/400) returns nil without calling `/fail` so the short claim lease lapses
// and hanzoai/tasks retries automatically once the node is idle.
func (w *worker) deferClaimedJob(ctx context.Context, out io.Writer, wf, run, reason string) error {
	code, err := w.call(ctx, http.MethodPost, w.actPath(wf, run, "release"), map[string]any{
		"reason":       reason,
		"identity":     w.identity,
		"delaySeconds": releaseRetryDelaySecs,
	}, nil)
	if err == nil {
		fmt.Fprintf(out, "  → deferred (%s) — released back to queue (retry in %ds)\n", reason, releaseRetryDelaySecs)
		return nil
	}
	if code == http.StatusNotFound || code == http.StatusBadRequest || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented {
		fmt.Fprintf(out, "  → deferred (%s) — release unsupported (HTTP %d), letting %ds claim lease lapse\n", reason, code, claimLeaseSecs)
		return nil
	}
	fmt.Fprintf(out, "  → deferred (%s) — release error (%v), letting %ds claim lease lapse\n", reason, err, claimLeaseSecs)
	return nil
}

// gpuQueue is this worker's OWN task-queue lane — the value a job names to target
// THIS machine ("gpu:spark"). Targeted jobs land here; the shared w.jobsNS
// ("gpu-jobs") lane stays the any-GPU broadcast. ONE namespace (w.jobsNS), two
// queue VALUES — targeting is the string, not a second store or endpoint.
func (w *worker) gpuQueue() string { return gpuQueuePrefix + w.identity }

// targetedQueues returns this worker's primary targeted queue ("gpu:<identity>")
// plus any host alias queues — notably trimming a trailing "-wsl" suffix (so a
// WSL2 guest whose hostname is "evo-wsl" also claims "gpu:evo", and vice versa).
func (w *worker) targetedQueues() []string {
	seen := map[string]bool{}
	var qs []string
	add := func(id string) {
		id = sanitizeID(id)
		if id == "" || id == "worker" && w.identity != "worker" {
			return
		}
		q := gpuQueuePrefix + id
		if !seen[q] {
			seen[q] = true
			qs = append(qs, q)
		}
	}
	add(w.identity)
	for _, cand := range []string{w.identity, w.hostname, w.rawHostname} {
		lc := strings.ToLower(strings.TrimSpace(cand))
		if trimmed, ok := strings.CutSuffix(lc, "-wsl"); ok && trimmed != "" {
			add(trimmed)
		}
	}
	if w.rawHostname != "" && sanitizeID(w.rawHostname) != w.identity {
		add(w.rawHostname)
	}
	return qs
}

// claimFrom claims the next job on ONE task-queue value within w.jobsNS. Returns
// (act, true, nil) when a job was claimed, (_, false, nil) on an empty queue (204).
func (w *worker) claimFrom(ctx context.Context, taskQueue string) (claimedActivity, bool, error) {
	var act claimedActivity
	code, err := w.call(ctx, http.MethodPost, engineAPI+"/namespaces/"+w.jobsNS+"/activities/claim", map[string]any{
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
	// Poison-loop guard, per-LANE: a node that can't serve renders must not sit on
	// render jobs — but a node with other lanes (fn.run) still claims. When renders
	// are this worker's only real lane and the studio isn't ready, stay idle; a
	// claimed render on a non-ready node is declined below so an eligible worker
	// takes it.
	if !w.studioReady && !w.hasNonRenderLane() {
		return nil
	}
	ignoreBusy := os.Getenv("HANZO_LINK_IGNORE_BUSY") == "1"
	busy, busyReason := false, ""
	if !ignoreBusy {
		busy, busyReason = w.isNodeBusy(ctx)
	}
	var act claimedActivity
	var claimed bool
	var err error
	for _, tq := range w.targetedQueues() {
		act, claimed, err = w.claimFrom(ctx, tq)
		if err != nil {
			return err
		}
		if claimed {
			break
		}
	}
	if !claimed {
		if busy {
			// Pre-claim busy skip: leave shared any-machine work for idle peers.
			return nil
		}
		if act, claimed, err = w.claimFrom(ctx, w.jobsNS); err != nil {
			return err
		}
		if !claimed {
			return nil // both lanes empty — nothing to do
		}
	}
	wf, run := act.Execution.WorkflowId, act.Execution.RunId
	fmt.Fprintf(out, "claimed job %s (type %s)\n", wf, act.Type.Name)

	hints := parseScheduleHints(act.Input)
	if hints.RequiredBackend != "" && !w.supportsBackend(hints.RequiredBackend) {
		return w.deferClaimedJob(ctx, out, wf, run, "required_backend_mismatch:"+hints.RequiredBackend)
	}
	if !ignoreBusy && hints.WhenIdleOnly {
		if !busy {
			busy, busyReason = w.isNodeBusy(ctx)
		}
		if busy {
			return w.deferClaimedJob(ctx, out, wf, run, cmp.Or(busyReason, "node_busy"))
		}
	}

	// Sharing policy: decline (fail back) a job this machine isn't allowed to run,
	// so an eligible worker can take it. Enforced once, here, at claim.
	if reason := w.policy.reject(act.Type.Name, act.Input); reason != "" {
		_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "fail"), map[string]any{"cause": "share policy: " + reason, "identity": w.identity}, nil)
		fmt.Fprintf(out, "  → declined (share policy: %s)\n", reason)
		return nil
	}

	if act.Type.Name == studioCap && !w.studioReady {
		// DECLINE BY LEASE LAPSE — deliberately no terminal report. `fail` is
		// terminal: it does not return the job to anyone, so declining with it
		// KILLED the render it claimed to be handing on. Two workers that had both
		// just restarted each claimed one render and each failed it, and the person
		// who asked for it got nothing back.
		//
		// The engine's verbs are claim/complete/fail/heartbeat/cancel — there is no
		// release. What there IS is the claim's LEASE (claimLeaseSecs): a claim that
		// is neither completed nor failed nor heartbeated expires and the job goes
		// back to pending for whoever can run it. So the way to hand a job back is
		// to say NOTHING and let the lease lapse, which is what this branch always
		// meant by "declined".
		fmt.Fprintf(out, "  → declined (studio not ready on this node) — returning it to the queue by letting the %ds claim lease lapse\n", claimLeaseSecs)
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
	// the handler runs. With claimLeaseSecs = 30s (for fast retry when a deferred
	// claim lapses on a pre-release server), refresh the claimed activity's lease
	// every claimHeartbeatEvery (10s) and refresh fleet presence every 3rd tick.
	hbCtx, stopHB := context.WithCancel(ctx)
	go func() {
		leaseTicker := time.NewTicker(claimHeartbeatEvery)
		presenceTicker := time.NewTicker(heartbeatEvery)
		defer leaseTicker.Stop()
		defer presenceTicker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-leaseTicker.C:
				_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "heartbeat"),
					map[string]any{"identity": w.identity}, nil)
			case <-presenceTicker.C:
				_ = w.heartbeat(ctx) // fleet presence — stays online through the job
				w.reportSample(ctx)  // live util during the job (the board's hottest point)
			}
		}
	}()
	result, herr := h(ctx, act.Input)
	stopHB()
	if herr != nil {
		_, _ = w.call(ctx, http.MethodPost, w.actPath(wf, run, "fail"), map[string]any{"cause": herr.Error(), "identity": w.identity}, nil)
		fmt.Fprintf(out, "  → failed: %v\n", herr)
		signalWake(w.wakeCh) // execution slot freed — check immediately for queued work
		return nil
	}
	if _, err := w.call(ctx, http.MethodPost, w.actPath(wf, run, "complete"), map[string]any{"result": result, "identity": w.identity}, nil); err != nil {
		signalWake(w.wakeCh)
		return fmt.Errorf("complete %s: %w", wf, err)
	}
	fmt.Fprintf(out, "  → completed\n")
	signalWake(w.wakeCh) // execution slot freed — start any next queued job at 0ms latency
	return nil
}

func (w *worker) actPath(wf, run, verb string) string {
	return fmt.Sprintf("%s/namespaces/%s/activities/%s/%s/%s", engineAPI, w.jobsNS, wf, run, verb)
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
// disconnect.
// ---------------------------------------------------------------------------

// fleetWorker is one row of GET /v1/compute/fleet/workers — what the org's fleet reports
// back about a machine that linked in. Rendering it is the fabric CLI's `status`;
// here it is the shape this binary's registration must round-trip to.
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
	Lanes         []string             `json:"lanes,omitempty"`
	Models        []laneModel          `json:"models,omitempty"`
	Busy          bool                 `json:"busy"`
	BusyReason    string               `json:"busyReason,omitempty"`
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
	return runDisconnectWithID(cmd, env, os.Getenv("HANZO_NODE_ID"))
}

func runDisconnectWithID(cmd *cobra.Command, env *Env, nodeID string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if _, err := env.ensureToken(ctx); err != nil {
		return err
	}
	w, _ := newNamedWorker(env, "", nodeID)
	out := cmd.OutOrStdout()
	path := fmt.Sprintf("%s/namespaces/%s/activities/%s/%s/complete", engineAPI, fleetNS, w.identity, w.identity)
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
	if c := os.Getenv("HANZO_TOKEN_COMMAND"); c != "" {
		return asked.token(ctx, c)
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

// tokenCommand holds the bearer HANZO_TOKEN_COMMAND last printed. The command is
// the box's one refresh authority (`hanzo auth token`, the same one `hanzo link dial
// --token-command` asks), so a worker that runs for days never holds a token past
// its expiry and never keeps a second refresh token of its own.
type tokenCommand struct {
	mu  sync.Mutex
	tok string
	exp int64
}

var asked tokenCommand

// token answers the held bearer while it has more than 90s left, else runs the
// command (its words split on spaces, no shell) and holds what it prints.
func (t *tokenCommand) token(ctx context.Context, command string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tok != "" && time.Now().Add(90*time.Second).Unix() < t.exp {
		return t.tok, nil
	}
	words := strings.Fields(command)
	if len(words) == 0 {
		return "", fmt.Errorf("HANZO_TOKEN_COMMAND is empty")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, words[0], words[1:]...).Output()
	if err != nil {
		return "", fmt.Errorf("HANZO_TOKEN_COMMAND %q: %w", words[0], err)
	}
	tok := strings.TrimSpace(string(out))
	claims, err := decodeJWTClaims(tok)
	if err != nil {
		return "", fmt.Errorf("HANZO_TOKEN_COMMAND %q printed no token: %w", words[0], err)
	}
	exp, _ := claims["exp"].(float64)
	t.tok, t.exp = tok, int64(exp)
	return tok, nil
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

// fnRunInput is the fn.run job payload: an inline script executed on this node in
// an ephemeral `uv run` environment. The queue is org-scoped, so the submitter is
// running code on their OWN fleet — same trust domain as a CI runner.
type fnRunInput struct {
	Name           string            `json:"name,omitempty"`           // label for logs
	Script         string            `json:"script"`                   // Python source (required)
	Requirements   []string          `json:"requirements,omitempty"`   // uv --with deps, e.g. ["numpy", "torch==2.5.*"]
	Env            map[string]string `json:"env,omitempty"`            // extra environment
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"` // default 3600, cap 21600
}

const (
	fnDefaultTimeout = time.Hour
	fnMaxTimeout     = 6 * time.Hour
	fnOutputTail     = 128 << 10 // keep the LAST 128 KiB of combined output
)

// fnValidate parses and bounds an fn.run payload. Requirements must look like
// package specs — a leading dash would inject uv flags.
func fnValidate(input json.RawMessage) (fnRunInput, error) {
	var in fnRunInput
	if err := json.Unmarshal(input, &in); err != nil {
		return in, fmt.Errorf("fn.run: bad input: %w", err)
	}
	if strings.TrimSpace(in.Script) == "" {
		return in, fmt.Errorf("fn.run: input needs a `script`")
	}
	for _, r := range in.Requirements {
		if r == "" || strings.HasPrefix(r, "-") {
			return in, fmt.Errorf("fn.run: bad requirement %q", r)
		}
	}
	if in.TimeoutSeconds <= 0 {
		in.TimeoutSeconds = int(fnDefaultTimeout / time.Second)
	}
	if in.TimeoutSeconds > int(fnMaxTimeout/time.Second) {
		in.TimeoutSeconds = int(fnMaxTimeout / time.Second)
	}
	return in, nil
}

// tailBuffer keeps the last cap bytes written — a training loop can log gigabytes;
// the activity result carries the end, where the outcome lives.
type tailBuffer struct {
	cap       int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.cap {
		t.buf = t.buf[len(t.buf)-t.cap:]
		t.truncated = true
	}
	return len(p), nil
}

// fnRunHandler executes an fn.run job: write the script to an ephemeral dir, run
// it under `uv run` (which resolves requirements into a throwaway env — ROCm/CUDA
// wheels included), and return the output tail + exit code. Nonzero exit fails
// the activity with the tail as the cause, so the submitter sees the traceback.
func fnRunHandler(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := fnValidate(input)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "fnrun-*")
	if err != nil {
		return nil, fmt.Errorf("fn.run: %w", err)
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "main.py")
	if err := os.WriteFile(script, []byte(in.Script), 0o600); err != nil {
		return nil, fmt.Errorf("fn.run: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutSeconds)*time.Second)
	defer cancel()
	args := []string{"run", "--no-project", "--quiet"}
	for _, r := range in.Requirements {
		args = append(args, "--with", r)
	}
	args = append(args, script)
	cmd := exec.CommandContext(runCtx, "uv", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range in.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	tail := &tailBuffer{cap: fnOutputTail}
	cmd.Stdout = tail
	cmd.Stderr = tail
	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)
	out := string(tail.buf)
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("fn.run: timed out after %ds\n%s", in.TimeoutSeconds, out)
	}
	if runErr != nil {
		return nil, fmt.Errorf("fn.run: %v\n%s", runErr, out)
	}
	return map[string]any{
		"name":       in.Name,
		"exitCode":   0,
		"durationMs": dur.Milliseconds(),
		"output":     out,
		"truncated":  tail.truncated,
	}, nil
}

// researchBenchInput is the research.bench job payload: either an inline Python
// script (run via `uv run --no-project --quiet`) OR a direct argv command from the
// explicit research allowlist (never passed through a shell), bounded by timeout,
// restricted workdir + env hygiene, and serialized per-host via advisory GPU flock.
// commandArgv unmarshals either a JSON string array (["python3", "-c", "..."])
// or a POSIX-quoted command string ("hanzo-research tick --campaign ml.json")
// into direct argv tokens with zero shell evaluation.
type commandArgv []string

func (c *commandArgv) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*c = nil
		return nil
	}
	if trimmed[0] == '[' {
		var arr []string
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return err
		}
		*c = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return err
	}
	tokens, err := splitPOSIXArgv(s)
	if err != nil {
		return err
	}
	*c = tokens
	return nil
}

func splitPOSIXArgv(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inToken := false
	var quote rune
	escaped := false
	for _, r := range s {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			inToken = true
			continue
		}
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			inToken = true
			continue
		}
		if quote == '"' {
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
			inToken = true
			continue
		}
		switch {
		case r == '\\':
			escaped = true
			inToken = true
		case r == '\'' || r == '"':
			quote = r
			inToken = true
		case unicode.IsSpace(r):
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape in command string")
	}
	if inToken {
		args = append(args, cur.String())
	}
	return args, nil
}

type researchBenchInput struct {
	Name           string            `json:"name,omitempty"`
	HypothesisID   string            `json:"hypothesisId,omitempty"`
	ExperimentID   string            `json:"experimentId,omitempty"`
	Script         string            `json:"script,omitempty"`
	Requirements   []string          `json:"requirements,omitempty"`
	Command        commandArgv       `json:"command,omitempty"`
	Workdir        string            `json:"workdir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
	ExclusiveGPU   *bool             `json:"exclusiveGpu,omitempty"`
}

const (
	researchDefaultTimeout = 3600
	researchMaxTimeout     = 14400
	researchOutputTail     = 256 << 10 // keep the LAST 256 KiB of output
)

var researchAllowedCommands = map[string]bool{
	"cargo":          true,
	"python3":        true,
	"uv":             true,
	"hanzo-bench":    true,
	"hanzo-research": true,
	"hanzo-server":   true,
	"llama-bench":    true,
	"nvidia-smi":     true,
	"vulkaninfo":     true,
}

var researchBlockedEnvKeys = map[string]bool{
	"LD_PRELOAD":            true,
	"LD_LIBRARY_PATH":       true,
	"DYLD_INSERT_LIBRARIES": true,
	"DYLD_LIBRARY_PATH":     true,
	"PATH":                  true,
	"HOME":                  true,
	"HANZO_TOKEN":           true,
	"HANZO_TOKEN_COMMAND":   true,
	"STUDIO_WORKER_TOKEN":   true,
	"SSH_AUTH_SOCK":         true,
}

// researchWorkdirRoots lists the canonical parent directories an explicit
// research.bench workdir may reside under. Package-level so tests can inject
// temporary roots without touching /data.
var researchWorkdirRoots = func() []string {
	return []string{"/home/z/work/hanzo", "/data", os.TempDir()}
}

func researchValidate(input json.RawMessage) (researchBenchInput, error) {
	var in researchBenchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return in, fmt.Errorf("research.bench: bad input: %w", err)
	}
	hasScript := strings.TrimSpace(in.Script) != ""
	hasCmd := len(in.Command) > 0
	if !hasScript && !hasCmd {
		return in, fmt.Errorf("research.bench: input needs `script` or `command`")
	}
	if hasScript && hasCmd {
		return in, fmt.Errorf("research.bench: specify either `script` or `command`, not both")
	}
	if hasScript {
		for _, r := range in.Requirements {
			if r == "" || strings.HasPrefix(r, "-") {
				return in, fmt.Errorf("research.bench: bad requirement %q", r)
			}
		}
	}
	if hasCmd {
		prog := strings.TrimSpace(in.Command[0])
		if prog == "" || strings.Contains(prog, "..") || strings.ContainsRune(prog, 0) {
			return in, fmt.Errorf("research.bench: disallowed command %q", in.Command[0])
		}
		base := filepath.Base(prog)
		if !researchAllowedCommands[base] {
			return in, fmt.Errorf("research.bench: command %q is not in the allowed research binary list", base)
		}
		for _, a := range in.Command[1:] {
			if strings.ContainsRune(a, 0) {
				return in, fmt.Errorf("research.bench: NUL byte in command argument")
			}
		}
		in.Command[0] = prog
	}
	for k, v := range in.Env {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" || strings.ContainsAny(trimmed, "=\x00") || strings.ContainsRune(v, 0) {
			return in, fmt.Errorf("research.bench: invalid env key %q", k)
		}
		if researchBlockedEnvKeys[strings.ToUpper(trimmed)] {
			return in, fmt.Errorf("research.bench: env key %q is forbidden", k)
		}
	}
	if strings.TrimSpace(in.Workdir) != "" {
		resolved, err := validateResearchWorkdir(in.Workdir)
		if err != nil {
			return in, err
		}
		in.Workdir = resolved
	}
	if in.TimeoutSeconds <= 0 {
		in.TimeoutSeconds = researchDefaultTimeout
	}
	if in.TimeoutSeconds > researchMaxTimeout {
		in.TimeoutSeconds = researchMaxTimeout
	}
	return in, nil
}

func validateResearchWorkdir(raw string) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("research.bench: invalid workdir")
	}
	for _, seg := range strings.Split(filepath.ToSlash(raw), "/") {
		if seg == ".." {
			return "", fmt.Errorf("research.bench: workdir %q must not contain '..' traversal", raw)
		}
	}
	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("research.bench: workdir %q must be an absolute path", raw)
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("research.bench: workdir %q does not exist: %w", raw, err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("research.bench: workdir %q is not a directory", raw)
	}
	for _, root := range researchWorkdirRoots() {
		if root == "" {
			continue
		}
		rClean := filepath.Clean(root)
		if rReal, rErr := filepath.EvalSymlinks(rClean); rErr == nil {
			rClean = rReal
		}
		if real == rClean || strings.HasPrefix(real, rClean+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", fmt.Errorf("research.bench: workdir %q is outside allowed roots", raw)
}

func researchBenchPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/home/z"
	}
	want := []string{
		filepath.Join(home, ".cargo", "bin"),
		filepath.Join(home, ".local", "bin"),
		"/usr/local/cuda/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
	}
	seen := map[string]bool{}
	var parts []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		parts = append(parts, p)
	}
	for _, p := range want {
		add(p)
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		add(p)
	}
	return strings.Join(parts, string(filepath.ListSeparator))
}

func resolveExecInPath(bin, pathEnv string) (string, error) {
	if strings.ContainsRune(bin, filepath.Separator) {
		info, err := os.Stat(bin)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("executable %q not found", bin)
		}
		return bin, nil
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, bin)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return exec.LookPath(bin)
}

func researchChildEnv(pathEnv string, extra map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PATH", "HANZO_TOKEN", "HANZO_TOKEN_COMMAND", "STUDIO_WORKER_TOKEN":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+pathEnv)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, strings.TrimSpace(k)+"="+extra[k])
	}
	return env
}

func extractResearchJSON(stdout string) json.RawMessage {
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if payload, ok := strings.CutPrefix(line, "RESEARCH_JSON:"); ok {
			payload = strings.TrimSpace(payload)
			var obj map[string]any
			if json.Unmarshal([]byte(payload), &obj) == nil {
				b, _ := json.Marshal(obj)
				return b
			}
			if json.Valid([]byte(payload)) {
				return json.RawMessage(payload)
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			var obj map[string]any
			if json.Unmarshal([]byte(line), &obj) == nil {
				b, _ := json.Marshal(obj)
				return b
			}
		}
		break // only inspect the final non-empty stdout line for bare JSON objects
	}
	return nil
}

func (w *worker) researchLockPaths() []string {
	paths := []string{filepath.Join(os.TempDir(), "hanzo-research-gpu.lock")}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/home/z"
	}
	researchDir := filepath.Join(home, ".hanzo", "research")
	if info, err := os.Stat(researchDir); err == nil && info.IsDir() {
		ctrlDir := filepath.Join(researchDir, "controller")
		if cInfo, cErr := os.Stat(ctrlDir); cErr == nil && cInfo.IsDir() {
			paths = append(paths, filepath.Join(ctrlDir, "gpu.lock"))
		}
		paths = append(paths, filepath.Join(researchDir, ".gpu.lock"))
	}
	return paths
}

func (w *worker) researchBenchHandler(ctx context.Context, input json.RawMessage) (any, error) {
	in, err := researchValidate(input)
	if err != nil {
		return nil, err
	}
	scratchDir, err := os.MkdirTemp("", "researchbench-*")
	if err != nil {
		return nil, fmt.Errorf("research.bench: %w", err)
	}
	defer os.RemoveAll(scratchDir)

	workdir := scratchDir
	if in.Workdir != "" {
		workdir = in.Workdir
	}

	pathEnv := researchBenchPath()
	var bin string
	var args []string
	if strings.TrimSpace(in.Script) != "" {
		uvBin, err := resolveExecInPath("uv", pathEnv)
		if err != nil {
			return nil, fmt.Errorf("research.bench: uv not installed: %w", err)
		}
		scriptPath := filepath.Join(scratchDir, "bench.py")
		if err := os.WriteFile(scriptPath, []byte(in.Script), 0o600); err != nil {
			return nil, fmt.Errorf("research.bench: %w", err)
		}
		bin = uvBin
		args = []string{"run", "--no-project", "--quiet"}
		for _, r := range in.Requirements {
			args = append(args, "--with", r)
		}
		args = append(args, scriptPath)
	} else {
		resolvedBin, err := resolveExecInPath(in.Command[0], pathEnv)
		if err != nil {
			return nil, fmt.Errorf("research.bench: %w", err)
		}
		bin = resolvedBin
		args = in.Command[1:]
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutSeconds)*time.Second)
	defer cancel()

	exclusive := len(w.gpus) > 0
	if in.ExclusiveGPU != nil {
		exclusive = *in.ExclusiveGPU
	}
	if exclusive {
		unlock, err := acquireGPULock(runCtx, w.researchLockPaths()...)
		if err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("research.bench: timed out waiting for exclusive GPU lock after %ds", in.TimeoutSeconds)
			}
			return nil, fmt.Errorf("research.bench: gpu lock: %w", err)
		}
		defer unlock()
	}

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = workdir
	cmd.Env = researchChildEnv(pathEnv, in.Env)

	tail := &tailBuffer{cap: researchOutputTail}
	stdoutTail := &tailBuffer{cap: researchOutputTail}
	cmd.Stdout = io.MultiWriter(tail, stdoutTail)
	cmd.Stderr = tail

	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)
	out := string(tail.buf)

	if runCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("research.bench: timed out after %ds\n%s", in.TimeoutSeconds, out)
	}
	exitCode := 0
	if runErr != nil {
		exitCode = -1
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
		return nil, fmt.Errorf("research.bench: exit %d: %v\n%s", exitCode, runErr, out)
	}

	nv := nvidiaSoftware()
	res := map[string]any{
		"exitCode":     exitCode,
		"durationMs":   dur.Milliseconds(),
		"output":       out,
		"truncated":    tail.truncated,
		"exclusiveGpu": exclusive,
		"hostname":     w.hostname,
		"arch":         w.arch,
		"gpus":         w.gpus,
	}
	if in.Name != "" {
		res["name"] = in.Name
	}
	if in.HypothesisID != "" {
		res["hypothesisId"] = in.HypothesisID
	}
	if in.ExperimentID != "" {
		res["experimentId"] = in.ExperimentID
	}
	if nv.cuda != "" {
		res["cuda"] = nv.cuda
	}
	if nv.driver != "" {
		res["driver"] = nv.driver
	}
	if rj := extractResearchJSON(string(stdoutTail.buf)); len(rj) > 0 {
		res["resultJson"] = rj
	}
	return res, nil
}

// workerToken is the KMS-sourced STUDIO_WORKER_TOKEN this box holds — the credential
// the local studio's --worker-mode gate checks. Empty on an unconfigured box (the
// render preflight refuses such a node before it ever claims a render lane).
func workerToken() string { return os.Getenv("STUDIO_WORKER_TOKEN") }

// authLocal stamps the worker token on a loopback studio request. The studio's
// --worker-mode gate guards the submit client today, but sending it on ALL loopback
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
	authLocal(post) // gated worker-mode submit client
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
			if why := renderFailed(entry); why != "" {
				requestStudioRecycle()
				return nil, fmt.Errorf("studio.render: prompt %s failed in the studio: %s", pr.PromptID, why)
			}
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
	base := strings.TrimRight(cmp.Or(uploadURL, w.studioUploadURL), "/")
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
	Data      string `json:"data"` // base64 — small inputs ride inside the job
	// URL is the other way an input arrives: the coordinator serves it and this
	// worker FETCHES it. Inlining is bounded by the tasks API's 4MB body, so a 4K
	// source (19MB, 25MB encoded) could never be enqueued at all — the dispatch was
	// refused, reported as "GPU worker unavailable", and the render waited forever
	// for a fleet that was healthy. Pulling the bytes moves them over a channel with
	// no such ceiling. Empty for an inlined input; when set, Data is empty.
	URL string `json:"url"`
}

// fetchInput pulls one input's bytes from the coordinator that dispatched this job.
//
// It exists because inlining has a ceiling and photographs do not. The tasks API
// takes a 4MB body, so a 4K source could never ride inside the job — its dispatch was
// refused, the refusal was reported as "GPU render worker is momentarily unavailable",
// and the render waited on a fleet that was never the problem. Fetching has no such
// ceiling, and the coordinator already serves the file it staged.
//
// Authorized with this worker's own credentials, exactly as uploadOutputs is: the
// worker is a member of the org whose work it claims, and the coordinator resolves
// the org from the token rather than from anything the job says.
func (w *worker) fetchInput(ctx context.Context, cl *http.Client, url string) ([]byte, error) {
	tok, err := w.env.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, serverMessage(raw))
	}
	// A source is megabytes, not gigabytes; the cap is a guard, not a budget.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchedInputBytes))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	return data, nil
}

// materializeInputs writes each shipped input into the LOCAL studio's input dir by
// POSTing it to the studio's own /upload/image (loopback, auth-bypassed). This is
// how an uploaded photo — which lives in orgs/{org}/input on the dispatching pod,
// unreadable by this worker — becomes resolvable by LoadImage before the render.
func (w *worker) materializeInputs(ctx context.Context, cl *http.Client, inputs []inputImage) error {
	for _, in := range inputs {
		if in.Name == "" || (in.Data == "" && in.URL == "") {
			continue
		}
		var data []byte
		var err error
		if in.Data != "" {
			if data, err = base64.StdEncoding.DecodeString(in.Data); err != nil {
				return fmt.Errorf("decode input %q: %w", in.Name, err)
			}
		} else if data, err = w.fetchInput(ctx, cl, in.URL); err != nil {
			return fmt.Errorf("fetch input %q: %w", in.Name, err)
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

// waitEngine blocks until the local engine answers its /prompt — up to 90s, which
// outlasts any supervisor recycle (engine restart is seconds, model reload longer).
func waitEngine(ctx context.Context, cl *http.Client) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, localComfyUI+"/prompt", nil)
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
// renderFailed is the studio's own verdict on a finished prompt: "" when it
// succeeded, else what failed (the node and its exception). A prompt that errored
// still lands in /history, with no outputs, and reading it as a success reported a
// render that never happened as COMPLETED.
func renderFailed(entry json.RawMessage) string {
	var e struct {
		Status struct {
			Str      string            `json:"status_str"`
			Messages []json.RawMessage `json:"messages"`
		} `json:"status"`
	}
	if json.Unmarshal(entry, &e) != nil || e.Status.Str != "error" {
		return ""
	}
	for _, m := range e.Status.Messages {
		var pair []json.RawMessage
		if json.Unmarshal(m, &pair) != nil || len(pair) != 2 {
			continue
		}
		var kind string
		var body struct {
			NodeType string `json:"node_type"`
			Message  string `json:"exception_message"`
		}
		if json.Unmarshal(pair[0], &kind) == nil && kind == "execution_error" && json.Unmarshal(pair[1], &body) == nil {
			return strings.TrimSpace(body.NodeType + ": " + body.Message)
		}
	}
	return "the studio reported an error"
}

func collectOutputs(entry json.RawMessage) []string {
	type namedFile struct {
		Filename  string `json:"filename"`
		Subfolder string `json:"subfolder"`
	}
	var e struct {
		Outputs map[string]struct {
			Images []namedFile `json:"images"`
			ThreeD []namedFile `json:"3d"`
			Audio  []namedFile `json:"audio"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		return nil
	}
	var files []string
	for _, node := range e.Outputs {
		for _, f := range slices.Concat(node.Images, node.ThreeD, node.Audio) {
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

// providerBody is the POST /v1/ai/providers payload registering this node's engine
// as an org model provider. hanzo-engine speaks the standard chat-completions wire, so Type=Local: the
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
	fmt.Fprintf(out, "    curl -sS %s/v1/ai/providers -H \"Authorization: Bearer $HANZO_TOKEN\" \\\n", w.baseURL)
	fmt.Fprintf(out, "      -H 'Content-Type: application/json' -d '%s'\n", body)
	fmt.Fprintln(out, "  (or pass --register-provider. The endpoint must be reachable from api.hanzo.ai —")
	fmt.Fprintln(out, "   a cloud GPU is in-cluster; a BYO node needs a public URL/tunnel. add-provider needs a platform-admin token today.)")
}

// registerProvider POSTs /v1/ai/providers so the gateway routes model calls to this
// node's engine. Requires the engine to be reachable and (today) a platform-admin
// token; both failures are reported clearly rather than swallowed.
func (w *worker) registerProvider(ctx context.Context, adv *engineAdvertisement) error {
	if adv == nil || adv.Status != "ready" {
		return fmt.Errorf("engine not ready at %s — start hanzo-engine, then retry", w.engineURL)
	}
	code, err := w.call(ctx, http.MethodPost, "/v1/ai/providers", w.providerBody(), nil)
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
	if opts.nodeID != "" {
		args += " --name " + sanitizeID(opts.nodeID)
	}
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
	fmt.Fprintln(out, "note: the daemon reuses ~/.hanzo/credentials.json — keep `hanzo login` current, or set HANZO_TOKEN_COMMAND (e.g. `hanzo auth token`) in the unit.")
	return nil
}
