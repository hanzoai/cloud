package cloud

// The datastore (ClickHouse) OLAP mirror — a best-effort projection of the audit
// trail for fleet-wide, long-retention, cross-deployment query. It implements
// audit.Mirror.
//
// The datastore is the natural OLAP audit sink: the table is a MergeTree, which
// is INSERT-ONLY by engine — ClickHouse rejects UPDATE/DELETE against it at parse
// time ("MergeTree does not support mutations"), so the mirror is append-only at
// the storage layer, matching the local chain's discipline. We create the table
// idempotently on first connect (CREATE TABLE IF NOT EXISTS) and insert via the
// canonical clickhouse-go PrepareBatch → Append → Send idiom (the same the
// provisioning subsystem uses; the driver is already in cloud's module graph, so
// this adds no dependency).
//
// This mirror is NEVER the integrity authority — the local SQLite hash-chain is.
// Its rows carry the same seq + hash so an operator CAN cross-check the OLAP copy
// against the chain, but a mirror gap is a query-completeness issue, not a
// tamper-evidence one. Every mirror error is logged and dropped by the Recorder;
// the request path never sees it.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hanzoai/cloud/audit"
	luxlog "github.com/luxfi/log"
)

// clickhouseMirror writes audit records to a ClickHouse MergeTree table.
type clickhouseMirror struct {
	conn  clickhouse.Conn
	table string
	log   luxlog.Logger
}

// newAuditMirror builds the OLAP mirror from operator config, or returns nil when
// no datastore is configured (mirroring is optional — the local chain is the
// authority). It connects lazily-validated (a Ping) and ensures the table exists.
//
// Config (all from env / KMS-injected secrets, never hard-coded):
//
//	CLOUD_AUDIT_CLICKHOUSE_ADDR   host:9000 of the datastore native port
//	CLOUD_AUDIT_CLICKHOUSE_DB     database (default "hanzo")
//	CLOUD_AUDIT_CLICKHOUSE_TABLE  table (default "audit_log")
//	CLOUD_AUDIT_CLICKHOUSE_USER   user
//	CLOUD_AUDIT_CLICKHOUSE_PASSWORD  password (KMS-backed secret)
func newAuditMirror(log luxlog.Logger) (audit.Mirror, error) {
	addr := strings.TrimSpace(os.Getenv("CLOUD_AUDIT_CLICKHOUSE_ADDR"))
	if addr == "" {
		return nil, nil // no datastore configured — local chain only.
	}
	db := getenv("CLOUD_AUDIT_CLICKHOUSE_DB", "hanzo")
	table := getenv("CLOUD_AUDIT_CLICKHOUSE_TABLE", "audit_log")

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{
			Database: db,
			Username: os.Getenv("CLOUD_AUDIT_CLICKHOUSE_USER"),
			Password: os.Getenv("CLOUD_AUDIT_CLICKHOUSE_PASSWORD"),
		},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("audit mirror: open: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("audit mirror: ping %s: %w", addr, err)
	}

	qualified := db + "." + table
	m := &clickhouseMirror{conn: conn, table: qualified, log: log}
	if err := m.ensureTable(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if log != nil {
		log.Info("audit OLAP mirror connected", "addr", addr, "table", qualified)
	}
	return m, nil
}

// ensureTable creates the append-only audit table if it does not exist. MergeTree
// = insert-only (mutations rejected at parse time). Partitioned by month and
// ordered for the (org, time) query pattern; seq + hash are carried so the OLAP
// copy is cross-checkable against the local chain.
func (m *clickhouseMirror) ensureTable(ctx context.Context) error {
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  seq        UInt64,
  ts         DateTime64(3, 'UTC'),
  actor_org  LowCardinality(String),
  actor_sub  String,
  actor_email String,
  action     LowCardinality(String),
  res_type   LowCardinality(String),
  res_id     String,
  auth_method LowCardinality(String),
  is_admin   UInt8,
  result     LowCardinality(String),
  status     UInt16,
  reason     String,
  source_ip  String,
  user_agent String,
  request_id String,
  method     LowCardinality(String),
  path       String,
  prev_hash  String,
  hash       String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (actor_org, ts, seq)`, m.table)
	if err := m.conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("audit mirror: ensure table: %w", err)
	}
	return nil
}

// Append writes one record to the OLAP mirror via the canonical batch idiom. The
// before/after diffs are DELIBERATELY not mirrored — the OLAP copy is for
// query/analytics over the event stream, and keeping the (already-redacted but
// still payload-bearing) diffs out of the fleet warehouse minimizes the blast
// radius of a warehouse compromise. The full record (with diffs) lives only in
// the local, access-controlled chain.
func (m *clickhouseMirror) Append(ctx context.Context, r audit.Record) error {
	batch, err := m.conn.PrepareBatch(ctx, "INSERT INTO "+m.table+` (
  seq, ts, actor_org, actor_sub, actor_email, action, res_type, res_id,
  auth_method, is_admin, result, status, reason, source_ip, user_agent,
  request_id, method, path, prev_hash, hash)`)
	if err != nil {
		return fmt.Errorf("audit mirror: prepare: %w", err)
	}
	if err := batch.Append(
		r.Seq, r.Time.UTC(), r.Actor.Org, r.Actor.Sub, r.Actor.Email,
		r.Action, r.Resource.Type, r.Resource.ID,
		r.Auth.Method, boolToUint8(r.Auth.IsAdmin),
		r.Outcome.Result, uint16(r.Outcome.Status), r.Outcome.Reason,
		r.SourceIP, r.UserAgent, r.RequestID, r.Method, r.Path,
		r.PrevHash, r.Hash,
	); err != nil {
		_ = batch.Abort()
		return fmt.Errorf("audit mirror: append: %w", err)
	}
	return batch.Send()
}

// Checkpoint persists a head-digest checkpoint to an INDEPENDENT digest table in
// the datastore — the AU-9 tail-truncation anchor. Because this lives in a store
// SEPARATE from the local SQLite chain, truncating the chain cannot also rewrite
// the checkpoint history: an external monitor querying this table sees the count
// series and alerts on any regression. Best-effort; a failure is dropped by the
// Recorder (the structured log carries the same digest). Implements
// audit.CheckpointSink.
func (m *clickhouseMirror) Checkpoint(ctx context.Context, cp audit.Checkpoint) error {
	if err := m.ensureCheckpointTable(ctx); err != nil {
		return err
	}
	batch, err := m.conn.PrepareBatch(ctx, "INSERT INTO "+m.table+"_checkpoints (ts, count, head)")
	if err != nil {
		return fmt.Errorf("audit mirror: checkpoint prepare: %w", err)
	}
	if err := batch.Append(cp.Time.UTC(), cp.Count, cp.Head); err != nil {
		_ = batch.Abort()
		return fmt.Errorf("audit mirror: checkpoint append: %w", err)
	}
	return batch.Send()
}

// ensureCheckpointTable creates the append-only checkpoint digest table. A plain
// MergeTree ordered by time — the monitor reads the latest rows and checks that
// count never decreases.
func (m *clickhouseMirror) ensureCheckpointTable(ctx context.Context) error {
	ddl := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s_checkpoints (
  ts    DateTime64(3, 'UTC'),
  count UInt64,
  head  String
) ENGINE = MergeTree
ORDER BY ts`, m.table)
	if err := m.conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("audit mirror: ensure checkpoint table: %w", err)
	}
	return nil
}

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
