package o11y

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newHandler must forward the request path verbatim to the upstream and return
// its response — the behavior that turns the o11y 503 stub into real telemetry.
func TestNewHandlerProxiesPathVerbatim(t *testing.T) {
	var gotPath, gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer upstream.Close()

	h, err := newHandler(upstream.URL)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/o11y/v3/query_range", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// Path forwarded VERBATIM — the o11y runtime registers routes at their exact
	// public path (/v1/o11y/*). No /api/, no rewrite.
	if gotPath != "/v1/o11y/v3/query_range" {
		t.Fatalf("upstream path = %q, want /v1/o11y/v3/query_range (verbatim, no rewrite)", gotPath)
	}
	if gotHost == "api.hanzo.ai" {
		t.Fatalf("upstream Host = %q, want the upstream vhost (not the edge host)", gotHost)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
}

func TestNewHandlerRejectsBadURL(t *testing.T) {
	if _, err := newHandler("://nope"); err == nil {
		t.Fatal("expected error for malformed upstream URL")
	}
}

func TestUpstreamDefault(t *testing.T) {
	t.Setenv("O11Y_UPSTREAM", "")
	if got := upstream(); got != defaultUpstream {
		t.Fatalf("upstream() = %q, want default %q", got, defaultUpstream)
	}
	t.Setenv("O11Y_UPSTREAM", "http://example:9000")
	if got := upstream(); got != "http://example:9000" {
		t.Fatalf("upstream() = %q, want override", got)
	}
}

// A health endpoint must answer without a principal, or a readiness probe turns
// into a 403 and the pod never reports ready. /v1/o11y/health regressed exactly
// that way: isHealthPath enumerated only the runtime's own /api/… paths, so the
// generic HIP-0106 route fell through to the principal gate.
func TestGateServesHealthWithoutPrincipal(t *testing.T) {
	served := false
	h := gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))

	for _, p := range []string{
		"/v1/o11y/health", "/v1/o11y/health/", "/v1/sentry/health", "/health",
		"/v1/o11y/api/v1/health", "/v1/o11y/api/v2/healthz",
		"/v1/o11y/api/v2/readyz", "/v1/o11y/api/v2/livez",
	} {
		served = false
		rec := httptest.NewRecorder()
		// No X-User-Id: exactly what a kubelet or an external uptime check sends.
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai"+p, nil))
		if rec.Code != http.StatusOK || !served {
			t.Errorf("%s: got %d served=%v, want 200 served=true (health must be public)", p, rec.Code, served)
		}
	}
}

// The inverse, and the reason the gate exists: DATA reads stay principal-gated.
// A fix that opens health must not open telemetry.
func TestGateStillRefusesDataReadsWithoutPrincipal(t *testing.T) {
	h := gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("%s reached the handler without a principal", r.URL.Path)
	}))

	for _, p := range []string{
		"/v1/o11y/api/v1/query_range", "/v1/o11y/api/v3/query_range",
		"/v1/o11y/api/v1/logs", "/v1/sentry/issues",
		"/v1/o11y/healthcheck", "/v1/o11y/api/v1/health/detail",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai"+p, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403 (data reads stay gated)", p, rec.Code)
		}
	}
}
