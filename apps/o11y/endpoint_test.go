package o11y

// THE SURFACE, END TO END — the test that was missing while it refused.
//
// TestGateExemptsHealthPathsButGatesData (red_forge_test.go) calls gate()
// DIRECTLY, with a backend that answers 200 to anything. That proves the
// predicate and nothing else: it passed for months while
// api.hanzo.ai/v1/o11y/version answered
//   403 {"status":403,"error":"no validated principal"}
// because the refusal was never gate() in isolation — it was the whole chain.
// /version, /health and /global/config are TYPED ops, so a request reaches them
// through the zip route table, cloud.Bridge, the op, and only then relay ->
// gate; when the gate refused, relay re-wrapped the refusal as a zip.HTTPError,
// which is why those three answered zip's envelope while /livez, /healthz and
// /readyz — which fall through to the same gate directly — answered gate()'s own
// {"status":"error","msg":...}. Two shapes, one refuser, and a unit test on the
// refuser could not see either.
//
// So this exercises the REAL mount: Mount, the real route table, the real
// gate, against a runtime that actually routes. An anonymous caller must get the
// runtime's answer on the tenant-free reads and a refusal everywhere else.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// endpointApp mounts the whole surface with the runtime pointed at a stand-in that
// ROUTES — the reverse-proxy backing, whose upstream is a real server here
// rather than an in-cluster Service. Everything in front of it (route table,
// Bridge, typed ops, relay, gate) is production's.
func endpointApp(t *testing.T) *zip.App {
	t.Helper()
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/o11y/version":
			_, _ = w.Write([]byte(`{"version":"v1.5.49","ee":"N","setupCompleted":true}`))
		case "/v1/o11y/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			// The runtime's OWN refusal, so a test failure distinguishes "the
			// surface refused" from "the runtime refused".
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"error","error":{"code":"unauthenticated"}}`))
		}
	}))
	t.Cleanup(runtime.Close)

	t.Setenv("O11Y_PROBES", "false")
	t.Setenv("O11Y_UPSTREAM", runtime.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// Standing in for the composer: cloud.Bridge is installed once at the root,
	// ahead of the mount, exactly where cloud.Serve puts it. The mount does not
	// install one — a subsystem cannot know the identity boundary has already run
	// — so a fixture that means to reproduce production has to supply it here or
	// the chain it claims to exercise is missing a link.
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = shutdownAnnotationQueues() })
	return app
}

// TestEndpointServesTenantFreeReadsAnonymously is the lane's assertion: no
// credentials, and the two reads a console makes BEFORE it has a session answer
// the runtime's own bytes.
func TestEndpointServesTenantFreeReadsAnonymously(t *testing.T) {
	app := endpointApp(t)

	for _, tc := range []struct{ path, want string }{
		{"/v1/o11y/version", `"ee":"N"`},
		{"/v1/o11y/health", `"status":"ok"`},
	} {
		code, body := get(t, app, tc.path)
		if code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d %s, want 200 — the unified surface must serve what "+
				"the standalone serves; a principal cannot be required to learn what you are "+
				"talking to, or to pass a kubelet probe", tc.path, code, body)
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("anonymous GET %s body = %s, want it to contain %s (the RUNTIME's answer, "+
				"not a placeholder from this surface)", tc.path, body, tc.want)
		}
	}
}

// TestEndpointStillRefusesTenantReads is the other half, and the one that matters
// more: if everything answers 200 the gate is gone, which is worse than the 403
// this lane closed. A read of a TENANT's telemetry must still be refused, and
// the refusal must be the SURFACE's, not the runtime's 401 — proving the request
// never reached the runtime at all.
func TestEndpointStillRefusesTenantReads(t *testing.T) {
	app := endpointApp(t)

	for _, path := range []string{
		"/v1/o11y/dashboards",
		"/v1/o11y/stats",
		"/v1/o11y/licenses",
		"/v1/o11y/features",
	} {
		code, body := get(t, app, path)
		if code != http.StatusForbidden {
			t.Errorf("anonymous GET %s = %d %s, want 403 — a tenant's telemetry is not a "+
				"tenant-free read", path, code, body)
			continue
		}
		if !strings.Contains(body, "no validated principal") {
			t.Errorf("anonymous GET %s refused with %s, want the SURFACE's own reason; anything "+
				"else means the request reached the runtime before being refused", path, body)
		}
	}
}

// TestEndpointExemptionIsTheModulesAnswer pins WHERE the exempt set lives. This repo
// kept its own copy once — /v1/o11y/api/v1/health and three /api/v2 siblings,
// the internal namespace hanzoai/o11y stopped rewriting onto at v1.5.37 — so the
// list named four addresses no route served and the gate refused every real
// public op behind it. The fact belongs beside the routes; if it ever moves back
// here, this fails.
func TestEndpointExemptionIsTheModulesAnswer(t *testing.T) {
	for _, dead := range []string{
		"/v1/o11y/api/v1/health",
		"/v1/o11y/api/v2/healthz",
		"/v1/o11y/api/v2/readyz",
		"/v1/o11y/api/v2/livez",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai"+dead, nil)
		rec := httptest.NewRecorder()
		gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("%s was exempted — that namespace has not existed since o11y v1.5.37; "+
				"an exemption for a route nobody serves is how this gate ended up refusing every real one", dead)
		}
	}
}
