package websearch

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mounted brings the subsystem up on a bare app with the Bridge installed — the
// middleware cloud.Identify puts in front of every typed route in every process,
// and the thing that carries a validated principal across the typed-op seam. A
// test without it measures a gate that can only ever refuse.
func mounted(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
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

// TestWebSearchIsTheSameSearchAsTheCompatDoor is the claim the native door rests
// on: it is not a second search stack, it is the SAME metaSearch reached at an
// address a typed op can live at.
//
// The SearXNG door cannot be a typed op — it is registered with All, so it answers
// every method in the router's set, and zip has no typed All. That is a fact about
// the ADAPTER, and for a long time it was read as a fact about web search: this
// subsystem served the fleet's only path to the live internet and projected no
// tool at all, so an agent asked for today's weather had nothing to call. The
// native door is that capability at an address the registry can hold.
func TestWebSearchIsTheSameSearchAsTheCompatDoor(t *testing.T) {
	mockBing(t, bingFixture)
	t.Setenv("WEBSEARCH_API_KEY", "k")
	app := mounted(t)

	code, native := searched(t, app, `{"q":"example"}`, signedIn)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d %s, want 200", Path, code, native)
	}

	// The compat door, same query, same process.
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
		t.Errorf("the two doors disagree — one search, two addresses:\n native: %s\n compat: %s", native, compat)
	}
	var out webSearchResults
	if err := json.Unmarshal([]byte(native), &out); err != nil {
		t.Fatalf("the native answer is not the SearXNG envelope: %v — %s", err, native)
	}
	if len(out.Results) == 0 || out.Results[0].URL != "https://example.com/page" {
		t.Errorf("results = %+v, want the fixture hit", out.Results)
	}
}

// TestWebSearchIsClosedToAnAnonymousCaller measures the gate at the door a typed
// op adds. A tools/call reaches the handler with NO route and therefore NO
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
// MCP door over JSON-RPC, exactly as the fleet's door asks it.
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
