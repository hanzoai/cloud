package o11ysvc

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
	if gotPath != "/api/v3/query_range" {
		t.Fatalf("upstream path = %q, want /api/v3/query_range (/v1/o11y/* -> /api/*)", gotPath)
	}
	if gotHost == "api.hanzo.ai" {
		t.Fatalf("upstream Host = %q, want the upstream vhost (not the edge host)", gotHost)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
}

func TestRewritePath(t *testing.T) {
	cases := map[string]string{
		"/v1/o11y/v3/query_range": "/api/v3/query_range",
		"/v1/o11y/v1/version":     "/api/v1/version",
		"/v1/o11y/":               "/api/",
		"/v1/o11y":                "/api",
	}
	for in, want := range cases {
		if got := rewritePath(in); got != want {
			t.Errorf("rewritePath(%q) = %q, want %q", in, got, want)
		}
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
