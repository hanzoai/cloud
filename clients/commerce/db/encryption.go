// At-rest encryption for the per-tenant SQLite stores.
//
// Commerce keeps each tenant's data (balances, transactions, usage — MONEY) in
// its own SQLite file (users/<id>/data.db, orgs/<id>/data.db). This file wires
// those files to the Hanzo encrypted SQLite driver (github.com/hanzoai/sqlite):
// SQLCipher page-level AES-256 at rest, keyed per-tenant, with the master key
// sourced from KMS.
//
// ENVELOPE MODEL (identical to Hanzo IAM's object/orgdb.go, so there is exactly
// ONE at-rest encryption scheme across the platform):
//
//  1. A master key (32 bytes) is supplied via COMMERCE_KMS_MASTER_KEY, sourced
//     from KMS at deploy time — never hardcoded, never in git.
//  2. Each tenant file gets its OWN random Data Encryption Key (DEK). SQLCipher
//     encrypts the pages with the DEK; it never changes for the life of the file.
//  3. The DEK is wrapped (AES-256-GCM) under a per-tenant KEK =
//     HKDF(masterKey, principal) and stored in a sidecar (<data.db>.dek). The raw
//     DEK is never written to disk.
//  4. Rotating the master key only rewraps the sidecar — the DEK, and therefore
//     every encrypted page, is untouched (O(1), cannot brick a file).
//
// Posture is decided once, in resolveMasterKey():
//   - unset            → unencrypted per-tenant files (dev / CGO-off CI).
//   - set + cgo build  → per-tenant SQLCipher encryption (production).
//   - set + !cgo build → hard error. A master key was supplied but this binary
//     cannot encrypt, and silently writing plaintext money data would violate the
//     security contract.
package db

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sqlitedrv "github.com/hanzoai/sqlite"
)

const (
	// masterKeyEnv is the SINGLE canonical environment variable that supplies the
	// per-tenant encryption master key (32 bytes, 64 hex chars). It is materialised
	// into the pod from KMS (orgs/hanzo/secrets/commerce/COMMERCE_KMS_MASTER_KEY)
	// by the kms-operator — see universe/infra/k8s/commerce/master-key-kmssecret.yaml.
	masterKeyEnv = "COMMERCE_KMS_MASTER_KEY"

	// dekSuffix names the wrapped-DEK sidecar written beside each tenant data.db.
	dekSuffix = ".dek"

	// createLockSuffix names the cross-process mint-DEK+create-db lock file.
	createLockSuffix = ".create.lock"
)

// resolveMasterKey reads COMMERCE_KMS_MASTER_KEY and validates it. It returns:
//
//   - (nil, nil)   when unset → unencrypted dev/CI mode.
//   - (key, nil)   when set, 64 hex chars, AND this build ACTUALLY encrypts
//     (cgo + libsqlcipher linked and proven at runtime).
//   - (nil, error) when set but malformed, OR set on a build that cannot really
//     encrypt — we refuse to run rather than silently write plaintext money data.
//
// The codec check is the crux: EncryptionAvailable() is only a backend-capability
// flag (true for ANY cgo build), but a cgo build that forgot to link libsqlcipher
// silently no-ops the key and writes PLAINTEXT. CodecLinked() runs a one-time
// runtime probe (open a keyed temp db, assert the bytes are real ciphertext), so a
// mis-linked image FAILS CLOSED at boot instead of persisting plaintext balances.
//
// This is the ONE place the master key is sourced, so every tenant store shares an
// identical posture decision.
func resolveMasterKey() ([]byte, error) {
	mkHex := os.Getenv(masterKeyEnv)
	if mkHex == "" {
		return nil, nil
	}
	if !sqlitedrv.EncryptionAvailable() {
		return nil, fmt.Errorf("%s is set but this build cannot encrypt (pure-Go sqlite); rebuild with CGO_ENABLED=1 -tags \"libsqlite3 sqlite_fts5\" linked against libsqlcipher, or unset the variable for an unencrypted dev build", masterKeyEnv)
	}
	if !sqlitedrv.CodecLinked() {
		return nil, fmt.Errorf("%s is set and this is a cgo build, but libsqlcipher is NOT linked (CodecLinked()=false) — the key would be silently ignored and money data written as PLAINTEXT. Rebuild the image with -tags \"libsqlite3 sqlite_fts5\" + CGO_CFLAGS=-DSQLITE_HAS_CODEC -DSQLITE_USE_URI=1 + CGO_LDFLAGS=-lsqlcipher", masterKeyEnv)
	}
	mk, err := hex.DecodeString(strings.TrimSpace(mkHex))
	if err != nil {
		return nil, fmt.Errorf("%s must be hex-encoded: %w", masterKeyEnv, err)
	}
	if len(mk) != 32 {
		return nil, fmt.Errorf("%s must decode to 32 bytes, got %d", masterKeyEnv, len(mk))
	}
	return mk, nil
}

// ResolveMasterKey exposes resolveMasterKey to commands (cmd/commerce-encrypt-dbs)
// so the master key is sourced from COMMERCE_KMS_MASTER_KEY in exactly ONE place.
func ResolveMasterKey() ([]byte, error) { return resolveMasterKey() }

// principalFor maps a tenant type ("user"/"org") to the driver's KEK-derivation
// principal. Unknown types default to org (the shared-tenant store).
func principalFor(tenantType string) sqlitedrv.PrincipalType {
	if tenantType == "user" {
		return sqlitedrv.PrincipalUser
	}
	return sqlitedrv.PrincipalOrg
}

// resolveDEK returns the per-tenant DEK for dbPath, creating and persisting a
// wrapped-DEK sidecar on first use. It mirrors IAM object.openEncrypted but
// returns the DEK (rather than a *sql.DB) because commerce opens two separate
// connections — a concurrent read pool and a serialized writer — that must key
// the same file with the same DEK.
//
// Fail-closed: an existing db file WITHOUT a sidecar is refused — it is either a
// legacy plaintext file (migrate it with cmd/commerce-encrypt-dbs first) or
// corruption, and silently treating it as encrypted — or falling back to
// plaintext — would be wrong.
//
// CONCURRENCY: minting a fresh DEK and writing its sidecar is a read-decide-write.
// Two processes sharing the data dir (an accidental >1 replica, or the migration
// tool beside the daemon) could both observe "no sidecar", both mint a DIFFERENT
// DEK, and brick the file (the surviving .db/.dek pair would mismatch). The create
// path therefore runs under an exclusive cross-process lock and RE-CHECKS the
// sidecar inside the lock: the loser finds the winner's sidecar and uses it. The
// common path (sidecar present) takes no lock.
func resolveDEK(dbPath string, masterKey []byte, pt sqlitedrv.PrincipalType, id string) ([]byte, error) {
	// Fail closed on a build that cannot encrypt. resolveMasterKey already refuses
	// the env-sourced key on the pure-Go backend, but NewSQLiteDB also accepts a
	// MasterKey injected directly on its config (the encrypt-at-rest migration tool
	// and the encryption-proof test). Without this guard those paths reach
	// sqlitedrv.DSN(path, dek), which PANICS on the pure-Go backend — turning a
	// clear "cannot encrypt" refusal into a crash. Return the error the caller
	// expects instead, so a keyed open on !cgo never panics and never risks
	// silently persisting plaintext money data.
	if !sqlitedrv.EncryptionAvailable() {
		return nil, fmt.Errorf("cannot open %q encrypted: this build cannot encrypt (pure-Go sqlite); rebuild with CGO_ENABLED=1 -tags \"libsqlite3 sqlite_fts5\" linked against libsqlcipher, or open without a master key for an unencrypted dev build", dbPath)
	}
	kek, err := sqlitedrv.DeriveKey(masterKey, pt, id)
	if err != nil {
		return nil, fmt.Errorf("derive KEK for %s:%s: %w", pt, id, err)
	}
	defer zeroBytes(kek)

	dekPath := dbPath + dekSuffix
	aad := sqlitedrv.PrincipalAAD(pt, id)

	// Fast path: sidecar present → unwrap, no lock needed.
	if fileExists(dekPath) {
		return unwrapSidecar(dbPath, dekPath, kek, aad)
	}
	if fileExists(dbPath) {
		return nil, fmt.Errorf("encrypted db %q has no DEK sidecar %q; refusing to open (migrate the plaintext file with commerce-encrypt-dbs first)", dbPath, dekPath)
	}

	// Create path: serialize the mint+persist critical section across processes,
	// then RE-CHECK under the lock so a racing first-touch cannot produce a
	// mismatched .db/.dek pair.
	lockPath := dbPath + createLockSuffix
	var dek []byte
	lockErr := withExclusiveFileLock(lockPath, func() error {
		if fileExists(dekPath) {
			d, err := unwrapSidecar(dbPath, dekPath, kek, aad)
			if err != nil {
				return err
			}
			dek = d
			return nil
		}
		if fileExists(dbPath) {
			return fmt.Errorf("encrypted db %q has no DEK sidecar %q; refusing to open (migrate the plaintext file with commerce-encrypt-dbs first)", dbPath, dekPath)
		}

		// Genuinely fresh: mint a DEK, wrap it, persist the sidecar atomically.
		// The .db itself is created — keyed with this DEK — by the first Open in
		// NewSQLiteDB; because the DEK is now fixed by the sidecar, any concurrent
		// opener uses the SAME key, so the pair can never mismatch.
		fresh, err := sqlitedrv.NewDEK()
		if err != nil {
			return err
		}
		blob, err := sqlitedrv.WrapDEK(kek, fresh, aad)
		if err != nil {
			zeroBytes(fresh)
			return err
		}
		if err := writeFileAtomic(dekPath, blob, 0o600); err != nil {
			zeroBytes(fresh)
			return fmt.Errorf("write wrapped DEK %q: %w", dekPath, err)
		}
		dek = fresh
		return nil
	})
	if lockErr != nil {
		return nil, lockErr
	}
	return dek, nil
}

// unwrapSidecar reads the sidecar and unwraps the DEK under kek+aad. A wrong
// master key, a sidecar lifted from another principal, or a tampered blob fails
// the GCM tag and returns an error — never a partial/garbage key (fail-closed).
func unwrapSidecar(dbPath, dekPath string, kek, aad []byte) ([]byte, error) {
	blob, err := os.ReadFile(dekPath)
	if err != nil {
		return nil, fmt.Errorf("read wrapped DEK %q: %w", dekPath, err)
	}
	dek, err := sqlitedrv.UnwrapDEK(kek, blob, aad)
	if err != nil {
		return nil, fmt.Errorf("unwrap DEK for %q (wrong master key or corrupt sidecar): %w", dbPath, err)
	}
	return dek, nil
}

// encDriverDSN returns the (driverName, dsn) to open dbPath. Both branches use
// the SINGLE canonical "sqlite" driver registered by github.com/hanzoai/sqlite —
// mattn/SQLCipher under CGO, pure-Go modernc under CGO_ENABLED=0. Commerce imports
// no mattn/modernc driver directly; hanzoai/sqlite is the one sqlite backend.
//
//   - dek != nil → encrypted at rest (SQLCipher). The key rides the canonical
//     driver DSN as SQLCipher's URI `key` param (applied inside sqlite3_open_v2,
//     before any pragma battery, so create AND reopen succeed); commerce's extra
//     tuning pragmas are appended. The DSN CONTAINS the key — callers MUST NOT log
//     it. Reached only under CGO (resolveMasterKey fails closed on the pure-Go
//     build), so sqlitedrv.DSN's key path never panics.
//   - dek == nil → plaintext (dev/CI + the CGO-off production boot on modernc).
//     sqlitedrv.PragmaDSN renders plaintextPragmas in the ACTIVE backend's DSN
//     syntax (mattn `_busy_timeout=N`, modernc `_pragma=busy_timeout(N)`), so WAL +
//     busy_timeout actually apply under BOTH builds. It deliberately does NOT set
//     cache=shared: commerce opens a separate concurrent read pool and a serialized
//     write pool against the same file, and shared-cache table locking turns a
//     retryable SQLITE_BUSY into an un-retryable SQLITE_LOCKED ("database table is
//     locked") the instant the read pool holds a cursor open — exactly what the
//     cache=shared that sqlitedrv.DSN(dbPath, nil) appends did, panicking the test
//     suite's BeforeSuite fixture load.
func encDriverDSN(dbPath string, dek []byte, cfg SQLiteConfig) (string, string) {
	if dek != nil {
		dsn := sqlitedrv.DSN(dbPath, dek) // file:PATH?_busy_timeout=..&_journal_mode=WAL&..&key=x'..'
		if extra := extraEncPragmas(cfg); extra != "" {
			dsn += "&" + extra
		}
		return "sqlite", dsn
	}
	return "sqlite", sqlitedrv.PragmaDSN(dbPath, plaintextPragmas(cfg))
}

// plaintextPragmas maps commerce's SQLiteConfig to the connection pragmas applied
// to an UNENCRYPTED per-tenant store, rendered per-backend by sqlitedrv.PragmaDSN.
// It reproduces the historical buildPragmas floor — a 10s busy_timeout + WAL even
// for a zero-value config, so a second concurrent writer waits instead of failing
// with "database is locked" — and deliberately omits cache=shared (the dual
// read/write pool design requires private per-connection caches; see encDriverDSN).
func plaintextPragmas(cfg SQLiteConfig) []sqlitedrv.Pragma {
	busyTimeout := cfg.BusyTimeout
	if busyTimeout <= 0 {
		busyTimeout = 10000
	}
	journalMode := cfg.JournalMode
	if journalMode == "" {
		journalMode = "WAL"
	}
	// Order matters: busy_timeout leads so a connection blocks on a busy database
	// before journal_mode=WAL is set (WAL cannot be enabled while another
	// connection holds the database).
	pragmas := []sqlitedrv.Pragma{
		{Name: "busy_timeout", Value: strconv.Itoa(busyTimeout)},
		{Name: "journal_mode", Value: journalMode},
	}
	if cfg.Synchronous != "" {
		pragmas = append(pragmas, sqlitedrv.Pragma{Name: "synchronous", Value: cfg.Synchronous})
	}
	if cfg.CacheSize != 0 {
		pragmas = append(pragmas, sqlitedrv.Pragma{Name: "cache_size", Value: strconv.Itoa(cfg.CacheSize)})
	}
	pragmas = append(pragmas,
		sqlitedrv.Pragma{Name: "foreign_keys", Value: "ON"},
		sqlitedrv.Pragma{Name: "temp_store", Value: "MEMORY"},
	)
	return pragmas
}

// extraEncPragmas emits the tuning pragmas the driver's canonical DSN does NOT
// already set (it fixes busy_timeout=10000, WAL, synchronous=NORMAL,
// foreign_keys=ON — identical to commerce's DefaultConfig). Only cache_size and
// temp_store are commerce-specific, so only those are appended.
func extraEncPragmas(cfg SQLiteConfig) string {
	var p []string
	if cfg.CacheSize != 0 {
		p = append(p, fmt.Sprintf("_cache_size=%d", cfg.CacheSize))
	}
	p = append(p, "_temp_store=MEMORY")
	return strings.Join(p, "&")
}

// --- small helpers (no third-party deps) ---

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeFileAtomic writes data to a temp file in the same directory and renames it
// into place, so a crash never leaves a half-written sidecar.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dek-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// zeroBytes overwrites a key buffer in place.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// compile-time assertion that *sql.DB is what the driver hands back (documents
// the integration surface; keeps the import honest if the file is trimmed).
var _ = (*sql.DB)(nil)
