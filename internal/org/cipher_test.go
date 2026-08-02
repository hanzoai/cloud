package org

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/vfs/replica"
)

func testMaster() []byte {
	m := make([]byte, keyLen)
	for i := range m {
		m[i] = byte(i * 7)
	}
	return m
}

// testNS is the namespace for an org slug — the same construction the production
// open path uses, so a test seals under a name a real request could produce.
func testNS(org string) namespace.Namespace { return namespace.MustOrgProject(org, "") }

func TestCipherRoundTrip(t *testing.T) {
	c, err := NewCipher(testMaster())
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	pt := []byte("SQLite bytes for org acme — secret at rest")
	sealed, err := c.Seal(testNS("acme"), "research", pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, pt) {
		t.Fatal("sealed blob leaks plaintext")
	}
	got, err := c.Open(testNS("acme"), "research", sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
}

func TestCipherRejectsWrongMasterLen(t *testing.T) {
	if _, err := NewCipher([]byte("too-short")); err == nil {
		t.Fatal("must reject non-32-byte master")
	}
}

// The snapshot key IS cek's, demonstrated rather than asserted: derive the key
// independently through cek.DeriveKey, open the blob with a hand-built AES-GCM
// under it, and get the plaintext back. If this package ever grows a second
// derivation again, this test stops passing.
func TestCipherKeyIsCEKDerived(t *testing.T) {
	master := testMaster()
	c, err := NewCipher(master)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	ns, subsystem := testNS("acme"), "research"
	pt := []byte("keyed by cek, not by a copy of cek")
	sealed, err := c.Seal(ns, subsystem, pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	key, err := cek.DeriveKey(master, ns, subsystem)
	if err != nil {
		t.Fatalf("cek.DeriveKey: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	got, err := gcm.Open(nil, sealed[:nonceLen], sealed[nonceLen:], []byte(ns.String()+"/"+subsystem))
	if err != nil {
		t.Fatalf("the cek-derived key does not open the sealed blob: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("cek-derived open returned %q", got)
	}
}

// Cross-org isolation: a blob sealed for org A cannot be opened as org B (the
// namespace is both the GCM AAD and the key-derivation input).
func TestCipherCrossOrgIsolation(t *testing.T) {
	c, _ := NewCipher(testMaster())
	sealed, _ := c.Seal(testNS("acme"), "research", []byte("acme-only"))
	if _, err := c.Open(testNS("evil"), "research", sealed); err == nil {
		t.Fatal("blob sealed for acme must NOT open as evil")
	}
	// And a different org's key produces different ciphertext for same plaintext.
	a, _ := c.Seal(testNS("acme"), "research", []byte("same"))
	b, _ := c.Seal(testNS("other"), "research", []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("distinct orgs must seal identical plaintext to distinct ciphertext")
	}
}

// Subsystem isolation — the property the org-slug-only derivation did NOT have:
// one org's two databases sealed under one key, so either snapshot opened the
// other. cek binds the subsystem, so they are separate.
func TestCipherSubsystemIsolation(t *testing.T) {
	c, _ := NewCipher(testMaster())
	ns := testNS("acme")
	sealed, _ := c.Seal(ns, "research", []byte("research rows"))
	if _, err := c.Open(ns, "ledger", sealed); err == nil {
		t.Fatal("a research snapshot must NOT open as the ledger")
	}
	a, _ := c.Seal(ns, "research", []byte("same"))
	b, _ := c.Seal(ns, "ledger", []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("distinct subsystems must seal identical plaintext to distinct ciphertext")
	}
}

// A project-scoped namespace is a different database from its org root, and is
// keyed as one.
func TestCipherProjectScopeIsolation(t *testing.T) {
	c, _ := NewCipher(testMaster())
	sealed, _ := c.Seal(namespace.MustOrgProject("acme", "web"), "research", []byte("project rows"))
	if _, err := c.Open(testNS("acme"), "research", sealed); err == nil {
		t.Fatal("a project snapshot must NOT open at the org root")
	}
}

// Tamper detection: flipping any ciphertext byte fails authentication.
func TestCipherTamperDetected(t *testing.T) {
	c, _ := NewCipher(testMaster())
	sealed, _ := c.Seal(testNS("acme"), "research", []byte("integrity-protected"))
	sealed[len(sealed)-1] ^= 0x01 // flip a tag bit
	if _, err := c.Open(testNS("acme"), "research", sealed); err == nil {
		t.Fatal("tampered blob must fail authentication")
	}
}

// Deterministic: identical plaintext → identical ciphertext (content-addressable,
// so the version-skip still works under encryption); distinct plaintext →
// distinct ciphertext (GCM nonce uniqueness).
func TestCipherDeterministicPerContent(t *testing.T) {
	c, _ := NewCipher(testMaster())
	ns := testNS("acme")
	a, _ := c.Seal(ns, "research", []byte("payload-v1"))
	b, _ := c.Seal(ns, "research", []byte("payload-v1"))
	if !bytes.Equal(a, b) {
		t.Fatal("same (namespace, subsystem, plaintext) must seal identically")
	}
	d, _ := c.Seal(ns, "research", []byte("payload-v2"))
	if bytes.Equal(a, d) {
		t.Fatal("different plaintext must seal differently")
	}
}

// Rotating the master re-keys every database (old ciphertext no longer opens).
func TestCipherMasterRotationRekeys(t *testing.T) {
	c1, _ := NewCipher(testMaster())
	m2 := testMaster()
	m2[0] ^= 0xFF
	c2, _ := NewCipher(m2)
	sealed, _ := c1.Seal(testNS("acme"), "research", []byte("x"))
	if _, err := c2.Open(testNS("acme"), "research", sealed); err == nil {
		t.Fatal("rotated master must not open old ciphertext")
	}
}

// End-to-end on the PRODUCTION path: a Durable wired with a Cipher ships
// ciphertext to the object store, and a successor restores the plaintext from
// it. This replaces a round-trip that went through replica.Replicator — a
// sealing route nothing in cloud used — so the property is proven where it ships.
func TestDurableShipsCiphertext(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	c, err := NewCipher(testMaster())
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	const orgID, subsystem = "acme", "research"
	dbKey := replica.DBPath(orgID, "", subsystem)

	dy := NewDurability(cs, stubView{id: "pod-0", set: []Member{{ID: "pod-0"}}}, c)
	pod := &durablePod{id: "pod-0", d: dy.For(testNS(orgID), subsystem, dbKey, filepath.Join(t.TempDir(), "research.db"))}
	if err := pod.d.Hydrate(ctx); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	pod.open(t)
	const marker = "plaintext-marker-must-not-appear"
	if _, err := pod.db.Exec(`INSERT INTO kv(k,v) VALUES('secret',?)`, marker); err != nil {
		t.Fatalf("insert: %v", err)
	}
	acked, err := pod.d.Sync(ctx)
	if err != nil || !acked {
		t.Fatalf("sync: acked=%v err=%v", acked, err)
	}

	raw, _, err := cs.Get(ctx, dbKey)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("nothing shipped")
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatal("durable object leaks plaintext — not encrypted at rest")
	}

	// A successor with the same master restores the row from that ciphertext.
	succ := &durablePod{id: "pod-1", d: NewDurability(cs, stubView{id: "pod-1", set: []Member{{ID: "pod-1"}}}, c).
		For(testNS(orgID), subsystem, dbKey, filepath.Join(t.TempDir(), "research.db"))}
	if err := succ.d.Hydrate(ctx); err != nil {
		t.Fatalf("successor hydrate: %v", err)
	}
	succ.open(t)
	var got string
	if err := succ.db.QueryRow(`SELECT v FROM kv WHERE k='secret'`).Scan(&got); err != nil {
		t.Fatalf("successor read: %v", err)
	}
	if got != marker {
		t.Fatalf("successor restored %q, want %q", got, marker)
	}
}
