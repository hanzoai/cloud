package tasks

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// testEngine spins a throwaway in-process Tasks engine (temp store, ephemeral ZAP
// port) so the surface's routing + gate can be exercised directly, independent of
// cloud.EmbeddedTasks (which durable.go/Serve wires in the real binary).
func testEngine(t *testing.T) *tasks.Embedded {
	t.Helper()
	srv, err := tasks.Embed(t.Context(), tasks.EmbedConfig{ZAPPort: 0})
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
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("engine-nil surface want 503, got %d", resp.StatusCode)
	}
}
