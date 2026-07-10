package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	"github.com/hanzoai/cloud/internal/cek"
	_ "github.com/hanzoai/sqlite"
)

var (
	errConflict = errors.New("agents: agent already exists")
	errNotFound = errors.New("agents: agent not found")
)

// Agent is the org-scoped definition of an autonomous worker: a model, a system
// prompt (instructions), and a set of tool names it may call. Tenant isolation
// is the org column, enforced on every query. It never stores a secret — tool
// credentials live in KMS and are referenced by name at run time.
//
// The bot-lifecycle fields promote an agent from a one-shot callable into a
// long-running bot (per hanzo-agent-bot-architecture: "Bot = Agent + compute +
// long-running"):
//
//   - ExecutionMode: "one-shot" (default; runs only when POSTed) or
//     "long-running" (the scheduler invokes it on Schedule).
//   - Schedule: a 5-field cron expression; required when long-running, ignored
//     otherwise. The scheduler evaluates it once a minute.
//   - ComputeRef: an optional visor machine id the bot is bound to. It is an
//     opaque reference here; binding/lifecycle is owned elsewhere.
//   - ServiceAccountID: an optional IAM agent service-account (<org>-<agent>).
//     When set it is the Actor recorded on scheduled-run billing so an
//     autonomous run is attributable to a principal, not just the org.
type Agent struct {
	ID               string
	Org              string
	Name             string
	Model            string
	Instructions     string
	Description      string
	Tools            []string
	Status           string
	ExecutionMode    string
	Schedule         string
	ComputeRef       string
	ServiceAccountID string
	CreatedAt        int64
	UpdatedAt        int64
}

// Execution modes. One-shot agents run only on an explicit POST; long-running
// agents are additionally invoked by the scheduler on their Schedule.
const (
	ModeOneShot     = "one-shot"
	ModeLongRunning = "long-running"
)

// Run is one execution of an agent: the input, the produced output (or error),
// which model served it, and how long it took. Real history — every row is a
// call that actually happened.
type Run struct {
	ID         string
	Org        string
	AgentName  string
	Status     string
	Model      string
	Input      string
	Output     string
	Error      string
	DurationMs int64
	CreatedAt  int64
}

// Store is the agents database. ONE SQLite file ({DataDir}/agents.db) holds
// every org's records; tenancy is the org column.
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

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agents (
  id                 TEXT PRIMARY KEY,
  org                TEXT NOT NULL,
  name               TEXT NOT NULL,
  model              TEXT NOT NULL DEFAULT '',
  instructions       TEXT NOT NULL DEFAULT '',
  description        TEXT NOT NULL DEFAULT '',
  tools              TEXT NOT NULL DEFAULT '[]',
  status             TEXT NOT NULL DEFAULT 'ready',
  execution_mode     TEXT NOT NULL DEFAULT 'one-shot',
  schedule           TEXT NOT NULL DEFAULT '',
  compute_ref        TEXT NOT NULL DEFAULT '',
  service_account_id TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_agents_org_name ON agents(org, name);
CREATE INDEX IF NOT EXISTS ix_agents_org_updated ON agents(org, updated_at);

CREATE TABLE IF NOT EXISTS agent_runs (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  agent_name  TEXT NOT NULL,
  status      TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  input       TEXT NOT NULL DEFAULT '',
  output      TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_runs_org_agent_created ON agent_runs(org, agent_name, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Forward, idempotent migration for databases created before the
	// bot-lifecycle columns existed. Each ADD COLUMN is guarded by a live
	// column-existence check (PRAGMA table_info), so re-running migrate() on an
	// already-upgraded DB is a no-op and never errors — the DDL above handles
	// fresh DBs, this handles pre-existing ones. It touches no storage-backend
	// knob (driverName/DSN), so the SQLite-only storage lockdown is unaffected.
	if err := s.addColumns("agents", map[string]string{
		"execution_mode":     "TEXT NOT NULL DEFAULT 'one-shot'",
		"schedule":           "TEXT NOT NULL DEFAULT ''",
		"compute_ref":        "TEXT NOT NULL DEFAULT ''",
		"service_account_id": "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	// Partial index for the once-a-minute scheduler scan — created AFTER the
	// lifecycle columns exist (a legacy DB gains them just above), so it selects
	// only the (typically few) scheduled long-running agents instead of
	// full-scanning every org's agents on the single shared SQLite connection.
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS ix_agents_scheduled
		ON agents(org, name) WHERE execution_mode='long-running' AND schedule<>''`); err != nil {
		return fmt.Errorf("migrate: scheduled index: %w", err)
	}
	// Live agent-session control-plane tables live in the SAME agents.db (one
	// store, one tenancy column) — sessions/events are to runs what the subagent
	// tree is to a single call.
	if err := s.migrateSessions(); err != nil {
		return err
	}
	return nil
}

// addColumns adds each missing column to table, idempotently. A column already
// present is skipped; a fresh install (all present from the CREATE) is a no-op.
func (s *Store) addColumns(table string, cols map[string]string) error {
	have, err := s.columns(table)
	if err != nil {
		return err
	}
	for name, def := range cols {
		if have[name] {
			continue
		}
		// name/def are package-internal literals, never user input — no
		// injection surface. SQLite forbids parameterizing DDL identifiers.
		if _, err := s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + def); err != nil {
			return fmt.Errorf("migrate: add %s.%s: %w", table, name, err)
		}
	}
	return nil
}

// columns returns the set of column names on table via PRAGMA table_info.
func (s *Store) columns(table string) (map[string]bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, fmt.Errorf("migrate: table_info %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	have := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("migrate: scan table_info: %w", err)
		}
		have[name] = true
	}
	return have, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

func encodeList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeList(s string) []string {
	if s == "" {
		return nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil
	}
	return xs
}

const agentCols = `id,org,name,model,instructions,description,tools,status,execution_mode,schedule,compute_ref,service_account_id,created_at,updated_at`

func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var tools string
	err := sc.Scan(&a.ID, &a.Org, &a.Name, &a.Model, &a.Instructions, &a.Description,
		&tools, &a.Status, &a.ExecutionMode, &a.Schedule, &a.ComputeRef, &a.ServiceAccountID,
		&a.CreatedAt, &a.UpdatedAt)
	a.Tools = decodeList(tools)
	return a, err
}

// normalizeMode is the lowest-layer fail-safe default: an empty execution_mode
// is stored as one-shot so NO path (handler, scheduler, or a direct store call)
// can persist an agent the scheduler would treat ambiguously. The HTTP handler
// also defaults+validates, but this makes the invariant hold at the store.
func normalizeMode(m string) string {
	if strings.TrimSpace(m) == "" {
		return ModeOneShot
	}
	return m
}

// Create inserts one agent. A UNIQUE(org,name) violation surfaces as errConflict.
func (s *Store) Create(ctx context.Context, a Agent) error {
	a.ExecutionMode = normalizeMode(a.ExecutionMode)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (`+agentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.Name, a.Model, a.Instructions, a.Description,
		encodeList(a.Tools), a.Status, a.ExecutionMode, a.Schedule, a.ComputeRef,
		a.ServiceAccountID, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert agent: %w", err)
	}
	return nil
}

// Get returns the agent for the exact (org,name) or errNotFound. It is the
// precise name primitive; path-addressed handlers use Resolve (id-or-name).
func (s *Store) Get(ctx context.Context, org, name string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE org=? AND name=?`, org, name)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, errNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("get agent: %w", err)
	}
	return a, nil
}

// Resolve returns the agent identified by ref within org, matching either its
// public id (the `agent_...` handle create and list hand back) OR its org-unique
// name. This is the ONE lookup every path-addressed handler (get/update/delete/
// run/runs) uses, so a just-created agent is immediately addressable by exactly
// the identifier create/list returned — no id-vs-name split. If a ref somehow
// equals one agent's id and another's name, the id match wins (the stable public
// handle is authoritative). Tenancy is the org filter, so a ref belonging to
// another tenant is errNotFound — fail-closed, never cross-org.
func (s *Store) Resolve(ctx context.Context, org, ref string) (Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE org=? AND (id=? OR name=?)
		 ORDER BY (id=?) DESC LIMIT 1`, org, ref, ref, ref)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, errNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("resolve agent: %w", err)
	}
	return a, nil
}

// List returns every agent for org, most-recently-updated first.
func (s *Store) List(ctx context.Context, org string) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE org=? ORDER BY updated_at DESC, name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Update overwrites the mutable fields of an existing agent.
func (s *Store) Update(ctx context.Context, a Agent) error {
	a.ExecutionMode = normalizeMode(a.ExecutionMode)
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET model=?,instructions=?,description=?,tools=?,status=?,
		 execution_mode=?,schedule=?,compute_ref=?,service_account_id=?,updated_at=?
		 WHERE org=? AND name=?`,
		a.Model, a.Instructions, a.Description, encodeList(a.Tools), a.Status,
		a.ExecutionMode, a.Schedule, a.ComputeRef, a.ServiceAccountID, a.UpdatedAt, a.Org, a.Name)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
}

// ListLongRunning returns every agent across ALL orgs whose execution_mode is
// long-running and that carries a non-empty schedule — the scheduler's work
// set. It is the ONE cross-org query in this store; the scheduler is a trusted
// in-process subsystem (not a tenant request), and each returned agent carries
// its own Org so every downstream action (run, gate, meter) stays scoped to the
// agent's own tenant.
func (s *Store) ListLongRunning(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentCols+` FROM agents
		 WHERE execution_mode=? AND schedule<>'' ORDER BY org, name`, ModeLongRunning)
	if err != nil {
		return nil, fmt.Errorf("list long-running: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountLongRunning returns how many scheduled long-running agents an org has —
// used to cap an org's scheduler footprint at create time.
func (s *Store) CountLongRunning(ctx context.Context, org string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE org=? AND execution_mode=? AND schedule<>''`,
		org, ModeLongRunning).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count long-running: %w", err)
	}
	return n, nil
}

// Delete removes an agent and its run history. Reports whether a row went.
func (s *Store) Delete(ctx context.Context, org, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE org=? AND name=?`, org, name)
	if err != nil {
		return false, fmt.Errorf("delete agent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_runs WHERE org=? AND agent_name=?`, org, name); err != nil {
		return false, fmt.Errorf("delete runs: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// InsertRun records one agent execution.
func (s *Store) InsertRun(ctx context.Context, r Run) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_runs (id,org,agent_name,status,model,input,output,error,duration_ms,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Org, r.AgentName, r.Status, r.Model, r.Input, r.Output, r.Error, r.DurationMs, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

// ListRuns returns the run history for (org,agent), newest first, capped.
func (s *Store) ListRuns(ctx context.Context, org, agent string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,agent_name,status,model,input,output,error,duration_ms,created_at
		 FROM agent_runs WHERE org=? AND agent_name=? ORDER BY created_at DESC LIMIT ?`, org, agent, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Org, &r.AgentName, &r.Status, &r.Model, &r.Input,
			&r.Output, &r.Error, &r.DurationMs, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunsSince returns the org's runs across ALL agents with created_at >= since,
// newest first, capped. It powers the org-wide surfaces: the recent-activity
// feed (since=0 → the newest runs regardless of age) and the invocation
// histogram (since=windowStart → every run in the window, order-independent for
// bucketing). since<=0 means "no lower bound". Tenancy is the org column, so a
// caller never sees another org's runs. Every row is a real recorded execution.
func (s *Store) RunsSince(ctx context.Context, org string, since int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 10000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,agent_name,status,model,input,output,error,duration_ms,created_at
		 FROM agent_runs WHERE org=? AND created_at>=? ORDER BY created_at DESC LIMIT ?`, org, since, limit)
	if err != nil {
		return nil, fmt.Errorf("runs since: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Org, &r.AgentName, &r.Status, &r.Model, &r.Input,
			&r.Output, &r.Error, &r.DurationMs, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountRuns returns how many runs an org's agent has (for the list rollup).
func (s *Store) CountRuns(ctx context.Context, org, agent string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_runs WHERE org=? AND agent_name=?`, org, agent).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count runs: %w", err)
	}
	return n, nil
}
