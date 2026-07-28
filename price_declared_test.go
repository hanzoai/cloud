package cloud

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestPriceDeclared is the gate three separate comments already claimed existed —
// price.go ("TestPriceDeclared fails until somebody does"), build.go and
// middleware_billing.go all cite it as the reason an unpriced surface cannot ship.
// It did not exist. Every one of those comments described a gate that was never
// built, which is worse than no gate: it reads as enforced, so nobody checks.
//
// It is a TEST and not a runtime branch on purpose. A surface with no declared
// price is not a request to refuse at 3am; it is a composition root somebody
// forgot to finish, and the place to catch that is before it ships. Adding a
// runtime 402 for it would move the cost of the omission onto a customer.
//
// Each plugin/<name>/main.go is its own composition root — the host is thin and
// loads plugins, so there is no single list to walk at runtime. The declaration
// is therefore a source fact, and this reads the source.
func TestPriceDeclared(t *testing.T) {
	roots, err := filepath.Glob(filepath.Join("plugin", "*", "main.go"))
	if err != nil {
		t.Fatalf("glob composition roots: %v", err)
	}
	if len(roots) == 0 {
		t.Fatal("no plugin/*/main.go found — this test is walking the wrong tree, " +
			"which would make it pass vacuously forever")
	}

	var checked int
	for _, root := range roots {
		src, err := os.ReadFile(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), root, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", root, err)
		}

		// The roots are written cloud.Serve([]cloud.Plugin{{Name: …}}), so the
		// element carrying Price is an IMPLICIT literal with a nil Type — only the
		// enclosing slice names cloud.Plugin. Match the slice, then walk into it.
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			arr, ok := lit.Type.(*ast.ArrayType)
			if !ok || !isCloudPlugin(arr.Elt) {
				return true
			}
			for _, elt := range lit.Elts {
				plugin, ok := elt.(*ast.CompositeLit)
				if !ok {
					continue
				}
				checked++
				if !declaresPrice(plugin) {
					t.Errorf("%s: cloud.Plugin declares no Price.\n"+
						"Every surface costs something or is deliberately Free, and the "+
						"zero value is Undeclared — nobody answered. Add Price: "+
						"cloud.Free, cloud.Metered, or a positive number of cents.", root)
				}
			}
			return true
		})
	}

	// A literal that stops matching (a rename, a helper indirection) would make
	// every assertion above silently vacuous. Pin the fact that we saw work.
	if checked == 0 {
		t.Fatalf("walked %d composition roots and matched no cloud.Plugin literal — "+
			"the pattern this test recognises has moved, so it is asserting nothing", len(roots))
	}
	t.Logf("checked %d cloud.Plugin declarations across %d composition roots", checked, len(roots))
}

// isCloudPlugin reports whether an expression names the cloud.Plugin type.
func isCloudPlugin(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Plugin" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "cloud"
}

// declaresPrice reports whether a Plugin literal answers what its surface costs.
// Free and Metered are both answers; only absence is a failure.
func declaresPrice(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Price" {
			return true
		}
	}
	return false
}
