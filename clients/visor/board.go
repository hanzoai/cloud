// board.go — GET /v1/fleet: the org's compute, from every source, on ONE board,
// each unit carrying its latest utilization.
//
// The fleet is already visible, just never in one place: Visor's machines
// (/v1/machines), the BYO workers that dialed in (/v1/fleet/workers), the BYO
// clusters (/v1/clusters) and the agent run-targets (/v1/agents/targets) each
// answer for their own plane. This unions them behind the tenant's ONE question —
// "what compute do I have, and how hot is it?" — and overlays the utilization
// series (clients/samples) that no source used to keep.
//
//	GET /v1/fleet          the org's units + their latest sample  -> {units:[fleetUnit]}
//	GET /v1/fleet/samples  one unit's / the org's time series      -> {samples:[sampleView]}
//	GET /v1/fleet/workers  the raw BYO inventory (fleet.go)        -> unchanged
//
// It lives in visor because visor already owns /v1/fleet/workers and the compute
// surface — this is that surface completed, not a rival face.
//
// FAIL-SOFT BY SOURCE. Every source is folded in independently and a broken one
// contributes an empty slice, never an error: a wedged Visor, an unmounted agents
// subsystem or an absent warehouse each cost the board THAT source's rows and
// nothing else. A tenant's own dialed-in GPU must never disappear because an
// upstream they do not use is down. The board 500s for exactly one reason: no
// validated tenant.
//
// ISOLATION: principal.Org is the ONLY tenant key, taken from the validated IAM
// claim (never a client field) and passed to each source's own org-scoped read.
package visor

import (
	"errors"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/samples"
	"github.com/zap-proto/zip"
)

// ---- the published board shapes ----

// fleetSpec is a unit's static capability, normalized across sources. GPUs is a
// COUNT plus the representative model — the same shape the sample row carries
// (gpus UInt8 + gpu_model String), so the board and the series describe a machine
// the same way. Full per-accelerator detail stays on each source's own face
// (/v1/agents/targets, /v1/fleet/workers); the board summarizes.
type fleetSpec struct {
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	CPUs     int    `json:"cpus,omitempty"`
	Memory   int64  `json:"memory,omitempty"`
	GPUs     int    `json:"gpus,omitempty"`
	GPUModel string `json:"gpuModel,omitempty"`
}

// fleetMetrics is a unit's live utilization. `at` is when it was measured — the
// console renders staleness from it rather than the board guessing liveness.
type fleetMetrics struct {
	Load1   float64 `json:"load1,omitempty"`
	MemUsed int64   `json:"memUsed,omitempty"`
	MemFree int64   `json:"memFree,omitempty"`
	GPUUtil float64 `json:"gpuUtil,omitempty"`
	At      string  `json:"at,omitempty"`
}

// fleetUnit is one compute unit on the board. (source, unit) is its identity:
// `unit` is the source's OWN id, so a row links straight back to the face that
// owns it. sessions/running are the agent session load — 0 for a source that
// cannot host agent sessions, which is a fact, not a gap.
type fleetUnit struct {
	Source   string        `json:"source"`
	Unit     string        `json:"unit"`
	Kind     string        `json:"kind"`
	Label    string        `json:"label,omitempty"`
	Host     string        `json:"host,omitempty"`
	Status   string        `json:"status,omitempty"`
	Spec     *fleetSpec    `json:"spec,omitempty"`
	Metrics  *fleetMetrics `json:"metrics,omitempty"`
	Sessions int           `json:"sessions"`
	Running  int           `json:"running"`
}

// sampleView is one row of the time series on the wire.
type sampleView struct {
	Source    string  `json:"source"`
	Unit      string  `json:"unit"`
	Kind      string  `json:"kind,omitempty"`
	Host      string  `json:"host,omitempty"`
	At        string  `json:"at"`
	CPUs      int     `json:"cpus,omitempty"`
	Memory    int64   `json:"memory,omitempty"`
	MemUsed   int64   `json:"memUsed,omitempty"`
	MemFree   int64   `json:"memFree,omitempty"`
	Load1     float64 `json:"load1,omitempty"`
	Load5     float64 `json:"load5,omitempty"`
	Load15    float64 `json:"load15,omitempty"`
	GPUUtil   float64 `json:"gpuUtil,omitempty"`
	GPUs      int     `json:"gpus,omitempty"`
	GPUModel  string  `json:"gpuModel,omitempty"`
	CostCents int64   `json:"costCents,omitempty"`
}

func toSampleView(s samples.Sample) sampleView {
	return sampleView{
		Source: s.Source, Unit: s.Unit, Kind: s.Kind, Host: s.Host,
		At:   s.At.UTC().Format(time.RFC3339),
		CPUs: s.CPUs, Memory: s.Memory, MemUsed: s.MemUsed, MemFree: s.MemFree,
		Load1: s.Load1, Load5: s.Load5, Load15: s.Load15,
		GPUUtil: s.GPUUtil, GPUs: s.GPUs, GPUModel: s.GPUModel, CostCents: s.CostCents,
	}
}

// metricsOf projects a sample's live half onto the board.
func metricsOf(s samples.Sample) *fleetMetrics {
	m := &fleetMetrics{
		Load1: s.Load1, MemUsed: s.MemUsed, MemFree: s.MemFree, GPUUtil: s.GPUUtil,
	}
	if !s.At.IsZero() {
		m.At = s.At.UTC().Format(time.RFC3339)
	}
	return m
}

// ---- the sources ----
//
// Each returns the org's units for ONE plane and swallows its own failure into an
// empty slice, so the board's fold is a plain append with no error plumbing.

// agentUnits folds in the org's registered run-targets — the only source that
// carries a live snapshot of its own (the heartbeat on the target row) and the
// only one that can host agent sessions.
func agentUnits(s *cloud.Service[state], c *zip.Ctx, org string) []fleetUnit {
	targets, err := agents.TargetsForOrg(c.Context(), org)
	if err != nil {
		s.Log.Warn("fleet board: agent targets unavailable", "org", org, "err", err)
		return nil
	}
	out := make([]fleetUnit, 0, len(targets))
	for _, t := range targets {
		u := fleetUnit{
			Source: samples.SourceAgent, Unit: t.ID, Kind: t.Kind,
			Label: t.Label, Host: t.Host, Status: t.Status,
		}
		if !t.Spec.IsZero() {
			sp := &fleetSpec{OS: t.Spec.OS, Arch: t.Spec.Arch, CPUs: t.Spec.CPUs, Memory: t.Spec.Memory, GPUs: len(t.Spec.GPUs)}
			if len(t.Spec.GPUs) > 0 {
				sp.GPUModel = t.Spec.GPUs[0].Model
				if sp.GPUModel == "" {
					sp.GPUModel = t.Spec.GPUs[0].Vendor
				}
			}
			u.Spec = sp
		}
		// The target row's own heartbeat is authoritative and needs no warehouse,
		// so it is used directly; the sample overlay only fills sources that have
		// no live snapshot of their own.
		if !t.Metrics.IsZero() && t.MetricsAt > 0 {
			u.Metrics = &fleetMetrics{
				Load1: t.Metrics.Load1, MemUsed: t.Metrics.MemUsed,
				MemFree: t.Metrics.MemFree, GPUUtil: t.Metrics.GPUUtil,
				At: time.Unix(t.MetricsAt, 0).UTC().Format(time.RFC3339),
			}
		}
		if load, err := agents.LoadOn(c.Context(), org, t.ID, t.Host); err == nil {
			u.Sessions, u.Running = load.Sessions, load.Running
		}
		out = append(out, u)
	}
	return out
}

// workerUnits folds in the BYO machines that dialed in via `hanzo gpu connect`.
// byoWorkers is already fail-soft (nil on any error).
func workerUnits(org string) []fleetUnit {
	workers := byoWorkers(org)
	out := make([]fleetUnit, 0, len(workers))
	for _, w := range workers {
		out = append(out, byoUnit(w))
	}
	return out
}

// byoUnit projects one dialed-in BYO worker onto the board, carrying the host's full
// static spec — OS, CPU arch, logical cores, total RAM and the GPU summary — in the
// SAME fleetSpec a code-linked run-target reports (agentUnits). This is what surfaces
// a gpu-connect node's real arch (amd64/arm64) + memory on GET /v1/fleet, not just
// its GPU. A field the worker did not report stays zero (omitempty), never invented.
func byoUnit(w byoWorker) fleetUnit {
	u := fleetUnit{
		Source: samples.SourceBYO, Unit: w.ID, Kind: samples.KindWorker,
		Label: w.Hostname, Host: w.Hostname, Status: w.Status,
	}
	sp := fleetSpec{OS: w.Os, Arch: w.Arch, CPUs: w.CPUs, Memory: w.Memory, GPUs: len(w.GPUs)}
	if len(w.GPUs) > 0 {
		sp.GPUModel = w.GPUs[0].Name
	}
	if sp != (fleetSpec{}) {
		u.Spec = &sp
	}
	return u
}

// clusterUnits folds in the org's attached BYO clusters. A cluster's accelerators
// are counted, not modelled — the registry reports vendor totals across nodes, and
// the board reports exactly that rather than inventing per-card detail.
func clusterUnits(s *cloud.Service[state], org, proj string) []fleetUnit {
	list := byoClusters(s, org, proj)
	out := make([]fleetUnit, 0, len(list))
	for _, cl := range list {
		u := fleetUnit{
			Source: samples.SourceBYO, Unit: cl.Name, Kind: samples.KindCluster,
			Label: cl.Name, Status: cl.Status,
		}
		if n := cl.NvidiaGPU + cl.AmdGPU; n > 0 {
			u.Spec = &fleetSpec{GPUs: n}
		}
		out = append(out, u)
	}
	return out
}

// machineUnits folds in the Visor-provisioned machines — the SAME managed-machine
// union listMachines serves (registry + live DO droplets, deduped), so the board
// and the Machines page can never disagree about which machines exist, not just how
// they normalize. BYO workers are NOT folded in here (unlike /v1/machines, which
// merges them for the console's Machines page): on the board they are their own
// source, so each row says where it really came from. Fail-soft: a wedged Visor
// costs its rows, not the board (managedMachines logs and returns what it can).
func machineUnits(s *cloud.Service[state], c *zip.Ctx, org string) []fleetUnit {
	machines := managedMachines(s, c, org)
	out := make([]fleetUnit, 0, len(machines))
	for _, m := range machines {
		// Reuse the ONE visorMachine normalizer (size → GPU model, cpuSize → vcpu)
		// so the board and the Machines page can never disagree about a machine.
		v := toMachineView(m)
		u := fleetUnit{
			Source: samples.SourceVisor, Unit: v.ID, Kind: samples.KindMachine,
			Label: v.Name, Status: v.Status,
		}
		sp := fleetSpec{OS: v.Os}
		if v.Vcpu != nil {
			sp.CPUs = *v.Vcpu
		}
		if v.GPU != "" {
			sp.GPUs, sp.GPUModel = 1, v.GPU
		}
		if sp != (fleetSpec{}) {
			u.Spec = &sp
		}
		out = append(out, u)
	}
	return out
}

// ---- the routes ----

// listFleet serves GET /v1/fleet — every source, unioned, each unit with its
// latest utilization.
func listFleet(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	units := make([]fleetUnit, 0, 16)
	units = append(units, agentUnits(s, c, org)...)
	units = append(units, workerUnits(org)...)
	units = append(units, clusterUnits(s, org, project(c))...)
	units = append(units, machineUnits(s, c, org)...)

	// Overlay the series' latest sample onto the units that carry no live snapshot
	// of their own. Fail-soft: an absent or broken warehouse costs the overlay, not
	// the board — every unit still renders, just without utilization.
	latest, err := samples.Latest(c.Context(), org)
	if err != nil {
		s.Log.Warn("fleet board: samples unavailable", "org", org, "err", err)
		latest = nil
	}
	for i := range units {
		if units[i].Metrics != nil {
			continue // a native live snapshot always wins
		}
		// The sample must agree about the SOURCE before it is attached: `unit` is
		// each source's own id, so two planes could in principle mint the same id
		// within one org, and a board must never show one machine's load on
		// another's row.
		if smp, ok := latest[units[i].Unit]; ok && smp.Source == units[i].Source {
			units[i].Metrics = metricsOf(smp)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"units": units})
}

// listFleetSamples serves GET /v1/fleet/samples?unit=&source=&range= — the org's
// utilization series, oldest first. `unit`/`source`/`range` narrow WITHIN the
// caller's tenant; every one of them is bound or allowlisted by clients/samples,
// and org is never a client field.
func listFleetSamples(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := samples.Series(c.Context(), samples.Query{
		Org:    org, // the VALIDATED principal, never a query param
		Unit:   c.Query("unit"),
		Source: c.Query("source"),
		Range:  c.Query("range"),
	})
	if err != nil {
		// The caller's error and the warehouse's are NOT the same thing:
		//   - a rejected narrower is a 400, and its text is safe to echo (it is
		//     our own closed vocabulary telling them what is allowed);
		//   - a query failure is neither their fault nor theirs to read — the text
		//     names our tables and hosts — so it is logged and answered with an
		//     honest "unavailable". Never a fabricated empty series: a chart that
		//     silently reads "no load" when the truth is "we cannot tell" is worse
		//     than one that says so. (An ABSENT warehouse is different again:
		//     Series returns an empty series and no error, which renders honestly
		//     as "no samples yet".)
		if errors.Is(err, samples.ErrInvalid) {
			return zip.ErrBadRequest(err.Error())
		}
		s.Log.Warn("fleet samples query failed", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "fleet samples unavailable")
	}
	out := make([]sampleView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSampleView(r))
	}
	return c.JSON(http.StatusOK, map[string]any{"samples": out})
}
