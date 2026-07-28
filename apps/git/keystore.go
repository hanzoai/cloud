package git

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/hanzoai/sqlite" // registers the "sqlite" driver
)

// keystore.go is the SSH public-key registry: the ONE place a presented SSH key
// is resolved to an (org, user). It is a SINGLE global SQLite file
// ({DataDir}/git/ssh_keys.db) keyed by the key fingerprint, NOT a per-org file
// like the repo store — because the SSH PublicKeyCallback runs BEFORE any org is
// known: the ONLY thing the server has is the presented key. Auth is therefore a
// single fingerprint lookup, and the row carries the org the key belongs to.
//
// The fingerprint (SHA256, RFC 4253 form) is the PRIMARY KEY, so a given public
// key belongs to exactly one org: re-registering the same key under a different
// org is refused (errKeyConflict). Listing + deletion filter by org, so an org
// only ever sees/removes its own keys.
//
// What is stored: the key TITLE (a human label), the full authorized-key line
// (the public key — public keys are NOT secrets, and the full bytes are needed
// to match a presented key exactly), the SHA256 fingerprint (the lookup key),
// the owning org, and the registering user id. No private material ever touches
// this store; there is nothing here to hash — a fingerprint is already a hash.

var (
	// errKeyConflict is returned when a fingerprint is already registered (to
	// this or another org). Handlers map it to 409.
	errKeyConflict = errors.New("git: ssh key already registered")
	// errKeyNotFound is returned when a delete misses. Handlers map it to 404.
	errKeyNotFound = errors.New("git: ssh key not found")
)

// sshKey is one registered public key row.
type sshKey struct {
	ID          string
	Org         string
	UserID      string
	Title       string
	PublicKey   string // full authorized-key line, e.g. "ssh-ed25519 AAAA... comment"
	Fingerprint string // SHA256:... (RFC 4253 form from ssh.FingerprintSHA256)
	CreatedAt   int64
}

// keyStore is the global SSH-key registry DB. MaxOpenConns(1) serializes writes
// against the file lock, matching the repo store's discipline.
type keyStore struct {
	db *sql.DB
}

// openKeyStore opens (creating if absent) the global SSH-key registry and runs
// its migration.
func openKeyStore(path string) (*keyStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open ssh key db: %w", err)
	}
	db.SetMaxOpenConns(1) // serialize writes against the file lock (same discipline as OrgDB)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("ssh key db pragma %q: %w", pragma, err)
		}
	}
	ks := &keyStore{db: db}
	if err := ks.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return ks, nil
}

func (k *keyStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS ssh_keys (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  user_id     TEXT NOT NULL DEFAULT '',
  title       TEXT NOT NULL DEFAULT '',
  public_key  TEXT NOT NULL,
  fingerprint TEXT NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_ssh_keys_org ON ssh_keys(org, created_at);
`
	if _, err := k.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate ssh keys: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (k *keyStore) Close() error { return k.db.Close() }

// Add inserts a new key row. Returns errKeyConflict when the fingerprint is
// already registered (UNIQUE on fingerprint — a key belongs to exactly one org).
func (k *keyStore) Add(ctx context.Context, key sshKey) error {
	_, err := k.db.ExecContext(ctx,
		`INSERT INTO ssh_keys (id,org,user_id,title,public_key,fingerprint,created_at) VALUES (?,?,?,?,?,?,?)`,
		key.ID, key.Org, key.UserID, key.Title, key.PublicKey, key.Fingerprint, key.CreatedAt)
	if err != nil {
		if isUnique(err) {
			return errKeyConflict
		}
		return fmt.Errorf("insert ssh key: %w", err)
	}
	return nil
}

// ByFingerprint resolves a presented key's fingerprint to its row — the auth
// lookup the SSH PublicKeyCallback runs. errKeyNotFound (fail CLOSED) when the
// key was never registered.
func (k *keyStore) ByFingerprint(ctx context.Context, fp string) (sshKey, error) {
	row := k.db.QueryRowContext(ctx,
		`SELECT id,org,user_id,title,public_key,fingerprint,created_at FROM ssh_keys WHERE fingerprint=?`, fp)
	return scanKey(row)
}

// List returns an org's registered keys, most-recent first. Filtered by org so an
// org never sees another's keys.
func (k *keyStore) List(ctx context.Context, org string) ([]sshKey, error) {
	rows, err := k.db.QueryContext(ctx,
		`SELECT id,org,user_id,title,public_key,fingerprint,created_at FROM ssh_keys WHERE org=? ORDER BY created_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list ssh keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []sshKey
	for rows.Next() {
		key, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ssh key: %w", err)
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// Delete removes an org's key by id. Scoped by org so an org can only delete
// its OWN keys (an id from another org matches no row). Returns errKeyNotFound
// when nothing went.
func (k *keyStore) Delete(ctx context.Context, org, id string) error {
	res, err := k.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE org=? AND id=?`, org, id)
	if err != nil {
		return fmt.Errorf("delete ssh key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errKeyNotFound
	}
	return nil
}

func scanKey(sc interface{ Scan(...any) error }) (sshKey, error) {
	var key sshKey
	err := sc.Scan(&key.ID, &key.Org, &key.UserID, &key.Title, &key.PublicKey, &key.Fingerprint, &key.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sshKey{}, errKeyNotFound
	}
	if err != nil {
		return sshKey{}, fmt.Errorf("scan ssh key: %w", err)
	}
	return key, nil
}

// keyView is the API shape for a registered key. The full public key is echoed
// (it is public); the fingerprint is the stable handle a UI shows.
type keyView struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"createdAt"`
}

func (key sshKey) view() keyView {
	return keyView{
		ID: key.ID, Title: key.Title, PublicKey: key.PublicKey,
		Fingerprint: key.Fingerprint, CreatedAt: time.Unix(key.CreatedAt, 0).UTC().Format(time.RFC3339),
	}
}
