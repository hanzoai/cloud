package kms

// RED RE-REVIEW — attacking the new 3-way store-open switch in New() and the
// hoisted validCoords. Goal: defeat the fail-secure switch via any
// {key present|absent|wrong} × {store absent|present|MANIFEST-missing|plaintext}
// combination, or find a silent downgrade / brick the switch was meant to close.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	luxlog "github.com/luxfi/log"
	zapdb "github.com/luxfi/zapdb"
)

func rrKey(t *testing.T, fill byte) string {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = fill
	}
	return base64.StdEncoding.EncodeToString(k)
}

func rrRandKey(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// RR-1: the fix's headline claim — fresh health-only boot (no key, no store) must
// NOT write ANYTHING to disk (no plaintext KEYREGISTRY, no MANIFEST), so the first
// keyed boot opens a clean encrypted store. Verify the dir stays empty AND the
// subsequent keyed boot succeeds + roundtrips.
func TestRR1_FreshHealthOnlyWritesNothing(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()
	dbDir := filepath.Join(root, "kms")

	// Boot 1: no key, no existing store → ephemeral in-memory.
	c1, err := New(Config{DataDir: root, MasterKeyB64: ""}, log)
	if err != nil {
		t.Fatalf("fresh health-only New: %v", err)
	}
	if c1.Ready() {
		t.Fatal("health-only must not be Ready")
	}
	c1.Close()

	// The on-disk kms dir must contain NO store files (ideally not exist at all).
	if entries, statErr := os.ReadDir(dbDir); statErr == nil {
		for _, e := range entries {
			t.Errorf("BREACH: fresh health-only wrote to disk: %s/%s", dbDir, e.Name())
		}
		// A MANIFEST or KEYREGISTRY here would re-introduce the brick.
		if _, e := os.Stat(filepath.Join(dbDir, "MANIFEST")); e == nil {
			t.Fatal("BRICK-REGRESSION: MANIFEST written in health-only mode")
		}
		if _, e := os.Stat(filepath.Join(dbDir, "KEYREGISTRY")); e == nil {
			t.Fatal("BRICK-REGRESSION: plaintext KEYREGISTRY written in health-only mode")
		}
	} else {
		t.Logf("clean: %s does not exist after health-only boot", dbDir)
	}

	// Boot 2: real key → must open a CLEAN encrypted store and roundtrip (8c fixed).
	c2, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xA1)}, log)
	if err != nil {
		t.Fatalf("BRICK: keyed boot after health-only failed: %v", err)
	}
	defer c2.Close()
	if err := c2.Put("/orgs/x", "K", "default", []byte("v")); err != nil {
		t.Fatalf("put after health-only→key: %v", err)
	}
	if got, err := c2.Get("/orgs/x", "K", "default"); err != nil || string(got) != "v" {
		t.Fatalf("roundtrip after health-only→key: got=%q err=%v", got, err)
	}
	t.Logf("8c FIXED: health-only wrote nothing; keyed boot opened clean encrypted store + roundtrips")
}

// RR-2 (8b re-verify): encrypted store present + NO key → MUST fail loudly, NOT
// silently shadow with an in-memory store (Blue's first attempt regressed this).
func TestRR2_EncryptedStoreNoKeyFailsLoud(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()

	// Create an encrypted store with a key + a secret.
	c1, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xB2)}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	_ = c1.Put("/orgs/x", "SECRET", "default", []byte("must-not-vanish"))
	c1.Close()

	// Boot 2: no key. MANIFEST exists → must ERROR, not open in-memory.
	c2, err := New(Config{DataDir: root, MasterKeyB64: ""}, log)
	if err == nil {
		defer c2.Close()
		t.Fatalf("SILENT-DOWNGRADE: encrypted store + no key opened without error (Ready=%v). "+
			"On-disk secret would be shadowed by an empty in-mem KV.", c2.Ready())
	}
	t.Logf("8b holds: encrypted+no-key fails loud: %v", err)
}

// RR-3 (8a re-verify): wrong key over an encrypted store → fail closed (zapdb
// registry sanity), never silent.
func TestRR3_WrongKeyFailsClosed(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()
	c1, _ := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xC3)}, log)
	_ = c1.Put("/orgs/x", "K", "default", []byte("v"))
	c1.Close()

	c2, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xD4)}, log)
	if err == nil {
		defer c2.Close()
		_, gErr := c2.Get("/orgs/x", "K", "default")
		t.Fatalf("SILENT-DOWNGRADE: wrong key opened store without error (Get=%v)", gErr)
	}
	t.Logf("8a holds: wrong key fails closed: %v", err)
}

// RR-4: THE MANIFEST-SENTINEL BLIND SPOT. storeExistsOnDisk keys ONLY on MANIFEST.
// If a real encrypted store loses its MANIFEST but keeps KEYREGISTRY + SST/vlog
// (botched backup restore, partial rsync, fs corruption), a keyless boot sees no
// MANIFEST → goes IN-MEMORY, silently shadowing the on-disk encrypted data. Prove
// the sentinel's false-negative → silent downgrade. Then prove the follow-on: a
// keyed boot over the MANIFEST-less-but-KEYREGISTRY-present dir.
func TestRR4_ManifestSentinelBlindSpot(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()
	dbDir := filepath.Join(root, "kms")

	// Create a real encrypted store.
	c1, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xE5)}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	_ = c1.Put("/orgs/x", "K", "default", []byte("on-disk-encrypted"))
	c1.Close()

	// List the on-disk artifacts.
	before, _ := os.ReadDir(dbDir)
	var names []string
	for _, e := range before {
		names = append(names, e.Name())
	}
	t.Logf("store artifacts: %v", names)

	// Simulate MANIFEST loss (keep KEYREGISTRY + data files).
	keyReg := filepath.Join(dbDir, "KEYREGISTRY")
	if _, e := os.Stat(keyReg); e != nil {
		t.Fatalf("expected KEYREGISTRY present: %v", e)
	}
	if e := os.Remove(filepath.Join(dbDir, "MANIFEST")); e != nil {
		t.Fatalf("remove MANIFEST: %v", e)
	}

	// Boot 2: NO key, MANIFEST gone but KEYREGISTRY + data remain.
	c2, err := New(Config{DataDir: root, MasterKeyB64: ""}, log)
	if err == nil {
		// It opened. Is it in-memory (silent downgrade) while encrypted data sits
		// on disk? storeExistsOnDisk returned false because MANIFEST is gone.
		defer c2.Close()
		t.Logf("SENTINEL BLIND SPOT: no-key boot with MANIFEST removed but KEYREGISTRY present "+
			"→ New succeeded (Ready=%v). storeExistsOnDisk keys only on MANIFEST, so a store that "+
			"lost its MANIFEST is treated as absent → in-memory shadow of on-disk encrypted data. "+
			"KEYREGISTRY still on disk: %v", c2.Ready(), fileExists(keyReg))
		// Not a confidentiality breach (data still encrypted at rest), but a silent
		// data-availability downgrade the fix's own goal ("do not silently ignore
		// on-disk secrets") does not fully achieve when the sentinel file is the one lost.
	} else {
		t.Logf("no-key boot with MANIFEST-less store failed (conservative): %v", err)
	}
}

// RR-5: PLAINTEXT-STORE MIGRATION BRICK. An operator upgrading from the OLD blue
// binary (which wrote a plaintext store) to the new keyed binary: a KEYED boot
// with WithEncryptionKey against a PRE-EXISTING PLAINTEXT store. zapdb's registry
// sanity rejects it → brick. Prove the migration path fails (documented risk).
func TestRR5_PlaintextStoreThenKeyedBrick(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()
	dbDir := filepath.Join(root, "kms")

	// Simulate the OLD behavior: open a PLAINTEXT disk store directly (no key).
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pdb, err := zapdb.Open(zapdb.DefaultOptions(dbDir).WithLogger(nil))
	if err != nil {
		t.Fatalf("open plaintext store: %v", err)
	}
	// write something so it's a real store with a MANIFEST + KEYREGISTRY.
	_ = pdb.Update(func(txn *zapdb.Txn) error { return txn.Set([]byte("k"), []byte("v")) })
	pdb.Close()
	if !fileExists(filepath.Join(dbDir, "MANIFEST")) {
		t.Fatal("expected plaintext store MANIFEST")
	}

	// Now the NEW binary boots WITH a key → keyed branch, WithEncryptionKey.
	c, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xF6)}, log)
	if err != nil {
		t.Logf("MIGRATION BRICK CONFIRMED: keyed boot over a pre-existing PLAINTEXT store fails "+
			"(New → %v). An operator upgrading from a binary that wrote a plaintext store must wipe "+
			"{DataDir}/kms first. Not a confidentiality issue (no secret was in the plaintext store "+
			"in the fixed flow), but a migration foot-gun if a plaintext store ever reached disk.", err)
		return
	}
	defer c.Close()
	t.Logf("keyed boot over plaintext store SUCCEEDED (Ready=%v) — zapdb tolerated it?", c.Ready())
}

// RR-6: does the in-memory branch leak ANY disk write? Drive a full lifecycle in
// health-only (in-mem) — List/health-metadata — and confirm the dir is pristine.
func TestRR6_InMemoryNeverTouchesDisk(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()
	dbDir := filepath.Join(root, "kms")

	c, err := New(Config{DataDir: root, MasterKeyB64: ""}, log)
	if err != nil {
		t.Fatalf("in-mem New: %v", err)
	}
	defer c.Close()
	// List works (no key needed) but must not create disk files.
	if _, err := c.List("/orgs/x", "default"); err != nil {
		t.Fatalf("list in health-only: %v", err)
	}
	// Secret ops fail closed.
	if err := c.Put("/orgs/x", "K", "default", []byte("v")); err == nil {
		t.Fatal("BREACH: Put succeeded in health-only in-memory mode")
	}
	if _, err := c.Get("/orgs/x", "K", "default"); err == nil {
		t.Fatal("BREACH: Get succeeded in health-only in-memory mode")
	}
	if _, err := os.Stat(dbDir); err == nil {
		if entries, _ := os.ReadDir(dbDir); len(entries) > 0 {
			t.Fatalf("BREACH: in-memory mode created disk files under %s", dbDir)
		}
	}
	t.Logf("in-memory health-only: List OK, Put/Get fail closed, zero disk writes")
}

// RR-7: validator dedup — the facade (parseRef→Get/Put) must reject the SAME bad
// coords the HTTP boundary rejects, now that validation is hoisted into kms.
// Re-run the facade-asymmetry attack; it must now be BLOCKED.
func TestRR7_FacadeValidationNowEnforced(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: rrRandKey(t)}, log)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	// NUL + control chars in the ref — previously roundtripped; must now error.
	badRef := "svc/EVIL\x00NAME@pr\x01od"
	if err := c.PutSecret(ctx, badRef, []byte("x")); err == nil {
		t.Fatalf("REGRESSION: facade still accepts NUL+ctrl ref %q — validation not enforced on Put", badRef)
	} else {
		t.Logf("facade Put rejects bad ref: %v", err)
	}
	// Empty ref → name "" — must error now.
	if err := c.PutSecret(ctx, "", []byte("x")); err == nil {
		t.Fatalf("REGRESSION: facade still stores a nameless secret (empty ref)")
	}
	// Direct Get with bad coords must also reject (not just Put).
	if _, err := c.Get("/orgs/x", "bad\x00name", "default"); err == nil {
		t.Fatalf("REGRESSION: Get accepts NUL in name")
	}
	// A CLEAN ref must still work.
	if err := c.PutSecret(ctx, "svc/GOOD@prod", []byte("ok")); err != nil {
		t.Fatalf("clean ref rejected: %v", err)
	}
	if got, err := c.GetSecret(ctx, "svc/GOOD@prod"); err != nil || string(got) != "ok" {
		t.Fatalf("clean ref roundtrip: got=%q err=%v", got, err)
	}
	t.Logf("facade asymmetry CLOSED: bad coords rejected on Put+Get+empty, clean ref works")
}

// RR-8: validCoords must NOT reject a legitimate path='/' (org/collection root).
// A too-strict validator would break every root-level secret. Guard against an
// over-correction that fails closed on valid input.
func TestRR8_RootPathStillValid(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	c, _ := New(Config{DataDir: t.TempDir(), MasterKeyB64: rrRandKey(t)}, log)
	defer c.Close()
	// path "/" (root) with a bare name — the parseRef("NAME") case.
	if err := c.Put("/", "ROOT_SECRET", "default", []byte("v")); err != nil {
		t.Fatalf("root-path Put rejected (over-correction): %v", err)
	}
	if got, err := c.Get("/", "ROOT_SECRET", "default"); err != nil || string(got) != "v" {
		t.Fatalf("root-path roundtrip: got=%q err=%v", got, err)
	}
	// List at root must also pass validation.
	if _, err := c.List("/", "default"); err != nil {
		t.Fatalf("root-path List rejected: %v", err)
	}
	t.Logf("root path '/' remains valid for Put/Get/List")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// RR-4b: blast radius of the MANIFEST blind spot. After a store loses MANIFEST,
// does supplying the KEY on a later boot (keyed branch → WithEncryptionKey over
// the KEYREGISTRY-present-but-MANIFEST-absent dir) RECOVER the secret, or
// corrupt/brick? Determines whether RR4 is a transient blip or data loss.
func TestRR4b_ManifestLossThenKeyedRecovery(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()
	dbDir := filepath.Join(root, "kms")
	key := rrKey(t, 0xAB)

	c1, err := New(Config{DataDir: root, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	if err := c1.Put("/orgs/x", "K", "default", []byte("recover-me")); err != nil {
		t.Fatalf("put: %v", err)
	}
	c1.Close()

	// Lose MANIFEST.
	if e := os.Remove(filepath.Join(dbDir, "MANIFEST")); e != nil {
		t.Fatalf("rm MANIFEST: %v", e)
	}

	// Boot with the SAME key (keyed branch). What does zapdb do with no MANIFEST?
	c2, err := New(Config{DataDir: root, MasterKeyB64: key}, log)
	if err != nil {
		t.Logf("BLAST RADIUS: after MANIFEST loss, keyed boot FAILS (New → %v) — the store is "+
			"bricked until wiped; the on-disk secret is UNRECOVERABLE via this path.", err)
		return
	}
	defer c2.Close()
	got, gErr := c2.Get("/orgs/x", "K", "default")
	if gErr == nil && string(got) == "recover-me" {
		t.Logf("BLAST RADIUS: MANIFEST loss is RECOVERABLE — keyed boot rebuilt the manifest and "+
			"the secret survives (Get=%q). RR4 is a transient availability blip, not data loss.", got)
	} else {
		t.Logf("BLAST RADIUS: keyed boot after MANIFEST loss opened but the secret is GONE "+
			"(Get=%q err=%v) — silent data loss.", got, gErr)
	}
}
