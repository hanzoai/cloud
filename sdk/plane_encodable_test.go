// Copyright © 2026 Hanzo AI. MIT License.

package cloud_test

// plane_encodable_test.go — a plane type that cannot be ENCODED is an op that is
// unreachable while every other signal says it is up.
//
// zapenc carries scalars, strings, byte slices, structs, pointers and slices, and
// REFUSES anything else at encode so a field can never silently fail to arrive. A
// map on a plane type therefore fails every call inside zip.Call, before the
// socket, with the peer up and the op registered — an outage with no failing
// component. The suite walks KINDS rather than a list of types for that reason.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No type on this plane may carry a kind zapenc refuses, and the rule is TOTAL:
// every type client/peer.go declares is read, so a type added to the plane is
// judged without anyone listing it.
//
// It reads the source because a package's types cannot be enumerated by
// reflection. A field's type is followed through every type the package declares,
// so a named map or a struct that holds one is caught where it is used. A type
// from another package is the standard library's: the plane is a leaf, and the
// import walk below holds it to that.
func TestNoPlaneTypeCarriesAnUnencodableKind(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join("client", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	decls := map[string]ast.Expr{}
	var plane []*ast.TypeSpec
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, spec := range g.Specs {
				ts := spec.(*ast.TypeSpec)
				decls[ts.Name.Name] = ts.Type
				if filepath.Base(path) == "peer.go" {
					plane = append(plane, ts)
				}
			}
		}
	}
	// Counted, for the reason the import walk below counts: a gate that read
	// nothing reads as a gate that passed.
	if len(plane) < 100 {
		t.Fatalf("read %d types in client/peer.go — the plane moved and this gate is blind", len(plane))
	}
	// A plane op's types are not all in peer.go: the commerce peer's live in client/billing.go and
	// others, and a map in one failed every call (the discovery document and the checkout's links
	// each shipped one) with the peer up and the op registered. So every type any call across the
	// plane names as its In or Out is judged too, found where it is CALLED.
	called := calledTypes(t)
	if len(called) < 100 {
		t.Fatalf("found %d plane call sites naming a client type — the call sites moved and this gate is blind", len(called))
	}
	have := map[string]bool{}
	for _, ts := range plane {
		have[ts.Name.Name] = true
	}
	for name := range called {
		if have[name] {
			continue
		}
		if under, ok := decls[name]; ok {
			plane = append(plane, &ast.TypeSpec{Name: ast.NewIdent(name), Type: under})
			have[name] = true
		}
	}
	var bad []string
	for _, ts := range plane {
		if _, ok := ts.Type.(*ast.StructType); !ok {
			continue
		}
		walkSource(&bad, decls, ts.Type, ts.Name.Name, map[string]bool{ts.Name.Name: true})
	}
	seen := map[string]bool{}
	for _, msg := range bad {
		root, _, _ := strings.Cut(msg, ".")
		root, _, _ = strings.Cut(root, " ")
		if _, known := knownUnencodable[root]; known {
			seen[root] = true
			continue
		}
		t.Error(msg)
	}
	// The ledger may only shrink: a type that is fixed is taken off it, or this fails.
	for root, why := range knownUnencodable {
		if !seen[root] {
			t.Errorf("%s is on the known-unencodable ledger (%s) but no longer carries a map: take it off", root, why)
		}
	}
}

// knownUnencodable is the ledger of plane types found carrying a kind zapenc refuses, each a call
// that fails before the socket, named with who owns it so the list can only shrink. A type not
// here that carries a map fails the test, and a type here that no longer does fails it too. It is
// empty: the types the widened gate first found are fixed.
var knownUnencodable = map[string]string{}

// calledTypes is every client type some call site names as the In or Out of a plane call
// (plane.Call[In, Out], cloud.Ask[In, Out]), by name.
func calledTypes(t *testing.T) map[string]bool {
	t.Helper()
	call := regexp.MustCompile(`(?:client\.Call|cloud\.Ask|\bAsk|\bCall)\[([^\[\]]+)\]\(`)
	out := map[string]bool{}
	for _, root := range []string{"client", "apps", "internal", "plugin"} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			for _, m := range call.FindAllStringSubmatch(string(b), -1) {
				for _, arg := range strings.Split(m[1], ",") {
					arg = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(arg), "*"))
					arg = strings.TrimPrefix(arg, "[]")
					if name, ok := strings.CutPrefix(arg, "client."); ok {
						out[name] = true
					} else if !strings.Contains(arg, ".") && arg != "" && arg != "any" && !strings.HasPrefix(arg, "struct") {
						out[arg] = true
					}
				}
			}
			return nil
		})
	}
	return out
}

// walkSource reports every field under expr whose kind zapenc refuses, following
// the package's own named types; seen stops a type that refers to itself.
func walkSource(bad *[]string, decls map[string]ast.Expr, expr ast.Expr, path string, seen map[string]bool) {
	switch e := expr.(type) {
	case *ast.StructType:
		for _, f := range e.Fields.List {
			name := "embedded"
			if len(f.Names) > 0 {
				name = f.Names[0].Name
			}
			walkSource(bad, decls, f.Type, path+"."+name, seen)
		}
	case *ast.StarExpr:
		walkSource(bad, decls, e.X, path, seen)
	case *ast.ArrayType:
		walkSource(bad, decls, e.Elt, path+"[]", seen)
	case *ast.MapType, *ast.InterfaceType, *ast.ChanType, *ast.FuncType:
		*bad = append(*bad, fmt.Sprintf("%s is a %s — zapenc refuses it AT ENCODE, so every call carrying this "+
			"type fails before the socket and the peer looks healthy while the op is unreachable. "+
			"Carry it as a slice of structs ([]client.Field for names and values).", path, kindOf(e)))
	case *ast.Ident:
		if e.Name == "any" {
			*bad = append(*bad, fmt.Sprintf("%s is an interface — zapenc refuses it AT ENCODE. Give it a concrete type.", path))
			return
		}
		if under, ok := decls[e.Name]; ok && !seen[e.Name] {
			seen[e.Name] = true
			walkSource(bad, decls, under, path, seen)
		}
	}
}

func kindOf(e ast.Expr) string {
	switch e.(type) {
	case *ast.MapType:
		return "map"
	case *ast.InterfaceType:
		return "interface"
	case *ast.ChanType:
		return "chan"
	}
	return "func"
}

// The same rule as the walk above, made TOTAL rather than list-driven.
//
// The walk can only inspect the types someone remembered to add to it, and the failure it
// guards against is a type nobody thought about — so the guarantee is taken one level up:
// package plane cannot NAME metering.Usage, because it does not import the package the
// type lives in. That is a property of every plane type at once, including the ones
// written after this test.
//
// It is also the leaf rule the plane already lives by for its own reasons (client/ask.go:
// cloud drags 576 packages, this leaf 75) — apps/metering pulls the finance ledger, its
// SQLite store and the envelope keys behind it. One import would take the money's private
// vocabulary and its whole dependency tree into the package every peer call links.
func TestThePlaneCannotNameTheMetersUsage(t *testing.T) {
	const forbidden = `"github.com/hanzoai/cloud/sdk/meter"`
	fset := token.NewFileSet()
	// Counted, because a gate that reads nothing reads as a gate that passed. This
	// walk named "plane" for a while after the package moved into client/, and a
	// missing directory is the one failure a reviewer reads as "no violations".
	read := 0
	err := filepath.WalkDir("client", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		read++
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == forbidden {
				t.Errorf("%s imports apps/metering — the plane must not be able to name "+
					"metering.Usage, whose Ref is the ledger's key and whose `json:\"-\"` tag "+
					"zapenc does not honour", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk client/: %v", err)
	}
	if read == 0 {
		t.Fatal("read no Go files under client/ — the plane moved again and this gate is blind")
	}
}
