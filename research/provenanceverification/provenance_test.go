// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package provenanceverification

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestProvenanceDocumentShape locks the deliverable structure: every
// required field is present and the canonical form excludes the
// signature.
func TestProvenanceDocumentShape(t *testing.T) {
	d := FixtureDocument()
	if d.Schema == "" || d.SubjectID == "" || d.SourceRevision == "" ||
		d.ToolchainDigest == "" || d.RuntimeDigest == "" || d.OutputDigest == "" ||
		d.TenantSalt == "" {
		t.Fatalf("fixture document has empty required fields: %+v", d)
	}
	// Canonical form is deterministic and excludes the signature.
	a := d.CanonicalBytes()
	d.Signature = []byte("forged")
	if !equal(d.CanonicalBytes(), a) {
		t.Fatal("canonical form must exclude the signature field")
	}
}

// TestSignThenVerifyPositive is the H3 positive path: a signed fixture
// verifies under its own public key, and the full digest-check chain
// passes for the matching artifact bytes.
func TestSignThenVerifyPositive(t *testing.T) {
	d := FixtureDocument()
	pub, priv := NewTestKeys()
	if err := d.Sign(priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := d.Verify(pub); err != nil {
		t.Fatalf("verify untampered document: %v", err)
	}
	artifact := []byte("fixture-artifact-bytes")
	if err := d.CheckArtifactDigest(artifact); err != nil {
		t.Fatalf("artifact digest check on the matching bytes: %v", err)
	}
	if err := d.CheckSourceRevision(d.SourceRevision); err != nil {
		t.Fatalf("source revision check: %v", err)
	}
	if err := d.CheckToolchain(d.ToolchainDigest); err != nil {
		t.Fatalf("toolchain check: %v", err)
	}
	if err := d.CheckRuntime(d.RuntimeDigest); err != nil {
		t.Fatalf("runtime check: %v", err)
	}
	if err := d.CheckTenant(d.TenantSalt); err != nil {
		t.Fatalf("tenant check: %v", err)
	}
}

// TestTamperEachFieldFails is H1: mutating any single signed field
// breaks the signature check with a named error.
func TestTamperEachFieldFails(t *testing.T) {
	cases := []struct {
		name string
		mut  func(d *ProvenanceDocument)
	}{
		{"source-revision", func(d *ProvenanceDocument) { d.SourceRevision = "0000000000000000000000000000000000000000" }},
		{"toolchain", func(d *ProvenanceDocument) { d.ToolchainDigest = SHA256Hex([]byte("tampered-toolchain")) }},
		{"runtime", func(d *ProvenanceDocument) { d.RuntimeDigest = SHA256Hex([]byte("tampered-runtime")) }},
		{"output-digest", func(d *ProvenanceDocument) { d.OutputDigest = SHA256Hex([]byte("tampered-output")) }},
		{"tenant-salt", func(d *ProvenanceDocument) { d.TenantSalt = "tenant-b" }},
		{"subject-id", func(d *ProvenanceDocument) { d.SubjectID = "agent-deliverable-9999" }},
		{"schema", func(d *ProvenanceDocument) { d.Schema = "slsa-provenance/v0.9" }},
	}
	for _, c := range cases {
		d := FixtureDocument()
		pub, priv := NewTestKeys()
		if err := d.Sign(priv); err != nil {
			t.Fatalf("%s: sign: %v", c.name, err)
		}
		c.mut(d)
		err := d.Verify(pub)
		if err == nil {
			t.Fatalf("%s: tampered document verified — H1 falsified", c.name)
		}
		if !strings.Contains(err.Error(), "signature mismatch") {
			t.Fatalf("%s: expected a signature mismatch, got: %v", c.name, err)
		}
	}
}

// TestUnauthorizedSubstitution is H2: replacing the artifact with
// different bytes while keeping the document's signature is rejected
// by the artifact-digest check. The document signature is still valid
// (the document was not edited); what fails is the artifact binding.
func TestUnauthorizedSubstitution(t *testing.T) {
	d := FixtureDocument()
	pub, priv := NewTestKeys()
	if err := d.Sign(priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	substituted := []byte("attacker-substituted-artifact")
	err := d.CheckArtifactDigest(substituted)
	if err == nil {
		t.Fatal("substituted artifact passed the digest check — H2 falsified")
	}
	// The document signature is still valid: the rejection is a
	// binding failure, not a tamper failure.
	if sigErr := d.Verify(pub); sigErr != nil {
		t.Fatalf("document signature must still be valid after substitution, got: %v", sigErr)
	}
}

// TestCrossTenantRejection: tenant B's key must reject tenant A's
// document; the correct key must reject a different tenant salt.
func TestCrossTenantRejection(t *testing.T) {
	da := FixtureDocument()
	da.TenantSalt = "tenant-a"
	db := FixtureDocument()
	db.TenantSalt = "tenant-b"
	pubA, privA := NewTestKeys()
	pubB, privB := NewTestKeys()
	if err := da.Sign(privA); err != nil {
		t.Fatal(err)
	}
	if err := db.Sign(privB); err != nil {
		t.Fatal(err)
	}
	if err := da.Verify(pubB); err == nil {
		t.Fatal("cross-tenant verify under a different key must fail")
	}
	_ = pubA
	dMixed := FixtureDocument()
	dMixed.TenantSalt = "tenant-a"
	if err := dMixed.Sign(privA); err != nil {
		t.Fatal(err)
	}
	if err := dMixed.CheckTenant("tenant-b"); err == nil {
		t.Fatal("CheckTenant must reject a different tenant salt")
	}
}

// TestVerifyRejectsMissingFields: structural errors are named.
func TestVerifyRejectsMissingFields(t *testing.T) {
	d := FixtureDocument()
	pub, priv := NewTestKeys()
	if err := d.Sign(priv); err != nil {
		t.Fatal(err)
	}
	d.Schema = ""
	if err := d.Verify(pub); err == nil {
		t.Fatal("missing schema must fail verification")
	}
	d2 := FixtureDocument()
	if err := d2.Sign(priv); err != nil {
		t.Fatal(err)
	}
	d2.Signature = nil
	if err := d2.Verify(pub); err == nil {
		t.Fatal("missing signature must fail verification")
	}
}

// TestJSONRoundtrip: the document survives a JSON marshal/unmarshal
// cycle with the signature intact (base64 transport) and still
// verifies.
func TestJSONRoundtrip(t *testing.T) {
	d := FixtureDocument()
	pub, priv := NewTestKeys()
	if err := d.Sign(priv); err != nil {
		t.Fatal(err)
	}
	b, err := d.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := UnmarshalDocument(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.OutputDigest != d.OutputDigest || back.SourceRevision != d.SourceRevision {
		t.Fatalf("round-trip mismatch: %+v vs %+v", back, d)
	}
	if err := back.Verify(pub); err != nil {
		t.Fatalf("round-trip verify: %v", err)
	}
}

// TestPEMKeyRoundtrip: PublicKeyFromPEM + MarshalPublicKeyPEM are
// inverses.
func TestPEMKeyRoundtrip(t *testing.T) {
	pub, _ := NewTestKeys()
	der, err := MarshalPublicKeyPEM(pub)
	if err != nil {
		t.Fatalf("marshal pem: %v", err)
	}
	back, err := PublicKeyFromPEM(der)
	if err != nil {
		t.Fatalf("unmarshal pem: %v", err)
	}
	if !equal(back, pub) {
		t.Fatal("PEM round-trip changed the public key")
	}
}

// TestPrivateKeyFromPEMParsesPKCS8: the private-key PEM helper accepts
// a valid PKCS#8 ed25519 key.
func TestPrivateKeyFromPEMParsesPKCS8(t *testing.T) {
	_, priv := NewTestKeys()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemBytes := []byte(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	back, err := PrivateKeyFromPEM(pemBytes)
	if err != nil {
		t.Fatalf("private key pem parse: %v", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(back.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatalf("re-marshal public: %v", err)
	}
	_, err = x509.ParsePKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("public key round-trip: %v", err)
	}
}

// TestSHA256HexStable: the digest helper is deterministic.
func TestSHA256HexStable(t *testing.T) {
	a := SHA256Hex([]byte("same-input"))
	if a != SHA256Hex([]byte("same-input")) {
		t.Fatal("SHA256Hex is not deterministic")
	}
	if SHA256Hex([]byte("different")) == a {
		t.Fatal("SHA256Hex must distinguish different inputs")
	}
}

// TestFixtureDocumentCommitted: the committed fixture (results/fixtures/
// pub.pem) parses and has the right shape.
func TestFixtureDocumentCommitted(t *testing.T) {
	pemBytes, err := os.ReadFile(filepath.Join("results", "fixtures", "pub.pem"))
	if err != nil {
		t.Skipf("fixture not present in this checkout: %v", err)
	}
	pub, err := PublicKeyFromPEM(pemBytes)
	if err != nil {
		t.Fatalf("committed pub.pem failed to parse: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("committed pub.pem is not a 32-byte ed25519 key: %d bytes", len(pub))
	}
}

// TestConcurrentVerify: the verifier is read-only over the document and
// safe for parallel use.
func TestConcurrentVerify(t *testing.T) {
	d := FixtureDocument()
	pub, priv := NewTestKeys()
	if err := d.Sign(priv); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.Verify(pub); err != nil {
				t.Errorf("concurrent verify: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestVerifierLatencyH3: a single verify+digest-check pass must
// complete in under 100 ms per op on any CI host (H3's overhead bound).
func TestVerifierLatencyH3(t *testing.T) {
	d := FixtureDocument()
	pub, priv := NewTestKeys()
	if err := d.Sign(priv); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("fixture-artifact-bytes")
	for i := 0; i < 200; i++ {
		_ = d.Verify(pub)
		_ = d.CheckArtifactDigest(artifact)
	}
	const iters = 2000
	start := time.Now()
	for i := 0; i < iters; i++ {
		_ = d.Verify(pub)
		_ = d.CheckArtifactDigest(artifact)
	}
	elapsed := time.Since(start)
	perOp := elapsed / time.Duration(iters)
	if perOp > 100*time.Millisecond {
		t.Fatalf("verify+digest took %s/op — H3's 100ms bound exceeded", perOp)
	}
}

// TestTamperSignatureDirectly: flipping one byte of the stored
// signature must fail verification.
func TestTamperSignatureDirectly(t *testing.T) {
	d := FixtureDocument()
	pub, priv := NewTestKeys()
	if err := d.Sign(priv); err != nil {
		t.Fatal(err)
	}
	d.Signature[0] ^= 0xFF
	if err := d.Verify(pub); err == nil {
		t.Fatal("flipped signature byte must fail verification")
	}
}

// TestWrongKeyVerification: a document signed by key A must not
// verify under key B.
func TestWrongKeyVerification(t *testing.T) {
	d := FixtureDocument()
	pubA, privA := NewTestKeys()
	pubB, _ := NewTestKeys()
	if err := d.Sign(privA); err != nil {
		t.Fatal(err)
	}
	if err := d.Verify(pubB); err == nil {
		t.Fatal("verification under a different key must fail")
	}
	_ = pubA
}

// TestMultipleOccurrencesCoexist: two tenants with the same fixture
// content but different salts produce distinct canonical forms and
// cannot verify each other's documents (multi-tenant isolation).
func TestMultipleOccurrencesCoexist(t *testing.T) {
	da := FixtureDocument()
	da.TenantSalt = "tenant-a"
	db := FixtureDocument()
	db.TenantSalt = "tenant-b"
	pubA, privA := NewTestKeys()
	pubB, privB := NewTestKeys()
	if err := da.Sign(privA); err != nil {
		t.Fatal(err)
	}
	if err := db.Sign(privB); err != nil {
		t.Fatal(err)
	}
	if equal(da.CanonicalBytes(), db.CanonicalBytes()) {
		t.Fatal("different tenant salts must produce different canonical forms")
	}
	if err := da.Verify(pubB); err == nil {
		t.Fatal("tenant B's key must not verify tenant A's document")
	}
	_ = pubA
}
