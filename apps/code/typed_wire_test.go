package code

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// This file is the MEASUREMENT that typing /v1/code did not move its wire. All
// seven operations are typed ops — untypedByDesign is deliberately EMPTY — and
// before this pass every one of them published no summary and no description,
// which is exactly the set that projects to NOTHING: no prose, no MCP tool, no
// CLI command, no typed SDK method. On THIS surface that mattered more than
// most: /v1/code exists to be driven by coding agents, and an MCP tool with no
// description is a tool no model will pick.

// untypedByDesign is the closed list of code operations that are NOT typed ops.
// It is empty, and TestEveryRouteIsTypedOrNamed is what keeps it that way.
var untypedByDesign = map[string]string{}

// codeOps reads BOTH projections of the LIVE router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. newTestApp calls the REAL routes(), so this reads what the
// binary registers rather than a reconstruction of it.
func codeOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app, _ := newTestApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "code", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/code") }
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a code operation is neither a typed op
// nor one named in untypedByDesign. The two ledgers must SUM to the served
// surface, so a route added untyped here goes red without anyone remembering.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := codeOps(t)
	if len(served) != 7 {
		t.Fatalf("code serves %d operations, not the 7 these ledgers know: %s", len(served), sortedOps(served))
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no prose, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (zip.Get/Post/... in routes()), or add it to untypedByDesign with the "+
			"wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the prose actually reached the registry.
// zipdoc lifts a handler's doc comment into zipdoc_gen.go at BUILD time, so a
// package that loses its //go:generate directive keeps compiling perfectly while
// every one of its operations goes back to publishing nothing at all.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := codeOps(t)
	if len(typed) != 7 {
		t.Fatalf("the registry carries %d operations, want 7", len(typed))
	}
	for key, op := range typed {
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s publishes NO description — the OpenAPI prose and the MCP tool description are both empty", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s publishes NO summary — the CLI command help and every SDK docstring are empty", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s has a line break in its one-line summary: %q", key, op.Summary)
		}
	}
}

// TestAskKeepsBodyOverQueryPrecedence is the reason askPostIn spells its two
// halves separately. This route has always read `?q=` FIRST and let a non-empty
// body `query` override it — the OPPOSITE of zip's binding order, which fills a
// field from the body and then lets the query overwrite it. One field per source
// (`json:"-" url:"q"` and `json:"query" url:"-"`) is what reproduces the original
// precedence instead of inverting it. All four combinations are asserted.
func TestAskKeepsBodyOverQueryPrecedence(t *testing.T) {
	app, _ := newTestApp(t)
	index(t, app)
	ask := func(path string, body any) AskAnswer {
		t.Helper()
		code, raw := doAuth(t, app, http.MethodPost, path, "acme", body)
		if code != http.StatusOK {
			t.Fatalf("POST %s want 200, got %d (%s)", path, code, raw)
		}
		var a AskAnswer
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return a
	}
	// Query only — the GET form's inputs, on the POST route.
	if got := ask("/v1/code/ask?q=from+the+query", map[string]any{}); got.Question != "from the query" {
		t.Errorf("query-only ask asked %q", got.Question)
	}
	// Body only.
	if got := ask("/v1/code/ask", map[string]any{"query": "from the body"}); got.Question != "from the body" {
		t.Errorf("body-only ask asked %q", got.Question)
	}
	// BOTH: the body wins, as it always has.
	if got := ask("/v1/code/ask?q=from+the+query", map[string]any{"query": "from the body"}); got.Question != "from the body" {
		t.Errorf("with both present the ask asked %q — the body no longer wins", got.Question)
	}
	// Neither: still a 400, not a silent empty search.
	if code, raw := doAuth(t, app, http.MethodPost, "/v1/code/ask", "acme", map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("an empty ask got %d, want 400 (%s)", code, raw)
	}
	// And the GET form still reads the query string.
	code, raw := doAuth(t, app, http.MethodGet, "/v1/code/ask?q=from+the+query", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("GET ask want 200, got %d (%s)", code, raw)
	}
	var a AskAnswer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.Question != "from the query" {
		t.Errorf("GET ask asked %q", a.Question)
	}
}

// TestTheQueryStringCannotRedirectAWrite is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body, so a converted write
// silently starts accepting `?prune=1` and `?repo=` — values these routes have
// never taken there, because the untyped handlers read c.Bind, which is the body
// and nothing else. On /index a query-borne prune DELETES files the body never
// asked to remove.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app, _ := newTestApp(t)
	// Seed two files in repo "one".
	code, raw := doAuth(t, app, http.MethodPost, "/v1/code/index", "acme", indexIn{
		Repo:  "one",
		Files: []fileInput{{Path: "a.go", Content: "package a\n"}, {Path: "b.go", Content: "package b\n"}},
	})
	if code != http.StatusOK {
		t.Fatalf("index want 200, got %d (%s)", code, raw)
	}
	// Re-index ONE file with ?prune=1 in the URL and no prune in the body: the
	// query must not turn an upsert into a full sync.
	code, raw = doAuth(t, app, http.MethodPost, "/v1/code/index?prune=1&repo=two", "acme", indexIn{
		Repo:  "one",
		Files: []fileInput{{Path: "a.go", Content: "package a\n"}},
	})
	if code != http.StatusOK {
		t.Fatalf("index want 200, got %d (%s)", code, raw)
	}
	var res indexResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Repo != "one" {
		t.Fatalf("the query string redirected the write to repo %q", res.Repo)
	}
	if res.Pruned != 0 {
		t.Fatalf("the query string turned an upsert into a prune: %d files deleted", res.Pruned)
	}
}

// TestSearchDegradedKeyIsConditional pins the one Out whose key set depends on
// the branch. A healthy answer has never carried "degraded"; the fail-honest
// branch has always carried it as true. `omitempty` is what keeps both shapes,
// and a status-code test cannot see either.
func TestSearchDegradedKeyIsConditional(t *testing.T) {
	healthy, err := json.Marshal(searchResults{Query: "q", Type: "hybrid", Results: []Span{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(healthy); got != `{"query":"q","results":[],"type":"hybrid"}` {
		t.Fatalf("a healthy search answers %s — it gained or lost a key", got)
	}
	degraded, err := json.Marshal(searchResults{Query: "q", Type: "hybrid", Results: []Span{}, Degraded: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(degraded); got != `{"degraded":true,"query":"q","results":[],"type":"hybrid"}` {
		t.Fatalf("a degraded search answers %s", got)
	}
}

// TestFailsClosedWithoutAValidatedPrincipal is the tenancy claim: no request
// field can name the tenant, so an anonymous caller reads and writes nothing —
// and the Bridge routes() installs is what carries the validated org to every op,
// so this also proves that middleware precedes the leaves.
func TestFailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	app, _ := newTestApp(t)
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/code/search?q=x", nil},
		{http.MethodPost, "/v1/code/context", contextIn{Query: "x"}},
		{http.MethodGet, "/v1/code/ask?q=x", nil},
		{http.MethodPost, "/v1/code/ask", map[string]any{"query": "x"}},
		{http.MethodPost, "/v1/code/index", indexIn{Repo: "r", Files: []fileInput{{Path: "a", Content: "b"}}}},
		{http.MethodGet, "/v1/code/tree?repo=r", nil},
		{http.MethodGet, "/v1/code/file?repo=r&path=a", nil},
	} {
		if code, got := doAuth(t, app, r.method, r.path, "", r.body); code != http.StatusForbidden {
			t.Errorf("%s %s anonymous got %d, want 403 (%s)", r.method, r.path, code, got)
		}
	}
}

// index seeds one small repo so the ask/search paths have something to ground on.
func index(t *testing.T, app *zip.App) {
	t.Helper()
	code, raw := doAuth(t, app, http.MethodPost, "/v1/code/index", "acme", indexIn{
		Repo:  "cloud",
		Files: []fileInput{{Path: "store.go", Content: "package store\n\nfunc openStore() error { return nil }\n"}},
	})
	if code != http.StatusOK {
		t.Fatalf("seed index want 200, got %d (%s)", code, raw)
	}
}

func sortedOps(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
