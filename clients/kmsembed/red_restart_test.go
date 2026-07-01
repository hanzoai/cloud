package kmsembed

// RED tests for the embedded KMS client internals: encryption-key restart
// semantics (rotation / downgrade), Sign fail-closed, and master-key non-leakage.
// White-box (same package) so we can drive New/Get/Put with explicit keys.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
)

func b64key(t *testing.T, fill byte) string {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = fill
	}
	return base64.StdEncoding.EncodeToString(k)
}

func randB64Key(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

// VECTOR 8a: restart with a DIFFERENT master key (rotation without re-encrypt).
// zapdb's KEYREGISTRY sanity-text check must make badger.Open FAIL — so the
// store does NOT silently open with a wrong key (which would corrupt/lose data
// or, worse, appear to work). We assert New returns an error → pickKMSClient
// falls back to DisabledKMS (fail-closed). Proof it is not a silent downgrade.
func TestVector8a_RestartWrongKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()

	// Boot 1: key A, write a secret.
	c1, err := New(Config{DataDir: dir, MasterKeyB64: b64key(t, 0xAA)}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	if err := c1.Put("/orgs/x", "K", "default", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("close1: %v", err)
	}

	// Boot 2: key B (rotation, store still encrypted with A's datakey registry).
	// zapdb sanity check should reject B → New errors.
	c2, err := New(Config{DataDir: dir, MasterKeyB64: b64key(t, 0xBB)}, log)
	if err == nil {
		// If it did NOT error, prove it at least cannot read A's secret with B.
		defer c2.Close()
		_, gErr := c2.Get("/orgs/x", "K", "default")
		t.Fatalf("SILENT-DOWNGRADE RISK: reopened store with WRONG key without error "+
			"(Get err=%v). Expected badger.Open to fail on KEYREGISTRY sanity mismatch.", gErr)
	}
	t.Logf("rotation w/o re-encrypt fails closed: New(wrong key) → %v", err)
}

// VECTOR 8b: store first created ENCRYPTED (key A), then restarted with NO key
// (health-only). zapdb must reject the plaintext-open of an encrypted registry —
// so you cannot silently DOWNGRADE an encrypted store to unencrypted. New should
// error (→ DisabledKMS), never open an encrypted store as plaintext.
func TestVector8b_EncryptedThenNoKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()

	c1, err := New(Config{DataDir: dir, MasterKeyB64: b64key(t, 0xCC)}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	_ = c1.Put("/orgs/x", "K", "default", []byte("v"))
	c1.Close()

	// Boot 2: NO key. Store dir already has an encrypted KEYREGISTRY.
	c2, err := New(Config{DataDir: dir, MasterKeyB64: ""}, log)
	if err == nil {
		defer c2.Close()
		if c2.Ready() {
			t.Fatalf("BREACH: encrypted store reopened in READY mode with no key")
		}
		// It opened but is health-only. Is that a silent downgrade of the encrypted KV?
		t.Fatalf("SILENT-DOWNGRADE RISK: encrypted store reopened WITHOUT key did not error "+
			"(Ready=%v). Expected badger.Open to fail decrypting the KEYREGISTRY.", c2.Ready())
	}
	t.Logf("encrypted→no-key fails closed: New(no key on encrypted dir) → %v", err)
}

// VECTOR 8c: the FOOT-GUN — store first created in HEALTH-ONLY (no key, plaintext
// KEYREGISTRY), then the operator injects the real key on the next boot. Does
// badger.Open reject the now-mismatched (plaintext) registry, bricking the store
// until wiped? This is the availability trap Blue flagged as untested.
func TestVector8c_HealthOnlyThenKeyBricks(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()

	// Boot 1: NO key → health-only, plaintext KEYREGISTRY created.
	c1, err := New(Config{DataDir: dir, MasterKeyB64: ""}, log)
	if err != nil {
		t.Fatalf("boot1 (health-only): %v", err)
	}
	if c1.Ready() {
		t.Fatal("health-only client should not be Ready")
	}
	c1.Close()

	// Boot 2: operator injects the real key. Plaintext registry now mismatches.
	c2, err := New(Config{DataDir: dir, MasterKeyB64: b64key(t, 0xDD)}, log)
	if err != nil {
		t.Logf("FOOT-GUN CONFIRMED: after a health-only boot, injecting the real key BRICKS "+
			"the store (New → %v). Operator must wipe {DataDir}/kms before first real boot, "+
			"or the KMS never comes up. Availability trap, not a data-confidentiality breach.", err)
		return
	}
	// If it opened, is it usable (Ready + roundtrip)?
	defer c2.Close()
	if !c2.Ready() {
		t.Fatalf("after health-only→key boot: client not Ready (err=nil but unusable)")
	}
	if err := c2.Put("/orgs/x", "K", "default", []byte("v")); err != nil {
		t.Fatalf("FOOT-GUN: Ready but Put fails after health-only→key transition: %v", err)
	}
	got, err := c2.Get("/orgs/x", "K", "default")
	if err != nil || string(got) != "v" {
		t.Fatalf("FOOT-GUN: roundtrip broken after health-only→key transition: got=%q err=%v", got, err)
	}
	t.Logf("health-only→key transition is CLEAN: store upgrades to encrypted and roundtrips OK")
}

// VECTOR 8d: clean restart with the SAME key must preserve + decrypt secrets
// (no data loss, sane path). The happy-path baseline for the above.
func TestVector8d_SameKeyRestartRoundtrip(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.NewNoOpLogger()
	key := randB64Key(t)

	c1, err := New(Config{DataDir: dir, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("boot1: %v", err)
	}
	if err := c1.Put("/orgs/x", "K", "prod", []byte("persist-me")); err != nil {
		t.Fatalf("put: %v", err)
	}
	c1.Close()

	c2, err := New(Config{DataDir: dir, MasterKeyB64: key}, log)
	if err != nil {
		t.Fatalf("boot2 (same key): %v", err)
	}
	defer c2.Close()
	got, err := c2.Get("/orgs/x", "K", "prod")
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if string(got) != "persist-me" {
		t.Fatalf("data loss/corruption: got %q, want persist-me", got)
	}
}

// VECTOR 6: Sign must NEVER return a non-nil signature. Exhaustively drive both
// the unconfigured and the "configured but not co-hosted" branches.
func TestVector6_SignNeverFabricates(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	ctx := context.Background()

	// Unconfigured MPC.
	c1, _ := New(Config{DataDir: t.TempDir(), MasterKeyB64: randB64Key(t)}, log)
	defer c1.Close()
	if sig, err := c1.Sign(ctx, "k", []byte("p")); sig != nil || err == nil {
		t.Fatalf("BREACH: Sign(no MPC) sig=%v err=%v — must be (nil, ErrSignUnavailable)", sig, err)
	}

	// MPC "configured" (addr+vault set) but co-hosting is out of scope: still nil sig.
	c2, _ := New(Config{DataDir: t.TempDir(), MasterKeyB64: randB64Key(t),
		MPCAddr: "mpc.internal:9000", MPCVaultID: "v1"}, log)
	defer c2.Close()
	if !c2.SigningConfigured() {
		t.Fatal("expected SigningConfigured true with addr+vault set")
	}
	sig, err := c2.Sign(ctx, "k", []byte("p"))
	if sig != nil {
		t.Fatalf("BREACH: Sign(MPC configured) returned a %d-byte sig — must never fabricate", len(sig))
	}
	if err == nil {
		t.Fatal("BREACH: Sign(MPC configured) returned nil error — must fail closed until real RPC wired")
	}
	t.Logf("Sign fails closed in both branches: unconfigured=%v, configured-not-cohosted=%v",
		"(nil, ErrSignUnavailable)", err)
}

// VECTOR 5: master key never appears in any error surfaced by Get/Put/Open on a
// corrupt record. Feed a tampered record through Open indirectly by storing then
// corrupting is store-level; here we assert the New warn path + Get/Put errors do
// not echo key bytes. (Log capture is covered by inspection; this asserts the
// error strings are key-free.)
func TestVector5_ErrorsNeverEchoKey(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = 0xEE
	}
	b64 := base64.StdEncoding.EncodeToString(rawKey)
	c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: b64}, log)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()

	// Get a missing secret — the error must not contain key material.
	_, gErr := c.Get("/orgs/x", "MISSING", "default")
	if gErr != nil && (strings.Contains(gErr.Error(), b64) || strings.Contains(gErr.Error(), string(rawKey))) {
		t.Fatalf("BREACH: Get error leaked key: %v", gErr)
	}
	// Health-only error path.
	hc, _ := New(Config{DataDir: t.TempDir(), MasterKeyB64: ""}, log)
	defer hc.Close()
	if _, e := hc.Get("/orgs/x", "K", "default"); e != nil {
		if strings.Contains(e.Error(), "AA") || strings.Contains(e.Error(), b64) {
			t.Fatalf("BREACH: health-only error leaked key material: %v", e)
		}
	}
	t.Logf("errors are key-free")
}

// VECTOR 7-facade: the programmatic KMSClient facade (deps.KMS.PutSecret/GetSecret,
// used by OTHER subsystems) goes through parseRef, which does NO validSegment
// check. So an in-process caller can store a secret whose name/path/env contain
// NUL or control chars — the HTTP boundary's validation does NOT protect the
// facade. Confirm the asymmetry: the store accepts what the REST layer rejects.
// (In-process callers are trusted, so this is a consistency/robustness gap, not a
// remote breach — but a malicious/confused ref from a caller that forwards
// user-influenced strings into a KMS ref would key a malformed record.)
func TestVector7Facade_ParseRefNoValidation(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: randB64Key(t)}, log)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	// A ref carrying a NUL and control chars in the name — REST would 400 this.
	badRef := "svc/EVIL\x00NAME@pr\x01od"
	if err := c.PutSecret(ctx, badRef, []byte("stored-via-facade")); err != nil {
		t.Logf("facade rejected bad ref (good, validation added): %v", err)
		return
	}
	got, err := c.GetSecret(ctx, badRef)
	if err != nil {
		t.Fatalf("roundtrip bad ref: %v", err)
	}
	if string(got) != "stored-via-facade" {
		t.Fatalf("facade roundtrip mismatch: %q", got)
	}
	t.Logf("ASYMMETRY CONFIRMED: parseRef/Put accepted a NUL+control-char ref the REST "+
		"boundary rejects (%q). deps.KMS callers bypass validSegment. Robustness gap, "+
		"not a remote breach (in-process callers are trusted).", badRef)
}

// VECTOR emptyRef: a facade Put with an empty ref keys (path="/", name="", env).
// Does it store a nameless secret? Prove what an empty programmatic ref does.
func TestVectorEmptyRefFacade(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	c, _ := New(Config{DataDir: t.TempDir(), MasterKeyB64: randB64Key(t)}, log)
	defer c.Close()
	ctx := context.Background()
	err := c.PutSecret(ctx, "", []byte("nameless"))
	t.Logf("PutSecret(ref=\"\") → err=%v (keys path=/ name=\"\" env=default)", err)
	if err == nil {
		got, gErr := c.GetSecret(ctx, "")
		t.Logf("GetSecret(ref=\"\") → %q err=%v", got, gErr)
	}
}
