// board.go — GET /v1/visor/fleet: the org's compute, from every source, on ONE board,
// each unit carrying its latest utilization.
//
// The fleet is already visible, just never in one place: Visor's machines
// (/v1/visor/machines), the BYO workers that dialed in (/v1/visor/fleet/workers), the BYO
// clusters (/v1/visor/clusters) and the agent run-targets (/v1/agents/targets) each
// answer for their own plane. This unions them behind the tenant's ONE question —
// "what compute do I have, and how hot is it?" — and overlays the utilization
// series (clients/samples) that no source used to keep.
//
//	GET /v1/visor/fleet          the org's units + their latest sample  -> {units:[fleetUnit]}
//	GET /v1/visor/fleet/samples  one unit's / the org's time series      -> {samples:[sampleView]}
//	GET /v1/visor/fleet/workers  the raw BYO inventory (fleet.go)        -> unchanged
//
// It lives in visor because visor already owns /v1/visor/fleet/workers and the compute
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
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/samples"
	"github.com/zap-proto/zip"
)

// ---- the published board shapes ----

// fleetSpec is a unit's static capability, normalized across sources. GPUs is a
// COUNT plus the representative model — the same shape the sample row carries
// (gpus UInt8 + gpu_model String), so the board and the series describe a machine
// the same way. Full per-accelerator detail stays on each source's own face
// (/v1/agents/targets, /v1/visor/fleet/workers); the board summarizes.
type fleetSpec struct {
	// OS is the operating system the unit runs: linux, darwin or windows. Empty
	// when the source does not report one — a cluster row does not.
	OS string `json:"os,omitempty"`
	// Arch is the CPU architecture, amd64 or arm64, and it is what decides whether
	// a binary built for the fleet will run here. Only the sources that report one
	// carry it (a linked run-target, a BYO worker).
	Arch string `json:"arch,omitempty"`
	// CPUs is logical cores on the unit.
	CPUs int `json:"cpus,omitempty"`
	// Memory is total system RAM in BYTES — not GB, and not what is free right now
	// (fleetMetrics carries that). Absent when the source reports no RAM figure.
	Memory int64 `json:"memory,omitempty"`
	// GPUs is how many accelerators the unit has. For a cluster it is the vendor
	// totals summed across every node, so it counts cards, not machines.
	GPUs int `json:"gpus,omitempty"`
	// GPUModel names the FIRST accelerator ("NVIDIA GB10") as the representative of
	// the set; GPUs carries how many. Empty for a cluster, whose cards are counted
	// rather than modelled, and for a unit with none.
	GPUModel string `json:"gpuModel,omitempty"`
}

// fleetMetrics is a unit's live utilization. `at` is when it was measured — the
// console renders staleness from it rather than the board guessing liveness.
type fleetMetrics struct {
	// Load1 is the host's 1-minute load average — runnable processes, not a
	// percentage, so it is read against the unit's core count and can exceed 1.
	Load1 float64 `json:"load1,omitempty"`
	// MemUsed is host memory in use, in BYTES.
	MemUsed int64 `json:"memUsed,omitempty"`
	// MemFree is host memory still available, in BYTES. It is what the source
	// reported, not fleetSpec.Memory minus MemUsed.
	MemFree int64 `json:"memFree,omitempty"`
	// GPUUtil is aggregate accelerator utilization as a FRACTION of 1 — 0.42 is
	// 42% busy, never 42. Across all of the unit's cards, not one of them.
	GPUUtil float64 `json:"gpuUtil,omitempty"`
	// At is when this reading was MEASURED, RFC 3339 in UTC — not when the board
	// was built. A console decides staleness by comparing it to now; the board
	// deliberately does not decide that for it.
	At string `json:"at,omitempty"`
}

// fleetUnit is one compute unit on the board. (source, unit) is its identity:
// `unit` is the source's OWN id, so a row links straight back to the face that
// owns it. sessions/running are the agent session load — 0 for a source that
// cannot host agent sessions, which is a fact, not a gap.
type fleetUnit struct {
	// Source is the plane this row came from: "agent" (a linked run-target), "byo"
	// (a worker or cluster the org dialed in) or "visor" (a machine Hanzo
	// provisioned). It is half the row's identity, and it says which face owns the
	// unit — /v1/agents/targets, /v1/visor/fleet/workers, /v1/visor/machines.
	Source string `json:"source"`
	// Unit is the SOURCE's own id for this unit — a run-target id, a BYO worker id,
	// a Visor machine name — so a row links straight back to the face that owns it.
	// It is unique within a source, not across them: two planes may mint the same
	// id, which is why (source, unit) together is the identity.
	Unit string `json:"unit"`
	// Kind is what the unit IS: laptop, cloud, gpu, cluster, machine or worker.
	Kind string `json:"kind"`
	// Label is the name to show a human — a target's label, a worker's hostname, a
	// machine's display name. Empty when the source has none to give.
	Label string `json:"label,omitempty"`
	// Host is the unit's hostname. Empty for a unit that is not one host: a cluster
	// row has no hostname to report.
	Host string `json:"host,omitempty"`
	// Status is liveness in the SOURCE's own vocabulary, because each plane decides
	// it differently: a run-target's is derived from its heartbeat, a BYO worker's
	// is online/offline on the 90s window, a BYO cluster's is "attached", and a
	// Visor machine's is the provider's word for its lifecycle state.
	Status string `json:"status,omitempty"`
	// Spec is the unit's static capability. Absent when the source reported none —
	// unknown capability, never a zeroed one.
	Spec *fleetSpec `json:"spec,omitempty"`
	// Metrics is the unit's latest utilization: its own live snapshot when it keeps
	// one (a run-target's heartbeat wins), else the newest sample from the series
	// for the SAME source. Absent means nothing is known about this unit's load —
	// which is deliberately not the same as a reading of zero.
	Metrics *fleetMetrics `json:"metrics,omitempty"`
	// Sessions is how many agent sessions are open on this unit. Always present,
	// and 0 for a source that cannot host agent sessions at all — a fact about that
	// plane, not a gap in the reading.
	Sessions int `json:"sessions"`
	// Running is what the unit is executing right now: agent sessions in flight for
	// a run-target, claimed renders for a BYO GPU.
	Running int `json:"running"`
	// Queued is how many renders are waiting on THIS GPU's own lane in the org's
	// gpu-jobs queue. BYO units only — an agent run-target dispatches, it does not
	// queue — and omitted when nothing is waiting.
	Queued int `json:"queued,omitempty"`
}

// sampleView is one row of the time series on the wire.
type sampleView struct {
	// Source is the plane that reported the reading: "agent", "byo" or "visor" —
	// the same vocabulary the board's rows carry, and what ?source= narrows on.
	Source string `json:"source"`
	// Unit is the source's own id for the measured unit. With Source it is the key
	// the chart groups by, and the key the board joins a unit's latest reading on.
	Unit string `json:"unit"`
	// Kind is what the measured unit is: laptop, cloud, gpu, cluster, machine or
	// worker.
	Kind string `json:"kind,omitempty"`
	// Host is the hostname the unit reported at the time of the reading.
	Host string `json:"host,omitempty"`
	// At is when the reading was MEASURED, RFC 3339 in UTC — the x-axis a chart
	// plots against. The series is returned oldest first, so it only increases.
	At string `json:"at"`
	// CPUs is logical cores. The static capability rides every row on purpose: a
	// chart can size load against cores without joining a registry whose row may
	// since have been rewritten or the unit deregistered.
	CPUs int `json:"cpus,omitempty"`
	// Memory is total system RAM in BYTES at the time of the reading.
	Memory int64 `json:"memory,omitempty"`
	// MemUsed is host memory in use, in BYTES.
	MemUsed int64 `json:"memUsed,omitempty"`
	// MemFree is host memory available, in BYTES, as reported rather than derived.
	MemFree int64 `json:"memFree,omitempty"`
	// Load1 is the 1-minute load average — runnable processes, not a percentage.
	Load1 float64 `json:"load1,omitempty"`
	// Load5 is the 5-minute load average, the same units as Load1.
	Load5 float64 `json:"load5,omitempty"`
	// Load15 is the 15-minute load average, the same units as Load1.
	Load15 float64 `json:"load15,omitempty"`
	// GPUUtil is aggregate accelerator utilization as a FRACTION of 1 — 0.42 is
	// 42% busy. Anything a reporter sends outside 0..1 is clamped into it on write.
	GPUUtil float64 `json:"gpuUtil,omitempty"`
	// GPUs is how many accelerators the reading covers.
	GPUs int `json:"gpus,omitempty"`
	// GPUModel names the representative accelerator ("GB10"); GPUs carries how many.
	GPUModel string `json:"gpuModel,omitempty"`
	// CostCents is what this unit resold for over the hour the reading falls in, in
	// whole US cents. 0 means UNPRICED, not free: the operator's own machines — a
	// linked run-target, a dialed-in BYO worker — are metered for utilization and
	// never resold, so only a priced source ever fills it.
	CostCents int64 `json:"costCents,omitempty"`
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
	now := time.Now()
	for _, t := range targets {
		u := fleetUnit{
			Source: samples.SourceAgent, Unit: t.ID, Kind: t.Kind,
			// Liveness is decided by the heartbeat, not by whatever the row was
			// last written with — see agents.Target.EffectiveStatus.
			Label: t.Label, Host: t.Host, Status: t.EffectiveStatus(now),
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

// workerUnits folds in the BYO machines that dialed in via `hanzo link`.
// byoWorkers is already fail-soft (nil on any error).
func workerUnits(ctx context.Context, org string) []fleetUnit {
	workers := byoWorkers(ctx, org)
	out := make([]fleetUnit, 0, len(workers))
	for _, w := range workers {
		out = append(out, byoUnit(w))
	}
	return out
}

// byoUnit projects one dialed-in BYO worker onto the board, carrying the host's full
// static spec — OS, CPU arch, logical cores, total RAM and the GPU summary — in the
// SAME fleetSpec a code-linked run-target reports (agentUnits). This is what surfaces
// a linked node's real arch (amd64/arm64) + memory on GET /v1/visor/fleet, not just
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
func clusterUnits(ctx context.Context, s *cloud.Service[state], org, proj string) []fleetUnit {
	list := byoClusters(ctx, s, org, proj)
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
// they normalize. BYO workers are NOT folded in here (unlike /v1/visor/machines, which
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

// fleetBoard is the whole board: every compute unit the org has, from every
// source, each with whatever utilization is known about it.
type fleetBoard struct {
	// Units is the union across sources — agent run-targets, BYO workers, BYO
	// clusters and Visor machines — each row naming the source it came from.
	Units []fleetUnit `json:"units"`
}

// listFleet returns every compute unit the caller's org has, from every source, each
// carrying its latest utilization: agent run-targets, the BYO machines that dialed
// in, attached BYO clusters and Visor-provisioned machines.
//
// A unit with a live snapshot of its own keeps it; the rest are overlaid from the
// utilization series, and only when the sample agrees about the SOURCE — two planes
// could mint the same unit id, and a board must never show one machine's load on
// another's row. BYO GPU units also carry their gpu-jobs queue depth. Every source
// is folded in independently: a broken one costs its own rows and nothing else.
//
// Response: {"units":[{"source":"byo","unit":"spark","kind":"worker","label":"spark","host":"spark","status":"online","spec":{"os":"linux","arch":"arm64","cpus":20,"gpus":1,"gpuModel":"NVIDIA GB10"},"metrics":{"gpuUtil":0.42,"at":"2026-07-27T09:00:00Z"},"sessions":0,"running":1,"queued":2}]}
func (o ops) listFleet(ctx context.Context, _ *cloud.Unit) (*fleetBoard, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	s := o.Service
	units := make([]fleetUnit, 0, 16)
	units = append(units, agentUnits(s, c, org)...)
	units = append(units, workerUnits(ctx, org)...)
	units = append(units, clusterUnits(ctx, s, org, project(c))...)
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

	// Overlay the org's gpu-jobs queue depth onto its BYO GPU units: Queued (waiting
	// on this GPU's lane) + Running (renders it is executing now). Only BYO units
	// carry a render queue; an agent unit's Running stays its session load. Fail-soft:
	// gpuJobs is nil on an unavailable engine, so counts is empty and nothing is
	// touched — the board never breaks because the queue read did.
	counts := gpuJobCounts(gpuJobs(ctx, org))
	for i := range units {
		if units[i].Source != samples.SourceBYO {
			continue
		}
		if cnt, ok := counts[units[i].Unit]; ok {
			units[i].Queued, units[i].Running = cnt.queued, cnt.running
		}
	}
	return &fleetBoard{Units: units}, nil
}

// sampleQuery narrows the utilization series WITHIN the caller's tenant. Each
// narrower is bound or allowlisted by clients/samples; the org never is one.
type sampleQuery struct {
	// Unit selects one compute unit's series by its source-local id.
	Unit string `json:"unit"`
	// Source selects one plane: "agent", "byo" or "visor".
	Source string `json:"source"`
	// Range is the lookback window (e.g. "1h", "24h", "7d"); empty takes the
	// warehouse default.
	Range string `json:"range"`
}

// sampleList is the utilization series on the wire.
type sampleList struct {
	// Samples are the readings, OLDEST first — the order a chart plots.
	Samples []sampleView `json:"samples"`
}

// listFleetSamples returns the caller org's utilization series, oldest first.
//
// A rejected narrower is a 400 carrying its own reason (the vocabulary is ours and
// safe to echo); a warehouse failure is logged and answered 503 "unavailable",
// because a chart that silently reads "no load" when the truth is "we cannot tell"
// is worse than one that says so. An ABSENT warehouse is different again: it returns
// an empty series, which renders honestly as "no samples yet".
//
// Response: {"samples":[{"source":"byo","unit":"spark","kind":"worker","host":"spark","at":"2026-07-27T09:00:00Z","gpuUtil":0.42,"gpus":1,"gpuModel":"GB10"}]}
func (o ops) listFleetSamples(ctx context.Context, in *sampleQuery) (*sampleList, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := samples.Series(c.Context(), samples.Query{
		Org:    org, // the VALIDATED principal, never a query param
		Unit:   in.Unit,
		Source: in.Source,
		Range:  in.Range,
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
			return nil, zip.ErrBadRequest(err.Error())
		}
		o.Log.Warn("fleet samples query failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "fleet samples unavailable")
	}
	out := make([]sampleView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSampleView(r))
	}
	return &sampleList{Samples: out}, nil
}
