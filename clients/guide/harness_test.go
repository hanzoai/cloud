package guide

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// newApp mounts the guide subsystem on a fresh zip.App with a temp DataDir. No AI
// or automations plane is wired, so the HTTP tests exercise the checklist engine,
// per-org store, curriculum loader, dependency gating, and auto-detect — the "do it
// for me" agent is exercised separately (agent_test.go) with injected fakes.
func newApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// Keep the real (store-backed) "acted" detector but drop "analytics" so the HTTP
	// tests never depend on a live warehouse. The analytics detector is exercised in
	// detect_test.go (firstCount + reconcile error semantics).
	mounted.State.detectors = map[string]Detector{"acted": mounted.State.detectors["acted"]}
	t.Cleanup(func() { _ = Shutdown() })
	return app
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

// testStore returns a real per-org *Store on a temp dir — the org isolation is
// physical, so this exercises the exact production open path.
func testStore(t *testing.T) *Store {
	t.Helper()
	stores := cloud.NewOrgStore(t.TempDir(), "guide", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	st, err := stores.For("acme", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	return v
}
