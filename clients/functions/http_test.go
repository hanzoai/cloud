package functions

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// do fires an in-process request. org is stamped as X-Org-Id — the header the
// gateway's SanitizeIdentity injects from the validated principal in prod; here
// we set it directly to exercise the subsystem's own tenant scoping.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (tenant() gates on it)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestHTTPTenantGateAndIsolation proves at the HTTP layer: no org → 403; a
// function created under one org is invisible + inaccessible to another.
func TestHTTPTenantGateAndIsolation(t *testing.T) {
	app := mountApp(t)

	// No org header → the tenant() gate refuses (403), never leaks an empty list.
	if code, _ := do(t, app, http.MethodGet, "/v1/functions", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	// maxpower creates a function.
	code, _ := do(t, app, http.MethodPost, "/v1/functions", "maxpower",
		map[string]any{"name": "resize", "runtime": "python", "code": "print('hi')"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}

	// maxpower sees it; the list shape is {functions:[...]}.
	code, body := do(t, app, http.MethodGet, "/v1/functions", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d", code)
	}
	var listed struct {
		Functions []functionView `json:"functions"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("list json: %v (%s)", err, body)
	}
	if len(listed.Functions) != 1 || listed.Functions[0].Name != "resize" {
		t.Fatalf("maxpower should see [resize], got %+v", listed.Functions)
	}
	if listed.Functions[0].Endpoint != "/v1/functions/resize/invoke" {
		t.Fatalf("endpoint should be the invoke URL, got %q", listed.Functions[0].Endpoint)
	}

	// acme (different org) must NOT see maxpower's function.
	code, body = do(t, app, http.MethodGet, "/v1/functions", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("acme list want 200, got %d", code)
	}
	_ = json.Unmarshal(body, &listed)
	if len(listed.Functions) != 0 {
		t.Fatalf("acme must see zero functions, got %+v", listed.Functions)
	}

	// acme cannot GET or DELETE maxpower's function — it is not found for acme.
	if code, _ := do(t, app, http.MethodGet, "/v1/functions/resize", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme GET maxpower fn want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/functions/resize", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme DELETE maxpower fn want 404, got %d", code)
	}

	// maxpower's function survived acme's delete attempt.
	if code, _ := do(t, app, http.MethodGet, "/v1/functions/resize", "maxpower", nil); code != http.StatusOK {
		t.Fatalf("maxpower fn must survive, got %d", code)
	}
}

// TestHTTPInvokeFailsClosed proves invoke never fabricates output when the
// sandbox isn't configured — it returns 503 (no CODE_EXEC_UPSTREAM in tests).
func TestHTTPInvokeFailsClosed(t *testing.T) {
	app := mountApp(t)
	do(t, app, http.MethodPost, "/v1/functions", "maxpower",
		map[string]any{"name": "job", "runtime": "python", "code": "print(1)"})
	code, body := do(t, app, http.MethodPost, "/v1/functions/job/invoke", "maxpower", map[string]any{"input": "x"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("invoke with no sandbox want 503, got %d (%s)", code, body)
	}
}

// TestHTTPStaticRoutesNotShadowed proves /metrics is not captured by :name.
func TestHTTPStaticRoutesNotShadowed(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/functions/metrics", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("metrics want 200, got %d", code)
	}
	if !bytes.Contains(body, []byte("series")) || !bytes.Contains(body, []byte("costCents")) {
		t.Fatalf("metrics shape wrong: %s", body)
	}
}
