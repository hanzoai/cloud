// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package sovereignty implements a host-independent, multi-server decentralized
// network exit harness and verification suite for HIP-1351 (hanzoai/cloud#20).
//
// Core acceptance invariant:
// Deliberately disabling all Hanzo-operated gateways, registries, schedulers,
// and payment facilitators MUST NOT prevent an independently administered
// community network from onboarding new providers, discovering content-addressed
// models, executing and verifying inference requests, settling payments,
// running community governance, and recovering authorized user data.
package sovereignty

import (
	"errors"
	"time"
)

// ProtocolVersion identifies the versioned HIP-1351 network exit interface.
const ProtocolVersion = "hip-1351/v1"

// ControlPlaneKind distinguishes Hanzo-operated convenience infrastructure from
// independently administered community infrastructure.
type ControlPlaneKind string

const (
	PlaneHanzo     ControlPlaneKind = "hanzo_convenience"
	PlaneCommunity ControlPlaneKind = "community_independent"
)

// VerificationClass makes explicit what security property an execution receipt
// asserts, avoiding conflation of deterministic cryptographic recomputation,
// hardware attestation, and economic dispute games.
type VerificationClass string

const (
	VerificationClassDeterministicHash VerificationClass = "deterministic_recomputation_sha256"
)

// Sentinel errors returned across network exit, verification, and recovery paths.
var (
	ErrHanzoHostBlocked       = errors.New("sovereignty: hanzo-operated host blocked during blackout")
	ErrHanzoPlaneOffline      = errors.New("sovereignty: hanzo control plane offline")
	ErrInvalidSignature       = errors.New("sovereignty: invalid secp256k1 signature")
	ErrSignerMismatch         = errors.New("sovereignty: recovered signer does not match declared address")
	ErrModelDigestMismatch    = errors.New("sovereignty: content-addressed model SHA-256 digest mismatch")
	ErrUnauthorizedDataAccess = errors.New("sovereignty: unauthorized data decryption key or tampered ciphertext")
	ErrInvalidReceipt         = errors.New("sovereignty: execution receipt failed independent verification")
	ErrBootstrapQuorumLost    = errors.New("sovereignty: all independent community bootstrap peers unreachable")
	ErrInsufficientBalance    = errors.New("sovereignty: insufficient payer balance")
	ErrReplayDetected         = errors.New("sovereignty: payment nonce or ID replay detected")
	ErrGovernanceQuorumNotMet = errors.New("sovereignty: governance quorum threshold not met")
	ErrInvalidRequest         = errors.New("sovereignty: invalid request")
	ErrNotFound               = errors.New("sovereignty: resource not found")
	ErrForkBundleCorrupted    = errors.New("sovereignty: fork bundle integrity or signature check failed")
)

// EIP712Domain separates typed-data signatures by protocol name, version,
// chain ID, and verifying contract address to prevent cross-network replay.
type EIP712Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// DefaultDomain returns the canonical deterministic local domain for HIP-1351.
func DefaultDomain() EIP712Domain {
	return EIP712Domain{
		Name:              "Hanzo Sovereign Network Exit",
		Version:           "1",
		ChainID:           31337,
		VerifyingContract: "0x507e000000000000000000000000000000000020",
	}
}

// ProviderAdvertisement is a provider-signed capability record published to a
// peer discovery registry (Hanzo or independent community bootstrap).
type ProviderAdvertisement struct {
	ProviderID       string   `json:"providerId"`
	OperatorAddress  string   `json:"operatorAddress"`
	EndpointURL      string   `json:"endpointUrl"`
	ModelIDs         []string `json:"modelIds"`
	PricePer1KAtomic uint64   `json:"pricePer1kAtomic"`
	CapacityRPM      uint32   `json:"capacityRpm"`
	IssuedAt         int64    `json:"issuedAt"`
	ExpiresAt        int64    `json:"expiresAt"`
	Signature        string   `json:"signature"`
}

// ModelManifest binds a human-readable model ID and license metadata to the
// exact SHA-256 digest of its weight artifact and one or more mirror URLs.
type ModelManifest struct {
	ModelID          string   `json:"modelId"`
	Version          string   `json:"version"`
	License          string   `json:"license"`
	DigestSHA256     string   `json:"digestSha256"`
	SizeBytes        int64    `json:"sizeBytes"`
	Mirrors          []string `json:"mirrors"`
	PublisherAddress string   `json:"publisherAddress"`
	IssuedAt         int64    `json:"issuedAt"`
	Signature        string   `json:"signature"`
}

// InferenceRequest represents a stateless inference job dispatched to any
// provider advertising the content-addressed model digest.
type InferenceRequest struct {
	JobID             string `json:"jobId"`
	TenantAddress     string `json:"tenantAddress"`
	ModelID           string `json:"modelId"`
	ModelDigestSHA256 string `json:"modelDigestSha256"`
	Prompt            string `json:"prompt"`
	Seed              uint64 `json:"seed"`
	MaxTokens         int    `json:"maxTokens"`
	Nonce             uint64 `json:"nonce"`
	Timestamp         int64  `json:"timestamp"`
}

// InferenceExecutionReceipt is the provider's EIP-712 signed commitment to
// the exact model artifact digest, input digest, and deterministic output.
type InferenceExecutionReceipt struct {
	JobID              string            `json:"jobId"`
	ProviderID         string            `json:"providerId"`
	ProviderAddress    string            `json:"providerAddress"`
	ModelID            string            `json:"modelId"`
	ModelDigestSHA256  string            `json:"modelDigestSha256"`
	InputDigestSHA256  string            `json:"inputDigestSha256"`
	OutputText         string            `json:"outputText"`
	OutputDigestSHA256 string            `json:"outputDigestSha256"`
	TokensGenerated    int               `json:"tokensGenerated"`
	ExecutedAt         int64             `json:"executedAt"`
	VerificationClass  VerificationClass `json:"verificationClass"`
	Signature          string            `json:"signature"`
}

// PaymentAuthorization is a payer-signed EIP-712 payment instruction that can
// be settled by ANY facilitator (Hanzo convenience or independent community).
type PaymentAuthorization struct {
	PaymentID    string `json:"paymentId"`
	JobID        string `json:"jobId"`
	PayerAddress string `json:"payerAddress"`
	PayeeAddress string `json:"payeeAddress"`
	AmountAtomic uint64 `json:"amountAtomic"`
	Nonce        uint64 `json:"nonce"`
	ValidBefore  int64  `json:"validBefore"`
	ChainID      uint64 `json:"chainId"`
	Signature    string `json:"signature"`
}

// SettlementReceipt is the facilitator-signed confirmation of atomic transfer
// and inclusion in the hash-chained settlement ledger.
type SettlementReceipt struct {
	PaymentID          string `json:"paymentId"`
	JobID              string `json:"jobId"`
	FacilitatorID      string `json:"facilitatorId"`
	FacilitatorAddress string `json:"facilitatorAddress"`
	PayerAddress       string `json:"payerAddress"`
	PayeeAddress       string `json:"payeeAddress"`
	AmountAtomic       uint64 `json:"amountAtomic"`
	SettledAt          int64  `json:"settledAt"`
	PrevLedgerHash     string `json:"prevLedgerHash"`
	LedgerHash         string `json:"ledgerHash"`
	Signature          string `json:"signature"`
}

// DataCapabilityManifest declares portability and non-custodial key ownership
// metadata attached to a user's encrypted workspace snapshot.
type DataCapabilityManifest struct {
	OwnerAddress     string   `json:"ownerAddress"`
	ExportFormat     string   `json:"exportFormat"`
	PortableMirrors  []string `json:"portableMirrors"`
	AllowedOperators []string `json:"allowedOperators"`
	CustodialEscrow  bool     `json:"custodialEscrow"`
}

// EncryptedDataEnvelope holds AES-256-GCM authenticated ciphertext encrypted
// client-side under the user's sovereign key (zero server key custody).
type EncryptedDataEnvelope struct {
	SnapshotID         string                 `json:"snapshotId"`
	OwnerAddress       string                 `json:"ownerAddress"`
	Algorithm          string                 `json:"algorithm"`
	SaltHex            string                 `json:"saltHex"`
	NonceHex           string                 `json:"nonceHex"`
	CiphertextHex      string                 `json:"ciphertextHex"`
	PlaintextSHA256    string                 `json:"plaintextSha256"`
	PlaintextSizeBytes int                    `json:"plaintextSizeBytes"`
	CapabilityManifest DataCapabilityManifest `json:"capabilityManifest"`
	CreatedAt          int64                  `json:"createdAt"`
	OwnerSignature     string                 `json:"ownerSignature"`
}

// GovernanceVote is a signed community operator vote for independent protocol
// governance (for example, ratifying a new bootstrap peer without Hanzo).
type GovernanceVote struct {
	ProposalID   string `json:"proposalId"`
	Action       string `json:"action"`
	Payload      string `json:"payload"`
	VoterAddress string `json:"voterAddress"`
	Approve      bool   `json:"approve"`
	VotedAt      int64  `json:"votedAt"`
	Signature    string `json:"signature"`
}

// GovernanceDecision summarizes the ratified outcome of a community proposal.
type GovernanceDecision struct {
	ProposalID     string           `json:"proposalId"`
	Action         string           `json:"action"`
	Payload        string           `json:"payload"`
	QuorumRequired int              `json:"quorumRequired"`
	Approvals      int              `json:"approvals"`
	Ratified       bool             `json:"ratified"`
	Votes          []GovernanceVote `json:"votes"`
}

// NetworkForkBundle is a complete, signed, portable state snapshot enabling any
// independent operator to fork or migrate the decentralized network state.
type NetworkForkBundle struct {
	ProtocolVersion   string                  `json:"protocolVersion"`
	ExportedAt        int64                   `json:"exportedAt"`
	OperatorID        string                  `json:"operatorId"`
	OperatorAddress   string                  `json:"operatorAddress"`
	Providers         []ProviderAdvertisement `json:"providers"`
	Models            []ModelManifest         `json:"models"`
	ModelBlobs        map[string][]byte       `json:"modelBlobs,omitempty"`
	SettlementLedger  []SettlementReceipt     `json:"settlementLedger"`
	Balances          map[string]uint64       `json:"balances"`
	Governance        []GovernanceDecision    `json:"governance"`
	EncryptedVault    []EncryptedDataEnvelope `json:"encryptedVault"`
	BundleSHA256      string                  `json:"bundleSha256"`
	OperatorSignature string                  `json:"operatorSignature"`
}

// HostGuardMetrics captures exact network call counts before and after the
// Hanzo Kill Switch is engaged.
type HostGuardMetrics struct {
	KillSwitchActive        bool  `json:"killSwitchActive"`
	HanzoCallsBeforeKill    int64 `json:"hanzoCallsBeforeKill"`
	HanzoCallsAfterKill     int64 `json:"hanzoCallsAfterKill"`
	HanzoBlockedAttempts    int64 `json:"hanzoBlockedAttempts"`
	CommunityCallsBeforeKil int64 `json:"communityCallsBeforeKill"`
	CommunityCallsAfterKill int64 `json:"communityCallsAfterKill"`
}

// DrillStageResult reports the outcome and timing of one stage of the 8-stage
// full sovereignty network exit drill.
type DrillStageResult struct {
	Stage                int     `json:"stage"`
	Name                 string  `json:"name"`
	Plane                string  `json:"plane"`
	Passed               bool    `json:"passed"`
	DurationMs           float64 `json:"durationMs"`
	HanzoCallsDuringStep int64   `json:"hanzoCallsDuringStep"`
	Detail               string  `json:"detail"`
}

// NetworkExitReport summarizes a complete 8-stage blackout acceptance run.
type NetworkExitReport struct {
	ProtocolVersion        string             `json:"protocolVersion"`
	StartedAt              time.Time          `json:"startedAt"`
	TotalDurationMs        float64            `json:"totalDurationMs"`
	FailoverRTOMs          float64            `json:"failoverRtoMs"`
	AllStagesPassed        bool               `json:"allStagesPassed"`
	Stages                 []DrillStageResult `json:"stages"`
	PostKillStagesPassed   int                `json:"postKillStagesPassed"`
	PostKillStagesTotal    int                `json:"postKillStagesTotal"`
	DecentralizationRatio  float64            `json:"decentralizationRatio"`
	SovereigntyRatio       float64            `json:"sovereigntyRatio"`
	GuardMetrics           HostGuardMetrics   `json:"guardMetrics"`
	HanzoBlackoutProbePass bool               `json:"hanzoBlackoutProbePass"`
	UnauthorizedKeyBlocked bool               `json:"unauthorizedKeyBlocked"`
	GovernanceRatified     bool               `json:"governanceRatified"`
	ForkBundleVerified     bool               `json:"forkBundleVerified"`
	ForkBundleBytes        int                `json:"forkBundleBytes"`
	ForkHydratedProviders  int                `json:"forkHydratedProviders"`
	ForkHydratedVaultItems int                `json:"forkHydratedVaultItems"`
	SettledAmountAtomic    uint64             `json:"settledAmountAtomic"`
	TokensGenerated        int                `json:"tokensGenerated"`
	ConservationDrift      int64              `json:"conservationDrift"`
}

// NegativeRegimeResult captures one honest negative or boundary experiment.
type NegativeRegimeResult struct {
	Name              string  `json:"name"`
	Category          string  `json:"category"`
	Trials            int     `json:"trials"`
	Succeeded         int     `json:"succeeded"`
	FailedClosed      int     `json:"failedClosed"`
	CompletionRate    float64 `json:"completionRate"`
	BlockedHanzoCalls int64   `json:"blockedHanzoCalls,omitempty"`
	ExpectedBehavior  string  `json:"expectedBehavior"`
	ObservedBehavior  string  `json:"observedBehavior"`
	Validated         bool    `json:"validated"`
}
