package connectorruntime

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (Mount says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing — and a test that skips it does not
// test a stricter program, it tests a program where every org-scoped op answers
// 403 for a reason that would never exist in production.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// newApp mounts connectorruntime ALONE — no automations group around it — which is
// what proves the subsystem composes on its own.
func newApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// post issues one run request. org != "" sets the gateway identity headers exactly
// as SanitizeIdentity would; org == "" sends NO identity — the anonymous-forge path.
func post(t *testing.T, app *zip.App, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(http.MethodPost, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestRunWire pins the route's wire: 403 with no principal, 404 for an unknown
// connector, 422 for a missing action, and — the infra-vs-piece split — HTTP 200
// with ok:false for an action the connector does not have (a piece-level failure
// the caller inspects, never an HTTP error).
func TestRunWire(t *testing.T) {
	app := newApp(t)

	if code, body := post(t, app, "/v1/auto/connectors/notion/run", "", map[string]any{"action": "x"}); code != http.StatusForbidden {
		t.Fatalf("no principal want 403, got %d (%s)", code, body)
	}
	if code, body := post(t, app, "/v1/auto/connectors/nope/run", "acme", map[string]any{"action": "x"}); code != http.StatusNotFound {
		t.Fatalf("unknown connector want 404, got %d (%s)", code, body)
	}
	if code, body := post(t, app, "/v1/auto/connectors/notion/run", "acme", map[string]any{}); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing action want 422, got %d (%s)", code, body)
	}
	code, body := post(t, app, "/v1/auto/connectors/notion/run", "acme", map[string]any{"action": "no_such_action"})
	if code != http.StatusOK {
		t.Fatalf("piece-level failure want 200, got %d (%s)", code, body)
	}
	var out runResp
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body: %v (%s)", err, body)
	}
	if out.Ok || out.Error == "" || !strings.Contains(out.Error, "no_such_action") {
		t.Fatalf("want ok:false naming the action, got %+v", out)
	}
}
