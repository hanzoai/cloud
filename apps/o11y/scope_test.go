package o11y

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// scopeApp builds the scoped o11y read surface (the three typed reads) exactly as
// Mount + mountScope register them, and stands in for the composer by
// installing cloud.Bridge at the root first — so the validated org reaches a typed
// op the same way it does in the real process and the tests exercise the real
// handlers.
func scopeApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	mountScopedReads(app)
	return app
}

// mountScopedReads is the typed reads, registered exactly as mountScope does —
// including the PRODUCT prefix the RED read moved to, so a test that asks for
// /v1/o11y/metrics here misses for the same reason it misses in the real process.
// Shared by the tests that need only this slice of the surface.
func mountScopedReads(app *zip.App) {
	g := app.Group(o11yPrefix)
	zip.Get(g, "/status", handleStatus)
	zip.Get(g, "/availability", handleAvailability)
	zip.Get(app.Group(productPrefix), "/metrics", handleMetrics)
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
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ── identity reaches a typed op through EVERY entry point ──────────────────────

// A typed op receives a context and its decoded input — never the request — so it
// sees its caller only because something parked the principal on the context.
// cloud.Bridge does that for a routed request, and it used to be the ONLY thing
// that did: a bare app answered 403 to a caller the host had already validated,
// which is what this test pinned.
//
// That was a total outage wherever the Bridge is not in front, and there are two
// such places: o11y as its OWN binary (plugin/o11y/main.go builds a bare zip.App
// and calls Mount — a context value does not cross the host↔plugin socket),
// and MCP's tools/call, which invokes an op DIRECTLY so no route middleware runs.
// principal.ValidatedFrom now falls back to zip's own caller, which crosses every
// entry point, so both are served.
//
// The fallback widens the WAY IN, not the TRUST, and the distinction is load-bearing:
// cloud installs SanitizeIdentity app-wide (app.go, `app.Use(IdentityMiddleware)`),
// and it DELETES every client-sent authority header before any handler reads one.
// So an X-User-Id a typed op sees was minted by cloud from a verified token.
//
// That reasoning covers cloud's OWN handlers and nothing else. It does not extend
// to a subsystem cloud PROXIES to — hanzoai/metrics resolved its own tenant from
// the raw X-Org-Id and was reachable unauthenticated, which is a different defect
// with a different fix (build.go hands it principal.Org). A trust boundary is a
// property of a process, not of a header name.
func TestTypedOpsSeeTheirCallerThroughEveryDoor(t *testing.T) {
	const path = "/v1/o11y/status?product=not-a-real-service"

	// No Bridge — the standalone binary, and the tools/call path.
	bare := zip.New(zip.Config{Logger: luxlog.New("test")})
	mountScopedReads(bare)
	if code, body := do(t, bare, scopeReq("GET", path, "acme")); code != http.StatusOK {
		t.Fatalf("without cloud.Bridge: want 200 (the principal rides zip's caller), got %d %s", code, body)
	}

	// With it — the shape cloud.App installs — the SAME request is served.
	if code, body := do(t, scopeApp(t), scopeReq("GET", path, "acme")); code != http.StatusOK {
		t.Fatalf("with cloud.Bridge: want 200 for a validated caller, got %d %s", code, body)
	}

	// And with NO principal, both shapes refuse. This is the assertion that must
	// never flip: the way in got wider, the trust did not.
	for _, app := range []*zip.App{bare, scopeApp(t)} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Org-Id", "acme") // an org with nobody behind it
		if code, body := do(t, app, req); code != http.StatusForbidden {
			t.Fatalf("no validated principal: want 403, got %d %s", code, body)
		}
	}
}

// ── the principal gate fails closed on every scoped read ───────────────────────

func TestScopedReadsRequireValidatedPrincipal(t *testing.T) {
	app := scopeApp(t)
	// A forged request: X-Org-Id present (as the bearer-less path would restore) but
	// NO X-User-Id → not a validated principal → 403 on every surface.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/o11y/product/metrics?product=kms"},
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
		for _, path := range []string{"/v1/o11y/product/metrics", "/v1/o11y/status"} {
			req := scopeReq("GET", path+"?product="+url.QueryEscape(bad), "acme")
			code, _ := do(t, app, req)
			if code != http.StatusBadRequest {
				t.Fatalf("%s product=%q: want 400, got %d", path, bad, code)
			}
		}
	}
	// A missing product is also a 400.
	code, _ := do(t, app, scopeReq("GET", "/v1/o11y/product/metrics", "acme"))
	if code != http.StatusBadRequest {
		t.Fatalf("missing product: want 400, got %d", code)
	}
}

// ── a well-formed but unbacked product is honest-empty, never an error ─────────

func TestUnknownProductHonestEmpty(t *testing.T) {
	app := scopeApp(t)
	// "not-a-real-service" is a valid slug but not in the allowlist → honest-empty.
	code, body := do(t, app, scopeReq("GET", "/v1/o11y/product/metrics?product=not-a-real-service", "acme"))
	if code != http.StatusOK {
		t.Fatalf("unknown product metrics: want 200 honest-empty, got %d %s", code, body)
	}
	var mr metricsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(mr.Series.Requests) != 0 {
		t.Fatalf("unknown product must return no series, got %d", len(mr.Series.Requests))
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
		{"event", "insights-capture"},
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

// ── the scoped reads own their addresses, and ONLY their addresses ─────────────
//
// This used to pin PRECEDENCE: mountScope registered its exact GET routes first,
// hanzoai/o11y then registered an All("/v1/o11y/*") catch-all, and the test proved
// the scoped handler won. That stack is gone — the module names every one of its
// routes and has no catch-all left (its mount.go says so), which is exactly why
// three addresses stopped being a silent shadow and became a refusal to compose.
//
// So the invariant flipped, and both halves matter:
//
//   - the addresses cloud DOES declare must reach cloud's tenant-pinned handler,
//     never a fall-through (a sentinel wildcard stands in for one here);
//   - the addresses cloud VACATED must fall through, because falling through is
//     what lets the module's own read answer there. Re-adding a cloud route at one
//     of them is the regression this half catches, and it is the same mistake that
//     took the whole subsystem down.
func TestScopedReadsOwnTheirAddressesAndOnlyTheirs(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	// order 69: the scoped GET handlers.
	mountScopedReads(app)
	// Stands in for whatever else is mounted under the prefix (SENTINEL: 599).
	app.All("/v1/o11y/*", func(c *zip.Ctx) error { return c.String(599, "FELL-THROUGH") })

	sentinel := func(t *testing.T, path string) bool {
		t.Helper()
		// THE DEADLINE MUST EXCEED THE HANDLER'S OWN BUDGET, or this asks a question
		// it has not left time to answer. /v1/o11y/status probes each deployment over
		// HTTP with a healthProbeTimeout budget (status.go), and zip's Test defaults to
		// ONE second — so the handler is permitted to take twice as long as the test
		// waits for it. Under load, or wherever those probes are unreachable, this
		// failed with "i/o timeout" at an address that had ROUTED perfectly well, which
		// is the one thing this test is about.
		resp, err := app.Test(scopeReq("GET", path, "acme"), zip.TestConfig{Timeout: 4 * healthProbeTimeout})
		if err != nil {
			t.Fatalf("Test %s: %v", path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 599
	}

	// Ours: must be answered by the tenant-pinned handler.
	for _, path := range []string{
		"/v1/o11y/product/metrics?product=kms",
		"/v1/o11y/status?product=kms",
		"/v1/o11y/availability",
	} {
		if sentinel(t, path) {
			t.Errorf("%s fell through — cloud's tenant-scoped read is not answering its own address", path)
		}
	}

	// The module's: cloud must NOT answer here. A non-sentinel means a cloud route
	// came back at an address the module declares, which is a compose failure in
	// the real binary rather than a wrong answer.
	for _, path := range []string{
		"/v1/o11y/logs?product=kms",
		"/v1/o11y/metrics?product=kms",
	} {
		if !sentinel(t, path) {
			t.Errorf("%s is served by cloud's scoped mount — that address belongs to hanzoai/o11y, "+
				"and declaring it again is what refuses to compose", path)
		}
	}
}
