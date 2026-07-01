package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	// modernc.org/sqlite is the pure-Go SQLite driver already in the cloud dep
	// graph. Blank import registers the "sqlite" driver name.
	_ "modernc.org/sqlite"
)

var (
	errConflict = errors.New("agents: agent already exists")
	errNotFound = errors.New("agents: agent not found")
)

// Agent is the org-scoped definition of an autonomous worker: a model, a system
// prompt (instructions), and a set of tool names it may call. Tenant isolation
// is the org column, enforced on every query. It never stores a secret — tool
// credentials live in KMS and are referenced by name at run time.
type Agent struct {
	ID           string
	Org          string
	Name         string
	Model        string
	Instructions string
	Description  string
	Tools        []string
	Status       string
	CreatedAt    int64
	UpdatedAt    int64
}

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
	db, err := sql.Open("sqlite", path)
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
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  name         TEXT NOT NULL,
  model        TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL DEFAULT '',
  description  TEXT NOT NULL DEFAULT '',
  tools        TEXT NOT NULL DEFAULT '[]',
  status       TEXT NOT NULL DEFAULT 'ready',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
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
	return nil
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

const agentCols = `id,org,name,model,instructions,description,tools,status,created_at,updated_at`

func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var tools string
	err := sc.Scan(&a.ID, &a.Org, &a.Name, &a.Model, &a.Instructions, &a.Description,
		&tools, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	a.Tools = decodeList(tools)
	return a, err
}

// Create inserts one agent. A UNIQUE(org,name) violation surfaces as errConflict.
func (s *Store) Create(ctx context.Context, a Agent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (`+agentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Org, a.Name, a.Model, a.Instructions, a.Description,
		encodeList(a.Tools), a.Status, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return errConflict
		}
		return fmt.Errorf("insert agent: %w", err)
	}
	return nil
}

// Get returns the agent for (org,name) or errNotFound.
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
	res, err := s.db.ExecContext(ctx,
		`UPDATE agents SET model=?,instructions=?,description=?,tools=?,status=?,updated_at=? WHERE org=? AND name=?`,
		a.Model, a.Instructions, a.Description, encodeList(a.Tools), a.Status, a.UpdatedAt, a.Org, a.Name)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errNotFound
	}
	return nil
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
