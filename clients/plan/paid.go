package plan

// paid.go answers ONE catalog question the subscription paywall asks: "is this plan
// tier a PAID Hanzo cloud subscription?" It reads the embedded @hanzo/plans catalog
// (the single source of truth for what every tier costs) — it does NOT restate the
// tier list in Go, so the answer auto-tracks the catalog and a future paid tier needs
// no code change here. The plan→product entitlement vocabulary still lives only in the
// goja bundle (plan.go); this is a price/category read, not a vocabulary reimpl.

import (
	"fmt"
	"strings"
	"sync"

	hplans "github.com/hanzoai/plans"
)

// accountCategories are the @hanzo/plans catalog categories that ARE a Hanzo cloud
// account tier — the subscription a customer buys to use the cloud product surface.
// The catalog ALSO carries separate product lines ("world", "social", "dns") sold on
// their own surfaces; a subscription to one of those is NOT a cloud account and must
// never clear the cloud paywall. This mirrors the console's own ACCOUNT_CATEGORIES
// (console src/lib/api/plans.ts) so "which categories are the cloud account tiers" has
// ONE definition — read from the catalog, not restated as a slug list.
var accountCategories = map[string]bool{
	"personal":   true,
	"team":       true,
	"enterprise": true,
}

var (
	paidOnce sync.Once
	paidSet  map[string]bool
	paidErr  error
)

// loadPaid builds the paid-cloud-tier set from the embedded @hanzo/plans
// subscription.json ONCE. A tier is PAID when it is a cloud account category AND it
// costs money: priceMonthly > 0, or a negotiated contactSales tier with no self-serve
// price (Custom). This realizes the owner's "Pro ($20/mo) and above clears the paywall"
// cut — {pro, plus, max, team, team-max, enterprise, custom} — WITHOUT hardcoding
// slugs, since the catalog is the single source of truth for which tiers cost money.
func loadPaid() {
	data, err := hplans.Data()
	if err != nil {
		paidErr = fmt.Errorf("plan.Paid: load @hanzo/plans catalog: %w", err)
		return
	}
	raw, ok := data["subscription.json"].([]any)
	if !ok {
		paidErr = fmt.Errorf("plan.Paid: @hanzo/plans subscription.json is not a plan array")
		return
	}
	set := make(map[string]bool, len(raw))
	for _, entry := range raw {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id, _ := obj["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		category, _ := obj["category"].(string)
		if !accountCategories[strings.TrimSpace(category)] {
			continue // a non-account product line (world/social/dns) — never a cloud tier.
		}
		if isPaidTier(obj) {
			set[id] = true
		}
	}
	paidSet = set
}

// isPaidTier reports whether a catalog plan object costs money: a contactSales/quote
// tier (negotiated, priceMonthly null) OR a positive monthly price. A zero/absent
// priceMonthly with no contactSales flag is the free tier (Developer). JSON numbers
// decode to float64, so priceMonthly is read as float64.
func isPaidTier(obj map[string]any) bool {
	if cs, _ := obj["contactSales"].(bool); cs {
		return true
	}
	if p, ok := obj["priceMonthly"].(float64); ok {
		return p > 0
	}
	return false
}

// Paid reports whether plan tier id is a PAID Hanzo cloud subscription tier — the
// paywall's "this org holds a real plan" predicate. Pro/Plus/Max/Team/Team-Max/
// Enterprise/Custom → (true, nil); the free Developer tier, a non-cloud product plan
// (world-*/social-*), an empty or unknown id → (false, nil).
//
// A non-nil error means the embedded catalog could not be read (a build/embed defect —
// effectively impossible in a booted binary). It is returned rather than swallowed so
// the caller can FAIL OPEN (admit the request) rather than mistake a catalog outage for
// "no paid tier" and lock a subscriber out. This function NEVER fabricates a grant: it
// returns true only for a tier the catalog says is a paid cloud account tier.
func Paid(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil // an empty tier is definitively not paid — no catalog read needed.
	}
	paidOnce.Do(loadPaid)
	if paidErr != nil {
		return false, paidErr
	}
	return paidSet[id], nil
}
