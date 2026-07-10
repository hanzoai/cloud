package cek

import (
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

// requireCipher skips when the build cannot produce REAL ciphertext. We do not
// trust EncryptionAvailable() alone (it is a capability flag; a mis-linked cgo
// build silently writes plaintext), so we probe: create a keyed db and require a
// non-magic header. This is the same posture the migration enforces at runtime.
func requireCipher(t *testing.T) {
	t.Helper()
	if !sqlitedrv.EncryptionAvailable() {
		t.Skip("sqlite build cannot encrypt (pure-Go); run with CGO + libsqlcipher")
	}
	probe := filepath.Join(t.TempDir(), "probe.db")
	dek, _ := sqlitedrv.NewDEK()
	db, err := sqlitedrv.OpenDB(probe, dek)
	if err != nil {
		t.Fatalf("probe open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatalf("probe ddl: %v", err)
	}
	_ = db.Close()
	if isPlaintextHeader(probe) {
		t.Skip("cgo build is NOT linked against libsqlcipher (keyed db is plaintext); " +
			"set CGO_CFLAGS/LDFLAGS for sqlcipher to run encryption tests")
	}
}

// makePlaintextDB writes a genuine UNENCRYPTED SQLite db with known rows and
// returns the per-table row counts. It is what production has on disk today.
func makePlaintextDB(t *testing.T, path string, companies, contacts int) map[string]int64 {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open plaintext: %v", err)
	}
	// WAL mode + no clean checkpoint, to exercise exportPlaintext's WAL fold
	// (mirrors crm.db/audit.db carrying MB of live WAL in prod).
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"} {
		if _, err := db.Exec(p); err != nil {
			t.Fatalf("pragma %q: %v", p, err)
		}
	}
	if _, err := db.Exec(`
CREATE TABLE crm_companies(id TEXT PRIMARY KEY, org TEXT, name TEXT, arr INTEGER);
CREATE INDEX ix_co_org ON crm_companies(org);
CREATE TABLE crm_contacts(id TEXT PRIMARY KEY, org TEXT, email TEXT);`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	for i := 0; i < companies; i++ {
		if _, err := db.Exec(`INSERT INTO crm_companies VALUES(?,?,?,?)`,
			fmt.Sprintf("co-%d", i), "maxpower", fmt.Sprintf("Co %d", i), int64(i*1000)); err != nil {
			t.Fatalf("insert company: %v", err)
		}
	}
	for i := 0; i < contacts; i++ {
		if _, err := db.Exec(`INSERT INTO crm_contacts VALUES(?,?,?)`,
			fmt.Sprintf("ct-%d", i), "maxpower", fmt.Sprintf("u%d@x.io", i)); err != nil {
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

// TestMigratePlaintextToCipher is the keystone: a plaintext financial-shaped db
// is migrated in place to SQLCipher with (1) a ciphertext header, (2) exact
// per-table row parity, (3) an unwrappable .dek sidecar, (4) a preserved
// plaintext backup, and (5) fully readable data through the returned handle.
func TestMigratePlaintextToCipher(t *testing.T) {
	requireCipher(t)
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	path := filepath.Join(dir, "crm.db")
	want := makePlaintextDB(t, path, 37, 91)

	resetMaster(testMaster(t))
	db, err := Open(path)
	if err != nil {
		t.Fatalf("cek.Open (migrate): %v", err)
	}
	defer func() { _ = db.Close() }()

	// (1) ciphertext header — the whole point.
	if isPlaintextHeader(path) {
		t.Fatalf("FAIL: %s is still plaintext after migration", path)
	}
	hdr := firstBytes(t, path, 16)
	t.Logf("migrated header (hex): %s (NOT 'SQLite format 3')", hex.EncodeToString(hdr))

	// (2) per-table row parity.
	for tbl, n := range want {
		if got := rowCount(t, db, tbl); got != n {
			t.Errorf("row-count mismatch %s: want %d got %d", tbl, n, got)
		}
	}

	// (3) .dek sidecar exists and unwraps under the principal-bound KEK.
	dekPath := path + dekSuffix
	blob, err := os.ReadFile(dekPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	kek, err := sqlitedrv.DeriveKey(testMaster(t), principalType, principalID(path))
	if err != nil {
		t.Fatalf("derive kek: %v", err)
	}
	dek, err := sqlitedrv.UnwrapDEK(kek, blob, sqlitedrv.PrincipalAAD(principalType, principalID(path)))
	if err != nil {
		t.Fatalf("unwrap dek: %v", err)
	}
	if len(dek) != 32 {
		t.Fatalf("dek length %d", len(dek))
	}

	// (4) plaintext backup preserved (defense-in-depth for the cutover window).
	if !fileExists(path + plainBakField) {
		t.Errorf("expected preserved plaintext backup %s", path+plainBakField)
	}
	if !isPlaintextHeader(path + plainBakField) {
		t.Errorf("plaintext backup should be readable plaintext")
	}

	// (5) real data content survived, not just counts.
	var name string
	if err := db.QueryRow(`SELECT name FROM crm_companies WHERE id='co-5'`).Scan(&name); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if name != "Co 5" {
		t.Errorf("content mismatch: got %q want %q", name, "Co 5")
	}
}

// TestOpenIdempotent proves a second Open of an already-migrated db is a no-op
// (no re-migration, data intact) — the steady-state boot path.
func TestOpenIdempotent(t *testing.T) {
	requireCipher(t)
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	path := filepath.Join(dir, "treasury.db")
	want := makePlaintextDB(t, path, 5, 5)

	resetMaster(testMaster(t))
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = db1.Close()
	hdr1 := firstBytes(t, path, 16)

	db2, err := Open(path) // already encrypted → openWithSidecar
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
	// Salt (header) is unchanged: the DEK/pages were not rewritten.
	if hdr2 := firstBytes(t, path, 16); string(hdr1) != string(hdr2) {
		t.Errorf("header changed on reopen (pages rewritten?)")
	}
}

// TestFreshCreateEncrypted proves a brand-new db is born encrypted (never a
// plaintext intermediate on disk).
func TestFreshCreateEncrypted(t *testing.T) {
	requireCipher(t)
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	path := filepath.Join(dir, "wallets.db")

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

// TestWrongMasterFailsClosed proves a sidecar minted under one master cannot be
// unwrapped under another — a lifted file + wrong key yields an error, never
// plaintext or a garbage key.
func TestWrongMasterFailsClosed(t *testing.T) {
	requireCipher(t)
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	path := filepath.Join(dir, "audit.db")
	makePlaintextDB(t, path, 3, 3)

	resetMaster(testMaster(t))
	db, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = db.Close()

	// Different master → different KEK → unwrap must fail.
	other := make([]byte, 32)
	for i := range other {
		other[i] = 0xAB
	}
	resetMaster(other)
	if _, err := Open(path); err == nil {
		t.Fatalf("SECURITY: opened encrypted db under the WRONG master key")
	}
}

// TestUnencryptedModeWhenNoKey proves that with no master key configured the
// open is a plain passthrough (dev/CI), leaving the file plaintext — so the
// encryption is genuinely gated on the KMS key, not accidental.
func TestUnencryptedModeWhenNoKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	path := filepath.Join(dir, "settings.db")

	resetMaster(nil)
	os.Unsetenv(masterKeyEnv)
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open dev: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE s(k TEXT)`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	_ = db.Close()
	if !isPlaintextHeader(path) {
		t.Fatalf("dev-mode db should be plaintext when no master key set")
	}
	if fileExists(path + dekSuffix) {
		t.Fatalf("dev-mode db should have no sidecar")
	}
}

// TestSetKeyButNoCipherFailsClosed proves that a configured master key on a
// build that cannot encrypt is a hard error — never a silent plaintext write.
func TestSetKeyButNoCipherFailsClosed(t *testing.T) {
	if sqlitedrv.EncryptionAvailable() {
		t.Skip("this build can encrypt; the fail-closed path is only reachable on pure-Go")
	}
	dir := t.TempDir()
	t.Setenv(dataDirEnv, dir)
	resetMaster(testMaster(t))
	if _, err := Open(filepath.Join(dir, "x.db")); err == nil {
		t.Fatalf("SECURITY: expected hard error when key set but build cannot encrypt")
	}
}

// TestPrincipalIDStable proves the key-derivation identity is the path relative
// to the data dir and is stable/injective across the tree.
func TestPrincipalIDStable(t *testing.T) {
	t.Setenv(dataDirEnv, "/var/lib/cloud")
	cases := map[string]string{
		"/var/lib/cloud/treasury.db":   "treasury.db",
		"/var/lib/cloud/base/data.db":  "base/data.db",
		"/var/lib/cloud/team/acct.db":  "team/acct.db",
	}
	for in, want := range cases {
		if got := principalID(in); got != want {
			t.Errorf("principalID(%q)=%q want %q", in, got, want)
		}
	}
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
