// Package samples is the fleet's compute-utilization time series: ONE table, ONE
// writer seam, ONE read face that every compute source feeds.
//
// The fleet already reports lifecycle, liveness and static inventory — the Visor
// machines, the BYO workers that dialed in, the BYO clusters, the agent
// run-targets. None of it answers "how hot is this GPU / how loaded is this
// machine, over time, per org": hanzo.compute_usage is spend/lifecycle-shaped
// (org, app, project, kind, event, machine_id, size, price_cents, ts) and carries
// no cpu/mem/gpu, and o11y's samples_v4 carries no org. An agent run-target does
// carry a real Spec + Metrics, but only the LAST one — a snapshot on the row, not
// a series. This package is the missing plane, and it is the ONLY one: sources
// append here rather than growing private telemetry tables.
//
// It is deliberately a LEAF. It depends on the datastore seam (ai/object) and the
// tenancy vocabulary (clients/principal) and nothing else in cloud, so every
// compute source — clients/agents, clients/visor, ml — can import it without a
// cycle (clients/link imports clients/agents, and clients/agents imports this).
//
//   - Record(ctx, Sample)  append one utilization sample (samples.go)
//   - Series(ctx, Query)   an org's samples over a bounded window (read.go)
//   - Latest(ctx, org)     each unit's most recent sample, for a fleet board
//
// ISOLATION: org is the ONLY tenant key. It leads every statement as a BOUND
// parameter (never interpolated), and a blank or oversized org fails CLOSED on
// both the write and the read — a caller for org A can never write into, or read,
// org B's series. The caller MUST pass an org it already validated server-side
// (principal.Org / a verified claim), never a raw client header.
//
// AVAILABILITY: the warehouse is never in a caller's critical path. With no
// datastore configured, Record is a no-op that returns nil — a heartbeat, a
// session or a board must never fail because the time series is absent.
package samples

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud/clients/principal"
)

// Sources — the CLOSED vocabulary of compute planes that feed the series. Closed
// (validated, never coerced) so `source` stays a real dimension: an unknown source
// is a wiring bug, and silently storing it would quietly fragment every rollup.
const (
	SourceAgent = "agent" // a linked run-target's heartbeat (clients/agents)
	SourceBYO   = "byo"   // a bring-your-own worker or cluster that dialed in
	SourceCloud = "cloud" // a cloud workload
	SourceVisor = "visor" // a Visor-provisioned machine
)

// Kinds — the CLOSED vocabulary of compute units. The first five mirror the agent
// run-target kinds (clients/agents.Target*); `worker` is the BYO fleet's own unit,
// which is presence, not a dispatch destination. This is the fleet-wide superset,
// deliberately NOT an import of the agents vocabulary: agents owns "what an agent
// can be dispatched to", this owns "what the fleet can meter". They overlap today
// and are free to diverge without either dragging the other.
const (
	KindLaptop  = "laptop"
	KindCloud   = "cloud"
	KindGPU     = "gpu"
	KindCluster = "cluster"
	KindMachine = "machine"
	KindWorker  = "worker"
)

// Bounds. The numeric caps are the COLUMN's own range (see tableDDL) — a value the
// column cannot represent is a bug or an attack, and clamping to the type ceiling
// is the only write that is guaranteed well-formed.
const (
	maxUnit     = 128   // a unit id (target id / worker id / machine name / cluster name)
	maxHost     = 253   // a DNS name
	maxGPUModel = 64    // matches the agent Spec's field cap
	maxVocab    = 32    // an over-long source/kind can only be junk; bounds the compare
	maxCPUs     = 65535 // cpus is UInt16
	maxGPUs     = 255   // gpus is UInt8
	// load* is Float32. A real loadavg is single digits; a million is already
	// absurd, and staying far below the float32 ceiling (~3.4e38) means the
	// float64→float32 narrowing can never produce +Inf.
	maxLoad = 1e6
)

// ErrInvalid marks a CALLER's error — a malformed tenant or an out-of-vocabulary
// narrower — as categorically distinct from an infrastructure failure. The two must
// never be conflated at an HTTP face: an unknown ?source is a 400 whose text is
// safe to echo (it is our own closed vocabulary), whereas a warehouse failure is
// neither the caller's fault nor theirs to read — its text names our tables and
// hosts. Faces branch on errors.Is(err, ErrInvalid).
var ErrInvalid = errors.New("samples: invalid request")

var (
	errOrg    = fmt.Errorf("%w: org required", ErrInvalid)
	errUnit   = fmt.Errorf("%w: unit required", ErrInvalid)
	errSource = fmt.Errorf("%w: source must be %s|%s|%s|%s", ErrInvalid, SourceAgent, SourceBYO, SourceCloud, SourceVisor)
	errKind   = fmt.Errorf("%w: kind must be %s|%s|%s|%s|%s|%s", ErrInvalid,
		KindLaptop, KindCloud, KindGPU, KindCluster, KindMachine, KindWorker)
)

// Sample is one utilization measurement of one compute unit at one instant — the
// value the whole plane is built around. Org/Source/Unit/Kind identify it, At is
// when it was measured (server-stamped by the caller; a zero value means "now"),
// and the rest is what the unit WAS and was DOING at that instant.
//
// The static capability (CPUs/Memory/GPUs/GPUModel) rides every row on purpose:
// the series then answers "how hot was this GPU" without a join against a registry
// whose row may have since been rewritten or deregistered. A sample is a fact, and
// a fact carries its own context.
type Sample struct {
	Org    string // the tenant — the ONLY tenancy key
	Source string // agent | byo | cloud | visor
	Unit   string // the source's own id for this unit
	Host   string // the hostname the unit reports (may be empty)
	Kind   string // laptop | cloud | gpu | cluster | machine | worker
	At     time.Time

	CPUs     int   // logical cores
	Memory   int64 // total RAM, bytes
	MemUsed  int64 // bytes
	MemFree  int64 // bytes
	Load1    float64
	Load5    float64
	Load15   float64
	GPUUtil  float64 // 0..1 aggregate utilization
	GPUs     int     // accelerator count
	GPUModel string  // the representative accelerator ("GB10"); GPUs carries the count

	CostCents int64 // the resale price of this unit for this sample's hour; 0 when unpriced
}

func validSource(s string) bool {
	switch s {
	case SourceAgent, SourceBYO, SourceCloud, SourceVisor:
		return true
	}
	return false
}

func validKind(k string) bool {
	switch k {
	case KindLaptop, KindCloud, KindGPU, KindCluster, KindMachine, KindWorker:
		return true
	}
	return false
}

// sanitize returns a Sample every field of which the columns can represent. It is
// total (never errors) and splits on a deliberate line:
//
//   - KEYS (org, unit) are only TRIMMED here, never truncated. Truncating a key
//     re-attributes the row: a clamped org could collide with another tenant's
//     prefix, and a clamped unit merges two units' series. Over-long keys fail
//     CLOSED in validate instead.
//   - COSMETIC fields (host, gpu_model) are clamped — they label the row, they do
//     not key it, so a truncated value is inert.
//   - NUMBERS are clamped to their column's range, with non-finite floats coerced
//     to 0, so no client can smuggle a NaN/Inf or overflow a column.
func (s Sample) sanitize() Sample {
	out := Sample{
		Org:       strings.TrimSpace(s.Org),
		Source:    strings.ToLower(clampStr(s.Source, maxVocab)),
		Unit:      strings.TrimSpace(s.Unit),
		Host:      clampStr(s.Host, maxHost),
		Kind:      strings.ToLower(clampStr(s.Kind, maxVocab)),
		At:        s.At.UTC(),
		CPUs:      clampInt(s.CPUs, maxCPUs),
		Memory:    nonNegI64(s.Memory),
		MemUsed:   nonNegI64(s.MemUsed),
		MemFree:   nonNegI64(s.MemFree),
		Load1:     clampF(s.Load1, maxLoad),
		Load5:     clampF(s.Load5, maxLoad),
		Load15:    clampF(s.Load15, maxLoad),
		GPUUtil:   clampF01(s.GPUUtil),
		GPUs:      clampInt(s.GPUs, maxGPUs),
		GPUModel:  clampStr(s.GPUModel, maxGPUModel),
		CostCents: nonNegI64(s.CostCents),
	}
	if out.At.IsZero() {
		out.At = time.Now().UTC() // the server owns the clock, always
	}
	return out
}

// validate fails CLOSED on anything that would make the row untenanted,
// unattributable, or silently re-keyed. It judges the SANITIZED value, so it is
// the last word on what reaches the table.
func (s Sample) validate() error {
	if s.Org == "" || len(s.Org) > principal.MaxOrgLen {
		return errOrg
	}
	if s.Unit == "" || len(s.Unit) > maxUnit {
		return errUnit
	}
	if !validSource(s.Source) {
		return errSource
	}
	if !validKind(s.Kind) {
		return errKind
	}
	return nil
}

// args renders the sanitized Sample as the INSERT's positional binds, each in the
// column's OWN type. sanitize has already bounded every value to that column's
// range, so these narrowings are lossless by construction (proven in the tests).
func (s Sample) args() []any {
	return []any{
		s.Org, s.Source, s.Unit, s.Host, s.Kind, s.At,
		uint16(s.CPUs), uint64(s.Memory), uint64(s.MemUsed), uint64(s.MemFree),
		float32(s.Load1), float32(s.Load5), float32(s.Load15), float32(s.GPUUtil),
		uint8(s.GPUs), s.GPUModel, uint64(s.CostCents),
	}
}

// ── schema ──────────────────────────────────────────────────────────────────

const table = "hanzo.compute_samples"

// cols is the ONE column list. The INSERT binds it positionally and the SELECTs
// project it, so the write and read shapes can never drift apart.
const cols = `org, source, unit, host, kind, ts, cpus, memory, mem_used, mem_free, load1, load5, load15, gpu_util, gpus, gpu_model, cost_cents`

// tableDDL is the ONE definition of hanzo.compute_samples: the raw series.
// MergeTree (append-only — a sample is a fact, never updated), partitioned by
// month so the TTL expiry drops whole parts instead of mutating, and ordered
// org-FIRST so a tenant's read is a contiguous prefix scan — the same shape
// eval_traces and cloud_usage use.
const tableDDL = `
CREATE TABLE IF NOT EXISTS hanzo.compute_samples (
  org        LowCardinality(String),
  source     LowCardinality(String),
  unit       String,
  host       String,
  kind       LowCardinality(String),
  ts         DateTime64(3),
  cpus       UInt16,
  memory     UInt64,
  mem_used   UInt64,
  mem_free   UInt64,
  load1      Float32,
  load5      Float32,
  load15     Float32,
  gpu_util   Float32,
  gpus       UInt8,
  gpu_model  String,
  cost_cents UInt64
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org, unit, ts)
TTL toDateTime(ts) + INTERVAL 90 DAY`

// rollupDDL is the hourly trend target table. Keyed (org, unit, hour) — org first,
// same tenancy prefix as the raw table.
//
// Its TTL matches the raw table's 90 days ON PURPOSE: this rollup exists to make a
// long-window trend CHEAP (a 30d read is ~720 rows per unit instead of ~43k), not
// to retain beyond the raw series. Keeping trends longer than the raw samples is a
// storage-cost decision the operator owns — it is a one-line TTL change here.
const rollupDDL = `
CREATE TABLE IF NOT EXISTS hanzo.compute_samples_hourly (
  org            LowCardinality(String),
  unit           String,
  source         LowCardinality(String),
  kind           LowCardinality(String),
  hour           DateTime,
  gpu_util_avg   AggregateFunction(avg, Float32),
  gpu_util_max   AggregateFunction(max, Float32),
  load1_avg      AggregateFunction(avg, Float32),
  mem_used_avg   AggregateFunction(avg, UInt64),
  cost_cents_max AggregateFunction(max, UInt64)
) ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(hour)
ORDER BY (org, unit, hour)
TTL hour + INTERVAL 90 DAY`

// rollupViewDDL feeds the rollup on every insert into the raw table. The TO form
// (an explicit target table, not an implicit .inner) so the aggregate survives a
// view rebuild and can be read directly. A TO-view is an INSERT SELECT: its
// columns match the target POSITIONALLY, so this projection is in the target's
// exact declared order — keep the two in lockstep.
const rollupViewDDL = `
CREATE MATERIALIZED VIEW IF NOT EXISTS hanzo.compute_samples_hourly_mv
TO hanzo.compute_samples_hourly
AS SELECT
  org,
  unit,
  source,
  kind,
  toStartOfHour(ts) AS hour,
  avgState(gpu_util)   AS gpu_util_avg,
  maxState(gpu_util)   AS gpu_util_max,
  avgState(load1)      AS load1_avg,
  avgState(mem_used)   AS mem_used_avg,
  maxState(cost_cents) AS cost_cents_max
FROM hanzo.compute_samples
GROUP BY org, unit, source, kind, hour`

// schema is the whole plane, in dependency order: the raw table, the rollup's
// target, then the view that feeds it. Every statement is IF NOT EXISTS.
var schema = []string{tableDDL, rollupDDL, rollupViewDDL}

const insertStmt = `INSERT INTO ` + table + ` (` + cols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

var (
	ensureMu sync.Mutex
	ensured  bool
)

// ensure creates the plane idempotently, serialized so a burst of first writes
// issues one DDL pass rather than a storm. It latches ONLY on success, so a
// transient datastore failure at boot is retried on the next write instead of
// poisoning the process (the eval telemetry pattern).
func ensure(ctx context.Context) error {
	ensureMu.Lock()
	defer ensureMu.Unlock()
	if ensured {
		return nil
	}
	for _, ddl := range schema {
		if err := aiobject.DatastoreExec(ctx, ddl); err != nil {
			return fmt.Errorf("samples: ensure schema: %w", err)
		}
	}
	ensured = true
	return nil
}

// Record appends one utilization sample to the series.
//
// Fail-SOFT on absence, fail-CLOSED on nonsense — the two are different and are
// reported differently:
//
//   - No datastore configured (DatastoreEnabled() == false): a no-op returning
//     nil. The time series is optional infrastructure; its absence must never
//     fail or stall the heartbeat, session or board that emitted the sample.
//   - A blank/oversized org, a blank/oversized unit, or an unknown source/kind:
//     an error and NO write. An untenanted or misattributed row is worse than no
//     row at all.
//
// It is synchronous and honours the ctx it is given — the CALLER owns the
// concurrency policy, exactly as the billing warehouse write does (`go
// zapWriteUsage(...)`). An emitter on a request path should hand it a DETACHED,
// bounded context so neither a slow warehouse nor a client disconnect can touch
// its own contract.
func Record(ctx context.Context, s Sample) error {
	if !aiobject.DatastoreEnabled() {
		return nil // no warehouse — the sample is dropped, the caller is untouched
	}
	s = s.sanitize()
	if err := s.validate(); err != nil {
		return err
	}
	if err := ensure(ctx); err != nil {
		return err
	}
	if err := aiobject.DatastoreExec(ctx, insertStmt, s.args()...); err != nil {
		return fmt.Errorf("samples: record: %w", err)
	}
	return nil
}

// ── bounds (total functions; mirrors clients/agents.Spec/Metrics Sanitize) ───

func clampStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}

func clampInt(i, hi int) int {
	if i < 0 {
		return 0
	}
	if i > hi {
		return hi
	}
	return i
}

func nonNegI64(i int64) int64 {
	if i < 0 {
		return 0
	}
	return i
}

// clampF returns a finite, non-negative float bounded by hi (NaN/Inf/negative → 0),
// so no sample can poison a column or overflow the float32 narrowing.
func clampF(f float64, hi float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	if f > hi {
		return hi
	}
	return f
}

// clampF01 returns a finite float in [0,1] — a utilization ratio.
func clampF01(f float64) float64 { return clampF(f, 1) }
