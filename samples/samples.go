package samples

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/text"
)

// Sources — the CLOSED vocabulary of compute planes that feed the series. Closed
// (validated, never coerced) so `source` stays a real dimension: an unknown source
// is a wiring bug, and silently storing it would quietly fragment every rollup.
const (
	SourceAgent = "agent" // a linked run-target's heartbeat (clients/agents)
	SourceBYO   = "byo"   // a bring-your-own worker or cluster that dialed in
	SourceCloud = "cloud" // a cloud workload
	// SourceCompute is the dimension VALUE, and it stays "visor" though the app is
	// now compute: it is stored on every sample under a 90-day TTL, it is the
	// ?source= a caller filters on, and it is the key the fleet board joins a unit
	// to its latest sample by. Renaming it orphans every row already written.
	SourceCompute = "visor" // a Hanzo Compute-provisioned machine
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
	maxModel    = 128   // matches the agent Metrics' field cap
	maxVocab    = 32    // an over-long source/kind can only be junk; bounds the compare
	maxCPUs     = 65535 // cpus is UInt16
	maxGPUs     = 255   // gpus is UInt8
	// load* is Float32. A real loadavg is single digits; a million is already
	// absurd, and staying far below the float32 ceiling (~3.4e38) means the
	// float64→float32 narrowing can never produce +Inf.
	maxLoad = 1e6
	// The telemetry bounds match the agent Metrics' sanitize; every Float32 one is
	// far below the float32 ceiling for the same reason maxLoad is.
	maxTemp    = 200     // °C
	maxPower   = 100000  // W
	maxRate    = 1e12    // B/s, disk and network
	maxTokRate = 1e7     // tok/s, decode and prefill
	maxTTFT    = 3600    // s
	maxReqs    = 1000000 // running, waiting (UInt32)
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
	errSource = fmt.Errorf("%w: source must be %s|%s|%s|%s", ErrInvalid, SourceAgent, SourceBYO, SourceCloud, SourceCompute)
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

	CPUUtil       float64 // 0..1 busy fraction of all cores
	CPUTemp       float64 // °C, CPU package
	GPUTemp       float64 // °C, hottest GPU
	GPUPower      float64 // W, total draw
	GPUPowerLimit float64 // W, total enforced limit
	GPUMemUsed    int64   // bytes held by GPU processes
	GPUMemTotal   int64   // bytes of dedicated VRAM; 0 on unified memory
	DiskUsed      int64   // bytes used across local block filesystems
	DiskTotal     int64   // bytes, size of those filesystems
	DiskRead      float64 // B/s
	DiskWrite     float64 // B/s
	NetRx         float64 // B/s, physical interfaces
	NetTx         float64 // B/s, physical interfaces
	Model         string  // model id(s) served, comma-separated
	Decode        float64 // tok/s, generation
	Prefill       float64 // tok/s, prompt
	TTFT          float64 // s, mean time to first token
	Running       int     // requests being served
	Waiting       int     // requests queued
	KVCache       float64 // 0..1 KV-cache occupancy
}

func validSource(s string) bool {
	switch s {
	case SourceAgent, SourceBYO, SourceCloud, SourceCompute:
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

		CPUUtil:       clampF01(s.CPUUtil),
		CPUTemp:       clampF(s.CPUTemp, maxTemp),
		GPUTemp:       clampF(s.GPUTemp, maxTemp),
		GPUPower:      clampF(s.GPUPower, maxPower),
		GPUPowerLimit: clampF(s.GPUPowerLimit, maxPower),
		GPUMemUsed:    nonNegI64(s.GPUMemUsed),
		GPUMemTotal:   nonNegI64(s.GPUMemTotal),
		DiskUsed:      nonNegI64(s.DiskUsed),
		DiskTotal:     nonNegI64(s.DiskTotal),
		DiskRead:      clampF(s.DiskRead, maxRate),
		DiskWrite:     clampF(s.DiskWrite, maxRate),
		NetRx:         clampF(s.NetRx, maxRate),
		NetTx:         clampF(s.NetTx, maxRate),
		Model:         clampStr(s.Model, maxModel),
		Decode:        clampF(s.Decode, maxTokRate),
		Prefill:       clampF(s.Prefill, maxTokRate),
		TTFT:          clampF(s.TTFT, maxTTFT),
		Running:       clampInt(s.Running, maxReqs),
		Waiting:       clampInt(s.Waiting, maxReqs),
		KVCache:       clampF01(s.KVCache),
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
		float32(s.CPUUtil), float32(s.CPUTemp), float32(s.GPUTemp),
		float32(s.GPUPower), float32(s.GPUPowerLimit),
		uint64(s.GPUMemUsed), uint64(s.GPUMemTotal), uint64(s.DiskUsed), uint64(s.DiskTotal),
		float32(s.DiskRead), float32(s.DiskWrite), float32(s.NetRx), float32(s.NetTx),
		s.Model, float32(s.Decode), float32(s.Prefill), float32(s.TTFT),
		uint32(s.Running), uint32(s.Waiting), float32(s.KVCache),
	}
}

// ── schema ──────────────────────────────────────────────────────────────────

const table = "hanzo.compute_samples"

// cols is the ONE column list. The INSERT binds it positionally and the SELECTs
// project it, so the write and read shapes can never drift apart.
const cols = `org, source, unit, host, kind, ts, cpus, memory, mem_used, mem_free, load1, load5, load15, gpu_util, gpus, gpu_model, cost_cents, ` +
	`cpu_util, cpu_temp, gpu_temp, gpu_power, gpu_power_limit, gpu_mem_used, gpu_mem_total, disk_used, disk_total, ` +
	`disk_read, disk_write, net_rx, net_tx, model, decode, prefill, ttft, running, waiting, kv_cache`

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
  cost_cents UInt64,
  cpu_util        Float32,
  cpu_temp        Float32,
  gpu_temp        Float32,
  gpu_power       Float32,
  gpu_power_limit Float32,
  gpu_mem_used    UInt64,
  gpu_mem_total   UInt64,
  disk_used       UInt64,
  disk_total      UInt64,
  disk_read       Float32,
  disk_write      Float32,
  net_rx          Float32,
  net_tx          Float32,
  model           LowCardinality(String),
  decode          Float32,
  prefill         Float32,
  ttft            Float32,
  running         UInt32,
  waiting         UInt32,
  kv_cache        Float32
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org, unit, ts)
TTL toDateTime(ts) + INTERVAL 90 DAY`

// alterDDL adds the columns tableDDL gained after it first shipped to a table
// created before them: CREATE TABLE IF NOT EXISTS never alters a live table. Each
// clause names a column tableDDL declares, in the same order, so an upgraded table
// and a fresh one have one shape.
const alterDDL = `
ALTER TABLE hanzo.compute_samples
  ADD COLUMN IF NOT EXISTS cpu_util        Float32,
  ADD COLUMN IF NOT EXISTS cpu_temp        Float32,
  ADD COLUMN IF NOT EXISTS gpu_temp        Float32,
  ADD COLUMN IF NOT EXISTS gpu_power       Float32,
  ADD COLUMN IF NOT EXISTS gpu_power_limit Float32,
  ADD COLUMN IF NOT EXISTS gpu_mem_used    UInt64,
  ADD COLUMN IF NOT EXISTS gpu_mem_total   UInt64,
  ADD COLUMN IF NOT EXISTS disk_used       UInt64,
  ADD COLUMN IF NOT EXISTS disk_total      UInt64,
  ADD COLUMN IF NOT EXISTS disk_read       Float32,
  ADD COLUMN IF NOT EXISTS disk_write      Float32,
  ADD COLUMN IF NOT EXISTS net_rx          Float32,
  ADD COLUMN IF NOT EXISTS net_tx          Float32,
  ADD COLUMN IF NOT EXISTS model           LowCardinality(String),
  ADD COLUMN IF NOT EXISTS decode          Float32,
  ADD COLUMN IF NOT EXISTS prefill         Float32,
  ADD COLUMN IF NOT EXISTS ttft            Float32,
  ADD COLUMN IF NOT EXISTS running         UInt32,
  ADD COLUMN IF NOT EXISTS waiting         UInt32,
  ADD COLUMN IF NOT EXISTS kv_cache        Float32`

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

// schema is the whole plane, in dependency order: the raw table and its added
// columns, the rollup's target, then the view that feeds it. Every statement is
// IF NOT EXISTS.
var schema = []string{tableDDL, alterDDL, rollupDDL, rollupViewDDL}

// insertStmt binds one placeholder per column of cols.
var insertStmt = `INSERT INTO ` + table + ` (` + cols + `) VALUES (` +
	strings.Repeat("?,", strings.Count(cols, ",")) + `?)`

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
		if err := datastore.Exec(ctx, ddl); err != nil {
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
	if !datastore.Ready() {
		return nil // no warehouse — the sample is dropped, the caller is untouched
	}
	s = s.sanitize()
	if err := s.validate(); err != nil {
		return err
	}
	if err := ensure(ctx); err != nil {
		return err
	}
	if err := datastore.Exec(ctx, insertStmt, s.args()...); err != nil {
		return fmt.Errorf("samples: record: %w", err)
	}
	return nil
}

// ── bounds (total functions; mirrors clients/agents.Spec/Metrics Sanitize) ───

func clampStr(s string, n int) string {
	return strings.ToValidUTF8(text.Cut(strings.TrimSpace(s), n), "")
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
