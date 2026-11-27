package forgeimport

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// State of a repository in the registry.
const (
	Pending = "pending"
	Running = "running"
	Done    = "done"
	Failed  = "failed"
)

// Row is one forge repository's place in the import.
type Row struct {
	Owner     string // the forge namespace
	Repo      string
	Org       string // the org whose objects store holds it
	StoreID   string // the repository's id in that store
	State     string
	Attempts  int64
	Error     string
	LastSHA   string // digest of the ref set last imported
	Refs      int64
	Objects   int64
	Bytes     int64 // as stored
	SizeKB    int64 // the forge's own figure, to order the work
	Updated   int64
	Verified  int64  // unix time of the last verification, 0 never
	VerifyHow string // refs | refs+count | fsck | fsck-connectivity
}

// Registry is the importer's record, a table of the database it is given (hanzo-sql in
// production). Nothing else is kept: a repository's state is a row, so a run that dies
// is resumed by the next one.
type Registry struct {
	db *sql.DB
	d  dialect.Dialect
}

// OpenRegistry migrates db and wraps it.
func OpenRegistry(ctx context.Context, db *sql.DB) (*Registry, error) {
	r := &Registry{db: db, d: dialect.Of(db)}
	err := r.d.Migrate(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS forge_import (
  owner      TEXT NOT NULL,
  repo       TEXT NOT NULL,
  org        TEXT NOT NULL,
  store_id   TEXT NOT NULL,
  state      TEXT NOT NULL,
  attempts   BIGINT NOT NULL DEFAULT 0,
  error      TEXT NOT NULL DEFAULT '',
  last_sha   TEXT NOT NULL DEFAULT '',
  refs       BIGINT NOT NULL DEFAULT 0,
  objects    BIGINT NOT NULL DEFAULT 0,
  bytes      BIGINT NOT NULL DEFAULT 0,
  size_kb    BIGINT NOT NULL DEFAULT 0,
  updated    BIGINT NOT NULL DEFAULT 0,
  verified   BIGINT NOT NULL DEFAULT 0,
  verify_how TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (owner, repo)
);
CREATE TABLE IF NOT EXISTS forge_import_lease (
  name   TEXT PRIMARY KEY,
  holder TEXT NOT NULL,
  until  BIGINT NOT NULL
);`)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("forgeimport: migrate: %w", err)
	}
	return r, nil
}

// Discover records a repository as pending, or refreshes its size and mapping, keeping
// whatever state it has.
func (r *Registry) Discover(ctx context.Context, row Row) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO forge_import (owner, repo, org, store_id, state, size_kb, updated)
VALUES ($1, $2, $3, $4, 'pending', $5, $6)
ON CONFLICT (owner, repo) DO UPDATE SET org = excluded.org, store_id = excluded.store_id, size_kb = excluded.size_kb`,
		row.Owner, row.Repo, row.Org, row.StoreID, row.SizeKB, time.Now().Unix())
	return err
}

const cols = `owner, repo, org, store_id, state, attempts, error, last_sha, refs, objects, bytes, size_kb, updated, verified, verify_how`

func scan(sc interface{ Scan(...any) error }) (Row, error) {
	var x Row
	err := sc.Scan(&x.Owner, &x.Repo, &x.Org, &x.StoreID, &x.State, &x.Attempts, &x.Error, &x.LastSHA,
		&x.Refs, &x.Objects, &x.Bytes, &x.SizeKB, &x.Updated, &x.Verified, &x.VerifyHow)
	return x, err
}

// Get reads one row.
func (r *Registry) Get(ctx context.Context, owner, repo string) (Row, error) {
	return scan(r.db.QueryRowContext(ctx, `SELECT `+cols+` FROM forge_import WHERE owner = $1 AND repo = $2`, owner, repo))
}

// All lists every row, smallest first.
func (r *Registry) All(ctx context.Context) ([]Row, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+cols+` FROM forge_import ORDER BY size_kb, owner, repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		x, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Work lists what a run should do, smallest first: pending, running (a run that died),
// and failed with attempts left; with resync, done ones too (their refs are compared).
func (r *Registry) Work(ctx context.Context, maxAttempts int64, resync bool) ([]Row, error) {
	all, err := r.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []Row
	for _, x := range all {
		switch {
		case x.State == Pending, x.State == Running:
			out = append(out, x)
		case x.State == Failed && x.Attempts < maxAttempts:
			out = append(out, x)
		case x.State == Done && resync:
			out = append(out, x)
		}
	}
	return out, nil
}

// Start marks a repository running and counts the attempt.
func (r *Registry) Start(ctx context.Context, owner, repo string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE forge_import SET state = 'running', attempts = attempts + 1, updated = $1 WHERE owner = $2 AND repo = $3`,
		time.Now().Unix(), owner, repo)
	return err
}

// Finish records a repository's outcome.
func (r *Registry) Finish(ctx context.Context, x Row) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE forge_import SET state = $1, error = $2, last_sha = $3, refs = $4, objects = $5, bytes = $6,
  updated = $7, verified = $8, verify_how = $9
WHERE owner = $10 AND repo = $11`,
		x.State, x.Error, x.LastSHA, x.Refs, x.Objects, x.Bytes, time.Now().Unix(), x.Verified, x.VerifyHow, x.Owner, x.Repo)
	return err
}

// Reset puts a repository back to pending (a run that wants it done again).
func (r *Registry) Reset(ctx context.Context, owner, repo string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE forge_import SET state = 'pending', attempts = 0, error = '' WHERE owner = $1 AND repo = $2`, owner, repo)
	return err
}

// Lease takes the named lease for ttl, or answers false when another holder has it.
// The holder that has it renews by asking again.
func (r *Registry) Lease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error) {
	now := time.Now()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO forge_import_lease (name, holder, until) VALUES ($1, $2, 0) ON CONFLICT (name) DO NOTHING`, name, holder); err != nil {
		return false, err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE forge_import_lease SET holder = $1, until = $2 WHERE name = $3 AND (holder = $1 OR until < $4)`,
		holder, now.Add(ttl).Unix(), name, now.Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Release gives the lease up.
func (r *Registry) Release(ctx context.Context, name, holder string) {
	_, _ = r.db.ExecContext(ctx, `UPDATE forge_import_lease SET until = 0 WHERE name = $1 AND holder = $2`, name, holder)
}
