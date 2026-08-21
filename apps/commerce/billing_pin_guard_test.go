// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// COMMERCE REGISTERS NO /v1/billing ROUTE.
//
// This replaces a guard on the class of defect that made the fold worth doing,
// and it is worth reading for what it used to assert. Every subject-taking
// /v1/billing route mounted here had to PIN the subject, because the class had
// already bitten: /v1/billing/tier was mounted RequestContext ->
// IAMTokenRequired -> TokenRequired with no pin, and TokenRequired
// AUTHENTICATES without PINNING — so any signed-in customer could name another
// subject with ?user= and read their wallet.
//
// It was cross-CUSTOMER, not merely cross-subject: every self-serve signup lands
// in the same org (account.SignupOrg = "hanzo") with a per-person subject, so
// the org namespace was closed while the subject was not. Measured live before
// the fix — one caller, four wallets: hanzo/z 10966, hanzo 6375, hanzo/admin
// 10000, hanzo/dev 0.
//
// The fold retires the whole shape rather than pinning it again. /v1/billing is
// billing's address (HIP-0018, HIP-1220 §2); commerce keeps the store and
// publishes plane operations, and every plane input names its subject as a field
// the DOOR fills from the caller's own credential. There is no query string on
// that wire, so a subject a caller could name is not something to remember to
// pin — it is unrepresentable. apps/billing's own guard asserts the door end.
//
// So the assertion here is the one that keeps the address from coming back: no
// registration in this app may name /v1/billing. A route re-added here would win
// its address by specificity, answer without the door's subject resolution, and
// reopen the leak silently. It walks the AST rather than matching source text —
// the first version of the old guard used a regexp and SILENTLY SKIPPED the very
// route it was written for, reporting green while the pin was absent.
func TestCommerceRegistersNoBillingRoute(t *testing.T) {
	// The WHOLE package, not mount.go alone: a route re-added anywhere in this app
	// wins the address just as well, and a guard that reads one file is a guard
	// that is bypassed by opening a second.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()

	verbs := map[string]bool{
		"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true, "All": true,
	}
	inspected := 0
	walk := func(file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				// zip.Get[In, Out](router, path, …) parses as an IndexExpr when the
				// types are written out; inference leaves it a plain call. Reach the
				// selector either way, or the typed half of the surface is invisible
				// to this guard — which is precisely the shape a route would be
				// re-added in today.
				if ix, isIndex := call.Fun.(*ast.IndexExpr); isIndex {
					sel, ok = ix.X.(*ast.SelectorExpr)
				} else if ix, isList := call.Fun.(*ast.IndexListExpr); isList {
					sel, ok = ix.X.(*ast.SelectorExpr)
				}
			}
			if !ok || !verbs[sel.Sel.Name] {
				return true
			}
			// The path is the FIRST string literal argument: arg 0 for a method on a
			// router (app.Get("/v1/x", …)), arg 1 for the package-level generic,
			// which takes the router first (zip.Get(app, "/v1/x", …)).
			path := ""
			for _, a := range call.Args {
				if lit, isLit := a.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
					path = strings.Trim(lit.Value, `"`)
					break
				}
			}
			if !strings.HasPrefix(path, "/v1/") {
				return true
			}
			inspected++
			if path == "/v1/billing" || strings.HasPrefix(path, "/v1/billing/") {
				t.Errorf("%s %s is registered here at %s — /v1/billing is billing's address, and a "+
					"route re-added on this app wins it by specificity and answers WITHOUT the "+
					"door's subject resolution, which is the cross-customer read the fold closed",
					sel.Sel.Name, path, fset.Position(call.Pos()))
			}
			return true
		})
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(name)
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		file, perr := parser.ParseFile(fset, name, src, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		walk(file)
	}

	// The walk must be REACHING the routes. Without this an empty result and
	// a parse that found nothing are the same green, which is the failure mode the
	// regexp version shipped with.
	if inspected < 5 {
		t.Fatalf("only %d /v1 registrations inspected; the walk is not reaching the routes", inspected)
	}
	t.Logf("inspected %d /v1 registrations across the package", inspected)
}
