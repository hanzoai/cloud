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

// centsOf rounds a registrar USD price to integer cents.
//
// It takes a float because the registrar's wire IS one: name.com sends
// purchasePrice and renewalPrice as JSON numbers, so the value is a binary
// approximation before this package sees it and there is no exact decimal left to
// parse. money.ParseUSD is the reader for a price that arrives as a STRING — the
// DigitalOcean balances take that path — and it is not this one's replacement.
//
// The rounding is what makes the figure exact again, and it is sound for these
// inputs: a registrar price is exact to the cent, so the value is never near a
// half-cent midpoint where a representation error of one part in 10^12 could
// decide the answer.
func centsOf(usd float64) int64 {
	if usd <= 0 {
		return 0
	}
	return int64(math.Round(usd * 100))
}
