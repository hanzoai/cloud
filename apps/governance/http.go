// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Deps holds everything the governance subsystem reads from its host. It
// imports zip and luxfi/log only, never github.com/hanzoai/cloud, so the same
// package mounts unchanged in any host.
type Deps struct {
	// Log is the host's scoped logger.
	Log luxlog.Logger

	// Org returns the validated org of the caller on ctx (ok=false if anonymous).
	Org func(ctx context.Context) (org string, ok bool)

	// Acting returns the validated org a write acts for, or a zip.HTTPError
	// (401 when unauthenticated, 403 when missing org).
	Acting func(ctx context.Context) (org string, err error)

	// Caller resolves the authenticated (userID, org) pair from ctx. When nil,
	// defaults to reading zip.CallerOf(ctx). Mutating endpoints NEVER trust
	// caller or org identifiers supplied in JSON request bodies.
	Caller func(ctx context.Context) (userID string, org string, ok bool)

	// Engine is the governance state machine to expose. When nil, Mount
	// initializes a self-contained local-sovereign genesis engine with zero
	// Hanzo-admin overrides.
	Engine *Engine

	// ExporterID and ExporterKey optionally allow GET /v1/governance/export to
	// emit a signed SovereignBundle. When unset, export fails closed with 503.
	ExporterID  string
	ExporterKey ed25519.PrivateKey
}

// Mount wires the /v1/governance typed HTTP/OpenAPI/MCP surface onto app.
func Mount(app *zip.App, deps Deps) error {
	if app == nil {
		return errors.New("governance: nil app")
	}
	if deps.Log == nil || deps.Org == nil || deps.Acting == nil {
		return errors.New("governance: Deps requires Log, Org, and Acting")
	}
	if deps.Caller == nil {
		deps.Caller = func(ctx context.Context) (string, string, bool) {
			c := zip.CallerOf(ctx)
			if strings.TrimSpace(c.User) == "" || strings.TrimSpace(c.Org) == "" {
				return "", "", false
			}
			return strings.TrimSpace(c.User), strings.TrimSpace(c.Org), true
		}
	}
	if deps.Engine == nil {
		eng, err := defaultLocalEngine()
		if err != nil {
			return fmt.Errorf("governance.Mount: init local engine: %w", err)
		}
		deps.Engine = eng
	}

	o := ops{deps: deps}
	g := app.Group("/v1/governance")
	g.Get("/health", o.health)
	g.Get("/config", o.getConfig)
	g.Get("/parameters", o.listParameters)
	g.Get("/models", o.listModels)
	g.Get("/verifiers", o.listVerifiers)
	g.Get("/treasury", o.getTreasury)
	g.Get("/members", o.listMembers)
	g.Get("/proposals", o.listProposals)
	g.Post("/proposals", o.submitProposal, zip.WithStatus(http.StatusCreated))
	g.Get("/proposals/:id", o.getProposal)
	g.Post("/proposals/:id/votes", o.castVote, zip.WithStatus(http.StatusCreated))
	g.Post("/proposals/:id/queue", o.queueProposal)
	g.Post("/proposals/:id/execute", o.executeProposal)
	g.Post("/proposals/:id/cancel", o.cancelProposal)
	g.Post("/delegations", o.delegate)
	g.Get("/emergency", o.listEmergency)
	g.Post("/emergency", o.triggerEmergency, zip.WithStatus(http.StatusCreated))
	g.Get("/ledger", o.getLedger)
	g.Get("/export", o.exportBundle)
	g.Post("/fork/verify", o.verifyBundle)

	deps.Log.Info("governance mounted",
		"prefix", "/v1/governance",
		"protocol_id", deps.Engine.ProtocolID(),
		"no_admin_override", deps.Engine.Config().NoAdminOverride)
	return nil
}

// defaultLocalEngine boots an independent local-sovereign governance engine
// with an ephemeral local genesis member key and zero Hanzo-hosted dependency.
func defaultLocalEngine() (*Engine, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	cfg := DefaultProductionConfig("hanzo-local-governance")
	members := []Member{
		{
			ID:                  "local-genesis",
			PublicKeyHex:        hex.EncodeToString(pub),
			VotingPower:         1000,
			IsEmergencyGuardian: true,
			Active:              true,
		},
	}
	minFee := 0.0
	maxFee := 1000.0
	params := map[string]ParameterEntry{
		"max_inference_fee_bps": {
			Key:         "max_inference_fee_bps",
			Value:       "250",
			ValueType:   "uint64",
			MinNumeric:  &minFee,
			MaxNumeric:  &maxFee,
			Description: "Maximum protocol inference fee in basis points",
		},
	}
	return NewEngine(cfg, members, params, 0)
}

// ops binds the typed /v1/governance operations.
type ops struct {
	deps Deps
}

func (o ops) authenticatedCaller(ctx context.Context) (userID string, org string, err error) {
	actingOrg, err := o.deps.Acting(ctx)
	if err != nil {
		return "", "", err
	}
	u, cOrg, ok := o.deps.Caller(ctx)
	if !ok || strings.TrimSpace(u) == "" {
		return "", "", zip.ErrUnauthorized("authenticated governance caller identity is required")
	}
	if strings.TrimSpace(cOrg) != "" && strings.TrimSpace(cOrg) != strings.TrimSpace(actingOrg) {
		return "", "", zip.ErrForbidden("caller org does not match acting org")
	}
	return strings.TrimSpace(u), strings.TrimSpace(actingOrg), nil
}

// ── Typed Request / Response DTOs ────────────────────────────────────────────

type emptyIn struct{}

// HealthOut reports liveness and key protocol sovereignty invariants.
type HealthOut struct {
	// Status is "ok" when the governance engine is ready.
	Status string `json:"status"`
	// ProtocolID is the active domain-separated protocol identifier.
	ProtocolID string `json:"protocol_id"`
	// NetworkVersion is the currently active protocol version.
	NetworkVersion string `json:"network_version"`
	// NoAdminOverride is always true; permanent Hanzo-admin overrides are forbidden.
	NoAdminOverride bool `json:"no_admin_override"`
	// SnapshotID is the latest voting power checkpoint height.
	SnapshotID uint64 `json:"snapshot_id"`
	// ProposalCount is the total number of recorded proposals.
	ProposalCount int `json:"proposal_count"`
	// LedgerHeadHash is the SHA-256 head of the hash-chained audit ledger.
	LedgerHeadHash string `json:"ledger_head_hash"`
}

// ParametersOut wraps the governed network parameter registry.
type ParametersOut struct {
	// Parameters are sorted deterministically by key.
	Parameters []ParameterEntry `json:"parameters"`
}

// ModelsOut wraps the governed open model registry.
type ModelsOut struct {
	// Models are sorted deterministically by model_id.
	Models []ModelRegistryEntry `json:"models"`
}

// VerifiersOut wraps the governed verifier standards registry.
type VerifiersOut struct {
	// Verifiers are sorted deterministically by profile_id.
	Verifiers []VerifierStandardEntry `json:"verifiers"`
}

// MembersOut wraps registered voting members and the current power checkpoint.
type MembersOut struct {
	// Snapshot is the current voting power checkpoint.
	Snapshot PowerCheckpoint `json:"snapshot"`
	// Members are sorted deterministically by member ID.
	Members []Member `json:"members"`
}

// ProposalsOut wraps all proposals with their live evaluated states.
type ProposalsOut struct {
	// Proposals are sorted chronologically.
	Proposals []Proposal `json:"proposals"`
}

// ProposalRef addresses a single proposal by path parameter :id.
type ProposalRef struct {
	// ID is the proposal identifier from the URL path.
	ID string `json:"id"`
}

// SubmitProposalIn carries a cryptographically signed proposal body.
// ProposerID and Org are NEVER accepted from the body — they are strictly
// bound to the authenticated request principal.
type SubmitProposalIn struct {
	// ProposalID is the unique identifier for the new proposal.
	ProposalID string `json:"proposal_id" url:"-"`
	// Kind is the typed governance proposal category.
	Kind ProposalKind `json:"kind" url:"-"`
	// Title is a concise human-readable title (1..256 chars).
	Title string `json:"title" url:"-"`
	// Description explains the proposal rationale.
	Description string `json:"description" url:"-"`
	// Payload holds the single typed proposal payload arm matching Kind.
	Payload ProposalPayload `json:"payload" url:"-"`
	// CreatedAt is the RFC3339 timestamp signed by the proposer.
	CreatedAt time.Time `json:"created_at" url:"-"`
	// Nonce is a unique per-member replay-prevention token.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical proposal bytes.
	Signature string `json:"signature" url:"-"`
}

// CastVoteIn carries a cryptographically signed ballot for proposal :id.
// VoterID and Org are strictly bound from the authenticated caller context.
type CastVoteIn struct {
	// ID is the target proposal identifier from the URL path.
	ID string `json:"id"`
	// Support is one of "for", "against", or "abstain".
	Support VoteSupport `json:"support" url:"-"`
	// Reason is an optional human-readable rationale.
	Reason string `json:"reason,omitempty" url:"-"`
	// CastAt is the RFC3339 timestamp signed by the voter.
	CastAt time.Time `json:"cast_at" url:"-"`
	// Nonce is a unique per-member replay-prevention token.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical vote bytes.
	Signature string `json:"signature" url:"-"`
}

// QueueProposalIn carries a signed timelock queue request for proposal :id.
type QueueProposalIn struct {
	// ID is the target proposal identifier from the URL path.
	ID string `json:"id"`
	// QueuedAt is the RFC3339 timestamp signed by the caller.
	QueuedAt time.Time `json:"queued_at" url:"-"`
	// Nonce is a unique per-member replay-prevention token.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical queue bytes.
	Signature string `json:"signature" url:"-"`
}

// ExecuteProposalIn carries a signed execution request for a matured proposal :id.
type ExecuteProposalIn struct {
	// ID is the target proposal identifier from the URL path.
	ID string `json:"id"`
	// ExecutedAt is the RFC3339 timestamp signed by the executor.
	ExecutedAt time.Time `json:"executed_at" url:"-"`
	// Nonce is a unique per-member replay-prevention token.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical execute bytes.
	Signature string `json:"signature" url:"-"`
}

// CancelProposalIn carries a signed cancellation request for proposal :id.
type CancelProposalIn struct {
	// ID is the target proposal identifier from the URL path.
	ID string `json:"id"`
	// Reason explains why the proposal is being canceled.
	Reason string `json:"reason,omitempty" url:"-"`
	// CanceledAt is the RFC3339 timestamp signed by the caller.
	CanceledAt time.Time `json:"canceled_at" url:"-"`
	// Nonce is a unique per-member replay-prevention token.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical cancel bytes.
	Signature string `json:"signature" url:"-"`
}

// DelegateIn carries a signed voting power delegation instruction.
// DelegatorID and Org are strictly bound from the authenticated caller context.
type DelegateIn struct {
	// DelegateeID is the target active member ID (empty or self resets to self-delegation).
	DelegateeID string `json:"delegatee_id" url:"-"`
	// IssuedAt is the RFC3339 timestamp signed by the delegator.
	IssuedAt time.Time `json:"issued_at" url:"-"`
	// Nonce is a unique per-member replay-prevention token.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical delegation bytes.
	Signature string `json:"signature" url:"-"`
}

// EmergencyIn carries a signed bounded emergency safeguard invocation.
// GuardianID and Org are strictly bound from the authenticated caller context.
type EmergencyIn struct {
	// ActionID is a unique identifier for the emergency safeguard action.
	ActionID string `json:"action_id" url:"-"`
	// Kind must be "pause_subsystem" or "circuit_breaker".
	Kind EmergencyActionKind `json:"kind" url:"-"`
	// TargetSubsystem is the narrow non-governance subsystem to pause.
	TargetSubsystem string `json:"target_subsystem" url:"-"`
	// DurationMs is the bounded duration in milliseconds (must be <= max_emergency_duration).
	DurationMs int64 `json:"duration_ms" url:"-"`
	// Reason explains the emergency condition.
	Reason string `json:"reason" url:"-"`
	// IssuedAt is the RFC3339 timestamp signed by the guardian.
	IssuedAt time.Time `json:"issued_at" url:"-"`
	// Nonce is a unique per-member replay-prevention token.
	Nonce string `json:"nonce" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical emergency bytes.
	Signature string `json:"signature" url:"-"`
}

// EmergencyOut wraps active and historical bounded emergency actions.
type EmergencyOut struct {
	// Actions are sorted deterministically by action_id.
	Actions []EmergencyAction `json:"actions"`
}

// LedgerOut wraps the hash-chained governance audit ledger.
type LedgerOut struct {
	// HeadHash is the SHA-256 digest of the latest ledger record.
	HeadHash string `json:"head_hash"`
	// Entries are ordered contiguously from sequence 1.
	Entries []LedgerEntry `json:"entries"`
}

// VerifyBundleIn carries a SovereignBundle to cryptographically verify.
type VerifyBundleIn struct {
	// Bundle is the exported sovereign state + ledger package to verify.
	Bundle SovereignBundle `json:"bundle" url:"-"`
}

// VerifyBundleOut summarizes cryptographic verification of a SovereignBundle.
type VerifyBundleOut struct {
	// Valid is true when every signature, state digest, snapshot root, and ledger link verified.
	Valid bool `json:"valid"`
	// ProtocolID is the verified source protocol identifier.
	ProtocolID string `json:"protocol_id"`
	// BundleHash is the verified SHA-256 bundle hash.
	BundleHash string `json:"bundle_hash"`
	// LedgerHeadHash is the verified audit ledger head hash.
	LedgerHeadHash string `json:"ledger_head_hash"`
	// ProposalCount is the number of verified proposals in the bundle.
	ProposalCount int `json:"proposal_count"`
	// LedgerEntries is the number of verified hash-chained ledger entries.
	LedgerEntries int `json:"ledger_entries"`
}

// ── Typed Operations ─────────────────────────────────────────────────────────

// health returns liveness and core protocol sovereignty invariants.
func (o ops) health(_ context.Context, _ *emptyIn) (*HealthOut, error) {
	cfg := o.deps.Engine.Config()
	snap := o.deps.Engine.CurrentSnapshot()
	return &HealthOut{
		Status:          "ok",
		ProtocolID:      cfg.ProtocolID,
		NetworkVersion:  cfg.NetworkVersion,
		NoAdminOverride: cfg.NoAdminOverride,
		SnapshotID:      snap.SnapshotID,
		ProposalCount:   len(o.deps.Engine.Proposals()),
		LedgerHeadHash:  o.deps.Engine.LedgerHeadHash(),
	}, nil
}

// getConfig returns the active governance configuration and immutable safety floors.
func (o ops) getConfig(_ context.Context, _ *emptyIn) (*GovernanceConfig, error) {
	cfg := o.deps.Engine.Config()
	return &cfg, nil
}

// listParameters returns all governed network parameters and their validation bounds.
func (o ops) listParameters(_ context.Context, _ *emptyIn) (*ParametersOut, error) {
	return &ParametersOut{Parameters: o.deps.Engine.Parameters()}, nil
}

// listModels returns all open model registry entries governed by the DAO.
func (o ops) listModels(_ context.Context, _ *emptyIn) (*ModelsOut, error) {
	return &ModelsOut{Models: o.deps.Engine.Models()}, nil
}

// listVerifiers returns all verifier compliance standards governed by the DAO.
func (o ops) listVerifiers(_ context.Context, _ *emptyIn) (*VerifiersOut, error) {
	return &VerifiersOut{Verifiers: o.deps.Engine.VerifierStandards()}, nil
}

// getTreasury returns the current treasury balance, per-epoch spending totals, and disbursement log.
func (o ops) getTreasury(_ context.Context, _ *emptyIn) (*TreasuryState, error) {
	t := o.deps.Engine.Treasury()
	return &t, nil
}

// listMembers returns registered voting members and the current delegated voting power snapshot.
func (o ops) listMembers(_ context.Context, _ *emptyIn) (*MembersOut, error) {
	return &MembersOut{
		Snapshot: o.deps.Engine.CurrentSnapshot(),
		Members:  o.deps.Engine.Members(),
	}, nil
}

// listProposals lists all governance proposals with their live evaluated states.
func (o ops) listProposals(_ context.Context, _ *emptyIn) (*ProposalsOut, error) {
	return &ProposalsOut{Proposals: o.deps.Engine.Proposals()}, nil
}

// getProposal retrieves a single governance proposal and its live evaluated state.
func (o ops) getProposal(_ context.Context, in *ProposalRef) (*Proposal, error) {
	prop, err := o.deps.Engine.Proposal(in.ID)
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return prop, nil
}

// submitProposal validates the authenticated caller, verifies the Ed25519 signature, and creates a snapshot-locked proposal.
func (o ops) submitProposal(ctx context.Context, in *SubmitProposalIn) (*Proposal, error) {
	userID, org, err := o.authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	prop, err := o.deps.Engine.SubmitProposal(ProposalRequest{
		ProposalID:  in.ProposalID,
		ProposerID:  userID,
		Org:         org,
		Kind:        in.Kind,
		Title:       in.Title,
		Description: in.Description,
		Payload:     in.Payload,
		CreatedAt:   in.CreatedAt,
		Nonce:       in.Nonce,
		Signature:   in.Signature,
	})
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return prop, nil
}

// castVote validates the authenticated voter, verifies the Ed25519 signature, and records a snapshot-weighted ballot.
func (o ops) castVote(ctx context.Context, in *CastVoteIn) (*VoteReceipt, error) {
	userID, org, err := o.authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := o.deps.Engine.CastVote(VoteRequest{
		ProposalID: in.ID,
		VoterID:    userID,
		Org:        org,
		Support:    in.Support,
		Reason:     in.Reason,
		CastAt:     in.CastAt,
		Nonce:      in.Nonce,
		Signature:  in.Signature,
	})
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return rec, nil
}

// queueProposal enqueues a succeeded proposal into the mandatory timelock.
func (o ops) queueProposal(ctx context.Context, in *QueueProposalIn) (*Proposal, error) {
	userID, org, err := o.authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	prop, err := o.deps.Engine.QueueProposal(QueueRequest{
		ProposalID: in.ID,
		CallerID:   userID,
		Org:        org,
		QueuedAt:   in.QueuedAt,
		Nonce:      in.Nonce,
		Signature:  in.Signature,
	})
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return prop, nil
}

// executeProposal executes a matured timelocked proposal after MinTimelockDelay and before GracePeriod expiry.
func (o ops) executeProposal(ctx context.Context, in *ExecuteProposalIn) (*ExecutionReceipt, error) {
	userID, org, err := o.authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := o.deps.Engine.ExecuteProposal(ExecuteRequest{
		ProposalID: in.ID,
		ExecutorID: userID,
		Org:        org,
		ExecutedAt: in.ExecutedAt,
		Nonce:      in.Nonce,
		Signature:  in.Signature,
	})
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return rec, nil
}

// cancelProposal cancels an eligible unexecuted proposal by its proposer or when proposer power falls below threshold.
func (o ops) cancelProposal(ctx context.Context, in *CancelProposalIn) (*Proposal, error) {
	userID, org, err := o.authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	prop, err := o.deps.Engine.CancelProposal(CancelRequest{
		ProposalID: in.ID,
		CallerID:   userID,
		Org:        org,
		Reason:     in.Reason,
		CanceledAt: in.CanceledAt,
		Nonce:      in.Nonce,
		Signature:  in.Signature,
	})
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return prop, nil
}

// delegate updates the authenticated member's voting power delegation and captures a new snapshot.
func (o ops) delegate(ctx context.Context, in *DelegateIn) (*PowerCheckpoint, error) {
	userID, org, err := o.authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := o.deps.Engine.Delegate(DelegationRequest{
		DelegatorID: userID,
		DelegateeID: in.DelegateeID,
		Org:         org,
		IssuedAt:    in.IssuedAt,
		Nonce:       in.Nonce,
		Signature:   in.Signature,
	})
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return &snap, nil
}

// listEmergency lists all active and historical bounded emergency safeguard actions.
func (o ops) listEmergency(_ context.Context, _ *emptyIn) (*EmergencyOut, error) {
	return &EmergencyOut{Actions: o.deps.Engine.EmergencyActions()}, nil
}

// triggerEmergency triggers a bounded, auto-expiring emergency pause or circuit breaker by an authorized guardian.
func (o ops) triggerEmergency(ctx context.Context, in *EmergencyIn) (*EmergencyAction, error) {
	userID, org, err := o.authenticatedCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.DurationMs <= 0 || in.DurationMs > MaxAllowedEmergencyDuration.Milliseconds() {
		return nil, mapEngineErr(fmt.Errorf("%w: duration_ms %d out of bounds (1..%d)", ErrEmergencyAbuse, in.DurationMs, MaxAllowedEmergencyDuration.Milliseconds()))
	}
	action, err := o.deps.Engine.TriggerEmergency(EmergencyActionRequest{
		ActionID:        in.ActionID,
		GuardianID:      userID,
		Org:             org,
		Kind:            in.Kind,
		TargetSubsystem: in.TargetSubsystem,
		Duration:        time.Duration(in.DurationMs) * time.Millisecond,
		Reason:          in.Reason,
		IssuedAt:        in.IssuedAt,
		Nonce:           in.Nonce,
		Signature:       in.Signature,
	})
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return action, nil
}

// getLedger returns the hash-chained governance audit ledger entries and head hash.
func (o ops) getLedger(_ context.Context, _ *emptyIn) (*LedgerOut, error) {
	return &LedgerOut{
		HeadHash: o.deps.Engine.LedgerHeadHash(),
		Entries:  o.deps.Engine.LedgerEntries(),
	}, nil
}

// exportBundle exports a signed sovereign snapshot bundle for backup, migration, or independent community fork.
func (o ops) exportBundle(_ context.Context, _ *emptyIn) (*SovereignBundle, error) {
	if o.deps.ExporterID == "" || len(o.deps.ExporterKey) != ed25519.PrivateKeySize {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "governance: exporter signing key not configured")
	}
	bundle, err := o.deps.Engine.ExportSovereignBundle(o.deps.ExporterID, o.deps.ExporterKey)
	if err != nil {
		return nil, mapEngineErr(err)
	}
	return bundle, nil
}

// verifyBundle cryptographically verifies a SovereignBundle's signatures, state digest, snapshots, and audit ledger chain.
func (o ops) verifyBundle(_ context.Context, in *VerifyBundleIn) (*VerifyBundleOut, error) {
	if err := VerifySovereignBundle(&in.Bundle); err != nil {
		return nil, mapEngineErr(err)
	}
	return &VerifyBundleOut{
		Valid:          true,
		ProtocolID:     in.Bundle.ProtocolID,
		BundleHash:     in.Bundle.BundleHash,
		LedgerHeadHash: in.Bundle.LedgerHeadHash,
		ProposalCount:  len(in.Bundle.State.Proposals),
		LedgerEntries:  len(in.Bundle.Ledger),
	}, nil
}

func mapEngineErr(err error) error {
	switch {
	case errors.Is(err, ErrProposalNotFound):
		return zip.Errorf(http.StatusNotFound, "%v", err)
	case errors.Is(err, ErrInvalidSignature),
		errors.Is(err, ErrUnknownMember),
		errors.Is(err, ErrInsufficientPower),
		errors.Is(err, ErrUnauthorizedCancel),
		errors.Is(err, ErrEmergencyAbuse),
		errors.Is(err, ErrAdminOverrideForbidden),
		errors.Is(err, ErrProtocolInvariant),
		errors.Is(err, ErrTreasuryPolicy):
		return zip.Errorf(http.StatusForbidden, "%v", err)
	case errors.Is(err, ErrAlreadyVoted),
		errors.Is(err, ErrNonceReplay),
		errors.Is(err, ErrInvalidState),
		errors.Is(err, ErrTimelockNotReady),
		errors.Is(err, ErrProposalExpired),
		errors.Is(err, ErrEmergencyCooldown),
		errors.Is(err, ErrSubsystemPaused):
		return zip.Errorf(http.StatusConflict, "%v", err)
	default:
		return zip.Errorf(http.StatusBadRequest, "%v", err)
	}
}
