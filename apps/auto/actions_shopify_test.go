package auto

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/provider"
)

// Shopify tests use a loopback stub for the Admin GraphQL API. No live store is
// contacted. The stub swaps the org->connection resolver and the operator origin
// override; both are restored by t.Cleanup.

type shopifyCapture struct {
	method, path, token, auth, contentType string
	query                                  string
	variables                              map[string]any
}

// shopifyStub points the action's origin at a loopback server and makes the
// connection metadata a bare handle ("acme"), which must normalize to
// acme.myshopify.com before any request is built.
func shopifyStub(t *testing.T, store, response string, status int, cap *shopifyCapture, calls *int) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = *calls + 1
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.token = r.Header.Get("X-Shopify-Access-Token")
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		cap.query, cap.variables = req.Query, req.Variables
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Setenv("SHOPIFY_API_BASE", ts.URL)
	previous := shopifyStoreForOrg
	shopifyStoreForOrg = func(_ context.Context, org string) (string, error) {
		if org != "acme" {
			return "", fmt.Errorf("unexpected org %q", org)
		}
		return store, nil
	}
	t.Cleanup(func() {
		shopifyStoreForOrg = previous
		ts.Close()
	})
}

func shopifyRun(t *testing.T, action string, input map[string]any) (any, error) {
	t.Helper()
	_, a, err := provider.ActionFor(shopifyProviderID, action)
	if err != nil {
		t.Fatalf("ActionFor(shopify, %s): %v", action, err)
	}
	return a.Run(context.Background(), provider.RunContext{
		Org: "acme", Input: input, Token: tokenOK,
	})
}

// TestShopifyIsRunnableInAutoCatalog is the registry/catalogue proof: Shopify's
// runnable bit comes from ActionFor's registry, and every published action has
// prose, typed props and a body.
func TestShopifyIsRunnableInAutoCatalog(t *testing.T) {
	var shopify *ConnectorMetadata
	catalog := connectorCatalog()
	for i := range catalog.Connectors {
		if catalog.Connectors[i].Name == shopifyProviderID {
			shopify = &catalog.Connectors[i]
		}
	}
	if shopify == nil {
		t.Fatalf("shopify is absent from GET /v1/auto/provider")
	}
	if !shopify.Runnable {
		t.Fatalf("shopify is reported runnable:false with %d actions", len(shopify.Actions))
	}
	want := map[string]bool{
		"list_orders": false, "get_order": false, "list_products": false,
		"get_product": false, "list_customers": false, "create_product_draft": false,
	}
	for _, a := range shopify.Actions {
		if _, native := want[a.Name]; !native {
			// The piece card keeps its own (unvendored) action list; this test is
			// about the native actions that actually run here.
			continue
		}
		want[a.Name] = true
		if a.Description == "" || a.DisplayName == "" {
			t.Errorf("%s: catalog card has no prose", a.Name)
		}
		for _, p := range a.Props {
			if p.Name == "" || p.Type == "" {
				t.Errorf("%s: untyped prop %+v", a.Name, p)
			}
			switch strings.ToLower(p.Name) {
			case "host", "domain", "shop", "store", "url", "origin":
				t.Errorf("%s exposes host-like prop %q; the store comes from the connection", a.Name, p.Name)
			}
		}
		if _, _, err := provider.ActionFor(shopifyProviderID, a.Name); err != nil {
			t.Errorf("%s is published but does not resolve: %v", a.Name, err)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("shopify.%s is missing from the catalog", name)
		}
	}
	// The one host rule is shared: a bare handle normalizes exactly as Verify's
	// shopifyOrigin would.
	host, err := provider.ShopHost("acme")
	if err != nil || host != "acme.myshopify.com" {
		t.Fatalf("ShopHost(acme) = %q, %v", host, err)
	}
}

// TestShopifyReadActions covers all five read paths against the stub: pinned
// Admin version, POST JSON body, X-Shopify-Access-Token auth, cursor/query
// variables, and the data object returned to the flow.
func TestShopifyReadActions(t *testing.T) {
	cases := []struct {
		action, queryWant, response, resultKey string
		input                                  map[string]any
	}{
		{"list_orders", "orders(", `{"data":{"orders":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`, "orders",
			map[string]any{"first": 2, "after": "cur-1", "query": "status:any"}},
		{"get_order", "order(id: $id)", `{"data":{"order":{"id":"gid://shopify/Order/1","name":"#1001"}}}`, "order",
			map[string]any{"id": "gid://shopify/Order/1"}},
		{"list_products", "products(", `{"data":{"products":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`, "products",
			map[string]any{"first": 2}},
		{"get_product", "product(id: $id)", `{"data":{"product":{"id":"gid://shopify/Product/1","title":"Tee"}}}`, "product",
			map[string]any{"id": "gid://shopify/Product/1"}},
		{"list_customers", "customers(", `{"data":{"customers":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}`, "customers",
			map[string]any{"first": 2}},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			cap, calls := &shopifyCapture{}, 0
			shopifyStub(t, "acme", tc.response, http.StatusOK, cap, &calls)
			out, err := shopifyRun(t, tc.action, tc.input)
			if err != nil {
				t.Fatalf("%s: %v", tc.action, err)
			}
			if calls != 1 {
				t.Fatalf("%s made %d calls, want 1", tc.action, calls)
			}
			if cap.method != http.MethodPost || cap.path != shopifyGraphQLPath {
				t.Errorf("%s called %s %s, want POST %s", tc.action, cap.method, cap.path, shopifyGraphQLPath)
			}
			if cap.token != "fake-token" || cap.auth != "" {
				t.Errorf("%s auth: X-Shopify-Access-Token=%q Authorization=%q", tc.action, cap.token, cap.auth)
			}
			if !strings.Contains(cap.contentType, "application/json") {
				t.Errorf("%s Content-Type = %q", tc.action, cap.contentType)
			}
			if !strings.Contains(cap.query, tc.queryWant) {
				t.Errorf("%s query %q does not contain %q", tc.action, cap.query, tc.queryWant)
			}
			if tc.action == "get_order" || tc.action == "get_product" {
				if cap.variables["id"] != tc.input["id"] {
					t.Errorf("%s variables = %v", tc.action, cap.variables)
				}
			} else {
				if cap.variables["first"] != float64(2) {
					t.Errorf("%s first = %v, want 2", tc.action, cap.variables["first"])
				}
				if tc.action == "list_orders" {
					if cap.variables["after"] != "cur-1" || cap.variables["query"] != "status:any" {
						t.Errorf("list_orders variables = %v", cap.variables)
					}
				}
			}
			m, ok := out.(map[string]any)
			if !ok || m[tc.resultKey] == nil {
				t.Fatalf("%s output = %v, want data.%s", tc.action, out, tc.resultKey)
			}
		})
	}
}

// TestShopifyCreateProductDraftNeverPublishes pins the safe write: the mutation
// sets status DRAFT in the variables, never calls productPublish, and still
// carries the org's token on the pinned Admin endpoint.
func TestShopifyCreateProductDraftNeverPublishes(t *testing.T) {
	cap, calls := &shopifyCapture{}, 0
	response := `{"data":{"productCreate":{"product":{"id":"gid://shopify/Product/9","title":"Draft","status":"DRAFT"},"userErrors":[]}}}`
	shopifyStub(t, "acme.myshopify.com", response, http.StatusOK, cap, &calls)
	out, err := shopifyRun(t, "create_product_draft", map[string]any{
		"title": "Draft", "vendor": "Hanzo", "tags": []any{"new", "draft"},
	})
	if err != nil {
		t.Fatalf("create_product_draft: %v", err)
	}
	if calls != 1 || cap.path != shopifyGraphQLPath || cap.token != "fake-token" {
		t.Fatalf("call = %d path = %s token = %q", calls, cap.path, cap.token)
	}
	if !strings.Contains(cap.query, "productCreate") || strings.Contains(cap.query, "productPublish") {
		t.Errorf("mutation = %q", cap.query)
	}
	input, ok := cap.variables["input"].(map[string]any)
	if !ok || input["status"] != "DRAFT" || input["title"] != "Draft" {
		t.Fatalf("mutation input = %v", cap.variables)
	}
	if tags, ok := input["tags"].([]any); !ok || len(tags) != 2 {
		t.Errorf("tags = %v", input["tags"])
	}
	created, ok := out.(map[string]any)["productCreate"].(map[string]any)
	if !ok || created["product"] == nil {
		t.Fatalf("output = %v", out)
	}
}

func TestShopifyDraftWriteReportsUserErrors(t *testing.T) {
	cap, calls := &shopifyCapture{}, 0
	response := `{"data":{"productCreate":{"product":null,"userErrors":[{"field":["title"],"message":"Title can't be blank"}]}}}`
	shopifyStub(t, "acme", response, http.StatusOK, cap, &calls)
	_, err := shopifyRun(t, "create_product_draft", map[string]any{"title": "Draft"})
	if err == nil || !strings.Contains(err.Error(), "Title can't be blank") {
		t.Fatalf("want the mutation's userError, got %v", err)
	}
}

// TestShopifyMissingTokenFailsClosed: no credential, no request. The credential
// is resolved BEFORE the store or the network, so a not-connected org cannot
// even reach the API.
func TestShopifyMissingTokenFailsClosed(t *testing.T) {
	cap, calls := &shopifyCapture{}, 0
	shopifyStub(t, "acme", `{"data":{}}`, http.StatusOK, cap, &calls)
	_, a, err := provider.ActionFor(shopifyProviderID, "list_orders")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), provider.RunContext{
		Org: "acme", Token: tokenFail, Input: map[string]any{},
	})
	if err == nil || !strings.Contains(err.Error(), "shopify not connected") {
		t.Fatalf("want shopify not connected, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("a not-connected org made %d requests", calls)
	}
}

// TestShopifyRejectsBadStoredIdentifier: a connection whose stored account is
// not a Shopify host is refused without a request. The operator override is set
// by the stub, proving normalization is not skipped when it is present.
func TestShopifyRejectsBadStoredIdentifier(t *testing.T) {
	for _, store := range []string{"", "evil.example.com", "a b.myshopify.com", "acme.myshopify.com.evil.com"} {
		t.Run(store, func(t *testing.T) {
			cap, calls := &shopifyCapture{}, 0
			shopifyStub(t, store, `{"data":{}}`, http.StatusOK, cap, &calls)
			_, err := shopifyRun(t, "list_orders", nil)
			if err == nil || !strings.Contains(err.Error(), "invalid store identifier") {
				t.Fatalf("store %q: want an invalid-store refusal, got %v", store, err)
			}
			if calls != 0 {
				t.Fatalf("store %q made %d requests", store, calls)
			}
		})
	}
}

// TestShopifyPropagatesAPIErrors covers both failure channels: a non-2xx status
// and a 200 carrying a top-level GraphQL errors array.
func TestShopifyPropagatesAPIErrors(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		cap, calls := &shopifyCapture{}, 0
		shopifyStub(t, "acme", `{"errors":"Access denied"}`, http.StatusForbidden, cap, &calls)
		_, err := shopifyRun(t, "list_orders", nil)
		if err == nil || !strings.Contains(err.Error(), "shopify api 403") || !strings.Contains(err.Error(), "Access denied") {
			t.Fatalf("want status and body in the error, got %v", err)
		}
	})
	t.Run("graphql", func(t *testing.T) {
		cap, calls := &shopifyCapture{}, 0
		shopifyStub(t, "acme", `{"errors":[{"message":"Field 'nope' doesn't exist"}]}`, http.StatusOK, cap, &calls)
		_, err := shopifyRun(t, "list_orders", nil)
		if err == nil || !strings.Contains(err.Error(), "Field 'nope' doesn't exist") {
			t.Fatalf("want the GraphQL error message, got %v", err)
		}
	})
}

// TestShopifyHostNeverComesFromInput: a caller that adds host-like keys cannot
// send the org's token anywhere, and the store identifier is read under the
// validated org through the connection resolver.
func TestShopifyHostNeverComesFromInput(t *testing.T) {
	cap, calls := &shopifyCapture{}, 0
	shopifyStub(t, "acme", `{"data":{"orders":{"edges":[]}}}`, http.StatusOK, cap, &calls)
	_, err := shopifyRun(t, "list_orders", map[string]any{
		"host": "evil.example.com", "store": "evil.myshopify.com", "url": "https://evil.example.com",
	})
	if err != nil {
		t.Fatalf("list_orders: %v", err)
	}
	for _, key := range []string{"host", "store", "url"} {
		if _, ok := cap.variables[key]; ok {
			t.Errorf("input key %q reached Shopify variables: %v", key, cap.variables)
		}
	}
	if calls != 1 || cap.path != shopifyGraphQLPath {
		t.Fatalf("call = %d path = %s", calls, cap.path)
	}
}
