// client.go is the ONE HTTP path from this subsystem to the Lux chain-data plane.
// Every handler in graph.go routes through this client, so the wire contract (base
// URLs, read-only auth, JSON/GraphQL decoding, error mapping) lives once here and can
// never drift between hand-rolled fetches.
//
// TWO upstreams, ONE client:
//
//   - the INDEXER (luxfi/indexer) explorer REST — GET /health and
//     GET /v1/explorer/blocks — read as plain JSON. Base from INDEXER_URL, default
//     the in-cluster indexer Service.
//   - the GRAPH (luxfi/graph) GraphQL — POST /v1/explorer/graphql with a query — the
//     O-Chain priceFeeds registry. Base from GRAPH_URL, default the in-cluster graph
//     Service. (graphd's default route prefix is /v1/explorer.)
//
// AUTH (one rule, read-only). Chain data is a public ledger, so the default is an
// UNAUTHENTICATED read. When a KMS-injected service token is configured (CHAIN_DATA_TOKEN)
// it is sent as a Bearer to both upstreams; otherwise the caller's own forwarded
// Authorization is passed through when present. The token is never logged.
//
// ERROR MAPPING is honest and customer-appropriate: an unreachable upstream → 502, a
// non-2xx HTTP status → that status, and a GraphQL {errors} envelope → 502 with the
// upstream message. It never masks an upstream failure as success or fabricates data.
package graph

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// default in-cluster upstreams. Overridable by INDEXER_URL / GRAPH_URL for other
// environments and for tests (an httptest.Server URL).
const (
	defaultIndexerBase = "http://indexer.lux.svc"
	defaultGraphBase   = "http://graph.lux.svc"
)

// graphQLPath is the GraphQL endpoint under the graph base — graphd mounts it under
// its default /v1/explorer prefix (cmd/graphd/main.go).
const graphQLPath = "/v1/explorer/graphql"

// maxBody bounds an upstream response read — chain-data payloads are small status
// objects + feed lists, not blobs; the cap stops a pathological upstream from
// ballooning memory.
const maxBody = 8 << 20

func envTrim(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func indexerBase() string {
	if v := envTrim("INDEXER_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultIndexerBase
}

func graphBase() string {
	if v := envTrim("GRAPH_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultGraphBase
}

// serviceToken is the optional KMS-injected read token applied to both upstreams.
func serviceToken() string { return envTrim("CHAIN_DATA_TOKEN") }

// client is the read-only chain-data HTTP client. Bases have no trailing slash.
type client struct {
	indexer string
	graph   string
	cc      *http.Client
}

func newClient() *client {
	return &client{
		indexer: indexerBase(),
		graph:   graphBase(),
		cc:      &http.Client{Timeout: 30 * time.Second},
	}
}

// ---- indexer REST ----

// health reads the indexer's /health ({healthy, chain_id, chain_name}). Present on
// the production explorer Service; a mode without it simply yields an error the caller
// treats as best-effort.
func (cl *client) health(c *zip.Ctx) (map[string]any, error) {
	return cl.getJSON(c, cl.indexer+"/health")
}

// latestBlock reads the newest indexed block from /v1/explorer/blocks (items are
// ordered newest-first; items[0] is the latest). This endpoint is common to every
// indexer mode, so it is the mode-agnostic reachability + height probe. Returns nil
// (no error) when the indexer is up but has indexed no blocks yet.
func (cl *client) latestBlock(c *zip.Ctx) (map[string]any, error) {
	raw, err := cl.getJSON(c, cl.indexer+"/v1/explorer/blocks?items_count=1")
	if err != nil {
		return nil, err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return nil, nil // reachable, honestly no blocks yet
	}
	if m, ok := items[0].(map[string]any); ok {
		return m, nil
	}
	return nil, nil
}

// getJSON issues one read-only GET and decodes the JSON body into a generic map. A
// transport failure → 502; a non-2xx → that status.
func (cl *client) getJSON(c *zip.Ctx, url string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(c.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "graph: build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	authorize(req, c)

	resp, err := cl.cc.Do(req)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "graph: indexer unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, zip.Errorf(resp.StatusCode, "graph: indexer %d: %s", resp.StatusCode, snippet(raw))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "graph: decode indexer response: %v", err)
	}
	return out, nil
}

// ---- graph GraphQL ----

// priceFeeds queries the graph's O-Chain price-feed registry. It returns the raw feed
// objects (dynamic maps — the graph stores entities as maps); the caller maps them to
// the console view. A transport failure or a GraphQL {errors} envelope → honest 502.
func (cl *client) priceFeeds(c *zip.Ctx) ([]map[string]any, error) {
	const query = `{ priceFeeds(first: 200) { id pair price timestamp } }`
	body, _ := json.Marshal(map[string]string{"query": query})

	req, err := http.NewRequestWithContext(c.Context(), http.MethodPost, cl.graph+graphQLPath, bytes.NewReader(body))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "graph: build query: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	authorize(req, c)

	resp, err := cl.cc.Do(req)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "graph: graph unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, zip.Errorf(resp.StatusCode, "graph: graph %d: %s", resp.StatusCode, snippet(raw))
	}

	var out struct {
		Data struct {
			PriceFeeds []map[string]any `json:"priceFeeds"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "graph: decode graphql response: %v", err)
	}
	if len(out.Errors) > 0 {
		return nil, zip.Errorf(http.StatusBadGateway, "graph: %s", firstNonEmpty(out.Errors[0].Message, "graphql error"))
	}
	return out.Data.PriceFeeds, nil
}

// authorize attaches a read identity per the one-rule model: a configured service
// token (Bearer) wins; otherwise the caller's forwarded Authorization is passed
// through. Chain data is public, so an absent identity is fine (open read). Never
// logged.
func authorize(req *http.Request, c *zip.Ctx) {
	if tok := serviceToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
		return
	}
	if a := c.Header("Authorization"); a != "" {
		req.Header.Set("Authorization", a)
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
