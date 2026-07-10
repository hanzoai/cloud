package o11y

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// scopeApp builds the scoped o11y read surface (the three static routes) exactly as
// mountScope registers them, so the tests exercise the real handlers.
func scopeApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/o11y/logs", handleLogs)
	app.Get("/v1/o11y/metrics", handleMetrics)
	app.Get("/v1/o11y/status", handleStatus)
	return app
}

// authReq builds a request with a VALIDATED principal (X-User-Id set, as
// SanitizeIdentity would from a verified token) for org.
func scopeReq(method, path, org string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	return req
}

func do(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ── the principal gate fails closed on every scoped read ───────────────────────

func TestScopedReadsRequireValidatedPrincipal(t *testing.T) {
	app := scopeApp(t)
	// A forged request: X-Org-Id present (as the bearer-less path would restore) but
	// NO X-User-Id → not a validated principal → 403 on every surface.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/o11y/logs?product=kms"},
		{"GET", "/v1/o11y/metrics?product=kms"},
		{"GET", "/v1/o11y/status?product=kms"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-Org-Id", "victim") // forged, no validated user
		code, _ := do(t, app, req)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s: want 403 for forged (no X-User-Id), got %d", tc.method, tc.path, code)
		}
	}
}

// ── product validation rejects traversal/injection at the boundary ─────────────

func TestScopedProductValidation(t *testing.T) {
	app := scopeApp(t)
	// Malformed slugs are a boundary 400 on every read (never smuggle injection/SSRF).
	for _, bad := range []string{"KMS", "a b", "a'b", `a"b`, "../etc", "up{} or", "a}b"} {
		for _, path := range []string{"/v1/o11y/logs", "/v1/o11y/metrics", "/v1/o11y/status"} {
			req := scopeReq("GET", path+"?product="+url.QueryEscape(bad), "acme")
			code, _ := do(t, app, req)
			if code != http.StatusBadRequest {
				t.Fatalf("%s product=%q: want 400, got %d", path, bad, code)
			}
		}
	}
	// A missing product is also a 400.
	code, _ := do(t, app, scopeReq("GET", "/v1/o11y/metrics", "acme"))
	if code != http.StatusBadRequest {
		t.Fatalf("missing product: want 400, got %d", code)
	}
}

// ── a well-formed but unbacked product is honest-empty, never an error ─────────

func TestUnknownProductHonestEmpty(t *testing.T) {
	app := scopeApp(t)
	// "not-a-real-service" is a valid slug but not in the allowlist → honest-empty.
	code, body := do(t, app, scopeReq("GET", "/v1/o11y/logs?product=not-a-real-service", "acme"))
	if code != http.StatusOK {
		t.Fatalf("unknown product logs: want 200 honest-empty, got %d %s", code, body)
	}
	var lr logsResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lr.Lines) != 0 {
		t.Fatalf("unknown product must return no lines, got %d", len(lr.Lines))
	}

	// status of an unknown product is honest down/unknown-service — no probe.
	code, body = do(t, app, scopeReq("GET", "/v1/o11y/status?product=not-a-real-service", "acme"))
	if code != http.StatusOK {
		t.Fatalf("unknown product status: want 200, got %d %s", code, body)
	}
	var st statusResult
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Up || st.Source != "unknown-service" {
		t.Fatalf("unknown product must be down/unknown-service, got up=%v source=%s", st.Up, st.Source)
	}
}

// ── aliases resolve console slugs to their workload (cloud-api → cloud, o11y) ───

func TestProductAliasResolution(t *testing.T) {
	for _, tc := range []struct {
		slug, wantApp string
	}{
		{"cloud-api", "cloud"},
		{"api", "cloud"},
		{"llm", "gateway"},
		{"router", "gateway"},
		{"analytics", "insights-capture"},
		{"observe", "o11y"},
		{"o11y", "o11y"},
		{"kms", "kms"}, // identity — no alias needed
	} {
		svc, ok := resolveService(tc.slug)
		if !ok {
			t.Fatalf("slug %q should resolve to a known workload", tc.slug)
		}
		if svc.App != tc.wantApp || svc.PromService != tc.wantApp {
			t.Fatalf("slug %q → app=%q promService=%q, want %q", tc.slug, svc.App, svc.PromService, tc.wantApp)
		}
		// The ID stays the client-facing slug (so the response echoes what was asked).
		if svc.ID != tc.slug {
			t.Fatalf("slug %q → ID=%q, want the slug preserved", tc.slug, svc.ID)
		}
	}
	// An ill-formed alias key never resolves.
	if _, ok := resolveService("O11Y"); ok {
		t.Fatal("uppercase slug must not resolve")
	}
	// A well-formed but unbacked/unaliased product does not resolve.
	if _, ok := resolveService("definitely-not-a-service"); ok {
		t.Fatal("unbacked product must not resolve")
	}
}

// ── the scoped routes WIN over the hanzoai/o11y wildcard proxy (precedence) ─────
//
// This reproduces the PRODUCTION Fiber route stack for the /v1/o11y/* surface:
// mountScope (inside the one order-69 `o11y` mount) registers its three EXACT GET
// routes FIRST, then hanzoai/o11y (order 70) registers the catch-all
// All("/v1/o11y/*") wildcard. A
// request that reaches the UNSCOPED wildcard (sentinel 599) is a route-precedence
// bypass of tenant scoping. Every scoped path/method must land on the scoped
// handler, never the sentinel.
func TestRoutePrecedence_ScopedWinsOverWildcard(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// order 69: the scoped GET handlers.
	app.Get("/v1/o11y/logs", handleLogs)
	app.Get("/v1/o11y/metrics", handleMetrics)
	app.Get("/v1/o11y/status", handleStatus)
	// order 70: the hanzoai/o11y catch-all wildcard (SENTINEL: 599).
	app.All("/v1/o11y/*", func(c *zip.Ctx) error { return c.String(599, "REACHED-UNSCOPED-WILDCARD") })

	for _, path := range []string{
		"/v1/o11y/logs?product=kms",
		"/v1/o11y/metrics?product=kms",
		"/v1/o11y/status?product=kms",
	} {
		req := scopeReq("GET", path, "acme")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == 599 {
			t.Fatalf("%s reached the UNSCOPED wildcard proxy — tenant-scoping precedence bypass", path)
		}
	}
}
