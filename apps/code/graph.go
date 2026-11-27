// Copyright © 2026 Hanzo AI. MIT License.

package code

// graph.go — what a pushed tree states about its code, filed into the org's graph.
//
// The index answers questions about code; the graph answers how things relate,
// and since when. A push hands both the same tree. This reads four relations off
// it — a module depends_on a module, a file defines a symbol, an owner is
// owner_of a path, a decision record decides — and restates them to the graph as
// the whole of what the repo says at the commit (client.GraphRestate): what the
// commit no longer states ends there, and reading the same commit again files
// nothing.
//
// Every key is code:{project}/{repo}#{path}, and ::{symbol} for a definition —
// code:{repo}#{path} for an org-level repo. The parser is this plane's, which is
// why the op is.

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zap-proto/zip"
	"golang.org/x/mod/modfile"

	"github.com/hanzoai/cloud/client"
	graphpeer "github.com/hanzoai/cloud/client/graph"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
)

// The relations a tree states. A restatement is whole about exactly these, so an
// assertion anybody files about the repo under another relation is never ended
// by a push.
const (
	dependsOn = "depends_on"
	defines   = "defines"
	ownerOf   = "owner_of"
	decides   = "decides"
)

var relations = []string{dependsOn, defines, ownerOf, decides}

// valueMax is the most one graph value holds, in bytes.
const valueMax = 2048

// exposeGraph publishes the op. Use calls it.
func exposeGraph() {
	cloud.Plane().Post("/code/graph", planeGraph,
		zip.WithOperationID(client.CodeGraph),
		zip.WithSummary("File what a pushed tree states about its code into the graph"))
}

// planeGraph restates one commit's tree into the graph of the org and project
// the call acts for. The org is the CALLER's plane identity, never the argument.
//
// A deployment that hosts no graph answers an EMPTY restatement, as one that
// hosts no index answers an empty reconcile: it is not a fault in the push.
func planeGraph(ctx context.Context, in *client.Tree) (*client.Restated, error) {
	if _, ok := cloud.Tenant(ctx); !ok {
		return nil, zip.ErrForbidden("code graph: org required")
	}
	if in.Repo == "" || in.At == "" || in.Source == "" {
		return nil, zip.ErrBadRequest("code graph: repo, at and source are required")
	}
	scope := "code:" + path.Join(principal.ProjectScopeFrom(ctx), in.Repo) + "#"
	stated, whole := statements(scope, in.Files)
	if !whole {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge,
			"code graph: the tree states more than %d statements, the most one restatement files", client.RestateMax)
	}
	out, err := graphpeer.GraphRestate(ctx, &client.Restate{
		Scope:     scope,
		Relations: relations,
		At:        in.At,
		Source:    in.Source,
		Stated:    stated,
	})
	if errors.Is(err, plane.ErrNoPeer) {
		return &client.Restated{}, nil
	}
	return out, err
}

// statements is everything a tree states, keyed under scope, and false once it
// states more than one restatement files. It stops there: a tree's size is its
// pusher's to choose, and one file can define thousands of symbols.
func statements(scope string, files []client.IndexFile) ([]client.Stated, bool) {
	var out []client.Stated
	add := func(s client.Stated) bool {
		out = append(out, s)
		return len(out) <= client.RestateMax
	}
	if !definitions(scope, files, add) || !dependencies(scope, files, add) ||
		!owners(scope, files, add) || !decisions(scope, files, add) {
		return nil, false
	}
	return out, true
}

// definitions: a file defines each symbol the parser finds in it. A method is
// named by its receiver, Type.Method, so two types' methods of one name are two
// symbols. Go the go tool does not build — vendored modules, testdata — defines
// nothing of this repo's.
func definitions(scope string, files []client.IndexFile, add func(client.Stated) bool) bool {
	for _, f := range files {
		if lang := langOf(f.Path); lang == "text" || (lang == "go" && !buildable(f.Path)) {
			continue
		}
		file := scope + f.Path
		for _, s := range Parse(f.Path, f.Content).Symbols {
			name := s.Name
			if s.Scope != "" {
				name = s.Scope + "." + name
			}
			if !add(client.Stated{Entity: file, Relation: defines, Value: file + "::" + name, Names: true, Evidence: f.Path}) {
				return false
			}
		}
	}
	return true
}

// dependencies: a module depends on each module of the same repo it imports. A
// Go package is its directory, reached through the go.mod naming its module
// path; a JS or TS module is its file, reached from a relative specifier the way
// the bundlers resolve one. An import that leaves the repo names nothing this
// repo keys, and is not stated.
func dependencies(scope string, files []client.IndexFile, add func(client.Stated) bool) bool {
	have := map[string]bool{}
	pkgs := map[string]bool{}
	mods := map[string]string{} // module path → the directory of its go.mod
	for _, f := range files {
		have[f.Path] = true
		switch {
		case path.Base(f.Path) == "go.mod":
			if m := modfile.ModulePath([]byte(f.Content)); m != "" {
				mods[m] = path.Dir(f.Path)
			}
		case langOf(f.Path) == "go" && buildable(f.Path):
			pkgs[path.Dir(f.Path)] = true
		}
	}
	for _, f := range files {
		var from string
		var to []string
		switch langOf(f.Path) {
		case "go":
			if !buildable(f.Path) {
				continue
			}
			from, to = path.Dir(f.Path), goImports(f, mods, pkgs)
		case "ts", "js":
			from, to = f.Path, jsImports(f, have)
		}
		for _, t := range to {
			if t != from && !add(client.Stated{Entity: scope + from, Relation: dependsOn, Value: scope + t, Names: true, Evidence: f.Path}) {
				return false
			}
		}
	}
	return true
}

// buildable reports whether the go tool builds a file: not under vendor or
// testdata, nor a directory whose name starts with . or _.
func buildable(p string) bool {
	for d := range strings.SplitSeq(path.Dir(p), "/") {
		if d == "vendor" || d == "testdata" || (d != "." && (strings.HasPrefix(d, ".") || strings.HasPrefix(d, "_"))) {
			return false
		}
	}
	return true
}

// goImports is the repo directories a Go file imports.
func goImports(f client.IndexFile, mods map[string]string, pkgs map[string]bool) []string {
	syntax, err := parser.ParseFile(token.NewFileSet(), f.Path, f.Content, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	var out []string
	for _, im := range syntax.Imports {
		ip, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		if dir, ok := goDir(ip, mods); ok && pkgs[dir] {
			out = append(out, dir)
		}
	}
	return out
}

// goDir maps an import path to a directory of the repo through the module whose
// path is its longest prefix, which is how the go tool picks among nested ones.
func goDir(ip string, mods map[string]string) (string, bool) {
	best := ""
	for m := range mods {
		if (ip == m || strings.HasPrefix(ip, m+"/")) && len(m) > len(best) {
			best = m
		}
	}
	if best == "" {
		return "", false
	}
	return path.Join(mods[best], strings.TrimPrefix(ip, best)), true
}

// jsSpecifier is a relative module specifier after from, import or require.
var jsSpecifier = regexp.MustCompile(`(?:\bfrom|\bimport|\brequire)\s*\(?\s*["'](\.{1,2}/[^"'\n]+)["']`)

// jsExts are the extensions a specifier may omit, in the order they are tried.
var jsExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// jsImports is the repo files a JS or TS file imports.
func jsImports(f client.IndexFile, have map[string]bool) []string {
	var out []string
	for _, m := range jsSpecifier.FindAllStringSubmatch(f.Content, -1) {
		if p, ok := jsFile(f.Path, m[1], have); ok {
			out = append(out, p)
		}
	}
	return out
}

// jsFile resolves a relative specifier: the file itself, the file with an
// extension, or the directory's index. A specifier written with .js for a
// TypeScript source resolves to the source.
func jsFile(from, spec string, have map[string]bool) (string, bool) {
	base := path.Join(path.Dir(from), spec)
	if have[base] {
		return base, true
	}
	stems := []string{base}
	if e := path.Ext(base); slices.Contains(jsExts, e) {
		stems = append(stems, strings.TrimSuffix(base, e))
	}
	for _, s := range stems {
		for _, e := range jsExts {
			if have[s+e] {
				return s + e, true
			}
		}
		for _, e := range jsExts {
			if have[s+"/index"+e] {
				return s + "/index" + e, true
			}
		}
	}
	return "", false
}

// codeowners are where a forge looks for the file, in the order it looks. The
// first one found is the repo's.
var codeowners = []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"}

// owners: each owner a CODEOWNERS line names is owner_of the path its pattern
// names. The pattern is kept as written less its anchoring slashes, so a glob
// stays a glob.
func owners(scope string, files []client.IndexFile, add func(client.Stated) bool) bool {
	for _, where := range codeowners {
		i := slices.IndexFunc(files, func(f client.IndexFile) bool { return f.Path == where })
		if i < 0 {
			continue
		}
		for line := range strings.Lines(files[i].Content) {
			fields := strings.Fields(line)
			if c := slices.IndexFunc(fields, func(f string) bool { return strings.HasPrefix(f, "#") }); c >= 0 {
				fields = fields[:c]
			}
			if len(fields) < 2 || strings.HasPrefix(fields[0], "[") || strings.HasPrefix(fields[0], "^[") {
				continue
			}
			target := strings.Trim(fields[0], "/")
			if target == "" {
				target = "."
			}
			for _, o := range fields[1:] {
				if !add(client.Stated{Entity: o, Relation: ownerOf, Value: scope + target, Names: true, Evidence: where}) {
					return false
				}
			}
		}
		return true
	}
	return true
}

// decisionDirs are the directory names decision records are kept under.
var decisionDirs = []string{"adr", "adrs", "decisions", "decision"}

// decisions: a decision record decides what its Decision section says.
func decisions(scope string, files []client.IndexFile, add func(client.Stated) bool) bool {
	for _, f := range files {
		if !record(f.Path) {
			continue
		}
		if d := decided(f.Content); d != "" && !add(client.Stated{Entity: scope + f.Path, Relation: decides, Value: d, Evidence: f.Path}) {
			return false
		}
	}
	return true
}

// record reports whether a path is a decision record: markdown under a
// directory named for them, or named adr-* itself, and not an index or a
// template.
func record(p string) bool {
	base := strings.ToLower(path.Base(p))
	if path.Ext(base) != ".md" || base == "readme.md" || base == "index.md" || strings.Contains(base, "template") {
		return false
	}
	if strings.HasPrefix(base, "adr-") || strings.HasPrefix(base, "adr_") {
		return true
	}
	for d := range strings.SplitSeq(strings.ToLower(path.Dir(p)), "/") {
		if slices.Contains(decisionDirs, d) {
			return true
		}
	}
	return false
}

// decided is what a record decides: the text under its Decision heading — the
// section Nygard's form, adr-tools and MADR ("Decision Outcome") all write —
// else its title. It is one value, so it is cut to fit one.
func decided(md string) string {
	var title string
	var body []string
	in, fenced := false, false
	for line := range strings.Lines(md) {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
		}
		if level, h := heading(t); level > 0 && !fenced {
			if title == "" {
				title = h
			}
			if in {
				break
			}
			in = level > 1 && strings.HasPrefix(strings.ToLower(h), "decision")
			continue
		}
		if in {
			body = append(body, t)
		}
	}
	text := strings.Join(strings.Fields(strings.Join(body, " ")), " ")
	if text == "" {
		text = title
	}
	return clip(text, valueMax)
}

// heading reads an ATX heading: its level and its text, or level 0.
func heading(t string) (int, string) {
	h := strings.TrimLeft(t, "#")
	level := len(t) - len(h)
	if level == 0 || level > 6 || (h != "" && h[0] != ' ') {
		return 0, ""
	}
	return level, strings.TrimSpace(h)
}

// clip cuts s to at most n bytes without splitting a rune.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
