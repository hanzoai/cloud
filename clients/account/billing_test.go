package account

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// billing_test.go — the per-tenant billing bridge (billing.go). Proves the tenant
// scoping that prevents cross-tenant billing reads (the Go port of console's
// billing-scope.test.ts) AND the IDOR-safe handler forwarding.

// ── pure scoping ─────────────────────────────────────────────────────────────

func TestBillingSubject(t *testing.T) {
	cases := []struct{ org, name, want string }{
		{"acme", "alice", "acme"},  // any member bills the ONE org account
		{"hanzo", "Dave", "hanzo"}, // no per-user wallet; org, lowercased
		{"hanzo", "z", "hanzo"},    // another member — same org account
		{"hanzo", "", "hanzo"},     // no name → org
		{"Hanzo", "z", "hanzo"},    // lowercased
		{"", "x", ""},              // no org → empty subject
	}
	for _, c := range cases {
		if got := billingSubject(c.org, c.name); got != c.want {
			t.Fatalf("billingSubject(%q,%q): want %q, got %q", c.org, c.name, c.want, got)
		}
	}
}

// TestBillingSubject_IgnoresLegacyEnv locks that the killed allowlist envs have NO
// effect: the subject is ALWAYS the org, whether or not the old PERSONAL_BILLING_ORGS
// / ORG_BILLING_ORGS knobs are set. This mirrors ai/object.BillingSubject (one rule,
// no config) so the console view and the gateway gate can never disagree.
func TestBillingSubject_IgnoresLegacyEnv(t *testing.T) {
	t.Setenv("PERSONAL_BILLING_ORGS", "hanzo,acme")
	t.Setenv("ORG_BILLING_ORGS", "hanzo")
	cases := []struct{ org, name, want string }{
		{"hanzo", "z", "hanzo"},
		{"acme", "alice", "acme"},
		{"maxpower", "dave", "maxpower"},
	}
	for _, c := range cases {
		if got := billingSubject(c.org, c.name); got != c.want {
			t.Fatalf("legacy env must be ignored: billingSubject(%q,%q) want %q, got %q", c.org, c.name, c.want, got)
		}
	}
}

func TestScopedBillingSearch_PinsSubjectDropsOrgKeepsRest(t *testing.T) {
	in := url.Values{}
	in.Set("userId", "victim")     // forged subject — must be OVERWRITTEN
	in.Set("customerId", "victim") // forged subject — must be OVERWRITTEN
	in.Set("user", "victim")       // forged subject — must be OVERWRITTEN
	in.Set("org", "othercorp")     // must be DROPPED (cannot widen scope)
	in.Set("currency", "usd")      // non-subject — must PASS THROUGH
	in.Set("start", "2026-01-01")  // non-subject — must PASS THROUGH

	out := scopedBillingSearch(in, "acme")

	for _, k := range billingSubjectKeys {
		if out.Get(k) != "acme" {
			t.Fatalf("subject key %s must be pinned to acme, got %q", k, out.Get(k))
		}
	}
	if out.Has("org") {
		t.Fatal("org must be dropped")
	}
	if out.Get("currency") != "usd" || out.Get("start") != "2026-01-01" {
		t.Fatalf("non-subject params must pass through, got %v", out)
	}
}

func TestScopedBillingBody(t *testing.T) {
	// a forged subject in a JSON object write is OVERWRITTEN; other fields survive.
	out := scopedBillingBody([]byte(`{"userId":"victim","amount":500,"note":"x"}`), "acme")
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("scoped body is not JSON: %v", err)
	}
	for _, k := range billingSubjectKeys {
		if obj[k] != "acme" {
			t.Fatalf("body subject key %s must be pinned to acme, got %v", k, obj[k])
		}
	}
	if obj["amount"].(float64) != 500 || obj["note"] != "x" {
		t.Fatalf("non-subject body fields must survive, got %v", obj)
	}

	// non-object / empty bodies pass through untouched (never invent a body).
	for _, raw := range []string{"not json", `[1,2,3]`, `"scalar"`, ""} {
		if got := string(scopedBillingBody([]byte(raw), "acme")); got != raw {
			t.Fatalf("non-object body %q must pass through, got %q", raw, got)
		}
	}
}

// ── handler (IDOR + forwarding) ──────────────────────────────────────────────

// fakeBilling records exactly what the handler forwarded to commerce.
type fakeBilling struct {
	mu    sync.Mutex
	path  string
	query url.Values
	body  map[string]any
	org   string
	auth  string
}

func (f *fakeBilling) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.path, f.query = r.URL.Path, r.URL.Query()
		f.org, f.auth = r.Header.Get("X-Org-Id"), r.Header.Get("Authorization")
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &f.body)
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"balance":123}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBilling_RequiresValidatedPrincipal(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")
	// A forged X-Org-Id with NO validated X-User-Id is the exact forge — refuse it
	// BEFORE any commerce call (no cross-tenant ledger read on a victim org).
	code, _ := callH(t, app, http.MethodGet, "/v1/billing/balance", map[string]string{"X-Org-Id": "victim"}, "")
	if code != http.StatusForbidden {
		t.Fatalf("no validated principal: want 403, got %d", code)
	}
}

func TestBilling_NotConfiguredWithoutToken(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	app := mountApp(t, "http://iam.invalid", "", "")
	code, _ := callH(t, app, http.MethodGet, "/v1/billing/balance", alice, "")
	if code != http.StatusNotImplemented {
		t.Fatalf("no commerce token: want 501, got %d", code)
	}
}

func TestBilling_ScopesQueryToCallerAndForwards(t *testing.T) {
	f := &fakeBilling{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")

	// alice/acme with a FORGED ?userId=victim & ?org=othercorp: the handler must pin
	// every subject key to the caller's own subject (acme) and drop org.
	code, body := callH(t, app, http.MethodGet,
		"/v1/billing/invoices?userId=victim&customerId=victim&org=othercorp&status=open", alice, "")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	if f.path != "/v1/billing/invoices" {
		t.Fatalf("forwarded path: want /v1/billing/invoices, got %q", f.path)
	}
	for _, k := range billingSubjectKeys {
		if f.query.Get(k) != "acme" {
			t.Fatalf("commerce must receive %s=acme (the caller's subject), got %q", k, f.query.Get(k))
		}
	}
	if f.query.Has("org") {
		t.Fatal("org must be dropped before commerce")
	}
	if f.query.Get("status") != "open" {
		t.Fatalf("non-subject filter must pass through, got %q", f.query.Get("status"))
	}
	if f.org != "acme" || f.auth != "Bearer svc-tok" {
		t.Fatalf("S2S must send X-Org-Id=acme + the service token, got org=%q auth=%q", f.org, f.auth)
	}
}

func TestBilling_ScopesWriteBodyToCaller(t *testing.T) {
	f := &fakeBilling{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")

	// a POST with a forged userId in the body must be overwritten to acme.
	code, _ := callH(t, app, http.MethodPost, "/v1/billing/spend-alerts", alice,
		`{"userId":"victim","threshold":5000}`)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if f.body["userId"] != "acme" {
		t.Fatalf("write-body subject must be pinned to acme, got %v", f.body["userId"])
	}
	if f.body["threshold"].(float64) != 5000 {
		t.Fatalf("non-subject body field must survive, got %v", f.body["threshold"])
	}
}

func TestBilling_RejectsTraversalSegment(t *testing.T) {
	f := &fakeBilling{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")
	// Every traversal form — literal `..`, encoded slash (`%2f`), encoded dot
	// (`%2e%2e`), matrix param (`;`) — must be REFUSED (400) and must NEVER reach
	// commerce. Without the percent-escape rejection, `invoices/..%2fadmin` decodes
	// downstream to `/v1/admin`, tunneling PAST /v1/billing into another surface.
	for _, p := range []string{
		"/v1/billing/invoices/../admin",   // literal ..
		"/v1/billing/invoices/..%2fadmin", // encoded slash (%2f)
		"/v1/billing/%2e%2e/admin",        // encoded dots (%2e%2e)
		"/v1/billing/invoices;statement",  // matrix param (;)
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
