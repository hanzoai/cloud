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
