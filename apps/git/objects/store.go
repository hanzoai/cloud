package objects

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/namespace"
)

// MaxObject is the largest object, uncompressed, a push may carry. Reading a push
// inflates one object at a time and holds it, so the bound is what a replica can
// afford to hold; a clone does not hold an object that large, it streams the stored
// bytes. A pack carrying a larger object is refused by name.
const MaxObject = 256 << 20

var (
	// ErrExists is a repository that is already there.
	ErrExists = errors.New("objects: repository already exists")
	// ErrNotFound is a repository the store does not hold.
	ErrNotFound = errors.New("objects: no such repository")
	// ErrTooLarge is an object over MaxObject.
	ErrTooLarge = errors.New("objects: object is larger than the store takes")
	// ErrNotCommit is a branch that names something other than a commit.
	ErrNotCommit = errors.New("objects: a branch must name a commit")
	// ErrMissing is a push whose objects refer to objects the repository does not have.
	ErrMissing = errors.New("objects: push refers to objects the repository does not have")
)

// Store is one org's repositories' objects.
type Store struct {
	db     *sql.DB
	d      dialect.Dialect
	blobs  Blobs
	prefix string
	// tag names this Store to the pod's caches; cache is the pod's.
	tag   string
	cache *caches
	// now is the clock the grace periods read; a test moves it.
	now func() time.Time
}

// Open migrates the database and wraps it, with pack bytes in blobs under the prefix of
// namespace ns. A store opened with no blobs (nil) refuses every read and write of an
// object with ErrNoBlobs and keeps nothing on a disk. Rows a release that kept objects
// in the database left behind are moved into packs before Open answers.
func Open(db *sql.DB, blobs Blobs, ns namespace.Namespace) (*Store, error) {
	if blobs == nil {
		blobs = absent{}
	}
	sum := sha256.Sum256([]byte(ns.ID()))
	s := &Store{db: db, d: dialect.Of(db), blobs: blobs, prefix: "git/" + hex.EncodeToString(sum[:8]), tag: token(), cache: pod(), now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("objects: migrate: %w", err)
	}
	if err := s.moveLegacy(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("objects: move rows into packs: %w", err)
	}
	return s, nil
}

// Opener is Open as the func an OrgStore calls once per org.
func Opener(blobs Blobs) func(namespace.Namespace, *sql.DB) (*Store, error) {
	return func(ns namespace.Namespace, db *sql.DB) (*Store, error) { return Open(db, blobs, ns) }
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// packKey is where pack id of repository repo lives in the blob store. The repository is
// named by a digest, so a key carries no name a log would leak and no repository's prefix
// is another's.
func (s *Store) packKey(repo, pack string) string {
	return s.repoPrefix(repo) + "pack-" + pack + ".pack"
}

func (s *Store) repoPrefix(repo string) string { return s.prefix + "/" + s.repoDir(repo) + "/" }

// repoDir is the name a repository's blobs are kept under: a digest, so a key carries no
// name. Its first hex digit is the shard Maintain lists the bucket by.
func (s *Store) repoDir(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(sum[:10])
}

func (s *Store) migrate(ctx context.Context, tx *sql.Tx) error {
	blob, bytewise := s.d.Blob(), s.d.Bytewise()
	had, err := s.d.HasTable(ctx, tx, "entries")
	if err != nil {
		return err
	}
	ddl := `
CREATE TABLE IF NOT EXISTS repos (
  repo    TEXT PRIMARY KEY,
  bytes   BIGINT NOT NULL DEFAULT 0,
  created BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS packs (
  repo    TEXT NOT NULL,
  pack    TEXT NOT NULL,
  bytes   BIGINT NOT NULL,
  objects BIGINT NOT NULL,
  created BIGINT NOT NULL,
  PRIMARY KEY (repo, pack)
);
CREATE TABLE IF NOT EXISTS entries (
  repo TEXT NOT NULL,
  oid  ` + blob + ` NOT NULL,
  pack TEXT NOT NULL,
  at   BIGINT NOT NULL,
  n    BIGINT NOT NULL,
  type BIGINT NOT NULL,
  size BIGINT NOT NULL,
  base ` + blob + `,
  PRIMARY KEY (repo, oid)
);
CREATE TABLE IF NOT EXISTS doomed (
  key   TEXT PRIMARY KEY,
  after BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS leases (
  name    TEXT PRIMARY KEY,
  holder  TEXT NOT NULL,
  expires BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS sweeps (
  name TEXT PRIMARY KEY,
  at   BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS moves (
  job     TEXT NOT NULL,
  oid     ` + blob + ` NOT NULL,
  at      BIGINT NOT NULL,
  n       BIGINT NOT NULL,
  created BIGINT NOT NULL,
  PRIMARY KEY (job, oid)
);
CREATE TABLE IF NOT EXISTS refs (
  repo  TEXT NOT NULL,
  name  TEXT NOT NULL ` + bytewise + `,
  value TEXT NOT NULL,
  PRIMARY KEY (repo, name)
);
CREATE TABLE IF NOT EXISTS shallows (
  repo TEXT NOT NULL,
  oid  ` + blob + ` NOT NULL,
  PRIMARY KEY (repo, oid)
);
CREATE TABLE IF NOT EXISTS files (
  repo TEXT NOT NULL,
  name TEXT NOT NULL,
  data ` + blob + ` NOT NULL,
  PRIMARY KEY (repo, name)
);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if !had {
		// A table that is there already is not asked to make an index again: that takes the
		// table's writers' lock, and every replica opens its stores while the others write.
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS entries_by_pack ON entries (repo, pack, at)`); err != nil {
			return err
		}
	}
	return nil
}

// Has reports whether the store holds repository id.
func (s *Store) Has(ctx context.Context, id string) (bool, error) {
	return has(ctx, s.db, id)
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func has(ctx context.Context, q querier, id string) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos WHERE repo = $1`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("objects: has %s: %w", id, err)
	}
	return n > 0, nil
}

// Size is the bytes repository id occupies: the packs it holds, as stored. It is a
// counter the writers keep, not a scan, because a push meters it.
func (s *Store) Size(ctx context.Context, id string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT bytes FROM repos WHERE repo = $1`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("objects: size %s: %w", id, err)
	}
	return n, nil
}

// Remove deletes repository id and everything of it, in one transaction, and then its
// packs from the blob store. A repository that is not there is not an error: a delete
// is repeated. The packs are recorded as doomed in the same transaction, so a delete
// whose blob requests fail is finished by Collect.
func (s *Store) Remove(ctx context.Context, id string) error {
	tx, err := s.d.Serialize(ctx, s.db, id)
	if err != nil {
		return fmt.Errorf("objects: remove %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT pack FROM packs WHERE repo = $1`, id)
	if err != nil {
		return fmt.Errorf("objects: remove %s: %w", id, err)
	}
	var keys []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return fmt.Errorf("objects: remove %s: %w", id, err)
		}
		keys = append(keys, s.packKey(id, p))
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("objects: remove %s: %w", id, err)
	}
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO doomed (key, after) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, k, s.now().Unix()); err != nil {
			return fmt.Errorf("objects: remove %s: %w", id, err)
		}
	}
	for _, stmt := range []string{
		`DELETE FROM entries WHERE repo = $1`,
		`DELETE FROM packs WHERE repo = $1`,
		`DELETE FROM refs WHERE repo = $1`,
		`DELETE FROM shallows WHERE repo = $1`,
		`DELETE FROM files WHERE repo = $1`,
		`DELETE FROM repos WHERE repo = $1`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return fmt.Errorf("objects: remove %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cache.locs.purge(s.tag + "|" + id + "|")
	s.reap(ctx, keys)
	return nil
}

// reap deletes blobs that are recorded as doomed, and forgets the ones it deleted. A blob
// it cannot delete stays recorded for the next Collect.
func (s *Store) reap(ctx context.Context, keys []string) {
	for _, k := range keys {
		if err := s.blobs.Delete(ctx, k); err != nil {
			continue
		}
		_, _ = s.db.ExecContext(ctx, `DELETE FROM doomed WHERE key = $1`, k)
	}
}

// Repo is repository id read and written without a transaction: every call is its own
// statement. A reader uses this; a writer that must land a set of changes together uses
// Begin.
func (s *Store) Repo(ctx context.Context, id string) *Repo {
	return newRepo(ctx, s.db, s, id)
}

// Create makes an empty repository whose HEAD names branch, ErrExists when id is there.
func (s *Store) Create(ctx context.Context, id, branch string) error {
	tx, err := s.Make(ctx, id, branch)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return tx.Commit()
}

func now() int64 { return time.Now().Unix() }

// hashSize is the length of an object name in bytes.
const hashSize = 20

func hashOf(b []byte) plumbing.Hash {
	var h plumbing.Hash
	copy(h[:], b)
	return h
}
