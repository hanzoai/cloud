package cloud

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// A flag's third argument is its DEFAULT, and usage prints defaults — on -h and
// on any parse error alike. In a pod that stderr is the log. So no flag may take
// its default from something holding key material: the value would be published
// by a typo. Offering such a flag also invites the value onto a command line,
// where /proc and ps keep it.
func TestNoFlagDefaultsToASecret(t *testing.T) {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "config.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	secret := []string{"masterkey", "secret", "password", "token", "privatekey", "credential"}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 3 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "String") {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "flag" {
			return true
		}
		def := strings.ToLower(render(call.Args[2]))
		for _, s := range secret {
			if strings.Contains(def, s) {
				t.Errorf("%s takes its default from %s — usage would print it",
					render(call.Args[1]), render(call.Args[2]))
			}
		}
		return true
	})
}

func render(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return render(v.X) + "." + v.Sel.Name
	case *ast.BasicLit:
		return v.Value
	case *ast.CallExpr:
		return render(v.Fun) + "()"
	}
	return ""
}
