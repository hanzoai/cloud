// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package provenanceverification is the research harness for hanzoai/cloud#39
// ([Bounty #49], verifiable agent-produced artifacts, SLSA v1.2 + Cosign,
// HIP-1348): a dependency-free in-process provenance document model with
// Ed25519 signing, verification, and the two rejection classes required by
// the acceptance criterion (single-field tamper, unauthorized substitution).
//
// Standard library only; no changes to go.mod. The cosign binary is the
// upstream baseline, not imported code; results/PROTOCOL.md documents the
// cross-check protocol for any host that can run it.
package provenanceverification

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// ProvenanceDocument is the SLSA v1.2-style attestation for one agent
// produced deliverable. It binds the source revision, the toolchain and
// runtime digests, and the output artifact digest into a single signed
// record. The document is the *verification unit*: a verifier checks the
// signature over the canonical bytes, then checks each digest against the
// artifact it is meant to cover.
type ProvenanceDocument struct {
	// Schema is the SLSA provenance schema version this document targets.
	Schema string `json:"schema"`
	// SubjectID identifies the output artifact this document attests to
	// (typically the artifact's content digest).
	SubjectID string `json:"subject_id"`
	// SourceRevision is the exact source commit the build started from.
	SourceRevision string `json:"source_revision"`
	// ToolchainDigest is the SHA-256 of the toolchain manifest
	// (compiler, libraries, flags) the build used.
	ToolchainDigest string `json:"toolchain_digest"`
	// RuntimeDigest is the SHA-256 of the runtime environment manifest
	// (OS, kernel, Hanzo runtime identifier per HIP §1.3).
	RuntimeDigest string `json:"runtime_digest"`
	// OutputDigest is the SHA-256 of the produced artifact.
	OutputDigest string `json:"output_digest"`
	// TenantSalt isolates multi-tenant provenance: two tenants building
	// the same source+toolchain produce different documents.
	TenantSalt string `json:"tenant_salt"`
	// Signature is the Ed25519 signature over CanonicalBytes().
	Signature []byte `json:"signature"`
}

// CanonicalBytes renders the document for signing/verification: all
// fields except the signature, joined in a fixed order with a unit
// separator, then SHA-256 hashed. The hash is what the signer signs;
// a verifier recomputes it from the (possibly tampered) document and
// checks the signature before checking individual digests.
func (d *ProvenanceDocument) CanonicalBytes() []byte {
	// The signature is the only field excluded from the canonical form.
	// The join order is fixed and documented so cross-signer
	// compatibility (cosign vs in-process) is a matter of matching
	// this exact serialization, not a matter of JSON key order.
	var b strings.Builder
	b.WriteString(d.Schema)
	b.WriteByte(0)
	b.WriteString(d.SubjectID)
	b.WriteByte(0)
	b.WriteString(d.SourceRevision)
	b.WriteByte(0)
	b.WriteString(d.ToolchainDigest)
	b.WriteByte(0)
	b.WriteString(d.RuntimeDigest)
	b.WriteByte(0)
	b.WriteString(d.OutputDigest)
	b.WriteByte(0)
	b.WriteString(d.TenantSalt)
	sum := sha256.Sum256([]byte(b.String()))
	return sum[:]
}

// Sign computes the Ed25519 signature over the document's canonical form
// and stores it on the document. The signer is stateless: any key can
// sign any document, and the verifier only needs the public key.
func (d *ProvenanceDocument) Sign(priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("provenanceverification: invalid private key length %d", len(priv))
	}
	d.Signature = ed25519.Sign(priv, d.CanonicalBytes())
	return nil
}

// Verify checks the Ed25519 signature over the document's canonical form.
// It returns nil only when the signature is valid AND the document
// structure is intact (no zero-length required fields). It does NOT
// check the individual digests against the actual artifacts — that is
// the caller's job, and the rejection tests cover it explicitly.
func (d *ProvenanceDocument) Verify(pub ed25519.PublicKey) error {
	if d == nil {
		return errors.New("provenanceverification: nil document")
	}
	if d.Schema == "" {
		return errors.New("provenanceverification: missing schema")
	}
	if d.SubjectID == "" {
		return errors.New("provenanceverification: missing subject_id")
	}
	if d.SourceRevision == "" {
		return errors.New("provenanceverification: missing source_revision")
	}
	if d.ToolchainDigest == "" {
		return errors.New("provenanceverification: missing toolchain_digest")
	}
	if d.RuntimeDigest == "" {
		return errors.New("provenanceverification: missing runtime_digest")
	}
	if d.OutputDigest == "" {
		return errors.New("provenanceverification: missing output_digest")
	}
	if d.TenantSalt == "" {
		return errors.New("provenanceverification: missing tenant_salt")
	}
	if len(d.Signature) == 0 {
		return errors.New("provenanceverification: missing signature")
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("provenanceverification: invalid public key length %d", len(pub))
	}
	if !ed25519.Verify(pub, d.CanonicalBytes(), d.Signature) {
		return errors.New("provenanceverification: signature mismatch")
	}
	return nil
}

// CheckArtifactDigest verifies that the artifact's actual digest matches
// the document's OutputDigest. This is the *substitution* check: a
// verifier who has the artifact in hand must confirm the document was
// produced for THIS artifact, not a different one that reuses the
// same signature.
func (d *ProvenanceDocument) CheckArtifactDigest(actual []byte) error {
	sum := sha256.Sum256(actual)
	want := fmt.Sprintf("%x", sum)
	if d.OutputDigest != want {
		return fmt.Errorf("provenanceverification: artifact digest mismatch (document %s, actual %s)", d.OutputDigest, want)
	}
	return nil
}

// CheckSourceRevision verifies that the document's SourceRevision matches
// the revision the verifier expects the artifact to have been built from.
// A mismatch is a tamper signal (the document was edited after signing).
func (d *ProvenanceDocument) CheckSourceRevision(expected string) error {
	if expected == "" {
		return errors.New("provenanceverification: expected revision is empty")
	}
	if d.SourceRevision != expected {
		return fmt.Errorf("provenanceverification: source revision mismatch (document %s, expected %s)", d.SourceRevision, expected)
	}
	return nil
}

// CheckToolchain verifies the toolchain digest against an expected value.
func (d *ProvenanceDocument) CheckToolchain(expected string) error {
	if expected == "" {
		return errors.New("provenanceverification: expected toolchain digest is empty")
	}
	if d.ToolchainDigest != expected {
		return fmt.Errorf("provenanceverification: toolchain digest mismatch (document %s, expected %s)", d.ToolchainDigest, expected)
	}
	return nil
}

// CheckRuntime verifies the runtime digest against an expected value.
func (d *ProvenanceDocument) CheckRuntime(expected string) error {
	if expected == "" {
		return errors.New("provenanceverification: expected runtime digest is empty")
	}
	if d.RuntimeDigest != expected {
		return fmt.Errorf("provenanceverification: runtime digest mismatch (document %s, expected %s)", d.RuntimeDigest, expected)
	}
	return nil
}

// CheckTenant verifies the document belongs to the calling tenant.
// Cross-tenant verification is a rejection class: tenant A's document
// must not verify under tenant B's salt even if every other field
// matches (the canonical form includes TenantSalt).
func (d *ProvenanceDocument) CheckTenant(expected string) error {
	if expected == "" {
		return errors.New("provenanceverification: expected tenant salt is empty")
	}
	if d.TenantSalt != expected {
		return fmt.Errorf("provenanceverification: tenant salt mismatch (document %s, expected %s)", d.TenantSalt, expected)
	}
	return nil
}

// NewTestKeys returns a fresh Ed25519 keypair for tests and the
// fixture. The public key is what the verifier holds; the private key
// is what the signer holds. Neither is committed to the repo.
func NewTestKeys() (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(fmt.Sprintf("provenanceverification: ed25519 keygen: %v", err))
	}
	return pub, priv
}

// FixtureDocument builds a deterministic ProvenanceDocument for the
// acceptance tests. The digest fields are pre-computed SHA-256 values
// over the fixture payloads (see provenanceharness fixtures). The
// signature is left empty; callers invoke Sign before verifying.
func FixtureDocument() *ProvenanceDocument {
	return &ProvenanceDocument{
		Schema:          "slsa-provenance/v1.2",
		SubjectID:       "agent-deliverable-0001",
		SourceRevision:  "f4b2a1c9d8e7f6051423344556677889900aabb1",
		ToolchainDigest: SHA256Hex([]byte("go1.23.4 + stdlib-only + no CGO")),
		RuntimeDigest:   SHA256Hex([]byte("hanzo-runtime-2026.11")),
		OutputDigest:    SHA256Hex([]byte("fixture-artifact-bytes")),
		TenantSalt:      "tenant-a",
	}
}

// SHA256Hex is the public helper for computing the digest fields.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

// Marshal / Unmarshal round-trip the document through JSON. The
// signature is carried as a base64-friendly []byte (JSON encodes it
// as a base64 string automatically).
func (d *ProvenanceDocument) Marshal() ([]byte, error) {
	return json.Marshal(d)
}

func UnmarshalDocument(b []byte) (*ProvenanceDocument, error) {
	var d ProvenanceDocument
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("provenanceverification: unmarshal: %w", err)
	}
	return &d, nil
}

// PublicKeyFromPEM decodes a PEM-encoded ed25519 public key. The
// fixture key in results/fixtures/ is PEM so it is human-readable and
// diff-able; the harness signs with the matching private key.
func PublicKeyFromPEM(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("provenanceverification: no PEM block found")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("provenanceverification: parse pkix: %w", err)
	}
	ed, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("provenanceverification: key is not Ed25519")
	}
	return ed, nil
}

// PrivateKeyFromPEM decodes a PEM-encoded ed25519 private key
// (PKCS#8). The fixture private key is NOT committed; this helper
// exists for the harness's "load your own key" path.
func PrivateKeyFromPEM(pemBytes []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("provenanceverification: no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("provenanceverification: parse pkcs8: %w", err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("provenanceverification: key is not Ed25519")
	}
	return ed, nil
}

// MarshalPublicKeyPEM encodes an ed25519 public key as PEM for the
// fixture file. The harness uses this to write results/fixtures/pub.pem.
func MarshalPublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("provenanceverification: marshal pkix: %w", err)
	}
	return []byte(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// Base64Signature encodes a signature for JSON-safe transport (the
// ProvenanceDocument.Signature field is []byte; JSON marshals it as
// a base64 string automatically, but this helper is for the CLI).
func Base64Signature(sig []byte) string {
	return base64.StdEncoding.EncodeToString(sig)
}
