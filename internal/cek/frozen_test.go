package cek

import (
	"os"
	"path/filepath"
	"testing"
)

// The frozen fixture pins the SQLCipher on-disk FORMAT the data plane is written
// with. A committed encrypted store (created under the build's frozen
// libsqlcipher) is opened by TestFrozenFixtureOpens; if a future libsqlcipher
// upgrade changes the default cipher format, opening a store written under the
// old format FAILS — turning a silent prod brick into a red CI. (Build-time
// SQLITE_REQUIRE_CODEC only proves a FRESH db encrypts; it cannot detect a format
// change against pre-existing stores — the fixture is what does.)

const frozenCanary = "cek-format-canary-v1"

// frozenMaster is a FIXED, obviously-fake test key (0x00..0x1f) used only to wrap
// the committed fixture's DEK. Not a secret; never used in production.
func frozenMaster() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

const frozenDir = "testdata/frozen"

// TestGenerateFrozenFixture (re)writes the committed fixture. Gated on
// CEK_GEN_FIXTURE=1 and run by hand ONLY when the format is intentionally rev'd;
// the committed bytes are otherwise frozen.
func TestGenerateFrozenFixture(t *testing.T) {
	if os.Getenv("CEK_GEN_FIXTURE") != "1" {
		t.Skip("set CEK_GEN_FIXTURE=1 to regenerate the frozen fixture")
	}
	requireCipher(t)
	if err := os.MkdirAll(frozenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(frozenDir, "store.db")
	for _, s := range []string{"", dekSuffix, "-wal", "-shm"} {
		_ = os.Remove(dbPath + s)
	}
	dek, sidecar, err := mintSidecar(frozenMaster())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath+dekSuffix, sidecar, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := openKeyed(dbPath, dek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`CREATE TABLE frozen(id INTEGER PRIMARY KEY, note TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`INSERT INTO frozen VALUES(1,?)`, frozenCanary); err != nil {
		t.Fatal(err)
	}
	_, _ = h.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`) // fold into the main .db
	_ = h.Close()
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	if isPlaintextHeader(dbPath) {
		t.Fatal("generated fixture is plaintext")
	}
	t.Logf("wrote frozen fixture %s (+%s)", dbPath, dekSuffix)
}

// TestFrozenFixtureOpens opens the COMMITTED encrypted fixture (copied to temp so
// the committed bytes are never mutated) and reads its canary row. A libsqlcipher
// format change makes this fail — a red CI instead of a silent prod brick.
func TestFrozenFixtureOpens(t *testing.T) {
	requireCipher(t)
	src := filepath.Join(frozenDir, "store.db")
	if !fileExists(src) {
		t.Fatal("frozen fixture missing — regenerate with CEK_GEN_FIXTURE=1 and commit testdata/frozen/")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "store.db")
	copyFile(t, src, dst)
	copyFile(t, src+dekSuffix, dst+dekSuffix)

	resetMaster(frozenMaster())
	db, err := Open(dst)
	if err != nil {
		t.Fatalf("FORMAT DRIFT: frozen fixture failed to open (libsqlcipher format changed?): %v", err)
	}
	defer func() { _ = db.Close() }()
	var note string
	if err := db.QueryRow(`SELECT note FROM frozen WHERE id=1`).Scan(&note); err != nil {
		t.Fatalf("read canary: %v", err)
	}
	if note != frozenCanary {
		t.Fatalf("canary mismatch: got %q want %q", note, frozenCanary)
	}
}
