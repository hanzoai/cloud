// Package costs is commerce's vendor-cost / COGS surface — the mirror of the
// revenue/usage billing package. Where api/billing answers "what our customers
// owe US", api/costs answers "what WE owe our vendors" (DigitalOcean compute,
// the LLM providers we resell) so admin.hanzo.ai can show COGS beside revenue and
// compute MARGIN.
//
// It reuses commerce's billing primitives verbatim — the same admin gate
// (middleware.TokenRequired(permission.Admin)), the same per-org datastore
// namespace (org.Namespaced), the same usage ledger (transaction rows tagged
// "api-usage") — so cost and revenue read the SAME source of truth and margin is
// self-consistent.
//
// The HTTP contract, mounted under /v1 (api/api/api.go), admin-gated:
//
//	GET /v1/costs?period=YYYY-MM          -> {period, vendors:[...], totalCents}
//	GET /v1/costs/margin?period=YYYY-MM   -> {period, revenueCents, cogsCents, marginCents, grossMarginPct, vendors:[...]}
//
// Every credential is read from the environment (KMS-injected — the DO token from
// the `shared-credentials` secret, the LLM provider keys from `cloud-api-llm-keys`)
// and NEVER inlined, logged, or returned. When a credential is absent or a vendor
// API is unreachable the vendor is reported HONESTLY (source label + a zero or
// estimated amount) — a cost figure is never fabricated.
package costs

import (
	"strings"
	"time"
)

// Source labels the provenance of a vendor cost figure.
//
//	SourceActual    — pulled from the vendor's own billing/usage API (ground truth).
//	SourceEstimated — computed from our metering ledger x a cost-basis table, or a
//	                  placeholder for a vendor not yet wired (amount 0). Honest, not
//	                  fabricated: an estimate is labeled as one.
type Source string

const (
	SourceActual    Source = "actual"
	SourceEstimated Source = "estimated"
)

// VendorCost is one line of what we paid a vendor for a service in a period.
type VendorCost struct {
	Vendor      string `json:"vendor"`         // e.g. "digitalocean", "openai".
	Service     string `json:"service"`        // e.g. "compute", "llm-inference".
	AmountCents int64  `json:"amountCents"`    // what WE pay, in cents (>= 0).
	Period      string `json:"period"`         // the billing period, YYYY-MM.
	Source      Source `json:"source"`         // actual | estimated.
	Note        string `json:"note,omitempty"` // honest context (e.g. "no DO_API_TOKEN configured").
	Currency    string `json:"currency"`       // always "usd" today.
}

// CostReport is the /v1/costs response: every vendor line for a period + the total.
type CostReport struct {
	Period     string       `json:"period"`
	Vendors    []VendorCost `json:"vendors"`
	TotalCents int64        `json:"totalCents"`
	Currency   string       `json:"currency"`
}

// MarginReport is the /v1/costs/margin response: revenue (the usage ledger) minus
// COGS (this package), plus the gross-margin percentage.
type MarginReport struct {
	Period         string       `json:"period"`
	RevenueCents   int64        `json:"revenueCents"`
	COGSCents      int64        `json:"cogsCents"`
	MarginCents    int64        `json:"marginCents"`
	GrossMarginPct float64      `json:"grossMarginPct"`
	Vendors        []VendorCost `json:"vendors"`
	Currency       string       `json:"currency"`
}

// total sums the vendor lines (COGS in cents).
func total(vendors []VendorCost) int64 {
	var sum int64
	for _, v := range vendors {
		sum += v.AmountCents
	}
	return sum
}

// grossMarginPct returns 100 * margin / revenue, rounded to two decimals. Zero
// revenue yields 0 (not NaN/Inf) — an honest "no revenue yet" reads as 0%.
func grossMarginPct(revenueCents, marginCents int64) float64 {
	if revenueCents <= 0 {
		return 0
	}
	pct := float64(marginCents) / float64(revenueCents) * 100
	return float64(int64(pct*100+sign(pct)*0.5)) / 100
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// period returns a normalized YYYY-MM period string, defaulting to the current
// UTC month when the query param is empty or malformed. A caller-supplied period
// is validated (never trusted verbatim) so it can't smuggle anything into the
// downstream vendor-API calls.
func period(raw string) string {
	raw = strings.TrimSpace(raw)
	if validPeriod(raw) {
		return raw
	}
	now := time.Now().UTC()
	return now.Format("2006-01")
}

// validPeriod reports whether s is exactly YYYY-MM with a real month (01-12).
func validPeriod(s string) bool {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return false
	}
	return t.Format("2006-01") == s
}

// parseDay parses a YYYY-MM-DD date at UTC midnight and returns its unix seconds.
// Used by tests to place stubbed vendor buckets inside/outside a period window.
func parseDay(day string) (int64, error) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0, err
	}
	return t.UTC().Unix(), nil
}

// periodBounds returns [start, end) UTC instants for a YYYY-MM period.
func periodBounds(p string) (time.Time, time.Time) {
	start, err := time.Parse("2006-01", p)
	if err != nil {
		now := time.Now().UTC()
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	start = start.UTC()
	end := start.AddDate(0, 1, 0)
	return start, end
}
