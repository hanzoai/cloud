package openapi

// Where a product's PROSE comes from.
//
// The document already knows a product's name mechanically — the first path
// segment after /v1/ (Product). What it never had is a sentence saying what the
// product IS, and there is exactly one place that sentence is already written and
// already reviewed: the package doc of the package that implements the app. So
// this reads it rather than asking anyone to write it a second time, which is the
// only version of "one source" that survives contact with 112 apps.
//
// It is computed ONCE, when the app describes itself, and lands in that app's own
// subset as info.description (describe.go). The weave then reads the tag prose off
// the subsets it is already reading — it does not look anything up a second time.
// One producer, one consumer, no second copy of the mapping to fall out of step.

import (
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Synopsis is the one-sentence summary of the package that backs the app whose
// binary lives in dir (a plugin/<name> directory), or "" when there is none.
//
// The OWNER is read from the app's own composition root: plugin/<name>/main.go
// imports exactly the package it mounts, so the import IS the mapping. Nothing
// else could be — four apps are not named after their package (audit → auditlog,
// evals → eval, plugins → plugin), so a name-derived guess would
// be right 107 times and silently wrong 4. (It was wrong 5 while apps/account
// backed a second app, account-bridge; reading the import means a package taking
// on or shedding a mount changes nothing here.) An app whose subsystem is another MODULE (authz,
// licensing, metrics) imports no package here, has no doc comment to read, and
// gets "" — the honest answer, not a fabricated one.
//
// Empty on any failure, deliberately. This is prose: a describe run from outside
// the source tree (a container) still has to produce a document, and a missing
// sentence is a document without a sentence, never a failed projection.
func Synopsis(dir string) string {
	main, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "main.go"), nil, parser.ImportsOnly)
	if err != nil {
		return ""
	}
	root := filepath.Dir(filepath.Dir(dir))
	for _, im := range main.Imports {
		path, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		if src := home(root, path); src != "" {
			return doc.Synopsis(comment(src))
		}
	}
	return ""
}

// home is the directory under root that holds the imported package, or "" when
// the import is not a subsystem of this repo.
//
// ONE home, apps/, since the 131 subsystems moved out of clients/ (f873d1a1) —
// naming a second today would be a branch nothing reaches. It checks the DISK
// rather than the module path, which costs nothing (the directory has to be
// readable anyway) and means an import from another module that happens to carry
// an /apps/ segment resolves to nothing here rather than to a guess.
func home(root, path string) string {
	i := strings.Index(path, "/apps/")
	if i < 0 {
		return ""
	}
	d := filepath.Join(root, path[i+1:])
	if fi, err := os.Stat(d); err == nil && fi.IsDir() {
		return d
	}
	return ""
}

// comment is the package doc comment of the package in dir: the leading comment
// that opens "Package …", Go's own convention for a comment that documents the
// PACKAGE rather than the file it happens to sit in.
//
// The convention is the whole test, and it has to be, because go/doc's fallback
// is wrong here often enough to matter. go/doc takes the first file in filename
// order that carries ANY leading comment, and twenty of these packages open their
// alphabetically-first file with a note about that file — "actions.go — the two
// GitOps write actions" — while stating the real package doc in <name>.go. Under
// the fallback, `deploy` would publish that file note as the product's
// description, which is worse than publishing nothing: a caller cannot tell an
// unwritten description from a misfiled one.
//
// Restated over a package-clause-only parse, so reading one sentence does not
// cost a full type-check of apps/commerce.
func comment(dir string) string {
	ents, err := os.ReadDir(dir) // sorted by filename
	if err != nil {
		return ""
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil || f.Doc == nil {
			continue
		}
		if t := f.Doc.Text(); strings.HasPrefix(t, "Package ") {
			return t
		}
	}
	return ""
}
