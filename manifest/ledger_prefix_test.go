// Copyright © 2026 Hanzo AI. MIT License.

package manifest

import (
	"strings"
	"testing"
)

// The customer's own ledger reads must be ROUTABLE, not merely mounted.
//
// This guards a failure that survived a correct fix and looked identical to the
// bug it was meant to close. GET /v1/billing/{transactions,credit-balance,
// accounts} 404'd because the serving app's mount never named them; naming them
// there, building the image, and verifying the route strings were present in the
// shipped binary still left all three answering 404 in production — because a
// route reaches an app only when BOTH halves of its address exist. The mount
// says what the app will answer. This table says what the host may hand it. A
// leaf missing here falls to the "/v1" remainder on ai's row and gets ai's bare
// 404, which from outside is indistinguishable from a route that was never
// mounted.
//
// So the assertion is deliberately about the ROUTER's half. The app's half is
// covered by that app's own route tests, which cannot see this table, and the
// byte-level check on a built image cannot see it either — the string was in the
// binary the whole time it was returning 404.
//
// WHICH APP it names changed, and the reason is the point of the fold:
// /v1/billing is the billing capability's address (HIP-0018, HIP-1220 §2), so
// billing owns the door and commerce owns the store behind it. The router's
// half moved with the door.
func TestLedgerReadsAreRoutedToBilling(t *testing.T) {
	prefixes := PrefixesFor("billing")
	if len(prefixes) == 0 {
		t.Fatal("billing claims no prefixes — the app is not routable at all")
	}

	// Every family the money door answers, leaf by leaf, because a root prefix
	// is a claim about a SUBTREE and this test exists to catch the case where a
	// leaf is not in it. accounts/:id/members is here for the same reason: a
	// parameterised child is exactly where a prefix list is easiest to get wrong.
	for _, path := range []string{
		"/v1/billing/transactions",
		"/v1/billing/credits",
		"/v1/billing/credit-balance",
		"/v1/billing/credit-balance/breakdown",
		"/v1/billing/accounts",
		"/v1/billing/accounts/acme/members",
		"/v1/billing/invoices",
		"/v1/billing/invoices/inv_1/pdf",
		"/v1/billing/alerts",
		"/v1/billing/alerts/authorize",
		"/v1/billing/methods",
		"/v1/billing/portal/methods",
		"/v1/billing/payouts",
		"/v1/billing/plans",
		"/v1/billing/settings",
		"/v1/billing/subscriptions",
		"/v1/billing/tier",
		"/v1/billing/topup",
		"/v1/billing/topup/token",
		"/v1/billing/subscribe/card",
		"/v1/billing/recharge/run-all",
		"/v1/billing/usage/rollup",
		"/v1/billing/wire",
		"/v1/billing/crypto/options",
	} {
		routed := false
		for _, p := range prefixes {
			if path == p || strings.HasPrefix(path, p+"/") {
				routed = true
				break
			}
		}
		if !routed {
			t.Fatalf("%s is not routed to billing — the host will hand it to the "+
				"/v1 remainder and answer 404 no matter what the mount registers", path)
		}
	}
}

// Commerce may not be handed a /v1/billing address, because it no longer serves
// one.
//
// This is the fold's other half and it is worth its own assertion: the addresses
// above stop working if billing loses them, and they become UNREACHABLE A SECOND
// WAY if commerce keeps claiming them — the router resolves most-specific-first,
// so a leftover deeper claim on commerce's row would win the address and hand it
// to an app that answers 404 for it now.
//
// It also replaces a test that guarded a trap this shape cannot have. Commerce
// used to state twenty /v1/billing LEAVES, and "credits" looks, to the eye
// scanning that list, as though it already covers "credit-balance" — so a
// sibling that was never stated read as stated. One root prefix on one app
// removes the list and the trap with it.
func TestCommerceIsHandedNoBillingAddress(t *testing.T) {
	for _, p := range PrefixesFor("commerce") {
		if p == "/v1/billing" || strings.HasPrefix(p, "/v1/billing/") {
			t.Errorf("commerce still claims %s — /v1/billing is billing's address, and a "+
				"deeper claim here wins the route and hands it to an app that no longer serves it", p)
		}
	}
}
