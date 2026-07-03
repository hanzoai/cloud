package graph

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeChain is a stand-in for BOTH Lux chain-data upstreams behind one server: the
// luxfi/indexer explorer REST (/health, /v1/explorer/blocks) and the luxfi/graph
// GraphQL (/v1/explorer/graphql). A test points INDEXER_URL and GRAPH_URL at it to
// prove cloud reads REAL upstream state, gates on the validated principal, and never
// fabricates.
type fakeChain struct {
	healthy    bool
	chainName  string
	chainID    int
	blocks     []map[string]any // newest-first; blocks[0] is the latest
	priceFeeds []map[string]any
	gqlError   string // when set, the graph answers a {errors} envelope

	lastQuery string
}

func (f *fakeChain) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"healthy": f.healthy, "chain_id": f.chainID, "chain_name": f.chainName,
		})
	})
	mux.HandleFunc("/v1/explorer/blocks", func(w http.ResponseWriter, r *http.Request) {
		items := f.blocks
		if items == nil {
			items = []map[string]any{}
		}
		writeJSON(w, map[string]any{"items": items, "next_page_params": nil})
	})
	mux.HandleFunc("/v1/explorer/graphql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.lastQuery = req.Query
		if f.gqlError != "" {
			writeJSON(w, map[string]any{"errors": []map[string]any{{"message": f.gqlError}}})
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{"priceFeeds": f.priceFeeds}})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// mountApp mounts the graph surface against the fake upstream at base.
func mountApp(t *testing.T, base string) *zip.App {
	t.Helper()
	t.Setenv("INDEXER_URL", base)
	t.Setenv("GRAPH_URL", base)
	t.Setenv("CHAIN_DATA_TOKEN", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "lux", Env: "mainnet"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		// A validated principal: principal.Tenant requires a non-empty X-User-Id,
		// which the gateway sets ONLY from a verified credential. Inject it directly
		// (empty org → no user → the 403 path).
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestIndexersGatedAndShape(t *testing.T) {
	f := &fakeChain{
		healthy: true, chainName: "Lux C-Chain", chainID: 96369,
		blocks: []map[string]any{{"height": 12345, "timestamp": 1711929600}},
	}
	app := mountApp(t, f.server(t).URL)

	// No validated principal → 403, and the request never reaches the indexer.
	if code, _ := do(t, app, http.MethodGet, "/v1/indexers", ""); code != http.StatusForbidden {
		t.Fatalf("no-org indexers want 403, got %d", code)
	}

	code, body := do(t, app, http.MethodGet, "/v1/indexers", "acme")
	if code != http.StatusOK {
		t.Fatalf("indexers want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Indexers []indexerView `json:"indexers"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(listed.Indexers) != 1 {
		t.Fatalf("want 1 indexer, got %d", len(listed.Indexers))
	}
	iv := listed.Indexers[0]
	if iv.Chain != "Lux C-Chain" || iv.Network != "mainnet" || iv.Height != "12345" ||
		iv.Status != "active" || iv.UpdatedAt != "2024-04-01T00:00:00Z" {
		t.Fatalf("indexer view mismatch: %+v", iv)
	}
	// lag is honestly omitted (not derivable from the indexer REST).
	if iv.Lag != "" {
		t.Fatalf("lag must be omitted (never fabricated), got %q", iv.Lag)
	}
}

func TestIndexerUnhealthyDegraded(t *testing.T) {
	f := &fakeChain{healthy: false, chainName: "Lux C-Chain", blocks: []map[string]any{}}
	app := mountApp(t, f.server(t).URL)

	code, body := do(t, app, http.MethodGet, "/v1/indexers", "acme")
	if code != http.StatusOK {
		t.Fatalf("indexers want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Indexers []indexerView `json:"indexers"`
	}
	_ = json.Unmarshal(body, &listed)
	if len(listed.Indexers) != 1 || listed.Indexers[0].Status != "degraded" {
		t.Fatalf("unhealthy indexer must be degraded, got %+v", listed.Indexers)
	}
	// No blocks indexed yet → honest empty height (never a fabricated 0).
	if listed.Indexers[0].Height != "" {
		t.Fatalf("height must be empty when no blocks indexed, got %q", listed.Indexers[0].Height)
	}
}

func TestIndexerUnreachable502(t *testing.T) {
	t.Setenv("INDEXER_URL", "http://indexer.invalid.test")
	t.Setenv("GRAPH_URL", "http://graph.invalid.test")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "lux", Env: "mainnet"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/indexers", "acme"); code != http.StatusBadGateway {
		t.Fatalf("unreachable indexer want 502, got %d", code)
	}
}

func TestOraclesGatedShapeAndEmpty(t *testing.T) {
	f := &fakeChain{priceFeeds: []map[string]any{
		{"id": "LUX-USD", "pair": "LUX/USD", "price": "2.50", "timestamp": 1711929600},
		{"id": "ZOO-USD", "pair": "ZOO/USD", "price": "0.08", "timestamp": 1711929600},
	}}
	app := mountApp(t, f.server(t).URL)

	// No validated principal → 403.
	if code, _ := do(t, app, http.MethodGet, "/v1/oracles", ""); code != http.StatusForbidden {
		t.Fatalf("no-org oracles want 403, got %d", code)
	}

	code, body := do(t, app, http.MethodGet, "/v1/oracles", "acme")
	if code != http.StatusOK {
		t.Fatalf("oracles want 200, got %d (%s)", code, body)
	}
	var listed struct {
		Oracles []oracleView `json:"oracles"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if len(listed.Oracles) != 2 {
		t.Fatalf("want 2 oracles, got %d", len(listed.Oracles))
	}
	o := listed.Oracles[0]
	if o.ID != "LUX-USD" || o.Name != "LUX/USD" || o.Feed != "LUX/USD" || o.Value != "2.50" ||
		o.Source != "O-Chain" || o.Status != "active" || o.UpdatedAt != "2024-04-01T00:00:00Z" {
		t.Fatalf("oracle view mismatch: %+v", o)
	}

	// An empty registry is an honest empty list — a real, gated route, not a 404.
	f.priceFeeds = nil
	code, body = do(t, app, http.MethodGet, "/v1/oracles", "acme")
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Oracles) != 0 {
		t.Fatalf("empty registry want 200 [], got %d %+v", code, listed.Oracles)
	}
}

func TestOraclesGraphQLError502(t *testing.T) {
	f := &fakeChain{gqlError: "schema unavailable"}
	app := mountApp(t, f.server(t).URL)
	if code, _ := do(t, app, http.MethodGet, "/v1/oracles", "acme"); code != http.StatusBadGateway {
		t.Fatalf("graphql error want 502, got %d", code)
	}
}

func TestPublicDataSameAcrossOrgsButAuthRequired(t *testing.T) {
	f := &fakeChain{
		healthy: true, chainName: "Lux C-Chain",
		blocks:     []map[string]any{{"height": 100, "timestamp": 1711929600}},
		priceFeeds: []map[string]any{{"id": "LUX-USD", "pair": "LUX/USD", "price": "2.50"}},
	}
	app := mountApp(t, f.server(t).URL)

	// Two different validated orgs both see the SAME public ledger (a blockchain has
	// no per-org private row to leak); an UNauthenticated caller sees nothing (403).
	for _, path := range []string{"/v1/indexers", "/v1/oracles"} {
		if code, _ := do(t, app, http.MethodGet, path, ""); code != http.StatusForbidden {
			t.Fatalf("unauth %s want 403, got %d", path, code)
		}
		a, ba := do(t, app, http.MethodGet, path, "acme")
		b, bb := do(t, app, http.MethodGet, path, "other")
		if a != http.StatusOK || b != http.StatusOK {
			t.Fatalf("%s want 200 for both orgs, got %d/%d", path, a, b)
		}
		if string(ba) != string(bb) {
			t.Fatalf("%s public data must be identical across orgs:\n acme=%s\nother=%s", path, ba, bb)
		}
	}
}
