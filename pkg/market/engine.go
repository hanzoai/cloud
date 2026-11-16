package market

import (
	"errors"
	"math"
	"sort"
)

// EnsoRouter implements the selection logic for the Hanzo Cloud.
type EnsoRouter struct {
	Providers map[string]*Provider
}

func NewEnsoRouter() *EnsoRouter {
	return &EnsoRouter{
		Providers: make(map[string]*Provider),
	}
}

// Score calculates the desirability of an offer based on user policy.
func (e *EnsoRouter) Score(offer Offer, provider *Provider, policy SelectionPolicy) float64 {
	// Normalize Price (Lower is better) - assume max price of 1.0 for normalization
	priceScore := 1.0 - math.Min(offer.PricePerMin, 1.0)
	
	// Normalize Latency (Lower is better) - assume max latency of 500ms
	latencyScore := 1.0 - math.Min(float64(offer.LatencyMs)/500.0, 1.0)
	
	// Reliability (Higher is better)
	reliabilityScore := provider.Reliability

	return (priceScore * policy.WeightPrice) +
		(latencyScore * policy.WeightLatency) +
		(reliabilityScore * policy.WeightReliability)
}

// SelectBestOffer picks the optimal provider without preference for Hanzo infrastructure.
func (e *EnsoRouter) SelectBestOffer(demand Demand, offers []Offer) (Offer, error) {
	var bestOffer Offer
	maxScore := -1.0
	found := false

	for _, offer := range offers {
		provider, ok := e.Providers[offer.ProviderID]
		if !ok {
			continue
		}

		// Filter by constraints
		if offer.Specs.VRAM < demand.RequiredVRAM || offer.Specs.TFLOPS < demand.MinTFLOPS {
			continue
		}
		if offer.PricePerMin > demand.MaxPrice {
			continue
		}

		score := e.Score(offer, provider, demand.Policy)
		if score > maxScore {
			maxScore = score
			bestOffer = offer
			found = true
		}
	}

	if !found {
		return Offer{}, errors.New("no suitable providers found for the given constraints")
	}

	return bestOffer, nil
}

// UpdateReliability updates provider stats after a task completion or failure.
func (p *Provider) UpdateReliability(success bool) {
	p.History = append(p.History, success)
	if len(p.History) > 100 {
		p.History = p.History[1:]
	}

	successes := 0
	for _, h := range p.History {
		if h {
			successes++
		}
	}
	p.Reliability = float64(successes) / float64(len(p.History))
}
