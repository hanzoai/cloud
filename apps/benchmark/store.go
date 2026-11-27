package benchmark

// store.go — the arena's one database.
//
// Claims, attempts and presets were three append-only JSONL stores under the pod's data
// directory and a process mutex around the writes that check before they write. They are
// one store now, in the one dialect both engines share (sdk/dialect), opened
// through locus.Use like every other store, so it is a schema on hanzo-sql and moves
// there from its file when the deployment's policy says to. Every replica reads the same
// rows, and the mutex is the store's write transaction (Dialect.BeginWrite): the daily
// limit's count, the once-per-run check and the rows they admit are one transaction,
// so two replicas cannot both fit under one remaining allowance or both import one run.
//
// APPEND-ONLY STILL. A correction is evidence too, so a restated claim is a new row and
// the newest row of a key is the one that counts (effective). Nothing is updated in place.
// Each row is its document as JSON with the columns a query needs beside it.
//
// A WRITE IS A TRANSACTION, AND ITS READS ARE THE TRANSACTION'S. On a file there is one
// connection, and on hanzo-sql a pool of four whose writers each hold one while they
// wait for the lock, so the work under the lock asks the store only through the
// transaction it holds (book). The audit trail is another store and is asked as before.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Use is the ONE opener: on hanzo-sql the store is a schema of the system
	// namespace, moved there from its file when the deployment's policy says to; without
	// a DSN the database is born encrypted under the key cek derives from the process
	// master and that namespace, with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags).
	_ "github.com/hanzoai/sqlite"
)

// subsystem names the store in the system namespace; manifest.SQLStores lists it.
const subsystem = "benchmark"

// within bounds one statement that is not under a request's own deadline.
const within = 30 * time.Second

// querier is what a *sql.DB and a *sql.Tx both answer.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the benchmark plane's database. Its methods read through the pool; a write
// that checks before it writes goes through [Store.write], which hands it a [book] on
// the transaction that holds the store's write lock.
type Store struct {
	book
	db *sql.DB
}

// book is the arena's tables over one querier: the pool for a read, the transaction for
// a write under the lock.
type book struct{ q querier }

func openStore(dir string) (*Store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		d := dialect.Of(db)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, schema(d))
			return err
		}); err != nil {
			return nil, fmt.Errorf("benchmark: migrate: %w", err)
		}
		return &Store{book: book{db}, db: db}, nil
	})
}

func schema(d dialect.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS benchmark_claims (
  seq       ` + d.Identity() + `,
  benchmark TEXT NOT NULL,
  org       TEXT NOT NULL,
  written   BIGINT NOT NULL,
  doc       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_benchmark_claims_benchmark ON benchmark_claims(benchmark, seq);
CREATE INDEX IF NOT EXISTS ix_benchmark_claims_org ON benchmark_claims(org, written);
CREATE TABLE IF NOT EXISTS benchmark_attempts (
  seq       ` + d.Identity() + `,
  benchmark TEXT NOT NULL,
  model     TEXT NOT NULL,
  item      TEXT NOT NULL,
  run       TEXT NOT NULL DEFAULT '',
  doc       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_benchmark_attempts_benchmark ON benchmark_attempts(benchmark, seq);
CREATE INDEX IF NOT EXISTS ix_benchmark_attempts_run ON benchmark_attempts(benchmark, model, run);
CREATE TABLE IF NOT EXISTS benchmark_presets (
  seq     ` + d.Identity() + `,
  owner   TEXT NOT NULL,
  name    TEXT NOT NULL,
  written BIGINT NOT NULL,
  doc     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_benchmark_presets_owner ON benchmark_presets(owner, seq);
CREATE TABLE IF NOT EXISTS benchmark_legacy (
  source   TEXT PRIMARY KEY,
  imported BIGINT NOT NULL,
  n        BIGINT NOT NULL
);`
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// write runs fn in the store's one writing transaction: the lock is held from before
// fn's first read until the commit, and fn's work is rolled back if it returns an error.
// fn asks the store only through the book it is given.
func (s *Store) write(ctx context.Context, fn func(book) error) error {
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("benchmark: begin a write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(book{tx}); err != nil {
		return err
	}
	return tx.Commit()
}

// ── claims ───────────────────────────────────────────────────────────────────

// claimReader is what effective reads claims through: the pool, or a write's book.
type claimReader interface {
	Claims(ctx context.Context, benchmark string) ([]storedClaim, error)
}

// Claims returns stored claims for a benchmark in the order they were stored, or all of
// them when the benchmark is empty. Seed rows are NOT included — layering is the caller's.
func (b book) Claims(ctx context.Context, benchmark string) ([]storedClaim, error) {
	query, args := `SELECT doc FROM benchmark_claims ORDER BY seq`, []any{}
	if benchmark != "" {
		query, args = `SELECT doc FROM benchmark_claims WHERE benchmark=$1 ORDER BY seq`, []any{benchmark}
	}
	return docs[storedClaim](ctx, b.q, query, args...)
}

// PutClaim records one claim. Append-only: a correction is a new row, and the newest row
// for a claimKey is the one that counts.
func (b book) PutClaim(ctx context.Context, c storedClaim) error {
	if c.Benchmark == "" || c.Model == "" {
		return fmt.Errorf("claim needs a benchmark and a model")
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	return b.insertClaim(ctx, c)
}

// insertClaim stores a claim as it is: PutClaim's checks are a writer's, and the rows the
// file stores recorded are carried (legacy.go) whatever they hold.
func (b book) insertClaim(ctx context.Context, c storedClaim) error {
	if err := dialect.Text(c.Benchmark, c.Org); err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	doc, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = b.q.ExecContext(ctx, `INSERT INTO benchmark_claims (benchmark, org, written, doc) VALUES ($1, $2, $3, $4)`,
		c.Benchmark, c.Org, c.At.Unix(), string(doc))
	return err
}

// writtenToday counts the rows org has stored since the start of now's UTC day.
func (b book) writtenToday(ctx context.Context, org string, now time.Time) (int, error) {
	day := now.UTC().Truncate(24 * time.Hour)
	var n int
	err := b.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM benchmark_claims WHERE org=$1 AND written>=$2`, org, day.Unix()).Scan(&n)
	return n, err
}

// ── attempts ─────────────────────────────────────────────────────────────────

// Attempts returns every stored attempt for a benchmark (the measured plane), or for all
// of them when the benchmark is empty.
func (b book) Attempts(ctx context.Context, benchmark string) ([]attempt, error) {
	query, args := `SELECT doc FROM benchmark_attempts ORDER BY seq`, []any{}
	if benchmark != "" {
		query, args = `SELECT doc FROM benchmark_attempts WHERE benchmark=$1 ORDER BY seq`, []any{benchmark}
	}
	return docs[attempt](ctx, b.q, query, args...)
}

// imported reports whether a run of model on benchmark is stored already.
func (b book) imported(ctx context.Context, benchmark, model, run string) (bool, error) {
	var one int
	err := b.q.QueryRowContext(ctx, `SELECT 1 FROM benchmark_attempts WHERE benchmark=$1 AND model=$2 AND run=$3 LIMIT 1`,
		benchmark, model, run).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Append records attempts. Append-only and idempotent by what a re-run is: a re-scored
// label is a new attempt, and raw responses are never overwritten. On a book that is a
// write's transaction the attempts land with it; on the Store they land whole or not at
// all, in a transaction of their own.
func (b book) Append(ctx context.Context, as ...attempt) error {
	for _, a := range as {
		if err := dialect.Text(a.Benchmark, a.ID, a.Model, a.Run); err != nil {
			return fmt.Errorf("attempt %q: %w", a.ID, err)
		}
		doc, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if _, err := b.q.ExecContext(ctx, `INSERT INTO benchmark_attempts (benchmark, model, item, run, doc) VALUES ($1, $2, $3, $4, $5)`,
			a.Benchmark, a.Model, a.ID, a.Run, string(doc)); err != nil {
			return err
		}
	}
	return nil
}

// Append records attempts in one transaction.
func (s *Store) Append(ctx context.Context, as ...attempt) error {
	return s.write(ctx, func(b book) error { return b.Append(ctx, as...) })
}

// ── presets ──────────────────────────────────────────────────────────────────

// Versions is every version org has stored, in the order they were stored.
func (b book) Versions(ctx context.Context, org string) ([]Preset, error) {
	return docs[Preset](ctx, b.q, `SELECT doc FROM benchmark_presets WHERE owner=$1 ORDER BY seq`, org)
}

// PutPreset records one version. Append-only: the newest version of an (org, name) is
// the one that counts.
func (b book) PutPreset(ctx context.Context, p Preset) error {
	if p.Owner == "" || p.Name == "" {
		return fmt.Errorf("preset needs an owner and a name")
	}
	if err := dialect.Text(p.Owner, p.Name); err != nil {
		return fmt.Errorf("preset: %w", err)
	}
	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = b.q.ExecContext(ctx, `INSERT INTO benchmark_presets (owner, name, written, doc) VALUES ($1, $2, $3, $4)`,
		p.Owner, p.Name, p.At.Unix(), string(doc))
	return err
}

// docs reads the JSON documents a query selects, in the order it gives them.
func docs[T any](ctx context.Context, q querier, query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			return nil, fmt.Errorf("benchmark: a stored row is not a row: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
