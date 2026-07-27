package storage_test

// Integration tests for the /v1/s3 file-manager subsystem, driven through the
// REAL orchestrator path (BuildDeps → the init()-registered AppSpec → the
// zip/Fiber stack), exactly like clients/kms/kms_test.go. Requests run in-process
// via app.Fiber().Test — no listener, no live SeaweedFS.
//
// SanitizeIdentity does not run in this harness (it is wired in serve.go, not
// MountAll), so a test simulates a validated principal by setting the identity
// headers SanitizeIdentity would emit (X-Org-Id, X-User-IsAdmin). In production
// those are stripped from client input and re-issued only for a JWT-validated
// principal, so the org gate is real; here we drive it directly.
//
// The handlers that touch S3 (list/create/delete/presign) cannot reach a real
// backend here, so these tests assert the SECURITY GATES that run BEFORE any S3
// call — fail-closed 503, org 403, name/key 400, and (the load-bearing one) that
// /v1/s3/buckets + /v1/s3/health reach the s3 subsystem's handlers and NOT
// provisioning's /v1/s3/:name. The live S3 round-trips are covered by the pure
// mapping tests (s3_internal_test.go) + live e2e post-deploy.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/fiber/v3"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"

	// Mount storage (118) then provisioning (120) IN ORDER via their composition-root
	// specs, so storage's static /v1/s3/buckets + /v1/s3/health register before
	// provisioning's /v1/s3/:name — the route-precedence guarantee, on the real Mounts.
	"github.com/hanzoai/cloud/clients/provisioning"
	"github.com/hanzoai/cloud/clients/storage"
)

// newApp wires BuildDeps + canonical middleware + MountAll, like main()'s path.
// `creds` toggles whether S3 admin credentials are present (fail-closed testing).
func newApp(t *testing.T, creds bool) *zip.App {
	t.Helper()
	// Clear any ambient S3 env; set creds only when requested.
	for _, k := range []string{"S3_ADMIN_ACCESS_KEY", "S3_ADMIN_SECRET_KEY", "S3_ADMIN_ENDPOINT", "S3_PUBLIC_ENDPOINT"} {
		t.Setenv(k, "")
	}
	if creds {
		t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
		t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
		// Point the internal endpoint at an unroutable host so any accidental live
		// call fails fast (the gates we assert run before it anyway).
		t.Setenv("S3_ADMIN_ENDPOINT", "127.0.0.1:1")
	}
	cfg := &cloud.Config{
		Brand:     "hanzo",
		Domain:    "api.hanzo.ai",
		IAMIssuer: "https://hanzo.id",
		DataDir:   t.TempDir(),
		// Enable both subsystems under test. (Empty Enable = all-on, but naming
		// them keeps the harness explicit and fast.)
		Enable: []string{"storage", "provisioning"},
	}
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	specs := []cloud.AppSpec{
		{Name: "storage", Mount: storage.Mount, OwnsHealth: true},
		{Name: "provisioning", Mount: provisioning.Mount},
	}
	if err := cloud.MountAll(app, specs, cfg, deps); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org, body string, admin bool) *http.Response {
	t.Helper()
	resp, _, err := doOrTimeout(app, method, path, org, body, admin)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// doOrTimeout issues a request and reports whether the handler timed out reaching
// the (deliberately unroutable) S3 backend. A timeout means the s3 handler RAN and
// attempted an S3 call — which is itself proof the route reached the s3 subsystem
// (provisioning's store-only :name handler never dials S3). `FailOnTimeout:false`
// so a slow S3 dial returns (nil, timedOut=true) instead of erroring the test.
func doOrTimeout(app *zip.App, method, path, org, body string, admin bool) (resp *http.Response, timedOut bool, err error) {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if admin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err = app.Fiber().Test(req, fiber.TestConfig{Timeout: 750 * time.Millisecond, FailOnTimeout: false})
	if err != nil && (strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "empty response")) {
		// The handler ran but the deliberately-unroutable S3 backend did not answer
		// within the window — from the router's view the s3 handler owned the route.
		return nil, true, nil
	}
	return resp, false, err
}

func decode(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	b, _ := io.ReadAll(r)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode json %q: %v", string(b), err)
	}
	return m
}

// TestHealthFailClosedWithoutCreds: absent S3 credentials the subsystem still
// mounts, but /v1/s3/health reports 503 (health-only) — never a silent 200.
func TestHealthFailClosedWithoutCreds(t *testing.T) {
	app := newApp(t, false)
	resp := do(t, app, "GET", "/v1/s3/health", "", "", false)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/s3/health (no creds) = %d, want 503", resp.StatusCode)
	}
	body := decode(t, resp.Body)
	if body["ready"] != false {
		t.Errorf("health ready=%v, want false", body["ready"])
	}
	if body["service"] != "s3" {
		t.Errorf("health service=%v, want s3 (NOT the generic liveness route)", body["service"])
	}
	if s, _ := body["error"].(string); !strings.Contains(s, "credentials") {
		t.Errorf("health error=%q, want it to name the missing credentials", s)
	}
}

// TestHealthOwnedByS3NotGenericLiveness: the real /v1/s3/health probe is the s3
// subsystem's, NOT serve.go's generic GET /v1/<name>/health (which would be a
// fake 200). Proven by: it 503s without creds AND returns 200 WITH creds carrying
// the s3-specific "presign" field. (serve.go's generic route is not mounted in
// this MountAll-only harness; in production the s3 subsystem registers with
// cloud.HealthOwner, so Serve skips the generic route entirely and this real
// probe owns /v1/s3/health.)
func TestHealthOwnedByS3NotGenericLiveness(t *testing.T) {
	app := newApp(t, true)
	resp := do(t, app, "GET", "/v1/s3/health", "", "", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/s3/health (creds) = %d, want 200", resp.StatusCode)
	}
	body := decode(t, resp.Body)
	if body["ready"] != true {
		t.Errorf("health ready=%v, want true", body["ready"])
	}
	if _, ok := body["presign"]; !ok {
		t.Error("health missing s3-specific 'presign' field — is this the generic route?")
	}
}

// TestBucketsRouteReachesS3NotProvisioning: THE load-bearing routing proof.
// provisioning owns GET /v1/s3/:name (order 120); the s3 subsystem owns the
// static GET /v1/s3/buckets (order 118). A request to /v1/s3/buckets must reach
// the s3 handler (which requires an org → 403 without one), NOT provisioning's
// list-resource-by-name handler (which would treat "buckets" as a resource name).
// The s3 403 body says "X-Org-Id required" from the s3 guard; provisioning's
// :name GET with a valid org would 404 "resource not found" for name "buckets".
// We assert the s3 path wins by checking the WITHOUT-org 403 (s3's guard fires
// first) — provisioning's GET /v1/s3/:name also 403s without org, so to
// disambiguate we ALSO assert that WITH an org the request does NOT return
// provisioning's "resource not found" 404 shape.
func TestBucketsRouteReachesS3NotProvisioning(t *testing.T) {
	app := newApp(t, true)

	// Without org: s3 guard → 403 "X-Org-Id required".
	resp := do(t, app, "GET", "/v1/s3/buckets", "", "", false)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /v1/s3/buckets (no org) = %d, want 403", resp.StatusCode)
	}

	// With org: the request reaches the s3 listBuckets handler, which dials the
	// (unroutable) S3 endpoint. Either it times out (proof: the s3 handler RAN and
	// tried an S3 call — provisioning's store-only :name never dials S3) or it
	// returns a 502/503. A 404 "resource not found" would mean provisioning's
	// :name won the route (broken ordering) — that is the failure we guard against.
	resp2, timedOut, err := doOrTimeout(app, "GET", "/v1/s3/buckets", "acme", "", false)
	if err != nil {
		t.Fatalf("GET /v1/s3/buckets (org): %v", err)
	}
	if timedOut {
		return // s3 handler reached S3 → route correctly owned by s3, not provisioning
	}
	if resp2.StatusCode == http.StatusNotFound {
		body := decode(t, resp2.Body)
		t.Fatalf("GET /v1/s3/buckets (org) = 404 %v — provisioning's :name shadowed the s3 route (ordering broken)", body)
	}
}

// TestHealthRouteReachesS3NotProvisioning: /v1/s3/health must be the s3 subsystem's
// real probe, not provisioning's GET /v1/s3/:name treating "health" as a resource
// name. Proven by the 503 (no creds) carrying the s3 health body, which
// provisioning's :name handler would never produce (it 403s without org or 404s
// a missing resource — never a {service:"s3", ready:false} health doc).
func TestHealthRouteReachesS3NotProvisioning(t *testing.T) {
	app := newApp(t, false)
	resp := do(t, app, "GET", "/v1/s3/health", "", "", false)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/s3/health = %d, want 503 (s3 health-only)", resp.StatusCode)
	}
	body := decode(t, resp.Body)
	if body["service"] != "s3" {
		t.Fatalf("GET /v1/s3/health body service=%v — provisioning's :name shadowed the health route", body["service"])
	}
}

// TestGateRequiresOrg: every mutating/reading op refuses without an org (403),
// before any S3 call. Covers the full route surface.
func TestGateRequiresOrg(t *testing.T) {
	app := newApp(t, true)
	cases := []struct{ method, path, body string }{
		{"GET", "/v1/s3/buckets", ""},
		{"POST", "/v1/s3/buckets", `{"name":"photos"}`},
		{"DELETE", "/v1/s3/buckets/photos", ""},
		{"GET", "/v1/s3/buckets/photos/objects", ""},
		{"POST", "/v1/s3/buckets/photos/objects", `{"key":"a.txt"}`},
		{"GET", "/v1/s3/buckets/photos/objects/a.txt", ""},
		{"DELETE", "/v1/s3/buckets/photos/objects/a.txt", ""},
	}
	for _, c := range cases {
		resp := do(t, app, c.method, c.path, "", c.body, false)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s (no org) = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestForgedOrgWithoutPrincipalRefused (RED HIGH): a request that carries a
// forged X-Org-Id but NO validated principal (no X-User-Id) — exactly the
// SanitizeIdentity "Phase-1 data path" residual an in-cluster caller could send
// with no bearer — must be refused 403, NOT granted cross-tenant access. The
// guard requires ctx.User() (X-User-Id), which SanitizeIdentity sets only for a
// validated bearer/cookie; the anonymous forge path leaves it empty. Every
// legitimate caller reaches s3 through the console BFF (which mints a user
// bearer), so this breaks no real client while closing the forge path.
func TestForgedOrgWithoutPrincipalRefused(t *testing.T) {
	app := newApp(t, true)
	cases := []struct{ method, path, body string }{
		{"GET", "/v1/s3/buckets", ""},
		{"POST", "/v1/s3/buckets", `{"name":"photos"}`},
		{"DELETE", "/v1/s3/buckets/victim-bucket", ""},
		{"GET", "/v1/s3/buckets/victim-bucket/objects", ""},
		{"POST", "/v1/s3/buckets/victim-bucket/objects", `{"key":"a.txt"}`},
		{"DELETE", "/v1/s3/buckets/victim-bucket/objects/secret.pdf", ""},
	}
	for _, c := range cases {
		var rdr io.Reader
		if c.body != "" {
			rdr = strings.NewReader(c.body)
		}
		req := httptest.NewRequest(c.method, c.path, rdr)
		if c.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		// Forge the victim's org WITHOUT any validated principal (no X-User-Id) —
		// the exact anonymous in-cluster attack.
		req.Header.Set("X-Org-Id", "victim-org")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s (forged org, no principal) = %d, want 403 — cross-tenant forge NOT closed!", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestFailClosedWhenUnconfigured: with no creds, every op (not just health) is a
// fail-closed 503 — the subsystem never fabricates a result.
func TestFailClosedWhenUnconfigured(t *testing.T) {
	app := newApp(t, false)
	// A fully-authorized request (org present) still 503s because the backend is
	// unconfigured — the guard's fail-closed check fires before the org check
	// reaches an S3 call.
	resp := do(t, app, "GET", "/v1/s3/buckets", "acme", "", false)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/s3/buckets (no creds, with org) = %d, want 503", resp.StatusCode)
	}
}

// TestCreateBucketValidatesName: a bad bucket name is a 400 before any S3 call.
func TestCreateBucketValidatesName(t *testing.T) {
	app := newApp(t, true)
	bad := []string{`{"name":"UPPER"}`, `{"name":"has space"}`, `{"name":"-lead"}`, `{"name":""}`, `{"name":"under_score"}`}
	for _, body := range bad {
		resp := do(t, app, "POST", "/v1/s3/buckets", "acme", body, false)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST /v1/s3/buckets %s = %d, want 400", body, resp.StatusCode)
		}
	}
	// Malformed JSON → 400.
	resp := do(t, app, "POST", "/v1/s3/buckets", "acme", `{not json`, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /v1/s3/buckets (bad json) = %d, want 400", resp.StatusCode)
	}
}

// TestObjectKeyTraversalRejected: a traversal key on presign/delete is a 400
// before any S3 call — the object-key guard.
func TestObjectKeyTraversalRejected(t *testing.T) {
	app := newApp(t, true)
	// Presign upload with a traversal key.
	resp := do(t, app, "POST", "/v1/s3/buckets/photos/objects", "acme", `{"key":"../../etc/passwd"}`, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("presign upload traversal key = %d, want 400", resp.StatusCode)
	}
	// Presign upload with an empty key.
	resp = do(t, app, "POST", "/v1/s3/buckets/photos/objects", "acme", `{"key":""}`, false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("presign upload empty key = %d, want 400", resp.StatusCode)
	}
}

// TestBadBucketParamRejected: a malformed bucket path param is a 400 before any
// physical name is derived (covers the object-listing + delete routes).
func TestBadBucketParamRejected(t *testing.T) {
	app := newApp(t, true)
	// An uppercase bucket param fails friendlyParam.
	resp := do(t, app, "GET", "/v1/s3/buckets/BADNAME/objects", "acme", "", false)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET objects with bad bucket param = %d, want 400", resp.StatusCode)
	}
}
