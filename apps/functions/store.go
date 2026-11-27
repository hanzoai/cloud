package functions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
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

// Store is one org's functions database — ONE database per org: a SQLite file at
// {DataDir}/orgs/{orgSlug}/functions.db (opened via cloud.OrgDB), or on hanzo-sql a
// schema of that name. functions is org-scoped (it carries no project axis);
// org-scoping is PHYSICAL, with the org column retained as defense-in-depth. Its SQL
// is the dialect both engines share (sdk/dialect).
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

// migrate brings the database to the current schema in ONE transaction that holds
// the store's migration lock; everything below goes through tx.
func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
		// Additive column for pre-existing stores.
		has, err := d.HasColumn(ctx, tx, "functions", "target")
		if err != nil || has {
			return err
		}
		_, err = tx.ExecContext(ctx, `ALTER TABLE functions ADD COLUMN target TEXT NOT NULL DEFAULT ''`)
		return err
	}); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

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
  timeout_sec    BIGINT NOT NULL DEFAULT 30,
  memory_limit   TEXT NOT NULL DEFAULT '256Mi',
  env_names      TEXT NOT NULL DEFAULT '[]',
  status         TEXT NOT NULL DEFAULT 'ready',
  deploy_version BIGINT NOT NULL DEFAULT 1,
  created_at     BIGINT NOT NULL,
  last_deploy_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_functions_org_name ON functions(org, name);
CREATE INDEX IF NOT EXISTS ix_functions_org_deployed ON functions(org, last_deploy_at);

CREATE TABLE IF NOT EXISTS invocations (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  function_name TEXT NOT NULL,
  status        TEXT NOT NULL,
  status_code   BIGINT NOT NULL DEFAULT 0,
  method        TEXT NOT NULL DEFAULT 'POST',
  duration_ms   BIGINT NOT NULL DEFAULT 0,
  output        TEXT NOT NULL DEFAULT '',
  error         TEXT NOT NULL DEFAULT '',
  created_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_inv_org_fn_created ON invocations(org, function_name, created_at);
`

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
	if err := dialect.Text(append([]string{f.ID, f.Org, f.Name, f.Namespace, f.Runtime, f.Image, f.Code, f.Handler, f.Target, f.MemoryLimit, f.Status}, f.EnvNames...)...); err != nil {
		return Function{}, fmt.Errorf("upsert function: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Function{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var curVer int
	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT deploy_version, created_at FROM functions WHERE org=$1 AND name=$2`, f.Org, f.Name)
	switch err := row.Scan(&curVer, &createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		f.DeployVer = 1
		f.CreatedAt = f.LastDeployAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO functions (`+fnCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			f.ID, f.Org, f.Name, f.Namespace, f.Runtime, f.Image, f.Code, f.Handler, f.Target,
			f.TimeoutSec, f.MemoryLimit, encodeList(f.EnvNames), f.Status, f.DeployVer,
			f.CreatedAt, f.LastDeployAt); err != nil {
			if dialect.Unique(err) {
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
			`UPDATE functions SET namespace=$1,runtime=$2,image=$3,code=$4,handler=$5,target=$6,timeout_sec=$7,memory_limit=$8,env_names=$9,status=$10,deploy_version=$11,last_deploy_at=$12
			 WHERE org=$13 AND name=$14`,
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
	row := s.db.QueryRowContext(ctx, `SELECT `+fnCols+` FROM functions WHERE org=$1 AND name=$2`, org, name)
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
		`SELECT `+fnCols+` FROM functions WHERE org=$1 ORDER BY last_deploy_at DESC, name ASC`, org)
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
	res, err := tx.ExecContext(ctx, `DELETE FROM functions WHERE org=$1 AND name=$2`, org, name)
	if err != nil {
		return false, fmt.Errorf("delete function: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM invocations WHERE org=$1 AND function_name=$2`, org, name); err != nil {
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
	if err := dialect.Text(iv.ID, iv.Org, iv.FunctionName, iv.Status, iv.Method, iv.Output, iv.Error); err != nil {
		return fmt.Errorf("insert invocation: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO invocations (id,org,function_name,status,status_code,method,duration_ms,output,error,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
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
		 FROM invocations WHERE org=$1 AND function_name=$2 ORDER BY created_at DESC LIMIT $3`, org, fn, limit)
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
		 FROM invocations WHERE org=$1 AND created_at>=$2 ORDER BY created_at DESC LIMIT $3`, org, since, limit)
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
		`SELECT COUNT(*), CAST(COALESCE(SUM(duration_ms),0) AS BIGINT), CAST(COALESCE(SUM(CASE WHEN status<>'ok' THEN 1 ELSE 0 END),0) AS BIGINT)
		 FROM invocations WHERE org=$1 AND function_name=$2 AND created_at>=$3`, org, fn, since).
		Scan(&st.Count, &sumDur, &errs)
	if err != nil {
		return InvStats{}, fmt.Errorf("stats: %w", err)
	}
	st.SumDuration = sumDur.Int64
	st.Errors = int(errs.Int64)
	return st, nil
}
