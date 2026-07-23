package functions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	errConflict = errors.New("functions: function already exists")
	errNotFound = errors.New("functions: function not found")
)

// Function is the org-scoped definition of a serverless function: a runtime
// (environment) + source code + resource limits + the NAMES of the secrets it
// mounts (values live in KMS, never here). Org isolation is the org column.
type Function struct {
	ID           string
	Org          string
	Name         string
	Namespace    string
	Runtime      string
	Image        string
	Code         string
	Handler      string
	Target       string // "" = sandbox (default) | "fleet" = org GPU fleet (fn.run)
	TimeoutSec   int
	MemoryLimit  string
	EnvNames     []string
	Status       string
	DeployVer    int
	CreatedAt    int64
	LastDeployAt int64
}

// Invocation is one execution of a function: real history, one row per call.
type Invocation struct {
	ID           string
	Org          string
	FunctionName string
	Status       string // ok | error | timeout
	StatusCode   int
	Method       string
	DurationMs   int64
	Output       string
	Error        string
	CreatedAt    int64
}

// Store is one org's functions database — ONE SQLite file per org at
// {DataDir}/orgs/{orgSlug}/functions.db (opened via cloud.OrgDB). functions is
// org-scoped (it carries no project axis); org-scoping is PHYSICAL, with the org
// column retained as defense-in-depth.
type Store struct {
	db *sql.DB
}

// openStore wraps an org DB — already opened + pragma'd by cloud.OrgDB —
// into the functions store, running its migration. It is the open func the shared
// cloud.OrgStore cache calls once per org file.
func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS functions (
  id             TEXT PRIMARY KEY,
  org            TEXT NOT NULL,
  name           TEXT NOT NULL,
  namespace      TEXT NOT NULL DEFAULT 'default',
  runtime        TEXT NOT NULL DEFAULT '',
  image          TEXT NOT NULL DEFAULT '',
  code           TEXT NOT NULL DEFAULT '',
  handler        TEXT NOT NULL DEFAULT '',
  target         TEXT NOT NULL DEFAULT '',
  timeout_sec    INTEGER NOT NULL DEFAULT 30,
  memory_limit   TEXT NOT NULL DEFAULT '256Mi',
  env_names      TEXT NOT NULL DEFAULT '[]',
  status         TEXT NOT NULL DEFAULT 'ready',
  deploy_version INTEGER NOT NULL DEFAULT 1,
  created_at     INTEGER NOT NULL,
  last_deploy_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_functions_org_name ON functions(org, name);
CREATE INDEX IF NOT EXISTS ix_functions_org_deployed ON functions(org, last_deploy_at);

CREATE TABLE IF NOT EXISTS invocations (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  function_name TEXT NOT NULL,
  status        TEXT NOT NULL,
  status_code   INTEGER NOT NULL DEFAULT 0,
  method        TEXT NOT NULL DEFAULT 'POST',
  duration_ms   INTEGER NOT NULL DEFAULT 0,
  output        TEXT NOT NULL DEFAULT '',
  error         TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_inv_org_fn_created ON invocations(org, function_name, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Additive column for pre-existing stores; "duplicate column" means done.
	if _, err := s.db.Exec(`ALTER TABLE functions ADD COLUMN target TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("migrate target: %w", err)
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

const fnCols = `id,org,name,namespace,runtime,image,code,handler,target,timeout_sec,memory_limit,env_names,status,deploy_version,created_at,last_deploy_at`

func scanFunction(sc interface{ Scan(...any) error }) (Function, error) {
	var f Function
	var env string
	err := sc.Scan(&f.ID, &f.Org, &f.Name, &f.Namespace, &f.Runtime, &f.Image, &f.Code,
		&f.Handler, &f.Target, &f.TimeoutSec, &f.MemoryLimit, &env, &f.Status, &f.DeployVer,
		&f.CreatedAt, &f.LastDeployAt)
	f.EnvNames = decodeList(env)
	return f, err
}

// Upsert creates a function at deploy version 1 or, when (org,name) already
// exists, redeploys it (advances deploy_version, updates the spec). One
// transaction so the record never drifts. Returns the post-write function.
func (s *Store) Upsert(ctx context.Context, f Function) (Function, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Function{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var curVer int
	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT deploy_version, created_at FROM functions WHERE org=? AND name=?`, f.Org, f.Name)
	switch err := row.Scan(&curVer, &createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		f.DeployVer = 1
		f.CreatedAt = f.LastDeployAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO functions (`+fnCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.ID, f.Org, f.Name, f.Namespace, f.Runtime, f.Image, f.Code, f.Handler, f.Target,
			f.TimeoutSec, f.MemoryLimit, encodeList(f.EnvNames), f.Status, f.DeployVer,
			f.CreatedAt, f.LastDeployAt); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return Function{}, errConflict
			}
			return Function{}, fmt.Errorf("insert function: %w", err)
		}
	case err != nil:
		return Function{}, fmt.Errorf("lookup function: %w", err)
	default:
		f.DeployVer = curVer + 1
		f.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE functions SET namespace=?,runtime=?,image=?,code=?,handler=?,target=?,timeout_sec=?,memory_limit=?,env_names=?,status=?,deploy_version=?,last_deploy_at=?
			 WHERE org=? AND name=?`,
			f.Namespace, f.Runtime, f.Image, f.Code, f.Handler, f.Target, f.TimeoutSec, f.MemoryLimit,
			encodeList(f.EnvNames), f.Status, f.DeployVer, f.LastDeployAt, f.Org, f.Name); err != nil {
			return Function{}, fmt.Errorf("update function: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Function{}, fmt.Errorf("commit: %w", err)
	}
	return f, nil
}

// Get returns the function for (org,name) or errNotFound.
func (s *Store) Get(ctx context.Context, org, name string) (Function, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+fnCols+` FROM functions WHERE org=? AND name=?`, org, name)
	f, err := scanFunction(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Function{}, errNotFound
	}
	if err != nil {
		return Function{}, fmt.Errorf("get function: %w", err)
	}
	return f, nil
}

// List returns every function for org, most-recently-deployed first.
func (s *Store) List(ctx context.Context, org string) ([]Function, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+fnCols+` FROM functions WHERE org=? ORDER BY last_deploy_at DESC, name ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list functions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Function
	for rows.Next() {
		f, err := scanFunction(rows)
		if err != nil {
			return nil, fmt.Errorf("scan function: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Delete removes a function and its invocation history. Reports whether a row went.
func (s *Store) Delete(ctx context.Context, org, name string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM functions WHERE org=? AND name=?`, org, name)
	if err != nil {
		return false, fmt.Errorf("delete function: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM invocations WHERE org=? AND function_name=?`, org, name); err != nil {
		return false, fmt.Errorf("delete invocations: %w", err)
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return n > 0, nil
}

// InsertInvocation records one function execution.
func (s *Store) InsertInvocation(ctx context.Context, iv Invocation) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invocations (id,org,function_name,status,status_code,method,duration_ms,output,error,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		iv.ID, iv.Org, iv.FunctionName, iv.Status, iv.StatusCode, iv.Method, iv.DurationMs,
		iv.Output, iv.Error, iv.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert invocation: %w", err)
	}
	return nil
}

// ListInvocations returns invocation history for (org,function), newest first.
func (s *Store) ListInvocations(ctx context.Context, org, fn string, limit int) ([]Invocation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,function_name,status,status_code,method,duration_ms,output,error,created_at
		 FROM invocations WHERE org=? AND function_name=? ORDER BY created_at DESC LIMIT ?`, org, fn, limit)
	if err != nil {
		return nil, fmt.Errorf("list invocations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Invocation
	for rows.Next() {
		var iv Invocation
		if err := rows.Scan(&iv.ID, &iv.Org, &iv.FunctionName, &iv.Status, &iv.StatusCode,
			&iv.Method, &iv.DurationMs, &iv.Output, &iv.Error, &iv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invocation: %w", err)
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// InvocationsSince returns raw invocation rows for an org since a unix time
// (newest first, capped) — the real basis for the metrics chart. One query
// across all the org's functions; bucketing happens in memory.
func (s *Store) InvocationsSince(ctx context.Context, org string, since int64, limit int) ([]Invocation, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,function_name,status,status_code,method,duration_ms,output,error,created_at
		 FROM invocations WHERE org=? AND created_at>=? ORDER BY created_at DESC LIMIT ?`, org, since, limit)
	if err != nil {
		return nil, fmt.Errorf("invocations since: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Invocation
	for rows.Next() {
		var iv Invocation
		if err := rows.Scan(&iv.ID, &iv.Org, &iv.FunctionName, &iv.Status, &iv.StatusCode,
			&iv.Method, &iv.DurationMs, &iv.Output, &iv.Error, &iv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invocation: %w", err)
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// InvStats is a real per-function rollup over a time window (since unix).
type InvStats struct {
	Count       int
	Errors      int
	SumDuration int64
}

// StatsSince returns invocation aggregates for (org,function) since a unix time
// — the REAL basis for invocations7d / successRate / avgDurationMs. Never a
// fabricated rollup.
func (s *Store) StatsSince(ctx context.Context, org, fn string, since int64) (InvStats, error) {
	var st InvStats
	var sumDur sql.NullInt64
	var errs sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(duration_ms),0), COALESCE(SUM(CASE WHEN status!='ok' THEN 1 ELSE 0 END),0)
		 FROM invocations WHERE org=? AND function_name=? AND created_at>=?`, org, fn, since).
		Scan(&st.Count, &sumDur, &errs)
	if err != nil {
		return InvStats{}, fmt.Errorf("stats: %w", err)
	}
	st.SumDuration = sumDur.Int64
	st.Errors = int(errs.Int64)
	return st, nil
}
