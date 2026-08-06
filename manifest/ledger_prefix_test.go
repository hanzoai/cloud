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
// accounts} 404'd because apps/commerce/mount.go never named them; naming them
// there, building the image, and verifying the route strings were present in the
// shipped binary still left all three answering 404 in production — because a
// route reaches an app only when BOTH halves of its address exist. mount.go says
// what commerce will answer. This table says what the host may hand it. A leaf
// missing here falls to the "/v1" remainder on ai's row and gets ai's bare 404,
// which from outside is indistinguishable from a route that was never mounted.
//
// So the assertion is deliberately about the ROUTER's half. The app's half is
// covered by apps/commerce's own route tests, which cannot see this table, and
// the byte-level check on a built image cannot see it either — the string was in
// the binary the whole time it was returning 404.
func TestLedgerReadsAreRoutedToCommerce(t *testing.T) {
	prefixes := PrefixesFor("commerce")
	if len(prefixes) == 0 {
		t.Fatal("commerce claims no prefixes — the app is not routable at all")
	}

	// accounts/:id/members is covered by the accounts prefix; the others are
	// leaves in their own right. credit-balance is NOT a child of credits —
	// they are sibling prefixes, and matching one does not match the other.
	for _, path := range []string{
		"/v1/billing/transactions",
		"/v1/billing/credit-balance",
		"/v1/billing/accounts",
		"/v1/billing/accounts/acme/members",
	} {
		routed := false
		for _, p := range prefixes {
			if path == p || strings.HasPrefix(path, p+"/") {
				routed = true
				break
			}
		}
		if !routed {
			t.Fatalf("%s is not routed to commerce — the host will hand it to the "+
				"/v1 remainder and answer 404 no matter what mount.go registers", path)
		}
	}
}

// credits and credit-balance are distinct addresses, and a prefix that matched
// both would mean one of them was never stated. This is the specific trap in
// adding credit-balance next to an existing credits entry: eyeballing the list,
// "credits" looks like it already covers it.
func TestCreditBalanceIsItsOwnPrefix(t *testing.T) {
	var credits, balance bool
	for _, p := range PrefixesFor("commerce") {
		switch p {
		case "/v1/billing/credits":
			credits = true
		case "/v1/billing/credit-balance":
			balance = true
		}
	}
	if !credits || !balance {
		t.Fatalf("both /v1/billing/credits and /v1/billing/credit-balance must be "+
			"stated (credits=%v credit-balance=%v)", credits, balance)
	}
}
