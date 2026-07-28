// Command gen-app-cmds generates one cmd/<app>/main.go per app in apps.Wire()
// so each app builds as its own standalone binary AND still mounts into the
// unified cloud binary. Adding an app in apps.Wire() is the one edit; this
// regenerates its cmd/<app> stub. Run via the go:generate directive in
// apps/apps.go: `go generate ./apps`.
//
// The generated main rebuilds the app's ONE Wire() entry from that entry's own
// source text and imports only the app's own package. It must NOT import
// package apps: apps names every subsystem, so `apps.ServeSingle(name)` links
// all ~68 of them into every per-app binary — 3.2k packages and ~4.5GiB of RSS
// per link, which is what makes a full build OOM. Copying the entry's text
// keeps Wire() the single source of truth (nothing is hand-maintained, and a
// field added there appears here on the next run) while cutting the link down
// to the one app.
//
// An entry the generator cannot express standalone falls back to the old
// apps.ServeSingle stub and says so on stderr, because a fat binary that runs
// beats a lean one that does not compile. Two shapes do that today: a field
// naming a helper defined in package apps (ctxShutdown, mountMetrics — they
// exist nowhere a standalone main can reach), and an entry built by a call
// rather than a literal (cloud.PluginSpec), which gets no generated main at all
// since a plugin app is already its own composition root with a hand-written
// entrypoint.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	// Resolve the repo root from the working directory, so the tool works whether
	// run from the repo root or via `go generate ./apps` (which runs it from apps/).
	root, err := repoRoot()
	if err != nil {
		die(err)
	}
	appsFile := filepath.Join(root, "apps", "apps.go")
	specs := wireSpecs(appsFile)
	if len(specs) == 0 {
		die(fmt.Errorf("no apps found in apps.Wire()"))
	}
	for _, s := range specs {
		if len(s.pluginPrefixes) > 0 {
			continue // already a plugin: it owns a hand-written entrypoint.
		}
		if s.why != "" {
			fmt.Printf("fat stub: %s — %s\n", s.name, s.why)
		}
		src, err := s.render()
		if err != nil {
			die(fmt.Errorf("%s: %w", s.name, err))
		}
		dir := filepath.Join(root, "cmd", s.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			die(err)
		}
		if err := writeIfChanged(filepath.Join(dir, "main.go"), src); err != nil {
			die(err)
		}
	}
	// The same parse feeds the light host's manifest: one read of Wire(), both
	// artifacts, so a per-app binary and the host's knowledge of it cannot drift.
	if err := writeManifest(root, manifest(root, specs, eagerSet(appsFile))); err != nil {
		die(err)
	}
}

// writeIfChanged keeps generation idempotent, so a no-op run leaves the tree
// clean and `-check` in CI means what it says.
func writeIfChanged(path string, src []byte) error {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, src) {
		return nil
	}
	if err := os.WriteFile(path, src, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "gen-app-cmds:", err)
	os.Exit(1)
}

// spec is one Wire() entry captured as SOURCE TEXT: the field list exactly as
// apps.go writes it, plus the import paths its package qualifiers resolve to.
// Capturing text rather than meaning is what keeps this a light parse (no
// type-check): the generator re-emits the composition root's own words into a
// one-app composition root, so the two cannot drift.
type spec struct {
	name    string
	fields  []string        // "Mount: admin.Mount", in source order
	imports map[string]bool // resolved import paths of the qualifiers used above
	why     string          // non-empty: not expressible standalone, emit the fat stub
	// prefixes is the Prefixes field's expression, unevaluated — the composition
	// root's own statement of the subtrees this subsystem owns. The manifest reads
	// it; the generated main copies its text like any other field.
	prefixes ast.Expr
	// mount is the Mount field's expression. The manifest reads the FUNCTION out
	// of it, because two Wire entries can share one package (account) and only
	// the function distinguishes what each of them serves.
	mount ast.Expr
	// pluginPrefixes is set for an entry built by cloud.PluginSpec: the prefixes
	// it hands zip.Load. Such an entry is ALREADY a plugin with its own
	// entrypoint, so it gets a manifest row and no generated main.
	pluginPrefixes []string
}

// wireSpecs parses apps.go and returns one spec per element of Wire()'s returned
// []cloud.MountSpec, in mount order. An element built by a call (cloud.PluginSpec)
// is captured for its name and prefixes only: it is already a plugin and owns its
// entrypoint, so it gets a manifest row and no generated main.
func wireSpecs(file string) []spec {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		die(fmt.Errorf("parse: %w", err))
	}
	paths := importPaths(f)
	var out []spec
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Wire" {
			return true
		}
		ast.Inspect(fn.Body, func(e ast.Node) bool {
			ret, ok := e.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				return true
			}
			lit, ok := ret.Results[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, el := range lit.Elts {
				switch e := el.(type) {
				case *ast.CompositeLit:
					if s := capture(fset, paths, e); s.name != "" {
						out = append(out, s)
					}
				case *ast.CallExpr:
					if s := capturePlugin(e); s.name != "" {
						out = append(out, s)
					}
				}
			}
			return false
		})
		return false
	})
	return out
}

// capture reads one {Name: ..., Mount: ...} literal into a spec.
func capture(fset *token.FileSet, paths map[string]string, cl *ast.CompositeLit) spec {
	s := spec{imports: map[string]bool{}}
	for _, e := range cl.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if key.Name == "Name" {
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			s.name, _ = strconv.Unquote(lit.Value)
		}
		if key.Name == "Prefixes" {
			s.prefixes = kv.Value
		}
		if key.Name == "Mount" {
			s.mount = kv.Value
		}
		text, err := source(fset, kv.Value)
		if err != nil {
			die(err)
		}
		s.fields = append(s.fields, key.Name+": "+text)
		if err := resolve(kv.Value, paths, s.imports); err != nil && s.why == "" {
			s.why = "its " + key.Name + " field " + err.Error()
		}
	}
	return s
}

// capturePlugin reads a cloud.PluginSpec(name, where, prefix...) element. Its
// name and prefixes are written out literally at the call site, so the manifest
// takes them verbatim — nothing about a plugin entry is inferred.
func capturePlugin(call *ast.CallExpr) spec {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "PluginSpec" || len(call.Args) < 3 {
		return spec{}
	}
	name, ok := stringLit(call.Args[0])
	if !ok {
		return spec{}
	}
	s := spec{name: name, imports: map[string]bool{}}
	for _, a := range call.Args[2:] {
		if p, ok := stringLit(a); ok {
			s.pluginPrefixes = append(s.pluginPrefixes, p)
		}
	}
	return s
}

// resolve records the import path behind every package qualifier in e and
// reports the first name it cannot place. A bare identifier that is not
// predeclared is a helper defined in package apps: it is unreachable from a
// standalone main, so the app keeps the fat stub instead of emitting a file
// that will not compile.
func resolve(e ast.Expr, paths map[string]string, into map[string]bool) error {
	var bad error
	ast.Inspect(e, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.SelectorExpr:
			id, ok := t.X.(*ast.Ident)
			if !ok {
				return true // a nested selector; its own idents get checked below
			}
			p, ok := paths[id.Name]
			if !ok {
				if bad == nil {
					bad = fmt.Errorf("qualifies %s, which apps.go does not import under that name", id.Name)
				}
				return false
			}
			into[p] = true
			return false // t.Sel names a member of that package, not a name to resolve
		case *ast.Ident:
			if types.Universe.Lookup(t.Name) == nil && bad == nil {
				bad = fmt.Errorf("references %s, a helper defined in package apps", t.Name)
			}
		}
		return true
	})
	return bad
}

// importPaths maps the qualifier apps.go uses for each import to its path. The
// key is the explicit alias, else the path's last element — which is only ever
// WRONG for a package whose declared name differs from its directory, and there
// the lookup simply misses and the app falls back. It cannot resolve to the
// wrong package.
func importPaths(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(p)
		if im.Name != nil {
			name = im.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		m[name] = p
	}
	return m
}

// source renders an AST node back to the text it was written as, so a Wire
// field lands in the generated main character-for-character.
func source(fset *token.FileSet, n ast.Node) (string, error) {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		return "", err
	}
	return b.String(), nil
}

// render produces the gofmt'd cmd/<app>/main.go for this spec.
func (s spec) render() ([]byte, error) {
	var src string
	if s.why != "" {
		src = fmt.Sprintf(fatStub, s.name, s.why, s.name)
	} else {
		var imports []string
		for p := range s.imports {
			imports = append(imports, p)
		}
		imports = append(imports, cloudPkg)
		sort.Strings(imports)
		var b strings.Builder
		for i, p := range imports {
			if i > 0 && p == imports[i-1] {
				continue
			}
			fmt.Fprintf(&b, "\t%q\n", p)
		}
		src = fmt.Sprintf(leanStub, b.String(), s.name, strings.Join(s.fields, ",\n"), s.name)
	}
	return format.Source([]byte(src))
}

const cloudPkg = "github.com/hanzoai/cloud"

// leanStub serves the app from its own one-entry composition root, linking only
// its own package graph.
const leanStub = `package main

import (
	"fmt"
	"os"

%s)

// Standalone entry for the %s app — generated by cmd/gen-app-cmds (the
// go:generate directive in apps/apps.go). Do not hand-edit: the app is the one
// edit in apps.Wire(), and this binary is regenerated from that entry.
//
// The spec below is apps.Wire()'s own text for this app. It is copied rather
// than reached for because importing package apps to serve ONE app links ALL of
// them — apps names every subsystem — which is 3.2k packages and ~4.5GiB per
// link. The same app still mounts into the unified cloud binary via apps.Wire().
func main() {
	if err := cloud.Serve([]cloud.MountSpec{{
%s,
	}}, []string{%q}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`

// fatStub is the fallback for an entry that cannot be expressed standalone. It
// links the whole app graph; the reason is in the comment so the fix is obvious.
const fatStub = `package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps"
)

// Standalone entry for the %s app — generated by cmd/gen-app-cmds (the
// go:generate directive in apps/apps.go). Do not hand-edit; the app is the one
// edit in apps.Wire(), this binary is regenerated. The same app also mounts into
// the unified cloud binary via apps.Wire().
//
// This one still goes through apps, which links EVERY subsystem, because
// %s.
// Move that helper where a standalone main can name it and the generator emits
// the lean form instead.
func main() {
	if err := apps.ServeSingle(%q); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`

// repoRoot finds the module root by walking up from the working directory for
// a go.mod. It anchors both apps/apps.go (read) and cmd/<app>/ (write) so the
// tool works regardless of the CWD `go generate` hands it (it runs the generator
// from the package dir, apps/ — the executable itself lives under a throwaway
// build cache with no go.mod ancestor, so CWD is the reliable anchor).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
