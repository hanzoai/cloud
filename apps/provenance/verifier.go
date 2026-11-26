// SPDX-License-Identifier: Apache-2.0

package provenance

import (
	"bytes"
	"crypto"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// Fail-closed sentinel errors for provenance verification and policy enforcement.
var (
	ErrDigestMismatch       = errors.New("provenance: artifact digest mismatch")
	ErrArtifactNotInSubject = errors.New("provenance: artifact not listed in statement subjects")
	ErrStatementInvalid     = errors.New("provenance: invalid SLSA v1.2 statement")
	ErrPayloadTypeMismatch  = errors.New("provenance: unexpected DSSE payloadType")
	ErrInvalidSignature     = errors.New("provenance: invalid DSSE cryptographic signature")
	ErrUntrustedSigner      = errors.New("provenance: signer key or OIDC identity is not trusted")
	ErrCertInvalid          = errors.New("provenance: certificate chain or validity window invalid")
	ErrRevokedSigner        = errors.New("provenance: signer key, identity, or certificate is revoked")
	ErrSourceMismatch       = errors.New("provenance: source repository or revision does not satisfy policy")
	ErrToolchainMismatch    = errors.New("provenance: build toolchain digest does not satisfy policy")
	ErrBuilderMismatch      = errors.New("provenance: builder ID is not allowed by policy")
	ErrTLogInvalid          = errors.New("provenance: transparency log proof or checkpoint invalid")
	ErrPolicyInvalid        = errors.New("provenance: verification policy is incomplete (fail-closed)")
)

// Bundle packages a signed DSSE SLSA v1.2 provenance envelope with its transparency log proof.
type Bundle struct {
	MediaType string       `json:"mediaType"`
	ID        string       `json:"id,omitempty"`
	Org       string       `json:"org,omitempty"`
	Envelope  DSSEEnvelope `json:"envelope"`
	LogEntry  LogEntry     `json:"logEntry"`
	CreatedAt time.Time    `json:"createdAt"`
}

// Clone returns a deep copy of Bundle.
func (b Bundle) Clone() Bundle {
	out := b
	out.Envelope = b.Envelope.Clone()
	out.LogEntry = b.LogEntry.Clone()
	return out
}

// AttestOptions controls SLSA v1.2 bundle creation via AttestStatement.
type AttestOptions struct {
	ID      string
	Org     string
	Signer  Signer
	CertPEM string
	TLog    *TransparencyLog
	Now     time.Time
}

// AttestStatement canonically encodes stmt, signs a DSSEv1 envelope, appends its
// cryptographic commitment to opts.TLog, and returns the resulting verifiable Bundle.
func AttestStatement(stmt *Statement, opts AttestOptions) (*Bundle, error) {
	if err := ValidateStatement(stmt); err != nil {
		return nil, err
	}
	if opts.Signer == nil {
		return nil, errors.New("provenance: AttestStatement requires a signer")
	}
	if opts.TLog == nil {
		return nil, errors.New("provenance: AttestStatement requires a transparency log")
	}
	payload, err := json.Marshal(stmt)
	if err != nil {
		return nil, fmt.Errorf("provenance: marshal SLSA statement: %w", err)
	}
	env, err := SignDSSE(InTotoPayloadType, payload, opts.Signer, opts.CertPEM)
	if err != nil {
		return nil, err
	}
	leafBytes, err := CanonicalLogLeaf(env)
	if err != nil {
		return nil, err
	}
	entry, err := opts.TLog.Append(leafBytes, opts.Now)
	if err != nil {
		return nil, err
	}
	return &Bundle{
		MediaType: BundleMediaType,
		ID:        opts.ID,
		Org:       opts.Org,
		Envelope:  *env,
		LogEntry:  *entry,
		CreatedAt: entry.IntegratedTime,
	}, nil
}

// VerificationPolicy defines the fail-closed verification rules for agent-produced artifacts.
type VerificationPolicy struct {
	// TrustedKeys maps allowed SPKI KeyIDs ("sha256:<hex>") to public keys for direct key verification.
	TrustedKeys map[string]crypto.PublicKey

	// TrustedCertRoots is the Fulcio-style Root CA certificate pool for short-lived OIDC certs.
	TrustedCertRoots *x509.CertPool

	// AllowedOIDCIssuers lists exact OIDC issuer URIs accepted on X.509 code-signing certificates.
	AllowedOIDCIssuers []string

	// AllowedIdentities lists exact OIDC SAN / builder identities authorized to sign artifacts.
	AllowedIdentities []string

	// AllowedBuilders lists exact SLSA runDetails.builder.id URIs authorized to produce builds.
	AllowedBuilders []string

	// AllowedSourceURIs lists exact source repository URIs authorized to build from.
	AllowedSourceURIs []string

	// RequiredBranch, RequiredCommit, and RequiredTree optionally pin exact Git references.
	RequiredBranch string
	RequiredCommit string
	RequiredTree   string

	// RequiredToolchainDigests pins required tool names to exact SHA-256 digests.
	RequiredToolchainDigests map[string]string

	// AllowedToolchainDigests optionally restricts tools to an allowlist of approved SHA-256 digests.
	AllowedToolchainDigests map[string]map[string]bool

	// StrictToolchainAllowlist rejects any declared toolchain component not in AllowedToolchainDigests.
	StrictToolchainAllowlist bool

	// Revocation lists checked fail-closed before accepting any signature or identity.
	RevokedKeyIDs      map[string]bool
	RevokedIdentities  map[string]bool
	RevokedCertSerials map[string]bool

	// TLogPublicKey verifies the transparency log SignedTreeHead checkpoint (REQUIRED).
	TLogPublicKey crypto.PublicKey

	// TLogOrigin optionally pins the expected transparency log origin string.
	TLogOrigin string

	// PinnedCheckpoint optionally anchors verification to a previously observed STH,
	// requiring a valid RFC 6962 consistency proof to detect split-view equivocation.
	PinnedCheckpoint *SignedTreeHead
}

// Validate enforces fail-closed policy completeness.
func (p *VerificationPolicy) Validate() error {
	if p == nil {
		return fmt.Errorf("%w: nil policy", ErrPolicyInvalid)
	}
	if p.TLogPublicKey == nil {
		return fmt.Errorf("%w: TLogPublicKey is required", ErrPolicyInvalid)
	}
	if len(p.TrustedKeys) == 0 && p.TrustedCertRoots == nil {
		return fmt.Errorf("%w: at least one trust anchor (TrustedKeys or TrustedCertRoots) is required", ErrPolicyInvalid)
	}
	if p.TrustedCertRoots != nil {
		if len(p.AllowedOIDCIssuers) == 0 {
			return fmt.Errorf("%w: AllowedOIDCIssuers is required when TrustedCertRoots is configured", ErrPolicyInvalid)
		}
		if len(p.AllowedIdentities) == 0 {
			return fmt.Errorf("%w: AllowedIdentities is required when TrustedCertRoots is configured", ErrPolicyInvalid)
		}
	}
	if len(p.AllowedBuilders) == 0 {
		return fmt.Errorf("%w: AllowedBuilders must be non-empty", ErrPolicyInvalid)
	}
	if len(p.AllowedSourceURIs) == 0 {
		return fmt.Errorf("%w: AllowedSourceURIs must be non-empty", ErrPolicyInvalid)
	}
	for name, digest := range p.RequiredToolchainDigests {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: empty tool name in RequiredToolchainDigests", ErrPolicyInvalid)
		}
		if err := ValidateSHA256Hex(digest); err != nil {
			return fmt.Errorf("%w: invalid digest for required tool %q: %v", ErrPolicyInvalid, name, err)
		}
	}
	return nil
}

// Clone returns a deep copy of VerificationPolicy.
func (p *VerificationPolicy) Clone() *VerificationPolicy {
	if p == nil {
		return nil
	}
	out := *p
	if p.TrustedKeys != nil {
		out.TrustedKeys = maps.Clone(p.TrustedKeys)
	}
	out.AllowedOIDCIssuers = slices.Clone(p.AllowedOIDCIssuers)
	out.AllowedIdentities = slices.Clone(p.AllowedIdentities)
	out.AllowedBuilders = slices.Clone(p.AllowedBuilders)
	out.AllowedSourceURIs = slices.Clone(p.AllowedSourceURIs)
	if p.RequiredToolchainDigests != nil {
		out.RequiredToolchainDigests = maps.Clone(p.RequiredToolchainDigests)
	}
	if p.AllowedToolchainDigests != nil {
		out.AllowedToolchainDigests = make(map[string]map[string]bool, len(p.AllowedToolchainDigests))
		for k, v := range p.AllowedToolchainDigests {
			out.AllowedToolchainDigests[k] = maps.Clone(v)
		}
	}
	if p.RevokedKeyIDs != nil {
		out.RevokedKeyIDs = maps.Clone(p.RevokedKeyIDs)
	}
	if p.RevokedIdentities != nil {
		out.RevokedIdentities = maps.Clone(p.RevokedIdentities)
	}
	if p.RevokedCertSerials != nil {
		out.RevokedCertSerials = maps.Clone(p.RevokedCertSerials)
	}
	if p.PinnedCheckpoint != nil {
		cp := *p.PinnedCheckpoint
		out.PinnedCheckpoint = &cp
	}
	return &out
}

// VerificationResult reports the cryptographically verified provenance claims.
type VerificationResult struct {
	Verified       bool      `json:"verified"`
	ArtifactName   string    `json:"artifactName"`
	ArtifactDigest string    `json:"artifactDigest"`
	SignerKeyID    string    `json:"signerKeyId"`
	SignerIdentity string    `json:"signerIdentity"`
	OIDCIssuer     string    `json:"oidcIssuer,omitempty"`
	CertSerial     string    `json:"certSerial,omitempty"`
	BuilderID      string    `json:"builderId"`
	SourceURI      string    `json:"sourceUri"`
	SourceBranch   string    `json:"sourceBranch,omitempty"`
	SourceCommit   string    `json:"sourceCommit"`
	LogIndex       uint64    `json:"logIndex"`
	IntegratedTime time.Time `json:"integratedTime"`
	TreeSize       uint64    `json:"treeSize"`
	RootHash       string    `json:"rootHash"`
}

// VerifyBundle executes fail-closed SLSA v1.2 + DSSE + X.509/key + RFC-6962 Merkle transparency
// log verification for an artifact digest against policy.
func VerifyBundle(artifactDigestSHA256, artifactName string, bundle *Bundle, consistency *ConsistencyProof, policy *VerificationPolicy) (*VerificationResult, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if bundle == nil {
		return nil, fmt.Errorf("%w: nil bundle", ErrStatementInvalid)
	}
	if bundle.MediaType != BundleMediaType {
		return nil, fmt.Errorf("%w: unsupported bundle mediaType %q", ErrStatementInvalid, bundle.MediaType)
	}
	if err := ValidateSHA256Hex(artifactDigestSHA256); err != nil {
		return nil, fmt.Errorf("%w: invalid artifact sha256 digest: %v", ErrDigestMismatch, err)
	}

	// 1. Refuse DSSE payload-type confusion immediately.
	if bundle.Envelope.PayloadType != InTotoPayloadType {
		return nil, fmt.Errorf("%w: got %q, want %q", ErrPayloadTypeMismatch, bundle.Envelope.PayloadType, InTotoPayloadType)
	}

	// 2. Verify Signed Tree Head (STH) checkpoint signature and bounds.
	sth := bundle.LogEntry.Checkpoint
	if err := VerifySignedTreeHead(sth, policy.TLogOrigin, policy.TLogPublicKey); err != nil {
		return nil, err
	}
	if bundle.LogEntry.IntegratedTime.IsZero() {
		return nil, fmt.Errorf("%w: missing transparency log integratedTime", ErrTLogInvalid)
	}
	if bundle.LogEntry.IntegratedTime.After(sth.Timestamp) {
		return nil, fmt.Errorf("%w: entry integratedTime %s exceeds checkpoint timestamp %s", ErrTLogInvalid, bundle.LogEntry.IntegratedTime.Format(time.RFC3339Nano), sth.Timestamp.Format(time.RFC3339Nano))
	}
	if bundle.LogEntry.InclusionProof.TreeSize != sth.TreeSize {
		return nil, fmt.Errorf("%w: inclusion proof treeSize %d != checkpoint treeSize %d", ErrTLogInvalid, bundle.LogEntry.InclusionProof.TreeSize, sth.TreeSize)
	}
	checkpointRoot, err := decodeHash32(sth.RootHash)
	if err != nil {
		return nil, fmt.Errorf("%w: decode checkpoint root: %v", ErrTLogInvalid, err)
	}

	// 3. Enforce consistency against any pinned checkpoint (prevents split-view / equivocation).
	if policy.PinnedCheckpoint != nil {
		pinned := *policy.PinnedCheckpoint
		if err := VerifySignedTreeHead(pinned, policy.TLogOrigin, policy.TLogPublicKey); err != nil {
			return nil, fmt.Errorf("%w: invalid pinned checkpoint: %v", ErrTLogInvalid, err)
		}
		if sth.TreeSize < pinned.TreeSize {
			return nil, fmt.Errorf("%w: checkpoint rollback detected (%d < pinned %d)", ErrTLogInvalid, sth.TreeSize, pinned.TreeSize)
		}
		pinnedRoot, err := decodeHash32(pinned.RootHash)
		if err != nil {
			return nil, fmt.Errorf("%w: decode pinned root: %v", ErrTLogInvalid, err)
		}
		if sth.TreeSize == pinned.TreeSize {
			if subtle.ConstantTimeCompare(pinnedRoot[:], checkpointRoot[:]) != 1 {
				return nil, fmt.Errorf("%w: split-view equivocation at treeSize=%d", ErrTLogInvalid, sth.TreeSize)
			}
		} else {
			if consistency == nil {
				return nil, fmt.Errorf("%w: consistency proof required from pinned size %d to %d", ErrTLogInvalid, pinned.TreeSize, sth.TreeSize)
			}
			if consistency.FirstSize != pinned.TreeSize || consistency.SecondSize != sth.TreeSize {
				return nil, fmt.Errorf("%w: consistency proof bounds (%d->%d) do not match checkpoints (%d->%d)",
					ErrTLogInvalid, consistency.FirstSize, consistency.SecondSize, pinned.TreeSize, sth.TreeSize)
			}
			if err := VerifyConsistencyProof(*consistency, pinnedRoot, checkpointRoot); err != nil {
				return nil, err
			}
		}
	}

	// 4. Verify cryptographic binding between DSSE envelope and transparency log leaf + Merkle inclusion proof.
	expectedLeaf, err := CanonicalLogLeaf(&bundle.Envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: compute canonical log leaf: %v", ErrTLogInvalid, err)
	}
	if subtle.ConstantTimeCompare(bundle.LogEntry.CanonicalBody, expectedLeaf) != 1 {
		return nil, fmt.Errorf("%w: transparency log entry does not commit to this DSSE envelope", ErrTLogInvalid)
	}
	if err := VerifyInclusionProof(bundle.LogEntry.CanonicalBody, bundle.LogEntry.InclusionProof, checkpointRoot); err != nil {
		return nil, err
	}

	// 5. Verify DSSE PAE signature and authenticate signer (Fulcio-style short-lived X.509 cert or pinned key).
	var verifiedCert *VerifiedCertIdentity
	rawPayload, matchedSig, err := VerifyDSSE(&bundle.Envelope, InTotoPayloadType, func(sig DSSESignature) (crypto.PublicKey, error) {
		if sig.CertPEM != "" {
			if policy.TrustedCertRoots == nil {
				return nil, fmt.Errorf("%w: certificate authentication not enabled by policy", ErrUntrustedSigner)
			}
			certID, err := VerifyCertificateChain(sig.CertPEM, policy.TrustedCertRoots, bundle.LogEntry.IntegratedTime)
			if err != nil {
				return nil, err
			}
			if policy.RevokedCertSerials[certID.SerialHex] || policy.RevokedKeyIDs[certID.KeyID] || (sig.KeyID != "" && policy.RevokedKeyIDs[sig.KeyID]) || policy.RevokedIdentities[certID.Subject] {
				return nil, fmt.Errorf("%w: certificate or signer identity revoked", ErrRevokedSigner)
			}
			if sig.KeyID != "" && sig.KeyID != certID.KeyID {
				return nil, fmt.Errorf("%w: signature keyid %q does not match certificate public key %q", ErrInvalidSignature, sig.KeyID, certID.KeyID)
			}
			if !slices.Contains(policy.AllowedOIDCIssuers, certID.Issuer) {
				return nil, fmt.Errorf("%w: OIDC issuer %q not allowed", ErrUntrustedSigner, certID.Issuer)
			}
			if !slices.Contains(policy.AllowedIdentities, certID.Subject) {
				return nil, fmt.Errorf("%w: certificate identity %q not allowed", ErrUntrustedSigner, certID.Subject)
			}
			verifiedCert = certID
			return certID.PublicKey, nil
		}

		// Direct pinned public key verification.
		if sig.KeyID == "" {
			return nil, fmt.Errorf("%w: missing signature keyid", ErrUntrustedSigner)
		}
		if policy.RevokedKeyIDs[sig.KeyID] {
			return nil, fmt.Errorf("%w: keyid %q is revoked", ErrRevokedSigner, sig.KeyID)
		}
		pub, ok := policy.TrustedKeys[sig.KeyID]
		if !ok || pub == nil {
			return nil, fmt.Errorf("%w: keyid %q not in TrustedKeys", ErrUntrustedSigner, sig.KeyID)
		}
		derivedID, err := KeyIDFromPublicKey(pub)
		if err != nil || derivedID != sig.KeyID {
			return nil, fmt.Errorf("%w: trusted keyid mismatch for %q", ErrUntrustedSigner, sig.KeyID)
		}
		return pub, nil
	})
	if err != nil {
		return nil, err
	}

	// 6. Decode and validate canonical SLSA v1.2 statement strictly (no trailing bytes).
	stmt, err := decodeStrictStatement(rawPayload)
	if err != nil {
		return nil, err
	}
	if err := ValidateStatement(stmt); err != nil {
		return nil, err
	}

	// 7. Cross-check certificate / identity bindings against SLSA statement.
	ext := stmt.Predicate.BuildDefinition.ExternalParameters
	signerIdentity := ext.BuilderIdentity
	if policy.RevokedIdentities[signerIdentity] {
		return nil, fmt.Errorf("%w: builder identity %q is revoked", ErrRevokedSigner, signerIdentity)
	}
	if verifiedCert != nil {
		if ext.BuilderIdentity != verifiedCert.Subject {
			return nil, fmt.Errorf("%w: statement builderIdentity %q != certificate SAN %q", ErrUntrustedSigner, ext.BuilderIdentity, verifiedCert.Subject)
		}
		if verifiedCert.SourceRepositoryURI != "" && verifiedCert.SourceRepositoryURI != ext.Source.URI {
			return nil, fmt.Errorf("%w: certificate source URI %q != statement source URI %q", ErrSourceMismatch, verifiedCert.SourceRepositoryURI, ext.Source.URI)
		}
		if verifiedCert.SourceRepositoryDigest != "" && verifiedCert.SourceRepositoryDigest != ext.Source.Commit {
			return nil, fmt.Errorf("%w: certificate source commit %q != statement commit %q", ErrSourceMismatch, verifiedCert.SourceRepositoryDigest, ext.Source.Commit)
		}
	} else if len(policy.AllowedIdentities) > 0 {
		if !slices.Contains(policy.AllowedIdentities, signerIdentity) {
			return nil, fmt.Errorf("%w: builder identity %q not in AllowedIdentities", ErrUntrustedSigner, signerIdentity)
		}
	}

	// 8. Verify exact artifact digest and subject binding (prevents 1-bit tamper and statement replay/swap).
	matchedArtifactName, err := matchArtifactSubject(stmt.Subject, artifactDigestSHA256, artifactName)
	if err != nil {
		return nil, err
	}

	// 9. Enforce builder policy.
	builderID := stmt.Predicate.RunDetails.Builder.ID
	if !slices.Contains(policy.AllowedBuilders, builderID) {
		return nil, fmt.Errorf("%w: builder %q not in AllowedBuilders", ErrBuilderMismatch, builderID)
	}

	// 10. Enforce source repository URI & Git revision pins.
	if !slices.Contains(policy.AllowedSourceURIs, ext.Source.URI) {
		return nil, fmt.Errorf("%w: source URI %q not in AllowedSourceURIs", ErrSourceMismatch, ext.Source.URI)
	}
	if policy.RequiredBranch != "" && ext.Source.Branch != policy.RequiredBranch {
		return nil, fmt.Errorf("%w: source branch %q != required %q", ErrSourceMismatch, ext.Source.Branch, policy.RequiredBranch)
	}
	if policy.RequiredCommit != "" && subtle.ConstantTimeCompare([]byte(ext.Source.Commit), []byte(policy.RequiredCommit)) != 1 {
		return nil, fmt.Errorf("%w: source commit %q != required %q", ErrSourceMismatch, ext.Source.Commit, policy.RequiredCommit)
	}
	if policy.RequiredTree != "" && subtle.ConstantTimeCompare([]byte(ext.Source.Tree), []byte(policy.RequiredTree)) != 1 {
		return nil, fmt.Errorf("%w: source tree %q != required %q", ErrSourceMismatch, ext.Source.Tree, policy.RequiredTree)
	}

	// 11. Enforce toolchain digest pins & allowlists.
	if err := verifyToolchainPolicy(ext.Toolchain, policy); err != nil {
		return nil, err
	}

	res := &VerificationResult{
		Verified:       true,
		ArtifactName:   matchedArtifactName,
		ArtifactDigest: artifactDigestSHA256,
		SignerKeyID:    matchedSig.KeyID,
		SignerIdentity: signerIdentity,
		BuilderID:      builderID,
		SourceURI:      ext.Source.URI,
		SourceBranch:   ext.Source.Branch,
		SourceCommit:   ext.Source.Commit,
		LogIndex:       bundle.LogEntry.LogIndex,
		IntegratedTime: bundle.LogEntry.IntegratedTime,
		TreeSize:       sth.TreeSize,
		RootHash:       hex.EncodeToString(checkpointRoot[:]),
	}
	if verifiedCert != nil {
		res.SignerKeyID = verifiedCert.KeyID
		res.OIDCIssuer = verifiedCert.Issuer
		res.CertSerial = verifiedCert.SerialHex
	}
	return res, nil
}

func decodeStrictStatement(raw []byte) (*Statement, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var stmt Statement
	if err := dec.Decode(&stmt); err != nil {
		return nil, fmt.Errorf("%w: decode statement JSON: %v", ErrStatementInvalid, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: unexpected trailing data after statement JSON", ErrStatementInvalid)
	}
	return &stmt, nil
}

func matchArtifactSubject(subjects []ResourceDescriptor, wantDigest, wantName string) (string, error) {
	if wantName != "" {
		foundName := false
		for _, sub := range subjects {
			if sub.Name == wantName {
				foundName = true
				if subtle.ConstantTimeCompare([]byte(sub.Digest["sha256"]), []byte(wantDigest)) == 1 {
					return sub.Name, nil
				}
			}
		}
		if foundName {
			return "", fmt.Errorf("%w: artifact %q sha256 does not match signed provenance", ErrDigestMismatch, wantName)
		}
		return "", fmt.Errorf("%w: artifact %q absent from signed subjects", ErrArtifactNotInSubject, wantName)
	}

	for _, sub := range subjects {
		if subtle.ConstantTimeCompare([]byte(sub.Digest["sha256"]), []byte(wantDigest)) == 1 {
			return sub.Name, nil
		}
	}
	return "", fmt.Errorf("%w: no signed subject matches sha256 %s", ErrDigestMismatch, wantDigest)
}

func verifyToolchainPolicy(declared []ToolchainComponent, policy *VerificationPolicy) error {
	seen := make(map[string]string, len(declared))
	for _, tc := range declared {
		digest := tc.Digest["sha256"]
		if prev, dup := seen[tc.Name]; dup && prev != digest {
			return fmt.Errorf("%w: conflicting digests declared for tool %q", ErrToolchainMismatch, tc.Name)
		}
		seen[tc.Name] = digest

		if len(policy.AllowedToolchainDigests) > 0 {
			allowed, governed := policy.AllowedToolchainDigests[tc.Name]
			if governed {
				if !allowed[digest] {
					return fmt.Errorf("%w: unapproved digest %s for tool %q", ErrToolchainMismatch, digest, tc.Name)
				}
			} else if policy.StrictToolchainAllowlist {
				return fmt.Errorf("%w: tool %q not permitted by strict toolchain allowlist", ErrToolchainMismatch, tc.Name)
			}
		}
	}

	for reqName, reqDigest := range policy.RequiredToolchainDigests {
		actual, ok := seen[reqName]
		if !ok {
			return fmt.Errorf("%w: required toolchain component %q missing from provenance", ErrToolchainMismatch, reqName)
		}
		if subtle.ConstantTimeCompare([]byte(actual), []byte(reqDigest)) != 1 {
			return fmt.Errorf("%w: toolchain %q digest %s != required %s", ErrToolchainMismatch, reqName, actual, reqDigest)
		}
	}
	return nil
}

// VerifyFile streams the file at path through SHA-256 and verifies it against bundle and policy.
func VerifyFile(path, artifactName string, bundle *Bundle, consistency *ConsistencyProof, policy *VerificationPolicy) (*VerificationResult, error) {
	digest, _, err := DigestFile(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: hash artifact file: %w", err)
	}
	return VerifyBundle(digest, artifactName, bundle, consistency, policy)
}
