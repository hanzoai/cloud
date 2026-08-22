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
// subset as info.description (describe.go). The compose then reads the tag prose off
// the subsets it is already reading — it does not look anything up a second time.
// One producer, one consumer, no second copy of the mapping to fall out of step.

import (
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
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
// on or shedding a mount changes nothing here.)
//
// A subsystem that lives in ANOTHER MODULE — authz, licensing — is read the SAME
// way, from the module cache at the version this repo pins (home). One mechanism
// for both, so where a subsystem lives decides which directory is read and
// nothing else: an app moving out of the tree keeps its sentence, and an app
// arriving from outside gets one. What it costs is a `go list` per such import,
// which stdlib and this module's own imports never reach.
//
// The first import that DOCUMENTS ITSELF wins, not merely the first that
// resolves. An app can import two packages of one subsystem — licensing's front
// door, and the type its host's adapter has to name — and only one of them says
// what the product is.
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
		if c := comment(home(root, path)); c != "" {
			return doc.Synopsis(c)
		}
	}
	return ""
}

// home is the directory that holds the imported package: under root when the
// package is a subsystem of THIS repo, in the module cache when it is one from
// another of the org's modules, "" when the import is neither.
//
// Two homes because a subsystem has two places to live, not because there are two
// kinds of subsystem. In-repo it is apps/, one place since the 131 subsystems
// moved out of clients/ (f873d1a1). It checks the DISK rather than the module
// path, which costs nothing (the directory has to be readable anyway) and means
// an import from another module that happens to carry an /apps/ segment falls
// through to the module cache rather than resolving to a guess.
func home(root, path string) string {
	if i := strings.Index(path, "/apps/"); i >= 0 {
		d := filepath.Join(root, path[i+1:])
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return pinned(root, path)
}

// The org's own import prefix, and this module within it.
//
// A subsystem is OURS — in this repo or in a sibling repo — which is the whole
// test, and it has to be: plugin/authz's main also links luxfi/log, whose package
// doc is a perfectly good sentence about a logging library and no answer at all
// to what /v1/authz is.
//
// This module is excluded because an app links the host in order to BE served,
// never as the thing it serves. Without that, metrics — which mounts a function
// of cloud's own and imports nothing else of ours — would publish "Package cloud
// is the unified Hanzo Cloud binary" as its description: the fleet's identity,
// claimed as one product's.
const (
	org  = "github.com/hanzoai/"
	self = org + "cloud"
)

// pinned is the module cache directory of a package from another of the org's
// modules, at the version root's go.mod pins, or "" for any other import.
//
// The go tool resolves it, because the cache path is a function of the module
// path, the version and the escaping of both, and `go list` already computes
// exactly that — including a go.mod replace, which is the whole point of asking
// the build rather than doing the arithmetic. GOPROXY=off keeps it a cache read:
// describing an app reaches no network, so a version nobody downloaded is absent
// rather than slow. GOWORK=off because the PIN is what ships — a developer's
// workspace replacement would otherwise describe their checkout in the document
// everyone reads.
func pinned(root, path string) string {
	if !strings.HasPrefix(path, org) || path == self || strings.HasPrefix(path, self+"/") {
		return ""
	}
	cmd := exec.Command("go", "list", "-e", "-f", "{{.Dir}}", path)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
