package tasks

import (
	"github.com/hanzoai/cloud/internal/planetest"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// testEngine spins a throwaway in-process Tasks engine (temp store, its own unix
// socket) so the surface's routing + gate can be exercised directly, independent of
// cloud.EmbeddedTasks (which durable.go/Serve wires in the real binary).
func testEngine(t *testing.T) *tasks.Embedded {
	t.Helper()
	dir := planetest.Dir(t)
	srv, err := tasks.Embed(t.Context(), tasks.EmbedConfig{
		Address: filepath.Join(dir, "tasks.sock"),
		DataDir: dir,
	})
	if err != nil {
		t.Fatalf("tasks.Embed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(t.Context()) })
	return srv
}

// req drives one request through h. A non-empty org is presented as a VALIDATED
// principal — principal-grade trust requires X-User-Id (which the gateway sets
// only from a verified credential), so gate honors X-Org-Id only when X-User-Id
// is also present.
func req(t *testing.T, h http.Handler, method, path, org string, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rq)
	return rec.Code, rec.Body.String()
}

// TestSurfaceOpenAndGated proves the route composition on the shared engine:
// settings is open, the data surface refuses an unvalidated principal (403, never
// the unscoped store), and probes stay reachable.
func TestSurfaceOpenAndGated(t *testing.T) {
	mux := httpMux(testEngine(t))

	if code, body := req(t, mux, http.MethodGet, "/v1/tasks/settings", "", nil); code != http.StatusOK {
		t.Fatalf("open GET /v1/tasks/settings want 200, got %d: %s", code, body)
	}
	if code, body := req(t, mux, http.MethodGet, "/v1/tasks/namespaces", "", nil); code != http.StatusForbidden {
		t.Fatalf("unvalidated GET /v1/tasks/namespaces want 403, got %d: %s", code, body)
	}
}

// TestOrglessValidatedPrincipalIsRefused pins the request that used to walk
// straight past the gate: a VALIDATED principal (X-User-Id, minted only from
// verified claims) carrying NO org.
//
// cloud's identity boundary produces exactly this shape on purpose —
// SanitizeIdentity sets X-User-Id from the claims but leaves X-Org-Id unset when
// homeOrg() is empty (auth_identity.go:289, a token with no `orgs` claim: a
// pre-IAM-v1.33.0 human JWT or a non-KMS machine token) — so that every org()
// gate refuses it rather than guessing a tenant. A `user != ""` gate is not that
// rule: it admitted the request with org "", and the engine reads the empty org
// as the ZERO Principal, i.e. the shared UNSCOPED store. Measured before the fix:
// an org-less caller registered namespace "leak" (200) and a DIFFERENT org-less
// caller listed it back — one store shared by every principal cloud could not
// resolve an org for.
//
// Both directions are asserted, because the fix is worthless if it also refuses
// the caller that DOES carry an org.
func TestOrglessValidatedPrincipalIsRefused(t *testing.T) {
	mux := httpMux(testEngine(t))

	orgless := func(method string, body any) (int, string) {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		rq := httptest.NewRequest(method, "/v1/tasks/namespaces", r)
		if body != nil {
			rq.Header.Set("Content-Type", "application/json")
		}
		rq.Header.Set("X-User-Id", "u-legacy") // validated user, NO X-Org-Id
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, rq)
		return rec.Code, rec.Body.String()
	}

	create := map[string]any{
		"namespaceInfo": map[string]any{"name": "leak", "state": "NAMESPACE_STATE_REGISTERED"},
		"config":        map[string]any{"workflowExecutionRetentionTtl": "24h"},
	}
	if code, body := orgless(http.MethodPost, create); code != http.StatusForbidden {
		t.Fatalf("org-less validated POST /v1/tasks/namespaces want 403, got %d: %s", code, body)
	}
	if code, body := orgless(http.MethodGet, nil); code != http.StatusForbidden {
		t.Fatalf("org-less validated GET /v1/tasks/namespaces want 403, got %d: %s", code, body)
	}

	// The refusal envelope is the ENGINE's, not zip's — the gate answers in the
	// same shape as every sibling path behind the wildcard (typed_wire_test.go
	// TestEngineErrorEnvelopeIsNotZips), so closing the hole moved no bytes.
	code, body := orgless(http.MethodGet, nil)
	if !strings.Contains(body, `"code":403`) {
		t.Fatalf("refusal body = %d %s, want the engine envelope with a numeric code", code, body)
	}

	// An org-bearing validated principal is unaffected.
	if code, body := req(t, mux, http.MethodGet, "/v1/tasks/namespaces", "acme", nil); code != http.StatusOK {
		t.Fatalf("org-scoped GET /v1/tasks/namespaces want 200, got %d: %s", code, body)
	}
}

// TestNamespaceRoundTripIsOrgScoped proves the identity bridge threads the
// validated tenant into the engine: a namespace created under "acme" is visible to
// "acme" and INVISIBLE to "other" — the per-(org,ns) shard isolation.
func TestNamespaceRoundTripIsOrgScoped(t *testing.T) {
	mux := httpMux(testEngine(t))

	create := map[string]any{
		"namespaceInfo": map[string]any{"name": "smoke", "state": "NAMESPACE_STATE_REGISTERED"},
		"config":        map[string]any{"workflowExecutionRetentionTtl": "24h"},
	}
	if code, body := req(t, mux, http.MethodPost, "/v1/tasks/namespaces", "acme", create); code != http.StatusOK {
		t.Fatalf("POST namespace (acme) want 200, got %d: %s", code, body)
	}

	code, body := req(t, mux, http.MethodGet, "/v1/tasks/namespaces", "acme", nil)
	if code != http.StatusOK || !strings.Contains(body, "smoke") {
		t.Fatalf("GET namespaces (acme) want 200 containing \"smoke\", got %d: %s", code, body)
	}

	code, body = req(t, mux, http.MethodGet, "/v1/tasks/namespaces", "other", nil)
	if code != http.StatusOK {
		t.Fatalf("GET namespaces (other) want 200, got %d: %s", code, body)
	}
	if strings.Contains(body, "smoke") {
		t.Fatalf("tenant isolation breach: \"other\" sees acme's namespace: %s", body)
	}
}

// TestMountFailSoftWhenEngineNil proves Mount registers cleanly and the lazy
// surface fails soft (503) while cloud.EmbeddedTasks is nil (engine not yet wired
// / embed failed) — never a panic, never another tenant's data.
func TestMountFailSoftWhenEngineNil(t *testing.T) {
	if cloud.EmbeddedTasks() != nil {
		t.Skip("engine already wired in this process")
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	rq := httptest.NewRequest(http.MethodGet, "/v1/tasks/settings", nil)
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("engine-nil surface want 503, got %d", resp.StatusCode)
	}
}
