// fleet.go — BYO ("bring your own") compute: the operator's OWN machines that
// dialed IN via `hanzo gpu connect`, as opposed to the DOKS/DigitalOcean machines
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
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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
	MemoryTotal string `json:"memoryTotal,omitempty"` // VRAM, e.g. "122880 MiB"
}

// engineAdvertisement is a hanzo-engine model server a BYO worker runs on its node
// (advertised by `hanzo gpu connect --serve-engine`). hanzo-engine serves the OpenAI
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
	GPUs          []byoGPU `json:"gpus"`
	LastHeartbeat string   `json:"lastHeartbeat,omitempty"`
	FirstSeen     string   `json:"firstSeen,omitempty"`
	Os            string   `json:"os,omitempty"`
	Version       string   `json:"version,omitempty"`
	JobQueue      string   `json:"jobQueue,omitempty"`
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
	Version      string               `json:"version"`
	JobQueue     string               `json:"jobQueue"`
	GPUs         []byoGPU             `json:"gpus"`
	Capabilities []string             `json:"capabilities,omitempty"`
	Engine       *engineAdvertisement `json:"engine,omitempty"`
}

// byoWorkers reads the org's BYO fleet from the in-process tasks engine and
// normalizes each presence activity. Fail-soft: a nil engine (not yet wired) or a
// read error yields an empty list, never an error — a BYO read must never break the
// Visor-backed machine/gpu listing it augments. Terminal (disconnected) presence
// records are excluded so a `hanzo gpu disconnect` removes the row.
func byoWorkers(org string) []byoWorker {
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil
	}
	acts, err := eng.ActivitiesForOrg(org, fleetNamespace)
	if err != nil {
		return nil
	}
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
			GPUs:          reg.GPUs,
			LastHeartbeat: a.LastHeartbeatTime,
			FirstSeen:     a.StartTime,
			Os:            reg.Os,
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

// listFleetWorkers serves GET /v1/fleet/workers — the raw BYO inventory for the
// caller's tenant. The Machines and GPUs pages fold the same data in via the unions
// below; this is the canonical list a fleet-specific view (or the CLI's `status`)
// reads.
func (s *svc) listFleetWorkers(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	return c.JSON(http.StatusOK, map[string]any{"workers": byoWorkers(org)})
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
