package automations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest; !cgo → pure-Go modernc). Blank import registers it. This
	// mirrors clients/crm exactly — the ONE storage pattern.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors mapped to HTTP status by the handlers: errNotFound → 404,
// errBadRef → 422.
var (
	errNotFound = errors.New("automations: not found")
	errBadRef   = errors.New("automations: referenced record not found in org")
)

// Store is the automations database. ONE SQLite file ({DataDir}/automations.db)
// holds every org's flows, versions, and runs; tenant isolation is the `org`
// column, physical on EVERY uniqueness + lookup index (each leads with org).
// MaxOpenConns(1) serializes writes against the single-writer WAL file.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the three tables. Idempotent (IF NOT EXISTS). Every uniqueness
// + lookup index leads with `org`, so tenant isolation is a physical property of
// the index, not just a WHERE clause.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS automations_flows (
  id                   TEXT PRIMARY KEY,
  org                  TEXT NOT NULL,
  external_id          TEXT NOT NULL DEFAULT '',
  folder_id            TEXT NOT NULL DEFAULT '',
  status               TEXT NOT NULL DEFAULT 'DISABLED',
  published_version_id TEXT NOT NULL DEFAULT '',
  metadata             TEXT NOT NULL DEFAULT '',
  created              INTEGER NOT NULL,
  updated              INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_auto_flows_org_updated  ON automations_flows(org, updated);
CREATE INDEX IF NOT EXISTS ix_auto_flows_org_status   ON automations_flows(org, status);

CREATE TABLE IF NOT EXISTS automations_flow_versions (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  flow_id        TEXT NOT NULL,
  display_name   TEXT NOT NULL DEFAULT '',
  trigger_json   TEXT NOT NULL DEFAULT '',
  valid          INTEGER NOT NULL DEFAULT 0,
  state          TEXT NOT NULL DEFAULT 'DRAFT',
  schema_version TEXT NOT NULL DEFAULT '',
  created        INTEGER NOT NULL,
  updated        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_auto_versions_org_flow    ON automations_flow_versions(org, flow_id, created);

CREATE TABLE IF NOT EXISTS automations_runs (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  flow_id         TEXT NOT NULL,
  flow_version_id TEXT NOT NULL,
  workflow_id     TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'RUNNING',
  start_time      INTEGER NOT NULL DEFAULT 0,
  finish_time     INTEGER NOT NULL DEFAULT 0,
  -- metered is the EXACTLY-ONCE billing/audit idempotency flag: a run is metered
  -- + audited by whichever caller flips it 0→1 (ClaimMeter), so the manual, MCP,
  -- and scheduled-cron entrypoints together bill a run at most once (MED-1).
  metered         INTEGER NOT NULL DEFAULT 0,
  created         INTEGER NOT NULL,
  updated         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_auto_runs_org_created ON automations_runs(org, created);
CREATE INDEX IF NOT EXISTS ix_auto_runs_org_flow    ON automations_runs(org, flow_id, created);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("automations migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ── Flow ────────────────────────────────────────────────────────────────────

const flowCols = `id,org,external_id,folder_id,status,published_version_id,metadata,created,updated`

func scanFlow(sc interface{ Scan(...any) error }) (Flow, error) {
	var f Flow
	var meta string
	err := sc.Scan(&f.ID, &f.Org, &f.ExternalID, &f.FolderID, &f.Status,
		&f.PublishedVersionID, &meta, &f.Created, &f.Updated)
	if meta != "" {
		f.Metadata = json.RawMessage(meta)
	}
	return f, err
}

func (s *Store) CreateFlow(ctx context.Context, f Flow) (Flow, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_flows (`+flowCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Org, f.ExternalID, f.FolderID, f.Status, f.PublishedVersionID,
		string(f.Metadata), f.Created, f.Updated); err != nil {
		return Flow{}, fmt.Errorf("insert flow: %w", err)
	}
	return f, nil
}

func (s *Store) GetFlow(ctx context.Context, org, id string) (Flow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+flowCols+` FROM automations_flows WHERE org=? AND id=?`, org, id)
	f, err := scanFlow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Flow{}, errNotFound
	}
	if err != nil {
		return Flow{}, fmt.Errorf("get flow: %w", err)
	}
	return f, nil
}

func (s *Store) ListFlows(ctx context.Context, org string, limit int) ([]Flow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+flowCols+` FROM automations_flows WHERE org=? ORDER BY updated DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list flows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Flow, 0, 16)
	for rows.Next() {
		f, err := scanFlow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan flow: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateFlow persists the mutable flow fields (status, folder, published version,
// metadata) for (org,id). RowsAffected==0 ⇒ errNotFound (cross-tenant or missing).
func (s *Store) UpdateFlow(ctx context.Context, f Flow) (Flow, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_flows SET external_id=?,folder_id=?,status=?,published_version_id=?,metadata=?,updated=? WHERE org=? AND id=?`,
		f.ExternalID, f.FolderID, f.Status, f.PublishedVersionID, string(f.Metadata), f.Updated, f.Org, f.ID)
	if err != nil {
		return Flow{}, fmt.Errorf("update flow: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Flow{}, errNotFound
	}
	return s.GetFlow(ctx, f.Org, f.ID)
}

// DeleteFlow removes a flow and all its versions + runs within the org. One
// transaction so a partial delete never strands a version/run.
func (s *Store) DeleteFlow(ctx context.Context, org, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM automations_flows WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete flow: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automations_flow_versions WHERE org=? AND flow_id=?`, org, id); err != nil {
		return false, fmt.Errorf("delete versions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automations_runs WHERE org=? AND flow_id=?`, org, id); err != nil {
		return false, fmt.Errorf("delete runs: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// ── FlowVersion ─────────────────────────────────────────────────────────────

const versionCols = `id,org,flow_id,display_name,trigger_json,valid,state,schema_version,created,updated`

func scanVersion(sc interface{ Scan(...any) error }) (FlowVersion, error) {
	var v FlowVersion
	var trig string
	var valid int
	err := sc.Scan(&v.ID, &v.Org, &v.FlowID, &v.DisplayName, &trig, &valid,
		&v.State, &v.SchemaVersion, &v.Created, &v.Updated)
	if err != nil {
		return FlowVersion{}, err
	}
	v.Valid = valid != 0
	if trig != "" {
		var t FlowTrigger
		if uerr := json.Unmarshal([]byte(trig), &t); uerr != nil {
			return FlowVersion{}, fmt.Errorf("decode trigger: %w", uerr)
		}
		v.Trigger = &t
	}
	return v, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CreateVersion inserts a version. The flow must exist in the SAME org (validated
// here, not by a SQL FK, so a cross-tenant flow_id can never anchor a version).
func (s *Store) CreateVersion(ctx context.Context, v FlowVersion) (FlowVersion, error) {
	ok, err := s.flowExists(ctx, v.Org, v.FlowID)
	if err != nil {
		return FlowVersion{}, err
	}
	if !ok {
		return FlowVersion{}, errBadRef
	}
	trig, err := marshalTrigger(v.Trigger)
	if err != nil {
		return FlowVersion{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_flow_versions (`+versionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Org, v.FlowID, v.DisplayName, trig, b2i(v.Valid), v.State, v.SchemaVersion, v.Created, v.Updated); err != nil {
		return FlowVersion{}, fmt.Errorf("insert version: %w", err)
	}
	return v, nil
}

func (s *Store) GetVersion(ctx context.Context, org, id string) (FlowVersion, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+versionCols+` FROM automations_flow_versions WHERE org=? AND id=?`, org, id)
	v, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowVersion{}, errNotFound
	}
	if err != nil {
		return FlowVersion{}, fmt.Errorf("get version: %w", err)
	}
	return v, nil
}

func (s *Store) ListVersions(ctx context.Context, org, flowID string, limit int) ([]FlowVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+versionCols+` FROM automations_flow_versions WHERE org=? AND flow_id=? ORDER BY created DESC LIMIT ?`, org, flowID, limit)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]FlowVersion, 0, 8)
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// LatestVersion returns the most-recently-created version for a flow, or
// errNotFound if the flow has none.
func (s *Store) LatestVersion(ctx context.Context, org, flowID string) (FlowVersion, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+versionCols+` FROM automations_flow_versions WHERE org=? AND flow_id=? ORDER BY created DESC LIMIT 1`, org, flowID)
	v, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowVersion{}, errNotFound
	}
	if err != nil {
		return FlowVersion{}, fmt.Errorf("latest version: %w", err)
	}
	return v, nil
}

// UpdateVersion replaces a version's editable content (display name, trigger tree,
// valid, state). RowsAffected==0 ⇒ errNotFound.
func (s *Store) UpdateVersion(ctx context.Context, v FlowVersion) (FlowVersion, error) {
	trig, err := marshalTrigger(v.Trigger)
	if err != nil {
		return FlowVersion{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_flow_versions SET display_name=?,trigger_json=?,valid=?,state=?,schema_version=?,updated=? WHERE org=? AND id=?`,
		v.DisplayName, trig, b2i(v.Valid), v.State, v.SchemaVersion, v.Updated, v.Org, v.ID)
	if err != nil {
		return FlowVersion{}, fmt.Errorf("update version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return FlowVersion{}, errNotFound
	}
	return s.GetVersion(ctx, v.Org, v.ID)
}

// ── FlowRun ─────────────────────────────────────────────────────────────────

const runCols = `id,org,flow_id,flow_version_id,workflow_id,status,start_time,finish_time,created,updated`

func scanRun(sc interface{ Scan(...any) error }) (FlowRun, error) {
	var r FlowRun
	err := sc.Scan(&r.ID, &r.Org, &r.FlowID, &r.FlowVersionID, &r.WorkflowID,
		&r.Status, &r.StartTime, &r.FinishTime, &r.Created, &r.Updated)
	return r, err
}

func (s *Store) CreateRun(ctx context.Context, r FlowRun) (FlowRun, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_runs (`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Org, r.FlowID, r.FlowVersionID, r.WorkflowID, r.Status,
		r.StartTime, r.FinishTime, r.Created, r.Updated); err != nil {
		return FlowRun{}, fmt.Errorf("insert run: %w", err)
	}
	return r, nil
}

// CreateRunIfAbsent inserts a run row keyed on its id, doing NOTHING if it already
// exists. It reports whether THIS call created the row. Idempotent by run id (=the
// workflow execution id), so a retried run-start bookkeeping step, or a manual
// handler + the durable path racing to record the same run, converge on one row.
func (s *Store) CreateRunIfAbsent(ctx context.Context, r FlowRun) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_runs (`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		r.ID, r.Org, r.FlowID, r.FlowVersionID, r.WorkflowID, r.Status,
		r.StartTime, r.FinishTime, r.Created, r.Updated)
	if err != nil {
		return false, fmt.Errorf("insert run if absent: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClaimMeter atomically flips the run's metered flag 0→1 for (org,id) and reports
// whether THIS call won the flip. It is the exactly-once billing gate: only the
// winner meters + audits the run, so no entrypoint double-bills a run (MED-1). The
// (org,id) predicate keeps the claim tenant-scoped.
func (s *Store) ClaimMeter(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET metered=1 WHERE org=? AND id=? AND metered=0`, org, id)
	if err != nil {
		return false, fmt.Errorf("claim meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) GetRun(ctx context.Context, org, id string) (FlowRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM automations_runs WHERE org=? AND id=?`, org, id)
	r, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return FlowRun{}, errNotFound
	}
	if err != nil {
		return FlowRun{}, fmt.Errorf("get run: %w", err)
	}
	return r, nil
}

func (s *Store) ListRuns(ctx context.Context, org, flowID string, limit int) ([]FlowRun, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if flowID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+runCols+` FROM automations_runs WHERE org=? ORDER BY created DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+runCols+` FROM automations_runs WHERE org=? AND flow_id=? ORDER BY created DESC LIMIT ?`, org, flowID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]FlowRun, 0, 16)
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRunStatus persists a terminal/observed status transition for (org,id).
func (s *Store) UpdateRunStatus(ctx context.Context, org, id string, status FlowRunStatus, finish, updated int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET status=?,finish_time=?,updated=? WHERE org=? AND id=?`,
		status, finish, updated, org, id)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// flowExists reports whether (org,flowID) names a flow — used to keep a version's
// flow_id inside the tenant before a write (a cross-tenant ref would be errBadRef).
func (s *Store) flowExists(ctx context.Context, org, flowID string) (bool, error) {
	if flowID == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM automations_flows WHERE org=? AND id=?`, org, flowID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("flow exists: %w", err)
	}
	return true, nil
}

// marshalTrigger encodes a version's trigger tree for storage. A nil trigger
// stores an empty string (an EMPTY/unconfigured flow), never the literal "null".
func marshalTrigger(t *FlowTrigger) (string, error) {
	if t == nil {
		return "", nil
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("encode trigger: %w", err)
	}
	return string(b), nil
}
