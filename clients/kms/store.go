package kms

// store.go is the per-ORG SQLite persistence for sealed KMS secrets — the
// backend that REPLACES the single OS-locked ZapDB store, so cloud is no longer
// pinned to replicas=1.
//
// WHY. luxfi/kms's SecretStore is one embedded ZapDB (a Badger fork) opened under
// a SINGLE directory for ALL orgs. Badger takes an exclusive OS lock on its live
// files, so exactly one process may open it — the hard reason cloud ran
// replicas=1 with a cross-process writer lease. This store drops ZapDB entirely:
// each org's sealed secrets live in ITS OWN encrypted SQLite file
// ({DataDir}/orgs/{org}/kms.db via the canonical cloud.OrgDB → cek seam), which
// has NO exclusive-opener lock. Two tenants never share a file, so different pods
// can serve different tenants (consistent-hash org→pod); within a tenant, WAL +
// busy_timeout serialize writers and the horizontal-scale model pins each tenant
// to one writer pod. See the Red Handoff in the package report.
//
// THE FILE IS THE TENANT BOUNDARY. fileOrg() shards a store path to a file by its
// leading "/orgs/{org}" segment (the shape BOTH faces already use: the REST
// handler folds :org into /orgs/{org}, and the in-process facade's refs carry it
// too). A path with no such segment is genuinely deployment-wide and routes to
// the reserved {DataDir}/orgs/_platform/kms.db partition. cloud.OrgDB folds the
// org through the INJECTIVE SanitizeOrg slugger, so two distinct orgs can never
// collide on one file — physical isolation ON TOP OF the REST authz gate.
//
// CRYPTO STAYS IN THE CLIENT. This layer persists ALREADY-SEALED records: Seal /
// Open (the AES-256-GCM envelope, luxfi/kms/pkg/store) run in kms.go, so plaintext
// never reaches this file. The stored `path` column is the FULL store path
// (including /orgs/{org}); Seal's AAD binds that full path, so a record physically
// moved into another org's file still fails to Open — the cross-org record-swap
// defense is preserved, the store merely shards WHERE the row lives.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	kmsstore "github.com/luxfi/kms/pkg/store"
)

// reservedPlatformSlug mirrors cloud.PlatformDB's reserved partition: a path that
// names no tenant org routes here. It carries '_', which SanitizeOrg never emits,
// so it can never alias a real org's file.
const reservedPlatformSlug = "_platform"

// errReadOnly is returned by a reader-mode store when a mutation is attempted. A
// reader must never fork the authoritative writer's state.
var errReadOnly = errors.New("kms: store is read-only (reader HA role)")

// secretStore holds the lazily-opened, cached per-org SQLite handles. Each file
// is opened + migrated once on first touch and cached by org slug. Opens are
// serialized so a concurrent first touch opens exactly once — the same shape as
// clients/finance.
type secretStore struct {
	dataDir  string
	readOnly bool

	mu  sync.Mutex
	dbs map[string]*sql.DB // key: fileOrg(path) → open handle for that org's kms.db
}

func newSecretStore(dataDir string, readOnly bool) *secretStore {
	return &secretStore{dataDir: dataDir, readOnly: readOnly, dbs: map[string]*sql.DB{}}
}

// fileOrg extracts the org whose file holds a secret path. A path shaped
// "/orgs/{org}[/…]" belongs to tenant {org}; anything else is deployment-wide and
// lands in the reserved platform partition. The returned value is the RAW org (or
// the reserved sentinel); cloud.OrgDB folds a raw org through SanitizeOrg, so
// distinct raw orgs stay on distinct files.
func fileOrg(path string) string {
	p := strings.Trim(strings.TrimSpace(path), "/")
	if p == "" {
		return reservedPlatformSlug
	}
	segs := strings.SplitN(p, "/", 3)
	if segs[0] == "orgs" && len(segs) >= 2 && segs[1] != "" {
		return segs[1]
	}
	return reservedPlatformSlug
}

// dbFor resolves (opening + migrating + caching on first use) the SQLite handle
// for the org that owns path. When the file does not yet exist:
//   - create=true  (the Put path) → create it.
//   - create=false (read/list/delete) → return (nil, nil); the caller treats
//     absence as "no such secret" and NEVER litters an empty store shell for an
//     org that only had a read attempted.
// A reader (s.readOnly) never creates: create is forced false, and it performs no
// DDL (the writer already migrated). The org is folded through the injective
// slugger inside cloud.OrgDB, so a path can never traverse out of {DataDir}/orgs
// or reach another tenant.
func (s *secretStore) dbFor(path string, create bool) (*sql.DB, error) {
	if s.readOnly {
		create = false
	}
	org := fileOrg(path)

	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.dbs[org]; ok {
		return db, nil
	}
	if !create && !s.orgFileExists(org) {
		return nil, nil // nothing to open; caller returns not-found / empty
	}

	var (
		db  *sql.DB
		err error
	)
	if org == reservedPlatformSlug {
		db, err = cloud.PlatformDB(s.dataDir, "kms")
	} else {
		// OrgDB SanitizeOrg-slugs the org, creates {DataDir}/orgs/{slug} 0700, opens
		// via cek (encrypted at rest, per-db DEK) with the single-writer + WAL pragmas.
		db, err = cloud.OrgDB(s.dataDir, org, "", "kms")
	}
	if err != nil {
		return nil, fmt.Errorf("kms: open org store %q: %w", org, err)
	}
	if !s.readOnly {
		if err := migrateSecrets(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("kms: migrate org store %q: %w", org, err)
		}
	}
	s.dbs[org] = db
	return db, nil
}

// orgFileExists reports whether the on-disk kms.db for org already exists — the
// reader's "is this store hydrated?" check. Best-effort: an unreadable path is
// treated as absent (fail closed).
func (s *secretStore) orgFileExists(org string) bool {
	var path string
	if org == reservedPlatformSlug {
		path = filepath.Join(s.dataDir, "orgs", reservedPlatformSlug, "kms.db")
	} else {
		slug := cloud.SanitizeOrg(org)
		if slug == "" {
			return false
		}
		path = filepath.Join(s.dataDir, "orgs", slug, "kms.db")
	}
	_, err := os.Stat(path)
	return err == nil
}

// migrateSecrets creates the sealed-secret table. Idempotent (IF NOT EXISTS), so
// a reopen or a reader (opening an already-migrated file) is a no-op. The primary
// key (path, env, name) is the exact coordinate the ZapDB key encoded
// (kms/secrets/{path}/{env}/{name}), so listing semantics carry over unchanged.
func migrateSecrets(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS kms_secrets (
  path        TEXT NOT NULL,
  env         TEXT NOT NULL,
  name        TEXT NOT NULL,
  scheme      TEXT NOT NULL DEFAULT 'aead+mlkem',
  ciphertext  BLOB NOT NULL,
  wrapped_dek BLOB NOT NULL,
  key_handle  TEXT NOT NULL DEFAULT '',
  policy_id   TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (path, env, name)
);`
	if _, err := db.Exec(ddl); err != nil {
		return err
	}
	return nil
}

// put upserts a SEALED secret into its org's file. On conflict it rewrites the
// ciphertext/wrapped-DEK (a re-seal always mints a fresh per-secret DEK) and bumps
// updated_at while preserving created_at.
func (s *secretStore) put(sec *kmsstore.Secret) error {
	if s.readOnly {
		return errReadOnly
	}
	db, err := s.dbFor(sec.Path, true)
	if err != nil {
		return err
	}
	scheme := sec.Scheme
	if scheme == "" {
		scheme = kmsstore.ModeStandard
	}
	now := time.Now().Unix()
	_, err = db.Exec(
		`INSERT INTO kms_secrets (path,env,name,scheme,ciphertext,wrapped_dek,key_handle,policy_id,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(path,env,name) DO UPDATE SET
		   scheme=excluded.scheme, ciphertext=excluded.ciphertext, wrapped_dek=excluded.wrapped_dek,
		   key_handle=excluded.key_handle, policy_id=excluded.policy_id, updated_at=excluded.updated_at`,
		sec.Path, sec.Env, sec.Name, scheme, sec.Ciphertext, sec.WrappedDEK, sec.KeyHandle, sec.PolicyID, now, now)
	if err != nil {
		return fmt.Errorf("kms: write secret: %w", err)
	}
	return nil
}

// get reads a sealed secret by its full coordinate. Returns ErrSecretNotFound
// (verbatim, for a 404 mapping) when absent. The reconstructed Secret carries the
// FULL stored path, so the Client's Open reproduces Seal's AAD exactly.
func (s *secretStore) get(path, name, env string) (*kmsstore.Secret, error) {
	db, err := s.dbFor(path, false)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, kmsstore.ErrSecretNotFound // org has no store file yet
	}
	sec := &kmsstore.Secret{Path: path, Name: name, Env: env}
	err = db.QueryRow(
		`SELECT scheme, ciphertext, wrapped_dek, key_handle, policy_id FROM kms_secrets
		 WHERE path=? AND env=? AND name=?`, path, env, name).
		Scan(&sec.Scheme, &sec.Ciphertext, &sec.WrappedDEK, &sec.KeyHandle, &sec.PolicyID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, kmsstore.ErrSecretNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("kms: read secret: %w", err)
	}
	return sec, nil
}

// list returns the metadata (never ciphertext) of the secrets at EXACTLY (path,
// env) — the same exact-coordinate listing the ZapDB prefix scan did (it never
// recursed into sub-paths). Nothing sensitive is decrypted.
func (s *secretStore) list(path, env string) ([]*kmsstore.Secret, error) {
	db, err := s.dbFor(path, false)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return nil, nil // org has no store file yet → no secrets
	}
	rows, err := db.Query(
		`SELECT name, scheme, key_handle, policy_id FROM kms_secrets
		 WHERE path=? AND env=? ORDER BY name`, path, env)
	if err != nil {
		return nil, fmt.Errorf("kms: list secrets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*kmsstore.Secret
	for rows.Next() {
		sec := &kmsstore.Secret{Path: path, Env: env}
		if err := rows.Scan(&sec.Name, &sec.Scheme, &sec.KeyHandle, &sec.PolicyID); err != nil {
			return nil, fmt.Errorf("kms: scan secret: %w", err)
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// del removes a secret, returning ErrSecretNotFound (verbatim) when it was absent
// so the REST layer maps a missing delete to 404 rather than 200.
func (s *secretStore) del(path, name, env string) error {
	if s.readOnly {
		return errReadOnly
	}
	db, err := s.dbFor(path, false)
	if err != nil {
		return err
	}
	if db == nil {
		return kmsstore.ErrSecretNotFound // org has no store file yet
	}
	res, err := db.Exec(`DELETE FROM kms_secrets WHERE path=? AND env=? AND name=?`, path, env, name)
	if err != nil {
		return fmt.Errorf("kms: delete secret: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("kms: delete secret: %w", err)
	}
	if n == 0 {
		return kmsstore.ErrSecretNotFound
	}
	return nil
}

// hasRestoredStore reports whether any per-org kms.db already exists under
// {dataDir}/orgs — the reader's boot-time "is there anything to serve?" check, so
// a reader with an empty data dir fails closed at New rather than opening nothing
// and answering as if healthy.
func hasRestoredStore(dataDir string) bool {
	root := filepath.Join(dataDir, "orgs")
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "kms.db")); err == nil {
			return true
		}
	}
	return false
}

// close closes every cached per-org handle (best-effort), returning the first
// error. Safe to call once at shutdown.
func (s *secretStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for org, db := range s.dbs {
		if err := db.Close(); err != nil && first == nil {
			first = err
		}
		delete(s.dbs, org)
	}
	return first
}
