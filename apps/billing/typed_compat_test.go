package billing

// typed_compat_test.go pins what the finance conversion to typed ops must not
// move: the contract. Every /v1/finance read keeps its path, its status, its
// top-level JSON kind (a bare array stays a bare array), its exact key set
// where the shape is an object, and the no-store cache discipline per-tenant
// money has always carried. The per-account breakdown keeps its 401/501 gates.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
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

func TestFinanceTyped_ContractUnmoved(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")

	for _, tc := range []struct {
		path  string
		array bool // a bare array stays a bare array
	}{
		{"/v1/finance/balance", false},
		{"/v1/finance/credits", true},
		{"/v1/finance/usage?range=24h", false},
		{"/v1/finance/invoices", true},
		{"/v1/finance/payment-methods", true},
		{"/v1/finance/ledger?range=90d", true},
	} {
		status, hdr, body := callRoute(t, app, http.MethodGet, tc.path, "acme/dave", "acme")
		if status != http.StatusOK {
			t.Fatalf("%s: want 200, got %d (%s)", tc.path, status, body)
		}
		if got := hdr.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: per-tenant money must be no-store, got %q", tc.path, got)
		}
		var first byte
		for _, b := range body {
			if b != ' ' && b != '\n' && b != '\t' {
				first = b
				break
			}
		}
		if tc.array && first != '[' {
			t.Fatalf("%s: a bare array must stay a bare array, got %s", tc.path, body)
		}
		if !tc.array && first != '{' {
			t.Fatalf("%s: want a JSON object, got %s", tc.path, body)
		}
	}

	// The balance object keeps its exact key set — nothing lost, nothing grown.
	_, _, body := callRoute(t, app, http.MethodGet, "/v1/finance/balance", "acme/dave", "acme")
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode balance: %v (%s)", err, body)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"asOf", "availableCents", "currency", "dueCents", "pendingCents"}
	if len(got) != len(want) {
		t.Fatalf("balance keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("balance keys = %v, want %v", got, want)
		}
	}
}

// TestUsageAccountsTyped_KeepsGates pins the per-account breakdown's two
// refusals: an absent identity is 401 "sign in", and a deployment without the
// linked-account plane is an honest 501 — never an empty breakdown.
func TestUsageAccountsTyped_KeepsGates(t *testing.T) {
	app := mountApp(t, "", "")
	if status, _, _ := callRoute(t, app, http.MethodGet, "/v1/billing/usage/accounts", "", "victim"); status != http.StatusUnauthorized {
		t.Fatalf("no principal: want 401, got %d", status)
	}
	if status, _, _ := callRoute(t, app, http.MethodGet, "/v1/billing/usage/accounts", "acme/dave", "acme"); status != http.StatusNotImplemented {
		t.Fatalf("no linked-account plane in this binary: want 501, got %d", status)
	}
}
