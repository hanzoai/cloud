package auto

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	// locus.Use is the ONE opener (it moves the store off its file first, when asked): with CLOUD_SQL_DSN set it is this subsystem's
	// schema on hanzo-sql; without, its encrypted SQLite file under the system
	// namespace, keyed by cek for that name and capped to one connection.
	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver; the blank import
	// registers the "sqlite" database/sql name. Mirrors clients/crm exactly — the
	// ONE storage pattern.

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors mapped to HTTP status by the handlers: errNotFound → 404,
// errBadRef → 422.
var (
	errNotFound = errors.New("auto: not found")
	errBadRef   = errors.New("auto: referenced record not found in org")
)

// Store is this app's database. ONE database — the system namespace's
// "automations", a SQLite file or a schema on hanzo-sql — holds every org's flows,
// versions, and runs; tenant isolation is the `org` column, physical on EVERY
// uniqueness + lookup index (each leads with org). Its SQL is the dialect both
// engines share (sdk/dialect) and its tests run on both (internal/sqltest).
// On a file MaxOpenConns(1) serializes writes against the single-writer WAL file;
// on hanzo-sql a transaction that reads and then writes starts with BeginWrite.
//
// THE SUB NAME IS "automations" AND STAYS THAT WAY. The app is called auto now,
// but this string is not the app's name — it is the file's, and cek derives the
// key from it. Change it and every org's flows, versions and runs are still on
// disk beside a .dek sidecar nothing opens any more: not a migration, a loss.
// The fleet renames; the bytes do not.
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	s, err := locus.Use(context.Background(), nil, namespace.System(), "automations", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open automations store: %w", err)
	}
	return s, nil
}

// migrate brings the database to the current schema in ONE transaction that holds
// the store's migration lock (two replicas opening one store must not run its DDL at
// once). Everything below goes through tx and nothing through s.db. Every uniqueness
// + lookup index leads with `org`, so tenant isolation is a physical property of the
// index, not just a WHERE clause.
func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error { return migrateIn(ctx, d, tx) }); err != nil {
		return fmt.Errorf("automations migrate: %w", err)
	}
	return nil
}

func migrateIn(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS automations_flows (
  id                   TEXT PRIMARY KEY,
  org                  TEXT NOT NULL,
  external_id          TEXT NOT NULL DEFAULT '',
  folder_id            TEXT NOT NULL DEFAULT '',
  status               TEXT NOT NULL DEFAULT 'DISABLED',
  published_version_id TEXT NOT NULL DEFAULT '',
  metadata             TEXT NOT NULL DEFAULT '',
  created              BIGINT NOT NULL,
  updated              BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_auto_flows_org_updated  ON automations_flows(org, updated);
CREATE INDEX IF NOT EXISTS ix_auto_flows_org_status   ON automations_flows(org, status);

CREATE TABLE IF NOT EXISTS automations_flow_versions (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  flow_id        TEXT NOT NULL,
  display_name   TEXT NOT NULL DEFAULT '',
  trigger_json   TEXT NOT NULL DEFAULT '',
  valid          BIGINT NOT NULL DEFAULT 0,
  state          TEXT NOT NULL DEFAULT 'DRAFT',
  schema_version TEXT NOT NULL DEFAULT '',
  created        BIGINT NOT NULL,
  updated        BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_auto_versions_org_flow    ON automations_flow_versions(org, flow_id, created);

CREATE TABLE IF NOT EXISTS automations_runs (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  flow_id         TEXT NOT NULL,
  flow_version_id TEXT NOT NULL,
  workflow_id     TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'RUNNING',
  start_time      BIGINT NOT NULL DEFAULT 0,
  finish_time     BIGINT NOT NULL DEFAULT 0,
  -- metered is the EXACTLY-ONCE billing/audit idempotency flag: a run is metered
  -- + audited by whichever caller flips it 0→1 (ClaimMeter), so the manual, MCP,
  -- and scheduled-cron entrypoints together bill a run at most once (MED-1).
  metered         BIGINT NOT NULL DEFAULT 0,
  created         BIGINT NOT NULL,
  updated         BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_auto_runs_org_created ON automations_runs(org, created);
CREATE INDEX IF NOT EXISTS ix_auto_runs_org_flow    ON automations_runs(org, flow_id, created);

-- automations_triggers is the WEBHOOK/APP_WEBHOOK routing index: one row per enabled
-- event-triggered flow, maintained by setEnabled (the inbound analog of the POLLING
-- cron schedule). An inbound event matches on (org, provider, event); the match index
-- leads with org so a delivered event can only ever reach its own tenant's flows. One
-- subscription per flow (PK org, flow_id): a flow has exactly one trigger.
CREATE TABLE IF NOT EXISTS automations_triggers (
  org        TEXT NOT NULL,
  provider   TEXT NOT NULL,
  event      TEXT NOT NULL,
  flow_id    TEXT NOT NULL,
  version_id TEXT NOT NULL,
  created    BIGINT NOT NULL,
  PRIMARY KEY (org, flow_id)
);
CREATE INDEX IF NOT EXISTS ix_auto_triggers_match ON automations_triggers(org, provider, event);

-- automations_ticks is every schedule tick a flow took (ClaimTick): one row per
-- (flow, tick), so a tick is run once whenever its start arrives.
CREATE TABLE IF NOT EXISTS automations_ticks (
  org  TEXT NOT NULL,
  flow TEXT NOT NULL,
  key  TEXT NOT NULL,
  at   BIGINT NOT NULL,
  PRIMARY KEY (org, flow, key)
);
CREATE INDEX IF NOT EXISTS ix_auto_ticks_at ON automations_ticks(org, flow, at);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	// Columns added after the tables first shipped. Neither engine has ADD COLUMN IF
	// NOT EXISTS that the other parses, so each is added only when the table does not
	// carry it yet.
	for _, c := range []struct{ table, name, decl string }{
		// The person a flow's agent step runs as, and where its notice goes: whoever
		// last said what the flow does (automation.go).
		{"automations_flows", "owner_sub", "TEXT NOT NULL DEFAULT ''"},
		{"automations_flows", "owner_email", "TEXT NOT NULL DEFAULT ''"},
		// The Dev run an agent step started, and the one line that says how it went.
		{"automations_runs", "session", "TEXT NOT NULL DEFAULT ''"},
		{"automations_runs", "summary", "TEXT NOT NULL DEFAULT ''"},
		// The person an agent step's run was made as, and their address: whose run
		// it stays, whoever the automation belongs to later (agent.go).
		{"automations_runs", "person", "TEXT NOT NULL DEFAULT ''"},
		{"automations_runs", "email", "TEXT NOT NULL DEFAULT ''"},
		// Who pressed Run; empty for a schedule's tick.
		{"automations_runs", "actor", "TEXT NOT NULL DEFAULT ''"},
		// What a posting automation's agent wrote, and where it went: one JSON
		// result per account (post.go).
		{"automations_runs", "draft", "TEXT NOT NULL DEFAULT ''"},
		{"automations_runs", "posts", "TEXT NOT NULL DEFAULT ''"},
		// The Library items a run made, as a JSON list of ids (agent.go keep).
		{"automations_runs", "outputs", "TEXT NOT NULL DEFAULT '[]'"},
		// The order a version and a run were written in (see addSeq).
		{"automations_flow_versions", "seq", "BIGINT NOT NULL DEFAULT 0"},
		{"automations_runs", "seq", "BIGINT NOT NULL DEFAULT 0"},
	} {
		if _, err := addColumn(ctx, d, tx, c.table, c.name, c.decl); err != nil {
			return err
		}
	}
	if err := addSeq(ctx, d, tx); err != nil {
		return err
	}
	// Whether a flow is an automation, set when createAutomation makes it: the
	// fact the per-org cap counts in SQL. A flow never stops being one — the
	// builder's routes refuse to change an automation — so it is written once. On
	// the migration that adds it, the automations already stored are marked.
	if err := migrateJobs(ctx, tx); err != nil {
		return err
	}
	added, err := addColumn(ctx, d, tx, "automations_flows", "automation", "BIGINT NOT NULL DEFAULT 0")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ix_auto_flows_org_automation ON automations_flows(org, automation)`); err != nil {
		return err
	}
	if added {
		return markAutomations(ctx, tx)
	}
	return nil
}

// addSeq indexes the order versions and runs were written in, and numbers the rows a
// file made before the column existed.
//
// Two rows of one flow can share a millisecond, and "the latest version" and "the last
// run" must still name the one written last. A SQLite file kept that order only as the
// rowid the engine holds out of sight; PostgreSQL has no rowid, so the order is a column
// that serves both. A new row takes MAX(seq)+1 in its own INSERT. Two writers that read
// the same maximum have written at once, with neither before the other, so they tie and
// the id breaks it: the number is an order, not a key, and carries no unique index.
//
// An older binary inserts rows with seq 0, and they list as the oldest; the numbering
// (WHERE seq = 0) runs on every open of a file, so a rollback needs no data step and a
// migration that stopped halfway is finished by the next. Only a SQLite file has rows
// to number: a PostgreSQL database is made with the column and has no older version.
func addSeq(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	for _, t := range []struct{ table, index string }{
		{"automations_flow_versions", "ix_auto_versions_seq"},
		{"automations_runs", "ix_auto_runs_seq"},
	} {
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS `+t.index+` ON `+t.table+`(seq)`); err != nil {
			return fmt.Errorf("index %s.seq: %w", t.table, err)
		}
		if d.Postgres() {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+t.table+` SET seq = rowid WHERE seq = 0`); err != nil {
			return fmt.Errorf("number %s.seq: %w", t.table, err)
		}
	}
	return nil
}

// markAutomations sets the automation flag on every stored flow whose latest
// version is one.
func markAutomations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT org,id FROM automations_flows`)
	if err != nil {
		return fmt.Errorf("mark: %w", err)
	}
	var flows [][2]string
	for rows.Next() {
		var org, id string
		if err := rows.Scan(&org, &id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("mark: %w", err)
		}
		flows = append(flows, [2]string{org, id})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("mark: %w", err)
	}
	for _, f := range flows {
		v, err := latestVersion(ctx, tx, f[0], f[1])
		if err != nil || !isAutomation(&v) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE automations_flows SET automation=1 WHERE org=$1 AND id=$2`, f[0], f[1]); err != nil {
			return fmt.Errorf("mark: %w", err)
		}
	}
	return nil
}

// addColumn adds one column to table unless the table already has it, and
// reports whether it did.
func addColumn(ctx context.Context, d dialect.Dialect, tx *sql.Tx, table, name, decl string) (bool, error) {
	has, err := d.HasColumn(ctx, tx, table, name)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", table, err)
	}
	if has {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+name+` `+decl); err != nil {
		return false, fmt.Errorf("add %s.%s: %w", table, name, err)
	}
	return true, nil
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
	if err := dialect.Text(f.ID, f.Org, f.ExternalID, f.FolderID, string(f.Status), f.PublishedVersionID, string(f.Metadata)); err != nil {
		return Flow{}, fmt.Errorf("insert flow: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_flows (`+flowCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		f.ID, f.Org, f.ExternalID, f.FolderID, f.Status, f.PublishedVersionID,
		string(f.Metadata), f.Created, f.Updated); err != nil {
		return Flow{}, fmt.Errorf("insert flow: %w", err)
	}
	return f, nil
}

func (s *Store) GetFlow(ctx context.Context, org, id string) (Flow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+flowCols+` FROM automations_flows WHERE org=$1 AND id=$2`, org, id)
	f, err := scanFlow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Flow{}, errNotFound
	}
	if err != nil {
		return Flow{}, fmt.Errorf("get flow: %w", err)
	}
	return f, nil
}

// ListFlows reads the flows one person holds in org, most recently changed
// first. A flow held by nobody is listed to nobody.
func (s *Store) ListFlows(ctx context.Context, org, sub string, limit int) ([]Flow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+flowCols+` FROM automations_flows WHERE org=$1 AND owner_sub=$2 AND owner_sub<>'' ORDER BY updated DESC LIMIT $3`, org, sub, limit)
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
	if err := dialect.Text(f.ExternalID, f.FolderID, string(f.Status), f.PublishedVersionID, string(f.Metadata)); err != nil {
		return Flow{}, fmt.Errorf("update flow: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_flows SET external_id=$1,folder_id=$2,status=$3,published_version_id=$4,metadata=$5,updated=$6 WHERE org=$7 AND id=$8`,
		f.ExternalID, f.FolderID, f.Status, f.PublishedVersionID, string(f.Metadata), f.Updated, f.Org, f.ID)
	if err != nil {
		return Flow{}, fmt.Errorf("update flow: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Flow{}, errNotFound
	}
	return s.GetFlow(ctx, f.Org, f.ID)
}

// UpdatePlainFlow is the builder's UpdateFlow: it writes f only while f is not
// an automation, checked in the write itself, so a builder that read a draft
// before it was finished cannot switch or pin it after.
func (s *Store) UpdatePlainFlow(ctx context.Context, f Flow) (Flow, error) {
	if err := dialect.Text(f.ExternalID, f.FolderID, string(f.Status), f.PublishedVersionID, string(f.Metadata)); err != nil {
		return Flow{}, fmt.Errorf("update flow: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_flows SET external_id=$1,folder_id=$2,status=$3,published_version_id=$4,metadata=$5,updated=$6 WHERE org=$7 AND id=$8 AND automation=0`,
		f.ExternalID, f.FolderID, f.Status, f.PublishedVersionID, string(f.Metadata), f.Updated, f.Org, f.ID)
	if err != nil {
		return Flow{}, fmt.Errorf("update flow: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if s.isAutomationFlow(ctx, f.Org, f.ID) {
			return Flow{}, errIsAutomation
		}
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
	res, err := tx.ExecContext(ctx, `DELETE FROM automations_flows WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete flow: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automations_flow_versions WHERE org=$1 AND flow_id=$2`, org, id); err != nil {
		return false, fmt.Errorf("delete versions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automations_runs WHERE org=$1 AND flow_id=$2`, org, id); err != nil {
		return false, fmt.Errorf("delete runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automations_triggers WHERE org=$1 AND flow_id=$2`, org, id); err != nil {
		return false, fmt.Errorf("delete triggers: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// ── FlowVersion ─────────────────────────────────────────────────────────────

const versionCols = `id,org,flow_id,display_name,trigger_json,valid,state,schema_version,created,updated`

// nextVersionSeq is the seq a version is written with: one past the highest (see addSeq).
const nextVersionSeq = `(SELECT COALESCE(MAX(seq), 0) + 1 FROM automations_flow_versions)`

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
	if err := dialect.Text(v.ID, v.Org, v.FlowID, v.DisplayName, trig, string(v.State), v.SchemaVersion); err != nil {
		return FlowVersion{}, fmt.Errorf("insert version: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_flow_versions (`+versionCols+`,seq) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,`+nextVersionSeq+`)`,
		v.ID, v.Org, v.FlowID, v.DisplayName, trig, b2i(v.Valid), v.State, v.SchemaVersion, v.Created, v.Updated); err != nil {
		return FlowVersion{}, fmt.Errorf("insert version: %w", err)
	}
	return v, nil
}

func (s *Store) GetVersion(ctx context.Context, org, id string) (FlowVersion, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+versionCols+` FROM automations_flow_versions WHERE org=$1 AND id=$2`, org, id)
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
		`SELECT `+versionCols+` FROM automations_flow_versions WHERE org=$1 AND flow_id=$2 ORDER BY created DESC LIMIT $3`, org, flowID, limit)
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
	return latestVersion(ctx, s.db, org, flowID)
}

// latestVersionSQL reads a flow's newest version: the last created, and of those
// created together the last written (seq, then id for a tie of concurrent writers).
const latestVersionSQL = `SELECT ` + versionCols + ` FROM automations_flow_versions WHERE org=$1 AND flow_id=$2 ORDER BY created DESC, seq DESC, id DESC LIMIT 1`

// latestVersion is LatestVersion on a handle or a transaction.
func latestVersion(ctx context.Context, q dialect.Querier, org, flowID string) (FlowVersion, error) {
	v, err := scanVersion(q.QueryRowContext(ctx, latestVersionSQL, org, flowID))
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
	if err := dialect.Text(v.DisplayName, trig, string(v.State), v.SchemaVersion); err != nil {
		return FlowVersion{}, fmt.Errorf("update version: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_flow_versions SET display_name=$1,trigger_json=$2,valid=$3,state=$4,schema_version=$5,updated=$6 WHERE org=$7 AND id=$8
		 AND flow_id NOT IN (SELECT id FROM automations_flows WHERE org=$7 AND automation=1)`,
		v.DisplayName, trig, b2i(v.Valid), v.State, v.SchemaVersion, v.Updated, v.Org, v.ID)
	if err != nil {
		return FlowVersion{}, fmt.Errorf("update version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if s.isAutomationFlow(ctx, v.Org, v.FlowID) {
			return FlowVersion{}, errIsAutomation
		}
		return FlowVersion{}, errNotFound
	}
	return s.GetVersion(ctx, v.Org, v.ID)
}

// errIsAutomation refuses a builder write to a flow that is an automation, which
// only /v1/auto/automations changes.
var errIsAutomation = errors.New("flow is an automation")

// isAutomationFlow reports whether flow id of org is marked an automation.
func (s *Store) isAutomationFlow(ctx context.Context, org, id string) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations_flows WHERE org=$1 AND id=$2 AND automation=1`, org, id).Scan(&n)
	return n > 0
}

// AddVersion is the builder's CreateVersion: it adds v to a flow that is not an
// automation, checked in the insert itself, so a builder that read a draft
// before it was finished cannot add a version after.
func (s *Store) AddVersion(ctx context.Context, v FlowVersion) (FlowVersion, error) {
	trig, err := marshalTrigger(v.Trigger)
	if err != nil {
		return FlowVersion{}, err
	}
	if err := dialect.Text(v.ID, v.Org, v.FlowID, v.DisplayName, trig, string(v.State), v.SchemaVersion); err != nil {
		return FlowVersion{}, fmt.Errorf("insert version: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_flow_versions (`+versionCols+`,seq) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,`+nextVersionSeq+`
		 WHERE EXISTS (SELECT 1 FROM automations_flows WHERE org=$2 AND id=$3 AND automation=0)`,
		v.ID, v.Org, v.FlowID, v.DisplayName, trig, b2i(v.Valid), v.State, v.SchemaVersion, v.Created, v.Updated)
	if err != nil {
		return FlowVersion{}, fmt.Errorf("insert version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if s.isAutomationFlow(ctx, v.Org, v.FlowID) {
			return FlowVersion{}, errIsAutomation
		}
		return FlowVersion{}, errBadRef
	}
	return v, nil
}

// ── FlowRun ─────────────────────────────────────────────────────────────────

const runCols = `id,org,flow_id,flow_version_id,workflow_id,status,start_time,finish_time,created,updated,session,summary,person,email,actor,draft,posts,outputs`

// nextRunSeq is the seq a run is written with: one past the highest (see addSeq).
const nextRunSeq = `(SELECT COALESCE(MAX(seq), 0) + 1 FROM automations_runs)`

// checkRun refuses a run whose text PostgreSQL could not hold.
func checkRun(r FlowRun) error {
	return dialect.Text(r.ID, r.Org, r.FlowID, r.FlowVersionID, r.WorkflowID, string(r.Status), r.Session, r.Summary, r.Person, r.Email, r.Actor, r.Draft, r.Posts, r.Outputs)
}

func scanRun(sc interface{ Scan(...any) error }) (FlowRun, error) {
	var r FlowRun
	err := sc.Scan(&r.ID, &r.Org, &r.FlowID, &r.FlowVersionID, &r.WorkflowID,
		&r.Status, &r.StartTime, &r.FinishTime, &r.Created, &r.Updated, &r.Session, &r.Summary, &r.Person, &r.Email, &r.Actor, &r.Draft, &r.Posts, &r.Outputs)
	return r, err
}

// SetRunOutputs records the Library items run id made, a JSON list of ids.
func (s *Store) SetRunOutputs(ctx context.Context, org, id, outputs string) error {
	if err := dialect.Text(outputs); err != nil {
		return fmt.Errorf("set run outputs: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET outputs=$1,updated=$2 WHERE org=$3 AND id=$4`, outputs, time.Now().UnixMilli(), org, id); err != nil {
		return fmt.Errorf("set run outputs: %w", err)
	}
	return nil
}

// SetRunDraft records what a posting automation's agent wrote for run id.
func (s *Store) SetRunDraft(ctx context.Context, org, id, draft string) error {
	if err := dialect.Text(draft); err != nil {
		return fmt.Errorf("set run draft: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET draft=$1,updated=$2 WHERE org=$3 AND id=$4`, draft, time.Now().UnixMilli(), org, id); err != nil {
		return fmt.Errorf("set run draft: %w", err)
	}
	return nil
}

// ClaimReview moves run id out of review, to running, and reports whether this
// call did: a run answered twice at once is answered once.
func (s *Store) ClaimReview(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET status=$1,updated=$2 WHERE org=$3 AND id=$4 AND status=$5`,
		RunRunning, time.Now().UnixMilli(), org, id, RunPaused)
	if err != nil {
		return false, fmt.Errorf("claim review: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetRunPosts records where run id posted, as JSON (post.go PostResult).
func (s *Store) SetRunPosts(ctx context.Context, org, id, posts string) error {
	if err := dialect.Text(posts); err != nil {
		return fmt.Errorf("set run posts: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET posts=$1,updated=$2 WHERE org=$3 AND id=$4`, posts, time.Now().UnixMilli(), org, id); err != nil {
		return fmt.Errorf("set run posts: %w", err)
	}
	return nil
}

func (s *Store) CreateRun(ctx context.Context, r FlowRun) (FlowRun, error) {
	if err := checkRun(r); err != nil {
		return FlowRun{}, fmt.Errorf("insert run: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_runs (`+runCols+`,seq) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,`+nextRunSeq+`)`,
		r.ID, r.Org, r.FlowID, r.FlowVersionID, r.WorkflowID, r.Status,
		r.StartTime, r.FinishTime, r.Created, r.Updated, r.Session, r.Summary, r.Person, r.Email, r.Actor, r.Draft, r.Posts, cmp.Or(r.Outputs, "[]")); err != nil {
		return FlowRun{}, fmt.Errorf("insert run: %w", err)
	}
	return r, nil
}

// CreateRunIfAbsent inserts a run row keyed on its id, doing NOTHING if it already
// exists. It reports whether THIS call created the row. Idempotent by run id (=the
// workflow execution id), so a retried run-start bookkeeping step, or a manual
// handler + the durable path racing to record the same run, converge on one row.
func (s *Store) CreateRunIfAbsent(ctx context.Context, r FlowRun) (bool, error) {
	if err := checkRun(r); err != nil {
		return false, fmt.Errorf("insert run if absent: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_runs (`+runCols+`,seq) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,`+nextRunSeq+`) ON CONFLICT(id) DO NOTHING`,
		r.ID, r.Org, r.FlowID, r.FlowVersionID, r.WorkflowID, r.Status,
		r.StartTime, r.FinishTime, r.Created, r.Updated, r.Session, r.Summary, r.Person, r.Email, r.Actor, r.Draft, r.Posts, cmp.Or(r.Outputs, "[]"))
	if err != nil {
		return false, fmt.Errorf("insert run if absent: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CreateRunExclusive is CreateRunIfAbsent for a flow that runs one at a time. In
// one transaction it reads whether run r already exists (created=false: another
// path recorded it first, and it is answered as stored), then whether another
// run of the same flow is still live, and inserts r RUNNING when none is and
// SKIPPED — finished at once, with the reason as its summary — when one is.
//
// A live run older than stale no longer holds the flow: a run whose workflow was
// lost never records its end, and it must not stop every tick after it.
func (s *Store) CreateRunExclusive(ctx context.Context, r FlowRun, stale int64) (FlowRun, bool, error) {
	if err := checkRun(r); err != nil {
		return FlowRun{}, false, fmt.Errorf("insert run: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return FlowRun{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	have, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runCols+` FROM automations_runs WHERE org=$1 AND id=$2`, r.Org, r.ID))
	if err == nil {
		return have, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return FlowRun{}, false, fmt.Errorf("read run: %w", err)
	}
	var busy int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM automations_runs WHERE org=$1 AND flow_id=$2 AND id<>$3 AND status IN ($4,$5) AND start_time>=$6 LIMIT 1`,
		r.Org, r.FlowID, r.ID, RunRunning, RunQueued, stale).Scan(&busy)
	switch {
	case err == nil:
		r.Status, r.FinishTime, r.Summary = RunSkipped, r.StartTime, skippedSummary
	case errors.Is(err, sql.ErrNoRows):
		r.Status = RunRunning
	default:
		return FlowRun{}, false, fmt.Errorf("read live runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO automations_runs (`+runCols+`,seq) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,`+nextRunSeq+`)`,
		r.ID, r.Org, r.FlowID, r.FlowVersionID, r.WorkflowID, r.Status,
		r.StartTime, r.FinishTime, r.Created, r.Updated, r.Session, r.Summary, r.Person, r.Email, r.Actor, r.Draft, r.Posts, cmp.Or(r.Outputs, "[]")); err != nil {
		return FlowRun{}, false, fmt.Errorf("insert run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FlowRun{}, false, fmt.Errorf("commit: %w", err)
	}
	return r, true, nil
}

// SetRunSession records the Dev run an agent step started for run id.
func (s *Store) SetRunSession(ctx context.Context, org, id, session string) error {
	if err := dialect.Text(session); err != nil {
		return fmt.Errorf("set run session: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET session=$1,updated=$2 WHERE org=$3 AND id=$4`,
		session, time.Now().UnixMilli(), org, id); err != nil {
		return fmt.Errorf("set run session: %w", err)
	}
	return nil
}

// SetRunPerson records the person run id is made as, and their address.
func (s *Store) SetRunPerson(ctx context.Context, org, id string, p Person) error {
	if err := dialect.Text(p.Sub, p.Email); err != nil {
		return fmt.Errorf("set run person: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET person=$1,email=$2,updated=$3 WHERE org=$4 AND id=$5`,
		p.Sub, p.Email, time.Now().UnixMilli(), org, id); err != nil {
		return fmt.Errorf("set run person: %w", err)
	}
	return nil
}

// SetRunActor records who pressed Run for run id.
func (s *Store) SetRunActor(ctx context.Context, org, id, actor string) error {
	if err := dialect.Text(actor); err != nil {
		return fmt.Errorf("set run actor: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET actor=$1 WHERE org=$2 AND id=$3`, actor, org, id); err != nil {
		return fmt.Errorf("set run actor: %w", err)
	}
	return nil
}

// errAutomationLimit refuses an automation past the org's cap.
var errAutomationLimit = errors.New("automation limit")

// CreateAutomationFlow creates f as an automation of its org unless the org
// already keeps max of them. The count and the insert are one transaction, so
// two creates at once cannot both take the last place.
func (s *Store) CreateAutomationFlow(ctx context.Context, f Flow, max int) (Flow, error) {
	if err := dialect.Text(f.ID, f.Org, f.ExternalID, f.FolderID, string(f.Status), f.PublishedVersionID, string(f.Metadata)); err != nil {
		return Flow{}, fmt.Errorf("insert flow: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Flow{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations_flows WHERE org=$1 AND automation=1`, f.Org).Scan(&n); err != nil {
		return Flow{}, fmt.Errorf("count automations: %w", err)
	}
	if n >= max {
		return Flow{}, errAutomationLimit
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO automations_flows (`+flowCols+`,automation) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1)`,
		f.ID, f.Org, f.ExternalID, f.FolderID, f.Status, f.PublishedVersionID,
		string(f.Metadata), f.Created, f.Updated); err != nil {
		return Flow{}, fmt.Errorf("insert flow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Flow{}, fmt.Errorf("commit: %w", err)
	}
	return f, nil
}

// SetRunSummary records the one line that says how run id went.
func (s *Store) SetRunSummary(ctx context.Context, org, id, summary string) error {
	if err := dialect.Text(summary); err != nil {
		return fmt.Errorf("set run summary: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET summary=$1,updated=$2 WHERE org=$3 AND id=$4`,
		summary, time.Now().UnixMilli(), org, id); err != nil {
		return fmt.Errorf("set run summary: %w", err)
	}
	return nil
}

// LastRun is a flow's newest run that was not skipped, or errNotFound.
func (s *Store) LastRun(ctx context.Context, org, flowID string) (FlowRun, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM automations_runs WHERE org=$1 AND flow_id=$2 AND status<>$3 ORDER BY created DESC, seq DESC, id DESC LIMIT 1`,
		org, flowID, RunSkipped))
	if errors.Is(err, sql.ErrNoRows) {
		return FlowRun{}, errNotFound
	}
	if err != nil {
		return FlowRun{}, fmt.Errorf("last run: %w", err)
	}
	return r, nil
}

// ClaimTick takes key — the tick a schedule fired (firing) — for org's flow id and
// reports whether this call took it. A tick is taken once, whenever its start
// arrives: a second fire of it, a start held up behind a later tick, or for a daily
// schedule the repeat of a wall-clock time the zone's clocks went back over. A flow
// the org does not hold takes nothing. Ticks older than ticksKept go with it.
func (s *Store) ClaimTick(ctx context.Context, org, id, key string) (bool, error) {
	now := time.Now().Unix()
	if err := dialect.Text(key); err != nil {
		return false, fmt.Errorf("claim tick: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_ticks (org, flow, key, at)
		 SELECT $1, $2, $3, $4 WHERE EXISTS (SELECT 1 FROM automations_flows WHERE org=$1 AND id=$2)
		 ON CONFLICT DO NOTHING`,
		org, id, key, now)
	if err != nil {
		return false, fmt.Errorf("claim tick: %w", err)
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM automations_ticks WHERE org=$1 AND flow=$2 AND at<$3`,
		org, id, now-int64(ticksKept/time.Second)); err != nil {
		return false, fmt.Errorf("prune ticks: %w", err)
	}
	return n == 1, nil
}

// ticksKept is how long a taken tick is kept: longer than a second fire of it can
// arrive — another engine's, a backlogged start, the repeat of an hour the clocks
// went back over — and short enough that a once-a-minute flow keeps a few thousand
// rows, so the prune beside every claim is a short range on ix_auto_ticks_at.
const ticksKept = 48 * time.Hour

// Definition is flow id's latest version and the person it runs as, read in one
// transaction, so they are always a pair DefineVersion wrote together.
func (s *Store) Definition(ctx context.Context, org, id string) (FlowVersion, Person, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FlowVersion{}, Person{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	p, err := scanPerson(tx.QueryRowContext(ctx, `SELECT `+personCols+` FROM automations_flows WHERE org=$1 AND id=$2`, org, id))
	if err != nil {
		return FlowVersion{}, Person{}, err
	}
	v, err := latestVersion(ctx, tx, org, id)
	if errors.Is(err, errNotFound) {
		return FlowVersion{}, Person{}, errNotFound
	}
	if err != nil {
		return FlowVersion{}, Person{}, fmt.Errorf("definition: %w", err)
	}
	return v, p, nil
}

// DefineVersion is UpdateVersion and SetFlowOwner in one transaction: what an
// automation does and the person it runs as are written together.
func (s *Store) DefineVersion(ctx context.Context, v FlowVersion, p Person) (FlowVersion, error) {
	trig, err := marshalTrigger(v.Trigger)
	if err != nil {
		return FlowVersion{}, err
	}
	if err := dialect.Text(v.DisplayName, trig, string(v.State), v.SchemaVersion, p.Sub, p.Email); err != nil {
		return FlowVersion{}, fmt.Errorf("define version: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FlowVersion{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE automations_flow_versions SET display_name=$1,trigger_json=$2,valid=$3,state=$4,schema_version=$5,updated=$6 WHERE org=$7 AND id=$8`,
		v.DisplayName, trig, b2i(v.Valid), v.State, v.SchemaVersion, v.Updated, v.Org, v.ID)
	if err != nil {
		return FlowVersion{}, fmt.Errorf("define version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return FlowVersion{}, errNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE automations_flows SET owner_sub=$1,owner_email=$2 WHERE org=$3 AND id=$4`, p.Sub, p.Email, v.Org, v.FlowID); err != nil {
		return FlowVersion{}, fmt.Errorf("define version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FlowVersion{}, fmt.Errorf("commit: %w", err)
	}
	return s.GetVersion(ctx, v.Org, v.ID)
}

// errChanged refuses to finish a draft that is no longer the draft that was read.
var errChanged = errors.New("the draft changed since it was read")

// FinishDraft makes draft flow v.FlowID an automation in place: v becomes its
// definition and p the person it runs as. One transaction checks, then writes:
// the flow is still a draft — not an automation, no person, no pinned version —
// v is still its latest version and still does nothing (bare), and its org
// keeps fewer than max automations. It drops any event subscription an earlier
// version left on the flow, so only a person's own Run, or the schedule saved
// with it, starts it. A write the builder read the draft for and makes after
// this is refused by the same flag (AddVersion, UpdateVersion, UpdatePlainFlow).
func (s *Store) FinishDraft(ctx context.Context, v FlowVersion, p Person, max int) (FlowVersion, error) {
	trig, err := marshalTrigger(v.Trigger)
	if err != nil {
		return FlowVersion{}, err
	}
	if err := dialect.Text(v.DisplayName, trig, string(v.State), v.SchemaVersion, p.Sub, p.Email); err != nil {
		return FlowVersion{}, fmt.Errorf("finish draft: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return FlowVersion{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	latest, err := latestVersion(ctx, tx, v.Org, v.FlowID)
	if errors.Is(err, errNotFound) {
		return FlowVersion{}, errNotFound
	}
	if err != nil {
		return FlowVersion{}, fmt.Errorf("finish draft: %w", err)
	}
	if latest.ID != v.ID || !bare(latest.Trigger) {
		return FlowVersion{}, errChanged
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations_flows WHERE org=$1 AND automation=1`, v.Org).Scan(&n); err != nil {
		return FlowVersion{}, fmt.Errorf("count automations: %w", err)
	}
	if n >= max {
		return FlowVersion{}, errAutomationLimit
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE automations_flows SET automation=1,owner_sub=$1,owner_email=$2 WHERE org=$3 AND id=$4 AND automation=0 AND owner_sub=$5 AND owner_sub<>'' AND published_version_id=''`,
		p.Sub, p.Email, v.Org, v.FlowID, p.Sub)
	if err != nil {
		return FlowVersion{}, fmt.Errorf("finish draft: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return FlowVersion{}, errChanged
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM automations_triggers WHERE org=$1 AND flow_id=$2`, v.Org, v.FlowID); err != nil {
		return FlowVersion{}, fmt.Errorf("finish draft: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE automations_flow_versions SET display_name=$1,trigger_json=$2,valid=$3,state=$4,schema_version=$5,updated=$6 WHERE org=$7 AND id=$8`,
		v.DisplayName, trig, b2i(v.Valid), v.State, v.SchemaVersion, v.Updated, v.Org, v.ID); err != nil {
		return FlowVersion{}, fmt.Errorf("finish draft: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FlowVersion{}, fmt.Errorf("commit: %w", err)
	}
	return s.GetVersion(ctx, v.Org, v.ID)
}

// ActiveOrgs is every org with an enabled flow or a run still going: the orgs
// whose engine worker must be up for a schedule or a wait to go on.
func (s *Store) ActiveOrgs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org FROM automations_flows WHERE status=$1 UNION SELECT org FROM automations_runs WHERE status IN ($2,$3) ORDER BY org`,
		FlowEnabled, RunRunning, RunQueued)
	if err != nil {
		return nil, fmt.Errorf("active orgs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return nil, fmt.Errorf("active orgs: %w", err)
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

// SetFlowOwner records who a flow's agent step runs as.
func (s *Store) SetFlowOwner(ctx context.Context, org, id string, p Person) error {
	if err := dialect.Text(p.Sub, p.Email); err != nil {
		return fmt.Errorf("set flow owner: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE automations_flows SET owner_sub=$1,owner_email=$2 WHERE org=$3 AND id=$4`,
		p.Sub, p.Email, org, id); err != nil {
		return fmt.Errorf("set flow owner: %w", err)
	}
	return nil
}

// FlowOwner is who a flow's agent step runs as.
func (s *Store) FlowOwner(ctx context.Context, org, id string) (Person, error) {
	return scanPerson(s.db.QueryRowContext(ctx, `SELECT `+personCols+` FROM automations_flows WHERE org=$1 AND id=$2`, org, id))
}

const personCols = `owner_sub,owner_email`

func scanPerson(row *sql.Row) (Person, error) {
	var p Person
	err := row.Scan(&p.Sub, &p.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return Person{}, errNotFound
	}
	if err != nil {
		return Person{}, fmt.Errorf("flow owner: %w", err)
	}
	return p, nil
}

// ClaimMeter atomically flips the run's metered flag 0→1 for (org,id) and reports
// whether THIS call won the flip. It is the exactly-once billing gate: only the
// winner meters + audits the run, so no entrypoint double-bills a run (MED-1). The
// (org,id) predicate keeps the claim tenant-scoped.
func (s *Store) ClaimMeter(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE automations_runs SET metered=1 WHERE org=$1 AND id=$2 AND metered=0`, org, id)
	if err != nil {
		return false, fmt.Errorf("claim meter: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) GetRun(ctx context.Context, org, id string) (FlowRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM automations_runs WHERE org=$1 AND id=$2`, org, id)
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
			`SELECT `+runCols+` FROM automations_runs WHERE org=$1 ORDER BY created DESC, seq DESC, id DESC LIMIT $2`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+runCols+` FROM automations_runs WHERE org=$1 AND flow_id=$2 ORDER BY created DESC, seq DESC, id DESC LIMIT $3`, org, flowID, limit)
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
		`UPDATE automations_runs SET status=$1,finish_time=$2,updated=$3 WHERE org=$4 AND id=$5`,
		status, finish, updated, org, id)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	return nil
}

// CountRunsSince counts an org's run rows created at/after `since` (unix millis). It is
// the DURABLE per-org run-rate signal (it reads persisted rows, so the bound survives a
// restart), scoped to org and served by ix_auto_runs_org_created.
func (s *Store) CountRunsSince(ctx context.Context, org string, since int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM automations_runs WHERE org=$1 AND created >= $2`, org, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count runs since: %w", err)
	}
	return n, nil
}

// DeleteRun removes a single run row for (org,id). Used to roll back a run whose durable
// START failed transiently — so the run id (the idempotency key) is NOT burned and a
// redelivery can retry, rather than being permanently dropped.
func (s *Store) DeleteRun(ctx context.Context, org, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM automations_runs WHERE org=$1 AND id=$2`, org, id); err != nil {
		return fmt.Errorf("delete run: %w", err)
	}
	return nil
}

// ── trigger subscriptions (WEBHOOK/APP_WEBHOOK routing index) ────────────────

// triggerSub is one webhook subscription resolved by MatchTriggers: the flow +
// version an inbound event should run.
type triggerSub struct {
	FlowID    string
	VersionID string
}

// UpsertTrigger records (or refreshes) a flow's webhook subscription. Idempotent on
// (org, flow_id): re-enabling or re-publishing a flow overwrites provider/event/version
// so exactly one subscription per flow ever exists.
func (s *Store) UpsertTrigger(ctx context.Context, org, provider, event, flowID, versionID string) error {
	if err := dialect.Text(org, provider, event, flowID, versionID); err != nil {
		return fmt.Errorf("upsert trigger: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO automations_triggers (org,provider,event,flow_id,version_id,created) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT(org,flow_id) DO UPDATE SET provider=excluded.provider,event=excluded.event,version_id=excluded.version_id`,
		org, provider, event, flowID, versionID, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("upsert trigger: %w", err)
	}
	return nil
}

// DeleteFlowTriggers removes a flow's webhook subscription (on disable/delete). It is
// idempotent — deleting an absent subscription is a no-op — so a disable can always
// fail-closed by calling it regardless of the flow's current trigger type.
func (s *Store) DeleteFlowTriggers(ctx context.Context, org, flowID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM automations_triggers WHERE org=$1 AND flow_id=$2`, org, flowID); err != nil {
		return fmt.Errorf("delete triggers: %w", err)
	}
	return nil
}

// MatchTriggers returns the subscriptions whose (provider,event) matches an inbound
// event, scoped to org — the LEADING index column, so tenant isolation is physical: a
// query for org A can never return org B's rows. Bounded by maxLimit.
func (s *Store) MatchTriggers(ctx context.Context, org, id, event string) ([]triggerSub, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT flow_id,version_id FROM automations_triggers WHERE org=$1 AND provider=$2 AND event=$3 ORDER BY flow_id LIMIT $4`,
		org, id, event, maxLimit)
	if err != nil {
		return nil, fmt.Errorf("match triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]triggerSub, 0, 8)
	for rows.Next() {
		var t triggerSub
		if err := rows.Scan(&t.FlowID, &t.VersionID); err != nil {
			return nil, fmt.Errorf("scan trigger: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
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
		`SELECT 1 FROM automations_flows WHERE org=$1 AND id=$2`, org, flowID).Scan(&one)
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
