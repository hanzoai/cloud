// SPDX-License-Identifier: Apache-2.0

package provenance

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type testFixture struct {
	now          time.Time
	ca           *EphemeralCA
	tlogKey      *KeyPair
	tlog         *TransparencyLog
	signer       *KeyPair
	certPEM      string
	certSerial   string
	artifactName string
	artifactData []byte
	artifactHash string
	stmt         *Statement
	bundle       *Bundle
	policy       *VerificationPolicy
}

func newTestFixture(t *testing.T, alg string) *testFixture {
	t.Helper()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	ca, err := NewEphemeralCA("Test Hanzo Ephemeral CA", now)
	if err != nil {
		t.Fatalf("NewEphemeralCA: %v", err)
	}
	tlogKey, err := GenerateEd25519KeyPair()
	if err != nil {
		t.Fatalf("GenerateEd25519KeyPair(tlog): %v", err)
	}
	tlog, err := NewTransparencyLog(DefaultTLogOrigin, tlogKey)
	if err != nil {
		t.Fatalf("NewTransparencyLog: %v", err)
	}

	var signer *KeyPair
	switch alg {
	case AlgorithmECDSAP256:
		signer, err = GenerateECDSAP256KeyPair()
	default:
		signer, err = GenerateEd25519KeyPair()
	}
	if err != nil {
		t.Fatalf("GenerateKeyPair(%s): %v", alg, err)
	}

	identity := "https://hanzo.ai/principals/hanzo/kai-builder"
	sourceURI := "git+https://github.com/hanzoai/cloud"
	commit := "0f31254abcdef0123456789abcdef0123456789a"
	tree := "9876543210fedcba9876543210fedcba98765432"
	builderID := "https://hanzo.ai/forge/runners/linux-amd64-v1"
	goDigest := DigestBytes([]byte("go1.27.1-linux-amd64-toolchain"))

	cert, certPEM, err := ca.IssueCodeSigningCert(signer.PublicKey(), OIDCIdentity{
		Subject:                identity,
		Issuer:                 DefaultOIDCIssuer,
		BuildSignerURI:         builderID,
		SourceRepositoryURI:    sourceURI,
		SourceRepositoryDigest: commit,
	}, now.Add(-1*time.Minute), 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueCodeSigningCert: %v", err)
	}

	artifactData := []byte("ELF-clean-room-hanzo-cloud-binary-v1.0.0")
	artifactHash := DigestBytes(artifactData)

	stmt, err := NewStatement(
		[]ResourceDescriptor{
			{
				Name:      "cloud-linux-amd64",
				MediaType: "application/octet-stream",
				Digest:    DigestSet{"sha256": artifactHash},
			},
		},
		BuildDefinition{
			BuildType: DefaultBuildType,
			ExternalParameters: ExternalParameters{
				Source: SourceRef{
					URI:    sourceURI,
					Branch: "refs/heads/main",
					Commit: commit,
					Tree:   tree,
				},
				BuilderIdentity: identity,
				Toolchain: []ToolchainComponent{
					{
						Name:    "go",
						Version: "1.27.1",
						Digest:  DigestSet{"sha256": goDigest},
					},
				},
				InvocationOrg: "hanzo",
			},
		},
		RunDetails{
			Builder: Builder{
				ID:      builderID,
				Version: map[string]string{"forge": "1.2.0"},
			},
			Metadata: BuildMetadata{
				InvocationID: "inv-20261126-0001",
				StartedOn:    now.Add(-5 * time.Second),
				FinishedOn:   now,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewStatement: %v", err)
	}

	bundle, err := AttestStatement(stmt, AttestOptions{
		ID:      "prov_test_0001",
		Org:     "hanzo",
		Signer:  signer,
		CertPEM: certPEM,
		TLog:    tlog,
		Now:     now,
	})
	if err != nil {
		t.Fatalf("AttestStatement: %v", err)
	}

	policy := &VerificationPolicy{
		TrustedCertRoots:   ca.CertPool(),
		AllowedOIDCIssuers: []string{DefaultOIDCIssuer},
		AllowedIdentities:  []string{identity},
		AllowedBuilders:    []string{builderID},
		AllowedSourceURIs:  []string{sourceURI},
		RequiredBranch:     "refs/heads/main",
		RequiredCommit:     commit,
		RequiredTree:       tree,
		RequiredToolchainDigests: map[string]string{
			"go": goDigest,
		},
		TLogPublicKey: tlog.PublicKey(),
		TLogOrigin:    tlog.Origin(),
	}

	return &testFixture{
		now:          now,
		ca:           ca,
		tlogKey:      tlogKey,
		tlog:         tlog,
		signer:       signer,
		certPEM:      certPEM,
		certSerial:   strings.ToLower(cert.SerialNumber.Text(16)),
		artifactName: "cloud-linux-amd64",
		artifactData: artifactData,
		artifactHash: artifactHash,
		stmt:         stmt,
		bundle:       bundle,
		policy:       policy,
	}
}

func TestCanonicalPAEVector(t *testing.T) {
	got := PAE("http://example.com/HelloWorld", []byte("hello world"))
	want := []byte("DSSEv1 29 http://example.com/HelloWorld 11 hello world")
	if !bytes.Equal(got, want) {
		t.Fatalf("PAE mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestVerifyBundleHappyPaths(t *testing.T) {
	for _, alg := range []string{AlgorithmEd25519, AlgorithmECDSAP256} {
		t.Run("keyless_"+alg, func(t *testing.T) {
			f := newTestFixture(t, alg)
			res, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, f.policy)
			if err != nil {
				t.Fatalf("VerifyBundle(%s) failed: %v", alg, err)
			}
			if !res.Verified || res.ArtifactDigest != f.artifactHash || res.ArtifactName != f.artifactName {
				t.Fatalf("unexpected verification result: %+v", res)
			}
			if res.CertSerial != f.certSerial || res.OIDCIssuer != DefaultOIDCIssuer {
				t.Fatalf("expected cert claims populated, got %+v", res)
			}
		})
	}

	t.Run("direct_pinned_key", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		bundle, err := AttestStatement(f.stmt, AttestOptions{
			ID:     "prov_direct",
			Org:    "hanzo",
			Signer: f.signer,
			TLog:   f.tlog,
			Now:    f.now.Add(time.Second),
		})
		if err != nil {
			t.Fatalf("AttestStatement(direct): %v", err)
		}
		pol := f.policy.Clone()
		pol.TrustedCertRoots = nil
		pol.TrustedKeys = map[string]crypto.PublicKey{f.signer.KeyID(): f.signer.PublicKey()}
		res, err := VerifyBundle(f.artifactHash, f.artifactName, bundle, nil, pol)
		if err != nil {
			t.Fatalf("VerifyBundle(direct) failed: %v", err)
		}
		if !res.Verified || res.SignerKeyID != f.signer.KeyID() {
			t.Fatalf("unexpected result: %+v", res)
		}
	})
}

// TestAdversarialRefusals verifies all 8+ required attack vectors are refused fail-closed
// with exact sentinel error classifications.
func TestAdversarialRefusals(t *testing.T) {
	t.Run("1_artifact_1bit_tamper", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		tampered := append([]byte(nil), f.artifactData...)
		tampered[len(tampered)-1] ^= 0x01 // single-bit flip
		tamperedHash := DigestBytes(tampered)

		_, err := VerifyBundle(tamperedHash, f.artifactName, f.bundle, nil, f.policy)
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("want ErrDigestMismatch on 1-bit flip, got: %v", err)
		}
	})

	t.Run("2a_unauthorized_signer_key_substitution", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		rogueKey, err := GenerateEd25519KeyPair()
		if err != nil {
			t.Fatal(err)
		}
		rogueBundle, err := AttestStatement(f.stmt, AttestOptions{
			Signer: rogueKey,
			TLog:   f.tlog,
			Now:    f.now.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		pol := f.policy.Clone()
		pol.TrustedKeys = map[string]crypto.PublicKey{f.signer.KeyID(): f.signer.PublicKey()}
		_, err = VerifyBundle(f.artifactHash, f.artifactName, rogueBundle, nil, pol)
		if !errors.Is(err, ErrUntrustedSigner) {
			t.Fatalf("want ErrUntrustedSigner for rogue key, got: %v", err)
		}
	})

	t.Run("2b_unauthorized_san_identity_substitution", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		rogueKey, _ := GenerateEd25519KeyPair()
		rogueSAN := "https://hanzo.ai/principals/attacker/rogue-agent"
		_, rogueCertPEM, err := f.ca.IssueCodeSigningCert(rogueKey.PublicKey(), OIDCIdentity{
			Subject:                rogueSAN,
			Issuer:                 DefaultOIDCIssuer,
			SourceRepositoryURI:    f.stmt.Predicate.BuildDefinition.ExternalParameters.Source.URI,
			SourceRepositoryDigest: f.stmt.Predicate.BuildDefinition.ExternalParameters.Source.Commit,
		}, f.now.Add(-time.Minute), 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		rogueStmt := f.stmt.Clone()
		rogueStmt.Predicate.BuildDefinition.ExternalParameters.BuilderIdentity = rogueSAN
		rogueBundle, err := AttestStatement(&rogueStmt, AttestOptions{
			Signer:  rogueKey,
			CertPEM: rogueCertPEM,
			TLog:    f.tlog,
			Now:     f.now.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = VerifyBundle(f.artifactHash, f.artifactName, rogueBundle, nil, f.policy)
		if !errors.Is(err, ErrUntrustedSigner) {
			t.Fatalf("want ErrUntrustedSigner for rogue SAN, got: %v", err)
		}
	})

	t.Run("2c_untrusted_ca_cert_substitution", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		rogueCA, err := NewEphemeralCA("Rogue CA", f.now)
		if err != nil {
			t.Fatal(err)
		}
		rogueKey, _ := GenerateEd25519KeyPair()
		_, rogueCertPEM, err := rogueCA.IssueCodeSigningCert(rogueKey.PublicKey(), OIDCIdentity{
			Subject: f.stmt.Predicate.BuildDefinition.ExternalParameters.BuilderIdentity,
			Issuer:  DefaultOIDCIssuer,
		}, f.now.Add(-time.Minute), 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		rogueBundle, err := AttestStatement(f.stmt, AttestOptions{
			Signer:  rogueKey,
			CertPEM: rogueCertPEM,
			TLog:    f.tlog,
			Now:     f.now.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = VerifyBundle(f.artifactHash, f.artifactName, rogueBundle, nil, f.policy)
		if !errors.Is(err, ErrCertInvalid) {
			t.Fatalf("want ErrCertInvalid for untrusted CA, got: %v", err)
		}
	})

	t.Run("2d_disallowed_builder_id", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		pol := f.policy.Clone()
		pol.AllowedBuilders = []string{"https://hanzo.ai/forge/runners/other-runner-v2"}
		_, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, pol)
		if !errors.Is(err, ErrBuilderMismatch) {
			t.Fatalf("want ErrBuilderMismatch, got: %v", err)
		}
	})

	t.Run("3a_source_repo_uri_substitution", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		pol := f.policy.Clone()
		pol.AllowedSourceURIs = []string{"git+https://github.com/hanzoai/other-repo"}
		_, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, pol)
		if !errors.Is(err, ErrSourceMismatch) {
			t.Fatalf("want ErrSourceMismatch on repo URI mismatch, got: %v", err)
		}
	})

	t.Run("3b_source_commit_substitution", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		pol := f.policy.Clone()
		pol.RequiredCommit = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
		_, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, pol)
		if !errors.Is(err, ErrSourceMismatch) {
			t.Fatalf("want ErrSourceMismatch on commit mismatch, got: %v", err)
		}
	})

	t.Run("4a_toolchain_digest_mismatch", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		pol := f.policy.Clone()
		pol.RequiredToolchainDigests["go"] = DigestBytes([]byte("different-compiler"))
		_, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, pol)
		if !errors.Is(err, ErrToolchainMismatch) {
			t.Fatalf("want ErrToolchainMismatch on compiler digest mismatch, got: %v", err)
		}
	})

	t.Run("4b_unapproved_compiler_in_allowlist", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		pol := f.policy.Clone()
		pol.RequiredToolchainDigests = nil
		pol.AllowedToolchainDigests = map[string]map[string]bool{
			"go": {DigestBytes([]byte("approved-go-1.27.2")): true},
		}
		_, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, pol)
		if !errors.Is(err, ErrToolchainMismatch) {
			t.Fatalf("want ErrToolchainMismatch for unapproved compiler, got: %v", err)
		}
	})

	t.Run("5a_dsse_payload_type_confusion", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		rawStmt, _ := json.Marshal(f.stmt)
		confusedEnv, err := SignDSSE("application/json", rawStmt, f.signer, f.certPEM)
		if err != nil {
			t.Fatal(err)
		}
		leafBytes, _ := CanonicalLogLeaf(confusedEnv)
		entry, err := f.tlog.Append(leafBytes, f.now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		confusedBundle := &Bundle{
			MediaType: BundleMediaType,
			Envelope:  *confusedEnv,
			LogEntry:  *entry,
			CreatedAt: f.now,
		}
		_, err = VerifyBundle(f.artifactHash, f.artifactName, confusedBundle, nil, f.policy)
		if !errors.Is(err, ErrPayloadTypeMismatch) {
			t.Fatalf("want ErrPayloadTypeMismatch, got: %v", err)
		}
	})

	t.Run("5b_dsse_pae_signature_tampering", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		tampered := f.bundle.Clone()
		rawSig, _ := base64.StdEncoding.DecodeString(tampered.Envelope.Signatures[0].Sig)
		rawSig[0] ^= 0xff
		tampered.Envelope.Signatures[0].Sig = base64.StdEncoding.EncodeToString(rawSig)
		// Even if an attacker re-logs the tampered envelope into the transparency log,
		// DSSE PAE verification MUST still fail!
		leaf, _ := CanonicalLogLeaf(&tampered.Envelope)
		entry, err := f.tlog.Append(leaf, f.now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		tampered.LogEntry = *entry
		_, err = VerifyBundle(f.artifactHash, f.artifactName, &tampered, nil, f.policy)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("want ErrInvalidSignature on PAE/sig tampering, got: %v", err)
		}
	})

	t.Run("6_replayed_swapped_provenance_from_another_artifact", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		otherHash := DigestBytes([]byte("other-binary-payload"))
		// Present valid bundle for cloud-linux-amd64 when verifying other-binary!
		_, err := VerifyBundle(otherHash, "other-binary", f.bundle, nil, f.policy)
		if !errors.Is(err, ErrArtifactNotInSubject) {
			t.Fatalf("want ErrArtifactNotInSubject on swapped provenance, got: %v", err)
		}
		// Even without artifactName, wrong digest must fail with ErrDigestMismatch!
		_, err = VerifyBundle(otherHash, "", f.bundle, nil, f.policy)
		if !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("want ErrDigestMismatch on digest-only swap, got: %v", err)
		}
	})

	t.Run("7a_forged_merkle_inclusion_proof", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		// Append a second entry so inclusion proof has a non-empty audit path.
		secondStmt := f.stmt.Clone()
		secondStmt.Predicate.RunDetails.Metadata.InvocationID = "inv-0002"
		b2, err := AttestStatement(&secondStmt, AttestOptions{
			Signer:  f.signer,
			CertPEM: f.certPEM,
			TLog:    f.tlog,
			Now:     f.now.Add(2 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		tampered := b2.Clone()
		hBytes, _ := hex.DecodeString(tampered.LogEntry.InclusionProof.Hashes[0])
		hBytes[0] ^= 0x80
		tampered.LogEntry.InclusionProof.Hashes[0] = hex.EncodeToString(hBytes)

		_, err = VerifyBundle(f.artifactHash, f.artifactName, &tampered, nil, f.policy)
		if !errors.Is(err, ErrTLogInvalid) {
			t.Fatalf("want ErrTLogInvalid on forged Merkle proof, got: %v", err)
		}
	})

	t.Run("7b_split_view_equivocation_and_inconsistent_checkpoint", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		pinnedSTH := f.bundle.LogEntry.Checkpoint

		// Create a forked transparency log signed with the SAME log key (equivocating log!).
		forkedLog, err := NewTransparencyLog(DefaultTLogOrigin, f.tlogKey)
		if err != nil {
			t.Fatal(err)
		}
		// Put a completely different entry at index 0, then append our target bundle at index 1.
		_, err = forkedLog.Append([]byte("equivocating-entry-0"), f.now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		bFork, err := AttestStatement(f.stmt, AttestOptions{
			Signer:  f.signer,
			CertPEM: f.certPEM,
			TLog:    forkedLog,
			Now:     f.now.Add(2 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		cp, err := forkedLog.ConsistencyProof(1, 2)
		if err != nil {
			t.Fatal(err)
		}
		pol := f.policy.Clone()
		pol.PinnedCheckpoint = &pinnedSTH

		_, err = VerifyBundle(f.artifactHash, f.artifactName, bFork, &cp, pol)
		if !errors.Is(err, ErrTLogInvalid) {
			t.Fatalf("want ErrTLogInvalid on split-view equivocation, got: %v", err)
		}
	})

	t.Run("8a_expired_certificate_outside_integrated_time", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		// Issue a cert that expired BEFORE the transparency log integratedTime!
		expiredKey, _ := GenerateEd25519KeyPair()
		_, expiredCertPEM, err := f.ca.IssueCodeSigningCert(expiredKey.PublicKey(), OIDCIdentity{
			Subject:                f.stmt.Predicate.BuildDefinition.ExternalParameters.BuilderIdentity,
			Issuer:                 DefaultOIDCIssuer,
			SourceRepositoryURI:    f.stmt.Predicate.BuildDefinition.ExternalParameters.Source.URI,
			SourceRepositoryDigest: f.stmt.Predicate.BuildDefinition.ExternalParameters.Source.Commit,
		}, f.now.Add(-30*time.Minute), 10*time.Minute) // expired 20 minutes ago!
		if err != nil {
			t.Fatal(err)
		}
		expiredBundle, err := AttestStatement(f.stmt, AttestOptions{
			Signer:  expiredKey,
			CertPEM: expiredCertPEM,
			TLog:    f.tlog,
			Now:     f.now.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = VerifyBundle(f.artifactHash, f.artifactName, expiredBundle, nil, f.policy)
		if !errors.Is(err, ErrCertInvalid) {
			t.Fatalf("want ErrCertInvalid for expired cert at integratedTime, got: %v", err)
		}
	})

	t.Run("8b_revoked_signer_key_serial_and_identity", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)

		polKey := f.policy.Clone()
		polKey.RevokedKeyIDs = map[string]bool{f.signer.KeyID(): true}
		if _, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, polKey); !errors.Is(err, ErrRevokedSigner) {
			t.Fatalf("want ErrRevokedSigner for revoked keyID, got: %v", err)
		}

		polSerial := f.policy.Clone()
		polSerial.RevokedCertSerials = map[string]bool{f.certSerial: true}
		if _, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, polSerial); !errors.Is(err, ErrRevokedSigner) {
			t.Fatalf("want ErrRevokedSigner for revoked cert serial, got: %v", err)
		}

		polID := f.policy.Clone()
		polID.RevokedIdentities = map[string]bool{f.stmt.Predicate.BuildDefinition.ExternalParameters.BuilderIdentity: true}
		if _, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, polID); !errors.Is(err, ErrRevokedSigner) {
			t.Fatalf("want ErrRevokedSigner for revoked identity, got: %v", err)
		}
	})

	t.Run("9_fail_closed_incomplete_policies", func(t *testing.T) {
		f := newTestFixture(t, AlgorithmEd25519)
		if _, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, nil); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatalf("want ErrPolicyInvalid for nil policy, got: %v", err)
		}
		noTLog := f.policy.Clone()
		noTLog.TLogPublicKey = nil
		if _, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, noTLog); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatalf("want ErrPolicyInvalid when TLogPublicKey omitted, got: %v", err)
		}
		noBuilders := f.policy.Clone()
		noBuilders.AllowedBuilders = nil
		if _, err := VerifyBundle(f.artifactHash, f.artifactName, f.bundle, nil, noBuilders); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatalf("want ErrPolicyInvalid when AllowedBuilders empty, got: %v", err)
		}
	})
}

func TestMerkleTreeExhaustiveProofsAndDomainSeparation(t *testing.T) {
	// Second-preimage domain separation: leaf(left || right) != node(left, right)
	left := HashLeaf([]byte("a"))
	right := HashLeaf([]byte("b"))
	concat := append(append([]byte(nil), left[:]...), right[:]...)
	if HashLeaf(concat) == HashChildren(left, right) {
		t.Fatal("RFC 6962 domain separation broken: leaf hash collided with interior node hash")
	}

	key, err := GenerateEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	tlog, err := NewTransparencyLog(DefaultTLogOrigin, key)
	if err != nil {
		t.Fatal(err)
	}

	const maxN = 33
	baseTime := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	rawLeaves := make([][]byte, maxN)
	leafHashes := make([][32]byte, maxN)
	roots := make([][32]byte, maxN+1)

	for i := 0; i < maxN; i++ {
		rawLeaves[i] = []byte("leaf-payload-" + string(rune('A'+i%26)) + "-" + hex.EncodeToString([]byte{byte(i)}))
		leafHashes[i] = HashLeaf(rawLeaves[i])
		entry, err := tlog.Append(rawLeaves[i], baseTime.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		wantRoot := ComputeMerkleRoot(leafHashes[:i+1])
		gotRoot, err := decodeHash32(entry.Checkpoint.RootHash)
		if err != nil {
			t.Fatalf("decodeHash32: %v", err)
		}
		if gotRoot != wantRoot {
			t.Fatalf("treeSize=%d root mismatch: incremental=%x reference=%x", i+1, gotRoot, wantRoot)
		}
		roots[i+1] = gotRoot
	}

	// Verify backdating refusal.
	if _, err := tlog.Append([]byte("backdated"), baseTime); !errors.Is(err, ErrTLogInvalid) {
		t.Fatalf("want ErrTLogInvalid for backdated append, got: %v", err)
	}

	// Exhaustive inclusion proofs across every (index, treeSize) pair for 1..33.
	for size := uint64(1); size <= maxN; size++ {
		for idx := uint64(0); idx < size; idx++ {
			proof, err := tlog.InclusionProof(idx, size)
			if err != nil {
				t.Fatalf("InclusionProof(%d, %d): %v", idx, size, err)
			}
			if err := VerifyInclusionProof(rawLeaves[idx], proof, roots[size]); err != nil {
				t.Fatalf("VerifyInclusionProof(%d, %d) failed: %v", idx, size, err)
			}
		}
	}

	// Exhaustive consistency proofs across every (first, second) pair for 1..33.
	for second := uint64(1); second <= maxN; second++ {
		for first := uint64(1); first <= second; first++ {
			cp, err := tlog.ConsistencyProof(first, second)
			if err != nil {
				t.Fatalf("ConsistencyProof(%d, %d): %v", first, second, err)
			}
			if err := VerifyConsistencyProof(cp, roots[first], roots[second]); err != nil {
				t.Fatalf("VerifyConsistencyProof(%d, %d) failed: %v", first, second, err)
			}
		}
	}
}

func TestTransparencyLogConcurrency(t *testing.T) {
	key, err := GenerateEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	tlog, err := NewTransparencyLog(DefaultTLogOrigin, key)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const perWorker = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				payload := []byte("concurrent-" + hex.EncodeToString([]byte{byte(id), byte(i)}))
				entry, err := tlog.Append(payload, time.Time{})
				if err != nil {
					t.Errorf("Append: %v", err)
					return
				}
				root, err := decodeHash32(entry.Checkpoint.RootHash)
				if err != nil {
					t.Errorf("decodeHash32: %v", err)
					return
				}
				if err := VerifyInclusionProof(payload, entry.InclusionProof, root); err != nil {
					t.Errorf("VerifyInclusionProof: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if got := tlog.Size(); got != workers*perWorker {
		t.Fatalf("tlog.Size() = %d, want %d", got, workers*perWorker)
	}
}

// --- Typed zip.App HTTP surface & tenant-isolation tests ---

func mountTestApp(t *testing.T) (*zip.App, *Service) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	svc, err := NewService(Deps{
		Log: luxlog.New("test"),
		Org: func(ctx context.Context) (string, bool) {
			c := zip.CallerOf(ctx)
			if c.User == "" || c.Org == "" {
				return "", false
			}
			return c.Org, true
		},
		Acting: func(ctx context.Context) (string, error) {
			c := zip.CallerOf(ctx)
			if c.User == "" {
				return "", zip.ErrUnauthorized("a validated principal is required")
			}
			if c.Org == "" {
				return "", zip.ErrForbidden("X-Org-Id required")
			}
			return c.Org, nil
		},
		Caller: func(ctx context.Context) (string, bool) {
			c := zip.CallerOf(ctx)
			if c.User == "" {
				return "", false
			}
			return c.User, true
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	MountService(app, svc)
	return app, svc
}

func doHTTP(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestEveryRouteIsDescribed(t *testing.T) {
	app, _ := mountTestApp(t)
	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal OpenAPI spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal OpenAPI spec: %v", err)
	}
	served := map[string]map[string]any{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/provenance") {
			continue
		}
		for method, op := range item {
			served[strings.ToUpper(method)+" "+path] = op
		}
	}
	if len(served) != 8 {
		t.Fatalf("expected 8 routes under /v1/provenance, got %d: %v", len(served), served)
	}
	for key, op := range served {
		if desc, _ := op["description"].(string); strings.TrimSpace(desc) == "" {
			t.Errorf("%s has empty OpenAPI description — run go generate ./apps/provenance", key)
		}
	}
}

func TestHTTPEndToEndTenantIsolationAndQueryHijackProtection(t *testing.T) {
	app, _ := mountTestApp(t)

	artifactHash := DigestBytes([]byte("release-tarball-v2"))
	goDigest := DigestBytes([]byte("go1.27.1-release"))
	commit := "0f31254abcdef0123456789abcdef0123456789a"
	sourceURI := "git+https://github.com/hanzoai/cloud"
	builderID := "https://hanzo.ai/forge/runners/linux-amd64-v1"

	attestReq := map[string]any{
		"subjects": []map[string]any{
			{"name": "cloud.tar.gz", "digest": map[string]string{"sha256": artifactHash}},
		},
		"source": map[string]any{
			"uri":    sourceURI,
			"branch": "refs/heads/main",
			"commit": commit,
		},
		"toolchain": []map[string]any{
			{"name": "go", "version": "1.27.1", "digest": map[string]string{"sha256": goDigest}},
		},
		"builderId":    builderID,
		"invocationId": "inv-http-001",
		// Attempt to smuggle untrusted org/identity fields — MUST be ignored!
		"org":             "victim-org",
		"builderIdentity": "https://hanzo.ai/principals/victim-org/admin",
	}

	// 1. Unauthenticated caller refused (401); authenticated without org refused (403).
	if code, _ := doHTTP(t, app, http.MethodPost, "/v1/provenance/attest", "", "", attestReq); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated attest want 401, got %d", code)
	}
	if code, _ := doHTTP(t, app, http.MethodPost, "/v1/provenance/attest", "", "u_alpha", attestReq); code != http.StatusForbidden {
		t.Fatalf("missing org attest want 403, got %d", code)
	}

	// 2. Query-string hijack attempt on POST /v1/provenance/attest MUST be ignored (`url:"-"`).
	code, body := doHTTP(t, app, http.MethodPost,
		"/v1/provenance/attest?builderId=https://evil.invalid/runner&invocationId=hijacked",
		"acme", "agent-01", attestReq)
	if code != http.StatusCreated {
		t.Fatalf("attest want 201, got %d (%s)", code, body)
	}
	var bundle Bundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if bundle.Org != "acme" {
		t.Fatalf("server must stamp caller org=acme, got %q", bundle.Org)
	}

	// 3. Cross-org isolation: beta cannot read or verify acme's bundle by ID!
	if code, _ := doHTTP(t, app, http.MethodGet, "/v1/provenance/bundles/"+bundle.ID, "beta", "u_beta", nil); code != http.StatusNotFound {
		t.Fatalf("cross-org getBundle want 404, got %d", code)
	}
	verifyBody := map[string]any{
		"artifactDigest":           artifactHash,
		"artifactName":             "cloud.tar.gz",
		"bundleId":                 bundle.ID,
		"allowedIdentities":        []string{PrincipalIdentityURI("acme", "agent-01")},
		"allowedBuilders":          []string{builderID},
		"allowedSourceUris":        []string{sourceURI},
		"requiredCommit":           commit,
		"requiredToolchainDigests": map[string]string{"go": goDigest},
	}
	if code, _ := doHTTP(t, app, http.MethodPost, "/v1/provenance/verify", "beta", "u_beta", verifyBody); code != http.StatusNotFound {
		t.Fatalf("cross-org verify by bundleId want 404, got %d", code)
	}

	// 4. Same-org verify succeeds (200) and proves query hijack on verify is ignored!
	code, body = doHTTP(t, app, http.MethodPost,
		"/v1/provenance/verify?artifactDigest=0000000000000000000000000000000000000000000000000000000000000000",
		"acme", "agent-01", verifyBody)
	if code != http.StatusOK {
		t.Fatalf("verify want 200, got %d (%s)", code, body)
	}
	var vr VerificationResult
	if err := json.Unmarshal(body, &vr); err != nil {
		t.Fatalf("decode verify result: %v", err)
	}
	if !vr.Verified || vr.SignerIdentity != PrincipalIdentityURI("acme", "agent-01") || vr.BuilderID != builderID {
		t.Fatalf("unexpected verify result: %+v", vr)
	}

	// 5. Tampered digest fails closed with 422 DIGEST_MISMATCH!
	tamperedVerify := map[string]any{
		"artifactDigest":           DigestBytes([]byte("tampered-tarball")),
		"artifactName":             "cloud.tar.gz",
		"bundleId":                 bundle.ID,
		"allowedIdentities":        []string{PrincipalIdentityURI("acme", "agent-01")},
		"allowedBuilders":          []string{builderID},
		"allowedSourceUris":        []string{sourceURI},
		"requiredCommit":           commit,
		"requiredToolchainDigests": map[string]string{"go": goDigest},
	}
	code, body = doHTTP(t, app, http.MethodPost, "/v1/provenance/verify", "acme", "agent-01", tamperedVerify)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "DIGEST_MISMATCH") {
		t.Fatalf("tampered verify want 422 DIGEST_MISMATCH, got %d (%s)", code, body)
	}

	// 6. Revoke certificate serial in org acme -> subsequent verification fails closed with 422 REVOKED_SIGNER!
	code, body = doHTTP(t, app, http.MethodPost, "/v1/provenance/revocations", "acme", "admin-01",
		map[string]any{"certSerial": vr.CertSerial})
	if code != http.StatusCreated {
		t.Fatalf("revoke want 201, got %d (%s)", code, body)
	}
	code, body = doHTTP(t, app, http.MethodPost, "/v1/provenance/verify", "acme", "agent-01", verifyBody)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "REVOKED_SIGNER") {
		t.Fatalf("revoked verify want 422 REVOKED_SIGNER, got %d (%s)", code, body)
	}

	// 7. Public checkpoint, health, and consistency endpoints work cleanly.
	code, _ = doHTTP(t, app, http.MethodGet, "/v1/provenance/health", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("health want 200, got %d", code)
	}
	code, body = doHTTP(t, app, http.MethodGet, "/v1/provenance/checkpoint", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("checkpoint want 200, got %d (%s)", code, body)
	}
	var cpOut checkpointOut
	if err := json.Unmarshal(body, &cpOut); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	tlogPub, err := ParsePublicKeyPEM(cpOut.TLogPublicKeyPEM)
	if err != nil {
		t.Fatalf("ParsePublicKeyPEM: %v", err)
	}
	if err := VerifySignedTreeHead(cpOut.Checkpoint, DefaultTLogOrigin, tlogPub); err != nil {
		t.Fatalf("VerifySignedTreeHead from /checkpoint: %v", err)
	}

	// Attest a second artifact and verify consistency proof from size 1 -> 2.
	attestReq["invocationId"] = "inv-http-002"
	if code, body := doHTTP(t, app, http.MethodPost, "/v1/provenance/attest", "acme", "agent-02", attestReq); code != http.StatusCreated {
		t.Fatalf("second attest want 201, got %d (%s)", code, body)
	}
	code, body = doHTTP(t, app, http.MethodGet, "/v1/provenance/consistency?firstSize=1&secondSize=2", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("consistency want 200, got %d (%s)", code, body)
	}
	var cProof ConsistencyProof
	if err := json.Unmarshal(body, &cProof); err != nil {
		t.Fatalf("decode consistency: %v", err)
	}
	r1, _ := decodeHash32(cProof.FirstRoot)
	r2, _ := decodeHash32(cProof.SecondRoot)
	if err := VerifyConsistencyProof(cProof, r1, r2); err != nil {
		t.Fatalf("VerifyConsistencyProof from HTTP: %v", err)
	}
}

func TestStatementAndCertValidationEdgeCases(t *testing.T) {
	f := newTestFixture(t, AlgorithmEd25519)

	// Invalid statement fields must be rejected fail-closed.
	badType := f.stmt.Clone()
	badType.Type = "https://in-toto.io/Statement/v0.1"
	if err := ValidateStatement(&badType); !errors.Is(err, ErrStatementInvalid) {
		t.Fatalf("want ErrStatementInvalid for bad _type, got %v", err)
	}

	badPred := f.stmt.Clone()
	badPred.PredicateType = "https://slsa.dev/provenance/v0.2"
	if err := ValidateStatement(&badPred); !errors.Is(err, ErrStatementInvalid) {
		t.Fatalf("want ErrStatementInvalid for bad predicateType, got %v", err)
	}

	badDigest := f.stmt.Clone()
	badDigest.Subject[0].Digest["sha256"] = strings.ToUpper(f.artifactHash)
	if err := ValidateStatement(&badDigest); !errors.Is(err, ErrStatementInvalid) {
		t.Fatalf("want ErrStatementInvalid for uppercase hex digest, got %v", err)
	}

	badTimes := f.stmt.Clone()
	badTimes.Predicate.RunDetails.Metadata.FinishedOn = badTimes.Predicate.RunDetails.Metadata.StartedOn.Add(-time.Second)
	if err := ValidateStatement(&badTimes); !errors.Is(err, ErrStatementInvalid) {
		t.Fatalf("want ErrStatementInvalid for finishedOn < startedOn, got %v", err)
	}

	// Presenting the Root CA cert itself as a leaf signing cert must fail closed.
	if _, err := VerifyCertificateChain(f.ca.RootPEM(), f.ca.CertPool(), f.now); !errors.Is(err, ErrCertInvalid) {
		t.Fatalf("want ErrCertInvalid when Root CA cert used as leaf, got %v", err)
	}

	// Presenting a certificate whose embedded source repo URI disagrees with SLSA statement must fail closed.
	mismatchKey, _ := GenerateEd25519KeyPair()
	_, mismatchCert, err := f.ca.IssueCodeSigningCert(mismatchKey.PublicKey(), OIDCIdentity{
		Subject:                f.stmt.Predicate.BuildDefinition.ExternalParameters.BuilderIdentity,
		Issuer:                 DefaultOIDCIssuer,
		SourceRepositoryURI:    "git+https://github.com/attacker/fork",
		SourceRepositoryDigest: f.stmt.Predicate.BuildDefinition.ExternalParameters.Source.Commit,
	}, f.now.Add(-time.Minute), DefaultCertTTL)
	if err != nil {
		t.Fatal(err)
	}
	mismatchBundle, err := AttestStatement(f.stmt, AttestOptions{
		Signer:  mismatchKey,
		CertPEM: mismatchCert,
		TLog:    f.tlog,
		Now:     f.now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBundle(f.artifactHash, f.artifactName, mismatchBundle, nil, f.policy); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("want ErrSourceMismatch when cert repo URI != statement repo URI, got %v", err)
	}
}

func TestMerkleProofMalformedRefusals(t *testing.T) {
	key, _ := GenerateEd25519KeyPair()
	tlog, _ := NewTransparencyLog(DefaultTLogOrigin, key)
	t0 := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	var leaves [][]byte
	for i := 0; i < 6; i++ {
		b := []byte{byte(i), 'x', 'y', 'z'}
		leaves = append(leaves, b)
		if _, err := tlog.Append(b, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	proof, err := tlog.InclusionProof(2, 6)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := decodeHash32(proof.RootHash)

	// Trailing extra hash in inclusion proof must be refused.
	withExtra := proof.Clone()
	withExtra.Hashes = append(withExtra.Hashes, proof.Hashes[0])
	if err := VerifyInclusionProof(leaves[2], withExtra, root); !errors.Is(err, ErrTLogInvalid) {
		t.Fatalf("want ErrTLogInvalid for trailing inclusion hash, got %v", err)
	}

	// Truncated inclusion proof must be refused.
	truncated := proof.Clone()
	truncated.Hashes = truncated.Hashes[:len(truncated.Hashes)-1]
	if err := VerifyInclusionProof(leaves[2], truncated, root); !errors.Is(err, ErrTLogInvalid) {
		t.Fatalf("want ErrTLogInvalid for truncated inclusion proof, got %v", err)
	}

	// Consistency proof rollback (firstSize > secondSize) must be refused.
	cp, err := tlog.ConsistencyProof(3, 6)
	if err != nil {
		t.Fatal(err)
	}
	r3, _ := decodeHash32(cp.FirstRoot)
	r6, _ := decodeHash32(cp.SecondRoot)
	rollback := cp.Clone()
	rollback.FirstSize = 6
	rollback.SecondSize = 3
	if err := VerifyConsistencyProof(rollback, r6, r3); !errors.Is(err, ErrTLogInvalid) {
		t.Fatalf("want ErrTLogInvalid for consistency rollback, got %v", err)
	}

	// Equal-size split-view equivocation must be refused.
	eqSplit := ConsistencyProof{FirstSize: 3, SecondSize: 3}
	if err := VerifyConsistencyProof(eqSplit, r3, r6); !errors.Is(err, ErrTLogInvalid) {
		t.Fatalf("want ErrTLogInvalid for equal-size equivocation, got %v", err)
	}
}

func TestHTTPConcurrentAttestAndVerify(t *testing.T) {
	app, _ := mountTestApp(t)
	goDigest := DigestBytes([]byte("go1.27.1"))
	commit := "0f31254abcdef0123456789abcdef0123456789a"
	sourceURI := "git+https://github.com/hanzoai/cloud"
	builderID := "https://hanzo.ai/forge/runners/linux-amd64-v1"

	const workers = 6
	const perWorker = 4
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			org := "org-a"
			if id%2 == 1 {
				org = "org-b"
			}
			user := "agent-" + hex.EncodeToString([]byte{byte(id)})
			for i := 0; i < perWorker; i++ {
				artName := "pkg-" + hex.EncodeToString([]byte{byte(id), byte(i)}) + ".tar.gz"
				artHash := DigestBytes([]byte(artName + org))
				attestReq := map[string]any{
					"subjects":     []map[string]any{{"name": artName, "digest": map[string]string{"sha256": artHash}}},
					"source":       map[string]any{"uri": sourceURI, "branch": "refs/heads/main", "commit": commit},
					"toolchain":    []map[string]any{{"name": "go", "version": "1.27.1", "digest": map[string]string{"sha256": goDigest}}},
					"builderId":    builderID,
					"invocationId": "conc-" + artName,
				}
				code, body := doHTTP(t, app, http.MethodPost, "/v1/provenance/attest", org, user, attestReq)
				if code != http.StatusCreated {
					t.Errorf("concurrent attest failed: %d %s", code, body)
					return
				}
				var b Bundle
				if err := json.Unmarshal(body, &b); err != nil {
					t.Errorf("decode bundle: %v", err)
					return
				}
				verifyReq := map[string]any{
					"artifactDigest":           artHash,
					"artifactName":             artName,
					"bundleId":                 b.ID,
					"allowedIdentities":        []string{PrincipalIdentityURI(org, user)},
					"allowedBuilders":          []string{builderID},
					"allowedSourceUris":        []string{sourceURI},
					"requiredCommit":           commit,
					"requiredToolchainDigests": map[string]string{"go": goDigest},
				}
				code, body = doHTTP(t, app, http.MethodPost, "/v1/provenance/verify", org, user, verifyReq)
				if code != http.StatusOK {
					t.Errorf("concurrent verify failed: %d %s", code, body)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Verify both orgs see exactly their own 12 bundles and never each other's.
	for _, org := range []string{"org-a", "org-b"} {
		code, body := doHTTP(t, app, http.MethodGet, "/v1/provenance/bundles", org, "auditor", nil)
		if code != http.StatusOK {
			t.Fatalf("listBundles(%s) want 200, got %d", org, code)
		}
		var out bundleListOut
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode listBundles: %v", err)
		}
		if len(out.Data) != (workers/2)*perWorker {
			t.Fatalf("org %s bundle count = %d, want %d", org, len(out.Data), (workers/2)*perWorker)
		}
		for _, item := range out.Data {
			if item.Org != org {
				t.Fatalf("org isolation leak: %s received bundle owned by %s", org, item.Org)
			}
		}
	}
}
