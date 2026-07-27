package eval

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/datastore"
	luxlog "github.com/luxfi/log"
)

// The eval TELEMETRY store is the high-volume, append-only half of the storage
// split (CTO directive): traces, observations and scores-as-events land in
// datastore (hanzoai's datastore fork), NOT SQLite. datastore is THE backend
// for all AI observability, so this is the same MergeTree, insert-only discipline
// the audit OLAP mirror uses (audit_mirror.go) and it reuses the v3
// datastore shapes (traces/observations/scores) since datastore IS datastore.
//
// ONE DATASTORE CLIENT (CTO consolidation). This store does NOT open a second
// datastore connection with a parallel CLOUD_EVALS_DATASTORE_* cred namespace.
// It routes every write and read over the SHARED datastore peer that ai/object
// owns (clients/datastore), the same client
// clients/analytics and the ai o11y ledger use. The connection, retry/backoff,
// pooling and KMS-injected DATASTORE_* creds live in exactly one place; eval only
// owns its two eval-specific tables (hanzo.eval_traces, hanzo.eval_scores) — the
// ai/object client owns hanzo.cloud_usage / hanzo.observations. Clean ownership,
// one transport, one cred namespace.
//
// SECURITY (tenant isolation on OLAP):
//   - Every table carries `org` LowCardinality(String), NOT a nullable, and it is
//     the FIRST key in ORDER BY so tenant reads are a prefix scan.
//   - Every read predicate binds org as a NAMED PARAMETER ({org:String}) — never
//     string-interpolated. datastore SQL injection through a crafted org/dataset
//     is thereby impossible; the value is bound by the driver.
//   - Every read carries a LIMIT (bounded response, no OLAP-scan DoS).
//   - Scores are events: a MergeTree rejects UPDATE/DELETE at parse time, so a
//     recorded score is immutable — the integrity property Red will probe. Score
//     VALUES are validated (finite, in-range) at the API boundary before they
//     ever reach Record; this store additionally refuses non-finite values as a
//     defense-in-depth backstop.
//
// The interface makes the store swappable: production wires the datastore impl;
// tests use memTelemetry (in-memory) so the store/API/orchestrator are testable
// without a datastore instance. The orchestrator composes Telemetry with the
// metastore + gateway + judge; it owns none of their internals.

// Trace is one model-under-test invocation recorded during a run. Input/Output
// are opaque JSON/text; the run/dataset/item fields carry the linkage.
//
// Attribution (HIP-0106): a trace records WHO/WHERE/WHEN alongside WHAT, so the
// eval trace list carries the same observability dimensions the production gen_ai
// span plane does:
//   - ProjectID  — the org SUB-SCOPE (principal.Project); "" / "default" is the
//     org's default project. Org stays the tenant-isolation key; project narrows.
//   - SessionID  — groups the traces of one logical session; an eval run stamps
//     its RunName so a run's item-traces read back as one session.
//   - APIKeyHash — a NON-reversible ref (SHA-256 hex) of the caller credential the
//     run drove the model with. Never the plaintext key: the ref correlates a
//     trace to a key without the store ever holding a secret.
//   - StartTime/EndTime — the model-under-test call window; EndTime-StartTime is
//     the trace latency the view surfaces (LatencyMs).
type Trace struct {
	ID         string
	Org        string
	ProjectID  string
	Name       string
	Dataset    string
	ItemID     string
	RunName    string
	SessionID  string
	APIKeyHash string
	Model      string
	Input      string
	Output     string
	StartTime  time.Time
	EndTime    time.Time
	Timestamp  time.Time
}

// ScoreEvent is one recorded score (append-only). Value is the numeric score;
// StringValue is the categorical/boolean label (empty for pure numeric). DataType
// is NUMERIC|CATEGORICAL|BOOLEAN. TraceID links it to its trace/run.
type ScoreEvent struct {
	ID          string
	Org         string
	Name        string
	TraceID     string
	RunName     string
	Dataset     string
	ItemID      string
	DataType    string
	Value       float64
	StringValue string
	Comment     string
	Timestamp   time.Time
}

// ScoreFilter bounds a scores read. Org is MANDATORY (the caller passes the
// authoritative org); the others narrow within the org. Limit is always applied.
type ScoreFilter struct {
	Org     string
	Name    string
	RunName string
	TraceID string
	Limit   int
}

// TraceFilter bounds a traces read. Org is MANDATORY; ProjectID/SessionID/RunName/
// Dataset narrow WITHIN the org. ProjectID is the caller's server-minted project
// (principal.Project), so a project-scoped caller sees only its project's traces.
type TraceFilter struct {
	Org       string
	ProjectID string
	SessionID string
	RunName   string
	Dataset   string
	Limit     int
}

// Telemetry is the append-only event store for eval traces + scores. Every
// method takes an authoritative org (via the value's Org field or the filter);
// no method can read across orgs.
//
// Production LLM generations ("observations") are NOT read here: the observation
// of record is the o11y gen_ai span plane (/v1/o11y/observations), not a second
// projection of hanzo.cloud_usage. cloud_usage stays the metering warehouse
// (read by /v1/usage + billing), never an obs store. eval owns only its two
// eval-specific tables (eval_traces, eval_scores).
type Telemetry interface {
	RecordTrace(ctx context.Context, t Trace) error
	RecordScore(ctx context.Context, sc ScoreEvent) error
	ListScores(ctx context.Context, f ScoreFilter) ([]ScoreEvent, error)
	ListTraces(ctx context.Context, f TraceFilter) ([]Trace, error)
	Metrics(ctx context.Context, f MetricsFilter) (Board, error)
	Close() error
}

// ── datastore (shared datastore client) implementation ──────────────────────

// dsTelemetry writes eval telemetry to the datastore over the SHARED ai/object
// datastore client. It holds no connection of its own — aiobject owns the peer,
// its retry/backoff, its pool and its DATASTORE_* creds. dsTelemetry owns only
// its two tables and the SQL for its rows.
type dsTelemetry struct {
	db  string
	log luxlog.Logger

	mu          sync.Mutex
	tablesReady bool
}

// newDatastoreTelemetry builds the datastore-backed telemetry store, or returns
// (nil, nil) when no datastore is configured (DATASTORE_ADDR unset) — the caller
// then runs with telemetry disabled (traces/scores are not persisted, and it says
// so; never a fake success). The connection is opened asynchronously by
// the first warehouse call, so this constructor
// does NOT dial: it defers readiness to request time, where every op gates on
// datastore.Ready() for an honest "unavailable" during the boot window.
//
// Creds are the ONE shared namespace (KMS-injected, never hard-coded), resolved
// by aiobject: DATASTORE_ADDR / DATASTORE_DB / DATASTORE_USER / DATASTORE_PASSWORD.
func newDatastoreTelemetry(log luxlog.Logger) (Telemetry, error) {
	if getenv("DATASTORE_ADDR") == "" {
		return nil, nil // no datastore configured — telemetry disabled.
	}
	return &dsTelemetry{db: getenvDefault("DATASTORE_DB", "hanzo"), log: log}, nil
}

func (t *dsTelemetry) table(name string) string { return t.db + "." + name }

// ready gates an op on the shared connection being live and the eval tables
// existing. It returns an honest error while the async datastore connect is still
// in flight (the boot window) rather than fabricating success, and it creates the
// eval-owned tables idempotently, latching only on first success so a transient
// DDL failure is retried on the next call.
func (t *dsTelemetry) ready(ctx context.Context) error {
	if !datastore.Ready() {
		return fmt.Errorf("evals telemetry: datastore not connected")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tablesReady {
		return nil
	}
	if err := t.ensureTables(ctx); err != nil {
		return err
	}
	t.tablesReady = true
	return nil
}

// ensureTables creates the append-only telemetry tables idempotently, then
// applies the additive column migrations so a table created before the
// attribution columns existed converges on the current shape. These are the
// datastore projections of the v3 traces/observations/scores model: MergeTree
// (insert-only), partitioned by month, ordered org-first so a tenant's reads are
// a contiguous prefix. The attribution columns (project/session_id/api_key_hash/
// start_time/end_time) are additive, so ORDER BY is unchanged (org stays the
// first key — tenant isolation is a prefix scan regardless).
func (t *dsTelemetry) ensureTables(ctx context.Context) error {
	stmts := []string{
		fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  id           String,
  org          LowCardinality(String),
  project      LowCardinality(String),
  name         String,
  dataset      LowCardinality(String),
  item_id      String,
  run_name     String,
  session_id   String,
  api_key_hash String,
  model        LowCardinality(String),
  input        String,
  output       String,
  start_time   DateTime64(3, 'UTC'),
  end_time     DateTime64(3, 'UTC'),
  ts           DateTime64(3, 'UTC')
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org, dataset, run_name, ts, id)`, t.table("eval_traces")),
		fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  id           String,
  org          LowCardinality(String),
  name         LowCardinality(String),
  trace_id     String,
  run_name     String,
  dataset      LowCardinality(String),
  item_id      String,
  data_type    LowCardinality(String),
  value        Float64,
  string_value String,
  comment      String,
  ts           DateTime64(3, 'UTC')
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (org, name, run_name, ts, id)`, t.table("eval_scores")),
	}
	for _, ddl := range stmts {
		if err := datastore.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("evals telemetry: ensure table: %w", err)
		}
	}
	// Additive migrations: CREATE TABLE IF NOT EXISTS is a no-op on a table made
	// before these columns, so each attribution column also needs an idempotent
	// ADD COLUMN IF NOT EXISTS. Kept in lockstep with the DDL and the INSERT.
	et := t.table("eval_traces")
	migrations := []string{
		"ALTER TABLE " + et + " ADD COLUMN IF NOT EXISTS project LowCardinality(String)",
		"ALTER TABLE " + et + " ADD COLUMN IF NOT EXISTS session_id String",
		"ALTER TABLE " + et + " ADD COLUMN IF NOT EXISTS api_key_hash String",
		"ALTER TABLE " + et + " ADD COLUMN IF NOT EXISTS start_time DateTime64(3, 'UTC')",
		"ALTER TABLE " + et + " ADD COLUMN IF NOT EXISTS end_time DateTime64(3, 'UTC')",
	}
	for _, ddl := range migrations {
		if err := datastore.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("evals telemetry: migrate eval_traces: %w", err)
		}
	}
	return nil
}

func (t *dsTelemetry) RecordTrace(ctx context.Context, tr Trace) error {
	if tr.Org == "" {
		return fmt.Errorf("evals telemetry: trace missing org")
	}
	if tr.Timestamp.IsZero() {
		tr.Timestamp = time.Now().UTC()
	}
	if err := t.ready(ctx); err != nil {
		return err
	}
	return datastore.Exec(ctx, "INSERT INTO "+t.table("eval_traces")+
		` (id, org, project, name, dataset, item_id, run_name, session_id, api_key_hash, model, input, output, start_time, end_time, ts)`+
		` VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tr.ID, tr.Org, tr.ProjectID, tr.Name, tr.Dataset, tr.ItemID, tr.RunName,
		tr.SessionID, tr.APIKeyHash, tr.Model, tr.Input, tr.Output,
		tr.StartTime.UTC(), tr.EndTime.UTC(), tr.Timestamp.UTC())
}

func (t *dsTelemetry) RecordScore(ctx context.Context, sc ScoreEvent) error {
	if sc.Org == "" {
		return fmt.Errorf("evals telemetry: score missing org")
	}
	if !finite(sc.Value) {
		return fmt.Errorf("evals telemetry: non-finite score value")
	}
	if sc.Timestamp.IsZero() {
		sc.Timestamp = time.Now().UTC()
	}
	if err := t.ready(ctx); err != nil {
		return err
	}
	return datastore.Exec(ctx, "INSERT INTO "+t.table("eval_scores")+
		` (id, org, name, trace_id, run_name, dataset, item_id, data_type, value, string_value, comment, ts)`+
		` VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, sc.Org, sc.Name, sc.TraceID, sc.RunName, sc.Dataset,
		sc.ItemID, sc.DataType, sc.Value, sc.StringValue, sc.Comment, sc.Timestamp.UTC())
}

func (t *dsTelemetry) ListScores(ctx context.Context, f ScoreFilter) ([]ScoreEvent, error) {
	if f.Org == "" {
		return nil, fmt.Errorf("evals telemetry: scores read missing org")
	}
	if err := t.ready(ctx); err != nil {
		return nil, err
	}
	// Org bound as a positional parameter (?) — never interpolated. Optional
	// narrowers are likewise bound. A LIMIT is always applied.
	q := "SELECT id, org, name, trace_id, run_name, dataset, item_id, data_type, value, string_value, comment, ts FROM " +
		t.table("eval_scores") + " WHERE org = ?"
	args := []any{f.Org}
	if f.Name != "" {
		q += " AND name = ?"
		args = append(args, f.Name)
	}
	if f.RunName != "" {
		q += " AND run_name = ?"
		args = append(args, f.RunName)
	}
	if f.TraceID != "" {
		q += " AND trace_id = ?"
		args = append(args, f.TraceID)
	}
	q += " ORDER BY ts DESC LIMIT ?"
	args = append(args, uint64(boundedLimit(f.Limit)))

	rows, err := datastore.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("evals telemetry: query scores: %w", err)
	}
	out := make([]ScoreEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, ScoreEvent{
			ID:          asString(r["id"]),
			Org:         asString(r["org"]),
			Name:        asString(r["name"]),
			TraceID:     asString(r["trace_id"]),
			RunName:     asString(r["run_name"]),
			Dataset:     asString(r["dataset"]),
			ItemID:      asString(r["item_id"]),
			DataType:    asString(r["data_type"]),
			Value:       asFloat(r["value"]),
			StringValue: asString(r["string_value"]),
			Comment:     asString(r["comment"]),
			Timestamp:   asTime(r["ts"]),
		})
	}
	return out, nil
}

func (t *dsTelemetry) ListTraces(ctx context.Context, f TraceFilter) ([]Trace, error) {
	if f.Org == "" {
		return nil, fmt.Errorf("evals telemetry: traces read missing org")
	}
	if err := t.ready(ctx); err != nil {
		return nil, err
	}
	q := "SELECT id, org, project, name, dataset, item_id, run_name, session_id, api_key_hash, model, input, output, start_time, end_time, ts FROM " +
		t.table("eval_traces") + " WHERE org = ?"
	args := []any{f.Org}
	if f.ProjectID != "" {
		q += " AND project = ?"
		args = append(args, f.ProjectID)
	}
	if f.SessionID != "" {
		q += " AND session_id = ?"
		args = append(args, f.SessionID)
	}
	if f.RunName != "" {
		q += " AND run_name = ?"
		args = append(args, f.RunName)
	}
	if f.Dataset != "" {
		q += " AND dataset = ?"
		args = append(args, f.Dataset)
	}
	q += " ORDER BY ts DESC LIMIT ?"
	args = append(args, uint64(boundedLimit(f.Limit)))

	rows, err := datastore.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("evals telemetry: query traces: %w", err)
	}
	out := make([]Trace, 0, len(rows))
	for _, r := range rows {
		out = append(out, Trace{
			ID:         asString(r["id"]),
			Org:        asString(r["org"]),
			ProjectID:  asString(r["project"]),
			Name:       asString(r["name"]),
			Dataset:    asString(r["dataset"]),
			ItemID:     asString(r["item_id"]),
			RunName:    asString(r["run_name"]),
			SessionID:  asString(r["session_id"]),
			APIKeyHash: asString(r["api_key_hash"]),
			Model:      asString(r["model"]),
			Input:      asString(r["input"]),
			Output:     asString(r["output"]),
			StartTime:  asTime(r["start_time"]),
			EndTime:    asTime(r["end_time"]),
			Timestamp:  asTime(r["ts"]),
		})
	}
	return out, nil
}

// Close is a no-op: dsTelemetry does not own the shared datastore connection
// (aiobject does), so it has nothing to release.
func (t *dsTelemetry) Close() error { return nil }

// ── in-memory implementation (tests + telemetry-disabled fallback is nil) ─────

// memTelemetry is an in-memory Telemetry for tests. It enforces the SAME org
// isolation and finiteness invariants as the datastore impl so the security
// tests exercise real behavior without a datastore.
type memTelemetry struct {
	mu     sync.Mutex
	traces []Trace
	scores []ScoreEvent
}

func newMemTelemetry() *memTelemetry { return &memTelemetry{} }

func (m *memTelemetry) RecordTrace(_ context.Context, tr Trace) error {
	if tr.Org == "" {
		return fmt.Errorf("evals telemetry: trace missing org")
	}
	if tr.Timestamp.IsZero() {
		tr.Timestamp = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.traces = append(m.traces, tr)
	return nil
}

func (m *memTelemetry) RecordScore(_ context.Context, sc ScoreEvent) error {
	if sc.Org == "" {
		return fmt.Errorf("evals telemetry: score missing org")
	}
	if !finite(sc.Value) {
		return fmt.Errorf("evals telemetry: non-finite score value")
	}
	if sc.Timestamp.IsZero() {
		sc.Timestamp = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scores = append(m.scores, sc)
	return nil
}

func (m *memTelemetry) ListScores(_ context.Context, f ScoreFilter) ([]ScoreEvent, error) {
	if f.Org == "" {
		return nil, fmt.Errorf("evals telemetry: scores read missing org")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ScoreEvent
	for _, sc := range m.scores {
		if sc.Org != f.Org {
			continue
		}
		if f.Name != "" && sc.Name != f.Name {
			continue
		}
		if f.RunName != "" && sc.RunName != f.RunName {
			continue
		}
		if f.TraceID != "" && sc.TraceID != f.TraceID {
			continue
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return capSlice(out, boundedLimit(f.Limit)), nil
}

func (m *memTelemetry) ListTraces(_ context.Context, f TraceFilter) ([]Trace, error) {
	if f.Org == "" {
		return nil, fmt.Errorf("evals telemetry: traces read missing org")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Trace
	for _, tr := range m.traces {
		if tr.Org != f.Org {
			continue
		}
		if f.ProjectID != "" && tr.ProjectID != f.ProjectID {
			continue
		}
		if f.SessionID != "" && tr.SessionID != f.SessionID {
			continue
		}
		if f.RunName != "" && tr.RunName != f.RunName {
			continue
		}
		if f.Dataset != "" && tr.Dataset != f.Dataset {
			continue
		}
		out = append(out, tr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return capSlice(out, boundedLimit(f.Limit)), nil
}

func (m *memTelemetry) Close() error { return nil }

// ── helpers ──────────────────────────────────────────────────────────────────

const (
	defaultTelemetryLimit = 100
	maxTelemetryLimit     = 1000
)

func boundedLimit(n int) int {
	if n <= 0 {
		return defaultTelemetryLimit
	}
	if n > maxTelemetryLimit {
		return maxTelemetryLimit
	}
	return n
}

func capSlice[T any](xs []T, n int) []T {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// ── datastore row coercion ────────────────────────────────────────────────────
//
// datastore.Query returns each column already decoded into its native
// datastore scan type (String→string, Float64→float64, DateTime64→time.Time).
// These coercers accept the native value (and defensively its pointer form) so a
// nil/absent column degrades to a zero value rather than panicking.

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case *string:
		if s != nil {
			return *s
		}
	}
	return ""
}

func asFloat(v any) float64 {
	switch f := v.(type) {
	case float64:
		return f
	case *float64:
		if f != nil {
			return *f
		}
	case float32:
		return float64(f)
	}
	return 0
}

func asTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case *time.Time:
		if t != nil {
			return *t
		}
	}
	return time.Time{}
}

func getenvDefault(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// asInt64 — restored: metrics.go consumes it; #217 removed it with the
// observations read path it previously also served.
func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int16:
		return int64(n)
	case int8:
		return int64(n)
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case uint:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case *uint64:
		if n != nil {
			return int64(*n)
		}
	case *int64:
		if n != nil {
			return *n
		}
	}
	return 0
}
