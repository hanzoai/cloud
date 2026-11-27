package billing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeCommerce stands in for the commerce billing S2S surface. It records the
// Authorization header, the trusted X-Org-Id selector, and the exact query it
// received (so a test can prove the subject is pinned to the CALLER's own org and
// a client-forged subject/org never reaches commerce), and answers a canned body
// so a test can prove the handler passes commerce's body + status through verbatim.
type fakeCommerce struct {
	mu        sync.Mutex
	gotAuth   string
	gotOrg    string
	gotPath   string
	gotMethod string
	gotQuery  url.Values
	gotBody   []byte
	// canned response
	status int
	body   string
}

func (f *fakeCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.gotAuth = r.Header.Get("Authorization")
		f.gotOrg = r.Header.Get("X-Org-Id")
		f.gotPath = r.URL.Path
		f.gotMethod = r.Method
		f.gotQuery = r.URL.Query()
		f.gotBody = reqBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/usage", h)
	mux.HandleFunc("/v1/billing/balance", h)
	mux.HandleFunc("/v1/billing/portal/methods", h)
	mux.HandleFunc("/v1/billing/portal/methods/", h) // the {id} sub-resource
	mux.HandleFunc("/v1/billing/methods", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountApp mounts the billing surface.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	// The shared anti-forgery key this surface's writes verify against. A deployment
	// takes it from KMS; the mount refuses without it (key_test.go).
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// The composer's install, once at the root, ahead of every route it serves:
	// cloud.Bridge parks the validated org on the context for the typed finance ops.
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_BRAND", "hanzo")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestUsage_NoValidatedPrincipal_401_NeverTouchesCommerce(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{"usage":[]}`}
	app := mountApp(t)
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

// A fleet that runs no commerce has no ledger to read: 503, an outage, never an empty
// list and never the 501 that once answered every process but commerce's own.
func TestUsage_NoCommerce_503(t *testing.T) {
	noFinance(t)
	planeDir(t)
	app := mountApp(t)
	code, _ := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no commerce: want 503, got %d", code)
	}
}
