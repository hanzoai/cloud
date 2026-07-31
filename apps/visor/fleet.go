// fleet.go — BYO ("bring your own") compute: the operator's OWN machines that
// dialed IN via `hanzo link`, as opposed to the DOKS/DigitalOcean machines
// Visor provisions. A BYO worker is an outbound agent behind NAT: it can't be
// listed by Visor (Visor never provisioned it), so its presence lives as a
// heartbeating standalone activity in the org's `fleet` namespace of the ONE
// in-process tasks engine (cloud.EmbeddedTasks). This file reads that registry and
// folds it into the SAME machineView / gpuView the console already renders, tagged
// provider="byo", so the existing Machines and GPUs pages light up for free — no
// parallel UI. It also serves the raw list at GET /v1/fleet/workers.
//
// Registration is written by the CLI over the public tasks surface
// (POST /v1/tasks/namespaces/fleet/activities + heartbeat) — this subsystem only
// READS, and only ever the caller's own tenant (principal.Org → org shard).

package visor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/samples"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	"github.com/zap-proto/zip"
)

// fleetNamespace is the tasks namespace a BYO worker registers its presence in.
// gpu-jobs (the work queue it claims from) is a SEPARATE namespace — presence and
// work never mix.
const fleetNamespace = "fleet"

// byoLiveWindow bounds heartbeat freshness: a worker whose last heartbeat is older
// is reported offline. ~1.5× the CLI's default 60s heartbeat so one missed beat
// does not flap it offline.
const byoLiveWindow = 90 * time.Second

// byoGPU is one accelerator reported by nvidia-smi on the connecting machine.
type byoGPU struct {
	Name        string `json:"name"`
	MemoryTotal string `json:"memoryTotal,omitempty"` // VRAM (or unified pool), e.g. "122880 MiB"
	Arch        string `json:"arch,omitempty"`        // native target, e.g. "gfx1151"
	Unified     bool   `json:"unified,omitempty"`     // unified CPU/GPU memory pool (APU / SoC)
}

// engineAdvertisement is a hanzo-engine model server a BYO worker runs on its node
// (advertised by `hanzo link --serve-engine`). hanzo-engine serves the OpenAI
// AND Anthropic HTTP APIs from one port, so the gateway can route model calls to this
// GPU as an OpenAI-compatible provider. Surfaced verbatim on GET /v1/fleet/workers.
type engineAdvertisement struct {
	URL    string   `json:"url"`
	APIs   []string `json:"apis,omitempty"`   // ["openai","anthropic"]
	Models []string `json:"models,omitempty"` // ids from the node's GET /v1/models
	Status string   `json:"status,omitempty"` // "ready" | "unreachable"
}

// byoWorker is a connected BYO machine, normalized from its `fleet` presence
// activity. It is the raw shape GET /v1/fleet/workers returns and the source the
// machine/gpu unions map from.
type byoWorker struct {
	ID            string   `json:"id"`
	Hostname      string   `json:"hostname"`
	Provider      string   `json:"provider"` // always "byo"
	Location      string   `json:"location"` // "on-prem" (BYO has no cloud region)
	Status        string   `json:"status"`   // online | offline
	Rocm          string   `json:"rocm,omitempty"`
	Hip           string   `json:"hip,omitempty"`
	Cuda          string   `json:"cuda,omitempty"`
	Driver        string   `json:"driver,omitempty"`
	GPUs          []byoGPU `json:"gpus"`
	LastHeartbeat string   `json:"lastHeartbeat,omitempty"`
	FirstSeen     string   `json:"firstSeen,omitempty"`
	Os            string   `json:"os,omitempty"`
	// Arch/CPUs/Memory are the connecting host's static CPU spec, mirrored from the
	// registration: Arch is runtime.GOARCH (amd64 | arm64), Memory is total RAM in
	// BYTES — the same fields a code-linked run-target carries, so the /v1/fleet
	// board renders a linked node's arch + cores + RAM like any other unit.
	Arch     string `json:"arch,omitempty"`
	CPUs     int    `json:"cpus,omitempty"`
	CPUModel string `json:"cpuModel,omitempty"`
	Memory   int64  `json:"memory,omitempty"`
	Version  string `json:"version,omitempty"`
	JobQueue string `json:"jobQueue,omitempty"`
	// Capabilities the worker advertises ("studio.render", "engine.serve"); Engine
	// is present when it runs a hanzo-engine model server. Both additive + omitempty.
	Capabilities []string             `json:"capabilities,omitempty"`
	Engine       *engineAdvertisement `json:"engine,omitempty"`
}

// fleetRegistration is the JSON the CLI stores as the presence activity's Input.
// Mirror of the CLI's `registration` (cloud/cli/gpu.go) — extend both in lockstep.
type fleetRegistration struct {
	Hostname     string               `json:"hostname"`
	Os           string               `json:"os"`
	Arch         string               `json:"arch,omitempty"`
	CPUs         int                  `json:"cpus,omitempty"`
	Memory       int64                `json:"memory,omitempty"`
	CPUModel     string               `json:"cpuModel,omitempty"`
	Version      string               `json:"version"`
	JobQueue     string               `json:"jobQueue"`
	Rocm         string               `json:"rocm,omitempty"`
	Hip          string               `json:"hip,omitempty"`
	Cuda         string               `json:"cuda,omitempty"`
	Driver       string               `json:"driver,omitempty"`
	GPUs         []byoGPU             `json:"gpus"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Engine       *engineAdvertisement `json:"engine,omitempty"`
}

// byoWorkers reads the org's BYO fleet from the in-process tasks engine and
// normalizes each presence activity. Fail-soft: a nil engine (not yet wired) or a
// read error yields an empty list, never an error — a BYO read must never break the
// Visor-backed machine/gpu listing it augments. Terminal (disconnected) presence
// records are excluded so a `hanzo unlink` removes the row.
func byoWorkers(org string) []byoWorker {
	// ALL pages, not the first 100: an online worker must never be truncated away
	// (dropping it from Machines/GPUs/status) because terminal presence rows crowd
	// the hash-ordered first page. allActivitiesForOrg is fail-soft (nil on no engine).
	acts := allActivitiesForOrg(org, fleetNamespace)
	now := time.Now().UTC()
	out := make([]byoWorker, 0, len(acts))
	for _, a := range acts {
		switch a.Status {
		case "ACTIVITY_TASK_STATE_COMPLETED", "ACTIVITY_TASK_STATE_FAILED", "ACTIVITY_TASK_STATE_CANCELED":
			continue // disconnected — drop from the fleet
		}
		var reg fleetRegistration
		if b, err := json.Marshal(a.Input); err == nil {
			_ = json.Unmarshal(b, &reg)
		}
		host := firstNonEmpty(reg.Hostname, a.Execution.WorkflowId)
		out = append(out, byoWorker{
			ID:            a.Execution.WorkflowId,
			Hostname:      host,
			Provider:      "byo",
			Location:      "on-prem",
			Status:        byoStatus(a.LastHeartbeatTime, now),
			Rocm:          reg.Rocm,
			Hip:           reg.Hip,
			Cuda:          reg.Cuda,
			Driver:        reg.Driver,
			GPUs:          reg.GPUs,
			LastHeartbeat: a.LastHeartbeatTime,
			FirstSeen:     a.StartTime,
			Os:            reg.Os,
			Arch:          reg.Arch,
			CPUs:          reg.CPUs,
			CPUModel:      reg.CPUModel,
			Memory:        reg.Memory,
			Version:       reg.Version,
			JobQueue:      reg.JobQueue,
			Capabilities:  reg.Capabilities,
			Engine:        reg.Engine,
		})
	}
	return out
}

// byoStatus is "online" when the last heartbeat is within byoLiveWindow, else
// "offline". A never-heartbeated worker (just registered) is offline until its
// first beat lands.
func byoStatus(lastHeartbeat string, now time.Time) string {
	if lastHeartbeat == "" {
		return "offline"
	}
	t, err := time.Parse(time.RFC3339, lastHeartbeat)
	if err != nil {
		return "offline"
	}
	if now.Sub(t.UTC()) <= byoLiveWindow {
		return "online"
	}
	return "offline"
}

// workerList is the raw BYO inventory: the machines that dialed IN, as they
// reported themselves.
type workerList struct {
	// Workers is one row per connected BYO machine, each carrying the host's own
	// report (GPUs, driver versions, capabilities) rather than a normalized view.
	Workers []byoWorker `json:"workers"`
}

// listFleetWorkers returns the caller org's BYO machines — the ones that dialed in
// via `hanzo link` — with everything each host reported about itself. The Machines
// and GPUs pages fold the same data into their normalized shapes; this is the
// canonical raw list a fleet view (or the CLI's `status`) reads.
//
// Response: {"workers":[{"id":"spark","hostname":"spark","provider":"byo","location":"on-prem","status":"online","gpus":[{"name":"NVIDIA GB10","memoryTotal":"122880 MiB"}],"arch":"arm64","cpus":20}]}
func (o ops) listFleetWorkers(ctx context.Context, _ *noArgs) (*workerList, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	return &workerList{Workers: byoWorkers(org)}, nil
}

// ---- unions into the existing console shapes ----

// byoMachineView maps a BYO worker to the machineView the console's Machines page
// renders. provider="byo" and region="on-prem" distinguish it from a Visor machine;
// the GPU summary lists the accelerators (e.g. "NVIDIA GB10" or "2× NVIDIA GB10").
func byoMachineView(w byoWorker) machineView {
	return machineView{
		ID:          w.ID,
		Name:        w.Hostname,
		Region:      w.Location,
		Type:        "byo-gpu",
		Status:      w.Status,
		Provider:    "byo",
		CreatedTime: w.FirstSeen,
		GPU:         byoGPUSummary(w.GPUs),
		Os:          w.Os,
	}
}

// byoGPUViews expands a BYO worker into one gpuView per accelerator for the GPUs
// page, carrying the real model name + VRAM nvidia-smi reported.
func byoGPUViews(w byoWorker) []gpuView {
	out := make([]gpuView, 0, len(w.GPUs))
	for i, g := range w.GPUs {
		out = append(out, gpuView{
			ID:       w.ID + "#" + strconv.Itoa(i),
			Name:     w.Hostname,
			Model:    g.Name,
			Region:   w.Location,
			Status:   w.Status,
			Location: w.Location,
			Machine:  w.ID,
			Provider: "byo",
			Memory:   g.MemoryTotal,
		})
	}
	return out
}

// byoGPUSummary renders the accelerator set as one line for the machineView.GPU
// field: "NVIDIA GB10" for a single card, "2× NVIDIA GB10" for a homogeneous pair,
// else a comma-joined list. Empty when the worker reported no GPU (CPU-only).
func byoGPUSummary(gpus []byoGPU) string {
	if len(gpus) == 0 {
		return ""
	}
	same := true
	for _, g := range gpus {
		if g.Name != gpus[0].Name {
			same = false
			break
		}
	}
	if same {
		if len(gpus) == 1 {
			return gpus[0].Name
		}
		return strconv.Itoa(len(gpus)) + "× " + gpus[0].Name
	}
	names := make([]string, len(gpus))
	for i, g := range gpus {
		names[i] = g.Name
	}
	return strings.Join(names, ", ")
}

// ---- gpu-jobs queue: the org's per-GPU render queue, visible + manageable ----
//
// The render queue lives in the tasks namespace `gpu-jobs`. A job's TARGET is the
// taskQueue VALUE: "gpu:<node>" is claimed only by that node (cli/gpu.go claims its
// own lane first), the shared value "gpu-jobs" is the any-GPU broadcast. This reads
// that queue org-scoped (the SAME ActivitiesForOrg primitive byoWorkers uses) and
// serves it on GET /v1/fleet/jobs, with POST /v1/fleet/jobs/:id/cancel to manage it.

// jobsNamespace is the tasks namespace the render queue lives in. gpuQueuePrefix
// mirrors the CLI/dispatcher convention — ONE way to name a per-GPU lane.
const (
	jobsNamespace  = "gpu-jobs"
	gpuQueuePrefix = "gpu:"
)

// gpuJob is one row of the gpu-jobs queue, normalized for the console + CLI. The
// full ComfyUI graph (activity Input) is intentionally OMITTED — the list is a queue
// view, not a render spec; `label` carries the cheap SaveImage prefix, and the tasks
// describe endpoint serves the graph. `gpu` is the TARGET lane, `worker` the
// claiming node — they differ (a shared-lane job has no target but has a claimant
// once picked up), and the UI needs both.
type gpuJob struct {
	ID            string `json:"id"`
	RunID         string `json:"runId,omitempty"`
	Type          string `json:"type,omitempty"`
	Status        string `json:"status"` // queued|running|completed|failed|canceled
	GPU           string `json:"gpu,omitempty"`
	Worker        string `json:"worker,omitempty"`
	Label         string `json:"label,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	StartTime     string `json:"startTime,omitempty"`
	CloseTime     string `json:"closeTime,omitempty"`
	LastHeartbeat string `json:"lastHeartbeat,omitempty"`
	LeaseExpiry   string `json:"leaseExpiry,omitempty"`
	FailureCause  string `json:"failureCause,omitempty"`
}

// gpuTarget parses the GPU a job targets from its taskQueue: "gpu:<node>" → "<node>";
// the shared lane (or any non-prefixed queue) → "".
func gpuTarget(taskQueue string) string {
	if id, ok := strings.CutPrefix(taskQueue, gpuQueuePrefix); ok {
		return id
	}
	return ""
}

// normalizeStatus maps the engine's ACTIVITY_TASK_STATE_* enum to the console's
// lifecycle vocabulary. An unknown state passes through lower-cased (honest — never
// silently "queued").
func normalizeStatus(s string) string {
	switch s {
	case "ACTIVITY_TASK_STATE_SCHEDULED":
		return "queued"
	case "ACTIVITY_TASK_STATE_STARTED":
		return "running"
	case "ACTIVITY_TASK_STATE_COMPLETED":
		return "completed"
	case "ACTIVITY_TASK_STATE_FAILED":
		return "failed"
	case "ACTIVITY_TASK_STATE_CANCELED":
		return "canceled"
	}
	return strings.ToLower(strings.TrimPrefix(s, "ACTIVITY_TASK_STATE_"))
}

// renderLabel extracts the cheap human label for a studio.render job — the
// SaveImage/SaveVideo/SaveWEBM filename_prefix from the ComfyUI graph in the activity
// Input. Fully defensive: any shape it does not recognize yields "" (the list never
// decodes or echoes the full graph).
func renderLabel(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	prompt, ok := m["prompt"].(map[string]any)
	if !ok {
		return ""
	}
	for _, n := range prompt {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		switch node["class_type"] {
		case "SaveImage", "SaveVideo", "SaveWEBM":
			if inp, ok := node["inputs"].(map[string]any); ok {
				if p, ok := inp["filename_prefix"].(string); ok && strings.TrimSpace(p) != "" {
					return p
				}
			}
		}
	}
	return ""
}

// Reading the queue at scale: the engine's single-page ActivitiesForOrg caps at 100
// rows in hash order, so a busy org past ~100 lifetime activities sees a RANDOM subset
// — live jobs hidden, busy GPUs read idle, online workers dropped. These bounds drive
// a complete cursor-walk (ActivitiesPageForOrg) plus a recency-sort + terminal cap so
// the surface is correct AND bounded regardless of lifetime volume.
const (
	activityPageSize = 10000 // one wide page normally covers the whole shard in a single scan
	maxActivityPages = 50    // safety valve (→ up to 500k rows) so a runaway can't loop forever
	maxTerminalJobs  = 50    // terminal history kept in the queue view; ALL live jobs are always kept
)

// allActivitiesForOrg cursor-walks the org's activities in ns to COMPLETION, defeating
// the 100-row truncation. Fail-soft: a nil engine or a read error yields what it has
// so far (possibly nil), never an error — a queue/fleet read must never 500 the board.
func allActivitiesForOrg(org, ns string) []tasks.StandaloneActivity {
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil
	}
	var out []tasks.StandaloneActivity
	cursor := ""
	for i := 0; i < maxActivityPages; i++ {
		page, next, err := eng.ActivitiesPageForOrg(org, ns, cursor, activityPageSize)
		if err != nil {
			return out
		}
		out = append(out, page...)
		if next == "" || len(page) == 0 {
			break
		}
		cursor = next
	}
	return out
}

// leaseElapsed reports whether an RFC3339 lease expiry is already in the past.
func leaseElapsed(leaseExpiry string, now time.Time) bool {
	if leaseExpiry == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, leaseExpiry)
	return err == nil && now.After(exp)
}

// toGPUJob projects one activity onto the console's gpuJob, normalizing status and
// flagging a STALLED run: a job STARTED whose lease elapsed (its worker died) but which
// the reaper has not yet reclaimed — otherwise it reads "running" forever. now is
// shared across the page so every row is judged against one clock.
func toGPUJob(a tasks.StandaloneActivity, now time.Time) gpuJob {
	j := gpuJob{
		ID:            a.Execution.WorkflowId,
		RunID:         a.Execution.RunId,
		Type:          a.Type.Name,
		Status:        normalizeStatus(a.Status),
		GPU:           gpuTarget(a.TaskQueue),
		Worker:        a.Identity,
		Label:         renderLabel(a.Input),
		Attempt:       a.Attempt,
		StartTime:     a.StartTime,
		CloseTime:     a.CloseTime,
		LastHeartbeat: a.LastHeartbeatTime,
		LeaseExpiry:   a.LeaseExpiry,
		FailureCause:  a.FailureCause,
	}
	if j.Status == "running" && leaseElapsed(a.LeaseExpiry, now) {
		j.Status = "stalled"
	}
	return j
}

// jobRecency is a job's most-recent activity timestamp for recency sorting. RFC3339
// sorts lexically = chronologically (the engine writes UTC Z), so the max string is
// the latest moment the job did anything.
func jobRecency(j gpuJob) string {
	r := j.StartTime
	if j.LastHeartbeat > r {
		r = j.LastHeartbeat
	}
	if j.CloseTime > r {
		r = j.CloseTime
	}
	return r
}

// isTerminalJob reports whether a normalized status is a finished lifecycle state.
func isTerminalJob(status string) bool {
	switch status {
	case "completed", "failed", "canceled":
		return true
	}
	return false
}

// gpuJobs reads the org's ENTIRE gpu-jobs queue (all pages), most-recent-first, keeping
// every LIVE job (queued/running/stalled) and bounding terminal history to the most
// recent maxTerminalJobs — so a busy org's live work is never hidden by truncation and
// the view stays bounded. The SAME cursor-walk backs byoWorkers, so jobs and workers
// can never disagree about the tenant. Fail-soft throughout.
func gpuJobs(org string) []gpuJob {
	acts := allActivitiesForOrg(org, jobsNamespace)
	now := time.Now().UTC()
	all := make([]gpuJob, 0, len(acts))
	for _, a := range acts {
		all = append(all, toGPUJob(a, now))
	}
	return orderAndBoundJobs(all)
}

// orderAndBoundJobs recency-sorts (newest first) and caps terminal history to
// maxTerminalJobs while keeping EVERY live job (queued/running/stalled) — the
// correctness-plus-bound the queue view needs past 100 lifetime rows. Pure, so it is
// unit-tested directly with >100 rows.
func orderAndBoundJobs(all []gpuJob) []gpuJob {
	sort.SliceStable(all, func(i, j int) bool { return jobRecency(all[i]) > jobRecency(all[j]) })
	out := make([]gpuJob, 0, len(all))
	terminal := 0
	for _, j := range all { // recency-desc: the first maxTerminalJobs terminal rows are the newest
		if isTerminalJob(j.Status) {
			if terminal >= maxTerminalJobs {
				continue
			}
			terminal++
		}
		out = append(out, j)
	}
	return out
}

// filterGPUJobs narrows jobs to one GPU's queue and/or a lifecycle status. Empty
// gpu/status match all. "spark's queue" is job.gpu==spark (targeted at it) OR
// job.worker==spark (it is running the job), so a shared-lane job spark picked up
// still shows under ?gpu=spark. gpu=="shared" (or the namespace name) selects the
// any-GPU lane: no target and no claimant yet. status matches the normalized form.
func filterGPUJobs(jobs []gpuJob, gpu, status string) []gpuJob {
	// Node ids are lower-case (sanitized hostnames), so match the filter
	// case-insensitively — ?gpu=Spark must find spark, like ?status= is normalized.
	gpu = strings.ToLower(strings.TrimSpace(gpu))
	status = strings.ToLower(strings.TrimSpace(status))
	sharedOnly := gpu == "shared" || gpu == jobsNamespace
	out := make([]gpuJob, 0, len(jobs))
	for _, j := range jobs {
		switch {
		case sharedOnly:
			if j.GPU != "" || j.Worker != "" {
				continue
			}
		case gpu != "" && j.GPU != gpu && j.Worker != gpu:
			continue
		}
		if status != "" && j.Status != status {
			continue
		}
		out = append(out, j)
	}
	return out
}

// jobCount is a node's live queue depth: queued (waiting) + running (in flight).
type jobCount struct{ queued, running int }

// gpuJobCounts attributes each job to a GPU node: a QUEUED job to its target lane
// (only a targeted job belongs to a specific GPU — a shared-lane queued job could go
// to any), a RUNNING job to the node actually claiming it. Feeds the board's
// per-unit Queued/Running.
func gpuJobCounts(jobs []gpuJob) map[string]jobCount {
	out := map[string]jobCount{}
	for _, j := range jobs {
		switch j.Status {
		case "queued":
			if j.GPU != "" {
				c := out[j.GPU]
				c.queued++
				out[j.GPU] = c
			}
		case "running":
			if j.Worker != "" {
				c := out[j.Worker]
				c.running++
				out[j.Worker] = c
			}
		}
	}
	return out
}

// jobFilter narrows the queue WITHIN the caller's tenant. Both are optional; the
// org is never one of them.
type jobFilter struct {
	// GPU selects one node's lane: jobs TARGETED at it (gpu:<node>) or CLAIMED by
	// it. The literal "shared" selects the any-GPU lane — no target, no claimant.
	// Matched case-insensitively.
	GPU string `json:"gpu"`
	// Status selects one lifecycle state: queued, running, stalled, completed,
	// failed or canceled.
	Status string `json:"status"`
}

// jobList is the org's render queue.
type jobList struct {
	// Jobs is the queue, most-recent-first. Every LIVE job is present; terminal
	// history is capped, so a busy org's running work is never crowded out.
	Jobs []gpuJob `json:"jobs"`
}

// listFleetJobs returns the caller org's gpu-jobs render queue, each row tagged with
// the GPU it targets (empty = the shared any-GPU lane) and the node claiming it,
// optionally narrowed to one GPU's queue and/or one status.
//
// A job whose worker died — STARTED with an elapsed lease and not yet reclaimed —
// reads "stalled", not "running". Fail-soft: an unavailable tasks engine yields an
// empty queue rather than an error.
//
// Response: {"jobs":[{"id":"job-1","runId":"job-1","type":"studio.render","status":"running","gpu":"spark","worker":"spark","label":"hero","attempt":1}]}
func (o ops) listFleetJobs(ctx context.Context, in *jobFilter) (*jobList, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	return &jobList{Jobs: filterGPUJobs(gpuJobs(org), in.GPU, in.Status)}, nil
}

// jobCancel identifies the render to cancel and why.
type jobCancel struct {
	// ID is the job (activity) id, from the URL path.
	ID string `json:"id"`
	// Run is the run id; empty defaults to the job id, which is what the dispatcher
	// sets (runId == activityId == prompt_id), so the common case sends no body.
	Run string `json:"run"`
	// Reason is recorded on the cancellation; empty records "canceled from console".
	Reason string `json:"reason"`
}

// jobCanceled names the render a cancel stopped.
type jobCanceled struct {
	// Canceled is the job id that was canceled.
	Canceled string `json:"canceled"`
	// Run is the run id the cancel was applied to.
	Run string `json:"run"`
}

// cancelFleetJob cancels a queued or running render in the caller's org. The engine
// cancel is org-scoped, so a tenant can only ever cancel its OWN job: a job in
// another tenant's shard is 404, exactly like one that never existed. An
// already-finished job is 409.
//
// Example: {"reason":"superseded"}
// Response: {"canceled":"job-1","run":"job-1"}
func (o ops) cancelFleetJob(ctx context.Context, in *jobCancel) (*jobCanceled, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("job id required")
	}
	run := firstNonEmpty(strings.TrimSpace(in.Run), id)
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tasks engine not ready")
	}
	who := firstNonEmpty(c.Header("X-User-Id"), org)
	if err := eng.CancelActivityForOrg(org, jobsNamespace, id, run, firstNonEmpty(in.Reason, "canceled from console"), who); err != nil {
		switch cancelErrStatus(err) {
		case http.StatusNotFound:
			return nil, zip.ErrNotFound("job not found")
		case http.StatusConflict:
			return nil, zip.Errorf(http.StatusConflict, "job already finished")
		default:
			return nil, zip.Errorf(http.StatusBadGateway, "cancel: %v", err)
		}
	}
	return &jobCanceled{Canceled: id, Run: run}, nil
}

// cancelErrStatus maps a CancelActivityForOrg result to the client HTTP status: a
// missing job (incl. one in another tenant's shard) is 404, an already-finished one
// 409, a live cancel 200, anything else a 502. The tasks engine's own test proves it
// returns these error shapes; this maps them, so the two together cover the full path.
func cancelErrStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound
	case strings.Contains(err.Error(), "terminal"):
		return http.StatusConflict
	default:
		return http.StatusBadGateway
	}
}

// ---- fleet utilization ingest ----

const sampleIngestTimeout = 10 * time.Second

// sampleIngest is a BYO worker's self-reported utilization (POST /v1/fleet/samples).
// Only the metrics a worker can measure locally; Org/Source/Kind are
// server-authoritative — a worker cannot claim to be another tenant or source.
type sampleIngest struct {
	// Unit is the reporting node's own id — the same id it registered under, and
	// the key the board joins this series onto. Required.
	Unit string `json:"unit"`
	// Host is the node's hostname, for display.
	Host string `json:"host"`
	// GPUUtil is accelerator utilization as a fraction 0..1; the warehouse clamps
	// anything outside that.
	GPUUtil float64 `json:"gpuUtil"`
	// GPUs is how many accelerators the reading covers, GPUModel the representative
	// model name.
	GPUs     int    `json:"gpus"`
	GPUModel string `json:"gpuModel"`
	// MemUsed and MemFree are host memory in bytes.
	MemUsed int64 `json:"memUsed"`
	MemFree int64 `json:"memFree"`
}

// sampleAccepted acknowledges an ingested reading.
type sampleAccepted struct {
	// Recorded is always true: the response is an acknowledgement, and the
	// warehouse write is detached, so it reports acceptance, not durability.
	Recorded bool `json:"recorded"`
}

// sample builds the warehouse Sample for org from the worker's report. Source/Kind
// are fixed (byo/worker); the warehouse sanitizes (clamps util to 0..1) and validates
// (requires unit) on Record.
func (r sampleIngest) sample(org string) (samples.Sample, error) {
	unit := strings.TrimSpace(r.Unit)
	if unit == "" {
		return samples.Sample{}, fmt.Errorf("'unit' is required")
	}
	return samples.Sample{
		Org: org, Source: samples.SourceBYO, Kind: samples.KindWorker,
		Unit: unit, Host: strings.TrimSpace(r.Host), At: time.Now().UTC(),
		GPUUtil: r.GPUUtil, GPUs: r.GPUs, GPUModel: r.GPUModel,
		MemUsed: r.MemUsed, MemFree: r.MemFree,
	}, nil
}

// ingestSample records a BYO worker's live GPU utilization into the SAME series the
// fleet board overlays. The org is the validated principal and source/kind are fixed
// server-side, so a worker names only its own metrics — never another tenant or
// another source. Answers 202: the warehouse write is DETACHED (its own bounded
// context, never in the response path), so a slow or absent warehouse cannot stall a
// heartbeat.
//
// Example: {"unit":"spark","host":"spark","gpuUtil":0.42,"gpus":1,"gpuModel":"GB10","memUsed":100,"memFree":200}
// Response: {"recorded":true}
func (o ops) ingestSample(ctx context.Context, in *sampleIngest) (*sampleAccepted, error) {
	_, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	smp, err := in.sample(org)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	go func() {
		wctx, cancel := context.WithTimeout(context.Background(), sampleIngestTimeout)
		defer cancel()
		if err := samples.Record(wctx, smp); err != nil {
			o.Log.Warn("byo fleet sample ingest failed", "org", org, "unit", smp.Unit, "err", err)
		}
	}()
	cloud.Accepted(ctx)
	return &sampleAccepted{Recorded: true}, nil
}
