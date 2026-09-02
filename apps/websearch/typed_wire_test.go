package websearch

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mounted brings the subsystem up on a bare app with the Bridge installed — the
// middleware cloud.Identify puts in front of every typed route in every process,
// and the thing that carries a validated principal across the typed-op client. A
// test without it measures a gate that can only ever refuse.
func mounted(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(account.KeyEnv, testCSRFKey)
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// searched posts a native search as a signed-in caller.
func searched(t *testing.T, app *zip.App, body string, hdr map[string]string) (int, string) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("POST %s: %v", Path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// signedIn is what the identity boundary mints for a verified bearer. principal.Org
// reads both, and a request carrying neither is anonymous.
var signedIn = map[string]string{"X-Org-Id": "acme", "X-User-Id": "u-acme"}

// TestWebSearchIsTheSameSearchAsTheCompatEndpoint is the claim the native endpoint
// rests on: it is not a second search stack, it is the SAME metaSearch reached at
// an address a typed op can live at.
//
// The SearXNG endpoint cannot be a typed op — it is registered with All, so it
// answers every method in the router's set, and zip has no typed All. That is a
// fact about the ADAPTER, and for a long time it was read as a fact about web
// search: this subsystem served the fleet's only path to the live internet and
// projected no tool at all, so an agent asked for today's weather had nothing to
// call. The native endpoint is that capability at an address the registry can hold.
func TestWebSearchIsTheSameSearchAsTheCompatEndpoint(t *testing.T) {
	mockBing(t, bingFixture)
	t.Setenv("WEBSEARCH_API_KEY", "k")
	app := mounted(t)

	code, native := searched(t, app, `{"q":"example"}`, signedIn)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d %s, want 200", Path, code, native)
	}

	// The compat endpoint, same query, same process.
	rq := httptest.NewRequest(http.MethodGet, "/v1/websearch/search?q=example", nil)
	rq.Header.Set("X-API-Key", "k")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("GET /v1/websearch/search: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	compat := strings.TrimSpace(string(raw))

	if native != compat {
		t.Errorf("the two endpoints disagree — one search, two addresses:\n native: %s\n compat: %s", native, compat)
	}
	var out webSearchResults
	if err := json.Unmarshal([]byte(native), &out); err != nil {
		t.Fatalf("the native answer is not the SearXNG envelope: %v — %s", err, native)
	}
	if len(out.Results) == 0 || out.Results[0].URL != "https://example.com/page" {
		t.Errorf("results = %+v, want the fixture hit", out.Results)
	}
}

// TestWebSearchIsClosedToAnAnonymousCaller measures the gate at the endpoint a
// typed op adds. A tools/call reaches the handler with NO route and therefore NO
// middleware, so the decision is in the handler; here it is asked over HTTP.
func TestWebSearchIsClosedToAnAnonymousCaller(t *testing.T) {
	mockBing(t, bingFixture)
	t.Setenv("WEBSEARCH_API_KEY", "k")
	app := mounted(t)

	if code, body := searched(t, app, `{"q":"example"}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("an anonymous search = %d %s, want 401", code, body)
	}
	if code, body := searched(t, app, `{"q":"  "}`, signedIn); code != http.StatusBadRequest {
		t.Fatalf("an empty query = %d %s, want 400 — the whole web is not an answer", code, body)
	}
}

// TestWebSearchProjectsAsATool is the fact the conversion was FOR.
//
// A raw handler appends nothing to zip's op registry, and that registry is what
// every projection reads — so a subsystem of raw routes is in no OpenAPI
// operation, no SDK method, no CLI command and no MCP tool. Asserting the handler
// answers correctly says nothing about any of that. This asks the subsystem's OWN
// MCP server over JSON-RPC, exactly as the fleet's MCP server asks it.
func TestWebSearchProjectsAsATool(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "k")
	app := mounted(t)

	rq := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rq.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var env struct {
		Result *struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Result == nil {
		t.Fatalf("POST /mcp did not answer MCP: %d — %.200s", resp.StatusCode, raw)
	}
	for _, tool := range env.Result.Tools {
		if tool.Name != "search_web" {
			continue
		}
		if tool.Description == "" {
			t.Fatal("search_web projects with NO description — a tool a model cannot " +
				"read is a tool it will not call; run `go generate -run zipdoc ./...`")
		}
		t.Logf("search_web projects: %.90s…", tool.Description)
		return
	}
	t.Fatalf("search_web is not in this subsystem's tools/list — %.300s", raw)
}

// untypedByDesign is the CLOSED list of addresses this subsystem serves raw,
// keyed by PATH rather than by "METHOD /path".
//
// The key is the path because ONE `All` registration refuses for every method at
// once: `/v1/websearch/search` publishes five operations from a single call, so
// keying by method would state one fact five times and let four copies rot
// independently. apps/iam keys its ledger the same way for the same reason.
//
// Both entries are COMPATIBILITY surfaces — wires we did not design and do not
// own — which is a different kind of refusal from "zip cannot express this" and
// does not expire when zip gains a capability:
//
//   - THE SEARXNG ENDPOINT answers every method in the router's set (registered
//     with All; zip has no typed All), and a client composes the address from its
//     own base URL. Typing it means either publishing five typed ops over one
//     handler or dropping the methods a SearXNG client actually sends. It ALSO
//     carries the precedence fact: `searchGuard` wraps the handler only for an
//     unvalidated caller, so an API-key refusal is decided before anything is
//     parsed.
//   - THE FIRECRAWL SCRAPE keeps firecrawl's BODY and firecrawl's ANSWER verbatim
//     (websearch.go says so at the registration). A typed In/Out would restate a
//     third party's shape in Go and drift from it on their next release.
//
// Neither costs the plane its capability: `POST /v1/websearch` is the SAME
// metaSearch at an address the registry can hold, and it is typed, described and
// projected — which is what TestWebSearchIsTheSameSearchAsTheCompatEndpoint proves.
var untypedByDesign = map[string]string{
	"/v1/websearch/search": "the SearXNG-compatible endpoint — one All registration answering every method, " +
		"whose request shape and answer belong to SearXNG's contract rather than to us.",
	"/v1/websearch/scrape": "the firecrawl-compatible fetch — the body and the answer are firecrawl's, " +
		"carried verbatim so a firecrawl client is re-pointed rather than rewritten.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface. A route added raw goes red without anyone remembering this file, and a
// reason naming an address this subsystem no longer serves goes red too.
//
// The sum is DERIVED from the live document rather than written down, because a
// hand-written total is the thing that goes stale: an `All` publishes five
// operations from one registration, so the count nobody can predict is exactly
// the count a constant would get wrong.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mounted(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "websearch", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/websearch") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/websearch") {
			typed[key] = true
		}
	}

	var untyped, named []string
	for key := range served {
		if typed[key] {
			continue
		}
		_, path, _ := strings.Cut(key, " ")
		if _, ok := untypedByDesign[path]; !ok {
			untyped = append(untyped, key)
			continue
		}
		named = append(named, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it, or name its PATH in untypedByDesign with the wire fact that "+
			"keeps it raw — re-read against the pinned zip, never inherited from an older pass.",
			strings.Join(untyped, ", "))
	}
	for path := range untypedByDesign {
		hit := false
		for key := range served {
			if _, p, _ := strings.Cut(key, " "); p == path {
				hit = true
				if typed[key] {
					t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
				}
			}
		}
		if !hit {
			t.Errorf("untypedByDesign names %q, which this subsystem no longer serves", path)
		}
	}
	if got, want := len(typed)+len(named), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(named), got, want)
	}
}
