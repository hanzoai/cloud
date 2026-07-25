// Copyright © 2026 Hanzo AI. MIT License.

// activeplan.go answers the subscription paywall's ONE question: does org X hold a
// LIVE PAID plan? It is the commerce "active-subscription check" — the tier sibling of
// CheckEntitlement (per-product license). CheckEntitlement deliberately cannot answer
// this: it filters Status=Active ONLY (a TRIALING subscriber would be mis-read as
// unentitled) and it is scoped to a single product id, whereas the paywall gates on the
// PLAN TIER regardless of product. So ActivePaidPlan is a distinct read over the SAME
// org resolver + subscription store, counting ACTIVE *and* TRIALING subscriptions and
// classifying the tier through @hanzo/plans (clients/plan.Paid) — the single source of
// truth for which tiers cost money.
//
// It is an OPTIONAL capability on the in-process client (not on the narrow
// types.CommerceClient interface): the paywall (routers.PlanChecker) resolves it by
// type-assertion, mirroring types.ModelLister. A commerce build that cannot answer
// (split-deploy ZAP client, disabled stub) simply does not implement it, and the
// paywall fails OPEN — an outage never locks out a subscriber.
package commerceclient

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/plan"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/subscription"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
)

// ActivePaidPlan reports whether org `orgID` holds a LIVE PAID Hanzo plan — the paywall's
// admit signal — and the resolved tier slug. "Live paid" = an ACTIVE or TRIALING,
// unexpired subscription whose plan tier is a paid cloud account tier per @hanzo/plans
// (Pro/Plus/Max/Team/Team-Max/Enterprise/Custom). Money-safety mirrors CheckEntitlement:
// it NEVER fabricates a grant, and any machinery it cannot resolve returns an ERROR so the
// paywall fails OPEN (admits) rather than locking a subscriber out.
//
//   - (tier, true,  nil) — a live paid subscription exists (tier is its plan slug).
//   - ("",   false, nil) — resolution succeeded, but NO live paid subscription: the
//     definitive "no plan" answer the paywall turns into a 402.
//   - (_,    _,     err) — commerce not co-resident, org unresolvable, subscription query
//     error, or plan-catalog unreadable: the paywall admits (fail open).
func (c *inProcessClient) ActivePaidPlan(ctx context.Context, orgID string) (string, bool, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return "", false, fmt.Errorf("commerce.ActivePaidPlan: empty orgID")
	}

	e := c.resolve()
	if e == nil || e.App() == nil {
		// Commerce not co-resident / not booted — the subscription store is unreadable,
		// so the plan cannot be verified. Fail open at the paywall; never fabricate.
		return "", false, fmt.Errorf("commerce.ActivePaidPlan: commerce not co-resident; cannot verify plan (org=%s)", orgID)
	}

	// Resolve org → its datastore namespace via commerce's OWN canonical resolver — the
	// SAME binding CheckEntitlement + the money path use, so the read is derived from a
	// real commerce Organization record and can never cross orgs.
	o, err := commerceorg.Resolve(ctx, orgID)
	if err != nil {
		return "", false, fmt.Errorf("commerce.ActivePaidPlan: resolve org %q: %w", orgID, err)
	}

	// Load every subscription in the org's namespace (the namespace IS the org boundary).
	// Unlike CheckEntitlement's Status=Active filter, we scan them all and keep ACTIVE OR
	// TRIALING here — the owner's cut counts a trial as a live plan.
	ds := datastore.New(o.Namespaced(ctx))
	var subs []*subscription.Subscription
	if _, err := subscription.Query(ds).GetAll(&subs); err != nil {
		return "", false, fmt.Errorf("commerce.ActivePaidPlan: query subscriptions for org %q: %w", orgID, err)
	}

	now := time.Now()
	for _, s := range subs {
		if s == nil {
			continue
		}
		if !liveStatus(s.Status) {
			continue // only ACTIVE or TRIALING grants; PAST_DUE/CANCELED/UNPAID do not.
		}
		if !s.PeriodEnd.IsZero() && !s.PeriodEnd.After(now) {
			continue // period elapsed despite the status — never grant on it.
		}
		slug := strings.TrimSpace(s.Plan.Slug)
		if slug == "" {
			continue // no resolvable plan tier on this sub — cannot classify it.
		}
		paid, perr := plan.Paid(slug)
		if perr != nil {
			// Plan catalog unreadable — cannot classify the tier. Surface the error so
			// the paywall FAILS OPEN, never deny-by-guess on a catalog outage.
			return "", false, fmt.Errorf("commerce.ActivePaidPlan: classify plan %q: %w", slug, perr)
		}
		if paid {
			return slug, true, nil
		}
	}

	// Resolution succeeded; the org has no ACTIVE/TRIALING PAID subscription — a real
	// "no plan" answer (not a machinery failure). The paywall turns this into the 402.
	return "", false, nil
}

// liveStatus reports whether a subscription status grants access: ACTIVE or TRIALING.
// PAST_DUE, CANCELED, and UNPAID do NOT (the owner's cut: "active/trialing … not
// canceled/past_due"). Read from commerce's own subscription.Status vocabulary
// (github.com/hanzoai/commerce/models/subscription) so it never drifts from the source.
func liveStatus(s subscription.Status) bool {
	return s == subscription.Active || s == subscription.Trialing
}
