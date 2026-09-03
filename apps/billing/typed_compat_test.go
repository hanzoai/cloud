package billing

// typed_compat_test.go pins what the typed money reads must not move: the
// contract. The ledger read keeps its status, its top-level JSON kind (a bare
// array stays a bare array) and the no-store cache discipline per-tenant money
// has always carried. The per-account breakdown keeps its 401/501 gates.

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/zap-proto/zip"
)

// callRoute drives a request like call() but also returns the response headers,
// so a test can see the cache discipline as well as the body.
func callRoute(t *testing.T, app *zip.App, method, path, user, org string) (int, http.Header, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func TestLedgerTyped_ContractUnmoved(t *testing.T) {
	ledgerPeer(t, "acme")
	f := &noHopCommerce{}
	app := mountApp(t, f.server(t).URL)

	status, hdr, body := callRoute(t, app, http.MethodGet, "/v1/billing/ledger?range=90d", "acme/dave", "acme")
	if status != http.StatusOK {
		t.Fatalf("ledger: want 200, got %d (%s)", status, body)
	}
	if got := hdr.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("per-tenant money must be no-store, got %q", got)
	}
	var first byte
	for _, b := range body {
		if b != ' ' && b != '\n' && b != '\t' {
			first = b
			break
		}
	}
	if first != '[' {
		t.Fatalf("a bare array must stay a bare array, got %s", body)
	}

	// A posting keeps its exact key set — nothing lost, nothing grown. The
	// optional balanceCents is omitted, so it is absent here by contract.
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode ledger: %v (%s)", err, body)
	}
	if len(rows) == 0 {
		t.Fatal("the fixture has postings; an empty page cannot pin a shape")
	}
	got := slices.Sorted(maps.Keys(rows[0]))
	want := []string{"account", "cents", "currency", "date", "description", "id"}
	if !slices.Equal(got, want) {
		t.Fatalf("posting keys = %v, want %v", got, want)
	}
}

// TestUsageAccountsTyped_KeepsGates pins the per-account breakdown's two
// refusals: an absent identity is 403 (no org scope), and a deployment without the
// linked-account plane is an honest 501 — never an empty breakdown.
func TestUsageAccountsTyped_KeepsGates(t *testing.T) {
	app := mountApp(t, "")
	if status, _, _ := callRoute(t, app, http.MethodGet, "/v1/billing/usage/accounts", "", "victim"); status != http.StatusForbidden {
		t.Fatalf("no principal: want 403, got %d", status)
	}
	if status, _, _ := callRoute(t, app, http.MethodGet, "/v1/billing/usage/accounts", "acme/dave", "acme"); status != http.StatusNotImplemented {
		t.Fatalf("no linked-account plane in this binary: want 501, got %d", status)
	}
}
