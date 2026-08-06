// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// EVERY SUBJECT-TAKING /v1/billing ROUTE MUST PIN THE SUBJECT.
//
// A class guard, not a guard on one route, because the class already bit:
// /v1/billing/tier was mounted RequestContext -> IAMTokenRequired -> TokenRequired
// with no pin. TokenRequired AUTHENTICATES and does not PIN, so any signed-in
// customer could name another subject with ?user= and read their wallet.
//
// It was cross-CUSTOMER, not merely cross-subject: every self-serve signup lands in
// the same org (account.SignupOrg = "hanzo") with a per-person subject, so the org
// namespace was closed while the subject was not. Measured live before the fix —
// one caller, four wallets: hanzo/z 10966, hanzo 6375, hanzo/admin 10000, hanzo/dev 0.
//
// This walks the AST rather than matching source text. The first version of this
// guard used a regexp and SILENTLY SKIPPED the very route it was written for — the
// non-greedy chain match ended at the first `\n\t)` and the scan resumed past
// several registrations, so it reported 14 routes checked and passed while the pin
// was absent. A guard that cannot see what it audits is worse than none, because it
// reads as green.
func TestEverySubjectTakingBillingRoutePinsTheSubject(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mount.go", nil, 0)
	if err != nil {
		t.Fatalf("parse mount.go: %v", err)
	}

	// Routes that take NO caller-named subject. Each was CHECKED, not assumed: the
	// alerts group derives its subject from the RESOLVED ORG (billingSubject ->
	// org.Name, commerce api/billing/spend_alerts.go:62) and scopes every read with
	// org.Namespaced, so there is no ?user= to name. The rest answer about the
	// catalog, the caller's own org, or an inbound provider callback.
	//
	// An entry added here without that check turns this guard into a rubber stamp
	// for the exact leak it exists to catch.
	subjectless := map[string]bool{
		"/v1/billing/plans":              true,
		"/v1/billing/settings":           true,
		"/v1/billing/mode":               true,
		"/v1/billing/webhooks/:provider": true,
		"/v1/billing/alerts":             true,
		"/v1/billing/alerts/:id":         true,
		"/v1/billing/alerts/authorize":   true,
	}

	verbs := map[string]bool{"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true}
	checked, seen := 0, map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !verbs[sel.Sel.Name] {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		path := strings.Trim(lit.Value, `"`)
		if !strings.HasPrefix(path, "/v1/billing/") || subjectless[path] {
			return true
		}
		seen[path] = true
		checked++

		// Does any middleware in the chain pin the subject?
		pinned, gated := false, false
		for _, arg := range call.Args[1:] {
			src := exprName(arg)
			if strings.Contains(src, "PinBillingSubject") {
				pinned = true
			}
			// A route unreachable by a customer needs no pin.
			if strings.Contains(src, "Mint") || strings.Contains(src, "PlatformOnly") {
				gated = true
			}
		}
		if !pinned && !gated {
			t.Errorf("%s %s has no PinBillingSubject at %s: IAMTokenRequired and "+
				"TokenRequired AUTHENTICATE without pinning, so any signed-in customer "+
				"can name another subject and read their wallet",
				sel.Sel.Name, path, fset.Position(call.Pos()))
		}
		return true
	})

	// The guard must be able to SEE the route it was written for. Without this the
	// regexp version's silent miss would have recurred unnoticed.
	if !seen["/v1/billing/tier"] {
		t.Fatal("/v1/billing/tier was not inspected — the guard cannot see the route " +
			"it exists for, so a green result here means nothing")
	}
	if checked < 8 {
		t.Fatalf("only %d subject-taking routes inspected; the walk is not reaching "+
			"the mount table", checked)
	}
	t.Logf("inspected %d subject-taking /v1/billing routes", checked)
}

// exprName renders a call/selector chain enough to test for a middleware name.
func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.CallExpr:
		return exprName(v.Fun)
	case *ast.SelectorExpr:
		return exprName(v.X) + "." + v.Sel.Name
	case *ast.Ident:
		return v.Name
	}
	return ""
}
