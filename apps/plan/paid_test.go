package plan

// Tests for the paywall predicate. They state the RULE — a cloud account category that
// costs money is paid — rather than pinning a snapshot of the catalog's current tier
// list, because that list changes every time pricing does and the predicate must not.

import "testing"

// TestPaid_RetiredTierStillCounts is the regression. @hanzo/plans v1.4.10 retired the
// plus/team-max/custom ladder, but commerce ARCHIVES a retired plan rather than deleting
// it precisely so a subscriber who is still being charged still resolves. Classifying
// against the for-sale catalog used to answer "not paid" for exactly those orgs, which
// 402s a paying customer at the spend gate. The facts the subscription recorded — a
// personal tier at $100/mo — are what decide, and they do not expire when the price
// list changes.
func TestPaid_RetiredTierStillCounts(t *testing.T) {
	for name, tier := range map[string]Tier{
		"plus $100/mo":      {Category: "personal", Price: 10000},
		"team-max $225/mo":  {Category: "team", Price: 22500},
		"custom negotiated": {Category: "enterprise", ContactSales: true},
	} {
		if !Paid(tier) {
			t.Errorf("Paid(%s) = false, want true — a retired tier is still being charged for", name)
		}
	}
}

// TestPaid_CurrentTiersArePaid covers the tiers on sale today (@hanzo/plans v1.4.11:
// go $9, dev $19, pro $49, max $99, team $25/seat, enterprise contact-sales).
func TestPaid_CurrentTiersArePaid(t *testing.T) {
	for name, tier := range map[string]Tier{
		"go":         {Category: "personal", Price: 900},
		"dev":        {Category: "personal", Price: 1900},
		"pro":        {Category: "personal", Price: 4900},
		"max":        {Category: "personal", Price: 9900},
		"team":       {Category: "team", Price: 2500},
		"enterprise": {Category: "enterprise", ContactSales: true},
	} {
		if !Paid(tier) {
			t.Errorf("Paid(%s) = false, want true (a paid cloud tier)", name)
		}
	}
}

// TestPaid_FreeAndNonAccountAreNotPaid: a free cloud tier buys nothing, and another
// product line is not a cloud account however much it costs — a World subscriber must
// not clear the CLOUD paywall. The zero Tier is the "we know nothing" case and must
// never grant.
func TestPaid_FreeAndNonAccountAreNotPaid(t *testing.T) {
	for name, tier := range map[string]Tier{
		"developer (free cloud)": {Category: "personal", Price: 0},
		"world-free":             {Category: "world", Price: 0},
		"world-pro $29/mo":       {Category: "world", Price: 2900},
		"world-enterprise":       {Category: "world", ContactSales: true},
		"social-pro $29/mo":      {Category: "social", Price: 2900},
		"dns-pro $5/mo":          {Category: "dns", Price: 500},
		"zero tier":              {},
		"unknown category":       {Category: "nonexistent", Price: 9999},
	} {
		if Paid(tier) {
			t.Errorf("Paid(%s) = true, want false (free / non-account / unknown)", name)
		}
	}
}
