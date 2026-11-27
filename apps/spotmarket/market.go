// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"encoding/hex"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/luxfi/crypto/secp256k1"
)

// Config controls verification, collateral slashing, anti-Sybil limits, and
// decentralized operation for a Market instance.
type Config struct {
	// VerifySignatures enforces EIP-2 low-S secp256k1 signature verification
	// on all offers and bids. Default true; only disabled in explicit negative-
	// control benchmarks.
	VerifySignatures bool

	// EnforceSlashing enforces minimum provider collateral bonds, strict agreed-
	// price billing, and SLA-breach / bait-and-switch bond slashing. Default true.
	EnforceSlashing bool

	// MaxPendingBidsPerProviderPerOrder caps how many bids a single provider may
	// submit against one Order to prevent Sybil/spam flooding. Default 1.
	MaxPendingBidsPerProviderPerOrder int

	// MinBondBPS is the minimum provider collateral required per bid as basis
	// points of maximum bid value (e.g. 2500 = 25%). Default 2500.
	MinBondBPS int

	// VerificationCostPerSigMicroUSD is the explicitly metered compute cost per
	// cryptographic verification operation in microdollars. Default 2 (0.000002 USD).
	VerificationCostPerSigMicroUSD int64

	// SettlementCostPerReceiptMicroUSD is the explicitly metered ledger clearing
	// overhead per settlement receipt in microdollars. Default 5 (0.000005 USD).
	SettlementCostPerReceiptMicroUSD int64

	// HanzoControlPlaneDisabled models full decentralized operation with all
	// Hanzo-operated infrastructure unreachable. The protocol never calls Hanzo
	// infrastructure in either mode (HanzoCalls == 0).
	HanzoControlPlaneDisabled bool

	// AttestationVerifier optionally verifies hardware TEE / confidential-compute
	// quotes attached to Offers claiming VerificationHardwareAttestation.
	// When nil, hardware attestation claims fail closed and never satisfy
	// Order.RequireHardwareAttested.
	AttestationVerifier AttestationVerifier
}

// DefaultConfig returns production-safe fail-closed defaults for the open spot market.
func DefaultConfig() Config {
	return Config{
		VerifySignatures:                  true,
		EnforceSlashing:                   true,
		MaxPendingBidsPerProviderPerOrder: 1,
		MinBondBPS:                        2500,
		VerificationCostPerSigMicroUSD:    2,
		SettlementCostPerReceiptMicroUSD:  5,
		HanzoControlPlaneDisabled:         true,
	}
}

// mulDivCeilChecked computes ceil(a * b / denom) for non-negative int64 inputs
// using exact 128-bit unsigned arithmetic and fails with ErrArithmeticOverflow
// whenever the result exceeds math.MaxInt64.
func mulDivCeilChecked(a, b, denom int64) (int64, error) {
	if denom <= 0 {
		return 0, ErrArithmeticOverflow
	}
	if a < 0 || b < 0 {
		return 0, ErrArithmeticOverflow
	}
	if a == 0 || b == 0 {
		return 0, nil
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	lo, carry := bits.Add64(lo, uint64(denom-1), 0)
	hi += carry
	if hi >= uint64(denom) {
		return 0, ErrArithmeticOverflow
	}
	q, _ := bits.Div64(hi, lo, uint64(denom))
	if q > uint64(math.MaxInt64) {
		return 0, ErrArithmeticOverflow
	}
	return int64(q), nil
}

// mulDivFloorChecked computes floor(a * b / denom) for non-negative int64 inputs
// using exact 128-bit unsigned arithmetic and fails with ErrArithmeticOverflow
// whenever the result exceeds math.MaxInt64.
func mulDivFloorChecked(a, b, denom int64) (int64, error) {
	if denom <= 0 {
		return 0, ErrArithmeticOverflow
	}
	if a < 0 || b < 0 {
		return 0, ErrArithmeticOverflow
	}
	if a == 0 || b == 0 {
		return 0, nil
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(denom) {
		return 0, ErrArithmeticOverflow
	}
	q, _ := bits.Div64(hi, lo, uint64(denom))
	if q > uint64(math.MaxInt64) {
		return 0, ErrArithmeticOverflow
	}
	return int64(q), nil
}

func addInt64Checked(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, ErrArithmeticOverflow
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, ErrArithmeticOverflow
	}
	return a + b, nil
}

// ComputeTokenCostMicroUSDChecked calculates the exact integer microdollar cost
// for `tokens` billed at `microUSDPer1M` microdollars per 1,000,000 tokens using
// exact 128-bit arithmetic, returning ErrArithmeticOverflow if the result cannot
// be represented as a non-negative int64.
func ComputeTokenCostMicroUSDChecked(tokens int64, microUSDPer1M int64) (int64, error) {
	if tokens < 0 || microUSDPer1M < 0 {
		return 0, ErrInvalidOrder
	}
	if tokens == 0 || microUSDPer1M == 0 {
		return 0, nil
	}
	return mulDivCeilChecked(tokens, microUSDPer1M, 1_000_000)
}

// ComputeTokenCostMicroUSD calculates the exact integer microdollar cost for
// `tokens` billed at `microUSDPer1M` microdollars per 1,000,000 tokens. On
// overflow it saturates to math.MaxInt64 (never wrapping negative). Financial
// mutations MUST call ComputeTokenCostMicroUSDChecked and reject overflows.
func ComputeTokenCostMicroUSD(tokens int64, microUSDPer1M int64) int64 {
	if tokens <= 0 || microUSDPer1M <= 0 {
		return 0
	}
	cost, err := ComputeTokenCostMicroUSDChecked(tokens, microUSDPer1M)
	if err != nil {
		return math.MaxInt64
	}
	return cost
}

// ProviderIDFromPubKey derives a canonical self-certifying provider identity
// directly from a compressed secp256k1 public key hex string.
func ProviderIDFromPubKey(pubKeyHex string) string {
	return "pk:" + strings.ToLower(strings.TrimSpace(pubKeyHex))
}

func canonicalCompressedPubKeyHex(pubKeyHex string) (string, error) {
	clean := strings.ToLower(strings.TrimSpace(pubKeyHex))
	raw, err := hex.DecodeString(clean)
	if err != nil || len(raw) != 33 {
		return "", fmt.Errorf("%w: compressed secp256k1 pubkey must be 33 bytes", ErrInvalidSignature)
	}
	if _, err := secp256k1.ToPublicKey(raw); err != nil {
		return "", fmt.Errorf("%w: invalid secp256k1 pubkey: %v", ErrInvalidSignature, err)
	}
	return clean, nil
}

// Market is a self-contained, thread-safe reverse-auction GPU spot market,
// escrow/bond ledger, Enso telemetry tracker, and permissionless peer registry.
type Market struct {
	mu  sync.RWMutex
	cfg Config

	// Offers, orders, bids, leases, and receipts
	offers               map[string]SignedOffer
	orders               map[string]Order
	orderCleared         map[string]bool
	bidsByOrder          map[string][]SignedBid
	bidByID              map[string]SignedBid
	leases               map[string]*Lease
	receipts             map[string]SettlementReceipt
	receiptOrder         []string
	leaseOrder           []string
	telemetry            map[string]ProviderTelemetry
	peers                map[string]PeerNode
	activeSlotsUsed      map[string]int
	providerKeys         map[string]string
	verifiedAttestations map[string]bool

	// Anti-replay & anti-Sybil tracking
	usedOfferNonces map[string]map[uint64]struct{}
	usedBidNonces   map[string]map[uint64]struct{}
	orderProvBids   map[string]map[string]int

	// Exact integer microdollar accounting
	buyerBalances          map[string]int64
	buyerEscrow            map[string]int64
	providerBalances       map[string]int64
	providerLockedBonds    map[string]int64
	totalDepositedMicroUSD int64

	// Metering & decentralization counters
	sigVerifications   atomic.Int64
	hanzoCalls         atomic.Int64
	leaseSeq           int64
	receiptSeq         int64
	verificationCostMu int64
}

// NewMarket initializes a new standalone spot market with the supplied config.
func NewMarket(cfg Config) *Market {
	if cfg.MaxPendingBidsPerProviderPerOrder <= 0 {
		cfg.MaxPendingBidsPerProviderPerOrder = 1
	}
	if cfg.MinBondBPS < 0 {
		cfg.MinBondBPS = 0
	}
	return &Market{
		cfg:                  cfg,
		offers:               make(map[string]SignedOffer),
		orders:               make(map[string]Order),
		orderCleared:         make(map[string]bool),
		bidsByOrder:          make(map[string][]SignedBid),
		bidByID:              make(map[string]SignedBid),
		leases:               make(map[string]*Lease),
		receipts:             make(map[string]SettlementReceipt),
		telemetry:            make(map[string]ProviderTelemetry),
		peers:                make(map[string]PeerNode),
		activeSlotsUsed:      make(map[string]int),
		providerKeys:         make(map[string]string),
		verifiedAttestations: make(map[string]bool),
		usedOfferNonces:      make(map[string]map[uint64]struct{}),
		usedBidNonces:        make(map[string]map[uint64]struct{}),
		orderProvBids:        make(map[string]map[string]int),
		buyerBalances:        make(map[string]int64),
		buyerEscrow:          make(map[string]int64),
		providerBalances:     make(map[string]int64),
		providerLockedBonds:  make(map[string]int64),
	}
}

// Config returns the active market configuration.
func (m *Market) Config() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// HanzoCalls returns the number of calls made to Hanzo-operated infrastructure.
// In the decentralized spot market protocol this is always 0.
func (m *Market) HanzoCalls() int64 {
	return m.hanzoCalls.Load()
}

// SignatureVerifications returns the cumulative count of cryptographic
// signature verifications performed by the market.
func (m *Market) SignatureVerifications() int64 {
	return m.sigVerifications.Load()
}

// TotalVerificationCostMicroUSD returns cumulative explicitly metered
// cryptographic verification overhead in microdollars.
func (m *Market) TotalVerificationCostMicroUSD() int64 {
	return m.sigVerifications.Load() * m.cfg.VerificationCostPerSigMicroUSD
}

// BindProviderKey binds a provider ledger identity to its authorized 33-byte
// compressed secp256k1 public key. Once bound, offers and bids naming providerID
// MUST be signed by this key (fail closed). Calling BindProviderKey again with
// the same key is idempotent; attempting to rebind to a different key fails
// with ErrUnauthorizedProviderKey.
func (m *Market) BindProviderKey(providerID, pubKeyHex string) error {
	if strings.TrimSpace(providerID) == "" {
		return ErrInvalidOffer
	}
	canon, err := canonicalCompressedPubKeyHex(pubKeyHex)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.providerKeys[providerID]; ok {
		if existing != canon {
			return fmt.Errorf("%w: provider %s already bound to a different key", ErrUnauthorizedProviderKey, providerID)
		}
		return nil
	}
	m.providerKeys[providerID] = canon
	return nil
}

// ProviderPubKey returns the authorized compressed secp256k1 public key hex
// bound to providerID, if any.
func (m *Market) ProviderPubKey(providerID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.providerKeys[providerID]
	return k, ok
}

// verifyProviderKeyLocked checks that pubKeyHex is authorized to act for
// providerID. Self-certifying key-derived provider IDs (equal to pubKeyHex or
// ProviderIDFromPubKey(pubKeyHex)) are automatically bound on first verified
// use; any other providerID MUST already be explicitly bound via BindProviderKey.
func (m *Market) verifyProviderKeyLocked(providerID, pubKeyHex string) error {
	canon, err := canonicalCompressedPubKeyHex(pubKeyHex)
	if err != nil {
		return err
	}
	if bound, ok := m.providerKeys[providerID]; ok {
		if bound != canon {
			return fmt.Errorf("%w: key does not match bound provider %s", ErrUnauthorizedProviderKey, providerID)
		}
		return nil
	}
	if strings.EqualFold(providerID, canon) || providerID == ProviderIDFromPubKey(canon) {
		m.providerKeys[providerID] = canon
		return nil
	}
	return fmt.Errorf("%w: provider %s has no bound signing key", ErrUnauthorizedProviderKey, providerID)
}

// DepositBuyer credits a buyer's integer microdollar ledger balance.
func (m *Market) DepositBuyer(buyerID string, microUSD int64) error {
	if buyerID == "" || microUSD <= 0 {
		return ErrInsufficientBalance
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	nextBal, err := addInt64Checked(m.buyerBalances[buyerID], microUSD)
	if err != nil {
		return err
	}
	nextTotal, err := addInt64Checked(m.totalDepositedMicroUSD, microUSD)
	if err != nil {
		return err
	}
	m.buyerBalances[buyerID] = nextBal
	m.totalDepositedMicroUSD = nextTotal
	return nil
}

// DepositProvider credits a provider's integer microdollar collateral balance.
func (m *Market) DepositProvider(providerID string, microUSD int64) error {
	if providerID == "" || microUSD <= 0 {
		return ErrInsufficientBalance
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	nextBal, err := addInt64Checked(m.providerBalances[providerID], microUSD)
	if err != nil {
		return err
	}
	nextTotal, err := addInt64Checked(m.totalDepositedMicroUSD, microUSD)
	if err != nil {
		return err
	}
	m.providerBalances[providerID] = nextBal
	m.totalDepositedMicroUSD = nextTotal
	return nil
}

// DepositProviderWithKey atomically binds providerID to pubKeyHex and credits
// microUSD to the provider's collateral balance.
func (m *Market) DepositProviderWithKey(providerID, pubKeyHex string, microUSD int64) error {
	if strings.TrimSpace(providerID) == "" || microUSD <= 0 {
		return ErrInsufficientBalance
	}
	canon, err := canonicalCompressedPubKeyHex(pubKeyHex)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.providerKeys[providerID]; ok && existing != canon {
		return fmt.Errorf("%w: provider %s already bound to a different key", ErrUnauthorizedProviderKey, providerID)
	}
	nextBal, err := addInt64Checked(m.providerBalances[providerID], microUSD)
	if err != nil {
		return err
	}
	nextTotal, err := addInt64Checked(m.totalDepositedMicroUSD, microUSD)
	if err != nil {
		return err
	}
	m.providerKeys[providerID] = canon
	m.providerBalances[providerID] = nextBal
	m.totalDepositedMicroUSD = nextTotal
	return nil
}

// BuyerBalance returns a buyer's available and escrowed microdollar balances.
func (m *Market) BuyerBalance(buyerID string) (available int64, escrowed int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buyerBalances[buyerID], m.buyerEscrow[buyerID]
}

// ProviderBalance returns a provider's available and locked-bond balances.
func (m *Market) ProviderBalance(providerID string) (available int64, lockedBond int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providerBalances[providerID], m.providerLockedBonds[providerID]
}

// LedgerSnapshot returns an immutable copy of all ledger accounts along with
// the exact conservation drift (which must always be 0).
func (m *Market) LedgerSnapshot() LedgerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ledgerSnapshotLocked()
}

func (m *Market) ledgerSnapshotLocked() LedgerSnapshot {
	snap := LedgerSnapshot{
		BuyerBalances:          copyInt64Map(m.buyerBalances),
		BuyerEscrow:            copyInt64Map(m.buyerEscrow),
		ProviderBalances:       copyInt64Map(m.providerBalances),
		ProviderLockedBonds:    copyInt64Map(m.providerLockedBonds),
		TotalDepositedMicroUSD: m.totalDepositedMicroUSD,
	}
	var sum int64
	for _, v := range snap.BuyerBalances {
		sum += v
	}
	for _, v := range snap.BuyerEscrow {
		sum += v
	}
	for _, v := range snap.ProviderBalances {
		sum += v
	}
	for _, v := range snap.ProviderLockedBonds {
		sum += v
	}
	snap.ConservationDrift = sum - snap.TotalDepositedMicroUSD
	return snap
}

// CheckConservation validates that total balances across buyer available,
// buyer escrow, provider available, and provider locked bonds equal total
// deposited funds with zero microdollar drift.
func (m *Market) CheckConservation() error {
	snap := m.LedgerSnapshot()
	if snap.ConservationDrift != 0 {
		return fmt.Errorf("%w: drift=%d microUSD", ErrLedgerDrift, snap.ConservationDrift)
	}
	return nil
}

// RegisterPeer adds or updates a permissionless peer node in the local gossip
// registry.
func (m *Market) RegisterPeer(peer PeerNode) error {
	if peer.PeerID == "" || peer.Endpoint == "" {
		return fmt.Errorf("spotmarket: peer requires id and endpoint")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	regions := append([]string(nil), peer.Regions...)
	sort.Strings(regions)
	peer.Regions = regions
	m.peers[peer.PeerID] = peer
	return nil
}

// RegisterOffer validates and indexes a provider's SignedOffer.
func (m *Market) RegisterOffer(so SignedOffer, nowUnix int64) error {
	o := so.Offer
	if o.OfferID == "" || o.ProviderID == "" || o.Hardware.GPUModel == "" || len(o.SupportedModels) == 0 {
		return ErrInvalidOffer
	}
	if o.Pricing.MicroUSDPer1MTokens <= 0 || o.Hardware.AvailableVRAMGB <= 0 || o.AvailableSlots <= 0 {
		return ErrInvalidOffer
	}
	if o.ExpiresAtUnix <= nowUnix {
		return ErrExpiredOffer
	}

	if m.cfg.VerifySignatures {
		m.sigVerifications.Add(1)
		if err := VerifySignedOffer(so); err != nil {
			return err
		}
	}

	// Evaluate hardware attestation strictly through a configured trusted
	// AttestationVerifier; never trust a provider's self-signed claim alone.
	attestVerified := false
	if o.AttestationClass == VerificationHardwareAttestation &&
		strings.TrimSpace(o.AttestationQuoteHash) != "" &&
		m.cfg.AttestationVerifier != nil {
		if err := m.cfg.AttestationVerifier(o); err == nil {
			attestVerified = true
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, dup := m.offers[o.OfferID]; dup {
		return fmt.Errorf("%w: duplicate offer %s", ErrInvalidOffer, o.OfferID)
	}

	if m.cfg.VerifySignatures {
		if err := m.verifyProviderKeyLocked(o.ProviderID, o.ProviderPubKeyHex); err != nil {
			return err
		}
		if seen, ok := m.usedOfferNonces[o.ProviderPubKeyHex]; ok {
			if _, dup := seen[o.Nonce]; dup {
				return ErrReplayNonce
			}
		} else {
			m.usedOfferNonces[o.ProviderPubKeyHex] = make(map[uint64]struct{})
		}
		m.usedOfferNonces[o.ProviderPubKeyHex][o.Nonce] = struct{}{}
	}

	// Copy slice for immutability
	models := append([]string(nil), o.SupportedModels...)
	sort.Strings(models)
	so.Offer.SupportedModels = models
	m.offers[o.OfferID] = so
	if attestVerified {
		m.verifiedAttestations[o.OfferID] = true
	} else {
		delete(m.verifiedAttestations, o.OfferID)
	}

	if _, exists := m.telemetry[o.ProviderID]; !exists {
		ttft := float64(o.SLO.TargetTTFTMs)
		if ttft <= 0 {
			ttft = 180.0
		}
		m.telemetry[o.ProviderID] = ProviderTelemetry{
			ProviderID:        o.ProviderID,
			MeasuredTTFTMs:    ttft,
			MeasuredLatencyMs: ttft * 3.0,
			MeasuredTokPerSec: o.SLO.MinThroughputTokPerSec,
			ReliabilityBPS:    9000,
		}
	}
	return nil
}

// ListOffers returns active non-expired offers sorted deterministically by OfferID.
func (m *Market) ListOffers(nowUnix int64) []SignedOffer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SignedOffer, 0, len(m.offers))
	for _, so := range m.offers {
		if so.Offer.ExpiresAtUnix > nowUnix {
			out = append(out, so)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Offer.OfferID < out[j].Offer.OfferID
	})
	return out
}

// SubmitOrder registers a buyer's auction order and locks its escrow in the ledger.
func (m *Market) SubmitOrder(order Order, nowUnix int64) error {
	if order.OrderID == "" || order.BuyerID == "" || order.Model == "" || order.RequestedTokens <= 0 || order.MaxMicroUSDPer1MTokens <= 0 {
		return ErrInvalidOrder
	}
	if order.EscrowMicroUSD < 0 {
		return ErrInvalidOrder
	}
	if order.ExpiresAtUnix <= nowUnix {
		return ErrExpiredOrder
	}
	minEscrow, err := ComputeTokenCostMicroUSDChecked(order.RequestedTokens, order.MaxMicroUSDPer1MTokens)
	if err != nil {
		return err
	}
	if order.EscrowMicroUSD == 0 {
		order.EscrowMicroUSD = minEscrow
	}
	if order.EscrowMicroUSD < minEscrow {
		return fmt.Errorf("%w: escrow %d < required ceiling %d", ErrInsufficientBalance, order.EscrowMicroUSD, minEscrow)
	}

	// Copy slices for immutability.
	if len(order.AllowedRegions) > 0 {
		order.AllowedRegions = append([]string(nil), order.AllowedRegions...)
	}
	if len(order.AllowedJurisdictions) > 0 {
		order.AllowedJurisdictions = append([]string(nil), order.AllowedJurisdictions...)
	}
	if len(order.BlockedJurisdictions) > 0 {
		order.BlockedJurisdictions = append([]string(nil), order.BlockedJurisdictions...)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.orders[order.OrderID]; dup {
		return fmt.Errorf("%w: duplicate order %s", ErrInvalidOrder, order.OrderID)
	}
	if m.buyerBalances[order.BuyerID] < order.EscrowMicroUSD {
		return fmt.Errorf("%w: buyer %s balance %d < escrow %d", ErrInsufficientBalance, order.BuyerID, m.buyerBalances[order.BuyerID], order.EscrowMicroUSD)
	}
	nextEscrow, err := addInt64Checked(m.buyerEscrow[order.BuyerID], order.EscrowMicroUSD)
	if err != nil {
		return err
	}
	m.buyerBalances[order.BuyerID] -= order.EscrowMicroUSD
	m.buyerEscrow[order.BuyerID] = nextEscrow
	m.orders[order.OrderID] = order
	m.orderProvBids[order.OrderID] = make(map[string]int)
	return nil
}

// SubmitBid verifies and accepts a provider's reverse-auction SignedBid against
// an open Order, enforcing signature validity, authorized key binding, expiry,
// replay protection, anti-Sybil bid limits, real capacity bounds, and provider
// collateral bonding.
func (m *Market) SubmitBid(sb SignedBid, nowUnix int64) error {
	b := sb.Bid
	if b.BidID == "" || b.OrderID == "" || b.OfferID == "" || b.ProviderID == "" || b.OfferedTokens <= 0 || b.BidMicroUSDPer1MTokens <= 0 || b.BondMicroUSD < 0 {
		return ErrInvalidBid
	}
	if b.ExpiresAtUnix <= nowUnix {
		return ErrExpiredBid
	}

	if m.cfg.VerifySignatures {
		m.sigVerifications.Add(1)
		if err := VerifySignedBid(sb); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	order, ok := m.orders[b.OrderID]
	if !ok {
		return fmt.Errorf("%w: order %s", ErrNotFound, b.OrderID)
	}
	if order.ExpiresAtUnix <= nowUnix {
		return ErrExpiredOrder
	}
	if m.orderCleared[b.OrderID] {
		return fmt.Errorf("%w: order %s already cleared", ErrInvalidBid, b.OrderID)
	}
	if _, dup := m.bidByID[b.BidID]; dup {
		return fmt.Errorf("%w: duplicate bid %s", ErrInvalidBid, b.BidID)
	}

	offer, ok := m.offers[b.OfferID]
	if !ok {
		return fmt.Errorf("%w: offer %s", ErrNotFound, b.OfferID)
	}
	if offer.Offer.ExpiresAtUnix <= nowUnix {
		return ErrExpiredOffer
	}
	if offer.Offer.ProviderID != b.ProviderID {
		return fmt.Errorf("%w: bid provider does not match offer provider", ErrInvalidBid)
	}
	if m.cfg.VerifySignatures {
		if err := m.verifyProviderKeyLocked(b.ProviderID, b.ProviderPubKeyHex); err != nil {
			return err
		}
		if !strings.EqualFold(offer.Offer.ProviderPubKeyHex, b.ProviderPubKeyHex) {
			return fmt.Errorf("%w: bid pubkey does not match offer pubkey", ErrInvalidSignature)
		}
	}

	// Anti-replay nonce check
	if m.cfg.VerifySignatures {
		if seen, exists := m.usedBidNonces[b.ProviderPubKeyHex]; exists {
			if _, dup := seen[b.Nonce]; dup {
				return ErrReplayNonce
			}
		} else {
			m.usedBidNonces[b.ProviderPubKeyHex] = make(map[uint64]struct{})
		}
	}

	// Anti-Sybil per-order bid limit check
	if m.cfg.VerifySignatures {
		if m.orderProvBids[b.OrderID][b.ProviderID] >= m.cfg.MaxPendingBidsPerProviderPerOrder {
			return ErrSybilBidLimit
		}
	}

	// Capacity over-claim check against real unallocated provider slots & VRAM
	freeSlots := offer.Offer.AvailableSlots - m.activeSlotsUsed[b.ProviderID]
	if m.cfg.VerifySignatures {
		if freeSlots <= 0 || b.CommittedVRAMGB > offer.Offer.Hardware.AvailableVRAMGB {
			return ErrCapacityOverclaim
		}
	}

	// Collateral bond lock (with checked arithmetic to prevent overflow)
	if m.cfg.EnforceSlashing && m.cfg.MinBondBPS > 0 {
		allocCap := b.OfferedTokens
		if allocCap > order.RequestedTokens {
			allocCap = order.RequestedTokens
		}
		bidVal, err := ComputeTokenCostMicroUSDChecked(allocCap, b.BidMicroUSDPer1MTokens)
		if err != nil {
			return err
		}
		minBond, err := mulDivCeilChecked(bidVal, int64(m.cfg.MinBondBPS), 10000)
		if err != nil {
			return err
		}
		if minBond < 1 {
			minBond = 1
		}
		if b.BondMicroUSD < minBond {
			return fmt.Errorf("%w: bond %d < required %d", ErrInsufficientBond, b.BondMicroUSD, minBond)
		}
	}
	if b.BondMicroUSD > 0 {
		if m.providerBalances[b.ProviderID] < b.BondMicroUSD {
			return fmt.Errorf("%w: provider %s balance %d < bond %d", ErrInsufficientBalance, b.ProviderID, m.providerBalances[b.ProviderID], b.BondMicroUSD)
		}
		nextLocked, err := addInt64Checked(m.providerLockedBonds[b.ProviderID], b.BondMicroUSD)
		if err != nil {
			return err
		}
		m.providerBalances[b.ProviderID] -= b.BondMicroUSD
		m.providerLockedBonds[b.ProviderID] = nextLocked
	}

	if m.cfg.VerifySignatures {
		m.usedBidNonces[b.ProviderPubKeyHex][b.Nonce] = struct{}{}
	}
	m.orderProvBids[b.OrderID][b.ProviderID]++
	m.bidsByOrder[b.OrderID] = append(m.bidsByOrder[b.OrderID], sb)
	m.bidByID[b.BidID] = sb
	return nil
}

// ClearOrder runs the neutral Enso auction selector across all submitted bids
// for orderID, awards one or more Leases (supporting partial fills across
// multiple providers when enabled), unlocks bonds for non-winning bids, and
// refunds unused buyer escrow.
func (m *Market) ClearOrder(orderID string, nowUnix int64) ([]Lease, []Rejection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	order, ok := m.orders[orderID]
	if !ok {
		return nil, nil, fmt.Errorf("%w: order %s", ErrNotFound, orderID)
	}
	if m.orderCleared[orderID] {
		return nil, nil, fmt.Errorf("%w: order %s already cleared", ErrInvalidOrder, orderID)
	}
	if order.ExpiresAtUnix <= nowUnix {
		m.expireOrderLocked(orderID, order)
		return nil, nil, fmt.Errorf("%w: order %s expired at %d (now %d)", ErrExpiredOrder, orderID, order.ExpiresAtUnix, nowUnix)
	}

	bids := m.bidsByOrder[orderID]
	candidates := make([]Candidate, 0, len(bids))
	for _, sb := range bids {
		offer, exists := m.offers[sb.Bid.OfferID]
		if !exists {
			continue
		}
		if m.cfg.VerifySignatures {
			if err := m.verifyProviderKeyLocked(sb.Bid.ProviderID, sb.Bid.ProviderPubKeyHex); err != nil {
				continue
			}
		}
		freeSlots := offer.Offer.AvailableSlots - m.activeSlotsUsed[sb.Bid.ProviderID]
		candidates = append(candidates, Candidate{
			SignedBid:                   sb,
			SignedOffer:                 offer,
			Telemetry:                   m.telemetry[sb.Bid.ProviderID],
			FreeSlots:                   freeSlots,
			HardwareAttestationVerified: m.verifiedAttestations[offer.Offer.OfferID],
		})
	}

	if m.cfg.VerifySignatures {
		// Re-verification at clearing time catches any bid/offer mutated in flight.
		m.sigVerifications.Add(int64(len(candidates) * 2))
	}
	ranked, rejections := RankCandidates(order, candidates, nowUnix, m.cfg.VerifySignatures)

	winningBidIDs := make(map[string]bool)
	awarded := make([]Lease, 0)
	remainingTokens := order.RequestedTokens
	remainingEscrow := order.EscrowMicroUSD
	usedProviders := make(map[string]bool)

	for _, rc := range ranked {
		if remainingTokens <= 0 {
			break
		}
		bid := rc.Candidate.SignedBid.Bid
		offer := rc.Candidate.SignedOffer.Offer

		// Avoid double-allocating the same provider twice on one order
		if usedProviders[bid.ProviderID] {
			continue
		}
		if offer.AvailableSlots-m.activeSlotsUsed[bid.ProviderID] <= 0 {
			continue
		}

		alloc := bid.OfferedTokens
		if alloc > remainingTokens {
			alloc = remainingTokens
		}
		if !order.AllowPartialFill && alloc < order.RequestedTokens {
			continue
		}

		leaseCost, err := ComputeTokenCostMicroUSDChecked(alloc, bid.BidMicroUSDPer1MTokens)
		if err != nil || leaseCost > remainingEscrow {
			continue
		}

		m.leaseSeq++
		leaseID := fmt.Sprintf("lease-%06d", m.leaseSeq)
		slashBPS := offer.SLO.SlashBPSOnBreach
		if slashBPS <= 0 {
			slashBPS = 5000
		}
		targetTTFT := effectiveLeaseTargetTTFT(offer.SLO.TargetTTFTMs, order.MaxTTFTMs)

		lease := &Lease{
			LeaseID:                   leaseID,
			OrderID:                   order.OrderID,
			BidID:                     bid.BidID,
			OfferID:                   bid.OfferID,
			BuyerID:                   order.BuyerID,
			ProviderID:                bid.ProviderID,
			Status:                    LeaseActive,
			AllocatedTokens:           alloc,
			AgreedMicroUSDPer1MTokens: bid.BidMicroUSDPer1MTokens,
			EscrowLockedMicroUSD:      leaseCost,
			ProviderBondMicroUSD:      bid.BondMicroUSD,
			SlashBPSOnBreach:          slashBPS,
			TargetTTFTMs:              targetTTFT,
			MinThroughputTokPerSec:    offer.SLO.MinThroughputTokPerSec,
			StartedAtUnix:             nowUnix,
		}
		m.leases[leaseID] = lease
		m.leaseOrder = append(m.leaseOrder, leaseID)
		m.activeSlotsUsed[bid.ProviderID]++
		usedProviders[bid.ProviderID] = true
		winningBidIDs[bid.BidID] = true

		remainingTokens -= alloc
		remainingEscrow -= leaseCost
		awarded = append(awarded, *lease)

		if !order.AllowPartialFill {
			break
		}
	}

	// Unlock bonds for every non-winning bid so zero provider funds remain trapped.
	for _, sb := range bids {
		if !winningBidIDs[sb.Bid.BidID] && sb.Bid.BondMicroUSD > 0 {
			m.providerLockedBonds[sb.Bid.ProviderID] -= sb.Bid.BondMicroUSD
			m.providerBalances[sb.Bid.ProviderID] += sb.Bid.BondMicroUSD
		}
	}

	// Refund unused buyer escrow immediately (price improvement, partial fill, or unfilled).
	if remainingEscrow > 0 {
		m.buyerEscrow[order.BuyerID] -= remainingEscrow
		m.buyerBalances[order.BuyerID] += remainingEscrow
	}

	m.orderCleared[orderID] = true

	if len(awarded) == 0 {
		return nil, rejections, ErrNoEligibleBids
	}
	return awarded, rejections, nil
}

func effectiveLeaseTargetTTFT(offerTargetTTFTMs, orderMaxTTFTMs int64) int64 {
	target := offerTargetTTFTMs
	if orderMaxTTFTMs > 0 && (target <= 0 || orderMaxTTFTMs < target) {
		target = orderMaxTTFTMs
	}
	return target
}

func computeSlashMicroUSD(bondMicroUSD int64, slashBPS int) int64 {
	if bondMicroUSD <= 0 {
		return 0
	}
	if slashBPS <= 0 {
		slashBPS = 5000
	}
	if slashBPS > 10000 {
		slashBPS = 10000
	}
	slashed, err := mulDivFloorChecked(bondMicroUSD, int64(slashBPS), 10000)
	if err != nil || slashed > bondMicroUSD {
		return bondMicroUSD
	}
	return slashed
}

// SettleLease settles an active Lease according to its DeliveryReport, enforces
// agreed pricing, promised latency/throughput SLA commitments, and collateral
// slashing, updates measured Enso telemetry, and emits a deterministic
// SettlementReceipt with exact zero microdollar drift.
func (m *Market) SettleLease(leaseID string, report DeliveryReport, nowUnix int64) (SettlementReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	lease, ok := m.leases[leaseID]
	if !ok {
		return SettlementReceipt{}, fmt.Errorf("%w: lease %s", ErrNotFound, leaseID)
	}
	if lease.Status != LeaseActive {
		return SettlementReceipt{}, fmt.Errorf("%w: lease %s status=%s", ErrLeaseAlreadySettled, leaseID, lease.Status)
	}

	if m.activeSlotsUsed[lease.ProviderID] > 0 {
		m.activeSlotsUsed[lease.ProviderID]--
	}

	delivered := report.DeliveredTokens
	if delivered < 0 {
		delivered = 0
	}
	if delivered > lease.AllocatedTokens {
		delivered = lease.AllocatedTokens
	}

	baitAndSwitch := report.BaitAndSwitchPriceAttempted ||
		(report.ReportedMicroUSDPer1MTokens > 0 && report.ReportedMicroUSDPer1MTokens > lease.AgreedMicroUSDPer1MTokens)

	var (
		providerPayout int64
		buyerRefund    int64
		slashedBond    int64
		bondReturned   int64
		status         LeaseStatus
		verClass       VerificationClass
	)

	cappedTokenPayout := func(toks, rate int64) int64 {
		cost, err := ComputeTokenCostMicroUSDChecked(toks, rate)
		if err != nil || cost > lease.EscrowLockedMicroUSD {
			return lease.EscrowLockedMicroUSD
		}
		return cost
	}

	if m.cfg.EnforceSlashing {
		verClass = VerificationEconomicStake
		if baitAndSwitch {
			// Fraudulent post-award price inflation / bait-and-switch: zero payout,
			// full escrow refund to buyer, and 100% bond slash paid to buyer.
			status = LeaseSlashedBaitSwitch
			delivered = 0
			providerPayout = 0
			buyerRefund = lease.EscrowLockedMicroUSD
			slashedBond = lease.ProviderBondMicroUSD
			bondReturned = 0
		} else if !report.Completed || delivered < lease.AllocatedTokens {
			// Mid-lease crash or partial non-delivery: pay only for delivered useful
			// tokens, refund remaining escrow, slash SLA breach bond fraction.
			if delivered > 0 {
				status = LeasePartialCompleted
			} else {
				status = LeaseSlashedNonDelivery
			}
			providerPayout = cappedTokenPayout(delivered, lease.AgreedMicroUSDPer1MTokens)
			buyerRefund = lease.EscrowLockedMicroUSD - providerPayout
			slashedBond = computeSlashMicroUSD(lease.ProviderBondMicroUSD, lease.SlashBPSOnBreach)
			bondReturned = lease.ProviderBondMicroUSD - slashedBond
		} else {
			// Evaluate snapshotted latency & throughput SLA commitments even when
			// all allocated tokens were delivered with Completed=true.
			ttftBreach := lease.TargetTTFTMs > 0 &&
				(report.ObservedTTFTMs <= 0 || report.ObservedTTFTMs > float64(lease.TargetTTFTMs))
			throughputBreach := lease.MinThroughputTokPerSec > 0 &&
				report.ObservedTokPerSec < lease.MinThroughputTokPerSec

			providerPayout = cappedTokenPayout(delivered, lease.AgreedMicroUSDPer1MTokens)
			buyerRefund = lease.EscrowLockedMicroUSD - providerPayout
			if ttftBreach || throughputBreach {
				status = LeaseSlashedSLABreach
				slashedBond = computeSlashMicroUSD(lease.ProviderBondMicroUSD, lease.SlashBPSOnBreach)
				bondReturned = lease.ProviderBondMicroUSD - slashedBond
			} else {
				status = LeaseCompleted
				slashedBond = 0
				bondReturned = lease.ProviderBondMicroUSD
			}
		}
	} else {
		// Negative-control unverified mode (slashing & price-enforcement disabled):
		// dishonest providers can bill inflated prices up to locked escrow and
		// suffer zero bond slashing on non-delivery.
		verClass = VerificationUnverified
		billedRate := lease.AgreedMicroUSDPer1MTokens
		if report.ReportedMicroUSDPer1MTokens > billedRate {
			billedRate = report.ReportedMicroUSDPer1MTokens
		}
		if baitAndSwitch {
			status = LeasePartialCompleted
			providerPayout = lease.EscrowLockedMicroUSD
			buyerRefund = 0
		} else if !report.Completed {
			status = LeaseSlashedNonDelivery
			// Even on crash, unverified settlement fails to claw back partial escrow
			providerPayout = lease.EscrowLockedMicroUSD / 2
			buyerRefund = lease.EscrowLockedMicroUSD - providerPayout
		} else {
			status = LeaseCompleted
			providerPayout = cappedTokenPayout(delivered, billedRate)
			buyerRefund = lease.EscrowLockedMicroUSD - providerPayout
		}
		slashedBond = 0
		bondReturned = lease.ProviderBondMicroUSD
	}

	// Execute exact conserving ledger transfers:
	//   BuyerEscrow:        -EscrowLocked
	//   BuyerBalance:       +BuyerRefund + SlashedBond
	//   ProviderLockedBond: -ProviderBond
	//   ProviderBalance:    +ProviderPayout + BondReturned
	// Net delta = -Escrow + (Escrow - Payout) + Slashed - Bond + Payout + (Bond - Slashed) == 0.
	m.buyerEscrow[lease.BuyerID] -= lease.EscrowLockedMicroUSD
	m.buyerBalances[lease.BuyerID] += buyerRefund + slashedBond
	m.providerLockedBonds[lease.ProviderID] -= lease.ProviderBondMicroUSD
	m.providerBalances[lease.ProviderID] += providerPayout + bondReturned

	lease.Status = status
	lease.DeliveredTokens = delivered
	lease.SettledAtUnix = nowUnix

	// Update measured Enso telemetry
	m.updateTelemetryLocked(lease.ProviderID, report, status)

	m.receiptSeq++
	overhead := report.VerificationOverheadMicroUSD
	if overhead == 0 {
		overhead = m.cfg.SettlementCostPerReceiptMicroUSD + 2*m.cfg.VerificationCostPerSigMicroUSD
	}
	rec := SettlementReceipt{
		ReceiptID:                    fmt.Sprintf("rcpt-%06d", m.receiptSeq),
		LeaseID:                      lease.LeaseID,
		OrderID:                      lease.OrderID,
		BidID:                        lease.BidID,
		BuyerID:                      lease.BuyerID,
		ProviderID:                   lease.ProviderID,
		Status:                       status,
		AllocatedTokens:              lease.AllocatedTokens,
		DeliveredTokens:              delivered,
		AgreedMicroUSDPer1MTokens:    lease.AgreedMicroUSDPer1MTokens,
		BuyerEscrowLockedMicroUSD:    lease.EscrowLockedMicroUSD,
		ProviderPayoutMicroUSD:       providerPayout,
		BuyerRefundMicroUSD:          buyerRefund,
		SlashedBondMicroUSD:          slashedBond,
		BondReturnedMicroUSD:         bondReturned,
		VerificationOverheadMicroUSD: overhead,
		VerificationClass:            verClass,
		SettledAtUnix:                nowUnix,
	}
	digest := ReceiptCommitment(rec)
	rec.ReceiptHashHex = hex.EncodeToString(digest[:])
	m.receipts[rec.ReceiptID] = rec
	m.receiptOrder = append(m.receiptOrder, rec.ReceiptID)

	return rec, nil
}

// FailoverLease re-allocates undelivered tokens from a settled, unrecovered
// failed or slashed lease onto the highest-ranked healthy alternative provider
// using Enso selection. Active, completed, expired, and already-recovered
// leases are rejected fail-closed.
func (m *Market) FailoverLease(failedLeaseID string, nowUnix int64) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	failed, ok := m.leases[failedLeaseID]
	if !ok {
		return nil, fmt.Errorf("%w: lease %s", ErrNotFound, failedLeaseID)
	}
	if failed.FailoverToLeaseID != "" || failed.Status == LeaseFailedOver {
		return nil, fmt.Errorf("%w: lease %s already failed over to %s", ErrLeaseNotEligibleForFailover, failedLeaseID, failed.FailoverToLeaseID)
	}
	switch failed.Status {
	case LeasePartialCompleted, LeaseSlashedNonDelivery, LeaseSlashedBaitSwitch, LeaseSlashedSLABreach:
		// Settled failure/partial state eligible for failover recovery.
	default:
		return nil, fmt.Errorf("%w: lease %s status=%s", ErrLeaseNotEligibleForFailover, failedLeaseID, failed.Status)
	}

	remainingTokens := failed.AllocatedTokens - failed.DeliveredTokens
	if remainingTokens <= 0 {
		return nil, fmt.Errorf("%w: lease %s has no undelivered tokens", ErrLeaseNotEligibleForFailover, failedLeaseID)
	}
	origOrder, ok := m.orders[failed.OrderID]
	if !ok {
		return nil, fmt.Errorf("%w: order %s", ErrNotFound, failed.OrderID)
	}

	// Exclude the failed provider and any ancestor failed providers in this chain.
	excludedProviders := map[string]bool{failed.ProviderID: true}
	seenLeases := map[string]bool{failed.LeaseID: true}
	for prevID := failed.FailoverFromLeaseID; prevID != "" && !seenLeases[prevID]; {
		seenLeases[prevID] = true
		prev, exists := m.leases[prevID]
		if !exists {
			break
		}
		excludedProviders[prev.ProviderID] = true
		prevID = prev.FailoverFromLeaseID
	}

	failoverOrder := origOrder
	failoverOrder.RequestedTokens = remainingTokens
	failoverOrder.AllowPartialFill = true

	// Build spot failover candidates from active signed offers of OTHER providers.
	candidates := make([]Candidate, 0, len(m.offers))
	for _, so := range m.offers {
		o := so.Offer
		if excludedProviders[o.ProviderID] {
			continue
		}
		if o.ExpiresAtUnix <= nowUnix {
			continue
		}
		freeSlots := o.AvailableSlots - m.activeSlotsUsed[o.ProviderID]
		if freeSlots <= 0 {
			continue
		}
		if m.cfg.VerifySignatures {
			if err := m.verifyProviderKeyLocked(o.ProviderID, o.ProviderPubKeyHex); err != nil {
				continue
			}
		}
		// Spot failover uses the provider's already-signed list offer price.
		synthBid := SignedBid{
			Bid: Bid{
				ProtocolVersion:        ProtocolVersion,
				BidID:                  "failover-bid-" + o.OfferID,
				OrderID:                origOrder.OrderID,
				OfferID:                o.OfferID,
				ProviderID:             o.ProviderID,
				ProviderPubKeyHex:      o.ProviderPubKeyHex,
				BidMicroUSDPer1MTokens: o.Pricing.MicroUSDPer1MTokens,
				OfferedTokens:          remainingTokens,
				CommittedVRAMGB:        o.Hardware.AvailableVRAMGB,
				IssuedAtUnix:           o.IssuedAtUnix,
				ExpiresAtUnix:          o.ExpiresAtUnix,
				Nonce:                  o.Nonce,
			},
			VerificationClass: VerificationCryptoSignature,
		}
		candidates = append(candidates, Candidate{
			SignedBid:                   synthBid,
			SignedOffer:                 so,
			Telemetry:                   m.telemetry[o.ProviderID],
			FreeSlots:                   freeSlots,
			HardwareAttestationVerified: m.verifiedAttestations[o.OfferID],
		})
	}

	// Verify offer signatures before failover ranking when VerifySignatures is active.
	verifiedCandidates := candidates
	if m.cfg.VerifySignatures {
		verifiedCandidates = make([]Candidate, 0, len(candidates))
		for _, c := range candidates {
			m.sigVerifications.Add(1)
			if err := VerifySignedOffer(c.SignedOffer); err == nil {
				verifiedCandidates = append(verifiedCandidates, c)
			}
		}
	}

	ranked, _ := RankCandidates(failoverOrder, verifiedCandidates, nowUnix, false)
	for _, rc := range ranked {
		offer := rc.Candidate.SignedOffer.Offer
		cost, err := ComputeTokenCostMicroUSDChecked(remainingTokens, offer.Pricing.MicroUSDPer1MTokens)
		if err != nil {
			continue
		}
		if m.buyerBalances[origOrder.BuyerID] < cost {
			continue
		}
		nextEscrow, err := addInt64Checked(m.buyerEscrow[origOrder.BuyerID], cost)
		if err != nil {
			continue
		}

		bond := int64(0)
		var nextLocked int64
		if m.cfg.EnforceSlashing && m.cfg.MinBondBPS > 0 {
			bond, err = mulDivCeilChecked(cost, int64(m.cfg.MinBondBPS), 10000)
			if err != nil {
				continue
			}
			if bond < 1 {
				bond = 1
			}
			if m.providerBalances[offer.ProviderID] < bond {
				continue
			}
			nextLocked, err = addInt64Checked(m.providerLockedBonds[offer.ProviderID], bond)
			if err != nil {
				continue
			}
		}

		// Lock buyer escrow & provider bond conserving total balance.
		m.buyerBalances[origOrder.BuyerID] -= cost
		m.buyerEscrow[origOrder.BuyerID] = nextEscrow
		if bond > 0 {
			m.providerBalances[offer.ProviderID] -= bond
			m.providerLockedBonds[offer.ProviderID] = nextLocked
		}

		m.leaseSeq++
		newLeaseID := fmt.Sprintf("lease-%06d", m.leaseSeq)
		slashBPS := offer.SLO.SlashBPSOnBreach
		if slashBPS <= 0 {
			slashBPS = 5000
		}
		targetTTFT := effectiveLeaseTargetTTFT(offer.SLO.TargetTTFTMs, origOrder.MaxTTFTMs)
		newLease := &Lease{
			LeaseID:                   newLeaseID,
			OrderID:                   origOrder.OrderID,
			BidID:                     rc.Candidate.SignedBid.Bid.BidID,
			OfferID:                   offer.OfferID,
			BuyerID:                   origOrder.BuyerID,
			ProviderID:                offer.ProviderID,
			Status:                    LeaseActive,
			AllocatedTokens:           remainingTokens,
			AgreedMicroUSDPer1MTokens: offer.Pricing.MicroUSDPer1MTokens,
			EscrowLockedMicroUSD:      cost,
			ProviderBondMicroUSD:      bond,
			SlashBPSOnBreach:          slashBPS,
			TargetTTFTMs:              targetTTFT,
			MinThroughputTokPerSec:    offer.SLO.MinThroughputTokPerSec,
			StartedAtUnix:             nowUnix,
			FailoverFromLeaseID:       failedLeaseID,
		}
		m.leases[newLeaseID] = newLease
		m.leaseOrder = append(m.leaseOrder, newLeaseID)
		m.activeSlotsUsed[offer.ProviderID]++

		failed.Status = LeaseFailedOver
		failed.FailoverToLeaseID = newLeaseID
		cp := *newLease
		return &cp, nil
	}

	return nil, ErrNoEligibleBids
}

// ExpireStale purges expired offers and unlocks bonds on expired uncleared orders.
func (m *Market) ExpireStale(nowUnix int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	expired := 0
	for id, so := range m.offers {
		if so.Offer.ExpiresAtUnix <= nowUnix {
			delete(m.offers, id)
			delete(m.verifiedAttestations, id)
			expired++
		}
	}
	for orderID, ord := range m.orders {
		if m.orderCleared[orderID] || ord.ExpiresAtUnix > nowUnix {
			continue
		}
		m.expireOrderLocked(orderID, ord)
		expired++
	}
	return expired
}

func (m *Market) expireOrderLocked(orderID string, ord Order) {
	for _, sb := range m.bidsByOrder[orderID] {
		if sb.Bid.BondMicroUSD > 0 {
			m.providerLockedBonds[sb.Bid.ProviderID] -= sb.Bid.BondMicroUSD
			m.providerBalances[sb.Bid.ProviderID] += sb.Bid.BondMicroUSD
		}
	}
	if ord.EscrowMicroUSD > 0 {
		m.buyerEscrow[ord.BuyerID] -= ord.EscrowMicroUSD
		m.buyerBalances[ord.BuyerID] += ord.EscrowMicroUSD
	}
	m.orderCleared[orderID] = true
}

// Order returns a copy of an order by ID.
func (m *Market) Order(orderID string) (Order, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ord, ok := m.orders[orderID]
	if !ok {
		return Order{}, false
	}
	if len(ord.AllowedRegions) > 0 {
		ord.AllowedRegions = append([]string(nil), ord.AllowedRegions...)
	}
	if len(ord.AllowedJurisdictions) > 0 {
		ord.AllowedJurisdictions = append([]string(nil), ord.AllowedJurisdictions...)
	}
	if len(ord.BlockedJurisdictions) > 0 {
		ord.BlockedJurisdictions = append([]string(nil), ord.BlockedJurisdictions...)
	}
	return ord, true
}

// Telemetry returns a copy of a provider's measured telemetry.
func (m *Market) Telemetry(providerID string) (ProviderTelemetry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.telemetry[providerID]
	return t, ok
}

// Lease returns a copy of a lease by ID.
func (m *Market) Lease(leaseID string) (Lease, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.leases[leaseID]
	if !ok {
		return Lease{}, false
	}
	return *l, true
}

// Receipts returns all settlement receipts in deterministic issuance order.
func (m *Market) Receipts() []SettlementReceipt {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SettlementReceipt, 0, len(m.receiptOrder))
	for _, id := range m.receiptOrder {
		out = append(out, m.receipts[id])
	}
	return out
}

func (m *Market) updateTelemetryLocked(providerID string, report DeliveryReport, status LeaseStatus) {
	t := m.telemetry[providerID]
	t.ProviderID = providerID
	t.ObservedSampleCount++

	const alpha = 0.30
	if report.ObservedTTFTMs > 0 {
		if t.ObservedSampleCount == 1 || t.MeasuredTTFTMs <= 0 {
			t.MeasuredTTFTMs = report.ObservedTTFTMs
		} else {
			t.MeasuredTTFTMs = alpha*report.ObservedTTFTMs + (1-alpha)*t.MeasuredTTFTMs
		}
	}
	if report.ObservedLatencyMs > 0 {
		if t.ObservedSampleCount == 1 || t.MeasuredLatencyMs <= 0 {
			t.MeasuredLatencyMs = report.ObservedLatencyMs
		} else {
			t.MeasuredLatencyMs = alpha*report.ObservedLatencyMs + (1-alpha)*t.MeasuredLatencyMs
		}
	}
	if report.ObservedTokPerSec > 0 {
		if t.ObservedSampleCount == 1 || t.MeasuredTokPerSec <= 0 {
			t.MeasuredTokPerSec = report.ObservedTokPerSec
		} else {
			t.MeasuredTokPerSec = alpha*report.ObservedTokPerSec + (1-alpha)*t.MeasuredTokPerSec
		}
	}

	switch status {
	case LeaseCompleted:
		t.CompletedJobs++
	case LeaseSlashedBaitSwitch, LeaseSlashedNonDelivery, LeaseSlashedSLABreach:
		t.FailedJobs++
		t.SlashedJobs++
	default:
		t.FailedJobs++
	}

	// Laplace-smoothed reliability with 4x slash penalty so bait-and-switch,
	// SLA-breaching, or chronically crashing providers are rapidly demoted by Enso.
	const priorSuccess = 9.0
	const priorTotal = 10.0
	num := priorSuccess + float64(t.CompletedJobs)
	den := priorTotal + float64(t.CompletedJobs+t.FailedJobs) + 4.0*float64(t.SlashedJobs)
	rel := int((num / den) * 10000.0)
	if rel < 0 {
		rel = 0
	} else if rel > 10000 {
		rel = 10000
	}
	t.ReliabilityBPS = rel
	m.telemetry[providerID] = t
}

func copyInt64Map(src map[string]int64) map[string]int64 {
	dst := make(map[string]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
