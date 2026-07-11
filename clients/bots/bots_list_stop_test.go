package bots

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"
)

// gwServer is a minimal bot-gateway double for the list/stop path. It records
// the X-Org-Id it was called with (a wrong tenant here would prove a cross-tenant
// leak) and serves configurable list/stop responses so the cloud proxy's
// normalization + honest-empty behavior can be asserted hermetically.
type gwServer struct {
	bots     []map[string]any // rows GET /v1/bots returns
	listCode int              // status for GET /v1/bots (0 => 200)
	stopCode int              // status for POST /v1/bots/{id}/stop (0 => 200)

	mu      sync.Mutex
	listOrg string // X-Org-Id seen on the last list
	stopOrg string // X-Org-Id seen on the last stop
	stopID  string // runId parsed from the last stop path
}

func (g *gwServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/bots", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.listOrg = r.Header.Get("X-Org-Id")
		g.mu.Unlock()
		if g.listCode != 0 {
			w.WriteHeader(g.listCode)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"bots": g.bots})
	})
	mux.HandleFunc("/v1/bots/", func(w http.ResponseWriter, r *http.Request) {
		// path is /v1/bots/{id}/stop
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/bots/"), "/stop")
		g.mu.Lock()
		g.stopOrg, g.stopID = r.Header.Get("X-Org-Id"), id
		g.mu.Unlock()
		if g.stopCode != 0 {
			w.WriteHeader(g.stopCode)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (g *gwServer) seenListOrg() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.listOrg
}

func (g *gwServer) seenStop() (string, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopOrg, g.stopID
}

// mountGW mounts the bots surface with the server-side bot-gateway pinned to
// serverURL (empty => the in-cluster default, i.e. unreachable in tests). It
// reuses mount(t, "") so the browser-facing gateway base stays the deterministic
// https://bot.example.test that sessionUrl assertions depend on.
func mountGW(t *testing.T, serverURL string) *zip.App {
	t.Helper()
	t.Setenv(serverGatewayURLEnv, serverURL)
	return mount(t, "")
}

// getBots issues GET /v1/bots with the gateway-minted identity headers (org +
// its validated X-User-Id). An empty org sends neither (the anonymous path).
func getBots(t *testing.T, app *zip.App, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// stopBotReq issues POST /v1/bots/{runID}/stop with identity headers.
func stopBotReq(t *testing.T, app *zip.App, org, runID string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/bots/"+runID+"/stop", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestListRequiresOrg: no X-Org-Id is 403 — no tenant, no list.
func TestListRequiresOrg(t *testing.T) {
	app := mountGW(t, "http://127.0.0.1:1")
	if code, _ := getBots(t, app, ""); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}
}

// TestListRequiresValidatedPrincipal: a bare, forgeable X-Org-Id (no validated
// X-User-Id) is 403 — an unvalidated caller cannot enumerate a victim tenant.
func TestListRequiresValidatedPrincipal(t *testing.T) {
	app := mountGW(t, "http://127.0.0.1:1")
	req := httptest.NewRequest(http.MethodGet, "/v1/bots", nil)
	req.Header.Set("X-Org-Id", "acme") // forged; no X-User-Id => no validated principal
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-principal list want 403, got %d", resp.StatusCode)
	}
}

// TestListNormalizesAndScopesToCaller: the gateway's session rows are normalized
// into the contract (sessionUrl derived control-plane side from runId), and the
// gateway is called scoped to the CALLER org (acme), never the client default.
func TestListNormalizesAndScopesToCaller(t *testing.T) {
	gw := &gwServer{bots: []map[string]any{
		{"runId": "bot_abc", "task": "summarize inbox", "surface": "desktop", "status": "running", "startedAt": "2026-07-11T00:00:00Z"},
		{"runId": "bot_def", "task": "book flight", "surface": "terminal", "status": "running", "startedAt": "2026-07-11T01:00:00Z"},
	}}
	app := mountGW(t, gw.start(t))

	code, body := getBots(t, app, "acme")
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var got botsView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(got.Bots) != 2 {
		t.Fatalf("want 2 bots, got %d (%s)", len(got.Bots), body)
	}
	first := got.Bots[0]
	if first.RunID != "bot_abc" || first.Task != "summarize inbox" || first.Surface != "desktop" {
		t.Fatalf("row not normalized: %+v", first)
	}
	if first.Status != statusRunning {
		t.Fatalf("status want %q, got %q", statusRunning, first.Status)
	}
	if first.StartedAt != "2026-07-11T00:00:00Z" {
		t.Fatalf("startedAt passthrough want set, got %q", first.StartedAt)
	}
	// sessionUrl is derived here from the browser-facing base + nodeId=runId, NOT
	// taken from the gateway — the ONE place a session URL is built.
	want := "https://bot.example.test/vnc?nodeId=bot_abc"
	if first.SessionURL != want {
		t.Fatalf("sessionUrl want %q, got %q", want, first.SessionURL)
	}
	if org := gw.seenListOrg(); org != "acme" {
		t.Fatalf("gateway saw X-Org-Id=%q, want caller %q (never default 'hanzo')", org, "acme")
	}
}

// TestListHonestEmptyWhenGatewayUnreachable: an unreachable gateway yields a 200
// {"bots":[]}, never a 5xx — the console renders "no bots", not an error.
func TestListHonestEmptyWhenGatewayUnreachable(t *testing.T) {
	app := mountGW(t, "http://127.0.0.1:1") // connection refused
	code, body := getBots(t, app, "acme")
	if code != http.StatusOK {
		t.Fatalf("unreachable list want 200, got %d (%s)", code, body)
	}
	assertEmptyBots(t, body)
}

// TestListHonestEmptyOnGatewayError: a non-2xx from the gateway is also honest-
// empty, not a propagated 5xx.
func TestListHonestEmptyOnGatewayError(t *testing.T) {
	gw := &gwServer{listCode: http.StatusInternalServerError}
	app := mountGW(t, gw.start(t))
	code, body := getBots(t, app, "acme")
	if code != http.StatusOK {
		t.Fatalf("gateway-500 list want 200, got %d (%s)", code, body)
	}
	assertEmptyBots(t, body)
}

// TestStopStopsCallerRun: a stop the gateway accepts returns {runId,"stopped"}
// and forwards the CALLER org + the exact runId to the gateway.
func TestStopStopsCallerRun(t *testing.T) {
	gw := &gwServer{}
	app := mountGW(t, gw.start(t))

	code, body := stopBotReq(t, app, "acme", "bot_abc")
	if code != http.StatusOK {
		t.Fatalf("stop want 200, got %d (%s)", code, body)
	}
	var got stopView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if got.RunID != "bot_abc" || got.Status != statusStopped {
		t.Fatalf("stop response want {bot_abc,stopped}, got %+v", got)
	}
	org, id := gw.seenStop()
	if org != "acme" {
		t.Fatalf("gateway saw stop X-Org-Id=%q, want caller %q", org, "acme")
	}
	if id != "bot_abc" {
		t.Fatalf("gateway saw stop runId=%q, want %q", id, "bot_abc")
	}
}

// TestStopNotFound: a run the caller's org does not own (gateway 404) is a 404,
// never a 200 — stop cannot claim a teardown that did not happen.
func TestStopNotFound(t *testing.T) {
	gw := &gwServer{stopCode: http.StatusNotFound}
	app := mountGW(t, gw.start(t))
	if code, body := stopBotReq(t, app, "acme", "bot_nope"); code != http.StatusNotFound {
		t.Fatalf("stop of unowned run want 404, got %d (%s)", code, body)
	}
}

// TestStopUnreachableIs502: a stop that cannot reach the gateway is a clean 502,
// not a false "stopped".
func TestStopUnreachableIs502(t *testing.T) {
	app := mountGW(t, "http://127.0.0.1:1")
	if code, _ := stopBotReq(t, app, "acme", "bot_abc"); code != http.StatusBadGateway {
		t.Fatalf("stop with unreachable gateway want 502, got %d", code)
	}
}

// TestStopRequiresOrg: no X-Org-Id is 403.
func TestStopRequiresOrg(t *testing.T) {
	app := mountGW(t, "http://127.0.0.1:1")
	if code, _ := stopBotReq(t, app, "", "bot_abc"); code != http.StatusForbidden {
		t.Fatalf("no-org stop want 403, got %d", code)
	}
}

func assertEmptyBots(t *testing.T, body []byte) {
	t.Helper()
	// Must be an explicit empty array, not null — {"bots":[]}.
	if !strings.Contains(string(body), `"bots":[]`) {
		t.Fatalf("want honest-empty {\"bots\":[]}, got %s", body)
	}
	var got botsView
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(got.Bots) != 0 {
		t.Fatalf("want 0 bots, got %d", len(got.Bots))
	}
}
