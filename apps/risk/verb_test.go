package risk

// verb_test.go — the three verbs stay three verbs.
//
// An observation is a VALUE this plane records. Learning is a TRANSFORMATION over
// observations. A verdict is a QUERY against the result. They are separable, they
// are separately useful, and they were braided: [plane.learn] recorded, learned
// AND answered a verdict, so a caller could not observe without training and could
// not train without being answered.
//
// The engine exposes the two halves as two calls and the difference is exactly
// this distinction — Inspect scores and moves nothing, Assess learns. So the
// invariant is spellable structurally, and it is worth spelling that way: a
// behavioural test cannot see the difference. Putting Inspect back into learn
// changes no counter that [riskModelState] reports and no verdict any caller
// reads; it silently doubles the model work per event and reintroduces the braid,
// and every other test in this package still passes.
//
// The two facing tests here are one property from both sides: the transformation
// does not query, and the query does not transform.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// verbs names, for each of the plane's two model-touching verbs, the engine call
// that belongs to the OTHER one. A verb that reaches for its counterpart's call
// has absorbed it.
var verbs = map[string]struct {
	forbidden string
	why       string
}{
	"learn": {
		forbidden: "Inspect",
		why: "learn is the TRANSFORMATION and Inspect is the QUERY. It ran both, so the model was " +
			"entered twice per event — projected twice, walked twice, and above the cut attributed " +
			"twice — to build a verdict the response carried and no caller read. A verdict comes " +
			"from plane.score, which is pure. See BenchmarkLearn for what the second pass cost.",
	},
	"score": {
		forbidden: "Assess",
		why: "score is the QUERY and Assess is the TRANSFORMATION. A query that learns cannot be " +
			"used to try a candidate against a tenant's own behaviour, because asking would change " +
			"the answer — and nothing would say so.",
	},
}

// TestPlane_TheVerbsStaySeparate walks the package and fails if either verb calls
// the other's engine entry point.
//
// Mutation proof: add `r.mod.Inspect(tx, types.Entity{OrgID: string(t)})` back to
// plane.learn's loop and this fails while every other test still passes.
func TestPlane_TheVerbsStaySeparate(t *testing.T) {
	fset := token.NewFileSet()
	// THE WHOLE PACKAGE, for the same reason [TestOps_EveryOpIsAdmittedAndPriced]
	// reads it all: a verb moved to another file would otherwise be checked by
	// nobody's assertion.
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	seen := map[string]bool{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				// The RECEIVER is part of the identity: `ops` carries methods called score
				// and learn too, and matching on the name alone would check the wire handler
				// instead of the verb.
				if !ok || recvType(fn) != "plane" {
					continue
				}
				rule, ours := verbs[fn.Name.Name]
				if !ours {
					continue
				}
				seen[fn.Name.Name] = true
				if called(fn, rule.forbidden) {
					t.Errorf("plane.%s calls the engine's %s.\n\n%s", fn.Name.Name, rule.forbidden, rule.why)
				}
			}
		}
	}
	// Both verbs were FOUND. A name this test checks and no file declares means the
	// scan stopped reading the plane, which is how a structural test passes by
	// looking at nothing.
	for name := range verbs {
		if !seen[name] {
			t.Errorf("no file in this package declares plane.%s — the scan is not reading the verbs "+
				"it claims to check", name)
		}
	}
}

// called reports whether fn's body contains a call to a method of the given name.
// It matches the SELECTOR and not the receiver expression, so it holds however the
// model is reached.
func called(fn *ast.FuncDecl, method string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method {
			found = true
		}
		return !found
	})
	return found
}
