package link

import (
	"encoding/json"
	"testing"
	"time"
)

func lk(provider, account, kind string, sessionPct float64) Link {
	u, _ := json.Marshal(Usage{SessionPct: sessionPct})
	return Link{
		ID: "id-" + account, Provider: provider, Account: account, Kind: kind,
		Status: StatusLinked, Usage: string(u),
	}
}

// TestRoutePlanRedundancyAndBilling: subscriptions (the flat-rate redundancy pool)
// come first, ordered by headroom; the api key is the trailing metered route; each
// candidate carries its billing mode; the primary is the most-available account.
func TestRoutePlanRedundancyAndBilling(t *testing.T) {
	links := []Link{
		lk("claude", "maxA", KindSubscription, 80), // headroom 20
		lk("claude", "maxB", KindSubscription, 10), // headroom 90 — two Claude Max, redundancy
		lk("hanzo", "hk", KindAPIKey, 0),           // headroom 100 — the API route
	}
	p := Plan(links, time.Unix(0, 0))
	if len(p.Candidates) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(p.Candidates))
	}
	if p.Candidates[0].Account != "maxB" || p.Candidates[1].Account != "maxA" {
		t.Fatalf("subscriptions must order by headroom desc, got %+v", p.Candidates)
	}
	if p.Candidates[2].Provider != "hanzo" {
		t.Fatalf("the api key must come after the subscriptions, got %+v", p.Candidates[2])
	}
	if p.Candidates[0].Billing != BillingPlan || p.Candidates[2].Billing != BillingCommerce {
		t.Fatalf("each candidate must carry its billing mode")
	}
	if p.Primary == nil || p.Primary.Account != "maxB" {
		t.Fatalf("primary should be the most-available subscription (maxB), got %+v", p.Primary)
	}
}

// TestRouteFailoverToAPIWhenSubsExhausted: when every subscription is rate-limited,
// the primary fails over to the pay-per-call api key (always usable); the exhausted
// subscriptions remain listed as unavailable candidates with a reason.
func TestRouteFailoverToAPIWhenSubsExhausted(t *testing.T) {
	links := []Link{
		lk("claude", "maxA", KindSubscription, 100),
		lk("claude", "maxB", KindSubscription, 100),
		lk("hanzo", "hk", KindAPIKey, 50),
	}
	p := Plan(links, time.Unix(0, 0))
	if p.Primary == nil || p.Primary.Kind != KindAPIKey {
		t.Fatalf("primary must fail over to the api key, got %+v", p.Primary)
	}
	for _, c := range p.Candidates {
		if c.Kind == KindSubscription {
			if c.Available {
				t.Fatalf("an exhausted subscription must be unavailable, got %+v", c)
			}
			if c.Reason == "" {
				t.Fatalf("an unavailable candidate must carry a reason")
			}
		}
	}
}

// TestRouteAllExhaustedNoPrimary: all subscriptions exhausted and no api key →
// an honest nil primary (nothing routable right now), never a fabricated pick.
func TestRouteAllExhaustedNoPrimary(t *testing.T) {
	p := Plan([]Link{lk("claude", "maxA", KindSubscription, 100)}, time.Unix(0, 0))
	if p.Primary != nil {
		t.Fatalf("no routable account → primary must be nil, got %+v", p.Primary)
	}
}

// TestRouteExcludesRevoked: a revoked (logged-out) account is never a candidate.
func TestRouteExcludesRevoked(t *testing.T) {
	l := lk("claude", "maxA", KindSubscription, 10)
	l.Status = StatusRevoked
	p := Plan([]Link{l}, time.Unix(0, 0))
	if len(p.Candidates) != 0 {
		t.Fatalf("a revoked account must never be a route candidate, got %+v", p.Candidates)
	}
}
