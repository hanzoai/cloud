package o11y

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestNewHandlerNamesAnUnreachableUpstream pins the ONE thing an operator reads
// when the fallback's Deployment is gone.
//
// httputil.ReverseProxy's default ErrorHandler answers 502 with an EMPTY body, so
// a retired upstream produced `{"detail":"","status":502}` on every /v1/o11y/*
// request AND on every Sentry envelope, which apps/event relays here. A fleet
// whose error ingest is down while saying nothing about why is the one failure
// that also conceals every other failure — measured in production, where the
// blank 502 had been answering every page load of the chat surface.
//
// The upstream is dialled and refused rather than mocked: an ErrorHandler is
// reachable only through a real transport failure, so a fake would assert the
// test's own arrangement instead of the proxy's behaviour.
func TestNewHandlerNamesAnUnreachableUpstream(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := dead.URL
	dead.Close() // nothing listens on addr now, so the dial is refused

	h, err := newHandler(addr)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/o11y/health", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	body := rec.Body.String()
	if body == "" {
		t.Fatal("an unreachable upstream answered with an EMPTY body — that is the default " +
			"ErrorHandler, and it is what made this outage unreadable")
	}
	if !strings.Contains(body, `"detail":"the o11y runtime is not reachable"`) {
		t.Errorf("body = %s, want a detail naming the condition", body)
	}
	// The internal address stays out of the answer; it belongs in the log.
	if host := strings.TrimPrefix(addr, "http://"); strings.Contains(body, host) {
		t.Errorf("the answer names the internal upstream %q — that is topology, not a fact "+
			"the caller can act on", host)
	}
}
