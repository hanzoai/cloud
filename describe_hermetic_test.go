package cloud

// THE PUBLISHED ARTIFACT IS A FUNCTION OF THE CODE ALONE — checked, not asserted.
//
// [SpecConfig] is careful: it hands the describe run a *Config with a brand, a
// domain and a throwaway data dir, and zero values everywhere else, and its doc
// comment explains why — "if this read the environment, the artifacts two
// developers generated from one commit would differ by whichever CLOUD_*
// variables their shells carried". That is exactly right, and it was the whole
// of the defence.
//
// It is not sufficient, because *Config is not the only way into the process
// environment. Measured on the tree this test ships with: 372 os.Getenv /
// os.LookupEnv calls across apps/ and clients/, every one of them reachable from
// a Mount and none of them passing through SpecConfig. A route registered under
// one of those is a route the document publishes or omits for a reason the code
// alone does not decide, and the two directions fail differently:
//
//	registered when the var is SET    the describe run omits it, so an address
//	                                  production serves is undescribed — a hole
//	registered when the var is EMPTY  the describe run PUBLISHES it and
//	                                  production never registers it — a 404 on a
//	                                  published address, in every SDK, the MCP
//	                                  tool list and the CLI at once
//
// The second is the lie this whole gate family exists to make impossible, and it
// is the one cause of it that openapi/reach.py cannot see until after a release
// has shipped: reach asks production, and production is where the damage already
// is. This asks the source, before the artifact is written.
//
// TODAY THE COUNT IS ZERO, and that is the point of writing it down. The document
// is honest about this by accident, not by construction — nothing stopped the
// 373rd env read from being the one inside an `if` around a route. Now something
// does.
//
// WHAT IT CANNOT SEE, stated so nobody mistakes green here for a proof: a guard
// whose value reaches it through a helper function or a struct field set
// elsewhere. It catches the direct shape, which is the shape people write.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// routeVerbs are the method names that register an address on a router — zip's,
// fiber's and the two foreign routers cloud still mounts (beego's Router, net/http's
// Handle). A call is only a registration if one of its first two arguments is a
// string literal containing "/", which is what separates app.Get("/v1/x", h) from
// client.Get(ctx, id).
var routeVerbs = map[string]bool{
	"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true,
	"All": true, "Head": true, "Options": true, "Router": true, "Handle": true,
}

func isRouteRegistration(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !routeVerbs[sel.Sel.Name] {
		return false
	}
	for i, arg := range call.Args {
		if i > 1 {
			break
		}
		if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "/") {
			return true
		}
	}
	return false
}

// readsEnv reports whether n reads the process environment, directly or through a
// local already known to hold an environment value.
func readsEnv(n ast.Node, tainted map[string]bool) (bool, string) {
	var found, why = false, ""
	ast.Inspect(n, func(x ast.Node) bool {
		switch v := x.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "os" &&
				(v.Sel.Name == "Getenv" || v.Sel.Name == "LookupEnv") {
				found, why = true, "os."+v.Sel.Name
			}
		case *ast.Ident:
			if tainted[v.Name] {
				found, why = true, "the environment, through "+v.Name
			}
		}
		return true
	})
	return found, why
}

func TestPublishedRoutesDoNotDependOnTheEnvironment(t *testing.T) {
	roots := []string{"apps", "clients"}
	var offences []string

	for _, root := range roots {
		err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil // not this gate's job to police syntax
			}
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				// Locals holding an environment value. Iterated to a fixpoint so a
				// value copied through a chain of assignments stays visible.
				tainted := map[string]bool{}
				for range 4 {
					ast.Inspect(fn.Body, func(x ast.Node) bool {
						as, ok := x.(*ast.AssignStmt)
						if !ok {
							return true
						}
						for _, rhs := range as.Rhs {
							if env, _ := readsEnv(rhs, tainted); env {
								for _, lhs := range as.Lhs {
									if id, ok := lhs.(*ast.Ident); ok {
										tainted[id.Name] = true
									}
								}
							}
						}
						return true
					})
				}
				ast.Inspect(fn.Body, func(x ast.Node) bool {
					var cond, body ast.Node
					switch v := x.(type) {
					case *ast.IfStmt:
						cond, body = v.Cond, v.Body
						if v.Init != nil {
							if env, _ := readsEnv(v.Init, tainted); env {
								cond = v.Init
							}
						}
					case *ast.SwitchStmt:
						if v.Tag == nil {
							return true
						}
						cond, body = v.Tag, v.Body
					default:
						return true
					}
					env, why := readsEnv(cond, tainted)
					if !env {
						return true
					}
					registers := false
					ast.Inspect(body, func(y ast.Node) bool {
						if isRouteRegistration(y) {
							registers = true
						}
						return true
					})
					if registers {
						offences = append(offences, path+":"+
							itoa(fset.Position(cond.Pos()).Line)+" — a route is registered only when this reads "+why)
					}
					return true
				})
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	for _, o := range offences {
		t.Error(o)
	}
	if len(offences) > 0 {
		t.Log("A describe run mounts with SpecConfig — brand, domain, throwaway data dir, " +
			"zero everywhere else — so every env read above returns \"\" there and whatever " +
			"the deployment sets in production. The address is published or omitted on a " +
			"difference the document cannot express. Register the route unconditionally and " +
			"let the HANDLER answer when the dependency is missing: an address that answers " +
			"503 is a truthful document plus an operational problem, and an address that " +
			"does not exist is a lie in eight SDKs.")
	}
}

// itoa avoids pulling strconv in for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
