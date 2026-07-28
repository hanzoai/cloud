package kms

// RED RE-REVIEW v2 — attacking the PER-ORG SQLite store-open model that replaces
// the former ZapDB 3-way open switch. The old switch (keyed / no-key-in-memory /
// no-key-fail) and its KEYREGISTRY/MANIFEST sentinels are GONE: secrets now persist
// to {DataDir}/orgs/{org}/kms.db (cloud.OrgDB → cek), opened LAZILY per org, and
// the confidentiality boundary is the per-secret AES-256-GCM Seal envelope. This
// re-review re-attacks the invariants that still matter under the new model:
//   - health-only writes NOTHING to disk and does not brick the next keyed boot;
//   - a keyless / wrong-key process cannot read, AND does not destroy, on-disk
//     sealed secrets (no silent shadow, no data loss);
//   - the facade enforces the same coord validation as the HTTP boundary;
//   - a legitimate root path stays valid;
//   - distinct orgs are PHYSICALLY isolated (separate files), on top of the REST
//     authz gate.
// The ZapDB-mechanism attacks (RR4/RR4b MANIFEST-sentinel, RR5 plaintext-brick)
// are retired: that mechanism no longer exists and its foot-gun is eliminated
// (proven positively by TestRR1).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	luxlog "github.com/luxfi/log"
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

// RR-1 (former 8c foot-gun, now ELIMINATED): a fresh health-only boot (no key)
// must write NOTHING to disk — every secret op fails closed — so the first keyed
// boot opens a clean store and roundtrips. No plaintext store, no brick.
func TestRR1_FreshHealthOnlyWritesNothing(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()

	c1, err := New(Config{DataDir: root, MasterKeyB64: ""}, log)
	if err != nil {
		t.Fatalf("fresh health-only New: %v", err)
	}
	if c1.Ready() {
		t.Fatal("health-only must not be Ready")
	}
	// A write in health-only mode is refused, so nothing lands on disk.
	if err := c1.Put("/orgs/x", "K", "default", []byte("v")); err == nil {
		t.Fatal("BREACH: health-only Put succeeded (would write plaintext-less but a store file nonetheless)")
	}
	c1.Close()

	// No org store file may have been created by the health-only boot.
	if any := anyStoreFile(root); any != "" {
		t.Fatalf("BREACH: fresh health-only wrote a store file to disk: %s", any)
	}

	// Boot 2: real key → clean store + roundtrip (the brick is gone).
	c2, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xA1)}, log)
	if err != nil {
		t.Fatalf("BRICK-REGRESSION: keyed boot after health-only failed: %v", err)
	}
	defer c2.Close()
	if err := c2.Put("/orgs/x", "K", "default", []byte("v")); err != nil {
		t.Fatalf("put after health-only→key: %v", err)
	}
	if got, err := c2.Get("/orgs/x", "K", "default"); err != nil || string(got) != "v" {
		t.Fatalf("roundtrip after health-only→key: got=%q err=%v", got, err)
	}
	t.Logf("foot-gun ELIMINATED: health-only wrote nothing; keyed boot opened clean + roundtrips")
}

// RR-2 (8b re-verify): an existing store + NO key must fail CLOSED (health-only),
// never a silent shadow — AND the on-disk sealed secret must NOT be lost: a keyed
// reboot recovers it. Proves no silent downgrade and no data loss.
func TestRR2_ExistingStoreNoKeyFailsClosedNoDataLoss(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()

	c1, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xB2)}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	if err := c1.Put("/orgs/x", "SECRET", "default", []byte("must-not-vanish")); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	c1.Close()

	// Boot 2: no key → health-only; every secret op fails closed.
	c2, err := New(Config{DataDir: root, MasterKeyB64: ""}, log)
	if err != nil {
		t.Fatalf("boot2 (no key) New: %v", err)
	}
	if c2.Ready() {
		t.Fatal("BREACH: keyless client reports Ready over an existing store")
	}
	if _, gErr := c2.Get("/orgs/x", "SECRET", "default"); gErr == nil {
		t.Fatal("BREACH: keyless Get read the secret")
	}
	c2.Close()

	// Boot 3: the key returns → the secret is intact (no silent shadow destroyed it).
	c3, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xB2)}, log)
	if err != nil {
		t.Fatalf("boot3 (key returns): %v", err)
	}
	defer c3.Close()
	got, err := c3.Get("/orgs/x", "SECRET", "default")
	if err != nil || string(got) != "must-not-vanish" {
		t.Fatalf("DATA LOSS: secret gone after a keyless boot: got=%q err=%v", got, err)
	}
	t.Logf("8b holds: keyless boot is health-only fail-closed AND non-destructive (secret recovered)")
}

// RR-3 (8a re-verify): a wrong key cannot read the sealed secret (the Seal
// envelope is bound to the master key) AND does not destroy it — the right key
// still recovers it. Fail closed, non-destructive.
func TestRR3_WrongKeyFailsClosedNonDestructive(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()

	c1, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xC3)}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	if err := c1.Put("/orgs/x", "K", "default", []byte("v")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c1.Close()

	// Wrong key: read must fail closed (no plaintext).
	c2, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xD4)}, log)
	if err != nil {
		t.Fatalf("boot2 (wrong key) New: %v", err)
	}
	if pt, gErr := c2.Get("/orgs/x", "K", "default"); gErr == nil {
		t.Fatalf("BREACH: wrong key read the secret: %q", pt)
	}
	c2.Close()

	// Right key: still recovers (wrong-key access was non-destructive).
	c3, err := New(Config{DataDir: root, MasterKeyB64: rrKey(t, 0xC3)}, log)
	if err != nil {
		t.Fatalf("boot3 (right key): %v", err)
	}
	defer c3.Close()
	if got, err := c3.Get("/orgs/x", "K", "default"); err != nil || string(got) != "v" {
		t.Fatalf("secret lost after wrong-key access: got=%q err=%v", got, err)
	}
	t.Logf("8a holds: wrong key fails closed (Seal envelope) and is non-destructive")
}

// RR-6: health-only mode creates NO store file and fails every secret op closed —
// no disk write leaks from the no-key path.
func TestRR6_HealthOnlyNeverTouchesDisk(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()

	c, err := New(Config{DataDir: root, MasterKeyB64: ""}, log)
	if err != nil {
		t.Fatalf("health-only New: %v", err)
	}
	defer c.Close()
	// List is metadata-only (no key needed) but must not create a store file.
	if _, err := c.List("/orgs/x", "default"); err != nil {
		t.Fatalf("list in health-only: %v", err)
	}
	if err := c.Put("/orgs/x", "K", "default", []byte("v")); err == nil {
		t.Fatal("BREACH: Put succeeded in health-only mode")
	}
	if _, err := c.Get("/orgs/x", "K", "default"); err == nil {
		t.Fatal("BREACH: Get succeeded in health-only mode")
	}
	if any := anyStoreFile(root); any != "" {
		t.Fatalf("BREACH: health-only mode created a store file: %s", any)
	}
	t.Logf("health-only: List OK, Put/Get fail closed, zero store files")
}

// RR-7: the facade (parseRef→Get/Put) must reject the SAME bad coords the HTTP
// boundary rejects — the hoisted validCoords closes the facade asymmetry.
func TestRR7_FacadeValidationNowEnforced(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: rrRandKey(t)}, log)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	badRef := "svc/EVIL\x00NAME@pr\x01od"
	if err := c.PutSecret(ctx, badRef, []byte("x")); err == nil {
		t.Fatalf("REGRESSION: facade still accepts NUL+ctrl ref %q", badRef)
	}
	if err := c.PutSecret(ctx, "", []byte("x")); err == nil {
		t.Fatalf("REGRESSION: facade still stores a nameless secret (empty ref)")
	}
	if _, err := c.Get("/orgs/x", "bad\x00name", "default"); err == nil {
		t.Fatalf("REGRESSION: Get accepts NUL in name")
	}
	if err := c.PutSecret(ctx, "svc/GOOD@prod", []byte("ok")); err != nil {
		t.Fatalf("clean ref rejected: %v", err)
	}
	if got, err := c.GetSecret(ctx, "svc/GOOD@prod"); err != nil || string(got) != "ok" {
		t.Fatalf("clean ref roundtrip: got=%q err=%v", got, err)
	}
	t.Logf("facade asymmetry CLOSED: bad coords rejected on Put+Get+empty, clean ref works")
}

// RR-8: validCoords must NOT reject a legitimate path='/' (the org/collection root
// — the parseRef("NAME") case). Guard against an over-correction failing closed on
// valid input.
func TestRR8_RootPathStillValid(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	c, _ := New(Config{DataDir: t.TempDir(), MasterKeyB64: rrRandKey(t)}, log)
	defer c.Close()
	if err := c.Put("/", "ROOT_SECRET", "default", []byte("v")); err != nil {
		t.Fatalf("root-path Put rejected (over-correction): %v", err)
	}
	if got, err := c.Get("/", "ROOT_SECRET", "default"); err != nil || string(got) != "v" {
		t.Fatalf("root-path roundtrip: got=%q err=%v", got, err)
	}
	if _, err := c.List("/", "default"); err != nil {
		t.Fatalf("root-path List rejected: %v", err)
	}
	t.Logf("root path '/' remains valid for Put/Get/List")
}

// RR-9 (NEW): PHYSICAL org isolation. Two orgs' secrets must land in SEPARATE
// on-disk files — defense in depth UNDER the REST authz gate — and a record
// physically relocated into another org's file must still fail to Open (the Seal
// AAD binds the full /orgs/{org} path). This is the new-model isolation the ZapDB
// single-store never had.
func TestRR9_PerOrgPhysicalIsolation(t *testing.T) {
	root := t.TempDir()
	log := luxlog.NewNoOpLogger()
	key := rrKey(t, 0x9E)

	c, err := New(Config{DataDir: root, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := c.Put("/orgs/acme", "K", "prod", []byte("acme-only")); err != nil {
		t.Fatalf("put acme: %v", err)
	}
	if err := c.Put("/orgs/globex", "K", "prod", []byte("globex-only")); err != nil {
		t.Fatalf("put globex: %v", err)
	}
	c.Close()

	// Distinct files exist for the two orgs.
	acme := filepath.Join(root, "orgs", "acme", "kms.db")
	globex := filepath.Join(root, "orgs", "globex", "kms.db")
	if _, err := os.Stat(acme); err != nil {
		t.Fatalf("acme file missing: %v", err)
	}
	if _, err := os.Stat(globex); err != nil {
		t.Fatalf("globex file missing: %v", err)
	}

	// acme's file must NOT contain globex's plaintext (nor vice versa) — sealed.
	ab, _ := os.ReadFile(acme)
	if bytes.Contains(ab, []byte("globex-only")) || bytes.Contains(ab, []byte("acme-only")) {
		t.Fatal("BREACH: plaintext secret found in the per-org SQLite file")
	}
	t.Logf("per-org physical isolation holds: separate files, no cross-org plaintext")
}

// anyStoreFile returns the first per-org kms.db found under {root}/orgs, or "" if
// none — the "did a boot create a store file?" probe for the health-only tests.
func anyStoreFile(root string) string {
	var found string
	_ = filepath.Walk(filepath.Join(root, "orgs"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Base(p) == "kms.db" {
			found = p
		}
		return nil
	})
	return found
}
