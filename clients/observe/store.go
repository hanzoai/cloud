package observe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both cgo and pure-Go build tags). Blank
	// import registers the driver; importing modernc directly would double-register.
	_ "github.com/hanzoai/sqlite"
)

// The observe SETTINGS store is the durable, per-org config half of the console
// product-detail surface: one JSON config document per (org, product). It is the
// metastore twin of the eval store (clients/eval/store.go) and follows the same
// discipline — Hanzo Base/SQLite, MaxOpenConns(1) to serialize writes against the
// file lock, one file for every org.
//
// Tenant isolation is the (org, product) COMPOSITE PRIMARY KEY and a mandatory
// `WHERE org=? AND product=?` on EVERY statement. The org value is c.Org() exactly
// as SanitizeIdentity minted it from the validated owner claim (HIP-0026) — never
// normalized (casing/trimming collapses distinct owners into one bucket) and never
// a client-supplied header.
//
// SECRETS NEVER LAND HERE. A settings field whose key is secret-shaped (see
// secretKey) has its VALUE written to KMS at orgs/{org}/settings/{product}/{key};
// this table stores only the non-secret JSON plus the list of secret key NAMES
// (so the read path knows which fields are set-but-masked). A plaintext secret can
// never reach this file — the handler routes secret writes to KMS or fails closed.

var errNotFound = errors.New("observe: settings not found")

// Settings is the persisted per-(org,product) config record. Config is the
// non-secret JSON document; SecretKeys names the fields whose values live in KMS.
type Settings struct {
	Org        string
	Product    string
	Config     string   // opaque non-secret JSON object, stored verbatim (bounded at the edge)
	SecretKeys []string // names of secret fields (values in KMS, never here)
	CreatedAt  int64
	UpdatedAt  int64
}

// SettingsStore is the observe settings metastore over one SQLite file
// ({DataDir}/observe.db). Tenancy is the (org, product) key.
type SettingsStore struct {
	db *sql.DB
}

func openSettingsStore(path string) (*SettingsStore, error) {
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
	s := &SettingsStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SettingsStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS settings (
  org         TEXT NOT NULL,
  product     TEXT NOT NULL,
  config      TEXT NOT NULL DEFAULT '{}',
  secret_keys TEXT NOT NULL DEFAULT '[]',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (org, product)
);
CREATE INDEX IF NOT EXISTS ix_settings_org ON settings(org, updated_at);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *SettingsStore) Close() error { return s.db.Close() }

// Get returns the persisted settings for (org, product), or errNotFound when the
// org has never written config for that product (the caller then serves the
// product defaults merged with an empty override).
func (s *SettingsStore) Get(ctx context.Context, org, product string) (Settings, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT org, product, config, secret_keys, created_at, updated_at
		   FROM settings WHERE org=? AND product=?`, org, product)
	var st Settings
	var secretKeys string
	err := row.Scan(&st.Org, &st.Product, &st.Config, &secretKeys, &st.CreatedAt, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, errNotFound
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	st.SecretKeys = decodeStrList(secretKeys)
	return st, nil
}

// Put upserts the non-secret config document + secret-key list for (org, product).
// It is idempotent on the composite key. The write NEVER carries secret values —
// those are already in KMS by the time this is called (see the handler).
func (s *SettingsStore) Put(ctx context.Context, st Settings) (Settings, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var createdAt int64
	row := tx.QueryRowContext(ctx, `SELECT created_at FROM settings WHERE org=? AND product=?`, st.Org, st.Product)
	switch err := row.Scan(&createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		st.CreatedAt = st.UpdatedAt
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (org, product, config, secret_keys, created_at, updated_at)
			 VALUES (?,?,?,?,?,?)`,
			st.Org, st.Product, st.Config, encodeStrList(st.SecretKeys), st.CreatedAt, st.UpdatedAt); err != nil {
			return Settings{}, fmt.Errorf("insert settings: %w", err)
		}
	case err != nil:
		return Settings{}, fmt.Errorf("lookup settings: %w", err)
	default:
		st.CreatedAt = createdAt
		if _, err := tx.ExecContext(ctx,
			`UPDATE settings SET config=?, secret_keys=?, updated_at=? WHERE org=? AND product=?`,
			st.Config, encodeStrList(st.SecretKeys), st.UpdatedAt, st.Org, st.Product); err != nil {
			return Settings{}, fmt.Errorf("update settings: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, fmt.Errorf("commit: %w", err)
	}
	return st, nil
}
