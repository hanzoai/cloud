package link

import (
	"encoding/json"
	"sort"
	"time"
)

// route.go is the redundancy seam: a PURE policy that turns a user's linked
// accounts into an ordered list of routing candidates, so a caller (the ai
// gateway / enso router) can fail over across accounts — two Claude Max
// subscriptions for redundancy, then the metered API as the always-available
// backstop — and know how each candidate BILLS before it dials.
//
// This is the policy + its types (the SEAM). EXECUTION — actually dialing a
// provider, detecting a live 429, and advancing to the next candidate — is the
// gateway's job (the deferred failover-execution increment). The policy reads the
// registry's own usage snapshots (the rate-limit headroom @hanzo/usage already
// meters), never a live provider probe, so it is a total function of the Links.

// RouteCandidate is one account the policy would route to, in preference order,
// annotated with how it bills and whether it currently has rate-limit headroom.
type RouteCandidate struct {
	Provider    string  `json:"provider"`
	Account     string  `json:"account,omitempty"`
	Plan        string  `json:"plan,omitempty"`
	Kind        string  `json:"kind"`    // subscription | apikey
	Billing     string  `json:"billing"` // plan | commerce (BillingMode(Kind))
	Available   bool    `json:"available"`
	HeadroomPct float64 `json:"headroomPct"` // remaining capacity 0..100
	Machine     string  `json:"machine,omitempty"`
	Host        string  `json:"host,omitempty"`
	LinkID      string  `json:"linkId"`
	Reason      string  `json:"reason,omitempty"` // why unavailable, when Available=false
}

// RoutePlan is the ordered redundancy plan for a user's accounts: the candidates
// in preference order plus the primary the caller would try first.
type RoutePlan struct {
	Candidates  []RouteCandidate `json:"candidates"`
	Primary     *RouteCandidate  `json:"primary,omitempty"`
	GeneratedAt string           `json:"generatedAt"`
}

// parseUsage decodes a Link's stored usage JSON, or nil when there is none / it
// is malformed (treated as "no snapshot" → full headroom, never a crash).
func parseUsage(raw string) *Usage {
	if raw == "" {
		return nil
	}
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return nil
	}
	return &u
}

// candidateOf projects a Link into a RouteCandidate: headroom from its usage
// snapshot, availability from that headroom (a fully-consumed window → not
// routable right now), and the billing mode from its kind.
func candidateOf(l Link) RouteCandidate {
	h := headroomPct(parseUsage(l.Usage))
	c := RouteCandidate{
		Provider: l.Provider, Account: l.Account, Plan: l.Plan, Kind: l.Kind,
		Billing: BillingMode(l.Kind), HeadroomPct: h,
		Machine: l.Machine, Host: l.Host, LinkID: l.ID,
		Available: h > 0,
	}
	if !c.Available {
		c.Reason = "rate limit reached"
	}
	return c
}

// Plan builds the redundancy route plan from a user's LINKED accounts. Ordering:
// subscription accounts first (the flat-rate pool the user already pays for —
// used first, and failed over between for redundancy: two Claude Max accounts),
// then api-key/hanzo accounts (the metered API route). Within each group the
// order is (available first, then most headroom, then most-recently seen). The
// primary is the first available candidate; if every account is rate-limited it
// falls back to the first api-key account (the pay-per-call backstop is always
// usable) — else nil, an honest "nothing routable right now".
//
// Only linked accounts should be passed (Store.ListLinked); a revoked account is
// never a candidate. The function is pure and total: any Links in, one plan out.
func Plan(links []Link, now time.Time) RoutePlan {
	subs := make([]RouteCandidate, 0, len(links))
	keys := make([]RouteCandidate, 0, len(links))
	for _, l := range links {
		if l.Status != StatusLinked {
			continue
		}
		c := candidateOf(l)
		if l.Kind == KindSubscription {
			subs = append(subs, c)
		} else {
			keys = append(keys, c)
		}
	}
	byPreference := func(cs []RouteCandidate) {
		sort.SliceStable(cs, func(i, j int) bool {
			if cs[i].Available != cs[j].Available {
				return cs[i].Available // available first
			}
			return cs[i].HeadroomPct > cs[j].HeadroomPct // then most headroom
		})
	}
	byPreference(subs)
	byPreference(keys)

	candidates := append(append(make([]RouteCandidate, 0, len(subs)+len(keys)), subs...), keys...)

	plan := RoutePlan{Candidates: candidates, GeneratedAt: now.UTC().Format(time.RFC3339)}
	plan.Primary = pickPrimary(candidates)
	return plan
}

// pickPrimary returns the first available candidate; failing that, the first
// api-key candidate (the metered backstop is usable even when the subscriptions
// are exhausted); failing that, nil.
func pickPrimary(cs []RouteCandidate) *RouteCandidate {
	for i := range cs {
		if cs[i].Available {
			return &cs[i]
		}
	}
	for i := range cs {
		if cs[i].Kind == KindAPIKey {
			return &cs[i]
		}
	}
	return nil
}
