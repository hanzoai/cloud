// Package lsp is live semantic code intelligence — definitions, references,
// types, hover and diagnostics — over a repository AND its resolved
// dependencies, served from the cloud with no toolchain on the caller's machine.
//
// # One value
//
// An lsp query is a language server rooted at a workspace, asked about a
// position. Everything on the wire is that value spelled out: WHICH workspace
// (repo, rev), WHICH position (path, line, character), and WHICH question
// (method). There is one door, POST /v1/lsp, because there is one value — eight
// endpoints differing only in a verb would be eight spellings of it.
//
// lsp and apps/code are two reads of the SAME checkout, not two systems: code is
// the static index (lexical, symbolic, semantic — fast, approximate, always
// available), lsp is the live server (exact, typed, resolves through
// dependencies, costs a cold start). An agent uses code to find candidates and
// lsp to be certain.
//
// # Positions are the LSP's, not a translation of them
//
// line and character are 0-BASED, and character counts UTF-16 code units, per the
// LSP specification. That is deliberately not the 1-based line an editor shows a
// human: this door's callers are agents and editors that already speak LSP, and a
// service that silently re-based positions would corrupt every multi-byte line —
// an emoji before the cursor is one UTF-16 unit in the protocol's arithmetic and
// two in Go's. Positions pass through untouched, so the protocol's answer is the
// answer.
//
// # Isolation
//
// Every request resolves its org from the validated principal, and that org is
// BOTH the pool key and the owner segment of the git URL. A caller supplies a
// repo slug, never a URL and never an owner. There is no input from which one
// tenant could name another tenant's repository.
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// diagSettle is how long diagnostics must stay unchanged before the snapshot is
// taken. See Conn.Diagnostics — LSP has no completion signal for them.
const diagSettle = 400 * time.Millisecond

// Query is one position question against one repository.
type Query struct {
	// Repo is the repository NAME within the caller's own org, e.g. "cloud".
	// Not a URL and not an owner/name pair: the owner is the validated
	// principal's org, so this names a repository the caller already owns.
	Repo string `json:"repo"`

	// Rev is a branch, tag or commit sha. Empty means the default branch. A
	// workspace is keyed by revision, so pinning a sha is what makes an answer
	// reproducible.
	Rev string `json:"rev,omitempty"`

	// Path is the repo-relative file, e.g. "apps/lsp/server.go".
	Path string `json:"path"`

	// Line is 0-based, per the LSP specification.
	Line int `json:"line"`

	// Character is a 0-based UTF-16 code-unit offset within Line, per the LSP
	// specification — not a byte offset and not a rune index.
	Character int `json:"character"`

	// Method is the question: hover, definition, references, typeDefinition,
	// implementation, documentSymbol, completion or diagnostics.
	Method string `json:"method"`
}

// Answer carries whichever result the method produces. Exactly one of the result
// fields is populated; the rest are omitted, so a client reads the field its
// method names and never has to discriminate a union.
type Answer struct {
	Method string `json:"method"`
	Repo   string `json:"repo"`
	Rev    string `json:"rev,omitempty"`
	Path   string `json:"path"`
	Lang   string `json:"lang"`

	// Cold reports that this request paid for a workspace cold start — the
	// checkout, the dependency fetch and the server's first index. It is the
	// billed event, surfaced so a caller can see what it was charged for.
	Cold bool `json:"cold"`

	Locations   []Location   `json:"locations,omitempty"`
	Hover       string       `json:"hover,omitempty"`
	Symbols     []Symbol     `json:"symbols,omitempty"`
	Completions []Completion `json:"completions,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// Location is one place in the workspace. Path is repo-relative when the target
// is inside the checkout; for a target in the dependency cache it is the absolute
// path the server reported, which is what makes "definition in a dependency"
// answerable at all.
type Location struct {
	Path  string `json:"path"`
	Range Range  `json:"range"`
}

// Position is the LSP's: 0-based line, 0-based UTF-16 character.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Symbol struct {
	Name   string `json:"name"`
	Kind   int    `json:"kind"`
	Detail string `json:"detail,omitempty"`
	Range  Range  `json:"range"`
}

type Completion struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Diagnostic is one problem the server reported. Severity is the LSP's: 1 error,
// 2 warning, 3 information, 4 hint.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// zipdoc lifts the doc comment off the typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/lsp describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ask resolves one position in one repository through a live language server:
// definition, references, type, implementation, hover, document symbols,
// completion or diagnostics — over the repo AND its resolved dependencies, with
// no toolchain on the caller's machine.
//
// Positions are the LSP's: line and character are 0-BASED and character counts
// UTF-16 code units, so an editor's 1-based line must have 1 subtracted before it
// is sent. The repository is named by slug and is always one in the caller's own
// org. rev pins a branch, tag or commit sha; empty means the default branch.
//
// The first query against a (repo, rev) pays a cold start — checkout, dependency
// fetch and the server's first index — and is the billed event; later queries
// against the same revision are served from the warm workspace and are free. The
// answer says which it was.
//
// Example: {"repo":"cloud","path":"apps/lsp/server.go","line":120,"character":18,"method":"definition"}
func (s *state) ask(ctx context.Context, in *Query) (*Answer, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("valid principal required")
	}
	method := strings.TrimSpace(in.Method)
	if !known(method) {
		return nil, zip.ErrBadRequest("method must be one of " + strings.Join(methods, ", "))
	}
	repo := strings.TrimSpace(in.Repo)
	if !slug.MatchString(repo) {
		return nil, zip.ErrBadRequest("repo must be a repository name in your org")
	}
	rev := strings.TrimSpace(in.Rev)
	if rev != "" && !revision.MatchString(rev) {
		return nil, zip.ErrBadRequest("rev must be a branch, tag or commit sha")
	}
	if in.Line < 0 || in.Character < 0 {
		return nil, zip.ErrBadRequest("line and character are 0-based and cannot be negative")
	}

	// MONEY GATE — before the checkout, so an out-of-funds caller is refused
	// rather than served work nobody can be billed for.
	c, onHTTP := cloud.Request(ctx)
	if onHTTP {
		if err := s.gate(ctx, c, org); err != nil {
			return nil, err
		}
	}

	tree, cold, err := s.pool.get(ctx, key{org: org, repo: repo, rev: rev}, in.Path)
	if err != nil {
		s.Log.Warn("lsp workspace failed", "org", org, "repo", repo, "err", err)
		return nil, zip.ErrInternal("workspace unavailable")
	}

	abs, err := clean(tree.dir, in.Path)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	uri, err := tree.conn.Open(tree.lang, abs)
	if err != nil {
		return nil, zip.ErrInternal("open document")
	}

	out := &Answer{
		Method: method, Repo: repo, Rev: rev,
		Path: in.Path, Lang: tree.lang.Name, Cold: cold,
	}
	if err := s.resolve(ctx, tree, out, uri, in); err != nil {
		s.Log.Warn("lsp query failed", "org", org, "repo", repo, "method", method, "err", err)
		return nil, zip.ErrInternal("language server did not answer")
	}
	if onHTTP {
		s.charge(c, org, method, cold)
	}
	return out, nil
}

// resolve asks the server the one question and folds its reply into out.
//
// diagnostics is the outlier and is handled first: it is not a request at all in
// LSP but an unsolicited notification the server publishes after didOpen, so it
// is collected rather than called.
func (s *state) resolve(ctx context.Context, t *Tree, out *Answer, uri string, in *Query) error {
	ctx, cancel := context.WithTimeout(ctx, callWait)
	defer cancel()

	if in.Method == "diagnostics" {
		out.Diagnostics = t.conn.Diagnostics(ctx, uri, diagSettle)
		if out.Diagnostics == nil {
			out.Diagnostics = []Diagnostic{}
		}
		return nil
	}

	doc := map[string]any{"uri": uri}
	pos := map[string]any{"line": in.Line, "character": in.Character}
	params := map[string]any{"textDocument": doc, "position": pos}

	var call string
	switch in.Method {
	case "hover":
		call = "textDocument/hover"
	case "definition":
		call = "textDocument/definition"
	case "typeDefinition":
		call = "textDocument/typeDefinition"
	case "implementation":
		call = "textDocument/implementation"
	case "completion":
		call = "textDocument/completion"
	case "references":
		call = "textDocument/references"
		params["context"] = map[string]any{"includeDeclaration": true}
	case "documentSymbol":
		call = "textDocument/documentSymbol"
		params = map[string]any{"textDocument": doc} // no position: the whole file
	default:
		return fmt.Errorf("unroutable method %q", in.Method) // known() already refused this
	}

	raw, err := t.conn.Call(ctx, call, params)
	if err != nil {
		return err
	}
	fold(out, in.Method, raw, t.dir)
	return nil
}

// fold decodes the server's result into the field the method names.
//
// A null result is not an error: "no definition here" is a real, useful answer,
// and it arrives as JSON null. Every branch therefore leaves out's slice empty
// rather than failing, so a caller distinguishes "nothing found" from "the server
// broke" by status code and not by guesswork.
func fold(out *Answer, method string, raw json.RawMessage, dir string) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	switch method {
	case "hover":
		out.Hover = hover(raw)
	case "documentSymbol":
		out.Symbols = symbols(raw)
	case "completion":
		out.Completions = completions(raw)
	default: // every location-shaped method
		out.Locations = locations(raw, dir)
	}
}

// locations decodes the three shapes a location-returning request may answer with
// — a single Location, an array of them, or an array of LocationLink (the
// linkSupport form, whose target range lives under a different key). All three
// are in the specification and gopls, rust-analyzer and tsserver do not agree on
// which to send, so all three are read.
func locations(raw json.RawMessage, dir string) []Location {
	var many []struct {
		URI       string `json:"uri"`
		Range     Range  `json:"range"`
		TargetURI string `json:"targetUri"`
		Target    Range  `json:"targetSelectionRange"`
	}
	if json.Unmarshal(raw, &many) != nil {
		var one struct {
			URI   string `json:"uri"`
			Range Range  `json:"range"`
		}
		if json.Unmarshal(raw, &one) != nil || one.URI == "" {
			return []Location{}
		}
		return []Location{{Path: rel(uriPath(one.URI), dir), Range: one.Range}}
	}

	out := make([]Location, 0, len(many))
	for _, m := range many {
		uri, rng := m.URI, m.Range
		if uri == "" { // a LocationLink
			uri, rng = m.TargetURI, m.Target
		}
		p := uriPath(uri)
		if p == "" {
			continue // a jar:/zipfile: target names no path of ours
		}
		out = append(out, Location{Path: rel(p, dir), Range: rng})
	}
	return out
}

// rel renders a path repo-relative when it is inside the checkout. A path OUTSIDE
// it — a definition in the module cache — is returned as the server gave it,
// because that is a real location and pretending otherwise would lose it.
//
// The checkout is matched in BOTH spellings, resolved and raw. A language server
// reports paths as the OS handed them to it, and a data directory reached through
// a symlink — /var → /private/var on a Mac, a mounted volume in the cluster —
// gives one file two spellings. Comparing one resolved path against one raw one
// puts every location "outside" the checkout, and the fallback then hands the
// caller the worker's ABSOLUTE path for files that were in their own repo all
// along.
//
// This is presentation, not the security boundary: [clean] is what proves a
// requested path is inside the tree, and it resolves symlinks precisely because
// it has to.
func rel(abs, dir string) string {
	if abs == "" {
		return ""
	}
	roots := []string{dir}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		roots = append(roots, resolved)
	}
	for _, root := range roots {
		if r, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return abs
}

// hover decodes MarkupContent, a MarkedString, or an array of either.
func hover(raw json.RawMessage) string {
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &h) != nil || len(h.Contents) == 0 {
		return ""
	}
	var markup struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(h.Contents, &markup) == nil && markup.Value != "" {
		return markup.Value
	}
	var plain string
	if json.Unmarshal(h.Contents, &plain) == nil {
		return plain
	}
	var list []json.RawMessage
	if json.Unmarshal(h.Contents, &list) != nil {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		if json.Unmarshal(item, &markup) == nil && markup.Value != "" {
			parts = append(parts, markup.Value)
			continue
		}
		if json.Unmarshal(item, &plain) == nil && plain != "" {
			parts = append(parts, plain)
		}
	}
	return strings.Join(parts, "\n\n")
}

func symbols(raw json.RawMessage) []Symbol {
	var list []struct {
		Name     string `json:"name"`
		Kind     int    `json:"kind"`
		Detail   string `json:"detail"`
		Range    Range  `json:"range"`
		Location struct {
			Range Range `json:"range"`
		} `json:"location"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return []Symbol{}
	}
	out := make([]Symbol, 0, len(list))
	for _, s := range list {
		rng := s.Range
		if rng == (Range{}) { // SymbolInformation carries it under location
			rng = s.Location.Range
		}
		out = append(out, Symbol{Name: s.Name, Kind: s.Kind, Detail: s.Detail, Range: rng})
	}
	return out
}

// completions decodes CompletionList or a bare CompletionItem array, and bounds
// the reply: a server offering every identifier in a large dependency tree can
// answer with tens of thousands of items, which is not an answer anybody reads.
func completions(raw json.RawMessage) []Completion {
	const maxItems = 200
	type item struct {
		Label  string `json:"label"`
		Kind   int    `json:"kind"`
		Detail string `json:"detail"`
	}
	var list struct {
		Items []item `json:"items"`
	}
	var items []item
	if json.Unmarshal(raw, &list) == nil && list.Items != nil {
		items = list.Items // CompletionList
	} else if json.Unmarshal(raw, &items) != nil {
		return []Completion{} // neither shape
	}
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	out := make([]Completion, 0, len(items))
	for _, i := range items {
		out = append(out, Completion{Label: i.Label, Kind: i.Kind, Detail: i.Detail})
	}
	return out
}
