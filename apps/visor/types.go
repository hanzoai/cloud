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
	"cmp"
	"maps"
	"regexp"
	"slices"
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

// visorNodes mirrors visor's controllers.Nodes — the Out of its TYPED
// GET /v1/k8s/nodes op, which answers {"nodes":[...]} with no envelope.
//
// Nodes is a slice rather than a pointer on purpose: nil means the field was NOT
// in the answer, which is how a caller tells "this org has no worker nodes" from
// "the Visor on the other end does not serve this op". Visor's op always writes
// the key, empty list included, so nil is only ever the second thing.
type visorNodes struct {
	Nodes []visorMachine `json:"nodes"`
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
	// ID addresses this machine on the /v1/visor/machines/:id routes: the
	// org-scoped NAME Visor keys a machine by, falling back to the provider id for
	// a machine that has no name. A BYO machine's is the id it dialed in under.
	ID string `json:"id"`
	// Name is the label to show a human — Visor's displayName, or the machine name
	// when it carries none. A BYO machine's is its hostname. It is not an address:
	// ID is what the routes take.
	Name string `json:"name"`
	// Region is the provider region slug ("sfo3"), or the zone when the provider
	// reports only that. "on-prem" for a BYO machine, which has no cloud region.
	Region string `json:"region,omitempty"`
	// Type is the provider SIZE SLUG the machine runs at ("s-2vcpu-4gb",
	// "gpu-h100x8-640gb") — the value a launch asks for, and what Vcpu/Mem/GPU are
	// read out of when the provider states them no other way. "byo-gpu" for a
	// dialed-in machine, which was never bought from a size catalog.
	Type string `json:"type,omitempty"`
	// Status is the lifecycle state in the PROVIDER's own words ("active",
	// "running", "off"), passed through rather than mapped onto a vocabulary of
	// ours. A BYO machine's is "online" or "offline", decided by whether its last
	// heartbeat is within 90s.
	Status string `json:"status,omitempty"`
	// Provider is the cloud that runs the machine ("digitalocean"), or "byo" for
	// one the operator dialed in with `hanzo link`.
	Provider string `json:"provider,omitempty"`
	// PublicIp is the internet-facing address the provider assigned. Empty while a
	// machine is still provisioning, and empty for a BYO machine — it dials out
	// from behind NAT, so no address is ever learned for it.
	PublicIp string `json:"publicIp,omitempty"`
	// PrivateIp is the address on the provider's own network, reachable from the
	// org's other machines in the same region. Empty on the same terms as PublicIp.
	PrivateIp string `json:"privateIp,omitempty"`
	// CreatedTime is when the machine came into being: the provider's own creation
	// timestamp for a Visor machine, passed through in whatever form it states it,
	// and for a BYO machine the RFC 3339 moment it first dialed in.
	CreatedTime string `json:"createdTime,omitempty"`
	// Vcpu is logical cores — the provider's own cpuSize when that is a clean
	// integer, else the count read out of the size slug (4 from "s-4vcpu-8gb").
	// ABSENT, never 0, when neither says. A BYO machine leaves it absent here; its
	// real core count is on GET /v1/visor/fleet/workers.
	Vcpu *int `json:"vcpu,omitempty"`
	// Mem is system RAM rendered for a human ("8 GB"), not a number to compute
	// with. Empty when the provider's figure is ambiguous, or when the only figure
	// available is a GPU slug's gb — that is VRAM, and reporting it as system RAM
	// would be a fabrication. A BYO machine's RAM is on /v1/visor/fleet/workers.
	Mem string `json:"mem,omitempty"`
	// GPU names the accelerators this machine holds ("H100", or "2× NVIDIA GB10"
	// for a BYO machine reporting a matched pair). Empty means the machine is not
	// a GPU machine — the size slug does not parse as one, or nvidia-smi found
	// nothing.
	GPU string `json:"gpu,omitempty"`
	// Image is the OS image the machine booted from, as the provider names it.
	Image string `json:"image,omitempty"`
	// Os is the operating system on the machine — Visor's record for a provisioned
	// one, the host's own report (linux, darwin, windows) for a BYO one.
	Os string `json:"os,omitempty"`
}

// gpuView is the shape console compute.ts normalizeGpu consumes — one row per
// physical accelerator. Model/region/status are REAL (from the GPU machine and
// its size slug); live telemetry is absent because Visor's machine object carries
// none, so the UI renders "—".
type gpuView struct {
	// ID is the card's address: its host machine's id, "#", and the card's ordinal
	// within that machine ("gpu-1#0"). Stable for as long as the machine is, and
	// the only id a single accelerator has — providers do not name cards.
	ID string `json:"id"`
	// Name is the HOST MACHINE's display name, not the card's — every card in a
	// gpu-h100x8 node repeats it. Model is what says which accelerator this is.
	Name string `json:"name,omitempty"`
	// Model is the accelerator: the model token read out of the size slug for a
	// Visor GPU droplet ("H100", "MI300X"), or the name nvidia-smi reported for a
	// BYO card ("NVIDIA GB10").
	Model string `json:"model,omitempty"`
	// Region is the host machine's provider region slug; "on-prem" for a BYO card.
	Region string `json:"region,omitempty"`
	// Status is the HOST MACHINE's lifecycle state, because nothing upstream
	// reports a card's own health. A card reads running because its machine does.
	Status string `json:"status,omitempty"`
	// Location is where the card physically sits, which for every source today is
	// the same value Region carries — the console renders it in its own column.
	Location string `json:"location,omitempty"`
	// Machine is the id of the machine holding this card, addressable as-is on
	// /v1/visor/machines/:id.
	Machine string `json:"machine,omitempty"`
	// Provider distinguishes a BYO accelerator ("byo") from a Visor-provisioned one
	// (the host machine's real provider). It is what tells a card the org owns from
	// a card the org rents.
	Provider string `json:"provider,omitempty"`
	// Memory is the card's VRAM as its own tooling reported it ("122880 MiB") — a
	// display string in the reporter's units, not a byte count. BYO cards carry it
	// (nvidia-smi); Visor's machine object states no VRAM, so a rented card leaves
	// it empty and the console renders "—" rather than a fabricated 0.
	Memory string `json:"memory,omitempty"`
}

// nodePoolView is the shape console platform.ts NodePool consumes.
type nodePoolView struct {
	// PoolID is the provider's id for the pool — the value the scale and delete
	// routes address it by. It falls back to the pool's name when the provider
	// answered without one, so it is always something the routes accept.
	PoolID string `json:"poolId,omitempty"`
	// Name is the pool's name as the provider knows it.
	Name string `json:"name,omitempty"`
	// Size is the provider size slug every node in the pool runs at
	// ("s-4vcpu-8gb", "gpu-h100x8-640gb"). One pool is one size — a mixed cluster
	// is several pools.
	Size string `json:"size,omitempty"`
	// Count is how many nodes the pool has right now. Always present, so 0 means a
	// pool that is genuinely empty rather than a figure the provider withheld.
	Count int `json:"count"`
	// MinNodes is the floor the autoscaler will not shrink the pool below. Read it
	// only with AutoScale set — the provider ignores it otherwise.
	MinNodes int `json:"minNodes,omitempty"`
	// MaxNodes is the ceiling the autoscaler will not grow the pool past, and so
	// the bound on what this pool can cost. Read it only with AutoScale set.
	MaxNodes int `json:"maxNodes,omitempty"`
	// AutoScale reports whether the provider's cluster autoscaler owns this pool's
	// size, moving Count between MinNodes and MaxNodes as workloads demand. False
	// means Count changes only when someone scales the pool.
	AutoScale bool `json:"autoScale,omitempty"`
}

// clusterView is the shape console platform.ts Cluster consumes. The authoritative
// node inventory is NodePools; nodeSize/nodeCount are the derived display fields
// the simple Clusters list uses (and the GPU derivation reads).
type clusterView struct {
	// DoksClusterID is the provider's own id for the cluster, and the value the
	// /v1/visor/k8s/clusters/:id routes take. Empty for a BYO cluster: an attached
	// kubeconfig was never provisioned, so there is no provider id to state.
	DoksClusterID string `json:"doksClusterId,omitempty"`
	// DoClusterID carries the SAME id as DoksClusterID. Both names exist because
	// the console's Cluster type reads either one; neither is a second identifier.
	DoClusterID string `json:"doClusterId,omitempty"`
	// Name is the cluster's name: the provider's for a managed cluster, and for a
	// BYO one the lower-cased fleet name it was attached under — which is also how
	// the detach route addresses it.
	Name string `json:"name"`
	// Region is the provider region slug for a managed cluster. A BYO cluster has
	// no region we can read, so it carries the free-form `provider` label the
	// attach named it with ("gke", "on-prem") instead.
	Region string `json:"region,omitempty"`
	// Status is the cluster's state: the provider's own word for a managed cluster
	// ("running", "provisioning"), "unknown" when the provider stated none, and
	// always "attached" for a BYO cluster — that one says the kubeconfig is on
	// file, not that the cluster is reachable this second.
	Status string `json:"status"`
	// NodePools is the authoritative node inventory — every pool, each with its own
	// size and count. It is empty in two cases that are not "no pools": a row from
	// the /v1/visor/k8s/clusters LIST, which is deliberately lightweight and whose
	// :id detail carries them, and a BYO cluster, whose pools were never read.
	NodePools []nodePoolView `json:"nodePools"`
	// NodeSize is a display convenience: the size slug of the FIRST pool. A cluster
	// mixing sizes has more than one, and NodePools is where they all are.
	NodeSize string `json:"nodeSize,omitempty"`
	// NodeCount is how many worker nodes the cluster has — the sum over its pools
	// for a managed cluster, and for a BYO one the node count read off the cluster
	// when it was attached.
	NodeCount int `json:"nodeCount"`
	// CreatedAt is when the cluster started existing: the earliest creation time
	// among its pools for a managed cluster, and for a BYO one the RFC 3339 moment
	// it was attached. Empty when the source states none.
	CreatedAt string `json:"createdAt,omitempty"`
	// Kind says which of the two kinds of cluster this row is, and there are only
	// two: "managed" — Visor provisioned it and Hanzo's account pays the provider —
	// or "byo", an existing cluster the org attached by kubeconfig.
	Kind string `json:"kind,omitempty"`
	// NvidiaGPU is how many NVIDIA accelerators the cluster's nodes advertise, the
	// sum of `nvidia.com/gpu` allocatable across them. BYO only, and counted ONCE
	// when the cluster was attached — it is an inventory, not live capacity.
	NvidiaGPU int `json:"nvidiaGpu,omitempty"`
	// AmdGPU is the same count for `amd.com/gpu`: AMD accelerators across the BYO
	// cluster's nodes, as of the attach.
	AmdGPU int `json:"amdGpu,omitempty"`
}

// ---- mapping (PURE) ----

// toMachineView maps a Visor machine to the console view. vcpu prefers a clean
// integer CpuSize and otherwise recovers the count from a provider size slug
// (s-4vcpu-8gb) — still no fabricated core count, just an honest read of the slug.
// mem is the trustworthy MemSize, or a NON-GPU slug's gb figure, else empty — a
// GPU slug's gb is VRAM, never system RAM, so it is never surfaced as mem. gpu is
// filled only when the size slug parses as a GPU accelerator.
func toMachineView(m visorMachine) machineView {
	v := machineView{
		ID:          cmp.Or(strings.TrimSpace(m.Name), m.Id),
		Name:        cmp.Or(m.DisplayName, m.Name),
		Region:      cmp.Or(m.Region, m.Zone),
		Type:        cmp.Or(strings.TrimSpace(m.Size), m.Type),
		Status:      m.State,
		Provider:    m.Provider,
		PublicIp:    m.PublicIp,
		PrivateIp:   m.PrivateIp,
		CreatedTime: m.CreatedTime,
		Image:       m.Image,
		Os:          m.Os,
	}
	slug := cmp.Or(strings.TrimSpace(m.Size), m.Type)
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
	spec, ok := gpuSpecOf(cmp.Or(strings.TrimSpace(m.Size), m.Type))
	if !ok {
		return nil
	}
	name := cmp.Or(m.DisplayName, m.Name)
	region := cmp.Or(m.Region, m.Zone)
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
		PoolID:    cmp.Or(strings.TrimSpace(p.PoolID), strings.TrimSpace(p.Name)),
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
	for _, p := range pools {
		key := cmp.Or(strings.TrimSpace(p.ClusterID), strings.TrimSpace(p.Name))
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
	order := slices.Sorted(maps.Keys(byID))
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
	if after, ok := strings.CutPrefix(token, "rtx"); ok {
		return "RTX " + strings.ToUpper(after)
	}
	return strings.ToUpper(token)
}
