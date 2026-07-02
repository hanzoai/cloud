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
