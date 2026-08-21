package billing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NO DOOR HERE READS A SUBJECT FROM THE REQUEST.
//
// This is the door end of the invariant the fold moved. Its other half lives in
// apps/commerce (TestCommerceRegistersNoBillingRoute) and neither can see the
// other, which is the point: one asserts the address did not come back, this
// asserts the address is answered safely.
//
// The class already bit, on the surface these doors replaced. /v1/billing/tier
// authenticated without pinning, so any signed-in customer could name another
// subject with ?user= and read their wallet — and it was cross-CUSTOMER rather
// than merely cross-subject, because every self-serve signup lands in one org
// with a per-person subject, so the org namespace was closed while the subject
// was not. Measured live before the fix: one caller, four wallets.
//
// The relay's answer is structural rather than procedural. Every plane input
// that names a subject has that field filled from `payer` / `payerOf`, which
// resolve it from the caller's own credential — so there is no pin to remember
// and no chain to get wrong. This walks the AST for the one way that could
// regress: a Subject field assigned from anything else, above all a query value.
func TestNoDoorReadsItsSubjectFromTheRequest(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	assigned := 0

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
		ast.Inspect(file, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Subject" {
				return true
			}
			assigned++
			// The admitted sources, and every one of them is the VALIDATED
			// principal wearing a different hat:
			//
			//   subject    what payer and payerOf return — the wallet this request
			//              bills from, resolved from the caller's own credential.
			//   c.Subject  the field callerIn filled from c.User(), for the roster
			//              read, where the subject is the PERSON asking rather than
			//              a wallet.
			//   c.User()   that same validated user id, read directly.
			//
			// What is refused is anything else, and the shape that matters is a
			// query value: a subject a caller can name is a wallet a caller can read.
			switch v := kv.Value.(type) {
			case *ast.Ident:
				if v.Name == "subject" {
					return true
				}
			case *ast.SelectorExpr:
				if x, ok := v.X.(*ast.Ident); ok && x.Name == "c" && v.Sel.Name == "Subject" {
					return true
				}
			case *ast.CallExpr:
				if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "c" && sel.Sel.Name == "User" {
						return true
					}
				}
			}
			t.Errorf("%s: a Subject field is filled from something other than the "+
				"resolved payer at %s — a subject a caller can name is a wallet a "+
				"caller can read", name, fset.Position(kv.Pos()))
			return true
		})
	}

	// The walk must be reaching the doors. An empty result and a parse that found
	// nothing are the same green otherwise, which is how the first version of this
	// class of guard shipped passing while the leak was open.
	if assigned < 5 {
		t.Fatalf("only %d Subject assignments inspected; the walk is not reaching the doors", assigned)
	}
	t.Logf("inspected %d Subject assignments across the relayed doors", assigned)
}
