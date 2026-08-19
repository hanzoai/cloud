// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// THE ADDRESSES A SUBSCRIBER'S OWN ACCOUNT PAGE CALLS MUST BE MOUNTED HERE.
//
// A handler existing is not a route existing, and the gap between the two has
// now cost three separate features, each failing the same silent way:
//
//	/v1/billing/credits       a customer with grants saw an empty list
//	/v1/billing/tier          every PAYING customer served the free rate limit
//	/v1/billing/usage/rollup  nothing could say what plan you are on
//
// Every one of them was written, reviewed, vendored and never reachable.
// commerce owns its own api.Route() bundle that registers them, that bundle is
// behind //go:build cloud, and it is not compiled into this binary — so the
// handlers ship and the addresses answer 404. A 404 from an API a client
// swallows reads as "no data", which is why all three looked like empty state
// rather than like a fault, and why none of them was noticed by looking.
//
// So the guard is the LIST, not the mechanism: these are what a subscriber's
// account surface asks for, and if one stops being registered the page it feeds
// goes quietly blank again. Adding a row here is how a new surface says which
// address it depends on.
func TestASubscriberCanReadTheirOwnPlan(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mount.go", nil, 0)
	if err != nil {
		t.Fatalf("parse mount.go: %v", err)
	}

	// Every string literal in the file that looks like one of our addresses.
	// Read from the AST rather than by matching text, for the reason the pin
	// guard beside this one gives: its first version used a regexp, skipped the
	// route it existed for, and passed.
	mounted := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING {
			if s := strings.Trim(lit.Value, `"`); strings.HasPrefix(s, "/v1/billing/") {
				mounted[s] = true
			}
		}
		return true
	})
	if len(mounted) == 0 {
		t.Fatal("no /v1/billing addresses found in mount.go — this guard is watching nothing")
	}

	for _, r := range []struct{ path, answers string }{
		{"/v1/billing/plans", "which plans exist, and what each costs"},
		{"/v1/billing/subscriptions", "which plan this customer actually holds"},
		{"/v1/billing/usage/rollup", "how much of that plan is left, and the wallet beside it"},
		{"/v1/billing/tier", "the rate this customer is served at"},
		{"/v1/billing/credits", "the prepaid balance they bought"},
	} {
		if !mounted[r.path] {
			t.Errorf("%s is not mounted, so nothing can answer %s — the handler exists in the vendored module and reaching it needs a row in billingRead", r.path, r.answers)
		}
	}
}
