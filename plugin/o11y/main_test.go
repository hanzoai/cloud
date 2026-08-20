package main

// The o11y app is the ONE app binary that composes its own root instead of going
// through cloud.Listen, and the host in front of it installs no middleware. That
// combination is how its three prefixes — /v1/o11y, /v1/sentinel and, the one a
// browser reads, /v1/summary — became the only public surface answering 200 with
// no Access-Control-Allow-Origin: the browser received the status document and
// then threw it away. These pin the edge policy onto THIS app's chain, over the
// same allowlist every other app answers with.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// probeApp is newApp with a stand-in for the mounted routes. Mount opens the
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
	// A config with no IAM issuer: the identity boundary still runs, still strips
	// every client-supplied authority header, and validates nothing — the shape a
	// deployment has before it is pointed at an issuer, and the one that must not
	// leave the chain trusting the wire.
	app := newApp(&cloud.Config{}, cloud.Deps{GatewayPolicy: pol})
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
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://insights.hanzo.ai" {
		t.Fatalf("ACAO = %q, want the reflected origin — a browser cannot read the status document without it", got)
	}
	// CONTAINS, not equals. What matters is that the response declares it varies by
	// Origin, so a shared cache cannot hand this body to a different origin. Other
	// middleware in the chain declare their own fields — markdown negotiation adds
	// Accept — and each owns one member of the set. Pinning the whole value asserts
	// that this app runs exactly one negotiating middleware, which is a fact about
	// the chain and not about CORS.
	if got := res.Header.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want it to include Origin — a cache may otherwise cross-serve origins", got)
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
	res, err := app.Test(req)
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
	res, err := app.Test(req)
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
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ACAO = %q, want none — an unset allowlist must not double the ingress header", got)
	}
}

// This process composes its own root, so the identity trust boundary is not
// inherited from cloud.Serve — it has to be installed here, and until it was,
// every X-* authority header on the wire reached o11y's own org scoping and admin
// gates verbatim. The chain must strip them whether or not a token validates.
func TestChainStripsClientSuppliedAuthority(t *testing.T) {
	pol, _ := edge.New("", "admin", edge.Policy{})
	app := newApp(&cloud.Config{}, cloud.Deps{GatewayPolicy: pol})

	var seen struct{ org, user, admin string }
	app.Get("/v1/summary", func(c *zip.Ctx) error {
		seen.org, seen.user, seen.admin = c.Org(), c.User(), c.Header("X-User-IsAdmin")
		return c.JSON(200, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/summary", nil)
	req.Header.Set("X-User-Id", "u-forged")
	req.Header.Set("X-User-IsAdmin", "true")
	if _, err := app.Test(req); err != nil {
		t.Fatalf("test: %v", err)
	}
	if seen.user != "" {
		t.Fatalf("a client-supplied X-User-Id survived the chain: %q", seen.user)
	}
	if seen.admin != "" {
		t.Fatalf("a client-supplied X-User-IsAdmin survived the chain: %q", seen.admin)
	}
}
