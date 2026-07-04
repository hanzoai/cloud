package billing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeCommerce stands in for the commerce billing S2S surface. It records the
// Authorization header, the trusted X-Org-Id selector, and the exact query it
// received (so a test can prove the subject is pinned to the CALLER's own org and
// a client-forged subject/org never reaches commerce), and answers a canned body
// so a test can prove the handler passes commerce's body + status through verbatim.
type fakeCommerce struct {
	mu       sync.Mutex
	gotAuth  string
	gotOrg   string
	gotPath  string
	gotQuery url.Values
	// canned response
	status int
	body   string
}

func (f *fakeCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.gotAuth = r.Header.Get("Authorization")
		f.gotOrg = r.Header.Get("X-Org-Id")
		f.gotPath = r.URL.Path
		f.gotQuery = r.URL.Query()
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/usage", h)
	mux.HandleFunc("/v1/billing/balance", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountApp mounts the billing surface against the fake commerce at base, with the
// service token wired (unless token is ""). base "" leaves commerce unconfigured.
func mountApp(t *testing.T, base, token string) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_COMMERCE_HTTP_URL", base)
	t.Setenv("COMMERCE_SERVICE_TOKEN", token)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// call drives a request. A non-empty user injects a VALIDATED principal (X-User-Id,
// which the gateway sets ONLY from a verified credential) with org as X-Org-Id.
func call(t *testing.T, app *zip.App, method, path, user, org string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestUsage_ScopedToCallerOrg_PassesThroughVerbatim(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"user":"maxpower","count":1,"usage":[{"transactionId":"t1","amount":42,"metadata":{"model":"gpt-4o-mini"},"createdAt":"2026-07-01T00:00:00Z"}]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("usage: want 200, got %d (%s)", code, body)
	}
	if string(body) != f.body {
		t.Fatalf("usage body not verbatim:\n got %s\nwant %s", body, f.body)
	}
	// commerce saw the caller's OWN org as both the S2S selector and the pinned subject.
	if f.gotOrg != "maxpower" {
		t.Fatalf("commerce X-Org-Id: want maxpower, got %q", f.gotOrg)
	}
	if got := f.gotQuery.Get("user"); got != "maxpower" {
		t.Fatalf("commerce subject: want user=maxpower, got %q", got)
	}
	if f.gotAuth != "Bearer svc-token" {
		t.Fatalf("commerce auth: want service token, got %q", f.gotAuth)
	}
	if f.gotPath != "/v1/billing/usage" {
		t.Fatalf("commerce path: want /v1/billing/usage, got %q", f.gotPath)
	}
}

func TestUsage_ClientCannotWidenScope(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"usage":[]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	// A malicious client forges every subject key + a foreign org in the query.
	code, _ := call(t, app, http.MethodGet,
		"/v1/billing/usage?user=victim&userId=victim&customerId=victim&org=other",
		"maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	// The subject is PINNED to the caller's own org; the forged keys never reach commerce.
	if got := f.gotQuery.Get("user"); got != "maxpower" {
		t.Fatalf("forged user must be overwritten with the caller org: got %q", got)
	}
	for _, k := range []string{"userId", "customerId", "org"} {
		if f.gotQuery.Has(k) {
			t.Fatalf("client-forged %q must NOT be forwarded to commerce (got %q)", k, f.gotQuery.Get(k))
		}
	}
	if f.gotOrg != "maxpower" {
		t.Fatalf("X-Org-Id must be the caller's own org, got %q", f.gotOrg)
	}
}

func TestUsage_PassthroughStartEnd(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"usage":[]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, _ := call(t, app, http.MethodGet, "/v1/billing/usage?start=2026-07-01&end=2026-07-03", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d", code)
	}
	if f.gotQuery.Get("start") != "2026-07-01" || f.gotQuery.Get("end") != "2026-07-03" {
		t.Fatalf("start/end must pass through: got start=%q end=%q", f.gotQuery.Get("start"), f.gotQuery.Get("end"))
	}
	if f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("subject still pinned: got %q", f.gotQuery.Get("user"))
	}
}

func TestBalance_ScopedAndCurrency(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"balance":2046200,"holds":0,"available":2046200}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/billing/balance?currency=usd", "maxpower/dave", "maxpower")
	if code != 200 || string(body) != f.body {
		t.Fatalf("balance: want 200 verbatim, got %d (%s)", code, body)
	}
	if f.gotPath != "/v1/billing/balance" || f.gotOrg != "maxpower" || f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("balance scope: path=%q org=%q user=%q", f.gotPath, f.gotOrg, f.gotQuery.Get("user"))
	}
	if f.gotQuery.Get("currency") != "usd" {
		t.Fatalf("currency must pass through: got %q", f.gotQuery.Get("currency"))
	}
}

func TestUsage_NoValidatedPrincipal_401_NeverTouchesCommerce(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"usage":[]}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	// A forged X-Org-Id with NO validated principal (no X-User-Id) must be refused
	// as 401 — never admin-gated, never served — and commerce must never be reached.
	code, _ := call(t, app, http.MethodGet, "/v1/billing/usage", "", "victim")
	if code != http.StatusUnauthorized {
		t.Fatalf("no principal: want 401, got %d", code)
	}
	if f.gotOrg != "" {
		t.Fatalf("commerce must NOT be reached on the unauthenticated path, saw org=%q", f.gotOrg)
	}
}

func TestUsage_CommerceUnconfigured_501(t *testing.T) {
	app := mountApp(t, "", "") // no commerce base/token
	code, _ := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != http.StatusNotImplemented {
		t.Fatalf("unconfigured: want 501, got %d", code)
	}
}

func TestUsage_CommerceStatusForwarded(t *testing.T) {
	// A non-2xx from commerce (e.g. a real 500) is forwarded verbatim, not masked.
	f := &fakeCommerce{status: http.StatusInternalServerError, body: `{"error":"boom"}`}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != http.StatusInternalServerError || !strings.Contains(string(body), "boom") {
		t.Fatalf("commerce status must pass through: got %d (%s)", code, body)
	}
}
