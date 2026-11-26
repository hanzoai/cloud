// SPDX-License-Identifier: Apache-2.0

package provenance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const (
	// DefaultOIDCIssuer is the default Hanzo IAM OIDC issuer embedded in Fulcio-style certs.
	DefaultOIDCIssuer = "https://iam.hanzo.ai"

	maxSubjects  = 64
	maxToolchain = 64
	maxFieldLen  = 4096
	maxBundles   = 10000
)

// Deps supplies the host dependencies required by the provenance app.
// It imports nothing of its host so the package mounts unchanged in any zip host.
type Deps struct {
	// Log is the host logger scoped to the provenance subsystem.
	Log luxlog.Logger

	// Org returns the validated org of the caller on ctx.
	Org func(ctx context.Context) (org string, ok bool)

	// Acting returns the validated org a call acts for, or a zip.ErrUnauthorized / zip.ErrForbidden.
	Acting func(ctx context.Context) (org string, err error)

	// Caller returns the validated principal user ID on ctx.
	Caller func(ctx context.Context) (user string, ok bool)

	// OIDCIssuer overrides the default Fulcio OIDC issuer URI (optional).
	OIDCIssuer string

	// TLogOrigin overrides the default transparency log checkpoint origin (optional).
	TLogOrigin string

	// CA overrides the default ephemeral Fulcio-style Root CA (optional).
	CA *EphemeralCA

	// TLog overrides the default append-only Merkle transparency log (optional).
	TLog *TransparencyLog

	// Clock overrides time.Now for deterministic tests (optional).
	Clock func() time.Time
}

type orgRevocations struct {
	keyIDs      map[string]bool
	identities  map[string]bool
	certSerials map[string]bool
}

// Service owns the provenance CA, append-only transparency log, and org-isolated bundle store.
type Service struct {
	log        luxlog.Logger
	org        func(context.Context) (string, bool)
	acting     func(context.Context) (string, error)
	caller     func(context.Context) (string, bool)
	oidcIssuer string
	ca         *EphemeralCA
	tlog       *TransparencyLog
	tlogPubPEM string
	clock      func() time.Time
	customClk  bool

	mu          sync.RWMutex
	bundles     map[string]map[string]Bundle // org -> bundleID -> Bundle
	revocations map[string]*orgRevocations   // org -> revocations
}

// NewService constructs a standalone provenance Service from deps.
func NewService(deps Deps) (*Service, error) {
	if deps.Log == nil || deps.Org == nil || deps.Acting == nil || deps.Caller == nil {
		return nil, errors.New("provenance: Deps requires Log, Org, Acting, and Caller")
	}
	customClk := deps.Clock != nil
	clock := deps.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	issuer := strings.TrimSpace(deps.OIDCIssuer)
	if issuer == "" {
		issuer = DefaultOIDCIssuer
	}
	ca := deps.CA
	if ca == nil {
		var err error
		ca, err = NewEphemeralCA("Hanzo Forge Ephemeral Provenance Root CA", clock())
		if err != nil {
			return nil, err
		}
	}
	tlog := deps.TLog
	if tlog == nil {
		tlogKey, err := GenerateEd25519KeyPair()
		if err != nil {
			return nil, err
		}
		tlog, err = NewTransparencyLog(deps.TLogOrigin, tlogKey)
		if err != nil {
			return nil, err
		}
	}
	tlogPubPEM, err := EncodePublicKeyPEM(tlog.PublicKey())
	if err != nil {
		return nil, fmt.Errorf("provenance: encode tlog public key: %w", err)
	}
	return &Service{
		log:         deps.Log,
		org:         deps.Org,
		acting:      deps.Acting,
		caller:      deps.Caller,
		oidcIssuer:  issuer,
		ca:          ca,
		tlog:        tlog,
		tlogPubPEM:  tlogPubPEM,
		clock:       clock,
		customClk:   customClk,
		bundles:     make(map[string]map[string]Bundle),
		revocations: make(map[string]*orgRevocations),
	}, nil
}

// CA returns the service's Fulcio-style ephemeral certificate authority.
func (s *Service) CA() *EphemeralCA { return s.ca }

// TLog returns the service's append-only Merkle transparency log.
func (s *Service) TLog() *TransparencyLog { return s.tlog }

// Mount registers the typed /v1/provenance HTTP surface on app.
func Mount(app *zip.App, deps Deps) error {
	if app == nil {
		return errors.New("provenance: nil app")
	}
	svc, err := NewService(deps)
	if err != nil {
		return fmt.Errorf("provenance.Mount: %w", err)
	}
	MountService(app, svc)
	svc.log.Info("provenance subsystem mounted", "prefix", "/v1/provenance", "origin", svc.tlog.Origin())
	return nil
}

// MountService mounts an existing Service on app at /v1/provenance.
func MountService(app *zip.App, svc *Service) {
	o := ops{s: svc}
	g := app.Group("/v1/provenance")
	g.Get("/health", o.health)
	g.Get("/checkpoint", o.checkpoint)
	g.Get("/consistency", o.consistency)
	g.Post("/attest", o.attest, zip.WithStatus(http.StatusCreated))
	g.Post("/verify", o.verify)
	g.Get("/bundles", o.listBundles)
	g.Get("/bundles/:id", o.getBundle)
	g.Post("/revocations", o.revoke, zip.WithStatus(http.StatusCreated))
}

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

type ops struct{ s *Service }

// healthOut reports provenance subsystem readiness and transparency log head metrics.
type healthOut struct {
	// Status is "ok" when the ephemeral CA and Merkle transparency log are operational.
	Status string `json:"status"`
	// Origin identifies the transparency log checkpoint domain.
	Origin string `json:"origin"`
	// TreeSize is the current number of entries committed to the transparency log.
	TreeSize uint64 `json:"treeSize"`
	// RootHash is the current RFC 6962 SHA-256 Merkle root hash in lowercase hex.
	RootHash string `json:"rootHash"`
}

// checkpointOut returns the latest signed tree head and public verification trust material.
type checkpointOut struct {
	// Checkpoint is the latest Ed25519 Signed Tree Head (STH).
	Checkpoint SignedTreeHead `json:"checkpoint"`
	// TLogPublicKeyPEM is the PKIX PEM public key used to verify Signed Tree Heads.
	TLogPublicKeyPEM string `json:"tlogPublicKeyPem"`
	// RootCAPEM is the PEM-encoded Root CA certificate for short-lived Fulcio-style OIDC certs.
	RootCAPEM string `json:"rootCaPem"`
	// OIDCIssuer is the expected Fulcio OIDC issuer URI stamped on issued certificates.
	OIDCIssuer string `json:"oidcIssuer"`
}

// consistencyIn specifies the two tree sizes for an RFC 6962 Merkle consistency proof query.
type consistencyIn struct {
	// FirstSize is the previously observed checkpoint tree size.
	FirstSize uint64 `json:"firstSize" query:"firstSize"`
	// SecondSize is the newer checkpoint tree size (defaults to current log size when 0).
	SecondSize uint64 `json:"secondSize" query:"secondSize"`
}

// attestIn carries the artifact subjects, source revision, builder ID, and toolchain digests
// to attest into an SLSA v1.2 DSSE bundle. Every field carries `url:"-"` so query parameters
// can never hijack or override the JSON request body. Caller org and identity are NEVER read
// from the request body — they are stamped exclusively from the authenticated principal.
type attestIn struct {
	// Subjects lists the artifact resource descriptors (each requiring name and sha256 digest).
	Subjects []ResourceDescriptor `json:"subjects" url:"-"`
	// BuildType overrides the default SLSA v1.2 buildType URI (optional).
	BuildType string `json:"buildType" url:"-"`
	// Source identifies the exact repository URI, branch, commit SHA, and optional tree SHA.
	Source SourceRef `json:"source" url:"-"`
	// Toolchain lists the pinned compilers/runtimes and their SHA-256 digests.
	Toolchain []ToolchainComponent `json:"toolchain" url:"-"`
	// BuilderID identifies the trusted build platform or runner (runDetails.builder.id).
	BuilderID string `json:"builderId" url:"-"`
	// BuilderVersion maps builder component names to versions (optional).
	BuilderVersion map[string]string `json:"builderVersion" url:"-"`
	// InvocationID uniquely identifies this build execution.
	InvocationID string `json:"invocationId" url:"-"`
	// StartedOn records when the build began (RFC3339; defaults to server UTC now).
	StartedOn time.Time `json:"startedOn" url:"-"`
	// FinishedOn records when the build finished (RFC3339; defaults to server UTC now).
	FinishedOn time.Time `json:"finishedOn" url:"-"`
	// Algorithm selects the ephemeral signing algorithm: "ed25519" (default) or "ecdsa-p256-sha256".
	Algorithm string `json:"algorithm" url:"-"`
}

// verifyIn specifies the artifact digest and fail-closed policy constraints to verify.
// Every field carries `url:"-"` to prevent query-string parameter hijacking.
type verifyIn struct {
	// ArtifactDigest is the canonical 64-char lowercase hex SHA-256 digest of the artifact to verify.
	ArtifactDigest string `json:"artifactDigest" url:"-"`
	// ArtifactName is the expected subject name in the SLSA statement (optional; prevents cross-artifact replay when set).
	ArtifactName string `json:"artifactName" url:"-"`
	// BundleID references a stored bundle owned by the caller's org (mutually optional with Bundle).
	BundleID string `json:"bundleId" url:"-"`
	// Bundle provides an inline provenance bundle to verify directly.
	Bundle *Bundle `json:"bundle" url:"-"`
	// AllowedIdentities lists exact OIDC SAN identities authorized to sign the artifact.
	AllowedIdentities []string `json:"allowedIdentities" url:"-"`
	// AllowedBuilders lists exact SLSA runDetails.builder.id URIs authorized for the build.
	AllowedBuilders []string `json:"allowedBuilders" url:"-"`
	// AllowedSourceURIs lists exact source repository URIs authorized for the build.
	AllowedSourceURIs []string `json:"allowedSourceUris" url:"-"`
	// RequiredBranch optionally pins the exact Git branch.
	RequiredBranch string `json:"requiredBranch" url:"-"`
	// RequiredCommit optionally pins the exact Git commit SHA.
	RequiredCommit string `json:"requiredCommit" url:"-"`
	// RequiredTree optionally pins the exact Git tree SHA.
	RequiredTree string `json:"requiredTree" url:"-"`
	// RequiredToolchainDigests pins required tool names to exact SHA-256 digests.
	RequiredToolchainDigests map[string]string `json:"requiredToolchainDigests" url:"-"`
	// PinnedCheckpoint optionally enforces Merkle consistency from a previously observed STH.
	PinnedCheckpoint *SignedTreeHead `json:"pinnedCheckpoint" url:"-"`
	// ConsistencyProof supplies the RFC 6962 consistency proof when PinnedCheckpoint is set and smaller.
	ConsistencyProof *ConsistencyProof `json:"consistencyProof" url:"-"`
}

// bundleRef addresses a single org-scoped provenance bundle by path parameter.
type bundleRef struct {
	// ID is the server-assigned bundle identifier from the path.
	ID string `json:"id"`
}

// bundleSummary is a compact listing record for one org-owned provenance bundle.
type bundleSummary struct {
	// ID is the server-assigned bundle identifier.
	ID string `json:"id"`
	// Org is the server-stamped owner organization.
	Org string `json:"org"`
	// LogIndex is the transparency log index of this bundle.
	LogIndex uint64 `json:"logIndex"`
	// IntegratedTime is the monotonic timestamp stamped by the transparency log.
	IntegratedTime time.Time `json:"integratedTime"`
	// CreatedAt is when the bundle was stored.
	CreatedAt time.Time `json:"createdAt"`
}

// bundleListOut wraps the org-scoped bundle summaries.
type bundleListOut struct {
	// Data lists provenance bundles owned exclusively by the caller's validated org.
	Data []bundleSummary `json:"data"`
}

// revokeIn revokes a signer key ID, OIDC identity, or certificate serial within the caller's org.
// Every field carries `url:"-"` to prevent query-string injection.
type revokeIn struct {
	// KeyID is an SPKI key identifier ("sha256:<hex>") to revoke (optional).
	KeyID string `json:"keyId" url:"-"`
	// Identity is an OIDC SAN identity URI to revoke (optional).
	Identity string `json:"identity" url:"-"`
	// CertSerial is a lowercase hex X.509 certificate serial number to revoke (optional).
	CertSerial string `json:"certSerial" url:"-"`
}

// revokeOut confirms an org-scoped revocation entry.
type revokeOut struct {
	// Org is the server-stamped organization scope of the revocation.
	Org string `json:"org"`
	// RevokedKeyIDs is the total count of revoked key IDs in this org.
	RevokedKeyIDs int `json:"revokedKeyIds"`
	// RevokedIdentities is the total count of revoked OIDC identities in this org.
	RevokedIdentities int `json:"revokedIdentities"`
	// RevokedCertSerials is the total count of revoked certificate serials in this org.
	RevokedCertSerials int `json:"revokedCertSerials"`
}

// health returns the readiness and current Merkle tree checkpoint summary of the provenance subsystem.
func (o ops) health(_ context.Context, _ *struct{}) (*healthOut, error) {
	sth, err := o.s.tlog.LatestCheckpoint()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "provenance: checkpoint unavailable: %v", err)
	}
	return &healthOut{
		Status:   "ok",
		Origin:   o.s.tlog.Origin(),
		TreeSize: sth.TreeSize,
		RootHash: sth.RootHash,
	}, nil
}

// checkpoint returns the latest Ed25519 Signed Tree Head (STH), the transparency log public key PEM,
// and the Fulcio-style ephemeral Root CA certificate PEM for offline verification and witness auditing.
func (o ops) checkpoint(_ context.Context, _ *struct{}) (*checkpointOut, error) {
	sth, err := o.s.tlog.LatestCheckpoint()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "provenance: checkpoint unavailable: %v", err)
	}
	return &checkpointOut{
		Checkpoint:       sth,
		TLogPublicKeyPEM: o.s.tlogPubPEM,
		RootCAPEM:        o.s.ca.RootPEM(),
		OIDCIssuer:       o.s.oidcIssuer,
	}, nil
}

// consistency returns an RFC 6962 Merkle consistency proof between firstSize and secondSize so
// verifiers can audit append-only history and detect split-view equivocation.
func (o ops) consistency(_ context.Context, in *consistencyIn) (*ConsistencyProof, error) {
	second := in.SecondSize
	if second == 0 {
		second = o.s.tlog.Size()
	}
	proof, err := o.s.tlog.ConsistencyProof(in.FirstSize, second)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &proof, nil
}

// attest mints a short-lived Fulcio-style OIDC certificate bound to the authenticated caller and org,
// signs a canonical SLSA v1.2 DSSEv1 statement, appends it to the Merkle transparency log, and stores
// the resulting bundle strictly under the caller's validated org.
func (o ops) attest(ctx context.Context, in *attestIn) (*Bundle, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	user, ok := o.s.caller(ctx)
	if !ok || strings.TrimSpace(user) == "" {
		return nil, zip.ErrUnauthorized("a validated principal is required")
	}

	if len(in.Subjects) == 0 || len(in.Subjects) > maxSubjects {
		return nil, zip.ErrBadRequest("subjects must contain between 1 and 64 artifacts")
	}
	if len(in.Toolchain) > maxToolchain {
		return nil, zip.ErrBadRequest("too many toolchain components (max 64)")
	}
	if len(in.Source.URI) > maxFieldLen || len(in.BuilderID) > maxFieldLen || len(in.InvocationID) > maxFieldLen {
		return nil, zip.ErrBadRequest("field length exceeds limit")
	}

	now := o.s.clock().UTC()
	started := in.StartedOn
	if started.IsZero() {
		started = now
	}
	finished := in.FinishedOn
	if finished.IsZero() {
		finished = now
	}

	// Server-minted OIDC identity bound exclusively to the authenticated org and principal.
	builderIdentity := PrincipalIdentityURI(org, user)

	stmt, err := NewStatement(
		in.Subjects,
		BuildDefinition{
			BuildType: in.BuildType,
			ExternalParameters: ExternalParameters{
				Source:          in.Source,
				BuilderIdentity: builderIdentity,
				Toolchain:       in.Toolchain,
				InvocationOrg:   org,
			},
		},
		RunDetails{
			Builder: Builder{
				ID:      in.BuilderID,
				Version: in.BuilderVersion,
			},
			Metadata: BuildMetadata{
				InvocationID: in.InvocationID,
				StartedOn:    started.UTC(),
				FinishedOn:   finished.UTC(),
			},
		},
	)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}

	var kp *KeyPair
	switch strings.ToLower(strings.TrimSpace(in.Algorithm)) {
	case "", AlgorithmEd25519:
		kp, err = GenerateEd25519KeyPair()
	case AlgorithmECDSAP256:
		kp, err = GenerateECDSAP256KeyPair()
	default:
		return nil, zip.ErrBadRequest("unsupported algorithm (use ed25519 or ecdsa-p256-sha256)")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "provenance: keygen: %v", err)
	}

	_, certPEM, err := o.s.ca.IssueCodeSigningCert(kp.PublicKey(), OIDCIdentity{
		Subject:                builderIdentity,
		Issuer:                 o.s.oidcIssuer,
		BuildSignerURI:         in.BuilderID,
		SourceRepositoryURI:    in.Source.URI,
		SourceRepositoryDigest: in.Source.Commit,
	}, now.Add(-1*time.Second), DefaultCertTTL)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "provenance: issue cert: %v", err)
	}

	id, err := newBundleID()
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "provenance: id: %v", err)
	}

	attestNow := time.Time{}
	if o.s.customClk {
		attestNow = now
	}
	bundle, err := AttestStatement(stmt, AttestOptions{
		ID:      id,
		Org:     org,
		Signer:  kp,
		CertPEM: certPEM,
		TLog:    o.s.tlog,
		Now:     attestNow,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "provenance: attest: %v", err)
	}

	o.s.mu.Lock()
	orgMap := o.s.bundles[org]
	if orgMap == nil {
		orgMap = make(map[string]Bundle)
		o.s.bundles[org] = orgMap
	}
	if len(orgMap) >= maxBundles {
		o.s.mu.Unlock()
		return nil, zip.Errorf(http.StatusInsufficientStorage, "provenance: org bundle capacity reached")
	}
	orgMap[id] = bundle.Clone()
	o.s.mu.Unlock()

	return bundle, nil
}

// verify executes fail-closed SLSA v1.2, DSSE, Fulcio-style X.509 certificate, revocation, and
// RFC 6962 Merkle transparency log verification for an artifact digest. Refusals answer 422.
func (o ops) verify(ctx context.Context, in *verifyIn) (*VerificationResult, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}

	var bundle *Bundle
	if in.Bundle != nil {
		b := in.Bundle.Clone()
		bundle = &b
	} else if strings.TrimSpace(in.BundleID) != "" {
		o.s.mu.RLock()
		stored, ok := o.s.bundles[org][strings.TrimSpace(in.BundleID)]
		o.s.mu.RUnlock()
		if !ok {
			return nil, zip.ErrNotFound("provenance bundle not found")
		}
		b := stored.Clone()
		bundle = &b
	} else {
		return nil, zip.ErrBadRequest("either bundle or bundleId is required")
	}

	revKeys, revIDs, revSerials := o.s.snapshotRevocations(org)
	policy := &VerificationPolicy{
		TrustedCertRoots:         o.s.ca.CertPool(),
		AllowedOIDCIssuers:       []string{o.s.oidcIssuer},
		AllowedIdentities:        in.AllowedIdentities,
		AllowedBuilders:          in.AllowedBuilders,
		AllowedSourceURIs:        in.AllowedSourceURIs,
		RequiredBranch:           in.RequiredBranch,
		RequiredCommit:           in.RequiredCommit,
		RequiredTree:             in.RequiredTree,
		RequiredToolchainDigests: in.RequiredToolchainDigests,
		RevokedKeyIDs:            revKeys,
		RevokedIdentities:        revIDs,
		RevokedCertSerials:       revSerials,
		TLogPublicKey:            o.s.tlog.PublicKey(),
		TLogOrigin:               o.s.tlog.Origin(),
		PinnedCheckpoint:         in.PinnedCheckpoint,
	}

	res, err := VerifyBundle(in.ArtifactDigest, in.ArtifactName, bundle, in.ConsistencyProof, policy)
	if err != nil {
		if errors.Is(err, ErrPolicyInvalid) {
			return nil, zip.ErrBadRequest(fmt.Sprintf("%s: %v", ErrorCode(err), err))
		}
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s: %v", ErrorCode(err), err)
	}
	return res, nil
}

// listBundles returns provenance bundle summaries belonging exclusively to the caller's validated org.
func (o ops) listBundles(ctx context.Context, _ *struct{}) (*bundleListOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	o.s.mu.RLock()
	orgMap := o.s.bundles[org]
	out := make([]bundleSummary, 0, len(orgMap))
	for _, b := range orgMap {
		out = append(out, bundleSummary{
			ID:             b.ID,
			Org:            b.Org,
			LogIndex:       b.LogEntry.LogIndex,
			IntegratedTime: b.LogEntry.IntegratedTime,
			CreatedAt:      b.CreatedAt,
		})
	}
	o.s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].LogIndex == out[j].LogIndex {
			return out[i].ID < out[j].ID
		}
		return out[i].LogIndex < out[j].LogIndex
	})
	return &bundleListOut{Data: out}, nil
}

// getBundle returns one provenance bundle belonging to the caller's validated org.
// A bundle owned by another org answers 404.
func (o ops) getBundle(ctx context.Context, in *bundleRef) (*Bundle, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	o.s.mu.RLock()
	b, ok := o.s.bundles[org][id]
	o.s.mu.RUnlock()
	if !ok {
		return nil, zip.ErrNotFound("provenance bundle not found")
	}
	out := b.Clone()
	return &out, nil
}

// revoke registers an org-scoped signer key ID, OIDC identity, or X.509 certificate serial
// revocation enforced fail-closed on subsequent verifications for the caller's validated org.
func (o ops) revoke(ctx context.Context, in *revokeIn) (*revokeOut, error) {
	org, err := o.s.acting(ctx)
	if err != nil {
		return nil, err
	}
	keyID := strings.TrimSpace(in.KeyID)
	identity := strings.TrimSpace(in.Identity)
	serial := strings.ToLower(strings.TrimSpace(in.CertSerial))
	if keyID == "" && identity == "" && serial == "" {
		return nil, zip.ErrBadRequest("at least one of keyId, identity, or certSerial is required")
	}
	if len(keyID) > maxFieldLen || len(identity) > maxFieldLen || len(serial) > maxFieldLen {
		return nil, zip.ErrBadRequest("revocation identifier too long")
	}

	o.s.mu.Lock()
	rev := o.s.revocations[org]
	if rev == nil {
		rev = &orgRevocations{
			keyIDs:      make(map[string]bool),
			identities:  make(map[string]bool),
			certSerials: make(map[string]bool),
		}
		o.s.revocations[org] = rev
	}
	if keyID != "" {
		rev.keyIDs[keyID] = true
	}
	if identity != "" {
		rev.identities[identity] = true
	}
	if serial != "" {
		rev.certSerials[serial] = true
	}
	out := &revokeOut{
		Org:                org,
		RevokedKeyIDs:      len(rev.keyIDs),
		RevokedIdentities:  len(rev.identities),
		RevokedCertSerials: len(rev.certSerials),
	}
	o.s.mu.Unlock()
	return out, nil
}

func (s *Service) snapshotRevocations(org string) (map[string]bool, map[string]bool, map[string]bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rev := s.revocations[org]
	if rev == nil {
		return nil, nil, nil
	}
	keys := make(map[string]bool, len(rev.keyIDs))
	for k, v := range rev.keyIDs {
		keys[k] = v
	}
	ids := make(map[string]bool, len(rev.identities))
	for k, v := range rev.identities {
		ids[k] = v
	}
	serials := make(map[string]bool, len(rev.certSerials))
	for k, v := range rev.certSerials {
		serials[k] = v
	}
	return keys, ids, serials
}

// PrincipalIdentityURI builds the canonical server-minted OIDC SAN URI for an authenticated caller.
func PrincipalIdentityURI(org, user string) string {
	return "https://hanzo.ai/principals/" + url.PathEscape(org) + "/" + url.PathEscape(user)
}

// ErrorCode returns a stable machine-readable refusal code for a provenance verification error.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrDigestMismatch):
		return "DIGEST_MISMATCH"
	case errors.Is(err, ErrArtifactNotInSubject):
		return "ARTIFACT_NOT_IN_SUBJECT"
	case errors.Is(err, ErrStatementInvalid):
		return "STATEMENT_INVALID"
	case errors.Is(err, ErrPayloadTypeMismatch):
		return "PAYLOAD_TYPE_MISMATCH"
	case errors.Is(err, ErrInvalidSignature):
		return "INVALID_SIGNATURE"
	case errors.Is(err, ErrUntrustedSigner):
		return "UNTRUSTED_SIGNER"
	case errors.Is(err, ErrCertInvalid):
		return "CERT_INVALID"
	case errors.Is(err, ErrRevokedSigner):
		return "REVOKED_SIGNER"
	case errors.Is(err, ErrSourceMismatch):
		return "SOURCE_MISMATCH"
	case errors.Is(err, ErrToolchainMismatch):
		return "TOOLCHAIN_MISMATCH"
	case errors.Is(err, ErrBuilderMismatch):
		return "BUILDER_MISMATCH"
	case errors.Is(err, ErrTLogInvalid):
		return "TLOG_INVALID"
	case errors.Is(err, ErrPolicyInvalid):
		return "POLICY_INVALID"
	default:
		return "VERIFICATION_FAILED"
	}
}

func newBundleID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "prov_" + hex.EncodeToString(raw[:]), nil
}
