// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package escrow implements an independently operable compute escrow, service
// guarantee, objective challenge, and atomic settlement engine (HIP-1350,
// hanzoai/cloud#17).
//
// Core protocol invariants:
//  1. Proof of service quality (SLA/liveness/deadline) and proof of correct
//     computation (deterministic output commitment / cryptographic equivocation)
//     are strictly separated.
//  2. Slashing applies exclusively to objectively defined, independently
//     challengeable protocol violations. Subjective quality claims are
//     fail-closed rejected (ErrSubjectiveSlashingForbidden) with zero funds seized.
//  3. Terminal states (Settled, Slashed, Refunded) are strictly absorbing:
//     funds can never be released twice, and exact atomic-unit conservation
//     (sum(balances) + sum(locked) == initialSupply) holds across all transitions.
package escrow

import (
	"errors"
	"time"
)

// Sentinel errors returned by the escrow state machine and verifiers.
var (
	ErrInvalidRequest                     = errors.New("escrow: invalid request")
	ErrInvalidSignature                   = errors.New("escrow: invalid secp256k1 signature")
	ErrSignerMismatch                     = errors.New("escrow: recovered signer does not match expected party")
	ErrQuoteAlreadyExists                 = errors.New("escrow: job quote already exists")
	ErrEscrowNotFound                     = errors.New("escrow: escrow not found")
	ErrInvalidStateTransition             = errors.New("escrow: invalid state transition")
	ErrAlreadyFinalized                   = errors.New("escrow: escrow already finalized in terminal state")
	ErrInsufficientBalance                = errors.New("escrow: insufficient account balance")
	ErrDeadlineNotReached                 = errors.New("escrow: job deadline not yet reached")
	ErrDeadlineExceeded                   = errors.New("escrow: job deadline exceeded")
	ErrChallengeWindowActive              = errors.New("escrow: challenge window still active")
	ErrChallengeWindowClosed              = errors.New("escrow: challenge window closed")
	ErrChallengeBondRequired              = errors.New("escrow: non-zero challenge bond required")
	ErrSubjectiveSlashingForbidden        = errors.New("escrow: slashing on subjective quality claims is strictly forbidden")
	ErrUnknownViolationType               = errors.New("escrow: unsupported or non-objective violation type")
	ErrNonDeterministicVerificationNeeded = errors.New("escrow: raw bitwise equality cannot slash non-deterministic sampling jobs")
	ErrEquivocationProofInvalid           = errors.New("escrow: equivocation proof does not show two conflicting signed outputs")
	ErrLedgerTampered                     = errors.New("escrow: hash-chained settlement ledger integrity check failed")
	ErrConservationViolation              = errors.New("escrow: conservation of funds invariant violated")
)

// EscrowState enumerates the deterministic lifecycle states of a compute escrow.
type EscrowState string

const (
	StateQuoted     EscrowState = "Quoted"
	StateFunded     EscrowState = "Funded"
	StateClaimed    EscrowState = "Claimed"
	StateChallenged EscrowState = "Challenged"
	StateSettled    EscrowState = "Settled"  // Terminal absorbing: provider paid (and challenger bond forfeited if malicious)
	StateSlashed    EscrowState = "Slashed"  // Terminal absorbing: provider objectively violated protocol; buyer refunded, challenger rewarded
	StateRefunded   EscrowState = "Refunded" // Terminal absorbing: worker crashed / missed deadline; buyer refunded + SLA penalty
)

// IsTerminal reports whether s is an absorbing terminal settlement state.
func (s EscrowState) IsTerminal() bool {
	return s == StateSettled || s == StateSlashed || s == StateRefunded
}

// ViolationType classifies dispute claims and enforces the strict boundary between
// objective, independently verifiable protocol violations and forbidden subjective disputes.
type ViolationType string

const (
	// ViolationMissedDeadline is an objective Service Quality (SLA) violation:
	// CompletedAt > Deadline or no claim submitted by Deadline.
	ViolationMissedDeadline ViolationType = "missed_deadline"

	// ViolationFalseCompletionClaim is an objective Computation Correctness violation:
	// OutputDigest / ExecutionProof fails independent deterministic recomputation.
	ViolationFalseCompletionClaim ViolationType = "false_completion_claim"

	// ViolationInvalidSignatureOrEquivocation is an objective Cryptographic Integrity violation:
	// signed claim conflicts with quote binding or provider signed two distinct outputs for the same JobID.
	ViolationInvalidSignatureOrEquivocation ViolationType = "invalid_signature_or_equivocation"

	// ViolationSubjectiveQuality represents a non-objective aesthetic, helpfulness, or preference claim.
	// The engine MUST fail-closed reject this with ErrSubjectiveSlashingForbidden.
	ViolationSubjectiveQuality ViolationType = "subjective_quality"
)

// ProofDomain distinguishes Proof of Service Quality from Proof of Correct Computation.
type ProofDomain string

const (
	ProofDomainServiceQuality       ProofDomain = "service_quality_sla"
	ProofDomainComputationIntegrity ProofDomain = "computation_correctness"
	ProofDomainSubjectiveForbidden  ProofDomain = "subjective_forbidden"
)

// VerificationClass declares how a job's computation output may be objectively verified.
type VerificationClass string

const (
	// ClassDeterministicHash supports independent recomputation and exact digest comparison.
	ClassDeterministicHash VerificationClass = "deterministic_hash"

	// ClassVerifiableProof supports cryptographic proof verification (e.g. ZK/hash-chain/execution trace).
	ClassVerifiableProof VerificationClass = "verifiable_proof"

	// ClassNonDeterministicSampling covers temperature > 0 / non-deterministic floating-point LLM runs.
	// Raw bitwise output equality MUST NOT be used to slash these jobs without deterministic commitments;
	// only objective SLA/deadline, signature binding, or equivocation violations apply.
	ClassNonDeterministicSampling VerificationClass = "non_deterministic_sampling"
)

// ChallengeOutcome records the objective verdict of challenge resolution.
type ChallengeOutcome string

const (
	ChallengeOutcomeNone              ChallengeOutcome = ""
	ChallengeOutcomeUpheldSlashed     ChallengeOutcome = "upheld_provider_slashed"
	ChallengeOutcomeRejectedMalicious ChallengeOutcome = "rejected_challenger_forfeited"
)

// Clock supplies deterministic Unix-second timestamps for testing and benchmarks.
type Clock interface {
	Now() time.Time
}

// RealClock returns wall-clock UTC time.
type RealClock struct{}

// Now returns time.Now().UTC().
func (RealClock) Now() time.Time { return time.Now().UTC() }

// Policy configures economic and safety parameters for an EscrowEngine.
// DefaultPolicy enforces safe non-zero bonds and challenge windows.
type Policy struct {
	// RequireChallengeBond enforces ChallengeBondAtomic > 0 (default: true).
	// Only disabled in explicit negative-regime ablation experiments.
	RequireChallengeBond bool `json:"requireChallengeBond"`

	// AllowInstantRelease permits ChallengeWindowSec == 0 (default: false).
	// Only enabled in explicit negative-regime ablation experiments.
	AllowInstantRelease bool `json:"allowInstantRelease"`

	// MinChallengeWindowSec enforces a minimum dispute window when AllowInstantRelease is false.
	MinChallengeWindowSec int64 `json:"minChallengeWindowSec"`
}

// DefaultPolicy returns the production-safe escrow policy.
func DefaultPolicy() Policy {
	return Policy{
		RequireChallengeBond:  true,
		AllowInstantRelease:   false,
		MinChallengeWindowSec: 1,
	}
}

// JobQuote is the provider-signed binding offer for a compute job.
// Both service-level guarantees (Deadline, SLAPenaltyAtomic) and computation
// verification parameters (InputDigest, VerificationClass, VerificationSpec) are explicit.
type JobQuote struct {
	JobID               string            `json:"jobId"`
	Buyer               string            `json:"buyer"`
	Provider            string            `json:"provider"`
	Asset               string            `json:"asset"`
	JobPriceAtomic      uint64            `json:"jobPriceAtomic"`
	ProviderBondAtomic  uint64            `json:"providerBondAtomic"`
	ChallengeBondAtomic uint64            `json:"challengeBondAtomic"`
	SLAPenaltyAtomic    uint64            `json:"slaPenaltyAtomic"`
	ChallengerRewardBps uint16            `json:"challengerRewardBps"` // Basis points (0..10000) of ProviderBond awarded to valid challenger
	Deadline            int64             `json:"deadline"`            // Unix seconds by which provider must claim
	ChallengeWindowSec  int64             `json:"challengeWindowSec"`  // Seconds after claim during which objective challenges are accepted
	InputDigest         string            `json:"inputDigest"`         // 0x-prefixed 32-byte commitment to job input
	VerificationClass   VerificationClass `json:"verificationClass"`
	VerificationSpec    string            `json:"verificationSpec"` // Canonical deterministic computation kernel identifier
	EstimatedTokens     uint64            `json:"estimatedTokens"`
	Nonce               uint64            `json:"nonce"`
	QuoteHash           string            `json:"quoteHash"`         // Computed EIP-712 struct digest (0x-prefixed)
	ProviderSignature   string            `json:"providerSignature"` // EIP-712 signature by Provider
	CreatedAt           int64             `json:"createdAt"`
}

// QuoteAcceptance is the buyer-signed EIP-712 acceptance authorizing escrow funding.
type QuoteAcceptance struct {
	JobID          string `json:"jobId"`
	QuoteHash      string `json:"quoteHash"`
	Buyer          string `json:"buyer"`
	JobPriceAtomic uint64 `json:"jobPriceAtomic"`
	AcceptedAt     int64  `json:"acceptedAt"`
	BuyerSignature string `json:"buyerSignature"`
}

// ServiceGuaranteeProof captures objective SLA/liveness evidence separately from computation proofs.
type ServiceGuaranteeProof struct {
	Deadline         int64 `json:"deadline"`
	ClaimedAt        int64 `json:"claimedAt"`
	OnTime           bool  `json:"onTime"`
	LatencySeconds   int64 `json:"latencySeconds"`
	WorkerResponsive bool  `json:"workerResponsive"`
}

// ComputationProof captures objective execution output commitment evidence separately from SLA proofs.
type ComputationProof struct {
	VerificationClass VerificationClass `json:"verificationClass"`
	InputDigest       string            `json:"inputDigest"`
	OutputDigest      string            `json:"outputDigest"`
	ExecutionProof    string            `json:"executionProof"` // Deterministic execution trace / preimage / proof payload
	TokensProcessed   uint64            `json:"tokensProcessed"`
}

// CompletionClaim is the provider-signed claim submitted upon finishing compute work.
type CompletionClaim struct {
	JobID             string `json:"jobId"`
	QuoteHash         string `json:"quoteHash"`
	Provider          string `json:"provider"`
	OutputDigest      string `json:"outputDigest"`
	ExecutionProof    string `json:"executionProof"`
	TokensProcessed   uint64 `json:"tokensProcessed"`
	CompletedAt       int64  `json:"completedAt"`
	ChallengeDeadline int64  `json:"challengeDeadline"`
	ProviderSignature string `json:"providerSignature"`
}

// ChallengeEvidence carries objective, independently verifiable violation proof.
// Subjective assertions (ViolationSubjectiveQuality) are rejected before state mutation.
type ChallengeEvidence struct {
	ViolationType ViolationType `json:"violationType"`
	ProofDomain   ProofDomain   `json:"proofDomain"`

	// For ViolationFalseCompletionClaim: raw input payload or expected deterministic output
	// enabling independent recomputation without trusting Hanzo or any oracle.
	CanonicalInput       string `json:"canonicalInput,omitempty"`
	ObservedOutput       string `json:"observedOutput,omitempty"`
	ExpectedOutputDigest string `json:"expectedOutputDigest,omitempty"`

	// For ViolationInvalidSignatureOrEquivocation: second conflicting provider-signed CompletionClaim.
	ConflictingClaim *CompletionClaim `json:"conflictingClaim,omitempty"`

	// For ViolationSubjectiveQuality (always rejected): optional human note demonstrating non-objectivity.
	SubjectiveNote string `json:"subjectiveNote,omitempty"`
}

// ChallengeSubmission is the challenger-signed dispute opening request.
type ChallengeSubmission struct {
	JobID               string            `json:"jobId"`
	Challenger          string            `json:"challenger"`
	ChallengeBondAtomic uint64            `json:"challengeBondAtomic"`
	SubmittedAt         int64             `json:"submittedAt"`
	Evidence            ChallengeEvidence `json:"evidence"`
	EvidenceHash        string            `json:"evidenceHash"`
	ChallengerSignature string            `json:"challengerSignature"`
}

// PayoutRecord is the immutable receipt emitted once and only once when an escrow reaches a terminal state.
type PayoutRecord struct {
	JobID                   string           `json:"jobId"`
	TerminalState           EscrowState      `json:"terminalState"`
	ChallengeOutcome        ChallengeOutcome `json:"challengeOutcome,omitempty"`
	ProofDomain             ProofDomain      `json:"proofDomain,omitempty"`
	SettledAt               int64            `json:"settledAt"`
	BuyerPayoutAtomic       uint64           `json:"buyerPayoutAtomic"`
	ProviderPayoutAtomic    uint64           `json:"providerPayoutAtomic"`
	ChallengerPayoutAtomic  uint64           `json:"challengerPayoutAtomic"`
	ProviderSlashedAtomic   uint64           `json:"providerSlashedAtomic"`
	ChallengerSlashedAtomic uint64           `json:"challengerSlashedAtomic"`
	Reason                  string           `json:"reason"`
	ReceiptHash             string           `json:"receiptHash"`
}

// EscrowRecord represents the complete canonical state of a single compute escrow job.
type EscrowRecord struct {
	JobID               string                 `json:"jobId"`
	State               EscrowState            `json:"state"`
	Quote               JobQuote               `json:"quote"`
	Acceptance          *QuoteAcceptance       `json:"acceptance,omitempty"`
	Claim               *CompletionClaim       `json:"claim,omitempty"`
	ServiceProof        *ServiceGuaranteeProof `json:"serviceProof,omitempty"`
	ComputationProof    *ComputationProof      `json:"computationProof,omitempty"`
	Challenge           *ChallengeSubmission   `json:"challenge,omitempty"`
	Payout              *PayoutRecord          `json:"payout,omitempty"`
	LockedBuyerAtomic   uint64                 `json:"lockedBuyerAtomic"`
	LockedProviderBond  uint64                 `json:"lockedProviderBond"`
	LockedChallengeBond uint64                 `json:"lockedChallengeBond"`
	PayoutCount         uint64                 `json:"payoutCount"`
	FundedAt            int64                  `json:"fundedAt,omitempty"`
	Finalized           bool                   `json:"finalized"`
}

// ConservationReport proves exact atomic-unit conservation across the entire ledger.
type ConservationReport struct {
	InitialMintedSupply uint64 `json:"initialMintedSupply"`
	TotalAccountBalance uint64 `json:"totalAccountBalance"`
	TotalLockedEscrow   uint64 `json:"totalLockedEscrow"`
	TotalAccounted      uint64 `json:"totalAccounted"`
	Drift               int64  `json:"drift"`
	DoublePayoutCount   uint64 `json:"doublePayoutCount"`
	SubjectiveSlashes   uint64 `json:"subjectiveSlashes"`
	Conserved           bool   `json:"conserved"`
}

// LedgerEntry is a single hash-chained settlement event exported as JSONL.
type LedgerEntry struct {
	Seq       uint64      `json:"seq"`
	Timestamp int64       `json:"timestamp"`
	EventType string      `json:"eventType"`
	JobID     string      `json:"jobId,omitempty"`
	Account   string      `json:"account,omitempty"`
	Amount    uint64      `json:"amount,omitempty"`
	State     EscrowState `json:"state,omitempty"`
	Detail    string      `json:"detail,omitempty"`
	PrevHash  string      `json:"prevHash"`
	EntryHash string      `json:"entryHash"`
}

// LedgerAuditReport summarizes offline cryptographic verification of an exported JSONL ledger.
type LedgerAuditReport struct {
	EntriesVerified   int    `json:"entriesVerified"`
	HeadHash          string `json:"headHash"`
	ChainValid        bool   `json:"chainValid"`
	TotalMinted       uint64 `json:"totalMinted"`
	SettledCount      int    `json:"settledCount"`
	SlashedCount      int    `json:"slashedCount"`
	RefundedCount     int    `json:"refundedCount"`
	TamperDetected    bool   `json:"tamperDetected"`
	VerificationError string `json:"verificationError,omitempty"`
}

// EngineSnapshot is the complete portable state used for sovereign migration & offline verification.
type EngineSnapshot struct {
	Version             string            `json:"version"`
	Domain              EIP712Domain      `json:"domain"`
	Policy              Policy            `json:"policy"`
	InitialMintedSupply uint64            `json:"initialMintedSupply"`
	Balances            map[string]uint64 `json:"balances"`
	Escrows             []*EscrowRecord   `json:"escrows"`
	Ledger              []LedgerEntry     `json:"ledger"`
	HeadHash            string            `json:"headHash"`
	SnapshotDigest      string            `json:"snapshotDigest"`
}
