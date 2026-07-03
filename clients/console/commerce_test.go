package console

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// commerce_test.go — the per-tenant STORE bridge (commerce.go). Proves the org
// scoping that prevents cross-tenant store access, the /v1/commerce/<x> → /v1/<x>
// remap, the store-head least-privilege gate, and the IDOR-safe forwarding — the
// store twin of billing_test.go.

// ── pure allow-list ──────────────────────────────────────────────────────────

func TestIsCommerceStoreHead(t *testing.T) {
	// Every store head (and its sub-paths) is admitted; a non-store head is not, so
	// the bridge can never tunnel to the money / tenant-admin surfaces.
	allow := []string{"product", "product/abc", "store/current", "order", "user", "variant"}
	deny := []string{"billing", "billing/balance", "checkout", "checkout/session", "namespace", "_", "tenants", ""}
	for _, p := range allow {
		if !isCommerceStoreHead(p) {
			t.Fatalf("store head %q must be admitted", p)
		}
	}
	for _, p := range deny {
		if isCommerceStoreHead(p) {
			t.Fatalf("non-store path %q must be refused", p)
		}
	}
}

// ── handler (IDOR + forwarding) ──────────────────────────────────────────────

// fakeStore records exactly what the handler forwarded to commerce.
type fakeStore struct {
	mu     sync.Mutex
	method string
	path   string
	query  url.Values
	body   map[string]any
	org    string
	auth   string
}

func (f *fakeStore) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.method, f.path, f.query = r.Method, r.URL.Path, r.URL.Query()
		f.org, f.auth = r.Header.Get("X-Org-Id"), r.Header.Get("Authorization")
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &f.body)
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[],"count":0}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCommerce_RequiresValidatedPrincipal(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")
	// A forged X-Org-Id with NO validated X-User-Id is the exact forge — refuse it
	// BEFORE any commerce call (no cross-tenant store read on a victim org). Cover a
	// mutating method too: a forged write must 403, never reach the store.
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		code, _ := callH(t, app, m, "/v1/commerce/product", map[string]string{"X-Org-Id": "victim"}, "")
		if code != http.StatusForbidden {
			t.Fatalf("%s no validated principal: want 403, got %d", m, code)
		}
	}
}

func TestCommerce_NotConfiguredWithoutToken(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	app := mountApp(t, "http://iam.invalid", "", "")
	code, _ := callH(t, app, http.MethodGet, "/v1/commerce/product", alice, "")
	if code != http.StatusNotImplemented {
		t.Fatalf("no commerce token: want 501, got %d", code)
	}
}

func TestCommerce_ForwardsToBareStoreScopedToCaller(t *testing.T) {
	f := &fakeStore{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")

	// alice/acme lists products: the console-side /v1/commerce/product must reach
	// commerce's BARE /v1/product (the `commerce` namespace stripped), scoped to acme.
	code, body := callH(t, app, http.MethodGet, "/v1/commerce/product?limit=100&q=shirt", alice, "")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	if f.path != "/v1/product" {
		t.Fatalf("forwarded path: want /v1/product (bare, namespace stripped), got %q", f.path)
	}
	if f.query.Get("limit") != "100" || f.query.Get("q") != "shirt" {
		t.Fatalf("store query must pass through verbatim, got %v", f.query)
	}
	if f.org != "acme" || f.auth != "Bearer svc-tok" {
		t.Fatalf("S2S must send X-Org-Id=acme (validated org) + the service token, got org=%q auth=%q", f.org, f.auth)
	}
}

func TestCommerce_ForwardsSubPathAndWriteBody(t *testing.T) {
	f := &fakeStore{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")

	// createProduct: POST /v1/commerce/product with a body → commerce /v1/product,
	// body forwarded verbatim, org bound to the caller.
	code, _ := callH(t, app, http.MethodPost, "/v1/commerce/product", alice,
		`{"name":"Tee","sku":"TEE-1","slug":"tee"}`)
	if code != http.StatusOK {
		t.Fatalf("POST want 200, got %d", code)
	}
	if f.method != http.MethodPost || f.path != "/v1/product" {
		t.Fatalf("want POST /v1/product, got %s %s", f.method, f.path)
	}
	if f.body["name"] != "Tee" || f.body["sku"] != "TEE-1" {
		t.Fatalf("write body must pass through verbatim, got %v", f.body)
	}
	if f.org != "acme" {
		t.Fatalf("write must be scoped to the caller's org acme, got %q", f.org)
	}

	// deleteProduct: DELETE /v1/commerce/product/<id> → commerce /v1/product/<id>.
	code, _ = callH(t, app, http.MethodDelete, "/v1/commerce/product/p_123", alice, "")
	if code != http.StatusOK {
		t.Fatalf("DELETE want 200, got %d", code)
	}
	if f.method != http.MethodDelete || f.path != "/v1/product/p_123" {
		t.Fatalf("want DELETE /v1/product/p_123, got %s %s", f.method, f.path)
	}
}

func TestCommerce_RefusesNonStoreHead(t *testing.T) {
	f := &fakeStore{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")

	// The bridge must NOT tunnel to commerce's money / tenant-admin surfaces: a
	// /v1/commerce/billing/* or /v1/commerce/checkout/* is 404'd BEFORE any upstream
	// call (billing has its OWN subject-scoped bridge; checkout is unreachable here).
	for _, p := range []string{"/v1/commerce/billing/balance", "/v1/commerce/checkout/session", "/v1/commerce/namespace"} {
		code, _ := callH(t, app, http.MethodGet, p, alice, "")
		if code != http.StatusNotFound {
			t.Fatalf("%s: want 404 (not a store endpoint), got %d", p, code)
		}
	}
	if f.path != "" {
		t.Fatalf("a refused head must never reach commerce, but upstream saw %q", f.path)
	}
}

func TestCommerce_RejectsTraversalSegment(t *testing.T) {
	f := &fakeStore{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")
	// Every traversal form — literal `..`, encoded slash (`%2f`), encoded dot
	// (`%2e%2e`) — must be REFUSED (400) and must NEVER reach commerce. Without the
	// percent-escape rejection, `product/..%2fbilling` decodes downstream to
	// `/v1/billing`, tunneling PAST the store-head allow-list into the money surface.
	for _, p := range []string{
		"/v1/commerce/product/../billing",
		"/v1/commerce/product/..%2fbilling",
		"/v1/commerce/%2e%2e/billing",
	} {
		code, _ := callH(t, app, http.MethodGet, p, alice, "")
		if code != http.StatusBadRequest {
			t.Fatalf("traversal %q: want 400, got %d", p, code)
		}
	}
	if f.path != "" {
		t.Fatalf("a traversal must never reach commerce, but upstream saw %q", f.path)
	}
}
