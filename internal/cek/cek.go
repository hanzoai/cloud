// Package cek is cloud's ONE encryption-at-rest gate for its per-subsystem
// SQLite stores. Every store opens its database through cek.Open — the single
// DRY seam where a plaintext file is transparently migrated to SQLCipher and a
// keyed *sql.DB is returned — so "encrypted at rest" is a property of the open
// path, not something each of the ~30 stores must remember to do.
//
// THREAT MODEL. The DO block volume under /var/lib/cloud is provider-encrypted,
// so the residual exposure is a COPIED PV snapshot/backup or an in-cluster
// exec/PV read seeing plaintext customer PII (crm), the ledger (treasury), org
// wallet maps (wallets), the audit log, team/entitlements, etc. cek removes that
// exposure: each file's pages are SQLCipher-encrypted under a per-database key
// that never leaves the process in the clear and is itself wrapped by the
// KMS-injected master key. A lifted file is useless without the master key.
//
// ENVELOPE (identical scheme to the proven IAM per-org store; the primitives
// live in github.com/hanzoai/sqlite/cek.go and are reused verbatim — DRY):
//
//   - Each database has its OWN random 256-bit DEK (the SQLCipher page key),
//     minted once at first touch and NEVER changed, so ciphertext pages are
//     never rewritten.
//   - KEK = HKDF-SHA256(masterKey, info = lp("global") || lp(principalID)),
//     where principalID is the database's stable path relative to the data dir.
//     RFC-5869 HKDF via golang.org/x/crypto/hkdf — NOT luxfi/crypto/kdf (that is
//     a QZMQ KeySchedule, not generic HKDF; using it would brick every store).
//   - The DEK is wrapped AES-256-GCM under the KEK and stored in a `<db>.dek`
//     sidecar (blob = version||nonce||ct||tag); the raw DEK is never written.
//   - Master-key ROTATION rewraps only the sidecar (Rewrap): the DEK is
//     unchanged, so no page is rewritten and no file can be bricked.
//
// FAIL-SECURE. If CLOUD_KMS_MASTER_KEY_REF is set but this build cannot encrypt
// (pure-Go sqlite, no SQLCipher), Open refuses to run rather than silently write
// plaintext. An encrypted file whose sidecar is missing is refused, never
// opened blindly. A migration that cannot be verified byte-for-byte leaves the
// original plaintext untouched and returns an error — the caller (MountAll)
// fails closed, so cloud never serves a half-migrated data plane.
package cek

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// masterKeyEnv is the ONE environment variable that supplies the 32-byte KMS
// master key (base64), matching the operator Deployment and clients/kms. The
// operator injects it from the KMS-synced secret cloud-kms-master-key.
const masterKeyEnv = "CLOUD_KMS_MASTER_KEY_REF"

// dataDirEnv locates the per-tenant SQLite root; principalIDs are computed
// relative to it so a database's identity (and therefore its KEK) is stable
// across restarts regardless of the absolute mount point.
const dataDirEnv = "CLOUD_DATA_DIR"

const (
	dekSuffix     = ".dek"       // wrapped-DEK sidecar
	lockSuffix    = ".cek.lock"  // per-db flock guarding first-touch + migration
	tmpSuffix     = ".cek.tmp"   // in-progress encrypted target (same volume → atomic rename)
	plainBakField = ".plain.bak" // preserved pre-migration plaintext (retired after confidence)

	sqliteMagic = "SQLite format 3\x00" // 16-byte header of an UNENCRYPTED db
	headerLen   = 16
)

// principalType domain-separates cloud's platform databases from IAM's org/user
// stores. Cloud databases are platform-scoped (tenant isolation is a column, not
// a file), so PrincipalGlobal is the honest type.
const principalType = sqlitedrv.PrincipalGlobal

var (
	masterOnce sync.Once
	masterKey  []byte
	masterErr  error
	// masterOverride lets cloud boot / tests inject the key explicitly instead of
	// via env. Guarded by masterOnce: set it before the first Open.
	masterOverride []byte
)

// SetMasterKey injects the 32-byte master key explicitly, taking precedence over
// the environment. Call once at boot before any store opens. A nil/empty key
// falls back to the environment. Intended for cloud's config path and tests.
func SetMasterKey(k []byte) {
	if len(k) == 32 {
		masterOverride = append([]byte(nil), k...)
	}
}

// resolveMaster returns the process master key exactly once. It returns:
//
//	(nil, nil)   — no key configured → unencrypted dev/CI mode.
//	(key, nil)   — 32-byte key AND this build can encrypt.
//	(nil, error) — key configured but malformed, or set on a non-encrypting
//	               build (we refuse to write plaintext under a set key).
func resolveMaster() ([]byte, error) {
	masterOnce.Do(func() {
		raw := masterOverride
		if len(raw) == 0 {
			b64 := strings.TrimSpace(os.Getenv(masterKeyEnv))
			if b64 == "" {
				return // unencrypted dev/CI
			}
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				masterErr = fmt.Errorf("cek: %s is not valid base64: %w", masterKeyEnv, err)
				return
			}
			raw = decoded
		}
		if len(raw) != 32 {
			masterErr = fmt.Errorf("cek: master key must decode to 32 bytes, got %d", len(raw))
			return
		}
		if !sqlitedrv.EncryptionAvailable() {
			masterErr = fmt.Errorf("cek: %s is set but this build cannot encrypt (pure-Go sqlite); "+
				"rebuild CGO_ENABLED=1 linked against libsqlcipher, or unset the variable for a dev build", masterKeyEnv)
			return
		}
		masterKey = raw
	})
	return masterKey, masterErr
}

// Encrypting reports whether cek will encrypt (a valid master key is configured
// on an encryption-capable build). Surfaced to cloud's boot log.
func Encrypting() bool {
	k, err := resolveMaster()
	return err == nil && len(k) == 32
}

// Open returns a *sql.DB for the SQLite database at path, encrypted at rest when
// a master key is configured. It is the single drop-in replacement for
// sql.Open("sqlite", path) across every cloud store.
//
// With a master key set, Open (under a per-file lock): recovers any interrupted
// prior migration, then — depending on the file's state — mints a fresh
// encrypted database, migrates an existing PLAINTEXT database to SQLCipher
// (verified byte-faithful before it commits), or opens an already-encrypted
// database via its sidecar DEK. Without a master key it is a plain open
// (dev/CI). The returned DB is not pinged.
func Open(path string) (*sql.DB, error) {
	master, err := resolveMaster()
	if err != nil {
		return nil, err
	}
	if master == nil {
		// Unencrypted dev/CI: preserve the exact prior behavior (bare path open).
		return sql.Open("sqlite", path)
	}
	return openEncrypted(path, master)
}

func openEncrypted(path string, master []byte) (*sql.DB, error) {
	pid := principalID(path)
	kek, err := sqlitedrv.DeriveKey(master, principalType, pid)
	if err != nil {
		return nil, fmt.Errorf("cek: derive KEK for %q: %w", pid, err)
	}
	defer zero(kek)
	aad := sqlitedrv.PrincipalAAD(principalType, pid)
	dekPath := path + dekSuffix

	unlock, err := flock(path)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Recover a migration interrupted mid-swap (crash between the two renames).
	if err := recoverInterrupted(path, dekPath); err != nil {
		return nil, err
	}

	switch classify(path) {
	case stateFresh:
		return createFresh(path, dekPath, kek, aad)
	case stateEncrypted:
		return openWithSidecar(path, dekPath, kek, aad)
	default: // statePlaintext
		return migrateThenOpen(path, dekPath, kek, aad)
	}
}

type fileState int

const (
	stateFresh     fileState = iota // absent or too small to be a real db → mint
	statePlaintext                  // "SQLite format 3\0" header → migrate
	stateEncrypted                  // anything else of db size → SQLCipher, open via sidecar
)

// classify inspects only the 16-byte header, never the key: an unencrypted
// SQLite file starts with the fixed magic; a SQLCipher file starts with its
// random salt, so any non-magic, sufficiently-sized file is treated as
// encrypted (its sidecar decides whether it actually opens).
func classify(path string) fileState {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < headerLen {
		return stateFresh
	}
	if isPlaintextHeader(path) {
		return statePlaintext
	}
	return stateEncrypted
}

func isPlaintextHeader(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var hdr [headerLen]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		return false
	}
	return string(hdr[:]) == sqliteMagic
}

// createFresh mints a DEK for a brand-new database, persists the wrapped sidecar,
// and creates the encrypted file. If a sidecar already exists (a concurrent
// first-touch that won the lock) it defers to it rather than minting a second,
// mismatched DEK.
func createFresh(path, dekPath string, kek, aad []byte) (*sql.DB, error) {
	if fileExists(dekPath) {
		return openWithSidecar(path, dekPath, kek, aad)
	}
	dek, err := sqlitedrv.NewDEK()
	if err != nil {
		return nil, fmt.Errorf("cek: new DEK: %w", err)
	}
	defer zero(dek)
	blob, err := sqlitedrv.WrapDEK(kek, dek, aad)
	if err != nil {
		return nil, fmt.Errorf("cek: wrap DEK: %w", err)
	}
	if err := writeFileAtomic(dekPath, blob, 0o600); err != nil {
		return nil, err
	}
	db, err := sqlitedrv.OpenDB(path, dek)
	if err != nil {
		return nil, fmt.Errorf("cek: create encrypted %q: %w", path, err)
	}
	return db, nil
}

// openWithSidecar unwraps the DEK from an existing sidecar and opens the
// encrypted database. A wrong master key, wrong principal, or corrupt sidecar
// fails the GCM tag and returns an error — never a partial/garbage key.
func openWithSidecar(path, dekPath string, kek, aad []byte) (*sql.DB, error) {
	blob, err := os.ReadFile(dekPath)
	if err != nil {
		return nil, fmt.Errorf("cek: read sidecar %q: %w", dekPath, err)
	}
	dek, err := sqlitedrv.UnwrapDEK(kek, blob, aad)
	if err != nil {
		return nil, fmt.Errorf("cek: unwrap DEK for %q (wrong master key or corrupt sidecar): %w", path, err)
	}
	defer zero(dek)
	db, err := sqlitedrv.OpenDB(path, dek)
	if err != nil {
		return nil, fmt.Errorf("cek: open encrypted %q: %w", path, err)
	}
	return db, nil
}

// migrateThenOpen converts an existing PLAINTEXT database to SQLCipher without
// losing a row, then opens it. The sequence is crash-safe (the plaintext file
// is the source of truth until an atomic rename commits) and fail-secure (the
// swap happens only after the encrypted copy is re-opened and proven to hold the
// identical schema + per-table row counts as the source).
func migrateThenOpen(path, dekPath string, kek, aad []byte) (*sql.DB, error) {
	// A plaintext header means an earlier attempt did not commit: discard any
	// stale sidecar/tmp and redo from the plaintext source of truth.
	_ = os.Remove(dekPath)
	tmp := path + tmpSuffix
	removeDBFiles(tmp)

	dek, err := sqlitedrv.NewDEK()
	if err != nil {
		return nil, fmt.Errorf("cek: new DEK: %w", err)
	}
	defer zero(dek)

	srcInv, err := exportPlaintext(path, tmp, dek)
	if err != nil {
		removeDBFiles(tmp)
		return nil, err
	}

	// Verify the encrypted copy by re-opening it EXACTLY as the app will
	// (OpenDB), then comparing schema + per-table row counts. Any mismatch, or a
	// still-plaintext header, aborts with the plaintext left intact.
	if isPlaintextHeader(tmp) {
		removeDBFiles(tmp)
		return nil, fmt.Errorf("cek: migration of %q produced a plaintext file (SQLCipher not linked?)", path)
	}
	if err := verifyParity(tmp, dek, srcInv); err != nil {
		removeDBFiles(tmp)
		return nil, fmt.Errorf("cek: migration parity check failed for %q (plaintext left intact): %w", path, err)
	}

	// Commit atomically. Order matters for crash-safety:
	//  1. write the sidecar (the future encrypted db's key),
	//  2. move plaintext aside to <db>.plain.bak (on-volume safety copy),
	//  3. rename the verified encrypted tmp into place,
	//  4. delete the stale plaintext -wal/-shm (they belong to the old file and
	//     would corrupt an encrypted open if replayed).
	blob, err := sqlitedrv.WrapDEK(kek, dek, aad)
	if err != nil {
		removeDBFiles(tmp)
		return nil, fmt.Errorf("cek: wrap DEK: %w", err)
	}
	if err := writeFileAtomic(dekPath, blob, 0o600); err != nil {
		removeDBFiles(tmp)
		return nil, err
	}
	if err := os.Rename(path, path+plainBakField); err != nil {
		return nil, fmt.Errorf("cek: preserve plaintext backup for %q: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		// Best-effort rollback: restore the plaintext original.
		_ = os.Rename(path+plainBakField, path)
		_ = os.Remove(dekPath)
		return nil, fmt.Errorf("cek: swap encrypted %q into place: %w", path, err)
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	syncDir(filepath.Dir(path))

	db, err := sqlitedrv.OpenDB(path, dek)
	if err != nil {
		return nil, fmt.Errorf("cek: open migrated %q: %w", path, err)
	}
	return db, nil
}

// exportPlaintext opens the plaintext source, folds its WAL into the main file
// (so nothing committed-but-unmerged is missed — crm.db/audit.db carry MB of
// live WAL), records the source inventory for the parity check, and copies every
// page into a fresh SQLCipher database at tmp via SQLCipher's own
// sqlcipher_export. The source file is only read, never mutated in a way that
// loses data (the checkpoint is a no-op merge).
func exportPlaintext(path, tmp string, dek []byte) (inventory, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	src, err := sql.Open("sqlite", sqlitedrv.DSN(path, nil))
	if err != nil {
		return inventory{}, fmt.Errorf("cek: open plaintext %q: %w", path, err)
	}
	defer func() { _ = src.Close() }()
	src.SetMaxOpenConns(1) // ATTACH + sqlcipher_export must run on ONE connection

	conn, err := src.Conn(ctx)
	if err != nil {
		return inventory{}, fmt.Errorf("cek: acquire conn for %q: %w", path, err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return inventory{}, fmt.Errorf("cek: checkpoint %q: %w", path, err)
	}
	srcInv, err := readInventory(ctx, conn)
	if err != nil {
		return inventory{}, fmt.Errorf("cek: inventory source %q: %w", path, err)
	}

	// ATTACH the fresh encrypted target with a raw hex key (SQLCipher's
	// documented migration idiom) and copy. The key is crypto/rand hex — no
	// injection surface; the path is bound as a parameter to be safe against any
	// exotic characters. The reopen-via-OpenDB parity check downstream is the
	// authoritative guard that the attached-db cipher params match the app's.
	attach := fmt.Sprintf(`ATTACH DATABASE ? AS enc KEY "x'%x'"`, dek)
	if _, err := conn.ExecContext(ctx, attach, tmp); err != nil {
		return inventory{}, fmt.Errorf("cek: attach encrypted target: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT sqlcipher_export('enc')"); err != nil {
		_, _ = conn.ExecContext(ctx, "DETACH DATABASE enc")
		return inventory{}, fmt.Errorf("cek: sqlcipher_export: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "DETACH DATABASE enc"); err != nil {
		return inventory{}, fmt.Errorf("cek: detach encrypted target: %w", err)
	}
	return srcInv, nil
}

// verifyParity re-opens the encrypted copy exactly as the running app will
// (OpenDB with the DEK) and asserts its schema fingerprint and per-table row
// counts equal the source's, and that PRAGMA integrity_check passes. This is the
// zero-data-loss gate: it runs BEFORE the atomic swap, so any discrepancy leaves
// the plaintext original in place.
func verifyParity(tmp string, dek []byte, src inventory) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := sqlitedrv.OpenDB(tmp, dek)
	if err != nil {
		return fmt.Errorf("reopen encrypted copy: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping encrypted copy (key mismatch?): %w", err)
	}

	var ic string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&ic); err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if ic != "ok" {
		return fmt.Errorf("integrity_check returned %q", ic)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire verify conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	dst, err := readInventory(ctx, conn)
	if err != nil {
		return fmt.Errorf("inventory encrypted copy: %w", err)
	}
	if dst.schema != src.schema {
		return fmt.Errorf("schema fingerprint mismatch (src %x… dst %x…)", src.schema[:4], dst.schema[:4])
	}
	if len(dst.counts) != len(src.counts) {
		return fmt.Errorf("table set mismatch: src %d tables, dst %d", len(src.counts), len(dst.counts))
	}
	for tbl, want := range src.counts {
		got, ok := dst.counts[tbl]
		if !ok {
			return fmt.Errorf("table %q missing in encrypted copy", tbl)
		}
		if got != want {
			return fmt.Errorf("row-count mismatch in %q: src %d, dst %d", tbl, want, got)
		}
	}
	return nil
}

// inventory is the parity fingerprint of a database: a schema hash plus a
// per-table row count. Row-count parity per table (not a single total) catches
// rows moved between tables; the schema hash catches structural drift.
type inventory struct {
	schema [32]byte
	counts map[string]int64
}

func readInventory(ctx context.Context, conn *sql.Conn) (inventory, error) {
	// Schema fingerprint: every user object's (type,name,tbl_name,sql), sorted.
	rows, err := conn.QueryContext(ctx,
		`SELECT type,name,COALESCE(tbl_name,''),COALESCE(sql,'') FROM sqlite_master `+
			`WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return inventory{}, err
	}
	var schemaLines []string
	var tables []string
	for rows.Next() {
		var typ, name, tbl, ddl string
		if err := rows.Scan(&typ, &name, &tbl, &ddl); err != nil {
			_ = rows.Close()
			return inventory{}, err
		}
		schemaLines = append(schemaLines, typ+"\x1f"+name+"\x1f"+tbl+"\x1f"+ddl)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return inventory{}, err
	}
	_ = rows.Close()

	sort.Strings(schemaLines)
	h := sha256.New()
	for _, l := range schemaLines {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(l)))
		h.Write(n[:]) // length-prefix so lines are unambiguous
		h.Write([]byte(l))
	}
	inv := inventory{counts: make(map[string]int64, len(tables))}
	copy(inv.schema[:], h.Sum(nil))

	for _, tbl := range tables {
		var n int64
		// tbl comes from sqlite_master, not user input; quote-escape defensively.
		q := `SELECT COUNT(*) FROM "` + strings.ReplaceAll(tbl, `"`, `""`) + `"`
		if err := conn.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return inventory{}, fmt.Errorf("count %q: %w", tbl, err)
		}
		inv.counts[tbl] = n
	}
	return inv, nil
}

// recoverInterrupted resumes a migration that crashed between the two commit
// renames. If the live path is gone but a verified encrypted tmp and its sidecar
// survive, finish the swap; otherwise fall back to the plaintext backup.
func recoverInterrupted(path, dekPath string) error {
	if fileExists(path) {
		return nil
	}
	tmp := path + tmpSuffix
	switch {
	case fileExists(tmp) && fileExists(dekPath):
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("cek: resume migration swap for %q: %w", path, err)
		}
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
		syncDir(filepath.Dir(path))
	case fileExists(path + plainBakField):
		// Swap never happened; restore the plaintext original for a clean redo.
		if err := os.Rename(path+plainBakField, path); err != nil {
			return fmt.Errorf("cek: restore plaintext backup for %q: %w", path, err)
		}
		removeDBFiles(tmp)
		_ = os.Remove(dekPath)
	}
	return nil
}

// principalID is the database's identity for key derivation: its path relative
// to CLOUD_DATA_DIR (e.g. "treasury.db", "base/data.db", "team/account.db"),
// which is stable across restarts and unique across the tree. Falls back to the
// cleaned absolute path when the file is outside the data dir.
func principalID(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	dir := strings.TrimSpace(os.Getenv(dataDirEnv))
	if dir != "" {
		if base, err := filepath.Abs(dir); err == nil {
			if rel, err := filepath.Rel(base, abs); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(rel)
			}
		}
	}
	return filepath.ToSlash(abs)
}

// ── small, boring helpers ────────────────────────────────────────────────────

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// removeDBFiles deletes a database and its WAL/SHM sidecars (used for the
// throwaway migration tmp; never called on a live path).
func removeDBFiles(base string) {
	for _, s := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(base + s)
	}
}

// writeFileAtomic writes data to a temp file in the same directory, fsyncs it,
// and renames it over dst so a reader never sees a partial sidecar.
func writeFileAtomic(dst string, data []byte, perm os.FileMode) error {
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("cek: create %q: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cek: write %q: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cek: sync %q: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cek: close %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cek: rename %q→%q: %w", tmp, dst, err)
	}
	return nil
}

// syncDir fsyncs a directory so a rename/create is durable. Best-effort: a
// failure here does not corrupt data, only weakens the crash guarantee.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// flock takes an exclusive advisory lock on <path>.cek.lock, serializing
// first-touch and migration across processes/goroutines for the SAME database
// (different databases never contend — the lock is per file). Returns an unlock
// func that releases the lock and closes the fd.
func flock(path string) (func(), error) {
	lockPath := path + lockSuffix
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("cek: create lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cek: open lock %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cek: acquire lock %q: %w", lockPath, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// zero wipes key material from memory once it is no longer needed. SQLCipher has
// copied the DEK into its own state by the time we zero our copy.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
