package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// newApp mounts the automations subsystem on a fresh zip.App with a temp DataDir,
// mirroring clients/integrations' test harness. No KMS/engine is wired: the HTTP
// tests exercise the org gate, the store, the catalogue, and the core-connector MCP
// dispatch — none of which need custody or the durable engine.
func newApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// newAppWithAudit mounts the subsystem with a REAL in-memory audit recorder so a
// test can read the tamper-evident trail back and assert outcomes (LOW-1) and
// exactly-once run bookkeeping (MED-1). Returns the recorder for querying.
func newAppWithAudit(t *testing.T) (*zip.App, *audit.Recorder) {
	t.Helper()
	rec, err := audit.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Audit: rec}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app, rec
}

// auditCount returns how many records match (org, action) in the recorder.
func auditCount(t *testing.T, rec *audit.Recorder, org, action string) int {
	t.Helper()
	rows, _, err := rec.Query(context.Background(), audit.Filter{Org: org, Action: action, Limit: 1000})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	return len(rows)
}

type httpResult struct {
	Code int
	Body []byte
}

// req issues one request. org != "" sets the gateway identity headers (X-Org-Id +
// a validated X-User-Id) exactly as SanitizeIdentity would; org == "" sends NO
// identity — the anonymous-forge path the 403 tests need.
func req(t *testing.T, app *zip.App, method, path, org string, body any) httpResult {
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
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: b}
}

// reqRaw is req for a raw (already-encoded) JSON body — used by the MCP JSON-RPC tests.
func reqRaw(t *testing.T, app *zip.App, path, org string, raw string) httpResult {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(raw)))
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: b}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir() + "/automations.db")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
