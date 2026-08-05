package plan

// paid.go answers ONE question the subscription paywall asks: "is the plan this org
// actually bought a PAID Hanzo cloud account?" It is a PREDICATE over a plan's money
// facts — category and price — and it reads no catalog.
//
// It used to take a tier id and look it up in the @hanzo/plans catalog. That was wrong,
// and it cost money. The catalog lists what is ON SALE TODAY; a subscription records
// what was bought, possibly years ago. Commerce keeps those two apart on purpose: when
// a tier is retired its row is ARCHIVED, never deleted, so "invoices and renewals that
// already reference it" still resolve — "retiring a tier stops new sales; it never
// strands a subscriber" (commerce models/plan.Status). Classifying a subscriber against
// the for-sale list strands exactly the subscribers commerce went out of its way to
// protect: @hanzo/plans v1.4.10 retired plus/team-max/custom, so every org still paying
// for one of them stopped counting as paid.
//
// So the caller passes the plan row the subscription itself froze at subscribe time,
// and this decides. One authority — the record of what the customer bought — instead of
// two that drift apart the moment the price list changes.

import "strings"

// accountCategories are the catalog categories that ARE a Hanzo cloud account tier —
// the subscription a customer buys to use the cloud product surface. The catalog also
// carries separate product lines ("world", "social", "dns") sold on their own surfaces;
// a subscription to one of those is NOT a cloud account and must never clear the cloud
// paywall. This mirrors the console's own ACCOUNT_CATEGORIES (console
// src/lib/api/plans.ts), so "which categories are cloud account tiers" has ONE
// definition. Categories are a closed vocabulary and outlive the tiers that use them —
// which is why classifying on the CATEGORY keeps working for a tier that is retired.
var accountCategories = map[string]bool{
	"personal":   true,
	"team":       true,
	"enterprise": true,
}

// Tier is the money facts of one plan: what product line it belongs to and whether it
// costs anything. It is the subset of a commerce plan row this predicate needs, so
// apps/plan states the paywall rule without importing the commerce models.
type Tier struct {
	// Category is the plan family — "personal"/"team"/"enterprise" are cloud account
	// tiers; "world"/"social"/"dns" are their own products.
	Category string
	// Price is the recurring charge in cents. Zero is the free tier.
	Price int64
	// ContactSales marks a negotiated tier whose price is not published (Custom,
	// Enterprise). It costs money; the amount just is not in the catalog.
	ContactSales bool
}

// Paid reports whether t is a PAID Hanzo cloud account tier — the paywall's "this org
// holds a real plan" predicate. It is true when the tier is a cloud account category AND
// it costs money: a published price above zero, or a negotiated contact-sales contract.
//
// A free cloud tier (priced zero), any tier from another product line whatever it costs,
// and a zero Tier are all false. It NEVER fabricates a grant.
func Paid(t Tier) bool {
	if !accountCategories[strings.TrimSpace(t.Category)] {
		return false
	}
	return t.ContactSales || t.Price > 0
}
