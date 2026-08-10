package admission

// The guard's mode read, over the REAL Mount. Nothing measured this route before,
// so nothing measured the one behaviour that makes it usable from a guard sitting
// in front of a governed host: with no ?host= it answers for the host the REQUEST
// was addressed to. A typed op receives only a context, so that fallback now
// crosses on cloud.Bridge — and if it stops crossing the route still answers 200,
// with known=false for every caller that relied on the default. Only this notices.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountGate installs the launch gate against a temp registry for the hanzo brand,
// through Mount itself — the routes a binary serves, not a reconstruction.
func mountGate(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir(), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// ask drives GET /v1/flags/waitlist. hostHeader "" leaves the request's own Host
// as httptest set it.
func ask(t *testing.T, app *zip.App, url, hostHeader string) waitlistModeView {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if hostHeader != "" {
		req.Host = hostHeader
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d (%s), want 200 — this route fails OPEN", url, resp.StatusCode, b)
	}
	var v waitlistModeView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("shape: %v (%s)", err, b)
	}
	return v
}

// TestQueriedHostResolvesToItsService — the explicit ?host= form the @file guard
// caches. The seeded hanzo brand governs chat.hanzo.ai, and the launch posture is
// GATED until an admin opens it.
func TestQueriedHostResolvesToItsService(t *testing.T) {
	app := mountGate(t)
	v := ask(t, app, "/v1/flags/waitlist?host=chat.hanzo.ai", "")
	if !v.Known || v.Service != "chat" {
		t.Fatalf("chat.hanzo.ai = %+v, want known chat", v)
	}
	if !v.WaitlistMode {
		t.Errorf("waitlistMode = false, want the launch posture (gated) for a service no admin has opened")
	}
	if v.Host != "chat.hanzo.ai" {
		t.Errorf("host = %q, want the normalized query host", v.Host)
	}
}

// TestHostIsNormalized — the answer echoes the host lowercased and port-stripped,
// which is what makes a guard's cache key stable across the forms a browser sends.
func TestHostIsNormalized(t *testing.T) {
	app := mountGate(t)
	v := ask(t, app, "/v1/flags/waitlist?host=CHAT.hanzo.ai:443", "")
	if v.Host != "chat.hanzo.ai" || v.Service != "chat" {
		t.Fatalf("normalization lost: %+v", v)
	}
}

// TestOmittedHostFallsBackToTheREQUESTHost is THE reason this op reaches for the
// request. A guard running on the governed host asks with no argument at all, and
// the route has always answered for the Host header. A typed op cannot see a header
// — so if that fallback is ever dropped, this call quietly becomes known=false and
// every such guard silently stops gating.
func TestOmittedHostFallsBackToTheREQUESTHost(t *testing.T) {
	app := mountGate(t)
	v := ask(t, app, "/v1/flags/waitlist", "chat.hanzo.ai")
	if !v.Known || v.Service != "chat" {
		t.Fatalf("no-query request from chat.hanzo.ai = %+v, want known chat — the Host fallback is gone", v)
	}
	if v.Host != "chat.hanzo.ai" {
		t.Errorf("host = %q, want the request's own", v.Host)
	}
}

// TestUngovernedHostFailsOpen — an unregistered host is honestly unknown, at 200,
// so a guard lets the request through rather than gating on a registry it cannot
// resolve.
func TestUngovernedHostFailsOpen(t *testing.T) {
	app := mountGate(t)
	v := ask(t, app, "/v1/flags/waitlist?host=example.com", "")
	if v.Known || v.Service != "" || v.WaitlistMode {
		t.Fatalf("example.com = %+v, want unknown/open", v)
	}
}

// TestNoCredentialRequired — the route answers for the ONE host asked about and
// never enumerates, which is why a gated (not yet approved) user can still resolve
// their own mode.
func TestNoCredentialRequired(t *testing.T) {
	app := mountGate(t)
	if v := ask(t, app, "/v1/flags/waitlist?host=api.hanzo.ai", ""); v.Service != "api" {
		t.Fatalf("anonymous read = %+v, want the api service", v)
	}
}
