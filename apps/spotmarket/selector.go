// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"math"
	"sort"
	"strings"
)

// Candidate bundles a provider's signed bid, its backing signed offer, and
// measured runtime telemetry for Enso multi-dimensional selection.
type Candidate struct {
	SignedBid   SignedBid
	SignedOffer SignedOffer
	Telemetry   ProviderTelemetry
	FreeSlots   int

	// HardwareAttestationVerified is populated strictly from a trusted
	// AttestationVerifier result (never from Offer.AttestationClass alone).
	// Orders with RequireHardwareAttested fail closed when this is false.
	HardwareAttestationVerified bool
}

// RankedCandidate is an eligible candidate annotated with its normalized
// Enso sub-scores and final composite score.
type RankedCandidate struct {
	Candidate        Candidate
	EffectiveTTFTMs  float64
	ReliabilityBPS   int
	PriceScore       float64
	LatencyScore     float64
	ReliabilityScore float64
	CompositeScore   float64
}

// NormalizeWeights ensures RankingWeights are non-negative and sum to 1.0.
// If all weights are zero or negative, DefaultRankingWeights is returned.
func NormalizeWeights(w RankingWeights) RankingWeights {
	p := math.Max(0, w.PriceWeight)
	l := math.Max(0, w.LatencyWeight)
	r := math.Max(0, w.ReliabilityWeight)
	sum := p + l + r
	if sum <= 0 {
		return DefaultRankingWeights()
	}
	return RankingWeights{
		PriceWeight:       p / sum,
		LatencyWeight:     l / sum,
		ReliabilityWeight: r / sum,
	}
}

// RankCandidates filters and ranks GPU spot-market candidates for an Order
// strictly by cryptographic validity (when verifySignatures is true), expiry,
// buyer policy constraints, measured latency, bid price, and observed
// reliability.
//
// Neutrality guarantee: Offer.OperatorClass (e.g., "hanzo" vs "independent")
// is NEVER read or consulted anywhere in this function. Identical offers and
// telemetry receive bit-for-bit identical scores regardless of operator class.
func RankCandidates(order Order, candidates []Candidate, nowUnix int64, verifySignatures bool) ([]RankedCandidate, []Rejection) {
	weights := NormalizeWeights(order.RankingWeights)
	eligible := make([]RankedCandidate, 0, len(candidates))
	rejections := make([]Rejection, 0)

	for _, cand := range candidates {
		offer := cand.SignedOffer.Offer
		bid := cand.SignedBid.Bid

		if verifySignatures {
			if err := VerifySignedOffer(cand.SignedOffer); err != nil {
				rejections = append(rejections, Rejection{
					EntityID:          bid.BidID,
					ProviderID:        bid.ProviderID,
					Reason:            "invalid_offer_signature: " + err.Error(),
					VerificationClass: VerificationCryptoSignature,
				})
				continue
			}
			if err := VerifySignedBid(cand.SignedBid); err != nil {
				rejections = append(rejections, Rejection{
					EntityID:          bid.BidID,
					ProviderID:        bid.ProviderID,
					Reason:            "invalid_bid_signature: " + err.Error(),
					VerificationClass: VerificationCryptoSignature,
				})
				continue
			}
		}

		if offer.ExpiresAtUnix <= nowUnix {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "offer_expired",
				VerificationClass: VerificationCryptoSignature,
			})
			continue
		}
		if bid.ExpiresAtUnix <= nowUnix {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "bid_expired",
				VerificationClass: VerificationCryptoSignature,
			})
			continue
		}
		if cand.FreeSlots <= 0 || offer.AvailableSlots <= 0 {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "no_available_capacity_slots",
				VerificationClass: VerificationEconomicStake,
			})
			continue
		}
		if !containsString(offer.SupportedModels, order.Model) {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "model_unsupported",
				VerificationClass: VerificationCryptoSignature,
			})
			continue
		}
		if offer.Hardware.AvailableVRAMGB < order.MinVRAMGB || bid.CommittedVRAMGB < order.MinVRAMGB {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "insufficient_vram",
				VerificationClass: VerificationCryptoSignature,
			})
			continue
		}
		if order.RequireHardwareAttested &&
			(!cand.HardwareAttestationVerified ||
				offer.AttestationClass != VerificationHardwareAttestation ||
				strings.TrimSpace(offer.AttestationQuoteHash) == "") {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "hardware_attestation_unverified",
				VerificationClass: VerificationHardwareAttestation,
			})
			continue
		}
		if len(order.AllowedRegions) > 0 && !containsString(order.AllowedRegions, offer.Location.Region) {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "region_policy_mismatch",
				VerificationClass: VerificationCryptoSignature,
			})
			continue
		}
		if len(order.AllowedJurisdictions) > 0 && !containsString(order.AllowedJurisdictions, offer.Location.Jurisdiction) {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "jurisdiction_not_allowed",
				VerificationClass: VerificationCryptoSignature,
			})
			continue
		}
		if len(order.BlockedJurisdictions) > 0 && containsString(order.BlockedJurisdictions, offer.Location.Jurisdiction) {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "jurisdiction_blocked",
				VerificationClass: VerificationCryptoSignature,
			})
			continue
		}
		if bid.BidMicroUSDPer1MTokens <= 0 || bid.BidMicroUSDPer1MTokens > order.MaxMicroUSDPer1MTokens {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "price_exceeds_order_ceiling",
				VerificationClass: VerificationEconomicStake,
			})
			continue
		}
		if bid.OfferedTokens <= 0 || (!order.AllowPartialFill && bid.OfferedTokens < order.RequestedTokens) {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "insufficient_offered_tokens",
				VerificationClass: VerificationEconomicStake,
			})
			continue
		}

		// Use measured runtime telemetry when available; fall back to advertised
		// SLO TTFT and conservative Laplace prior reliability for unmeasured peers.
		effTTFT := cand.Telemetry.MeasuredTTFTMs
		if effTTFT <= 0 {
			effTTFT = float64(offer.SLO.TargetTTFTMs)
			if effTTFT <= 0 {
				effTTFT = 250.0
			}
		}
		effRelBPS := cand.Telemetry.ReliabilityBPS
		if cand.Telemetry.ObservedSampleCount == 0 && effRelBPS == 0 {
			effRelBPS = 9000 // 90.00% neutral prior for fresh unmeasured providers
		}

		if order.MaxTTFTMs > 0 && effTTFT > float64(order.MaxTTFTMs) {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "measured_ttft_exceeds_policy",
				VerificationClass: VerificationEconomicStake,
			})
			continue
		}
		if order.MinReliabilityBPS > 0 && effRelBPS < order.MinReliabilityBPS {
			rejections = append(rejections, Rejection{
				EntityID:          bid.BidID,
				ProviderID:        bid.ProviderID,
				Reason:            "reliability_below_policy",
				VerificationClass: VerificationEconomicStake,
			})
			continue
		}

		eligible = append(eligible, RankedCandidate{
			Candidate:       cand,
			EffectiveTTFTMs: effTTFT,
			ReliabilityBPS:  effRelBPS,
		})
	}

	if len(eligible) == 0 {
		return eligible, rejections
	}

	minPrice := eligible[0].Candidate.SignedBid.Bid.BidMicroUSDPer1MTokens
	maxPrice := minPrice
	minLat := eligible[0].EffectiveTTFTMs
	maxLat := minLat

	for _, rc := range eligible[1:] {
		p := rc.Candidate.SignedBid.Bid.BidMicroUSDPer1MTokens
		if p < minPrice {
			minPrice = p
		}
		if p > maxPrice {
			maxPrice = p
		}
		if rc.EffectiveTTFTMs < minLat {
			minLat = rc.EffectiveTTFTMs
		}
		if rc.EffectiveTTFTMs > maxLat {
			maxLat = rc.EffectiveTTFTMs
		}
	}

	for i := range eligible {
		p := eligible[i].Candidate.SignedBid.Bid.BidMicroUSDPer1MTokens
		if maxPrice == minPrice {
			eligible[i].PriceScore = 1.0
		} else {
			eligible[i].PriceScore = float64(maxPrice-p) / float64(maxPrice-minPrice)
		}

		if maxLat == minLat {
			eligible[i].LatencyScore = 1.0
		} else {
			eligible[i].LatencyScore = (maxLat - eligible[i].EffectiveTTFTMs) / (maxLat - minLat)
		}

		rel := float64(eligible[i].ReliabilityBPS) / 10000.0
		if rel < 0 {
			rel = 0
		} else if rel > 1 {
			rel = 1
		}
		eligible[i].ReliabilityScore = rel

		eligible[i].CompositeScore =
			weights.PriceWeight*eligible[i].PriceScore +
				weights.LatencyWeight*eligible[i].LatencyScore +
				weights.ReliabilityWeight*eligible[i].ReliabilityScore
	}

	// Deterministic, operator-neutral ordering.
	const scoreEps = 1e-12
	sort.Slice(eligible, func(i, j int) bool {
		diff := eligible[i].CompositeScore - eligible[j].CompositeScore
		if math.Abs(diff) > scoreEps {
			return diff > 0
		}
		pi := eligible[i].Candidate.SignedBid.Bid.BidMicroUSDPer1MTokens
		pj := eligible[j].Candidate.SignedBid.Bid.BidMicroUSDPer1MTokens
		if pi != pj {
			return pi < pj
		}
		if math.Abs(eligible[i].EffectiveTTFTMs-eligible[j].EffectiveTTFTMs) > scoreEps {
			return eligible[i].EffectiveTTFTMs < eligible[j].EffectiveTTFTMs
		}
		if eligible[i].ReliabilityBPS != eligible[j].ReliabilityBPS {
			return eligible[i].ReliabilityBPS > eligible[j].ReliabilityBPS
		}
		return eligible[i].Candidate.SignedBid.Bid.BidID < eligible[j].Candidate.SignedBid.Bid.BidID
	})

	return eligible, rejections
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
