// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// BundleVersion identifies the canonical sovereign export bundle format.
const BundleVersion = "hip-1352.v1"

// MaxAllowedEmergencyDuration is the hard protocol ceiling on any single
// unratified emergency safeguard duration (72 hours).
const MaxAllowedEmergencyDuration = 72 * time.Hour

// Sentinel errors returned by the governance engine.
var (
	ErrInvalidConfig          = errors.New("governance: invalid configuration")
	ErrAdminOverrideForbidden = errors.New("governance: permanent admin override is forbidden by protocol")
	ErrInvalidSignature       = errors.New("governance: invalid ed25519 signature")
	ErrNonceReplay            = errors.New("governance: nonce already used")
	ErrUnknownMember          = errors.New("governance: unknown or inactive member")
	ErrInsufficientPower      = errors.New("governance: insufficient voting power")
	ErrInvalidProposal        = errors.New("governance: invalid proposal")
	ErrProposalNotFound       = errors.New("governance: proposal not found")
	ErrInvalidState           = errors.New("governance: invalid proposal state transition")
	ErrAlreadyVoted           = errors.New("governance: voter already cast ballot on proposal")
	ErrTimelockNotReady       = errors.New("governance: timelock delay has not elapsed")
	ErrProposalExpired        = errors.New("governance: queued proposal grace period expired")
	ErrUnauthorizedCancel     = errors.New("governance: caller not authorized to cancel proposal")
	ErrEmergencyAbuse         = errors.New("governance: emergency action violates safeguard bounds")
	ErrEmergencyCooldown      = errors.New("governance: emergency cooldown active for target subsystem")
	ErrSubsystemPaused        = errors.New("governance: target subsystem is paused by active emergency safeguard")
	ErrTreasuryPolicy         = errors.New("governance: treasury disbursement violates policy")
	ErrProtocolInvariant      = errors.New("governance: immutable protocol safety invariant violated")
	ErrInvalidBundle          = errors.New("governance: invalid or tampered sovereign bundle")
	ErrInvalidLedger          = errors.New("governance: invalid or tampered audit ledger")
)

// ProposalKind enumerates the typed governance proposal categories.
type ProposalKind string

const (
	ProposalParameterUpdate      ProposalKind = "parameter_update"
	ProposalModelRegistry        ProposalKind = "model_registry"
	ProposalVerifierStandard     ProposalKind = "verifier_standard"
	ProposalTreasuryDisbursement ProposalKind = "treasury_disbursement"
	ProposalEmergencyRatification ProposalKind = "emergency_ratification"
	ProposalProtocolUpgrade      ProposalKind = "protocol_upgrade"
)

// ProposalState represents the strict OpenZeppelin-style proposal lifecycle.
type ProposalState string

const (
	StatePending   ProposalState = "pending"
	StateActive    ProposalState = "active"
	StateDefeated  ProposalState = "defeated"
	StateSucceeded ProposalState = "succeeded"
	StateQueued    ProposalState = "queued"
	StateExecuted  ProposalState = "executed"
	StateCanceled  ProposalState = "canceled"
	StateExpired   ProposalState = "expired"
)

// VoteSupport is the discrete vote choice on a proposal.
type VoteSupport string

const (
	VoteAgainst VoteSupport = "against"
	VoteFor     VoteSupport = "for"
	VoteAbstain VoteSupport = "abstain"
)

// EmergencyActionKind restricts guardians strictly to bounded pause and
// circuit-breaker actions.
type EmergencyActionKind string

const (
	EmergencyPauseSubsystem EmergencyActionKind = "pause_subsystem"
	EmergencyCircuitBreaker EmergencyActionKind = "circuit_breaker"
)

// VerificationMethod explicitly distinguishes cryptographic proofs, hardware
// attestations, and economic incentives so they are never conflated.
type VerificationMethod string

const (
	VerifyCryptographicProof   VerificationMethod = "cryptographic_proof"
	VerifyHardwareAttestation  VerificationMethod = "hardware_attestation"
	VerifyEconomicIncentive    VerificationMethod = "economic_incentive"
	VerifyHybridDeterministic  VerificationMethod = "hybrid_deterministic"
)

// GovernanceConfig defines protocol governance parameters and immutable safety
// floors. No proposal or guardian may lower QuorumBps below MinQuorumBpsFloor,
// lower MinTimelockDelay below MinTimelockFloor, or enable an admin override.
type GovernanceConfig struct {
	ProtocolID                    string          `json:"protocol_id"`
	NetworkVersion                string          `json:"network_version"`
	VotingDelay                   time.Duration   `json:"voting_delay"`
	VotingPeriod                  time.Duration   `json:"voting_period"`
	MinTimelockDelay              time.Duration   `json:"min_timelock_delay"`
	MinTimelockFloor              time.Duration   `json:"min_timelock_floor"`
	GracePeriod                   time.Duration   `json:"grace_period"`
	QuorumBps                     uint64          `json:"quorum_bps"`
	MinQuorumBpsFloor             uint64          `json:"min_quorum_bps_floor"`
	ApprovalThresholdBps          uint64          `json:"approval_threshold_bps"`
	ProposalThreshold             uint64          `json:"proposal_threshold"`
	MaxEmergencyDuration          time.Duration   `json:"max_emergency_duration"`
	EmergencyCooldown             time.Duration   `json:"emergency_cooldown"`
	TreasuryEpochDuration         time.Duration   `json:"treasury_epoch_duration"`
	TreasuryEpochCapMicroUSD      uint64          `json:"treasury_epoch_cap_micro_usd"`
	MaxSingleDisbursementMicroUSD uint64          `json:"max_single_disbursement_micro_usd"`
	AllowlistedRecipients         map[string]bool `json:"allowlisted_recipients"`
	NoAdminOverride               bool            `json:"no_admin_override"`
}

// DefaultProductionConfig returns a production-oriented default configuration
// enforcing 24h timelock, 20% quorum (10% immutable floor), >50% approval,
// bounded 24h emergency pauses with 48h cooldown, and zero admin overrides.
func DefaultProductionConfig(protocolID string) GovernanceConfig {
	return GovernanceConfig{
		ProtocolID:                    protocolID,
		NetworkVersion:                "1.0.0",
		VotingDelay:                   time.Hour,
		VotingPeriod:                  72 * time.Hour,
		MinTimelockDelay:              24 * time.Hour,
		MinTimelockFloor:              6 * time.Hour,
		GracePeriod:                   7 * 24 * time.Hour,
		QuorumBps:                     2000,
		MinQuorumBpsFloor:             1000,
		ApprovalThresholdBps:          5001,
		ProposalThreshold:             100,
		MaxEmergencyDuration:          24 * time.Hour,
		EmergencyCooldown:             48 * time.Hour,
		TreasuryEpochDuration:         30 * 24 * time.Hour,
		TreasuryEpochCapMicroUSD:      100_000_000_000, // $100,000
		MaxSingleDisbursementMicroUSD: 25_000_000_000,  // $25,000
		AllowlistedRecipients:         map[string]bool{},
		NoAdminOverride:               true,
	}
}

// Clone returns a deep copy of GovernanceConfig.
func (c GovernanceConfig) Clone() GovernanceConfig {
	out := c
	out.AllowlistedRecipients = maps.Clone(c.AllowlistedRecipients)
	if out.AllowlistedRecipients == nil {
		out.AllowlistedRecipients = map[string]bool{}
	}
	return out
}

// Validate enforces all hard protocol configuration invariants.
func (c GovernanceConfig) Validate() error {
	if !c.NoAdminOverride {
		return ErrAdminOverrideForbidden
	}
	if c.ProtocolID == "" {
		return fmt.Errorf("%w: protocol_id is required", ErrInvalidConfig)
	}
	if c.NetworkVersion == "" {
		return fmt.Errorf("%w: network_version is required", ErrInvalidConfig)
	}
	if c.VotingDelay < 0 {
		return fmt.Errorf("%w: voting_delay cannot be negative", ErrInvalidConfig)
	}
	if c.VotingPeriod <= 0 {
		return fmt.Errorf("%w: voting_period must be positive", ErrInvalidConfig)
	}
	if c.MinTimelockFloor <= 0 {
		return fmt.Errorf("%w: min_timelock_floor must be positive", ErrInvalidConfig)
	}
	if c.MinTimelockDelay < c.MinTimelockFloor {
		return fmt.Errorf("%w: min_timelock_delay (%s) below immutable floor (%s)", ErrInvalidConfig, c.MinTimelockDelay, c.MinTimelockFloor)
	}
	if c.GracePeriod <= 0 {
		return fmt.Errorf("%w: grace_period must be positive", ErrInvalidConfig)
	}
	if c.MinQuorumBpsFloor == 0 || c.MinQuorumBpsFloor > 10000 {
		return fmt.Errorf("%w: min_quorum_bps_floor must be in [1, 10000]", ErrInvalidConfig)
	}
	if c.QuorumBps < c.MinQuorumBpsFloor || c.QuorumBps > 10000 {
		return fmt.Errorf("%w: quorum_bps (%d) must be in [%d, 10000]", ErrInvalidConfig, c.QuorumBps, c.MinQuorumBpsFloor)
	}
	if c.ApprovalThresholdBps < 5001 || c.ApprovalThresholdBps > 10000 {
		return fmt.Errorf("%w: approval_threshold_bps (%d) must be in [5001, 10000]", ErrInvalidConfig, c.ApprovalThresholdBps)
	}
	if c.MaxEmergencyDuration <= 0 || c.MaxEmergencyDuration > MaxAllowedEmergencyDuration {
		return fmt.Errorf("%w: max_emergency_duration must be in (0, %s]", ErrInvalidConfig, MaxAllowedEmergencyDuration)
	}
	if c.EmergencyCooldown <= 0 {
		return fmt.Errorf("%w: emergency_cooldown must be positive", ErrInvalidConfig)
	}
	if c.TreasuryEpochDuration <= 0 {
		return fmt.Errorf("%w: treasury_epoch_duration must be positive", ErrInvalidConfig)
	}
	if c.MaxSingleDisbursementMicroUSD > c.TreasuryEpochCapMicroUSD && c.TreasuryEpochCapMicroUSD > 0 {
		return fmt.Errorf("%w: max_single_disbursement_micro_usd exceeds treasury_epoch_cap_micro_usd", ErrInvalidConfig)
	}
	return nil
}

// Member represents a registered governance participant authenticated by an
// Ed25519 public key.
type Member struct {
	ID                  string    `json:"id"`
	Org                 string    `json:"org,omitempty"`
	PublicKeyHex        string    `json:"public_key_hex"`
	VotingPower         uint64    `json:"voting_power"`
	DelegateTo          string    `json:"delegate_to,omitempty"`
	IsEmergencyGuardian bool      `json:"is_emergency_guardian"`
	Active              bool      `json:"active"`
	JoinedAt            time.Time `json:"joined_at"`
}

// PowerCheckpoint is an immutable snapshot of total and delegated voting power.
type PowerCheckpoint struct {
	SnapshotID     uint64            `json:"snapshot_id"`
	Timestamp      time.Time         `json:"timestamp"`
	TotalSupply    uint64            `json:"total_supply"`
	EffectivePower map[string]uint64 `json:"effective_power"`
	Delegations    map[string]string `json:"delegations"`
	StateRoot      string            `json:"state_root"`
}

// Clone returns a deep copy of PowerCheckpoint.
func (p PowerCheckpoint) Clone() PowerCheckpoint {
	out := p
	out.EffectivePower = maps.Clone(p.EffectivePower)
	if out.EffectivePower == nil {
		out.EffectivePower = map[string]uint64{}
	}
	out.Delegations = maps.Clone(p.Delegations)
	if out.Delegations == nil {
		out.Delegations = map[string]string{}
	}
	return out
}

// ParameterEntry is a typed, bounded network parameter controlled by DAO votes.
type ParameterEntry struct {
	Key         string   `json:"key"`
	Value       string   `json:"value"`
	ValueType   string   `json:"value_type"` // "uint64", "int64", "float64", "bool", "string"
	MinNumeric  *float64 `json:"min_numeric,omitempty"`
	MaxNumeric  *float64 `json:"max_numeric,omitempty"`
	Immutable   bool     `json:"immutable,omitempty"`
	Description string   `json:"description,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   string   `json:"updated_by,omitempty"`
}

// Clone returns a deep copy of ParameterEntry.
func (p ParameterEntry) Clone() ParameterEntry {
	out := p
	if p.MinNumeric != nil {
		v := *p.MinNumeric
		out.MinNumeric = &v
	}
	if p.MaxNumeric != nil {
		v := *p.MaxNumeric
		out.MaxNumeric = &v
	}
	return out
}

// ModelRegistryEntry describes a governed open model entry with explicit SPDX
// license attribution and verifier profile binding.
type ModelRegistryEntry struct {
	ModelID           string    `json:"model_id"`
	Version           string    `json:"version"`
	DigestSHA256      string    `json:"digest_sha256"`
	LicenseSPDX       string    `json:"license_spdx"`
	VerifierProfileID string    `json:"verifier_profile_id"`
	Active            bool      `json:"active"`
	UpdatedAt         time.Time `json:"updated_at"`
	ProposalID        string    `json:"proposal_id,omitempty"`
}

// VerifierStandardEntry describes a governed verifier compliance profile.
type VerifierStandardEntry struct {
	ProfileID          string             `json:"profile_id"`
	Method             VerificationMethod `json:"method"`
	MinVerifiers       uint32             `json:"min_verifiers"`
	QuorumThresholdBps uint64             `json:"quorum_threshold_bps"`
	ChallengeWindowSec uint64             `json:"challenge_window_sec"`
	MaxLatencyMs       uint64             `json:"max_latency_ms"`
	Description        string             `json:"description,omitempty"`
	UpdatedAt          time.Time          `json:"updated_at"`
	ProposalID         string             `json:"proposal_id,omitempty"`
}

// ProtocolUpgradeRecord captures a timelocked protocol upgrade manifest.
type ProtocolUpgradeRecord struct {
	TargetVersion     string    `json:"target_version"`
	CompatibilityHash string    `json:"compatibility_hash"`
	ActivationEpoch   uint64    `json:"activation_epoch"`
	ReleaseNotesURL   string    `json:"release_notes_url,omitempty"`
	ProposalID        string    `json:"proposal_id"`
	ActivatedAt       time.Time `json:"activated_at"`
}

// ProposalPayload carries the typed payload for exactly one ProposalKind.
type ProposalPayload struct {
	ParameterUpdate      *ParameterUpdatePayload      `json:"parameter_update,omitempty"`
	ModelRegistry        *ModelRegistryPayload        `json:"model_registry,omitempty"`
	VerifierStandard     *VerifierStandardPayload     `json:"verifier_standard,omitempty"`
	TreasuryDisbursement *TreasuryDisbursementPayload `json:"treasury_disbursement,omitempty"`
	EmergencyRatification *EmergencyRatificationPayload `json:"emergency_ratification,omitempty"`
	ProtocolUpgrade      *ProtocolUpgradePayload      `json:"protocol_upgrade,omitempty"`
}

// Clone returns a deep copy of ProposalPayload.
func (p ProposalPayload) Clone() ProposalPayload {
	var out ProposalPayload
	if p.ParameterUpdate != nil {
		cp := *p.ParameterUpdate
		out.ParameterUpdate = &cp
	}
	if p.ModelRegistry != nil {
		cp := *p.ModelRegistry
		out.ModelRegistry = &cp
	}
	if p.VerifierStandard != nil {
		cp := *p.VerifierStandard
		out.VerifierStandard = &cp
	}
	if p.TreasuryDisbursement != nil {
		cp := *p.TreasuryDisbursement
		out.TreasuryDisbursement = &cp
	}
	if p.EmergencyRatification != nil {
		cp := *p.EmergencyRatification
		out.EmergencyRatification = &cp
	}
	if p.ProtocolUpgrade != nil {
		cp := *p.ProtocolUpgrade
		out.ProtocolUpgrade = &cp
	}
	return out
}

// ParameterUpdatePayload updates a single governed network parameter.
type ParameterUpdatePayload struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ModelRegistryPayload registers, updates, or deprecates an open model entry.
type ModelRegistryPayload struct {
	ModelID           string `json:"model_id"`
	Version           string `json:"version"`
	DigestSHA256      string `json:"digest_sha256"`
	LicenseSPDX       string `json:"license_spdx"`
	VerifierProfileID string `json:"verifier_profile_id"`
	Active            bool   `json:"active"`
}

// VerifierStandardPayload creates or updates a verifier compliance standard.
type VerifierStandardPayload struct {
	ProfileID          string             `json:"profile_id"`
	Method             VerificationMethod `json:"method"`
	MinVerifiers       uint32             `json:"min_verifiers"`
	QuorumThresholdBps uint64             `json:"quorum_threshold_bps"`
	ChallengeWindowSec uint64             `json:"challenge_window_sec"`
	MaxLatencyMs       uint64             `json:"max_latency_ms"`
	Description        string             `json:"description,omitempty"`
}

// TreasuryDisbursementPayload requests a timelocked treasury payout.
type TreasuryDisbursementPayload struct {
	Recipient      string `json:"recipient"`
	AmountMicroUSD uint64 `json:"amount_micro_usd"`
	Memo           string `json:"memo"`
	AllowlistAdd   bool   `json:"allowlist_add,omitempty"`
}

// EmergencyRatificationPayload ratifies (with bounded extension) or lifts an
// active emergency pause via full DAO vote.
type EmergencyRatificationPayload struct {
	ActionID        string        `json:"action_id"`
	Lift            bool          `json:"lift"`
	ExtendDuration  time.Duration `json:"extend_duration,omitempty"`
}

// ProtocolUpgradePayload schedules a versioned protocol upgrade.
type ProtocolUpgradePayload struct {
	TargetVersion     string `json:"target_version"`
	CompatibilityHash string `json:"compatibility_hash"`
	ActivationEpoch   uint64 `json:"activation_epoch"`
	ReleaseNotesURL   string `json:"release_notes_url,omitempty"`
}

// ProposalRequest is a signed proposal submission.
type ProposalRequest struct {
	ProposalID  string          `json:"proposal_id"`
	ProposerID  string          `json:"proposer_id"`
	Org         string          `json:"org,omitempty"`
	Kind        ProposalKind    `json:"kind"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Payload     ProposalPayload `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
	Nonce       string          `json:"nonce"`
	Signature   string          `json:"signature"`
}

// VoteRecord stores a single verified vote cast on a proposal.
type VoteRecord struct {
	ProposalID       string      `json:"proposal_id"`
	VoterID          string      `json:"voter_id"`
	Org              string      `json:"org,omitempty"`
	OriginProtocolID string      `json:"origin_protocol_id,omitempty"`
	Support          VoteSupport `json:"support"`
	Weight           uint64      `json:"weight"`
	Reason           string      `json:"reason,omitempty"`
	CastAt           time.Time   `json:"cast_at"`
	Nonce            string      `json:"nonce"`
	Signature        string      `json:"signature"`
}

// VoteRequest is a signed ballot submitted by an authorized member.
type VoteRequest struct {
	ProposalID string      `json:"proposal_id"`
	VoterID    string      `json:"voter_id"`
	Org        string      `json:"org,omitempty"`
	Support    VoteSupport `json:"support"`
	Reason     string      `json:"reason,omitempty"`
	CastAt     time.Time   `json:"cast_at"`
	Nonce      string      `json:"nonce"`
	Signature  string      `json:"signature"`
}

// VoteReceipt confirms a recorded ballot.
type VoteReceipt struct {
	ProposalID    string      `json:"proposal_id"`
	VoterID       string      `json:"voter_id"`
	Support       VoteSupport `json:"support"`
	Weight        uint64      `json:"weight"`
	SnapshotID    uint64      `json:"snapshot_id"`
	RecordedAt    time.Time   `json:"recorded_at"`
	LedgerSeq     uint64      `json:"ledger_seq"`
	LedgerHash    string      `json:"ledger_hash"`
}

// QueueRequest is a signed request to enqueue a succeeded proposal into the
// mandatory timelock.
type QueueRequest struct {
	ProposalID string    `json:"proposal_id"`
	CallerID   string    `json:"caller_id"`
	Org        string    `json:"org,omitempty"`
	QueuedAt   time.Time `json:"queued_at"`
	Nonce      string    `json:"nonce"`
	Signature  string    `json:"signature"`
}

// ExecuteRequest is a signed request to execute a matured timelocked proposal.
type ExecuteRequest struct {
	ProposalID string    `json:"proposal_id"`
	ExecutorID string    `json:"executor_id"`
	Org        string    `json:"org,omitempty"`
	ExecutedAt time.Time `json:"executed_at"`
	Nonce      string    `json:"nonce"`
	Signature  string    `json:"signature"`
}

// ExecutionReceipt is the verifiable record emitted on proposal execution.
type ExecutionReceipt struct {
	ProposalID  string    `json:"proposal_id"`
	ExecutorID  string    `json:"executor_id"`
	ExecutedAt  time.Time `json:"executed_at"`
	PayloadHash string    `json:"payload_hash"`
	ResultHash  string    `json:"result_hash"`
	LedgerSeq   uint64    `json:"ledger_seq"`
	LedgerHash  string    `json:"ledger_hash"`
}

// CancelRequest is a signed request to cancel a proposal before execution.
type CancelRequest struct {
	ProposalID string    `json:"proposal_id"`
	CallerID   string    `json:"caller_id"`
	Org        string    `json:"org,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	CanceledAt time.Time `json:"canceled_at"`
	Nonce      string    `json:"nonce"`
	Signature  string    `json:"signature"`
}

// DelegationRequest is a signed voting power delegation instruction.
type DelegationRequest struct {
	DelegatorID string    `json:"delegator_id"`
	DelegateeID string    `json:"delegatee_id"`
	Org         string    `json:"org,omitempty"`
	IssuedAt    time.Time `json:"issued_at"`
	Nonce       string    `json:"nonce"`
	Signature   string    `json:"signature"`
}

// TransferPowerRequest is a signed transfer of voting power between active
// members (used to prove post-snapshot transfer inflation defense).
type TransferPowerRequest struct {
	FromMemberID string    `json:"from_member_id"`
	ToMemberID   string    `json:"to_member_id"`
	Amount       uint64    `json:"amount"`
	Org          string    `json:"org,omitempty"`
	IssuedAt     time.Time `json:"issued_at"`
	Nonce        string    `json:"nonce"`
	Signature    string    `json:"signature"`
}

// EmergencyActionRequest is a signed bounded emergency pause or circuit-breaker
// invocation by an authorized guardian.
type EmergencyActionRequest struct {
	ActionID        string              `json:"action_id"`
	GuardianID      string              `json:"guardian_id"`
	Org             string              `json:"org,omitempty"`
	Kind            EmergencyActionKind `json:"kind"`
	TargetSubsystem string              `json:"target_subsystem"`
	Duration        time.Duration       `json:"duration"`
	Reason          string              `json:"reason"`
	IssuedAt        time.Time           `json:"issued_at"`
	Nonce           string              `json:"nonce"`
	Signature       string              `json:"signature"`
}

// EmergencyAction tracks a bounded, auto-expiring emergency safeguard.
type EmergencyAction struct {
	ActionID         string              `json:"action_id"`
	GuardianID       string              `json:"guardian_id"`
	Org              string              `json:"org,omitempty"`
	OriginProtocolID string              `json:"origin_protocol_id,omitempty"`
	Kind             EmergencyActionKind `json:"kind"`
	TargetSubsystem  string              `json:"target_subsystem"`
	Duration         time.Duration       `json:"duration"`
	Reason           string              `json:"reason"`
	IssuedAt         time.Time           `json:"issued_at"`
	ExpiresAt        time.Time           `json:"expires_at"`
	Ratified         bool                `json:"ratified"`
	RatifiedByProp   string              `json:"ratified_by_proposal,omitempty"`
	LiftedAt         *time.Time          `json:"lifted_at,omitempty"`
	Nonce            string              `json:"nonce"`
	Signature        string              `json:"signature"`
}

// Clone returns a deep copy of EmergencyAction.
func (e EmergencyAction) Clone() EmergencyAction {
	out := e
	if e.LiftedAt != nil {
		t := *e.LiftedAt
		out.LiftedAt = &t
	}
	return out
}

// ActiveAt reports whether the emergency safeguard is active at timestamp t.
func (e EmergencyAction) ActiveAt(t time.Time) bool {
	if e.LiftedAt != nil && !t.Before(*e.LiftedAt) {
		return false
	}
	return !t.Before(e.IssuedAt) && t.Before(e.ExpiresAt)
}

// Proposal captures the full lifecycle, tally, timelock, and signatures for a
// governance proposal.
type Proposal struct {
	ID               string                `json:"id"`
	ProposerID       string                `json:"proposer_id"`
	Org              string                `json:"org,omitempty"`
	OriginProtocolID string                `json:"origin_protocol_id,omitempty"`
	Kind             ProposalKind          `json:"kind"`
	Title            string                `json:"title"`
	Description      string                `json:"description"`
	Payload          ProposalPayload       `json:"payload"`
	PayloadHash      string                `json:"payload_hash"`
	SnapshotID       uint64                `json:"snapshot_id"`
	SnapshotSupply   uint64                `json:"snapshot_supply"`
	QuorumRequired   uint64                `json:"quorum_required"`
	ApprovalBps      uint64                `json:"approval_threshold_bps"`
	CreatedAt        time.Time             `json:"created_at"`
	VotingStartsAt   time.Time             `json:"voting_starts_at"`
	VotingEndsAt     time.Time             `json:"voting_ends_at"`
	QueuedAt         *time.Time            `json:"queued_at,omitempty"`
	ETA              *time.Time            `json:"eta,omitempty"`
	ExpiresAt        *time.Time            `json:"expires_at,omitempty"`
	ExecutedAt       *time.Time            `json:"executed_at,omitempty"`
	CanceledAt       *time.Time            `json:"canceled_at,omitempty"`
	CancelReason     string                `json:"cancel_reason,omitempty"`
	State            ProposalState         `json:"state"`
	ForVotes         uint64                `json:"for_votes"`
	AgainstVotes     uint64                `json:"against_votes"`
	AbstainVotes     uint64                `json:"abstain_votes"`
	Votes            map[string]VoteRecord `json:"votes"`
	Nonce            string                `json:"nonce"`
	Signature        string                `json:"signature"`
	ExecutionReceipt *ExecutionReceipt     `json:"execution_receipt,omitempty"`
}

// Clone returns a deep copy of Proposal.
func (p Proposal) Clone() Proposal {
	out := p
	out.Payload = p.Payload.Clone()
	if p.QueuedAt != nil {
		t := *p.QueuedAt
		out.QueuedAt = &t
	}
	if p.ETA != nil {
		t := *p.ETA
		out.ETA = &t
	}
	if p.ExpiresAt != nil {
		t := *p.ExpiresAt
		out.ExpiresAt = &t
	}
	if p.ExecutedAt != nil {
		t := *p.ExecutedAt
		out.ExecutedAt = &t
	}
	if p.CanceledAt != nil {
		t := *p.CanceledAt
		out.CanceledAt = &t
	}
	out.Votes = maps.Clone(p.Votes)
	if out.Votes == nil {
		out.Votes = map[string]VoteRecord{}
	}
	if p.ExecutionReceipt != nil {
		r := *p.ExecutionReceipt
		out.ExecutionReceipt = &r
	}
	return out
}

// TreasuryDisbursementRecord logs an executed treasury disbursement.
type TreasuryDisbursementRecord struct {
	ProposalID     string    `json:"proposal_id"`
	Recipient      string    `json:"recipient"`
	AmountMicroUSD uint64    `json:"amount_micro_usd"`
	Memo           string    `json:"memo"`
	EpochIndex     uint64    `json:"epoch_index"`
	ExecutedAt     time.Time `json:"executed_at"`
	ExecutorID     string    `json:"executor_id"`
	LedgerSeq      uint64    `json:"ledger_seq"`
}

// TreasuryState captures current treasury balance, per-epoch spending totals,
// and disbursement history.
type TreasuryState struct {
	BalanceMicroUSD uint64                       `json:"balance_micro_usd"`
	EpochSpent      map[uint64]uint64            `json:"epoch_spent"`
	Disbursements   []TreasuryDisbursementRecord `json:"disbursements"`
}

// Clone returns a deep copy of TreasuryState.
func (t TreasuryState) Clone() TreasuryState {
	out := TreasuryState{
		BalanceMicroUSD: t.BalanceMicroUSD,
		EpochSpent:      maps.Clone(t.EpochSpent),
		Disbursements:   slices.Clone(t.Disbursements),
	}
	if out.EpochSpent == nil {
		out.EpochSpent = map[uint64]uint64{}
	}
	if out.Disbursements == nil {
		out.Disbursements = []TreasuryDisbursementRecord{}
	}
	return out
}

// LedgerEntry is a single hash-chained audit record serialized as JSONL.
type LedgerEntry struct {
	Seq         uint64          `json:"seq"`
	Timestamp   time.Time       `json:"timestamp"`
	ProtocolID  string          `json:"protocol_id"`
	EventType   string          `json:"event_type"`
	ActorID     string          `json:"actor_id"`
	SubjectID   string          `json:"subject_id"`
	PayloadHash string          `json:"payload_hash"`
	Payload     json.RawMessage `json:"payload"`
	PrevHash    string          `json:"prev_hash"`
	EntryHash   string          `json:"entry_hash"`
}

// SovereignState is the complete deterministic state exported for backup,
// migration, or independent community fork.
type SovereignState struct {
	Config            GovernanceConfig        `json:"config"`
	GenesisTime       time.Time               `json:"genesis_time"`
	Members           []Member                `json:"members"`
	Snapshots         []PowerCheckpoint       `json:"snapshots"`
	Parameters        []ParameterEntry        `json:"parameters"`
	Models            []ModelRegistryEntry    `json:"models"`
	VerifierStandards []VerifierStandardEntry `json:"verifier_standards"`
	Proposals         []Proposal              `json:"proposals"`
	EmergencyActions  []EmergencyAction       `json:"emergency_actions"`
	Treasury          TreasuryState           `json:"treasury"`
	Upgrades          []ProtocolUpgradeRecord `json:"upgrades"`
	UsedNonces        map[string][]string     `json:"used_nonces"`
}

// SovereignBundle is a cryptographically signed, hash-chained export package
// enabling zero-dependency forkability and replayable audit verification.
type SovereignBundle struct {
	BundleVersion     string         `json:"bundle_version"`
	ProtocolID        string         `json:"protocol_id"`
	NetworkVersion    string         `json:"network_version"`
	ParentProtocolID  string         `json:"parent_protocol_id,omitempty"`
	ParentBundleHash  string         `json:"parent_bundle_hash,omitempty"`
	ExportedAt        time.Time      `json:"exported_at"`
	State             SovereignState `json:"state"`
	Ledger            []LedgerEntry  `json:"ledger"`
	LedgerHeadHash    string         `json:"ledger_head_hash"`
	StateDigest       string         `json:"state_digest"`
	BundleHash        string         `json:"bundle_hash"`
	ExporterID        string         `json:"exporter_id"`
	ExporterPubKeyHex string         `json:"exporter_pub_key_hex"`
	ExporterSignature string         `json:"exporter_signature"`
}
