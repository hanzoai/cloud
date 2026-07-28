package kms

import (
	"os"
	"path/filepath"
	"testing"

	kmsstore "github.com/luxfi/kms/pkg/store"
	luxlog "github.com/luxfi/log"
	zapdb "github.com/luxfi/zapdb"
)

// TestMigrateLegacyZapDB proves the one-time cutover: a SEALED secret in the legacy
// embedded ZapDB lands in its per-org SQLite file, still Opens to the SAME plaintext
// (envelope preserved byte-for-byte), the legacy store is archived (never reopened),
// and a second run is an idempotent no-op.
func TestMigrateLegacyZapDB(t *testing.T) {
	dir := t.TempDir()
	key := testKey()
	const path, name, env, plaintext = "/orgs/acme/ci", "TOKEN", "main", "s3cr3t-value"

	seedLegacyZapDB(t, dir, key, path, name, env, plaintext)

	dst := newSecretStore(dir, false)
	if err := migrateLegacyZapDB(dir, key, dst, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The secret is now in per-org SQLite and still opens to the original plaintext.
	sec, err := dst.get(path, name, env)
	if err != nil {
		t.Fatalf("get after migrate: %v", err)
	}
	pt, err := kmsstore.Open(key, sec)
	if err != nil {
		t.Fatalf("open migrated secret: %v", err)
	}
	if string(pt) != plaintext {
		t.Fatalf("plaintext mismatch after migrate: got %q want %q", pt, plaintext)
	}

	// Legacy store archived; original dir renamed away.
	if _, err := os.Stat(filepath.Join(dir, "kms.migrated")); err != nil {
		t.Fatalf("legacy store not archived to kms.migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kms", "MANIFEST")); err == nil {
		t.Fatal("legacy dir should have been renamed away, still present")
	}

	// Idempotent: a second run is a clean no-op.
	if err := migrateLegacyZapDB(dir, key, dst, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("second migrate (idempotent) failed: %v", err)
	}
}

// TestMigrateLegacyZapDB_NoLegacyStore: a fresh deploy with no legacy store is a
// silent no-op (never creates the archive marker on nothing).
func TestMigrateLegacyZapDB_NoLegacyStore(t *testing.T) {
	dir := t.TempDir()
	dst := newSecretStore(dir, false)
	if err := migrateLegacyZapDB(dir, testKey(), dst, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("migrate over empty dir must be a no-op, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kms.migrated")); err == nil {
		t.Fatal("no legacy store — must not create a .migrated marker")
	}
}

// TestMigrateLegacyZapDB_CrossOrgRelocationStillDefended: after migration, the
// AAD-bound envelope invariant holds — a migrated record's sealed bytes, physically
// placed under a DIFFERENT org's coordinate, fail to Open (the swap defense survives
// the cutover).
func TestMigrateLegacyZapDB_CrossOrgRelocationStillDefended(t *testing.T) {
	dir := t.TempDir()
	key := testKey()
	const path, name, env, plaintext = "/orgs/acme/ci", "TOKEN", "main", "acme-only"
	seedLegacyZapDB(t, dir, key, path, name, env, plaintext)

	dst := newSecretStore(dir, false)
	if err := migrateLegacyZapDB(dir, key, dst, luxlog.NewNoOpLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sec, err := dst.get(path, name, env)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Forge the same sealed bytes onto another org's coordinate; Open must reject it.
	relocated := &kmsstore.Secret{
		Path: "/orgs/evil/ci", Name: name, Env: env,
		Scheme: sec.Scheme, Ciphertext: sec.Ciphertext, WrappedDEK: sec.WrappedDEK,
	}
	if _, err := kmsstore.Open(key, relocated); err == nil {
		t.Fatal("relocated secret Opened — AAD path binding broke across migration")
	}
}

// seedLegacyZapDB writes one sealed secret into a legacy ZapDB at {dir}/kms, exactly
// as the pre-per-org KMS did.
func seedLegacyZapDB(t *testing.T, dir string, key []byte, path, name, env, plaintext string) {
	t.Helper()
	legacyDir := filepath.Join(dir, "kms")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := zapdb.DefaultOptions(legacyDir).WithLogger(nil).
		WithEncryptionKey(key).WithIndexCacheSize(16 << 20)
	db, err := zapdb.Open(opts)
	if err != nil {
		t.Fatalf("open legacy zapdb: %v", err)
	}
	sec, err := kmsstore.Seal(key, path, name, env, []byte(plaintext))
	if err != nil {
		_ = db.Close()
		t.Fatalf("seal: %v", err)
	}
	if err := kmsstore.NewSecretStore(db).Put(sec); err != nil {
		_ = db.Close()
		t.Fatalf("put: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy zapdb: %v", err)
	}
}

// testKey is a deterministic 32-byte master key for tests.
func testKey() []byte {
	k := make([]byte, masterKeyLen)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}
