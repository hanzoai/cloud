package cek

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// resetMaster clears the once-resolved master so a test can choose a posture.
// White-box tests only; production never resets.
func resetMaster(key []byte) {
	masterOnce = sync.Once{}
	masterKey = nil
	masterErr = nil
	masterOverride = nil
	if key != nil {
		SetMasterKey(key)
	}
}

func testMaster(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*7 + 1)
	}
	return k
}

// requireCipher skips when the build cannot produce REAL ciphertext (a mis-linked
// cgo build silently writes plaintext), probing rather than trusting the flag.
// With SQLITE_REQUIRE_CODEC=1 (the Docker image build sets it) a would-be skip
// becomes a FAILURE, so the in-image gate is airtight standalone.
func requireCipher(t *testing.T) {
	t.Helper()
	skipOrFail := func(msg string) {
		if os.Getenv("SQLITE_REQUIRE_CODEC") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	if !sqlitedrv.EncryptionAvailable() {
		skipOrFail("sqlite build cannot encrypt (pure-Go); run with CGO + libsqlcipher")
		return
	}
	probe := filepath.Join(t.TempDir(), "probe.db")
	dek, _ := sqlitedrv.NewDEK()
	db, err := openKeyed(probe, dek)
	if err != nil {
		t.Fatalf("probe open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatalf("probe ddl: %v", err)
	}
	_ = db.Close()
	if isPlaintextHeader(probe) {
		skipOrFail("cgo build is NOT linked against libsqlcipher (keyed db is plaintext)")
		return
	}
}

// makePlaintextDB writes a genuine UNENCRYPTED SQLite db with known rows in WAL
// mode (exercising exportPlaintext's WAL fold), returning per-table row counts.
func makePlaintextDB(t *testing.T, path string, companies, contacts int) map[string]int64 {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open plaintext: %v", err)
	}
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"} {
		if _, err := db.Exec(p); err != nil {
			t.Fatalf("pragma %q: %v", p, err)
		}
	}
	if _, err := db.Exec(`
CREATE TABLE crm_companies(id TEXT PRIMARY KEY, org TEXT, name TEXT, arr INTEGER);
CREATE INDEX ix_co_org ON crm_companies(org);
CREATE TABLE crm_contacts(id TEXT PRIMARY KEY, org TEXT, email TEXT, note TEXT);`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	for i := 0; i < companies; i++ {
		if _, err := db.Exec(`INSERT INTO crm_companies VALUES(?,?,?,?)`,
			fmt.Sprintf("co-%d", i), "maxpower", fmt.Sprintf("Co %d", i), int64(i*1000)); err != nil {
			t.Fatalf("insert company: %v", err)
		}
	}
	for i := 0; i < contacts; i++ {
		// note is NULL on evens to exercise NULL fidelity in the content hash.
		if i%2 == 0 {
			_, err = db.Exec(`INSERT INTO crm_contacts(id,org,email) VALUES(?,?,?)`,
				fmt.Sprintf("ct-%d", i), "maxpower", fmt.Sprintf("u%d@x.io", i))
		} else {
			_, err = db.Exec(`INSERT INTO crm_contacts VALUES(?,?,?,?)`,
				fmt.Sprintf("ct-%d", i), "maxpower", fmt.Sprintf("u%d@x.io", i), "vip")
		}
		if err != nil {
			t.Fatalf("insert contact: %v", err)
		}
	}
	if !isPlaintextHeader(path) {
		t.Fatalf("precondition: freshly created db is not plaintext")
	}
	_ = db.Close()
	return map[string]int64{"crm_companies": int64(companies), "crm_contacts": int64(contacts)}
}

func rowCount(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func firstBytes(t *testing.T, path string, n int) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, n)
	if _, err := f.ReadAt(b, 0); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// TestMigratePlaintextToCipher is the keystone: plaintext → SQLCipher with a
// ciphertext header, exact row parity, an unwrappable sidecar, readable data,
// and — per RED HIGH — NO surviving plaintext backup (shredded after verify).
func TestMigratePlaintextToCipher(t *testing.T) {
	requireCipher(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "crm.db")
	want := makePlaintextDB(t, path, 37, 91)

	resetMaster(testMaster(t))
	db, err := Open(path)
	if err != nil {
		t.Fatalf("cek.Open (migrate): %v", err)
	}
	defer func() { _ = db.Close() }()

	if isPlaintextHeader(path) {
		t.Fatalf("FAIL: %s still plaintext after migration", path)
	}
	t.Logf("migrated header (hex): %s", hex.EncodeToString(firstBytes(t, path, 16)))

	for tbl, n := range want {
		if got := rowCount(t, db, tbl); got != n {
			t.Errorf("row-count mismatch %s: want %d got %d", tbl, n, got)
		}
	}

	// sidecar unwraps under the id-derived KEK (path plays no role).
	sidecar, err := os.ReadFile(path + dekSuffix)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if dek, err := unwrapSidecar(testMaster(t), sidecar); err != nil || len(dek) != 32 {
		t.Fatalf("unwrap sidecar: dek=%d err=%v", len(dek), err)
	}

	// RED HIGH: the plaintext backup must be GONE (shredded), not lingering.
	if fileExists(path + plainBakSuffix) {
		t.Errorf("SECURITY: plaintext backup %s survived migration (must be shredded)", path+plainBakSuffix)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM crm_companies WHERE id='co-5'`).Scan(&name); err != nil || name != "Co 5" {
		t.Errorf("content check: got %q err %v", name, err)
	}
}

// TestOpenIdempotent: a second Open of an already-migrated db is a no-op.
func TestOpenIdempotent(t *testing.T) {
	requireCipher(t)
	path := filepath.Join(t.TempDir(), "treasury.db")
	want := makePlaintextDB(t, path, 5, 5)

	resetMaster(testMaster(t))
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = db1.Close()
	hdr1 := firstBytes(t, path, 16)

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = db2.Close() }()
	if isPlaintextHeader(path) {
		t.Fatalf("db became plaintext on reopen")
	}
	for tbl, n := range want {
		if got := rowCount(t, db2, tbl); got != n {
			t.Errorf("reopen row-count %s: want %d got %d", tbl, n, got)
		}
	}
	if string(hdr1) != string(firstBytes(t, path, 16)) {
		t.Errorf("header changed on reopen (pages rewritten?)")
	}
}

// TestFreshCreateEncrypted: a brand-new db is born encrypted.
func TestFreshCreateEncrypted(t *testing.T) {
	requireCipher(t)
	path := filepath.Join(t.TempDir(), "wallets.db")
	resetMaster(testMaster(t))
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE w(addr TEXT, org TEXT)`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	_ = db.Close()
	if isPlaintextHeader(path) {
		t.Fatalf("fresh db is plaintext")
	}
	if !fileExists(path + dekSuffix) {
		t.Fatalf("fresh db has no sidecar")
	}
}

// TestWrongMasterFailsClosed: a sidecar minted under one master cannot open under
// another — a lifted file + wrong key yields an error, never plaintext.
func TestWrongMasterFailsClosed(t *testing.T) {
	requireCipher(t)
	path := filepath.Join(t.TempDir(), "audit.db")
	makePlaintextDB(t, path, 3, 3)

	resetMaster(testMaster(t))
	db, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = db.Close()

	other := make([]byte, 32)
	for i := range other {
		other[i] = 0xAB
	}
	resetMaster(other)
	if _, err := Open(path); err == nil {
		t.Fatalf("SECURITY: opened encrypted db under the WRONG master key")
	}
}

// TestDataDirMoveNoBrick (RED MED #2, PoC): the KEK binds to the sidecar fileID,
// NOT CLOUD_DATA_DIR, so changing/unsetting the data dir — or moving the file to
// a different path — must NOT brick the store.
func TestDataDirMoveNoBrick(t *testing.T) {
	requireCipher(t)
	dir1 := t.TempDir()
	pathA := filepath.Join(dir1, "crm.db")
	want := makePlaintextDB(t, pathA, 8, 8)

	t.Setenv("CLOUD_DATA_DIR", dir1)
	resetMaster(testMaster(t))
	db, err := Open(pathA)
	if err != nil {
		t.Fatalf("migrate under dir1: %v", err)
	}
	_ = db.Close()

	// Simulate a data-dir change AND a physical move to a different filename.
	dir2 := t.TempDir()
	pathB := filepath.Join(dir2, "moved.db")
	copyFile(t, pathA, pathB)
	copyFile(t, pathA+dekSuffix, pathB+dekSuffix)
	t.Setenv("CLOUD_DATA_DIR", "/some/other/root") // the old brick trigger

	db2, err := Open(pathB) // KEK from fileID → must still open
	if err != nil {
		t.Fatalf("AVAILABILITY: data-dir change bricked the store: %v", err)
	}
	defer func() { _ = db2.Close() }()
	for tbl, n := range want {
		if got := rowCount(t, db2, tbl); got != n {
			t.Errorf("moved db row-count %s: want %d got %d", tbl, n, got)
		}
	}
}

// TestMissingKeyFatalOnCapableBuild (RED MED #3): an encryption-capable build
// with NO master key must FAIL CLOSED, never silently open plaintext. On a
// pure-Go build the same absence is dev-mode plaintext.
func TestMissingKeyFatalOnCapableBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	resetMaster(nil)
	os.Unsetenv(masterKeyEnv)

	_, err := Open(path)
	if sqlitedrv.EncryptionAvailable() {
		if err == nil {
			t.Fatalf("SECURITY: capable build opened the data plane with NO master key (silent plaintext)")
		}
		if fileExists(path) {
			t.Fatalf("SECURITY: a db file was created despite the fatal missing-key error")
		}
	} else {
		if err != nil {
			t.Fatalf("pure-Go dev build should allow plaintext when no key: %v", err)
		}
	}
}

// TestContentHashCatchesMutation (RED MED #4): the content hash catches a value
// change that row-count + schema + integrity_check all miss, AND does NOT
// false-positive on the benign implicit-rowid renumbering sqlcipher_export does.
func TestContentHashCatchesMutation(t *testing.T) {
	requireCipher(t)
	a := filepath.Join(t.TempDir(), "a.db")
	b := filepath.Join(t.TempDir(), "b.db")
	makePlaintextDB(t, a, 10, 10)
	makePlaintextDB(t, b, 10, 10)
	if inventoryOf(t, a).tables["crm_contacts"].content != inventoryOf(t, b).tables["crm_contacts"].content {
		t.Fatalf("identical data produced different content hashes")
	}

	// Mutate ONE value in b (same row count, same schema, integrity stays ok).
	base := inventoryOf(t, a).tables["crm_contacts"]
	mutate(t, b, `UPDATE crm_contacts SET email='HIJACKED' WHERE id='ct-3'`)
	after := inventoryOf(t, b).tables["crm_contacts"]
	if after.count != base.count {
		t.Fatalf("mutation changed the row count; test would not isolate content")
	}
	if base.content == after.content {
		t.Fatalf("SECURITY: content hash did NOT catch a value mutation (count-blind gate)")
	}

	// Benign rowid renumber (delete+reinsert identical row, VACUUM) must NOT change it.
	c := filepath.Join(t.TempDir(), "c.db")
	makePlaintextDB(t, c, 10, 10)
	before := inventoryOf(t, c).tables["crm_contacts"].content
	mutate(t, c, `DELETE FROM crm_contacts WHERE id='ct-0'; INSERT INTO crm_contacts(id,org,email) VALUES('ct-0','maxpower','u0@x.io'); VACUUM`)
	if before != inventoryOf(t, c).tables["crm_contacts"].content {
		t.Errorf("content hash false-positived on a rowid renumber (re-inserted identical row)")
	}
}

// TestCorruptSidecarFailsClosed (RED re-test #5, safety net): any undecryptable
// state — a corrupted or missing .dek sidecar, the family a libsqlcipher format
// mismatch would land in — FAILS CLOSED (errors), never silently opening
// plaintext or a garbage db. (Cross-version format stability itself is enforced
// by freezing the libsqlcipher version in the build; an at-open cipher_compat
// pin is infeasible with mattn's URI-key requirement — see openKeyed.)
func TestCorruptSidecarFailsClosed(t *testing.T) {
	requireCipher(t)
	path := filepath.Join(t.TempDir(), "audit.db")
	makePlaintextDB(t, path, 4, 4)

	resetMaster(testMaster(t))
	db, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = db.Close()

	// Flip a byte in the wrapped-DEK region of the sidecar → GCM tag fails.
	sc, err := os.ReadFile(path + dekSuffix)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	sc[len(sc)-1] ^= 0xFF
	if err := os.WriteFile(path+dekSuffix, sc, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	resetMaster(testMaster(t))
	if _, err := Open(path); err == nil {
		t.Fatalf("SECURITY: opened an encrypted db with a corrupted DEK sidecar")
	}

	// A missing sidecar over an encrypted db must also refuse (never open blind).
	if err := os.Remove(path + dekSuffix); err != nil {
		t.Fatalf("rm sidecar: %v", err)
	}
	resetMaster(testMaster(t))
	if _, err := Open(path); err == nil {
		t.Fatalf("SECURITY: opened an encrypted db with NO DEK sidecar")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func inventoryOf(t *testing.T, path string) inventory {
	t.Helper()
	db, err := sql.Open("sqlite", sqlitedrv.DSN(path, nil))
	if err != nil {
		t.Fatalf("open for inventory: %v", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	inv, err := readInventory(context.Background(), conn)
	if err != nil {
		t.Fatalf("readInventory: %v", err)
	}
	return inv
}

func mutate(t *testing.T, path, sqlText string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqlitedrv.DSN(path, nil))
	if err != nil {
		t.Fatalf("open mutate: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(sqlText); err != nil {
		t.Fatalf("mutate %q: %v", sqlText, err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}
