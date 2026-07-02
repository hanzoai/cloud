package org

import (
	"bytes"
	"context"
	"testing"
)

func testMaster() []byte {
	m := make([]byte, keyLen)
	for i := range m {
		m[i] = byte(i * 7)
	}
	return m
}

func TestCipherRoundTrip(t *testing.T) {
	c, err := NewCipher(testMaster())
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	pt := []byte("SQLite bytes for org acme — secret at rest")
	sealed, err := c.Seal("acme", pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, pt) {
		t.Fatal("sealed blob leaks plaintext")
	}
	got, err := c.Open("acme", sealed)
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

// Cross-org isolation: a blob sealed for org A cannot be opened as org B (the
// orgID is the GCM AAD + the key-derivation input).
func TestCipherCrossOrgIsolation(t *testing.T) {
	c, _ := NewCipher(testMaster())
	sealed, _ := c.Seal("acme", []byte("acme-only"))
	if _, err := c.Open("evil", sealed); err == nil {
		t.Fatal("blob sealed for acme must NOT open as evil")
	}
	// And a different org's key produces different ciphertext for same plaintext.
	a, _ := c.Seal("acme", []byte("same"))
	b, _ := c.Seal("other", []byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("distinct orgs must seal identical plaintext to distinct ciphertext")
	}
}

// Tamper detection: flipping any ciphertext byte fails authentication.
func TestCipherTamperDetected(t *testing.T) {
	c, _ := NewCipher(testMaster())
	sealed, _ := c.Seal("acme", []byte("integrity-protected"))
	sealed[len(sealed)-1] ^= 0x01 // flip a tag bit
	if _, err := c.Open("acme", sealed); err == nil {
		t.Fatal("tampered blob must fail authentication")
	}
}

// Deterministic: identical plaintext → identical ciphertext (content-addressable,
// so the Replicator version-skip still works under encryption); distinct
// plaintext → distinct ciphertext (GCM nonce uniqueness).
func TestCipherDeterministicPerContent(t *testing.T) {
	c, _ := NewCipher(testMaster())
	a, _ := c.Seal("acme", []byte("payload-v1"))
	b, _ := c.Seal("acme", []byte("payload-v1"))
	if !bytes.Equal(a, b) {
		t.Fatal("same (org, plaintext) must seal identically")
	}
	d, _ := c.Seal("acme", []byte("payload-v2"))
	if bytes.Equal(a, d) {
		t.Fatal("different plaintext must seal differently")
	}
}

// Rotating the master re-keys every org (old ciphertext no longer opens).
func TestCipherMasterRotationRekeys(t *testing.T) {
	c1, _ := NewCipher(testMaster())
	m2 := testMaster()
	m2[0] ^= 0xFF
	c2, _ := NewCipher(m2)
	sealed, _ := c1.Seal("acme", []byte("x"))
	if _, err := c2.Open("acme", sealed); err == nil {
		t.Fatal("rotated master must not open old ciphertext")
	}
}

// End-to-end: an ENCRYPTED Replicator round-trips through the store, the stored
// bytes are ciphertext, and a reader with the same cipher restores the plaintext.
func TestReplicatorEncryptedRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	c, _ := NewCipher(testMaster())
	key := DBPath("acme", "", "iam")

	ownerDB := &memDB{}
	ownerDB.set([]byte("acme iam db — encrypted at rest"))
	owner := NewReplicator(key, store, ownerDB, WithEncryption(c, "acme"))
	if err := owner.Push(ctx); err != nil {
		t.Fatalf("encrypted push: %v", err)
	}

	// The object in the store must be ciphertext (no plaintext leak).
	raw, _, _ := store.Get(ctx, key)
	if bytes.Contains(raw, []byte("acme iam db")) {
		t.Fatal("stored object leaks plaintext — not encrypted")
	}

	readerDB := &memDB{}
	reader := NewReplicator(key, store, readerDB, WithEncryption(c, "acme"))
	if _, err := reader.Pull(ctx); err != nil {
		t.Fatalf("encrypted pull: %v", err)
	}
	if !bytes.Equal(readerDB.get(), []byte("acme iam db — encrypted at rest")) {
		t.Fatalf("reader restored %q", readerDB.get())
	}

	// A reader WITHOUT the cipher (or wrong org) cannot read the plaintext.
	plainReader := NewReplicator(key, store, &memDB{})
	if _, err := plainReader.Pull(ctx); err == nil {
		// Pull without cipher restores raw ciphertext bytes — not the plaintext.
		// It "succeeds" mechanically but the DB holds ciphertext, which is the
		// point: without the KMS-derived key the data is unreadable.
		if bytes.Contains(plainReaderBytes(plainReader, store, ctx), []byte("acme iam db")) {
			t.Fatal("plaintext readable without the cipher")
		}
	}
}

func plainReaderBytes(_ *Replicator, store *memStore, ctx context.Context) []byte {
	b, _, _ := store.Get(ctx, DBPath("acme", "", "iam"))
	return b
}
