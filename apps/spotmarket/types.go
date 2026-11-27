// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import "errors"

// ProtocolVersion is the versioned domain tag included in every canonical
// commitment digest so offers, bids, receipts, and export bundles cannot be
// replayed across incompatible protocol versions.
const ProtocolVersion = "hip-1350/spotmarket/v1"

// VerificationClass explicitly classifies what a security check proves so
// callers and research reports never conflate cryptographic signatures,
// hardware attestations, and economically incentivized verification.
type VerificationClass string

const (
	// VerificationCryptoSignature proves signer key control and message
	// integrity via secp256k1 EIP-2 low-S ECDSA over a canonical SHA-256
	// commitment. It does NOT prove physical GPU possession or honest inference.
	VerificationCryptoSignature VerificationClass = "crypto_signature"

	// VerificationHardwareAttestation denotes an optional hardware TEE / GPU
	// confidential-compute quote. When absent, hardware claims remain unverified.
	VerificationHardwareAttestation VerificationClass = "hardware_attestation"

	// VerificationEconomicStake denotes bonded collateral escrow, SLA breach
	// slashing, and deterministic ledger conservation. It economically deters
	// bait-and-switch and non-delivery up to the posted bond, but is not a
	// mathematical proof of inference correctness.
	VerificationEconomicStake VerificationClass = "economic_stake_slashing"

	// VerificationUnverified marks records accepted when verification controls
	// are explicitly disabled (used strictly for negative-control benchmarks).
	VerificationUnverified VerificationClass = "unverified_control"
)

// Sentinel errors returned on fail-closed validation and settlement paths.
var (
	ErrInvalidSignature              = errors.New("spotmarket: invalid secp256k1 signature")
	ErrHighSMalleableSignature       = errors.New("spotmarket: high-S malleable signature rejected (EIP-2)")
	ErrCommitmentMismatch            = errors.New("spotmarket: canonical commitment digest mismatch")
	ErrExpiredOffer                  = errors.New("spotmarket: offer expired")
	ErrExpiredBid                    = errors.New("spotmarket: bid expired")
	ErrExpiredOrder                  = errors.New("spotmarket: order expired")
	ErrReplayNonce                   = errors.New("spotmarket: replayed nonce rejected")
	ErrSybilBidLimit                 = errors.New("spotmarket: provider exceeded per-order bid limit")
	ErrCapacityOverclaim             = errors.New("spotmarket: capacity claim exceeds available provider slots")
	ErrInsufficientBond              = errors.New("spotmarket: provider bond below minimum required collateral")
	ErrInsufficientBalance           = errors.New("spotmarket: insufficient account balance")
	ErrInvalidOffer                  = errors.New("spotmarket: invalid offer")
	ErrInvalidOrder                  = errors.New("spotmarket: invalid order")
	ErrInvalidBid                    = errors.New("spotmarket: invalid bid")
	ErrNotFound                      = errors.New("spotmarket: record not found")
	ErrLedgerDrift                   = errors.New("spotmarket: ledger conservation invariant violated")
	ErrInvalidExportBundle           = errors.New("spotmarket: invalid or tampered export bundle")
	ErrNoEligibleBids                = errors.New("spotmarket: no eligible bids satisfy order policy")
	ErrLeaseAlreadySettled           = errors.New("spotmarket: lease already settled")
	ErrArithmeticOverflow            = errors.New("spotmarket: integer arithmetic overflow")
	ErrUnauthorizedProviderKey       = errors.New("spotmarket: provider signing key not bound to provider identity")
	ErrLeaseNotEligibleForFailover   = errors.New("spotmarket: lease is not eligible for failover")
	ErrUnverifiedHardwareAttestation = errors.New("spotmarket: hardware attestation quote not verified")
)

// AttestationVerifier validates a provider's hardware TEE / confidential-compute
// attestation claim (e.g. NVIDIA CC, Intel TDX, AMD SEV-SNP quote). Returning
// nil certifies that the offer's AttestationQuoteHash was externally verified.
// When nil (unconfigured), hardware attestation verification fails closed.
type AttestationVerifier func(offer Offer) error

// GPUHardwareSpec describes a provider's offered GPU hardware capabilities.
type GPUHardwareSpec struct {
	GPUModel        string  `json:"gpuModel"`
	GPUCount        int     `json:"gpuCount"`
	AvailableVRAMGB int     `json:"availableVramGb"`
	TotalVRAMGB     int     `json:"totalVramGb"`
	Interconnect    string  `json:"interconnect"`
	FP8TFLOPS       float64 `json:"fp8Tflops"`
}

// LocationSpec describes geographic and jurisdictional residency attributes.
type LocationSpec struct {
	Region         string `json:"region"`
	Jurisdiction   string `json:"jurisdiction"`
	DatacenterTier int    `json:"datacenterTier"`
}

// PricingSpec expresses integer microdollar pricing ($1.00 = 1_000_000 microUSD)
// to guarantee deterministic ledger settlement with zero floating-point drift.
type PricingSpec struct {
	MicroUSDPer1MTokens int64 `json:"microUsdPer1MTokens"`
	MicroUSDPerGPUHour  int64 `json:"microUsdPerGpuHour"`
	MinBillableTokens   int64 `json:"minBillableTokens"`
}

// ServiceCommitment declares a provider's advertised SLO and posted collateral.
type ServiceCommitment struct {
	TargetTTFTMs           int64   `json:"targetTtftMs"`
	MinThroughputTokPerSec float64 `json:"minThroughputTokPerSec"`
	UptimeBPS              int     `json:"uptimeBps"`
	CollateralMicroUSD     int64   `json:"collateralMicroUsd"`
	SlashBPSOnBreach       int     `json:"slashBpsOnBreach"`
}

// Offer is the canonical unsigned capacity advertisement published by a GPU
// provider into the open spot market.
type Offer struct {
	ProtocolVersion      string            `json:"protocolVersion"`
	OfferID              string            `json:"offerId"`
	ProviderID           string            `json:"providerId"`
	ProviderPubKeyHex    string            `json:"providerPubKeyHex"`
	OperatorClass        string            `json:"operatorClass"` // informational only; ignored by Enso ranker
	Hardware             GPUHardwareSpec   `json:"hardware"`
	SupportedModels      []string          `json:"supportedModels"`
	Location             LocationSpec      `json:"location"`
	Pricing              PricingSpec       `json:"pricing"`
	SLO                  ServiceCommitment `json:"slo"`
	AvailableSlots       int               `json:"availableSlots"`
	IssuedAtUnix         int64             `json:"issuedAtUnix"`
	ExpiresAtUnix        int64             `json:"expiresAtUnix"`
	Nonce                uint64            `json:"nonce"`
	AttestationClass     VerificationClass `json:"attestationClass"`
	AttestationQuoteHash string            `json:"attestationQuoteHash,omitempty"`
}

// SignedOffer pairs a canonical Offer with its SHA-256 commitment and
// secp256k1 EIP-2 low-S signature.
type SignedOffer struct {
	Offer             Offer             `json:"offer"`
	CommitmentHex     string            `json:"commitmentHex"`
	SignatureHex      string            `json:"signatureHex"`
	VerificationClass VerificationClass `json:"verificationClass"`
}

// RankingWeights specifies user-controlled preference weights for Enso multi-
// dimensional offer/bid selection. Weights are normalized to sum to 1.0.
type RankingWeights struct {
	PriceWeight       float64 `json:"priceWeight"`
	LatencyWeight     float64 `json:"latencyWeight"`
	ReliabilityWeight float64 `json:"reliabilityWeight"`
}

// DefaultRankingWeights returns balanced Enso selection weights.
func DefaultRankingWeights() RankingWeights {
	return RankingWeights{
		PriceWeight:       0.45,
		LatencyWeight:     0.30,
		ReliabilityWeight: 0.25,
	}
}

// Order is a buyer's capacity / inference auction request with policy
// constraints and locked microdollar escrow.
type Order struct {
	OrderID                 string         `json:"orderId"`
	BuyerID                 string         `json:"buyerId"`
	Model                   string         `json:"model"`
	RequestedTokens         int64          `json:"requestedTokens"`
	MinVRAMGB               int            `json:"minVramGb"`
	MaxMicroUSDPer1MTokens  int64          `json:"maxMicroUsdPer1MTokens"`
	AllowedRegions          []string       `json:"allowedRegions,omitempty"`
	AllowedJurisdictions    []string       `json:"allowedJurisdictions,omitempty"`
	BlockedJurisdictions    []string       `json:"blockedJurisdictions,omitempty"`
	MaxTTFTMs               int64          `json:"maxTtftMs,omitempty"`
	MinReliabilityBPS       int            `json:"minReliabilityBps,omitempty"`
	AllowPartialFill        bool           `json:"allowPartialFill"`
	EscrowMicroUSD          int64          `json:"escrowMicroUsd"`
	CreatedAtUnix           int64          `json:"createdAtUnix"`
	ExpiresAtUnix           int64          `json:"expiresAtUnix"`
	RankingWeights          RankingWeights `json:"rankingWeights"`
	RequireHardwareAttested bool           `json:"requireHardwareAttested,omitempty"`
}

// Bid is a provider's Akash-inspired reverse-auction bid against a specific Order.
type Bid struct {
	ProtocolVersion        string `json:"protocolVersion"`
	BidID                  string `json:"bidId"`
	OrderID                string `json:"orderId"`
	OfferID                string `json:"offerId"`
	ProviderID             string `json:"providerId"`
	ProviderPubKeyHex      string `json:"providerPubKeyHex"`
	BidMicroUSDPer1MTokens int64  `json:"bidMicroUsdPer1MTokens"`
	OfferedTokens          int64  `json:"offeredTokens"`
	CommittedVRAMGB        int    `json:"committedVramGb"`
	BondMicroUSD           int64  `json:"bondMicroUsd"`
	IssuedAtUnix           int64  `json:"issuedAtUnix"`
	ExpiresAtUnix          int64  `json:"expiresAtUnix"`
	Nonce                  uint64 `json:"nonce"`
}

// SignedBid pairs a reverse-auction Bid with its canonical SHA-256 commitment
// and secp256k1 EIP-2 low-S signature.
type SignedBid struct {
	Bid               Bid               `json:"bid"`
	CommitmentHex     string            `json:"commitmentHex"`
	SignatureHex      string            `json:"signatureHex"`
	VerificationClass VerificationClass `json:"verificationClass"`
}

// LeaseStatus tracks the lifecycle state of an awarded spot-market lease.
type LeaseStatus string

const (
	LeaseActive             LeaseStatus = "active"
	LeaseCompleted          LeaseStatus = "completed"
	LeasePartialCompleted   LeaseStatus = "partial_completed"
	LeaseSlashedNonDelivery LeaseStatus = "slashed_non_delivery"
	LeaseSlashedBaitSwitch  LeaseStatus = "slashed_bait_and_switch"
	LeaseSlashedSLABreach   LeaseStatus = "slashed_sla_breach"
	LeaseFailedOver         LeaseStatus = "failed_over"
	LeaseExpired            LeaseStatus = "expired"
)

// Lease is the awarded capacity contract created when Enso clears an Order.
type Lease struct {
	LeaseID                   string      `json:"leaseId"`
	OrderID                   string      `json:"orderId"`
	BidID                     string      `json:"bidId"`
	OfferID                   string      `json:"offerId"`
	BuyerID                   string      `json:"buyerId"`
	ProviderID                string      `json:"providerId"`
	Status                    LeaseStatus `json:"status"`
	AllocatedTokens           int64       `json:"allocatedTokens"`
	DeliveredTokens           int64       `json:"deliveredTokens"`
	AgreedMicroUSDPer1MTokens int64       `json:"agreedMicroUsdPer1MTokens"`
	EscrowLockedMicroUSD      int64       `json:"escrowLockedMicroUsd"`
	ProviderBondMicroUSD      int64       `json:"providerBondMicroUsd"`
	SlashBPSOnBreach          int         `json:"slashBpsOnBreach"`
	TargetTTFTMs              int64       `json:"targetTtftMs,omitempty"`
	MinThroughputTokPerSec    float64     `json:"minThroughputTokPerSec,omitempty"`
	StartedAtUnix             int64       `json:"startedAtUnix"`
	SettledAtUnix             int64       `json:"settledAtUnix,omitempty"`
	FailoverFromLeaseID       string      `json:"failoverFromLeaseId,omitempty"`
	FailoverToLeaseID         string      `json:"failoverToLeaseId,omitempty"`
}

// DeliveryReport records measured execution telemetry and token delivery for a
// Lease so settlement can enforce pricing, SLA commitments, and slashing.
type DeliveryReport struct {
	LeaseID                      string  `json:"leaseId"`
	DeliveredTokens              int64   `json:"deliveredTokens"`
	ObservedTTFTMs               float64 `json:"observedTtftMs"`
	ObservedLatencyMs            float64 `json:"observedLatencyMs"`
	ObservedTokPerSec            float64 `json:"observedTokPerSec"`
	Completed                    bool    `json:"completed"`
	FailureReason                string  `json:"failureReason,omitempty"`
	ReportedMicroUSDPer1MTokens  int64   `json:"reportedMicroUsdPer1MTokens,omitempty"`
	BaitAndSwitchPriceAttempted  bool    `json:"baitAndSwitchPriceAttempted,omitempty"`
	VerificationOverheadMicroUSD int64   `json:"verificationOverheadMicroUsd,omitempty"`
}

// SettlementReceipt is the deterministic financial clearing record emitted on
// lease completion, partial delivery, or SLA-breach slashing.
type SettlementReceipt struct {
	ReceiptID                    string            `json:"receiptId"`
	LeaseID                      string            `json:"leaseId"`
	OrderID                      string            `json:"orderId"`
	BidID                        string            `json:"bidId"`
	BuyerID                      string            `json:"buyerId"`
	ProviderID                   string            `json:"providerId"`
	Status                       LeaseStatus       `json:"status"`
	AllocatedTokens              int64             `json:"allocatedTokens"`
	DeliveredTokens              int64             `json:"deliveredTokens"`
	AgreedMicroUSDPer1MTokens    int64             `json:"agreedMicroUsdPer1MTokens"`
	BuyerEscrowLockedMicroUSD    int64             `json:"buyerEscrowLockedMicroUsd"`
	ProviderPayoutMicroUSD       int64             `json:"providerPayoutMicroUsd"`
	BuyerRefundMicroUSD          int64             `json:"buyerRefundMicroUsd"`
	SlashedBondMicroUSD          int64             `json:"slashedBondMicroUsd"`
	BondReturnedMicroUSD         int64             `json:"bondReturnedMicroUsd"`
	VerificationOverheadMicroUSD int64             `json:"verificationOverheadMicroUsd"`
	VerificationClass            VerificationClass `json:"verificationClass"`
	SettledAtUnix                int64             `json:"settledAtUnix"`
	ReceiptHashHex               string            `json:"receiptHashHex"`
}

// ProviderTelemetry holds measured execution statistics used by the Enso
// selector. It is derived exclusively from observed leases, never operator tags.
type ProviderTelemetry struct {
	ProviderID          string  `json:"providerId"`
	MeasuredTTFTMs      float64 `json:"measuredTtftMs"`
	MeasuredLatencyMs   float64 `json:"measuredLatencyMs"`
	MeasuredTokPerSec   float64 `json:"measuredTokPerSec"`
	CompletedJobs       int64   `json:"completedJobs"`
	FailedJobs          int64   `json:"failedJobs"`
	SlashedJobs         int64   `json:"slashedJobs"`
	ReliabilityBPS      int     `json:"reliabilityBps"`
	ObservedSampleCount int64   `json:"observedSampleCount"`
}

// Rejection records why a bid or offer was excluded during validation or Enso
// ranking.
type Rejection struct {
	EntityID          string            `json:"entityId"`
	ProviderID        string            `json:"providerId"`
	Reason            string            `json:"reason"`
	VerificationClass VerificationClass `json:"verificationClass"`
}

// PeerNode represents a permissionless gossip peer in the open spot market.
type PeerNode struct {
	PeerID        string   `json:"peerId"`
	Endpoint      string   `json:"endpoint"`
	PubKeyHex     string   `json:"pubKeyHex"`
	Regions       []string `json:"regions"`
	LastSeenUnix  int64    `json:"lastSeenUnix"`
	OperatorClass string   `json:"operatorClass"`
}

// GossipSnapshot is a point-in-time permissionless view of peers, offers, and
// measured telemetry that any node can synchronize without a central registry.
type GossipSnapshot struct {
	ProtocolVersion string                       `json:"protocolVersion"`
	GeneratedAtUnix int64                        `json:"generatedAtUnix"`
	Peers           []PeerNode                   `json:"peers"`
	ActiveOffers    []SignedOffer                `json:"activeOffers"`
	Telemetry       map[string]ProviderTelemetry `json:"telemetry"`
	StateRootHex    string                       `json:"stateRootHex"`
}

// LedgerSnapshot captures exact integer microdollar balances across all
// participants for sovereignty export and conservation verification.
type LedgerSnapshot struct {
	BuyerBalances          map[string]int64 `json:"buyerBalances"`
	BuyerEscrow            map[string]int64 `json:"buyerEscrow"`
	ProviderBalances       map[string]int64 `json:"providerBalances"`
	ProviderLockedBonds    map[string]int64 `json:"providerLockedBonds"`
	TotalDepositedMicroUSD int64            `json:"totalDepositedMicroUsd"`
	ConservationDrift      int64            `json:"conservationDrift"`
}

// MarketExportBundle is a cryptographically signed, self-contained state dump
// allowing any user or operator to verify, migrate, or fork the entire market
// with zero dependency on Hanzo infrastructure.
type MarketExportBundle struct {
	ProtocolVersion             string              `json:"protocolVersion"`
	ExportedAtUnix              int64               `json:"exportedAtUnix"`
	ExporterPubKeyHex           string              `json:"exporterPubKeyHex"`
	Gossip                      GossipSnapshot      `json:"gossip"`
	Ledger                      LedgerSnapshot      `json:"ledger"`
	ProviderKeys                map[string]string   `json:"providerKeys,omitempty"`
	VerifiedAttestationOfferIDs []string            `json:"verifiedAttestationOfferIds,omitempty"`
	Orders                      []Order             `json:"orders,omitempty"`
	ClearedOrderIDs             []string            `json:"clearedOrderIds,omitempty"`
	PendingBids                 []SignedBid         `json:"pendingBids,omitempty"`
	Leases                      []Lease             `json:"leases"`
	Receipts                    []SettlementReceipt `json:"receipts"`
	CommitmentHex               string              `json:"commitmentHex"`
	SignatureHex                string              `json:"signatureHex"`
	VerificationClass           VerificationClass   `json:"verificationClass"`
}
