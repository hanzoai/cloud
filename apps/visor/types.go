// types.go holds the Visor wire structs (what upstream returns) and the console
// view structs (what this subsystem emits), plus the PURE mapping between them.
// The view JSON keys mirror the console normalizers EXACTLY so the Machines,
// GPUs and Clusters pages render with no front-end change:
//
//   - machineView  -> console src/lib/api/visor.ts    normalizeMachine
//   - gpuView      -> console src/lib/api/compute.ts   normalizeGpu
//   - clusterView  -> console src/lib/api/platform.ts  Cluster + NodePool
//
// Every field is a REAL Visor value or an honest omission. Telemetry Visor does
// not carry (GPU utilization/temperature/power) is left off the gpuView so the
// UI shows "—", never a fabricated 0.
package visor

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---- Visor wire structs (object.Machine / object.NodePool, JSON subset) ----

// visorMachine mirrors visor/object.Machine. Secret fields (RemotePassword) are
// intentionally omitted — this client never decodes, holds, or re-emits a
// machine credential (the verb endpoints already return masked machines).
type visorMachine struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Id          string `json:"id"`
	Provider    string `json:"provider"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Region      string `json:"region"`
	Zone        string `json:"zone"`
	Type        string `json:"type"`
	Size        string `json:"size"`
	State       string `json:"state"`
	Image       string `json:"image"`
	Os          string `json:"os"`
	PublicIp    string `json:"publicIp"`
	PrivateIp   string `json:"privateIp"`
	CpuSize     string `json:"cpuSize"`
	// MemSize is the machine's system RAM as reported upstream. Providers send it in
	// mixed forms (a DO CPU droplet as "8gb", the resell surface as raw "8192" MB),
	// so normalizeMem renders only the trustworthy shapes and drops the ambiguous.
	MemSize string `json:"memSize"`
	// Tag is the comma-joined provider tag list (the resell /v1/machines surface
	// carries it). It records `hanzo-kind:<kind>` (kind=bot for a Bot) and, once
	// the launch cloud-init installs the runtime, `hanzo-bot:<agentName>`. bots.go
	// reads it to tell a Bot machine from a plain one.
	Tag string `json:"tag"`
}

// visorNodePool mirrors visor/object.NodePool (JSON subset relevant to clusters).
type visorNodePool struct {
	Name        string `json:"name"`
	CreatedTime string `json:"createdTime"`
	ClusterID   string `json:"clusterId"`
	PoolID      string `json:"poolId"`
	Size        string `json:"size"`
	Count       int    `json:"count"`
	MinNodes    int    `json:"minNodes"`
	MaxNodes    int    `json:"maxNodes"`
	AutoScale   bool   `json:"autoScale"`
	State       string `json:"state"`
}

// ---- console view structs ----

// machineView is the shape console visor.ts normalizeMachine consumes. `id` is
// the org-scoped machine NAME (the stable key the :id routes address), not the
// ephemeral provider id.
type machineView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Region      string `json:"region,omitempty"`
	Type        string `json:"type,omitempty"`
	Status      string `json:"status,omitempty"`
	Provider    string `json:"provider,omitempty"`
	PublicIp    string `json:"publicIp,omitempty"`
	PrivateIp   string `json:"privateIp,omitempty"`
	CreatedTime string `json:"createdTime,omitempty"`
	Vcpu        *int   `json:"vcpu,omitempty"`
	Mem         string `json:"mem,omitempty"`
	GPU         string `json:"gpu,omitempty"`
	Image       string `json:"image,omitempty"`
	Os          string `json:"os,omitempty"`
}

// gpuView is the shape console compute.ts normalizeGpu consumes — one row per
// physical accelerator. Model/region/status are REAL (from the GPU machine and
// its size slug); live telemetry is absent because Visor's machine object carries
// none, so the UI renders "—".
type gpuView struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Model    string `json:"model,omitempty"`
	Region   string `json:"region,omitempty"`
	Status   string `json:"status,omitempty"`
	Location string `json:"location,omitempty"`
	Machine  string `json:"machine,omitempty"`
	// Provider distinguishes a BYO accelerator ("byo") from a Visor-provisioned
	// one (the machine's real provider). Memory is VRAM when known (BYO reports it
	// from nvidia-smi; Visor's machine object carries none, so it stays empty and
	// the UI renders "—"). Both are additive + omitempty: existing rows are
	// unaffected and the console normalizer ignores fields it does not read.
	Provider string `json:"provider,omitempty"`
	Memory   string `json:"memory,omitempty"`
}

// nodePoolView is the shape console platform.ts NodePool consumes.
type nodePoolView struct {
	PoolID    string `json:"poolId,omitempty"`
	Name      string `json:"name,omitempty"`
	Size      string `json:"size,omitempty"`
	Count     int    `json:"count"`
	MinNodes  int    `json:"minNodes,omitempty"`
	MaxNodes  int    `json:"maxNodes,omitempty"`
	AutoScale bool   `json:"autoScale,omitempty"`
}

// clusterView is the shape console platform.ts Cluster consumes. The authoritative
// node inventory is NodePools; nodeSize/nodeCount are the derived display fields
// the simple Clusters list uses (and the GPU derivation reads).
type clusterView struct {
	DoksClusterID string         `json:"doksClusterId,omitempty"`
	DoClusterID   string         `json:"doClusterId,omitempty"`
	Name          string         `json:"name"`
	Region        string         `json:"region,omitempty"`
	Status        string         `json:"status"`
	NodePools     []nodePoolView `json:"nodePools"`
	NodeSize      string         `json:"nodeSize,omitempty"`
	NodeCount     int            `json:"nodeCount"`
	CreatedAt     string         `json:"createdAt,omitempty"`
	// Fleet fields (additive): "managed" (Visor-provisioned) vs "byo" (attached
	// kubeconfig), and the live GPU inventory a BYO cluster reports.
	Kind      string `json:"kind,omitempty"`
	NvidiaGPU int    `json:"nvidiaGpu,omitempty"`
	AmdGPU    int    `json:"amdGpu,omitempty"`
}

// ---- mapping (PURE); firstNonEmpty lives in client.go (one helper, one place) ----

// toMachineView maps a Visor machine to the console view. vcpu prefers a clean
// integer CpuSize and otherwise recovers the count from a provider size slug
// (s-4vcpu-8gb) — still no fabricated core count, just an honest read of the slug.
// mem is the trustworthy MemSize, or a NON-GPU slug's gb figure, else empty — a
// GPU slug's gb is VRAM, never system RAM, so it is never surfaced as mem. gpu is
// filled only when the size slug parses as a GPU accelerator.
func toMachineView(m visorMachine) machineView {
	v := machineView{
		ID:          firstNonEmpty(m.Name, m.Id),
		Name:        firstNonEmpty(m.DisplayName, m.Name),
		Region:      firstNonEmpty(m.Region, m.Zone),
		Type:        firstNonEmpty(m.Size, m.Type),
		Status:      m.State,
		Provider:    m.Provider,
		PublicIp:    m.PublicIp,
		PrivateIp:   m.PrivateIp,
		CreatedTime: m.CreatedTime,
		Image:       m.Image,
		Os:          m.Os,
	}
	slug := firstNonEmpty(m.Size, m.Type)
	slugVcpu, slugMemGB := parseSizeSlug(slug)
	spec, isGpu := gpuSpecOf(slug)
	if n, err := strconv.Atoi(strings.TrimSpace(m.CpuSize)); err == nil && n > 0 {
		v.Vcpu = &n
	} else if slugVcpu > 0 {
		v.Vcpu = &slugVcpu
	}
	if mem := normalizeMem(m.MemSize); mem != "" {
		v.Mem = mem
	} else if slugMemGB > 0 && !isGpu { // a GPU slug's gb is VRAM, never system RAM
		v.Mem = strconv.Itoa(slugMemGB) + " GB"
	}
	if isGpu {
		v.GPU = spec.model
	}
	return v
}

// gpusFromMachine expands a GPU machine into one gpuView per physical
// accelerator (perNode from the size slug — a gpu-h100x8 node genuinely holds 8
// H100s). A non-GPU machine yields nothing. No telemetry is invented.
func gpusFromMachine(m visorMachine) []gpuView {
	spec, ok := gpuSpecOf(firstNonEmpty(m.Size, m.Type))
	if !ok {
		return nil
	}
	name := firstNonEmpty(m.DisplayName, m.Name)
	region := firstNonEmpty(m.Region, m.Zone)
	out := make([]gpuView, 0, spec.perNode)
	for i := 0; i < spec.perNode; i++ {
		out = append(out, gpuView{
			ID:       m.Name + "#" + strconv.Itoa(i),
			Name:     name,
			Model:    spec.model,
			Region:   region,
			Status:   m.State,
			Location: region,
			Machine:  m.Name,
			Provider: m.Provider,
		})
	}
	return out
}

func toNodePoolView(p visorNodePool) nodePoolView {
	return nodePoolView{
		PoolID:    firstNonEmpty(p.PoolID, p.Name),
		Name:      p.Name,
		Size:      p.Size,
		Count:     p.Count,
		MinNodes:  p.MinNodes,
		MaxNodes:  p.MaxNodes,
		AutoScale: p.AutoScale,
	}
}

// clustersFromPools groups an org's node pools into clusters keyed by ClusterID
// (a pool with no ClusterID is its own single-pool cluster keyed by its name).
// nodeCount is the real sum of pool counts; nodeSize/status/createdAt are taken
// from the pools deterministically. Ordering is stable (sorted keys) so the list
// does not jitter between reads.
func clustersFromPools(pools []visorNodePool) []clusterView {
	byID := map[string]*clusterView{}
	var order []string
	for _, p := range pools {
		key := firstNonEmpty(p.ClusterID, p.Name)
		if key == "" {
			continue
		}
		cv := byID[key]
		if cv == nil {
			cv = &clusterView{
				DoksClusterID: p.ClusterID,
				DoClusterID:   p.ClusterID,
				Name:          key,
				NodePools:     []nodePoolView{},
			}
			byID[key] = cv
			order = append(order, key)
		}
		cv.NodePools = append(cv.NodePools, toNodePoolView(p))
		cv.NodeCount += p.Count
		if cv.NodeSize == "" {
			cv.NodeSize = p.Size
		}
		if cv.Status == "" {
			cv.Status = p.State
		}
		if cv.CreatedAt == "" || (p.CreatedTime != "" && p.CreatedTime < cv.CreatedAt) {
			cv.CreatedAt = p.CreatedTime
		}
	}
	sort.Strings(order)
	out := make([]clusterView, 0, len(order))
	for _, k := range order {
		cv := byID[k]
		if cv.Status == "" {
			cv.Status = "unknown"
		}
		out = append(out, *cv)
	}
	return out
}

// ---- CPU/mem size-slug parsing (PURE) ----

var (
	slugVcpuRE = regexp.MustCompile(`(\d+)vcpu`)
	slugGbRE   = regexp.MustCompile(`(\d+)gb`)
	memRE      = regexp.MustCompile(`^(\d+)\s*(gib|gb|g|mib|mb|m)?$`)
)

// parseSizeSlug extracts the vCPU and memory-GB integers embedded in a provider
// size slug (s-4vcpu-8gb -> 4, 8; g-8vcpu-32gb -> 8, 32). Each figure is 0 when
// the slug does not carry it — the caller decides whether to use it.
func parseSizeSlug(slug string) (vcpu int, memGB int) {
	s := strings.ToLower(strings.TrimSpace(slug))
	if mm := slugVcpuRE.FindStringSubmatch(s); mm != nil {
		vcpu, _ = strconv.Atoi(mm[1])
	}
	if mm := slugGbRE.FindStringSubmatch(s); mm != nil {
		memGB, _ = strconv.Atoi(mm[1])
	}
	return vcpu, memGB
}

// normalizeMem renders system RAM as "N GB" ONLY for values it can trust: explicit
// GB ("8gb"/"8 GB"/"8gib") pass through; explicit MB ("8192mb"/"8192 MB") convert
// with rounding; a bare integer is treated as MB when >= 1024, else as GB. Anything
// ambiguous or unparseable returns "" — an honest omission, never a fabricated number.
func normalizeMem(raw string) string {
	mm := memRE.FindStringSubmatch(strings.ToLower(strings.TrimSpace(raw)))
	if mm == nil {
		return ""
	}
	n, err := strconv.Atoi(mm[1])
	if err != nil || n <= 0 {
		return ""
	}
	switch mm[2] {
	case "gb", "gib", "g":
		return strconv.Itoa(n) + " GB"
	case "mb", "mib", "m":
		return strconv.Itoa((n+512)/1024) + " GB"
	default: // bare integer: MB when large enough to be a byte-count, else already GB
		if n >= 1024 {
			return strconv.Itoa((n+512)/1024) + " GB"
		}
		return strconv.Itoa(n) + " GB"
	}
}

// ---- GPU size-slug parsing (PURE, ported from console compute.ts gpuSpecOf) ----

type gpuSpec struct {
	model   string
	perNode int
}

// doGPUModels maps a DigitalOcean GPU-slug model token to its display name, the
// same table the console uses so a machine and a cluster render the identical
// accelerator name.
var doGPUModels = map[string]string{
	"h100": "H100", "h200": "H200", "l40s": "L40S", "l40": "L40",
	"a100": "A100", "a6000": "A6000", "a5000": "A5000", "a4000": "A4000",
	"rtx6000": "RTX 6000", "rtx4000": "RTX 4000", "mi300": "MI300X", "mi300x": "MI300X",
}

var doGPUSlugRE = regexp.MustCompile(`^gpu-(.+?)x(\d+)(?:-|$)`)

// gpuSpecOf parses a DigitalOcean GPU Droplet size slug (gpu-h100x8-640gb,
// gpu-l40sx1-48gb) into its accelerator model + GPUs-per-node. Returns ok=false
// for a non-GPU slug. Only DO GPU slugs are recognized — a machine whose size is
// not a GPU slug is honestly not a GPU (no guessing from CPU/mem).
func gpuSpecOf(slug string) (gpuSpec, bool) {
	s := strings.ToLower(strings.TrimSpace(slug))
	if s == "" {
		return gpuSpec{}, false
	}
	mm := doGPUSlugRE.FindStringSubmatch(s)
	if mm == nil {
		return gpuSpec{}, false
	}
	perNode, err := strconv.Atoi(mm[2])
	if err != nil || perNode <= 0 {
		return gpuSpec{}, false
	}
	return gpuSpec{model: titleGPUModel(mm[1]), perNode: perNode}, true
}

func titleGPUModel(token string) string {
	if m, ok := doGPUModels[token]; ok {
		return m
	}
	if strings.HasPrefix(token, "rtx") {
		return "RTX " + strings.ToUpper(strings.TrimPrefix(token, "rtx"))
	}
	return strings.ToUpper(token)
}
