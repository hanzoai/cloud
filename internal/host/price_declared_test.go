package host

import (
	"go/ast"
)

// isCloudPlugin reports whether an expression names the cloud.Plugin type.
func isCloudPlugin(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Plugin" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "cloud"
}
