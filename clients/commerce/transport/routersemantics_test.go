package transport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// TestRouterSemantics establishes, by PROBE rather than by reading the router, the three
// behaviours that decide what happens if commerce's real /v1/billing/* routes are mounted
// alongside cloud's existing ones. Registration order here mirrors production: commerce
// mounts BEFORE billing (apps/apps.go — commerce at slice position 214, billing at 225),
// and fiber/zip walks its own stack, so "first" below means "commerce".
func TestRouterSemantics(t *testing.T) {
	probe := func(t *testing.T, register func(app *zip.App), path string) (status int, body string, panicked any) {
		t.Helper()
		defer func() { panicked = recover() }()
		app := zip.New(zip.Config{})
		register(app)
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("probe %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n]), nil
	}

	// CASE 1 — BYTE-IDENTICAL patterns. This is exactly commerce's GET /v1/billing/balance
	// vs cloud's GET /v1/billing/balance. Does it panic, or silently shadow?
	t.Run("byte identical", func(t *testing.T) {
		status, body, panicked := probe(t, func(app *zip.App) {
			app.Get("/v1/billing/balance", func(c *zip.Ctx) error { return c.Bytes(200, []byte("FIRST")) })
			app.Get("/v1/billing/balance", func(c *zip.Ctx) error { return c.Bytes(200, []byte("SECOND")) })
		}, "/v1/billing/balance")
		t.Logf("byte-identical => panicked=%v status=%d body=%q", panicked, status, body)
		if panicked != nil {
			t.Logf("VERDICT: duplicate registration PANICS — a boot-time failure, loud not silent")
			return
		}
		t.Logf("VERDICT: NO panic; %q wins => the loser is SILENTLY SHADOWED DEAD CODE", body)
	})

	// CASE 2 — equal specificity, DIFFERENT param names. If commerce registers
	// /v1/billing/:id where cloud has /v1/billing/:runId, does the binary panic AT BOOT?
	// That would be a production-down deploy gate.
	t.Run("equal specificity different param names", func(t *testing.T) {
		status, body, panicked := probe(t, func(app *zip.App) {
			app.Get("/v1/billing/:id", func(c *zip.Ctx) error { return c.Bytes(200, []byte("ID")) })
			app.Get("/v1/billing/:runId", func(c *zip.Ctx) error { return c.Bytes(200, []byte("RUNID")) })
		}, "/v1/billing/xyz")
		t.Logf("param-name conflict => panicked=%v status=%d body=%q", panicked, status, body)
		if panicked != nil {
			t.Logf("VERDICT: PANICS at registration — mounting a conflicting param route takes the binary DOWN AT BOOT")
			return
		}
		t.Logf("VERDICT: no panic; %q wins", body)
	})

	// CASE 3 — different specificity, registered WORST-first. Does a later, more specific
	// static route beat an earlier wildcard? This decides whether the account bridge's
	// /v1/billing/* can shadow a static commerce route.
	t.Run("wildcard registered before static", func(t *testing.T) {
		status, body, panicked := probe(t, func(app *zip.App) {
			app.Get("/v1/billing/*", func(c *zip.Ctx) error { return c.Bytes(200, []byte("WILDCARD")) })
			app.Get("/v1/billing/balance", func(c *zip.Ctx) error { return c.Bytes(200, []byte("STATIC")) })
		}, "/v1/billing/balance")
		t.Logf("wildcard-then-static => panicked=%v status=%d body=%q", panicked, status, body)
		switch body {
		case "STATIC":
			t.Logf("VERDICT: MOST-SPECIFIC wins regardless of registration order (ServeMux-1.22 semantics)")
		case "WILDCARD":
			t.Logf("VERDICT: FIRST REGISTRATION wins (registration-stack order)")
		}
	})
}
