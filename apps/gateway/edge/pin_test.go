package edge

// Pins. Each of these asserts a property of the SOURCE, because each is a defect
// that came back by being re-spelled somewhere else after it was fixed in one
// place. A behavioural test catches the instance; these catch the class.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bounded table is the only map in this package that may hold per-caller state,
// and it can only be built by newTenant — which supplies the ceiling, the
// per-entry cost and the process budget. A map literal built anywhere else is an
// unbounded one wearing the same shape, which is exactly how the shared,
// globally-capped table this design replaced came to exist.
func TestPin_EveryTableIsBuiltByOneConstructor(t *testing.T) {
	for file, src := range sources(t) {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		ast.Inspect(src, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if name := callName(call); name == "newTable" || name == "newTable[V]" {
				if fn := enclosing(src, call.Pos()); fn != "newTenant" {
					t.Errorf("%s: newTable is called from %s; the only constructor is newTenant", file, fn)
				}
			}
			return true
		})
	}
}

// The reclaim rule has ONE exit that deletes anything, and it deletes only what
// is dead. sort was the machinery of the second pass — "drop the oldest half" —
// which is what turned a memory bound into a way to evict a live caller, release
// a held verdict, and erase a neighbour's counts. If sorting reappears in the
// reclaim path, so has that pass.
func TestPin_ReclaimHasNoSecondPass(t *testing.T) {
	src, err := os.ReadFile("traffic.go")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := function(string(src), "func (t *table[V]) reclaim(")
	if !ok {
		t.Fatal("table.reclaim is gone; the reclaim policy moved and this pin did not")
	}
	for _, banned := range []string{"sort.", "len(all)/2", "pinned("} {
		if strings.Contains(body, banned) {
			t.Errorf("table.reclaim contains %q: the drop-the-oldest pass is back", banned)
		}
	}
	if strings.Count(body, "delete(") != 1 {
		t.Errorf("table.reclaim deletes from %d places; the rule is one place, dead keys only", strings.Count(body, "delete("))
	}
	if !strings.Contains(body, "!v.live(now)") {
		t.Error("table.reclaim no longer tests liveness before deleting")
	}
}

// A caller key is derived from ONE function, and that function may not read the
// credential a request merely PRESENTED. Signal.Presented is the value the caller
// picks; the day it reaches a key, holds become evadable and the table becomes
// mintable — which is the defect this whole file was rewritten for.
func TestPin_TheCallerKeyCannotSeeAPresentedCredential(t *testing.T) {
	src, err := os.ReadFile("traffic.go")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := function(string(src), "func callerKey(")
	if !ok {
		t.Fatal("callerKey is gone; the key derivation moved and this pin did not")
	}
	if strings.Contains(body, "Presented") {
		t.Error("callerKey reads Signal.Presented — a caller may not choose its own key")
	}
	// And nowhere else derives one.
	for file, text := range texts(t) {
		if strings.HasSuffix(file, "_test.go") || file == "traffic.go" {
			continue
		}
		if strings.Contains(text, `"cred:"`) || strings.Contains(text, `"ip:"`) {
			t.Errorf("%s spells a caller key; there is one derivation, in callerKey", file)
		}
	}
}

// Every ceiling in this package is charged in BYTES against the one budget. A cap
// on the NUMBER of entries is not a bound when the values behind them are not
// bounded, so a table that admits without a cost is a table with no ceiling.
func TestPin_EveryCeilingIsChargedInBytes(t *testing.T) {
	src, err := os.ReadFile("traffic.go")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := function(string(src), "func (t *table[V]) admit(")
	if !ok {
		t.Fatal("table.admit is gone; admission moved and this pin did not")
	}
	if !strings.Contains(body, "t.budget.take(t.cost)") {
		t.Error("table.admit no longer charges the budget: the byte bound is not enforced at admission")
	}
	if !strings.Contains(body, "t.refused.bump(") {
		t.Error("table.admit no longer counts a refusal: a bound that binds silently is banned")
	}
}

func sources(t *testing.T) map[string]*ast.File {
	t.Helper()
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = f
	}
	return out
}

func texts(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.IndexExpr: // newTable[*caller](...)
		if id, ok := fn.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

func enclosing(f *ast.File, pos token.Pos) string {
	name := "(file scope)"
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Pos() <= pos && pos <= fn.End() {
			name = fn.Name.Name
		}
	}
	return name
}

// function returns the body text of the declaration starting with head, from its
// opening brace to the first line that closes it at column zero.
func function(src, head string) (string, bool) {
	i := strings.Index(src, head)
	if i < 0 {
		return "", false
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		return rest[:j], true
	}
	return rest, true
}
