package domain

import "math"

// Markup turns a wholesale registrar cost into the price a customer pays. The
// multiplier is applied, then a minimum absolute margin is enforced (so a cheap TLD
// still clears fixed per-order cost), then the result is rounded UP to whole cents.
//
// This is the ONE place a margin is added, mirroring cloud/clients/pricing's
// THIRD_PARTY_MARKUP multiplier — kept as data (Config) so it is tunable without code.
type Markup struct {
	Multiplier     float64 // e.g. 1.15 = +15% over wholesale; <1 is clamped to 1 (never sell below cost)
	MinMarginCents int64   // floor absolute margin over cost, e.g. 300 = at least $3
}

// Sell returns the customer price in cents for a wholesale cost in cents. A
// non-positive cost yields 0 (free / unpriced — the caller treats it as not
// purchasable). The result is always ≥ cost (never sell below wholesale).
func (m Markup) Sell(costCents int64) int64 {
	if costCents <= 0 {
		return 0
	}
	mult := m.Multiplier
	if mult < 1 {
		mult = 1
	}
	marked := int64(math.Ceil(float64(costCents) * mult))
	if marked-costCents < m.MinMarginCents {
		marked = costCents + m.MinMarginCents
	}
	if marked < costCents {
		marked = costCents
	}
	return marked
}

// dollarsToCents converts a registrar USD price (a float) to integer cents, rounding
// to the nearest cent. Registrar prices are exact to the cent, so this is lossless in
// practice; rounding guards against float representation drift.
func dollarsToCents(usd float64) int64 {
	if usd <= 0 {
		return 0
	}
	return int64(math.Round(usd * 100))
}
