package o11y

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestRed_O11yProxyGatesForgedOrgNoPrincipal is the twin of the bot/crm forge
// guards, for the /v1/o11y/* reverse proxy. An off-gateway caller forges X-Org-Id
// with NO validated principal (no X-User-Id — the state the identity middleware
// leaves on the bearer-less path). The bare reverse proxy would forward that
// forged tenant to the o11y runtime (cross-tenant telemetry/logs); gate() must
// refuse it 403 before the upstream is ever reached, and must still pass a
// request that carries a validated principal.
func TestRed_O11yProxyGatesForgedOrgNoPrincipal(t *testing.T) {
	var reached atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer upstream.Close()

	h, err := newHandler(upstream.URL)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	gated := gate(h)

	// Forged org, NO X-User-Id → must 403, upstream never reached.
	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/o11y/v3/query_range", nil)
	req.Header.Set("X-Org-Id", "victim") // forged; no X-User-Id → no validated principal
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-principal forged /v1/o11y/* = HTTP %d, want 403 (proxy must gate like every data-plane resolver)", rec.Code)
	}
	if reached.Load() {
		t.Fatal("o11y runtime received a NO-PRINCIPAL forged request — cross-tenant telemetry hole forwarded through cloud's /v1/o11y proxy")
	}

	// A validated principal (X-User-Id set by the identity middleware) passes through.
	req2 := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/o11y/v3/query_range", nil)
	req2.Header.Set("X-Org-Id", "acme")
	req2.Header.Set("X-User-Id", "u_acme") // validated principal
	rec2 := httptest.NewRecorder()
	gated.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("validated /v1/o11y/* = HTTP %d, want 200 (gate must pass real principals)", rec2.Code)
	}
	if !reached.Load() {
		t.Fatal("validated request did not reach the o11y runtime — gate is over-blocking legitimate callers")
	}
}

// Health/liveness endpoints carry no tenant data and the o11y runtime serves them
// without identity (that is how the k8s pod probes pass). The gate MUST let them
// through unauthenticated so the admin System Health probe (CLOUD_O11Y_HEALTH_URL)
// and the external o11y.* hosts keep working after the embed serves in-process —
// while still blocking unauthenticated DATA routes.
func TestGateExemptsHealthPathsButGatesData(t *testing.T) {
	var reached atomic.Bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	gated := gate(backend)

	// Health under the /v1/o11y external prefix, NO principal → must pass (200).
	for _, p := range []string{
		"/v1/o11y/api/v1/health",
		"/v1/o11y/api/v2/livez",
		"/v1/o11y/api/v2/readyz",
	} {
		reached.Store(false)
		req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai"+p, nil)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unauth health %s = HTTP %d, want 200 (health must bypass the principal gate)", p, rec.Code)
		}
		if !reached.Load() {
			t.Fatalf("unauth health %s did not reach the runtime — gate over-blocking health", p)
		}
	}

	// A data route with NO principal must STILL be gated (403), backend never reached.
	reached.Store(false)
	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/o11y/api/v1/query_range", nil)
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauth data route = HTTP %d, want 403 (data must stay gated)", rec.Code)
	}
	if reached.Load() {
		t.Fatal("unauth data route reached the runtime — health exemption leaked to data paths")
	}
}

// The Sentry error-ingest WRITE endpoints authenticate with a DSN key downstream in
// the o11y handler (not a Hanzo principal), so gate() MUST let a principal-less POST
// to /v1/o11y/api/<project>/envelope|store/ through — else the tokenless ingest the
// gateway allowlists is 403'd here and error-tracking can never ingest. The exemption
// MUST stay tight: reads (GET, and the Issues list/detail/update) keep the principal
// gate, so it can't be widened into a cross-tenant read hole.
func TestGateExemptsErrorIngestButGatesReads(t *testing.T) {
	var reached atomic.Bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	gated := gate(backend)

	// DSN-authed ingest WRITEs, NO principal → must pass (the handler's DSN check authenticates).
	for _, p := range []string{
		"/v1/o11y/api/hanzo/envelope/",
		"/v1/o11y/api/hanzo/store/",
		"/v1/o11y/api/00000000-0000-0000-0000-000000000000/envelope", // slash-less tolerated
	} {
		reached.Store(false)
		req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai"+p, nil)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unauth ingest %s = HTTP %d, want 200 (DSN-authed ingest must bypass the principal gate)", p, rec.Code)
		}
		if !reached.Load() {
			t.Fatalf("unauth ingest %s did not reach the runtime — gate over-blocking DSN ingest", p)
		}
	}

	// These MUST stay gated (403) with no principal — the exemption must not leak to reads.
	type gc struct{ method, path string }
	for _, c := range []gc{
		{http.MethodGet, "/v1/o11y/api/errortracking/issues"},      // Issues LIST (read)
		{http.MethodGet, "/v1/o11y/api/errortracking/issues/abc"},  // Issue detail (read)
		{http.MethodPost, "/v1/o11y/api/errortracking/issues/abc"}, // Issue UPDATE (write, not ingest)
		{http.MethodGet, "/v1/o11y/api/hanzo/envelope/"},           // ingest is POST-only
		{http.MethodPost, "/v1/o11y/api/v1/query_range"},           // data query, not ingest suffix
	} {
		reached.Store(false)
		req := httptest.NewRequest(c.method, "http://api.hanzo.ai"+c.path, nil)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("unauth %s %s = HTTP %d, want 403 (ingest exemption must not leak to reads/updates)", c.method, c.path, rec.Code)
		}
		if reached.Load() {
			t.Fatalf("unauth %s %s reached the runtime — ingest exemption leaked", c.method, c.path)
		}
	}
}

// TestGateExemptsSentryIngestButGatesReads is the Sentry sibling: the DSN-authenticated
// /v1/sentry/{project}/envelope|store WRITE bypasses the principal gate (the runtime's
// DSN check authenticates), while EVERY Sentry read/write API stays principal-gated —
// the ingest exemption must not leak to projects/issues/discover/logs/traces/stats.
func TestGateExemptsSentryIngestButGatesReads(t *testing.T) {
	var reached atomic.Bool
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	gated := gate(backend)

	// DSN-authed ingest WRITEs, NO principal → must pass (the runtime's DSN check authenticates).
	for _, p := range []string{
		"/v1/sentry/00000000-0000-0000-0000-000000000000/envelope/",
		"/v1/sentry/00000000-0000-0000-0000-000000000000/store/",
		"/v1/sentry/00000000-0000-0000-0000-000000000000/envelope", // slash-less tolerated
	} {
		reached.Store(false)
		req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai"+p, nil)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("unauth sentry ingest %s = HTTP %d, want 200 (DSN-authed ingest must bypass the principal gate)", p, rec.Code)
		}
		if !reached.Load() {
			t.Fatalf("unauth sentry ingest %s did not reach the runtime — gate over-blocking DSN ingest", p)
		}
	}

	// These MUST stay gated (403) with no principal — the exemption must not leak.
	type gc struct{ method, path string }
	for _, c := range []gc{
		{http.MethodGet, "/v1/sentry/issues"},                                         // Issues LIST (read)
		{http.MethodGet, "/v1/sentry/projects"},                                       // Projects LIST (read)
		{http.MethodPost, "/v1/sentry/projects"},                                      // Project CREATE (write, not ingest)
		{http.MethodPost, "/v1/sentry/discover"},                                      // Discover (read query, not ingest suffix)
		{http.MethodGet, "/v1/sentry/logs"},                                           // Logs (read)
		{http.MethodGet, "/v1/sentry/traces"},                                         // Traces (read)
		{http.MethodGet, "/v1/sentry/stats"},                                          // Stats (read)
		{http.MethodGet, "/v1/sentry/00000000-0000-0000-0000-000000000000/envelope/"}, // ingest is POST-only
	} {
		reached.Store(false)
		req := httptest.NewRequest(c.method, "http://api.hanzo.ai"+c.path, nil)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("unauth %s %s = HTTP %d, want 403 (sentry ingest exemption must not leak to reads/writes)", c.method, c.path, rec.Code)
		}
		if reached.Load() {
			t.Fatalf("unauth %s %s reached the runtime — sentry ingest exemption leaked", c.method, c.path)
		}
	}
}
