package eval

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	luxlog "github.com/luxfi/log"
)

// The eval TELEMETRY store is the high-volume, append-only half of the storage
// split (CTO directive): traces, observations and scores-as-events land in
// datastore (hanzoai's ClickHouse fork), NOT SQLite. datastore is THE backend
// for all AI observability, so this is the same MergeTree, insert-only discipline
// the audit OLAP mirror uses (audit_mirror.go) and it reuses the Langfuse v3
// ClickHouse shapes (traces/observations/scores) since datastore IS ClickHouse.
//
// SECURITY (tenant isolation on OLAP):
//   - Every table carries `org` LowCardinality(String), NOT a nullable, and it is
//     the FIRST key in ORDER BY so tenant reads are a prefix scan.
//   - Every read predicate binds org as a NAMED PARAMETER ({org:String}) — never
//     string-interpolated. ClickHouse SQL injection through a crafted org/dataset
//     is thereby impossible; the value is bound by the driver.
//   - Every read carries a LIMIT (bounded response, no OLAP-scan DoS).
//   - Scores are events: a MergeTree rejects UPDATE/DELETE at parse time, so a
//     recorded score is immutable — the integrity property Red will probe. Score
//     VALUES are validated (finite, in-range) at the API boundary before they
//     ever reach Record; this store additionally refuses non-finite values as a
//     defense-in-depth backstop.
//
// The interface makes the store swappable: production wires the ClickHouse impl;
// tests use memTelemetry (in-memory) so the store/API/orchestrator are testable
// without a ClickHouse instance. The orchestrator composes Telemetry with the
// metastore + gateway + judge; it owns none of their internals.

// Trace is one model-under-test invocation recorded during a run. Input/Output
// are opaque JSON/text; metadata carries the run/dataset/item linkage.
type Trace struct {
	ID        string
	Org       string
	Name      string
	Dataset   string
	ItemID    string
	RunName   string
	Model     string
	Input     string
	Output    string
	Timestamp time.Time
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

// TraceFilter bounds a traces read. Org is MANDATORY.
type TraceFilter struct {
	Org     string
	RunName string
	Dataset string
	Limit   int
}

// Telemetry is the append-only event store for eval traces + scores. Every
// method takes an authoritative org (via the value's Org field or the filter);
// no method can read across orgs.
type Telemetry interface {
	RecordTrace(ctx context.Context, t Trace) error
	RecordScore(ctx context.Context, sc ScoreEvent) error
	ListScores(ctx context.Context, f ScoreFilter) ([]ScoreEvent, error)
	ListTraces(ctx context.Context, f TraceFilter) ([]Trace, error)
	Close() error
}

// ── ClickHouse implementation ────────────────────────────────────────────────

// chTelemetry writes eval telemetry to datastore (ClickHouse) MergeTree tables.
type chTelemetry struct {
	conn clickhouse.Conn
	db   string
	log  luxlog.Logger
}

// newCHTelemetry builds the datastore-backed telemetry store from operator
// config, or returns (nil, nil) when no datastore is configured — the caller
// then runs with telemetry disabled (traces/scores are not persisted, and it
// says so; never a fake success). Config mirrors the audit mirror's env keys
// under the EVALS namespace; the password is a KMS-injected secret, never
// hard-coded.
//
//	CLOUD_EVALS_CLICKHOUSE_ADDR      host:9000 of the datastore native port
//	CLOUD_EVALS_CLICKHOUSE_DB        database (default "hanzo")
//	CLOUD_EVALS_CLICKHOUSE_USER      user
//	CLOUD_EVALS_CLICKHOUSE_PASSWORD  password (KMS-backed secret)
func newCHTelemetry(log luxlog.Logger) (Telemetry, error) {
	addr := strings.TrimSpace(os.Getenv("CLOUD_EVALS_CLICKHOUSE_ADDR"))
	if addr == "" {
		return nil, nil // no datastore configured — telemetry disabled.
	}
	db := getenvDefault("CLOUD_EVALS_CLICKHOUSE_DB", "hanzo")
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{
			Database: db,
			Username: os.Getenv("CLOUD_EVALS_CLICKHOUSE_USER"),
			Password: os.Getenv("CLOUD_EVALS_CLICKHOUSE_PASSWORD"),
		},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("evals telemetry: open: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("evals telemetry: ping %s: %w", addr, err)
	}
	t := &chTelemetry{conn: conn, db: db, log: log}
	if err := t.ensureTables(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if log != nil {
		log.Info("evals telemetry connected", "addr", addr, "db", db)
	}
	return t, nil
}

func (t *chTelemetry) table(name string) string { return t.db + "." + name }

// ensureTables creates the append-only telemetry tables idempotently. These are
// the datastore projections of the Langfuse v3 traces/observations/scores model:
// MergeTree (insert-only), partitioned by month, ordered org-first so a tenant's
// reads are a contiguous prefix.
func (t *chTelemetry) ensureTables(ctx context.Context) error {
	stmts := []string{
		fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  id         String,
  org        LowCardinality(String),
  name       String,
  dataset    LowCardinality(String),
  item_id    String,
  run_name   String,
  model      LowCardinality(String),
  input      String,
  output     String,
  ts         DateTime64(3, 'UTC')
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
		if err := t.conn.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("evals telemetry: ensure table: %w", err)
		}
	}
	return nil
}

func (t *chTelemetry) RecordTrace(ctx context.Context, tr Trace) error {
	if tr.Org == "" {
		return fmt.Errorf("evals telemetry: trace missing org")
	}
	if tr.Timestamp.IsZero() {
		tr.Timestamp = time.Now().UTC()
	}
	batch, err := t.conn.PrepareBatch(ctx, "INSERT INTO "+t.table("eval_traces")+
		` (id, org, name, dataset, item_id, run_name, model, input, output, ts)`)
	if err != nil {
		return fmt.Errorf("evals telemetry: prepare trace: %w", err)
	}
	if err := batch.Append(tr.ID, tr.Org, tr.Name, tr.Dataset, tr.ItemID, tr.RunName,
		tr.Model, tr.Input, tr.Output, tr.Timestamp.UTC()); err != nil {
		_ = batch.Abort()
		return fmt.Errorf("evals telemetry: append trace: %w", err)
	}
	return batch.Send()
}

func (t *chTelemetry) RecordScore(ctx context.Context, sc ScoreEvent) error {
	if sc.Org == "" {
		return fmt.Errorf("evals telemetry: score missing org")
	}
	if !finite(sc.Value) {
		return fmt.Errorf("evals telemetry: non-finite score value")
	}
	if sc.Timestamp.IsZero() {
		sc.Timestamp = time.Now().UTC()
	}
	batch, err := t.conn.PrepareBatch(ctx, "INSERT INTO "+t.table("eval_scores")+
		` (id, org, name, trace_id, run_name, dataset, item_id, data_type, value, string_value, comment, ts)`)
	if err != nil {
		return fmt.Errorf("evals telemetry: prepare score: %w", err)
	}
	if err := batch.Append(sc.ID, sc.Org, sc.Name, sc.TraceID, sc.RunName, sc.Dataset,
		sc.ItemID, sc.DataType, sc.Value, sc.StringValue, sc.Comment, sc.Timestamp.UTC()); err != nil {
		_ = batch.Abort()
		return fmt.Errorf("evals telemetry: append score: %w", err)
	}
	return batch.Send()
}

func (t *chTelemetry) ListScores(ctx context.Context, f ScoreFilter) ([]ScoreEvent, error) {
	if f.Org == "" {
		return nil, fmt.Errorf("evals telemetry: scores read missing org")
	}
	// Org bound as a NAMED PARAMETER — never interpolated. Optional narrowers are
	// likewise bound. A LIMIT is always applied.
	q := "SELECT id, org, name, trace_id, run_name, dataset, item_id, data_type, value, string_value, comment, ts FROM " +
		t.table("eval_scores") + " WHERE org = {org:String}"
	args := []any{clickhouse.Named("org", f.Org)}
	if f.Name != "" {
		q += " AND name = {name:String}"
		args = append(args, clickhouse.Named("name", f.Name))
	}
	if f.RunName != "" {
		q += " AND run_name = {run:String}"
		args = append(args, clickhouse.Named("run", f.RunName))
	}
	if f.TraceID != "" {
		q += " AND trace_id = {trace:String}"
		args = append(args, clickhouse.Named("trace", f.TraceID))
	}
	q += " ORDER BY ts DESC LIMIT {lim:UInt32}"
	args = append(args, clickhouse.Named("lim", uint32(boundedLimit(f.Limit))))

	rows, err := t.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("evals telemetry: query scores: %w", err)
	}
	defer rows.Close()
	var out []ScoreEvent
	for rows.Next() {
		var sc ScoreEvent
		if err := rows.Scan(&sc.ID, &sc.Org, &sc.Name, &sc.TraceID, &sc.RunName, &sc.Dataset,
			&sc.ItemID, &sc.DataType, &sc.Value, &sc.StringValue, &sc.Comment, &sc.Timestamp); err != nil {
			return nil, fmt.Errorf("evals telemetry: scan score: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (t *chTelemetry) ListTraces(ctx context.Context, f TraceFilter) ([]Trace, error) {
	if f.Org == "" {
		return nil, fmt.Errorf("evals telemetry: traces read missing org")
	}
	q := "SELECT id, org, name, dataset, item_id, run_name, model, input, output, ts FROM " +
		t.table("eval_traces") + " WHERE org = {org:String}"
	args := []any{clickhouse.Named("org", f.Org)}
	if f.RunName != "" {
		q += " AND run_name = {run:String}"
		args = append(args, clickhouse.Named("run", f.RunName))
	}
	if f.Dataset != "" {
		q += " AND dataset = {ds:String}"
		args = append(args, clickhouse.Named("ds", f.Dataset))
	}
	q += " ORDER BY ts DESC LIMIT {lim:UInt32}"
	args = append(args, clickhouse.Named("lim", uint32(boundedLimit(f.Limit))))

	rows, err := t.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("evals telemetry: query traces: %w", err)
	}
	defer rows.Close()
	var out []Trace
	for rows.Next() {
		var tr Trace
		if err := rows.Scan(&tr.ID, &tr.Org, &tr.Name, &tr.Dataset, &tr.ItemID, &tr.RunName,
			&tr.Model, &tr.Input, &tr.Output, &tr.Timestamp); err != nil {
			return nil, fmt.Errorf("evals telemetry: scan trace: %w", err)
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

func (t *chTelemetry) Close() error {
	if t.conn == nil {
		return nil
	}
	return t.conn.Close()
}

// ── in-memory implementation (tests + telemetry-disabled fallback is nil) ─────

// memTelemetry is an in-memory Telemetry for tests. It enforces the SAME org
// isolation and finiteness invariants as the ClickHouse impl so the security
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

func getenvDefault(key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
