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
// the ENDPOINT fills from the caller's own credential. There is no query string on
// that wire, so a subject a caller could name is not something to remember to
// pin — it is unrepresentable. apps/billing's own guard asserts the endpoint end.
//
// So the assertion here is the one that keeps the address from coming back: no
// registration in this app may name /v1/billing. A route re-added here would win
// its address by specificity, answer without the endpoint's subject resolution,
// and reopen the leak silently. It walks the AST rather than matching source text —
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
		"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true, "Raw": true,
	}
	inspected := 0
	walk := func(file *ast.File) {
		// a group's prefix, by the name it is bound to in this file
		prefixes := map[string]string{}
		var prefixOf func(e ast.Expr) string
		prefixOf = func(e ast.Expr) string {
			switch v := e.(type) {
			case *ast.Ident:
				return prefixes[v.Name]
			case *ast.CallExpr:
				if s, ok := v.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Group" && len(v.Args) > 0 {
					if lit, ok := v.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						return prefixOf(s.X) + strings.Trim(lit.Value, `"`)
					}
				}
			}
			return ""
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					if p := prefixOf(as.Rhs[0]); p != "" {
						prefixes[id.Name] = p
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !verbs[sel.Sel.Name] {
				return true
			}
			path := ""
			for _, a := range call.Args {
				if lit, isLit := a.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
					path = prefixOf(sel.X) + strings.Trim(lit.Value, `"`)
					break
				}
			}
			if path == "" {
				return true
			}
			inspected++
			if path == "/v1/billing" || strings.HasPrefix(path, "/v1/billing/") {
				t.Errorf("%s %s is registered here at %s — /v1/billing is billing's address, and a "+
					"route re-added on this app wins it by specificity and answers WITHOUT the "+
					"endpoint's subject resolution, which is the cross-customer read the fold closed",
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
