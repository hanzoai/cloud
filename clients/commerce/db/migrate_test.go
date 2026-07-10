package db

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// TestEncryptDataDirRoundTrip proves the plaintext→encrypted migration loses no
// data: it writes three plaintext org rows, migrates them, then asserts the file
// is now ciphertext, the plaintext backup is retained, the row COUNT is preserved
// (before == after), and every row reads back through the encrypted daemon path
// with identical values.
func TestEncryptDataDirRoundTrip(t *testing.T) {
	if !sqlitedrv.EncryptionAvailable() || !sqlitedrv.CodecLinked() {
		t.Skip("codec not linked; migration proof requires SQLCipher")
	}
	const marker = "migrate-canary-commerce-9d2b"
	key := testMasterKey()
	ctx := context.Background()

	dataDir := t.TempDir()
	orgID := "acme"
	dbPath := filepath.Join(dataDir, "orgs", orgID, "data.db")

	// 1. Write THREE distinct PLAINTEXT rows (no master key). Key.Encode() returns
	// the stringID, so distinct string IDs ("row-1".."row-3") are three separate
	// _entities rows — a shared stringID would collapse them via ON CONFLICT.
	plain, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:       dbPath,
		Config:     DefaultConfig().SQLite,
		TenantID:   orgID,
		TenantType: "org",
	})
	if err != nil {
		t.Fatalf("open plaintext: %v", err)
	}
	memos := []string{marker, "second-row", "third-row"}
	for i, memo := range memos {
		k := plain.NewKey("ledger", fmt.Sprintf("row-%d", i+1), 0, nil)
		if _, err := plain.Put(ctx, k, &ledgerRow{Account: orgID, Amount: int64(100 + i), Memo: memo}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	before := countEntities(t, plain)
	if before != 3 {
		t.Fatalf("precondition: source has %d rows, want 3 (key collision?)", before)
	}
	plain.Close()

	// Precondition: the on-disk file is an UNENCRYPTED SQLite database (plaintext
	// header) and our canary is present in the clear (main file or its -wal sidecar
	// — the store writes in WAL mode). This is the state the migration converts away
	// from.
	rawMain, _ := os.ReadFile(dbPath)
	rawWAL, _ := os.ReadFile(dbPath + "-wal")
	if !bytes.HasPrefix(rawMain, []byte("SQLite format 3\x00")) {
		t.Fatal("precondition: source is not a plaintext SQLite file")
	}
	if !bytes.Contains(rawMain, []byte(marker)) && !bytes.Contains(rawWAL, []byte(marker)) {
		t.Fatal("precondition: canary not found in the clear in the plaintext db/-wal")
	}

	// 2. Migrate.
	rep, err := EncryptDataDir(dataDir, key, false)
	if err != nil {
		t.Fatalf("EncryptDataDir: %v", err)
	}
	if len(rep.Encrypted) != 1 || rep.Rows != before {
		t.Fatalf("report = %+v, want 1 db / %d rows", rep, before)
	}

	// 3. The file is now ciphertext; the plaintext backup is retained.
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read migrated db: %v", err)
	}
	if bytes.HasPrefix(raw, []byte("SQLite format 3\x00")) || bytes.Contains(raw, []byte(marker)) {
		t.Fatal("MIGRATION FAILURE: file is still plaintext after migration")
	}
	if !fileExists(dbPath + dekSuffix) {
		t.Fatal("no DEK sidecar after migration")
	}
	if !fileExists(dbPath + ".plaintext.bak") {
		t.Fatal("plaintext backup not retained")
	}

	// 4. COUNT-PRESERVED + VALUE-PRESERVED: every row reads back through the
	// encrypted daemon path with identical values.
	enc := newEncryptedTenant(t, dbPath, key)
	defer enc.Close()
	if after := countEntities(t, enc); after != before {
		t.Fatalf("row count changed across migration: before=%d after=%d", before, after)
	}
	for i, memo := range memos {
		var got ledgerRow
		k := enc.NewKey("ledger", fmt.Sprintf("row-%d", i+1), 0, nil)
		if err := enc.Get(ctx, k, &got); err != nil {
			t.Fatalf("read migrated row %d: %v", i+1, err)
		}
		if got.Memo != memo || got.Amount != int64(100+i) {
			t.Fatalf("migrated row %d = %+v, want Memo=%q Amount=%d", i+1, got, memo, 100+i)
		}
	}

	// 5. Idempotent: a second run skips (sidecar present).
	rep2, err := EncryptDataDir(dataDir, key, false)
	if err != nil {
		t.Fatalf("second EncryptDataDir: %v", err)
	}
	if len(rep2.Encrypted) != 0 {
		t.Fatalf("second run re-encrypted %v, want none", rep2.Encrypted)
	}
}

// TestEncryptDataDirFoldsPendingSourceWAL is the bug-1 data-safety invariant: a
// plaintext tenant left by a STOPPED daemon can have committed rows sitting only in
// an uncheckpointed -wal, and the migration MUST capture them (fold the WAL into
// the main file before reading). The prior bug opened the source with mode=ro,
// whose ability to read a live WAL is filesystem-dependent — where it cannot build
// the -shm it silently reads only the main file and drops those rows. The fix opens
// R/W and runs a verified TRUNCATE checkpoint, which is correct regardless.
//
// We reproduce the exact on-disk state by snapshotting a live WAL db's {main,-wal}
// fileset while the writer is still open and unchecked-pointed (a crash-consistent
// copy == a stopped daemon), then assert the WAL-only row survives migration into
// the encrypted output.
func TestEncryptDataDirFoldsPendingSourceWAL(t *testing.T) {
	if !sqlitedrv.EncryptionAvailable() || !sqlitedrv.CodecLinked() {
		t.Skip("codec not linked; migration proof requires SQLCipher")
	}
	const walMarker = "wal-only-row-canary-4b1f"
	key := testMasterKey()
	dataDir := t.TempDir()
	orgID := "walorg"
	if err := os.MkdirAll(filepath.Join(dataDir, "orgs", orgID), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "orgs", orgID, "data.db")

	// Build the pending-WAL state at a staging path (autocheckpoint OFF so the
	// insert's frames stay in the -wal), then copy the {main,-wal} fileset while the
	// connection is still open — never checkpointed — into the tenant path.
	staging := filepath.Join(t.TempDir(), "staging.db")
	src, err := sql.Open("sqlite", sqlitedrv.PragmaDSN(staging, []sqlitedrv.Pragma{
		{Name: "busy_timeout", Value: "10000"},
		{Name: "journal_mode", Value: "WAL"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	src.SetMaxOpenConns(1)
	if _, err := src.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for _, s := range baseSchemaDDL {
		if _, err := src.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.Exec(`INSERT INTO _entities (id, kind, namespace, data) VALUES (?,?,?,?)`,
		"row-1", "ledger", orgID, `{"account":"walorg","amount":777,"memo":"`+walMarker+`"}`); err != nil {
		t.Fatal(err)
	}
	copyFileForTest(t, staging, dbPath)               // main (lacks the row)
	copyFileForTest(t, staging+"-wal", dbPath+"-wal") // -wal (holds the row)
	src.Close()

	// The snapshot's MAIN file must NOT contain the row (it lives in the -wal): this
	// is exactly what a mode=ro read would misread as empty.
	if rawMain, _ := os.ReadFile(dbPath); bytes.Contains(rawMain, []byte(walMarker)) {
		t.Fatal("setup invalid: row already in main file — not exercising the pending-WAL path")
	}
	if rawWAL, _ := os.ReadFile(dbPath + "-wal"); !bytes.Contains(rawWAL, []byte(walMarker)) {
		t.Fatal("setup invalid: row not in the -wal snapshot")
	}

	// Migrate. Fold-the-WAL captures the row; a main-file-only read loses it.
	rep, err := EncryptDataDir(dataDir, key, false)
	if err != nil {
		t.Fatalf("EncryptDataDir: %v", err)
	}
	if rep.Rows != 1 {
		t.Fatalf("migrated %d rows, want 1 — the WAL-only row was dropped (source WAL not folded)", rep.Rows)
	}

	// The encrypted output must decrypt to the WAL-only row.
	if data := readEncryptedEntity(t, dbPath, key, sqlitedrv.PrincipalOrg, orgID, "row-1"); !strings.Contains(data, walMarker) {
		t.Fatalf("decrypted row = %q, missing WAL canary", data)
	}
}

// TestAssertNoPendingWAL is the bug-2 fail-closed safety net. The destination is
// checkpointed (TRUNCATE) before close so its -wal is empty; if SQLite ever leaves
// a NON-EMPTY -wal (persist-WAL, a reader-pinned close, a crash), cutover renaming
// only the main file would orphan those committed frames. assertNoPendingWAL turns
// that silent loss into a hard error BEFORE the rename, leaving the source intact.
func TestAssertNoPendingWAL(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "data.db")
	if err := os.WriteFile(db, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}

	// No -wal at all → ok (the expected post-TRUNCATE state).
	if err := assertNoPendingWAL(db); err != nil {
		t.Fatalf("absent -wal: got %v, want nil", err)
	}
	// Zero-length -wal husk → ok.
	if err := os.WriteFile(db+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := assertNoPendingWAL(db); err != nil {
		t.Fatalf("empty -wal: got %v, want nil", err)
	}
	// Non-empty -wal (unflushed frames) → MUST error.
	if err := os.WriteFile(db+"-wal", []byte("uncheckpointed frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := assertNoPendingWAL(db); err == nil {
		t.Fatal("non-empty -wal accepted — cutover would drop committed frames")
	}
}

// TestCheckpointAndCutoverPreserveDestWAL is the bug-2 regression. The encrypted
// temp db is written in WAL mode, so its committed rows can sit in an
// uncheckpointed -wal. cutover renames only <db> and <db>.dek — a -wal left behind
// would orphan its frames (data loss). The fix checkpoints the destination
// (TRUNCATE) before close so it is a single self-contained file, and cutover
// discards the empty husks. This test forces a pending destination -wal
// (autocheckpoint OFF) and asserts checkpointWAL + cutover yield a file that
// reopens with every row.
func TestCheckpointAndCutoverPreserveDestWAL(t *testing.T) {
	if !sqlitedrv.EncryptionAvailable() || !sqlitedrv.CodecLinked() {
		t.Skip("codec not linked; requires SQLCipher")
	}
	const canary = "dest-wal-canary-8c2e"
	key := testMasterKey()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	tmpDB := dbPath + ".encrypting"

	// A plaintext "current" db must exist for cutover to back up.
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00 placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Mint the destination DEK + open the encrypted temp with autocheckpoint OFF so
	// inserted frames stay in the -wal.
	dek, err := resolveDEK(tmpDB, key, sqlitedrv.PrincipalOrg, "acme")
	if err != nil {
		t.Fatal(err)
	}
	_, dsn := encDriverDSN(tmpDB, dek, DefaultConfig().SQLite)
	dst, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	dst.SetMaxOpenConns(1)
	if _, err := dst.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for _, s := range baseSchemaDDL {
		if _, err := dst.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := dst.Exec(`INSERT INTO _entities (id, kind, namespace, data) VALUES (?,?,?,?)`,
			fmt.Sprintf("row-%d", i), "ledger", "acme", fmt.Sprintf(`{"memo":"%s-%d"}`, canary, i)); err != nil {
			t.Fatal(err)
		}
	}
	// Frames must be pending in the -wal (not yet folded into the main temp file).
	if fi, err := os.Stat(tmpDB + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("setup invalid: expected a non-empty destination -wal, err=%v", err)
	}

	// The fix: verified checkpoint folds the WAL, close, assert none pending, cut over.
	if err := checkpointWAL(dst); err != nil {
		t.Fatalf("checkpointWAL: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := assertNoPendingWAL(tmpDB); err != nil {
		t.Fatalf("assertNoPendingWAL after checkpoint: %v", err)
	}
	if err := cutover(dbPath, tmpDB); err != nil {
		t.Fatalf("cutover: %v", err)
	}

	// Reopen the cut-over encrypted db: all 5 rows must be present (none orphaned in
	// a discarded -wal).
	var n int
	reopened := openEncrypted(t, dbPath, key, sqlitedrv.PrincipalOrg, "acme")
	defer reopened.Close()
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM _entities`).Scan(&n); err != nil {
		t.Fatalf("count after cutover: %v", err)
	}
	if n != 5 {
		t.Fatalf("after cutover the reopened db has %d rows, want 5 — destination WAL frames were lost", n)
	}
}

// --- test helpers ---

// countEntities returns the number of rows in _entities via the store's read pool.
func countEntities(t *testing.T, db *SQLiteDB) int {
	t.Helper()
	var n int
	if err := db.readDB.QueryRow(`SELECT COUNT(*) FROM _entities`).Scan(&n); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	return n
}

// copyFileForTest copies src to dst verbatim; a missing src (e.g. an absent -shm)
// is a no-op so callers can copy an optional sidecar unconditionally.
func copyFileForTest(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// openEncrypted opens dbPath through the encrypted driver, resolving its DEK from
// the sidecar under the given master key + principal.
func openEncrypted(t *testing.T, dbPath string, key []byte, pt sqlitedrv.PrincipalType, id string) *sql.DB {
	t.Helper()
	dek, err := resolveDEK(dbPath, key, pt, id)
	if err != nil {
		t.Fatalf("resolve dek for %s: %v", dbPath, err)
	}
	_, dsn := encDriverDSN(dbPath, dek, DefaultConfig().SQLite)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open encrypted %s: %v", dbPath, err)
	}
	return db
}

// readEncryptedEntity reads _entities.data for id from the encrypted db at dbPath.
func readEncryptedEntity(t *testing.T, dbPath string, key []byte, pt sqlitedrv.PrincipalType, id, rowID string) string {
	t.Helper()
	db := openEncrypted(t, dbPath, key, pt, id)
	defer db.Close()
	var data string
	if err := db.QueryRow(`SELECT data FROM _entities WHERE id=?`, rowID).Scan(&data); err != nil {
		t.Fatalf("read encrypted row %q: %v", rowID, err)
	}
	return data
}
