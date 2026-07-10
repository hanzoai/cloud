// Package cek is cloud's ONE encryption-at-rest gate for its per-subsystem
// SQLite stores. Every store opens its database through cek.Open — the single
// seam where a plaintext file is transparently migrated to SQLCipher and a keyed
// *sql.DB is returned — so "encrypted at rest" is a property of the open path,
// not something each of the ~30 stores must remember to do.
//
// THREAT MODEL. The DO block volume under /var/lib/cloud is provider-encrypted,
// so the residual exposure is a COPIED PV snapshot/backup or an in-cluster
// exec/PV read seeing plaintext customer PII (crm), the ledger (treasury), org
// wallet maps (wallets), the audit log, team/entitlements. cek removes that
// exposure: each file's pages are SQLCipher-encrypted under a per-database key
// that never leaves the process in the clear and is itself wrapped by the
// KMS-injected master key; the pre-migration plaintext copy is shredded once the
// encrypted store is proven readable. A lifted file is useless without the key.
//
// SCOPE / NON-GOALS. cek provides CONFIDENTIALITY at rest against the read-only
// exposure above (no master key ⇒ no plaintext). It is NOT integrity,
// authenticity, or anti-rollback against a PV-WRITE (node-compromise) adversary
// who can modify the volume: the per-file id lives in the (unauthenticated) .dek
// sidecar, so such an adversary could swap two of OUR OWN {db,.dek} pairs or
// replay an old snapshot. That is outside the stated model and is deliberately
// NOT defended here (a logical-id+epoch binding would add complexity for an
// out-of-model threat); revisit only if tenant-isolation-under-node-compromise
// is scoped in.
//
// ENVELOPE (the primitives live in github.com/hanzoai/sqlite/cek.go and are
// reused verbatim — one crypto implementation, KAT-gated there):
//
//   - Each database has its OWN random 256-bit DEK (the SQLCipher page key),
//     minted once at first touch and NEVER changed, so ciphertext pages are
//     never rewritten.
//   - Each database also gets a random 128-bit FILE ID, stored in the clear at
//     the head of its <db>.dek sidecar. The KEK is derived from that id, NOT the
//     file path: KEK = HKDF-SHA256(masterKey, lp("global") || lp(hex(fileID))).
//     The id is intrinsic to the file and travels with the sidecar, so moving
//     the data dir or changing CLOUD_DATA_DIR can never change the KEK and brick
//     a store. RFC-5869 HKDF via x/crypto/hkdf — NOT luxfi/crypto/kdf (a QZMQ
//     KeySchedule, not generic HKDF; using it would brick every store).
//   - The DEK is wrapped AES-256-GCM under the KEK, bound to the same id as AAD.
//     Sidecar = fileID(16) || wrapped-DEK. The raw DEK is never written.
//   - Master-key ROTATION rewraps only the sidecar: the DEK and fileID are
//     unchanged, so no page is rewritten and no file can be bricked.
//
// FAIL-SECURE. On an encryption-CAPABLE build (production is CGO + libsqlcipher)
// a missing master key is FATAL — the data plane refuses to open unencrypted,
// the same posture the KMS store takes. A key set on a NON-encrypting build is
// likewise fatal. An encrypted file whose sidecar is missing is refused. A
// migration whose encrypted copy does not reproduce the source's schema AND
// per-table content (a rowid-independent multiset hash, not just a row count)
// leaves the plaintext untouched and errors — the caller (MountAll) fails
// closed, so cloud never serves a half-migrated data plane.
package cek

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// masterKeyEnv is the ONE variable that supplies the 32-byte KMS master key
// (base64), matching the operator Deployment and clients/kms. No second gate.
const masterKeyEnv = "CLOUD_KMS_MASTER_KEY_REF"

const (
	dekSuffix      = ".dek"       // sidecar: fileID(16) || wrapped-DEK
	lockSuffix     = ".cek.lock"  // per-db flock guarding first-touch + migration
	tmpSuffix      = ".cek.tmp"   // in-progress encrypted target (same volume → atomic rename)
	plainBakSuffix = ".plain.bak" // transient pre-migration plaintext; SHREDDED after verified open

	sqliteMagic = "SQLite format 3\x00" // 16-byte header of an UNENCRYPTED db
	headerLen   = 16
	fileIDLen   = 16 // random per-file KEK-derivation id, stored in the sidecar head
)

// principalType domain-separates cloud's platform databases from IAM's org/user
// stores in the shared HKDF namespace.
const principalType = sqlitedrv.PrincipalGlobal

var (
	masterOnce     sync.Once
	masterKey      []byte
	masterErr      error
	masterOverride []byte
)

// SetMasterKey injects the 32-byte master key explicitly (cloud's boot resolves
// it once from cfg and hands it here), taking precedence over the environment.
// Call before the first Open. A wrong-length key is ignored so the env path can
// still apply.
func SetMasterKey(k []byte) {
	if len(k) == 32 {
		masterOverride = append([]byte(nil), k...)
	}
}

// resolveMaster resolves the process master key exactly once:
//
//	(key, nil)   — 32-byte key AND an encryption-capable build → encrypt.
//	(nil, nil)   — no key AND a non-encrypting (pure-Go) build → dev/CI plaintext.
//	(nil, error) — key malformed; OR key set on a non-encrypting build; OR NO key
//	               on an encryption-capable build. The last is the production
//	               fail-closed: a capable binary never silently ships plaintext.
func resolveMaster() ([]byte, error) {
	masterOnce.Do(func() {
		raw := masterOverride
		if len(raw) == 0 {
			b64 := strings.TrimSpace(os.Getenv(masterKeyEnv))
			if b64 == "" {
				if sqlitedrv.EncryptionAvailable() {
					masterErr = fmt.Errorf("cek: %s is required on an encryption-capable build; "+
						"refusing to open the data plane unencrypted (set the KMS master key, "+
						"or run a pure-Go dev build)", masterKeyEnv)
				}
				return // pure-Go dev/CI: plaintext is expected (no codec linked)
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
				"rebuild CGO_ENABLED=1 linked against libsqlcipher, or unset it for a dev build", masterKeyEnv)
			return
		}
		masterKey = raw
	})
	return masterKey, masterErr
}

// Encrypting reports whether cek will encrypt at rest (a valid master key is
// configured on an encryption-capable build). cloud calls this once at boot for
// the posture log; a false result on a capable build means resolveMaster errored
// and the first store Open will fail closed.
func Encrypting() bool {
	k, err := resolveMaster()
	return err == nil && len(k) == 32
}

// Open returns a *sql.DB for the SQLite database at path, encrypted at rest when
// a master key is configured. It is the single drop-in replacement for
// sql.Open("sqlite", path) across every cloud store.
func Open(path string) (*sql.DB, error) {
	master, err := resolveMaster()
	if err != nil {
		return nil, err
	}
	if master == nil {
		// Only reachable on a non-encrypting dev/CI build (a capable build with no
		// key already errored above). Preserve the prior bare-path behavior.
		return sql.Open("sqlite", path)
	}
	return openEncrypted(path, master)
}

func openEncrypted(path string, master []byte) (*sql.DB, error) {
	unlock, err := flock(path)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := recoverInterrupted(path); err != nil {
		return nil, err
	}

	var db *sql.DB
	switch classify(path) {
	case stateFresh:
		db, err = createFresh(path, master)
	case stateEncrypted:
		db, err = openExisting(path, master)
	default: // statePlaintext
		db, err = migrateThenOpen(path, master)
	}
	if err != nil {
		return nil, err
	}
	// No plaintext replica may survive a successful keyed open: shred any backup
	// left by this (or a crashed prior) migration. THIS is the security objective.
	shredPlainBak(path)
	return db, nil
}

type fileState int

const (
	stateFresh     fileState = iota // absent or too small to be a real db → mint
	statePlaintext                  // "SQLite format 3\0" header → migrate
	stateEncrypted                  // db-sized, non-magic header → SQLCipher, open via sidecar
)

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

// ── sidecar: fileID(16) || wrapped-DEK ───────────────────────────────────────

// mintSidecar generates a fresh fileID + DEK, wraps the DEK under the id-derived
// KEK, and returns the DEK and the sidecar bytes to persist.
func mintSidecar(master []byte) (dek, sidecar []byte, err error) {
	fileID := make([]byte, fileIDLen)
	if _, err = rand.Read(fileID); err != nil {
		return nil, nil, fmt.Errorf("cek: generate file id: %w", err)
	}
	kek, aad, err := deriveFor(master, fileID)
	if err != nil {
		return nil, nil, err
	}
	defer zero(kek)
	if dek, err = sqlitedrv.NewDEK(); err != nil {
		return nil, nil, fmt.Errorf("cek: new DEK: %w", err)
	}
	wrapped, err := sqlitedrv.WrapDEK(kek, dek, aad)
	if err != nil {
		zero(dek)
		return nil, nil, fmt.Errorf("cek: wrap DEK: %w", err)
	}
	sidecar = append(append(make([]byte, 0, fileIDLen+len(wrapped)), fileID...), wrapped...)
	return dek, sidecar, nil
}

// unwrapSidecar reads the fileID from the sidecar head and unwraps the DEK under
// the id-derived KEK. A wrong master key, tampered blob, or truncated sidecar
// fails the GCM tag and errors — never a partial/garbage key.
func unwrapSidecar(master, sidecar []byte) ([]byte, error) {
	if len(sidecar) <= fileIDLen {
		return nil, fmt.Errorf("cek: sidecar too short (%d bytes)", len(sidecar))
	}
	fileID, wrapped := sidecar[:fileIDLen], sidecar[fileIDLen:]
	kek, aad, err := deriveFor(master, fileID)
	if err != nil {
		return nil, err
	}
	defer zero(kek)
	dek, err := sqlitedrv.UnwrapDEK(kek, wrapped, aad)
	if err != nil {
		return nil, fmt.Errorf("cek: unwrap DEK (wrong master key or corrupt sidecar): %w", err)
	}
	return dek, nil
}

// deriveFor derives the KEK and the wrap-AAD for a file id. Both bind to
// hex(fileID) so the id — not any path or config value — is the sole identity.
func deriveFor(master, fileID []byte) (kek, aad []byte, err error) {
	id := hex.EncodeToString(fileID)
	kek, err = sqlitedrv.DeriveKey(master, principalType, id)
	if err != nil {
		return nil, nil, fmt.Errorf("cek: derive KEK: %w", err)
	}
	return kek, sqlitedrv.PrincipalAAD(principalType, id), nil
}

// ── open paths ───────────────────────────────────────────────────────────────

func createFresh(path string, master []byte) (*sql.DB, error) {
	dekPath := path + dekSuffix
	if fileExists(dekPath) {
		return openExisting(path, master) // a concurrent first-touch won the lock
	}
	dek, sidecar, err := mintSidecar(master)
	if err != nil {
		return nil, err
	}
	defer zero(dek)
	if err := writeFileAtomic(dekPath, sidecar, 0o600); err != nil {
		return nil, err
	}
	db, err := openKeyed(path, dek)
	if err != nil {
		return nil, fmt.Errorf("cek: create encrypted %q: %w", path, err)
	}
	return db, nil
}

func openExisting(path string, master []byte) (*sql.DB, error) {
	dekPath := path + dekSuffix
	sidecar, err := os.ReadFile(dekPath)
	if err != nil {
		return nil, fmt.Errorf("cek: read sidecar %q (encrypted db, refusing to open blind): %w", dekPath, err)
	}
	dek, err := unwrapSidecar(master, sidecar)
	if err != nil {
		return nil, err
	}
	defer zero(dek)
	db, err := openKeyed(path, dek)
	if err != nil {
		return nil, fmt.Errorf("cek: open encrypted %q: %w", path, err)
	}
	return db, nil
}

// migrateThenOpen converts an existing PLAINTEXT database to SQLCipher without
// losing a row, then opens it. Crash-safe (plaintext is the source of truth
// until an atomic rename commits) and fail-secure (the swap happens only after
// the encrypted copy reproduces the source schema + per-table content hash +
// integrity_check, re-opened via the exact keyed path the app uses).
func migrateThenOpen(path string, master []byte) (*sql.DB, error) {
	dekPath := path + dekSuffix
	// Plaintext header ⇒ an earlier attempt did not commit: discard any stale
	// sidecar/tmp and redo from the plaintext source of truth.
	_ = os.Remove(dekPath)
	tmp := path + tmpSuffix
	removeDBFiles(tmp)

	dek, sidecar, err := mintSidecar(master)
	if err != nil {
		return nil, err
	}
	defer zero(dek)

	srcInv, err := exportPlaintext(path, tmp, dek)
	if err != nil {
		removeDBFiles(tmp)
		return nil, err
	}

	if isPlaintextHeader(tmp) {
		removeDBFiles(tmp)
		return nil, fmt.Errorf("cek: migration of %q produced a plaintext file (SQLCipher not linked?)", path)
	}
	if err := verifyParity(tmp, dek, srcInv); err != nil {
		removeDBFiles(tmp)
		return nil, fmt.Errorf("cek: migration parity check failed for %q (plaintext left intact): %w", path, err)
	}

	// Commit. Order for crash-safety:
	//  1. sidecar (the encrypted db's key),
	//  2. plaintext → <db>.plain.bak,
	//  3. delete the now-orphaned plaintext -wal/-shm (WAL was checkpoint-folded
	//     into the main file, so .plain.bak is complete; removing them now closes
	//     the window where a stale plaintext WAL sits beside the encrypted db),
	//  4. atomic rename tmp → path.
	// A crash between any two steps is resolved by recoverInterrupted on reboot.
	if err := writeFileAtomic(dekPath, sidecar, 0o600); err != nil {
		removeDBFiles(tmp)
		return nil, err
	}
	if err := os.Rename(path, path+plainBakSuffix); err != nil {
		removeDBFiles(tmp)
		_ = os.Remove(dekPath)
		return nil, fmt.Errorf("cek: preserve plaintext backup for %q: %w", path, err)
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(path+plainBakSuffix, path) // roll back
		_ = os.Remove(dekPath)
		return nil, fmt.Errorf("cek: swap encrypted %q into place: %w", path, err)
	}
	syncDir(filepath.Dir(path))

	db, err := openKeyed(path, dek)
	if err != nil {
		return nil, fmt.Errorf("cek: open migrated %q: %w", path, err)
	}
	return db, nil // openEncrypted's tail shreds .plain.bak after this succeeds
}

// exportPlaintext opens the plaintext source, folds its WAL into the main file
// (crm.db/audit.db carry MB of live WAL), records the source inventory for the
// parity check, and copies every page into a fresh SQLCipher database at tmp via
// SQLCipher's own sqlcipher_export with the format compat pinned. The source is
// only read.
func exportPlaintext(path, tmp string, dek []byte) (inventory, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
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

	// The DEK is crypto/rand hex — no injection surface; the path is a bound
	// parameter. The reopen-via-openKeyed parity check downstream is the
	// authoritative guard that the copy opens under the app's exact keyed path.
	// The target is created at the runtime's SQLCipher format (the frozen
	// libsqlcipher default), which is exactly what openKeyed reopens it under.
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
// (openKeyed) and asserts: integrity_check ok, identical schema fingerprint, and
// — per table — identical row count AND identical rowid-independent content hash.
// The content hash (a commutative multiset sum of per-row hashes over the USER
// columns) is what backs the zero-loss claim: it catches any value change, NULL
// coercion, or row add/drop that count + schema + integrity_check miss, while
// correctly ignoring the benign implicit-rowid renumbering sqlcipher_export does.
// It runs BEFORE the atomic swap, so any discrepancy leaves the plaintext intact.
func verifyParity(tmp string, dek []byte, src inventory) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	db, err := openKeyed(tmp, dek)
	if err != nil {
		return fmt.Errorf("reopen encrypted copy: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping encrypted copy (key/compat mismatch?): %w", err)
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
		return fmt.Errorf("schema fingerprint mismatch")
	}
	if len(dst.tables) != len(src.tables) {
		return fmt.Errorf("table set mismatch: src %d tables, dst %d", len(src.tables), len(dst.tables))
	}
	for tbl, s := range src.tables {
		d, ok := dst.tables[tbl]
		if !ok {
			return fmt.Errorf("table %q missing in encrypted copy", tbl)
		}
		if d.count != s.count {
			return fmt.Errorf("row-count mismatch in %q: src %d, dst %d", tbl, s.count, d.count)
		}
		if d.content != s.content {
			return fmt.Errorf("content-hash mismatch in %q (row values differ despite equal count)", tbl)
		}
	}
	return nil
}

// inventory is the parity fingerprint: a schema hash plus, per table, a row count
// and a content hash.
type inventory struct {
	schema [32]byte
	tables map[string]tableStat
}

type tableStat struct {
	count   int64
	content [32]byte // commutative multiset sum of per-row hashes over user columns
}

func readInventory(ctx context.Context, conn *sql.Conn) (inventory, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT type,name,COALESCE(tbl_name,''),COALESCE(sql,'') FROM sqlite_master `+
			`WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return inventory{}, err
	}
	var schemaLines, tables []string
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
		writeLP(h, []byte(l))
	}
	inv := inventory{tables: make(map[string]tableStat, len(tables))}
	copy(inv.schema[:], h.Sum(nil))

	for _, tbl := range tables {
		st, err := tableContent(ctx, conn, tbl)
		if err != nil {
			return inventory{}, fmt.Errorf("content %q: %w", tbl, err)
		}
		inv.tables[tbl] = st
	}
	return inv, nil
}

// tableContent streams every row of a table and folds a per-row hash (over the
// user columns, faithfully distinguishing NULL / "" / 0 / storage class) into a
// commutative 256-bit sum. Order-independent (survives rowid-driven scan-order
// differences) and duplicate-safe (addition, not XOR). O(1) memory per table.
func tableContent(ctx context.Context, conn *sql.Conn, tbl string) (tableStat, error) {
	q := `SELECT * FROM "` + strings.ReplaceAll(tbl, `"`, `""`) + `"`
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return tableStat{}, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return tableStat{}, err
	}
	var st tableStat
	scan := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range scan {
		ptrs[i] = &scan[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return tableStat{}, err
		}
		rh := sha256.New()
		for _, v := range scan {
			encodeValue(rh, v)
		}
		var row [32]byte
		copy(row[:], rh.Sum(nil))
		addInto(&st.content, row)
		st.count++
	}
	return st, rows.Err()
}

// encodeValue writes a type-tagged, length-prefixed, NULL-sentinel encoding of a
// scanned SQLite value so two logically-equal rows hash identically and a NULL
// never collides with "" or 0.
func encodeValue(h hash.Hash, v any) {
	switch x := v.(type) {
	case nil:
		h.Write([]byte{0})
	case int64:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(x))
		h.Write([]byte{1})
		h.Write(b[:])
	case float64:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(x))
		h.Write([]byte{2})
		h.Write(b[:])
	case bool:
		h.Write([]byte{3})
		if x {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	case []byte:
		h.Write([]byte{4})
		writeLP(h, x)
	case string:
		h.Write([]byte{5})
		writeLP(h, []byte(x))
	case time.Time:
		h.Write([]byte{6})
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(x.UnixNano()))
		h.Write(b[:])
	default:
		h.Write([]byte{9})
		writeLP(h, []byte(fmt.Sprintf("%v", x)))
	}
}

// writeLP writes an 8-byte big-endian length prefix then the bytes, so
// concatenations are unambiguous.
func writeLP(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// addInto computes acc = (acc + row) mod 2^256, big-endian — the commutative,
// duplicate-safe multiset combiner.
func addInto(acc *[32]byte, row [32]byte) {
	var carry uint16
	for i := 31; i >= 0; i-- {
		s := uint16(acc[i]) + uint16(row[i]) + carry
		acc[i] = byte(s)
		carry = s >> 8
	}
}

// recoverInterrupted resumes a migration that crashed between the commit steps,
// keying only on files (no config). It also scrubs any stale plaintext -wal/-shm
// so a half-committed state can never leave one beside an encrypted db.
func recoverInterrupted(path string) error {
	dekPath := path + dekSuffix
	tmp := path + tmpSuffix
	if fileExists(path) {
		// If the live file is already encrypted, a leftover tmp is a dead
		// migration attempt — discard it. (A plaintext live file is handled by
		// migrateThenOpen, which clears tmp itself.)
		if !isPlaintextHeader(path) {
			removeDBFiles(tmp)
		}
		return nil
	}
	switch {
	case fileExists(tmp) && fileExists(dekPath):
		// Crashed after the sidecar+backup were written but before/during the swap
		// rename: finish it. The sidecar matches tmp.
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("cek: resume migration swap for %q: %w", path, err)
		}
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
		syncDir(filepath.Dir(path))
	case fileExists(path + plainBakSuffix):
		// Swap never happened; restore the plaintext original for a clean redo.
		if err := os.Rename(path+plainBakSuffix, path); err != nil {
			return fmt.Errorf("cek: restore plaintext backup for %q: %w", path, err)
		}
		removeDBFiles(tmp)
		_ = os.Remove(dekPath)
	}
	return nil
}

// openKeyed opens a keyed SQLCipher database via the driver's ONE blessed keyed
// open (the key rides the URI so it is applied at sqlite3_open_v2, before mattn's
// pragma battery touches the header — a post-open PRAGMA key would be too late).
//
// CROSS-VERSION SAFETY. SQLCipher's on-disk format is fixed by the libsqlcipher
// major the arcd image links; that version is FROZEN in the Dockerfile (see the
// runbook) so an image rebuild cannot silently change the default cipher format
// and orphan a store. An at-open `cipher_compatibility` pin is NOT usable here
// (the URI param is silently ignored, and a post-open pragma runs after mattn has
// already read the header) — the version freeze is the control. If a mismatch
// ever occurs it FAILS CLOSED: openExisting returns "file is not a database" and
// MountAll aborts — never a silent downgrade or corruption.
func openKeyed(path string, dek []byte) (*sql.DB, error) {
	db, err := sqlitedrv.OpenDB(path, dek)
	if err != nil {
		return nil, fmt.Errorf("cek: open keyed %q: %w", path, err) // no dsn (holds key)
	}
	return db, nil
}

// ── shred + boring helpers ───────────────────────────────────────────────────

// shredPlainBak overwrites and removes the transient pre-migration plaintext
// backup once the encrypted store is proven readable. Overwrite-then-remove is
// defense-in-depth (best-effort on a copy-on-write/journalled fs; the block
// volume is provider-encrypted regardless). Idempotent no-op when absent.
func shredPlainBak(path string) {
	bak := path + plainBakSuffix
	fi, err := os.Stat(bak)
	if err != nil {
		return
	}
	if f, err := os.OpenFile(bak, os.O_WRONLY, 0); err == nil {
		overwrite(f, fi.Size())
		_ = f.Sync()
		_ = f.Close()
	}
	_ = os.Remove(bak)
}

func overwrite(f *os.File, size int64) {
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	for remaining := size; remaining > 0; {
		n := int64(chunk)
		if remaining < n {
			n = remaining
		}
		if _, err := rand.Read(buf[:n]); err != nil {
			return
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return
		}
		remaining -= n
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func removeDBFiles(base string) {
	for _, s := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(base + s)
	}
}

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

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// flock takes an exclusive advisory lock on <path>.cek.lock, serializing
// first-touch and migration for the SAME database across processes/goroutines
// (different databases never contend). Returns an unlock func.
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

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
