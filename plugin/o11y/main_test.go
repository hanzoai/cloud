package main

// The o11y app is the ONE app binary that composes its own root instead of going
// through cloud.Serve, and the host in front of it installs no middleware. That
// combination is how its three prefixes — /v1/o11y, /v1/sentry and, the one a
// browser reads, /v1/summary — became the only public surface answering 200 with
// no Access-Control-Allow-Origin: the browser received the status document and
// then threw it away. These pin the edge policy onto THIS app's chain, over the
// same allowlist every other app answers with.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// probeApp is newApp with a stand-in for the mounted routes. MountO11y opens the
// event store and starts the fleet probes, none of which this asks about: the
// question here is what the CHAIN does to a response, so any leaf answers it.
func probeApp(t *testing.T, origins []string) *zip.App {
	t.Helper()
	// "" dataDir ⇒ static-only store, no SQLite file — the boot defaults the live
	// CLOUD_CORS_ORIGINS lands in. The error is ADVISORY: edge.New always returns a
	// working store and reports degradation separately, which is why BuildDeps logs
	// it and carries on. Failing on it here would test the fixture, not the edge.
	pol, _ := edge.New("", "admin", edge.Policy{CORSOrigins: origins})
	if pol == nil {
		t.Fatal("edge.New must always return a usable store")
	}
	app := newApp(cloud.Deps{GatewayPolicy: pol})
	app.Get("/v1/summary", func(c *zip.Ctx) error {
		return c.JSON(200, map[string]string{"page_title": "Hanzo status"})
	})
	return app
}

// prodOrigins is the live allowlist (universe charts/app/values/hanzo/cloud.yaml,
// CLOUD_CORS_ORIGINS). Spelled out so a test fails if the shipped list ever stops
// covering the brand hosts that read this endpoint, rather than passing against a
// convenient fixture.
var prodOrigins = []string{
	"*.hanzo.ai", "*.hanzo.app", "*.hanzo.chat",
	"*.hanzo.bot", "*.hanzo.team", "*.hanzo.dao",
	"https://hanzo-login.pages.dev",
}

func TestSummaryReflectsAllowlistedBrandOrigin(t *testing.T) {
	app := probeApp(t, prodOrigins)

	req := httptest.NewRequest(http.MethodGet, "/v1/summary", nil)
	req.Header.Set("Origin", "https://insights.hanzo.ai")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://insights.hanzo.ai" {
		t.Fatalf("ACAO = %q, want the reflected origin — a browser cannot read the status document without it", got)
	}
	if got := res.Header.Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
}

// A simple cross-origin GET is not preflighted, so the fix above is what actually
// unblocked the browser. OPTIONS is still answered — a client that sends one (a
// custom header away) must not meet the 405 the bare router used to return.
func TestSummaryAnswersPreflight(t *testing.T) {
	app := probeApp(t, prodOrigins)

	req := httptest.NewRequest(http.MethodOptions, "/v1/summary", nil)
	req.Header.Set("Origin", "https://insights.hanzo.ai")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != 204 {
		t.Fatalf("preflight status = %d, want 204", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://insights.hanzo.ai" {
		t.Fatalf("preflight ACAO = %q", got)
	}
}

// Public and tenant-free is not the same as public to everyone's JavaScript: the
// allowlist still decides, exactly as it does on every other prefix. No wildcard.
func TestSummaryGivesUnknownOriginNoCORS(t *testing.T) {
	app := probeApp(t, prodOrigins)

	req := httptest.NewRequest(http.MethodGet, "/v1/summary", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO = %q, want none for an origin the allowlist does not admit", got)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d — the read itself is public; only the browser read is gated", res.StatusCode)
	}
}

// An empty allowlist stays a no-op, so a deployment where CORS is owned by the
// ingress does not start emitting a second Access-Control-Allow-Origin from here.
func TestSummaryEmitsNothingWhenAllowlistEmpty(t *testing.T) {
	app := probeApp(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/summary", nil)
	req.Header.Set("Origin", "https://insights.hanzo.ai")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO = %q, want none — an unset allowlist must not double the ingress header", got)
	}
}
