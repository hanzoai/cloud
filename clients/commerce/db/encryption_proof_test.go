package db

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// ledgerRow is a minimal money-shaped entity used by the proof tests. Its
// exported fields are serialized (camelCase) into the _entities.data JSON, so the
// Memo marker appears verbatim in a PLAINTEXT file — and must NOT appear in an
// encrypted one.
type ledgerRow struct {
	Account string
	Amount  int64
	Memo    string
}

func (ledgerRow) Kind() string { return "ledger" }

func testMasterKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*7 + 1)
	}
	return k
}

// newEncryptedTenant opens an org tenant store at dir/data.db encrypted under key.
func newEncryptedTenant(t *testing.T, path string, key []byte) *SQLiteDB {
	t.Helper()
	db, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:               path,
		Config:             DefaultConfig().SQLite,
		EnableVectorSearch: false,
		TenantID:           "acme",
		TenantType:         "org",
		MasterKey:          key,
	})
	if err != nil {
		t.Fatalf("open encrypted tenant: %v", err)
	}
	return db
}

// TestTenantEncryptionProof is commerce's anti-silent-plaintext gate for the
// per-tenant money stores. Under a cgo+libsqlcipher build it writes a marker to a
// tenant DB opened with a master key and asserts:
//
//   - the on-disk data.db is real ciphertext (no "SQLite format 3" header, no
//     plaintext marker),
//   - a wrapped-DEK sidecar was written,
//   - reopening with the SAME master key reads the row back,
//   - reopening with a WRONG master key fails closed.
//
// A cgo build WITHOUT libsqlcipher linked would silently write plaintext; this
// SKIPs there (the Docker build sets SQLITE_REQUIRE_CODEC=1 to turn the skip into
// a hard failure, so a mis-linked image can never ship).
func TestTenantEncryptionProof(t *testing.T) {
	const marker = "anti-plaintext-canary-commerce-7f3a"
	key := testMasterKey()

	if !sqlitedrv.EncryptionAvailable() {
		// Pure-Go backend cannot encrypt: opening a tenant with a master key MUST
		// fail rather than silently persist plaintext money data.
		dir := t.TempDir()
		if _, err := NewSQLiteDB(&SQLiteDBConfig{
			Path:       filepath.Join(dir, "data.db"),
			Config:     DefaultConfig().SQLite,
			TenantID:   "acme",
			TenantType: "org",
			MasterKey:  key,
		}); err == nil {
			t.Fatal("pure-Go backend accepted a master key — would ship plaintext")
		}
		return
	}

	if !sqlitedrv.CodecLinked() {
		if os.Getenv("SQLITE_REQUIRE_CODEC") == "1" {
			t.Fatal("SQLITE_REQUIRE_CODEC=1 but CodecLinked()=false: this build advertises encryption (cgo) yet did NOT link libsqlcipher — it would ship PLAINTEXT money data. Build with -tags \"libsqlite3 sqlite_fts5\" + CGO_CFLAGS=-DSQLITE_HAS_CODEC -DSQLITE_USE_URI=1 + CGO_LDFLAGS=-lsqlcipher")
		}
		t.Skip("cgo build without libsqlcipher linked; encryption assertions skipped (Docker build is the hard gate)")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")
	ctx := context.Background()

	// Write a marker row through the encrypted store, then close.
	db := newEncryptedTenant(t, dbPath, key)
	if !db.Encrypted() {
		t.Fatal("tenant reports Encrypted()=false with a master key set")
	}
	k := db.NewKey("ledger", "row-1", 0, nil)
	if _, err := db.Put(ctx, k, &ledgerRow{Account: "acme", Amount: 4242, Memo: marker}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The DEK sidecar must exist and NOT contain the raw marker.
	if !fileExists(dbPath + dekSuffix) {
		t.Fatalf("no DEK sidecar written at %q", dbPath+dekSuffix)
	}

	// The raw db file must be ciphertext.
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read raw db: %v", err)
	}
	if bytes.HasPrefix(raw, []byte("SQLite format 3\x00")) {
		t.Fatal("ENCRYPTION FAILURE: data.db has a plaintext SQLite header — libsqlcipher not linked")
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatal("ENCRYPTION FAILURE: plaintext marker found in data.db — money data is NOT encrypted at rest")
	}

	// Reopen with the correct master key: the row must read back.
	db2 := newEncryptedTenant(t, dbPath, key)
	var got ledgerRow
	if err := db2.Get(ctx, db2.NewKey("ledger", "row-1", 0, nil), &got); err != nil {
		t.Fatalf("reopen read: %v", err)
	}
	if got.Memo != marker || got.Amount != 4242 {
		t.Fatalf("reopen read = %+v, want Memo=%q Amount=4242", got, marker)
	}
	db2.Close()

	// Reopen with a WRONG master key: the DEK unwrap must fail closed.
	wrong := testMasterKey()
	wrong[0] ^= 0xFF
	if _, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:       dbPath,
		Config:     DefaultConfig().SQLite,
		TenantID:   "acme",
		TenantType: "org",
		MasterKey:  wrong,
	}); err == nil {
		t.Fatal("ENCRYPTION FAILURE: wrong master key opened the tenant — key not enforced")
	}
}

// TestTenantIsolation proves two tenants get independent DEKs: a file wrapped for
// "acme" cannot be opened as "globex" even under the same master key (principal
// AAD binding). Runs only with the codec linked.
func TestTenantIsolation(t *testing.T) {
	if !sqlitedrv.EncryptionAvailable() || !sqlitedrv.CodecLinked() {
		t.Skip("codec not linked; isolation proof requires SQLCipher")
	}
	key := testMasterKey()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "data.db")

	// Create the file wrapped for org "acme".
	db := newEncryptedTenant(t, dbPath, key)
	db.Close()

	// Opening the SAME file+key but as tenant "globex" must fail: the DEK sidecar
	// is AAD-bound to acme, so the unwrap GCM tag rejects it.
	if _, err := NewSQLiteDB(&SQLiteDBConfig{
		Path:       dbPath,
		Config:     DefaultConfig().SQLite,
		TenantID:   "globex",
		TenantType: "org",
		MasterKey:  key,
	}); err == nil {
		t.Fatal("cross-tenant open succeeded — per-tenant DEK isolation broken")
	}
}

// TestResolveMasterKey covers the env parsing (pure Go; runs under any build).
func TestResolveMasterKey(t *testing.T) {
	t.Setenv(masterKeyEnv, "")
	if k, err := resolveMasterKey(); err != nil || k != nil {
		t.Fatalf("unset: got (%v,%v), want (nil,nil)", k, err)
	}

	// A valid key is accepted ONLY on a build that actually encrypts (cgo AND
	// libsqlcipher linked). On a pure-Go OR a codec-unlinked cgo build, ANY set key
	// must be refused — never silently write plaintext. Malformed hex only reaches
	// its specific error where encryption is real (the not-available / no-codec
	// error fires first otherwise).
	canEncrypt := sqlitedrv.EncryptionAvailable() && sqlitedrv.CodecLinked()
	if canEncrypt {
		t.Setenv(masterKeyEnv, "not-hex!!")
		if _, err := resolveMasterKey(); err == nil {
			t.Fatal("malformed hex accepted")
		}
		t.Setenv(masterKeyEnv, "abcd")
		if _, err := resolveMasterKey(); err == nil {
			t.Fatal("wrong-length key accepted")
		}
		t.Setenv(masterKeyEnv, "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
		k, err := resolveMasterKey()
		if err != nil || len(k) != 32 {
			t.Fatalf("valid key: got (len=%d,%v), want (32,nil)", len(k), err)
		}
	} else {
		// No real encryption available: any set key must be refused.
		t.Setenv(masterKeyEnv, "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
		if _, err := resolveMasterKey(); err == nil {
			t.Fatal("build without a linked codec accepted a master key")
		}
	}
}
