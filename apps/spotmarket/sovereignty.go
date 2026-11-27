// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/luxfi/crypto/secp256k1"
)

// GossipSnapshot builds a deterministic, permissionless snapshot of active
// peers, signed offers, and measured provider telemetry with a SHA-256 state root.
func (m *Market) GossipSnapshot(nowUnix int64) GossipSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gossipSnapshotLocked(nowUnix)
}

func (m *Market) gossipSnapshotLocked(nowUnix int64) GossipSnapshot {
	peers := make([]PeerNode, 0, len(m.peers))
	for _, p := range m.peers {
		cp := p
		cp.Regions = append([]string(nil), p.Regions...)
		sort.Strings(cp.Regions)
		peers = append(peers, cp)
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].PeerID < peers[j].PeerID
	})

	offers := make([]SignedOffer, 0, len(m.offers))
	for _, so := range m.offers {
		if so.Offer.ExpiresAtUnix > nowUnix {
			cp := so
			cp.Offer.SupportedModels = append([]string(nil), so.Offer.SupportedModels...)
			sort.Strings(cp.Offer.SupportedModels)
			offers = append(offers, cp)
		}
	}
	sort.Slice(offers, func(i, j int) bool {
		return offers[i].Offer.OfferID < offers[j].Offer.OfferID
	})

	telemetry := make(map[string]ProviderTelemetry, len(m.telemetry))
	for k, v := range m.telemetry {
		telemetry[k] = v
	}

	snap := GossipSnapshot{
		ProtocolVersion: ProtocolVersion,
		GeneratedAtUnix: nowUnix,
		Peers:           peers,
		ActiveOffers:    offers,
		Telemetry:       telemetry,
	}
	root := gossipStateRoot(snap)
	snap.StateRootHex = hex.EncodeToString(root[:])
	return snap
}

func gossipStateRoot(s GossipSnapshot) [32]byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, "HIP1350:GOSSIP:V1")
	writeLenPrefixed(&buf, s.ProtocolVersion)
	writeInt64(&buf, s.GeneratedAtUnix)

	writeUint64(&buf, uint64(len(s.Peers)))
	for _, p := range s.Peers {
		writeLenPrefixed(&buf, p.PeerID)
		writeLenPrefixed(&buf, p.Endpoint)
		writeLenPrefixed(&buf, p.PubKeyHex)
		regions := append([]string(nil), p.Regions...)
		sort.Strings(regions)
		writeStringSlice(&buf, regions)
		writeInt64(&buf, p.LastSeenUnix)
		writeLenPrefixed(&buf, p.OperatorClass)
	}

	writeUint64(&buf, uint64(len(s.ActiveOffers)))
	for _, so := range s.ActiveOffers {
		writeLenPrefixed(&buf, so.Offer.OfferID)
		writeLenPrefixed(&buf, so.CommitmentHex)
		writeLenPrefixed(&buf, so.SignatureHex)
		writeLenPrefixed(&buf, string(so.VerificationClass))
	}

	telKeys := make([]string, 0, len(s.Telemetry))
	for k := range s.Telemetry {
		telKeys = append(telKeys, k)
	}
	sort.Strings(telKeys)
	writeUint64(&buf, uint64(len(telKeys)))
	for _, k := range telKeys {
		t := s.Telemetry[k]
		writeLenPrefixed(&buf, k)
		writeLenPrefixed(&buf, t.ProviderID)
		writeUint64(&buf, math.Float64bits(t.MeasuredTTFTMs))
		writeUint64(&buf, math.Float64bits(t.MeasuredLatencyMs))
		writeUint64(&buf, math.Float64bits(t.MeasuredTokPerSec))
		writeInt64(&buf, t.CompletedJobs)
		writeInt64(&buf, t.FailedJobs)
		writeInt64(&buf, t.SlashedJobs)
		writeInt64(&buf, int64(t.ReliabilityBPS))
		writeInt64(&buf, t.ObservedSampleCount)
	}

	return sha256.Sum256(buf.Bytes())
}

// BundleCommitment computes the deterministic SHA-256 digest of a
// MarketExportBundle covering gossip state, exact ledger balances, provider key
// bindings, verified hardware attestations, orders, cleared status, pending
// bids, complete lease terms, and settlement receipts.
func BundleCommitment(b MarketExportBundle) [32]byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, "HIP1350:EXPORT_BUNDLE:V2")
	writeLenPrefixed(&buf, b.ProtocolVersion)
	writeInt64(&buf, b.ExportedAtUnix)
	writeLenPrefixed(&buf, b.ExporterPubKeyHex)
	writeLenPrefixed(&buf, b.Gossip.StateRootHex)

	// Ledger canonical serialization
	writeInt64(&buf, b.Ledger.TotalDepositedMicroUSD)
	writeInt64(&buf, b.Ledger.ConservationDrift)
	writeSortedInt64Map(&buf, b.Ledger.BuyerBalances)
	writeSortedInt64Map(&buf, b.Ledger.BuyerEscrow)
	writeSortedInt64Map(&buf, b.Ledger.ProviderBalances)
	writeSortedInt64Map(&buf, b.Ledger.ProviderLockedBonds)

	// Provider identity-to-key bindings
	writeSortedStringMap(&buf, b.ProviderKeys)

	// Verified hardware attestation offer IDs
	verifiedIDs := append([]string(nil), b.VerifiedAttestationOfferIDs...)
	sort.Strings(verifiedIDs)
	writeStringSlice(&buf, verifiedIDs)

	// Orders (all fields)
	writeUint64(&buf, uint64(len(b.Orders)))
	for _, o := range b.Orders {
		writeLenPrefixed(&buf, o.OrderID)
		writeLenPrefixed(&buf, o.BuyerID)
		writeLenPrefixed(&buf, o.Model)
		writeInt64(&buf, o.RequestedTokens)
		writeInt64(&buf, int64(o.MinVRAMGB))
		writeInt64(&buf, o.MaxMicroUSDPer1MTokens)
		writeStringSlice(&buf, o.AllowedRegions)
		writeStringSlice(&buf, o.AllowedJurisdictions)
		writeStringSlice(&buf, o.BlockedJurisdictions)
		writeInt64(&buf, o.MaxTTFTMs)
		writeInt64(&buf, int64(o.MinReliabilityBPS))
		writeBool(&buf, o.AllowPartialFill)
		writeInt64(&buf, o.EscrowMicroUSD)
		writeInt64(&buf, o.CreatedAtUnix)
		writeInt64(&buf, o.ExpiresAtUnix)
		writeUint64(&buf, math.Float64bits(o.RankingWeights.PriceWeight))
		writeUint64(&buf, math.Float64bits(o.RankingWeights.LatencyWeight))
		writeUint64(&buf, math.Float64bits(o.RankingWeights.ReliabilityWeight))
		writeBool(&buf, o.RequireHardwareAttested)
	}

	// Cleared order IDs
	clearedIDs := append([]string(nil), b.ClearedOrderIDs...)
	sort.Strings(clearedIDs)
	writeStringSlice(&buf, clearedIDs)

	// Pending bids (complete Bid + signature envelope)
	writeUint64(&buf, uint64(len(b.PendingBids)))
	for _, sb := range b.PendingBids {
		bid := sb.Bid
		writeLenPrefixed(&buf, bid.ProtocolVersion)
		writeLenPrefixed(&buf, bid.BidID)
		writeLenPrefixed(&buf, bid.OrderID)
		writeLenPrefixed(&buf, bid.OfferID)
		writeLenPrefixed(&buf, bid.ProviderID)
		writeLenPrefixed(&buf, bid.ProviderPubKeyHex)
		writeInt64(&buf, bid.BidMicroUSDPer1MTokens)
		writeInt64(&buf, bid.OfferedTokens)
		writeInt64(&buf, int64(bid.CommittedVRAMGB))
		writeInt64(&buf, bid.BondMicroUSD)
		writeInt64(&buf, bid.IssuedAtUnix)
		writeInt64(&buf, bid.ExpiresAtUnix)
		writeUint64(&buf, bid.Nonce)
		writeLenPrefixed(&buf, sb.CommitmentHex)
		writeLenPrefixed(&buf, sb.SignatureHex)
		writeLenPrefixed(&buf, string(sb.VerificationClass))
	}

	// Leases (EVERY field consumed after import)
	writeUint64(&buf, uint64(len(b.Leases)))
	for _, l := range b.Leases {
		writeLenPrefixed(&buf, l.LeaseID)
		writeLenPrefixed(&buf, l.OrderID)
		writeLenPrefixed(&buf, l.BidID)
		writeLenPrefixed(&buf, l.OfferID)
		writeLenPrefixed(&buf, l.BuyerID)
		writeLenPrefixed(&buf, l.ProviderID)
		writeLenPrefixed(&buf, string(l.Status))
		writeInt64(&buf, l.AllocatedTokens)
		writeInt64(&buf, l.DeliveredTokens)
		writeInt64(&buf, l.AgreedMicroUSDPer1MTokens)
		writeInt64(&buf, l.EscrowLockedMicroUSD)
		writeInt64(&buf, l.ProviderBondMicroUSD)
		writeInt64(&buf, int64(l.SlashBPSOnBreach))
		writeInt64(&buf, l.TargetTTFTMs)
		writeUint64(&buf, math.Float64bits(l.MinThroughputTokPerSec))
		writeInt64(&buf, l.StartedAtUnix)
		writeInt64(&buf, l.SettledAtUnix)
		writeLenPrefixed(&buf, l.FailoverFromLeaseID)
		writeLenPrefixed(&buf, l.FailoverToLeaseID)
	}

	// Receipts
	writeUint64(&buf, uint64(len(b.Receipts)))
	for _, r := range b.Receipts {
		writeLenPrefixed(&buf, r.ReceiptID)
		writeLenPrefixed(&buf, r.ReceiptHashHex)
	}

	return sha256.Sum256(buf.Bytes())
}

func writeBool(buf *bytes.Buffer, v bool) {
	if v {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
}

func writeStringSlice(buf *bytes.Buffer, list []string) {
	writeUint64(buf, uint64(len(list)))
	for _, item := range list {
		writeLenPrefixed(buf, item)
	}
}

func writeSortedStringMap(buf *bytes.Buffer, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	writeUint64(buf, uint64(len(keys)))
	for _, k := range keys {
		writeLenPrefixed(buf, k)
		writeLenPrefixed(buf, m[k])
	}
}

func writeSortedInt64Map(buf *bytes.Buffer, m map[string]int64) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	writeUint64(buf, uint64(len(keys)))
	for _, k := range keys {
		writeLenPrefixed(buf, k)
		writeInt64(buf, m[k])
	}
}

// ExportBundle produces a cryptographically signed, self-contained state dump
// so any user or operator can verify, migrate, or fork the entire market
// (including pending auctions, provider key bindings, verified attestations,
// active/settled leases, and receipts) without upstream Hanzo coordination.
func (m *Market) ExportBundle(signer *secp256k1.PrivateKey, nowUnix int64) (MarketExportBundle, error) {
	if signer == nil {
		return MarketExportBundle{}, ErrInvalidSignature
	}
	m.mu.RLock()
	gossip := m.gossipSnapshotLocked(nowUnix)
	ledger := m.ledgerSnapshotLocked()

	providerKeys := make(map[string]string, len(m.providerKeys))
	for k, v := range m.providerKeys {
		providerKeys[k] = v
	}

	verifiedIDs := make([]string, 0, len(m.verifiedAttestations))
	for _, so := range gossip.ActiveOffers {
		if m.verifiedAttestations[so.Offer.OfferID] {
			verifiedIDs = append(verifiedIDs, so.Offer.OfferID)
		}
	}
	sort.Strings(verifiedIDs)

	orders := make([]Order, 0, len(m.orders))
	clearedIDs := make([]string, 0, len(m.orderCleared))
	pendingBids := make([]SignedBid, 0)
	for id, ord := range m.orders {
		cp := ord
		if len(ord.AllowedRegions) > 0 {
			cp.AllowedRegions = append([]string(nil), ord.AllowedRegions...)
		}
		if len(ord.AllowedJurisdictions) > 0 {
			cp.AllowedJurisdictions = append([]string(nil), ord.AllowedJurisdictions...)
		}
		if len(ord.BlockedJurisdictions) > 0 {
			cp.BlockedJurisdictions = append([]string(nil), ord.BlockedJurisdictions...)
		}
		orders = append(orders, cp)
		if m.orderCleared[id] {
			clearedIDs = append(clearedIDs, id)
		} else {
			pendingBids = append(pendingBids, m.bidsByOrder[id]...)
		}
	}
	sort.Slice(orders, func(i, j int) bool {
		return orders[i].OrderID < orders[j].OrderID
	})
	sort.Strings(clearedIDs)
	sort.Slice(pendingBids, func(i, j int) bool {
		if pendingBids[i].Bid.OrderID != pendingBids[j].Bid.OrderID {
			return pendingBids[i].Bid.OrderID < pendingBids[j].Bid.OrderID
		}
		return pendingBids[i].Bid.BidID < pendingBids[j].Bid.BidID
	})

	leases := make([]Lease, 0, len(m.leaseOrder))
	for _, id := range m.leaseOrder {
		if l, ok := m.leases[id]; ok {
			leases = append(leases, *l)
		}
	}
	receipts := make([]SettlementReceipt, 0, len(m.receiptOrder))
	for _, id := range m.receiptOrder {
		receipts = append(receipts, m.receipts[id])
	}
	m.mu.RUnlock()

	if ledger.ConservationDrift != 0 {
		return MarketExportBundle{}, fmt.Errorf("%w: drift=%d", ErrLedgerDrift, ledger.ConservationDrift)
	}

	bundle := MarketExportBundle{
		ProtocolVersion:             ProtocolVersion,
		ExportedAtUnix:              nowUnix,
		ExporterPubKeyHex:           hex.EncodeToString(signer.PublicKey().Bytes()),
		Gossip:                      gossip,
		Ledger:                      ledger,
		ProviderKeys:                providerKeys,
		VerifiedAttestationOfferIDs: verifiedIDs,
		Orders:                      orders,
		ClearedOrderIDs:             clearedIDs,
		PendingBids:                 pendingBids,
		Leases:                      leases,
		Receipts:                    receipts,
		VerificationClass:           VerificationCryptoSignature,
	}
	digest := BundleCommitment(bundle)
	sig, err := SignDigestLowS(signer, digest)
	if err != nil {
		return MarketExportBundle{}, err
	}
	bundle.CommitmentHex = hex.EncodeToString(digest[:])
	bundle.SignatureHex = hex.EncodeToString(sig[:])
	return bundle, nil
}

// VerifyExportBundle verifies the exporter's EIP-2 low-S secp256k1 signature,
// gossip state root, every included SignedOffer and PendingBid signature, every
// receipt hash, provider key bindings, verified attestation claims, complete
// auction/lease escrow-and-bond consistency, and exact integer ledger
// conservation (drift == 0).
func VerifyExportBundle(bundle MarketExportBundle) error {
	if bundle.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol version %q", ErrInvalidExportBundle, bundle.ProtocolVersion)
	}
	expectedRoot := gossipStateRoot(bundle.Gossip)
	if hex.EncodeToString(expectedRoot[:]) != bundle.Gossip.StateRootHex {
		return fmt.Errorf("%w: gossip state root mismatch", ErrInvalidExportBundle)
	}
	for k, t := range bundle.Gossip.Telemetry {
		if strings.TrimSpace(k) == "" || k != t.ProviderID {
			return fmt.Errorf("%w: telemetry key %q does not match providerId %q", ErrInvalidExportBundle, k, t.ProviderID)
		}
	}

	// Verify non-negative balances and exact ledger conservation independently
	// using checked int64 addition.
	var sum int64
	addBalances := func(label string, m map[string]int64) error {
		for k, v := range m {
			if v < 0 {
				return fmt.Errorf("%w: negative %s balance for %s", ErrLedgerDrift, label, k)
			}
			var err error
			sum, err = addInt64Checked(sum, v)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrLedgerDrift, err)
			}
		}
		return nil
	}
	if err := addBalances("buyerBalances", bundle.Ledger.BuyerBalances); err != nil {
		return err
	}
	if err := addBalances("buyerEscrow", bundle.Ledger.BuyerEscrow); err != nil {
		return err
	}
	if err := addBalances("providerBalances", bundle.Ledger.ProviderBalances); err != nil {
		return err
	}
	if err := addBalances("providerLockedBonds", bundle.Ledger.ProviderLockedBonds); err != nil {
		return err
	}
	if sum != bundle.Ledger.TotalDepositedMicroUSD || bundle.Ledger.ConservationDrift != 0 {
		return fmt.Errorf("%w: ledger drift detected (sum=%d deposited=%d)", ErrLedgerDrift, sum, bundle.Ledger.TotalDepositedMicroUSD)
	}

	// Verify provider key bindings
	for provID, pubHex := range bundle.ProviderKeys {
		if strings.TrimSpace(provID) == "" {
			return fmt.Errorf("%w: empty provider ID in providerKeys", ErrInvalidExportBundle)
		}
		if _, err := canonicalCompressedPubKeyHex(pubHex); err != nil {
			return fmt.Errorf("%w: invalid provider key for %s: %v", ErrInvalidExportBundle, provID, err)
		}
	}

	// Verify every active offer's signature and index offers for attestation checks
	activeOffers := make(map[string]SignedOffer, len(bundle.Gossip.ActiveOffers))
	for _, so := range bundle.Gossip.ActiveOffers {
		if err := VerifySignedOffer(so); err != nil {
			return fmt.Errorf("%w: offer %s: %v", ErrInvalidExportBundle, so.Offer.OfferID, err)
		}
		activeOffers[so.Offer.OfferID] = so
	}

	for _, id := range bundle.VerifiedAttestationOfferIDs {
		so, ok := activeOffers[id]
		if !ok {
			return fmt.Errorf("%w: verified attestation references missing offer %s", ErrInvalidExportBundle, id)
		}
		if so.Offer.AttestationClass != VerificationHardwareAttestation || strings.TrimSpace(so.Offer.AttestationQuoteHash) == "" {
			return fmt.Errorf("%w: verified attestation offer %s lacks hardware quote", ErrInvalidExportBundle, id)
		}
	}

	// Index orders and cleared status; verify pending bids and lease references
	orderMap := make(map[string]Order, len(bundle.Orders))
	for _, ord := range bundle.Orders {
		if ord.OrderID == "" || ord.BuyerID == "" || ord.EscrowMicroUSD < 0 {
			return fmt.Errorf("%w: invalid order %q", ErrInvalidExportBundle, ord.OrderID)
		}
		if _, dup := orderMap[ord.OrderID]; dup {
			return fmt.Errorf("%w: duplicate order %s", ErrInvalidExportBundle, ord.OrderID)
		}
		orderMap[ord.OrderID] = ord
	}

	clearedSet := make(map[string]bool, len(bundle.ClearedOrderIDs))
	for _, id := range bundle.ClearedOrderIDs {
		if _, ok := orderMap[id]; !ok {
			return fmt.Errorf("%w: cleared order %s missing from orders", ErrInvalidExportBundle, id)
		}
		clearedSet[id] = true
	}

	expectedBuyerEscrow := make(map[string]int64)
	expectedProvBonds := make(map[string]int64)

	for _, ord := range bundle.Orders {
		if !clearedSet[ord.OrderID] && ord.EscrowMicroUSD > 0 {
			next, err := addInt64Checked(expectedBuyerEscrow[ord.BuyerID], ord.EscrowMicroUSD)
			if err != nil {
				return fmt.Errorf("%w: escrow overflow", ErrInvalidExportBundle)
			}
			expectedBuyerEscrow[ord.BuyerID] = next
		}
	}

	seenPendingBids := make(map[string]bool, len(bundle.PendingBids))
	for _, sb := range bundle.PendingBids {
		if err := VerifySignedBid(sb); err != nil {
			return fmt.Errorf("%w: pending bid %s: %v", ErrInvalidExportBundle, sb.Bid.BidID, err)
		}
		if sb.Bid.BondMicroUSD < 0 {
			return fmt.Errorf("%w: negative bond on pending bid %s", ErrInvalidExportBundle, sb.Bid.BidID)
		}
		if seenPendingBids[sb.Bid.BidID] {
			return fmt.Errorf("%w: duplicate pending bid %s", ErrInvalidExportBundle, sb.Bid.BidID)
		}
		seenPendingBids[sb.Bid.BidID] = true
		if _, ok := orderMap[sb.Bid.OrderID]; !ok {
			return fmt.Errorf("%w: pending bid %s references missing order %s", ErrInvalidExportBundle, sb.Bid.BidID, sb.Bid.OrderID)
		}
		if clearedSet[sb.Bid.OrderID] {
			return fmt.Errorf("%w: pending bid %s references already-cleared order %s", ErrInvalidExportBundle, sb.Bid.BidID, sb.Bid.OrderID)
		}
		if sb.Bid.BondMicroUSD > 0 {
			next, err := addInt64Checked(expectedProvBonds[sb.Bid.ProviderID], sb.Bid.BondMicroUSD)
			if err != nil {
				return fmt.Errorf("%w: bond overflow", ErrInvalidExportBundle)
			}
			expectedProvBonds[sb.Bid.ProviderID] = next
		}
	}

	for _, l := range bundle.Leases {
		if l.LeaseID == "" || l.BuyerID == "" || l.ProviderID == "" || l.EscrowLockedMicroUSD < 0 || l.ProviderBondMicroUSD < 0 {
			return fmt.Errorf("%w: invalid lease %q", ErrInvalidExportBundle, l.LeaseID)
		}
		if _, ok := orderMap[l.OrderID]; !ok {
			return fmt.Errorf("%w: lease %s references missing order %s", ErrInvalidExportBundle, l.LeaseID, l.OrderID)
		}
		if l.Status == LeaseActive {
			if l.EscrowLockedMicroUSD > 0 {
				next, err := addInt64Checked(expectedBuyerEscrow[l.BuyerID], l.EscrowLockedMicroUSD)
				if err != nil {
					return fmt.Errorf("%w: lease escrow overflow", ErrInvalidExportBundle)
				}
				expectedBuyerEscrow[l.BuyerID] = next
			}
			if l.ProviderBondMicroUSD > 0 {
				next, err := addInt64Checked(expectedProvBonds[l.ProviderID], l.ProviderBondMicroUSD)
				if err != nil {
					return fmt.Errorf("%w: lease bond overflow", ErrInvalidExportBundle)
				}
				expectedProvBonds[l.ProviderID] = next
			}
		}
	}

	if err := verifyAccountingMapMatches("buyerEscrow", bundle.Ledger.BuyerEscrow, expectedBuyerEscrow); err != nil {
		return err
	}
	if err := verifyAccountingMapMatches("providerLockedBonds", bundle.Ledger.ProviderLockedBonds, expectedProvBonds); err != nil {
		return err
	}

	// Verify every settlement receipt hash
	for _, r := range bundle.Receipts {
		d := ReceiptCommitment(r)
		if hex.EncodeToString(d[:]) != r.ReceiptHashHex {
			return fmt.Errorf("%w: receipt %s hash mismatch", ErrInvalidExportBundle, r.ReceiptID)
		}
	}

	// Verify exporter commitment & secp256k1 low-S signature
	digest := BundleCommitment(bundle)
	if hex.EncodeToString(digest[:]) != bundle.CommitmentHex {
		return fmt.Errorf("%w: bundle commitment mismatch", ErrInvalidExportBundle)
	}
	pubBytes, err := hex.DecodeString(bundle.ExporterPubKeyHex)
	if err != nil {
		return fmt.Errorf("%w: invalid exporter pubkey hex", ErrInvalidExportBundle)
	}
	sigBytes, err := hex.DecodeString(bundle.SignatureHex)
	if err != nil {
		return fmt.Errorf("%w: invalid exporter signature hex", ErrInvalidExportBundle)
	}
	if err := VerifyDigestLowS(pubBytes, digest, sigBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExportBundle, err)
	}
	return nil
}

func verifyAccountingMapMatches(label string, actual, expected map[string]int64) error {
	for k, v := range actual {
		if expected[k] != v {
			return fmt.Errorf("%w: %s mismatch for %s (ledger=%d state=%d)", ErrInvalidExportBundle, label, k, v, expected[k])
		}
	}
	for k, v := range expected {
		if actual[k] != v {
			return fmt.Errorf("%w: %s mismatch for %s (ledger=%d state=%d)", ErrInvalidExportBundle, label, k, actual[k], v)
		}
	}
	return nil
}

func maxSeqFromID(id, prefix string, cur int64) int64 {
	if !strings.HasPrefix(id, prefix) {
		return cur
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(id, prefix), 10, 64)
	if err == nil && n > cur {
		return n
	}
	return cur
}

// ImportBundle verifies a signed MarketExportBundle and boots a completely
// independent forked Market instance with zero Hanzo connectivity, preserving
// pending auctions, provider key bindings, verified attestations, active
// leases, and receipts.
func ImportBundle(bundle MarketExportBundle, cfg Config, nowUnix int64) (*Market, error) {
	if err := VerifyExportBundle(bundle); err != nil {
		return nil, err
	}
	cfg.HanzoControlPlaneDisabled = true
	m := NewMarket(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()

	for provID, pubHex := range bundle.ProviderKeys {
		canon, err := canonicalCompressedPubKeyHex(pubHex)
		if err != nil {
			return nil, err
		}
		m.providerKeys[provID] = canon
	}

	for _, p := range bundle.Gossip.Peers {
		cp := p
		if len(p.Regions) > 0 {
			cp.Regions = append([]string(nil), p.Regions...)
		}
		m.peers[p.PeerID] = cp
	}
	for _, so := range bundle.Gossip.ActiveOffers {
		if so.Offer.ExpiresAtUnix > nowUnix {
			cp := so
			cp.Offer.SupportedModels = append([]string(nil), so.Offer.SupportedModels...)
			m.offers[so.Offer.OfferID] = cp
			if _, ok := m.usedOfferNonces[so.Offer.ProviderPubKeyHex]; !ok {
				m.usedOfferNonces[so.Offer.ProviderPubKeyHex] = make(map[uint64]struct{})
			}
			m.usedOfferNonces[so.Offer.ProviderPubKeyHex][so.Offer.Nonce] = struct{}{}
		}
	}
	for _, offerID := range bundle.VerifiedAttestationOfferIDs {
		so, ok := m.offers[offerID]
		if !ok {
			continue
		}
		if cfg.AttestationVerifier != nil {
			if err := cfg.AttestationVerifier(so.Offer); err != nil {
				continue
			}
		}
		m.verifiedAttestations[offerID] = true
	}
	for k, t := range bundle.Gossip.Telemetry {
		m.telemetry[k] = t
	}

	m.buyerBalances = copyInt64Map(bundle.Ledger.BuyerBalances)
	m.buyerEscrow = copyInt64Map(bundle.Ledger.BuyerEscrow)
	m.providerBalances = copyInt64Map(bundle.Ledger.ProviderBalances)
	m.providerLockedBonds = copyInt64Map(bundle.Ledger.ProviderLockedBonds)
	m.totalDepositedMicroUSD = bundle.Ledger.TotalDepositedMicroUSD

	for _, ord := range bundle.Orders {
		cp := ord
		if len(ord.AllowedRegions) > 0 {
			cp.AllowedRegions = append([]string(nil), ord.AllowedRegions...)
		}
		if len(ord.AllowedJurisdictions) > 0 {
			cp.AllowedJurisdictions = append([]string(nil), ord.AllowedJurisdictions...)
		}
		if len(ord.BlockedJurisdictions) > 0 {
			cp.BlockedJurisdictions = append([]string(nil), ord.BlockedJurisdictions...)
		}
		m.orders[ord.OrderID] = cp
		m.orderProvBids[ord.OrderID] = make(map[string]int)
	}
	for _, id := range bundle.ClearedOrderIDs {
		m.orderCleared[id] = true
	}
	for _, sb := range bundle.PendingBids {
		m.bidsByOrder[sb.Bid.OrderID] = append(m.bidsByOrder[sb.Bid.OrderID], sb)
		m.bidByID[sb.Bid.BidID] = sb
		m.orderProvBids[sb.Bid.OrderID][sb.Bid.ProviderID]++
		if _, ok := m.usedBidNonces[sb.Bid.ProviderPubKeyHex]; !ok {
			m.usedBidNonces[sb.Bid.ProviderPubKeyHex] = make(map[uint64]struct{})
		}
		m.usedBidNonces[sb.Bid.ProviderPubKeyHex][sb.Bid.Nonce] = struct{}{}
	}

	m.leaseSeq = int64(len(bundle.Leases))
	for _, l := range bundle.Leases {
		cp := l
		m.leases[l.LeaseID] = &cp
		m.leaseOrder = append(m.leaseOrder, l.LeaseID)
		m.leaseSeq = maxSeqFromID(l.LeaseID, "lease-", m.leaseSeq)
		if l.Status == LeaseActive {
			m.activeSlotsUsed[l.ProviderID]++
		}
	}

	m.receiptSeq = int64(len(bundle.Receipts))
	for _, r := range bundle.Receipts {
		m.receipts[r.ReceiptID] = r
		m.receiptOrder = append(m.receiptOrder, r.ReceiptID)
		m.receiptSeq = maxSeqFromID(r.ReceiptID, "rcpt-", m.receiptSeq)
	}

	return m, nil
}
