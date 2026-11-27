package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both cgo and pure-Go build tags). Blank
	// import registers the driver; importing modernc directly would double-register.
	_ "github.com/hanzoai/sqlite"
)

// The settings STORE is the durable, per-org config half of the console
// product-detail surface: one JSON config document per (org, product). It follows
// the eval-store discipline — Hanzo Base, one database for every org (a schema on
// hanzo-sql, or a SQLite file with MaxOpenConns(1) to serialize writes against the
// file lock), in the dialect both engines share (sdk/dialect).
//
// Tenant isolation is the (org, product) COMPOSITE PRIMARY KEY and a mandatory
// `WHERE org=$1 AND product=$2` on EVERY statement. The org value is c.Org() exactly
// as SanitizeIdentity minted it from the validated owner claim (HIP-0026) — never
// normalized (casing/trimming collapses distinct owners into one bucket) and never
// a client-supplied header.
//
// SECRETS NEVER LAND HERE. A settings field whose key is secret-shaped has its
// VALUE written to KMS at orgs/{org}/settings/{product}/{key}; this table stores
// only the non-secret JSON plus the list of secret key NAMES (so the read path
// knows which fields are set-but-masked). A plaintext secret can never reach this
// file — the handler routes secret writes to KMS or fails closed.

var errNotFound = errors.New("settings: not found")

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

// SettingsStore is the settings metastore — the deployment's own "settings"
// subsystem, a schema on hanzo-sql or a SQLite file. Tenancy is the (org, product) key.
type SettingsStore struct {
	db *sql.DB
}

func openSettingsStore(dir string) (*SettingsStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "settings", dir, func(db *sql.DB) (*SettingsStore, error) {
		s := &SettingsStore{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *SettingsStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS settings (
  org         TEXT NOT NULL,
  product     TEXT NOT NULL,
  config      TEXT NOT NULL DEFAULT '{}',
  secret_keys TEXT NOT NULL DEFAULT '[]',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  PRIMARY KEY (org, product)
);
CREATE INDEX IF NOT EXISTS ix_settings_org ON settings(org, updated_at);`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return dialect.Of(s.db).Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		return nil
	})
}

// Close closes the underlying database.
func (s *SettingsStore) Close() error { return s.db.Close() }

// Get returns the persisted settings for (org, product), or errNotFound when the
// org has never written config for that product (the caller then serves the
// product defaults merged with an empty override).
func (s *SettingsStore) Get(ctx context.Context, org, product string) (Settings, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT org, product, config, secret_keys, created_at, updated_at
		   FROM settings WHERE org=$1 AND product=$2`, org, product)
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

// List returns every (org, product) record for one org, most-recently-written
// first — the cross-tenant "which products has this org configured" read behind the
// admin path. It rides the ix_settings_org(org, updated_at) index and, like Get,
// carries only non-secret documents plus secret-key names, never a secret value.
func (s *SettingsStore) List(ctx context.Context, org string) ([]Settings, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org, product, config, secret_keys, created_at, updated_at
		   FROM settings WHERE org=$1 ORDER BY updated_at DESC, product ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Settings
	for rows.Next() {
		var st Settings
		var secretKeys string
		if err := rows.Scan(&st.Org, &st.Product, &st.Config, &secretKeys, &st.CreatedAt, &st.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan settings: %w", err)
		}
		st.SecretKeys = decodeStrList(secretKeys)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	return out, nil
}

// Put upserts the non-secret config document + secret-key list for (org, product).
// It is idempotent on the composite key. The write NEVER carries secret values —
// those are already in KMS by the time this is called (see the handler).
func (s *SettingsStore) Put(ctx context.Context, st Settings) (Settings, error) {
	// One statement: a new pair is created at st.UpdatedAt, and an existing one keeps
	// the created_at it has (RETURNING says which). Two replicas writing one pair at
	// once land one after the other on the row's own lock.
	if err := s.db.QueryRowContext(ctx,
		`INSERT INTO settings (org, product, config, secret_keys, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$5)
		 ON CONFLICT(org, product) DO UPDATE SET
		   config=excluded.config, secret_keys=excluded.secret_keys, updated_at=excluded.updated_at
		 RETURNING created_at`,
		st.Org, st.Product, st.Config, encodeStrList(st.SecretKeys), st.UpdatedAt).Scan(&st.CreatedAt); err != nil {
		return Settings{}, fmt.Errorf("put settings: %w", err)
	}
	return st, nil
}
