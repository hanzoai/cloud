package testdeadline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// root is this module's directory, two above this package.
const root = "../.."

// Every x.Test(req) in a _test.go file of this module passes a config. One argument is
// the router's one-second default, which a loaded runner outlasts (see doc.go).
func TestEveryAppTestCallStatesItsDeadline(t *testing.T) {
	fset := token.NewFileSet()
	var bare []string
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			// Another module's tests are that module's gate.
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files++
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Test" && len(c.Args) == 1 {
				p := fset.Position(c.Pos())
				rel, _ := filepath.Rel(root, p.Filename)
				bare = append(bare, rel+":"+strconv.Itoa(p.Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 1000 {
		t.Fatalf("read %d test files under %s; the walk is not reading this module", files, root)
	}
	for _, at := range bare {
		t.Errorf("%s: app.Test with no config runs on the router's one-second default; pass zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true} (fiber.TestConfig on a *fiber.App)", at)
	}
}
