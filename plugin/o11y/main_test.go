package main

// The o11y app is the ONE app binary that composes its own root instead of going
// through cloud.Listen, and the host in front of it installs no middleware. That
// combination is how its surface — /v1/o11y, and the one a browser reads,
// /v1/o11y/summary — became the only public surface answering 200 with
// no Access-Control-Allow-Origin: the browser received the status document and
// then threw it away. These pin the edge policy onto THIS app's chain, over the
// same allowlist every other app answers with.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// probeApp is newApp with a stand-in for the mounted routes. Mount opens the
// event store and starts the fleet probes, none of which this asks about: the
// question here is what the CHAIN does to a response, so any leaf answers it.
func probeApp(t *testing.T, origins []string) *zip.App {
	t.Helper()
	// No data directory ⇒ the edge store is static-only, no SQLite file: the boot
	// defaults the live CLOUD_CORS_ORIGINS lands in.
	// A config with no IAM issuer: the identity boundary still runs, still strips
	// every client-supplied authority header, and validates nothing — the shape a
	// deployment has before it is pointed at an issuer, and the one that must not
	// leave the chain trusting the wire.
	app := newApp(&cloud.Config{CORSOrigins: origins}, cloud.Deps{})
	app.Raw(http.MethodGet, "/v1/o11y/summary", func(c *zip.Ctx) error {
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

	req := httptest.NewRequest(http.MethodGet, "/v1/o11y/summary", nil)
	req.Header.Set("Origin", "https://insights.hanzo.ai")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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

	req := httptest.NewRequest(http.MethodOptions, "/v1/o11y/summary", nil)
	req.Header.Set("Origin", "https://insights.hanzo.ai")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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

// Public and tenant-free is not the same as public to everyone's credentials: the
// allowlist still decides, exactly as it does on every other prefix. Since #639
// (92dfe85d09) every well-formed web origin READS the API, reflected exactly, and
// only an origin the allowlist declares or the brand registry owns is given
// Access-Control-Allow-Credentials, so a page elsewhere never reads a visitor's
// session. No wildcard either way.
func TestSummaryGivesUnknownOriginNoCredentials(t *testing.T) {
	app := probeApp(t, prodOrigins)

	req := httptest.NewRequest(http.MethodGet, "/v1/o11y/summary", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://evil.example" {
		t.Fatalf("ACAO = %q, want the origin reflected: every web origin reads without credentials", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("ACAC = %q, want none for an origin the allowlist does not admit", got)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d — the read itself is public; only the browser read is gated", res.StatusCode)
	}
}

// With no allowlist declared, a brand site is still credentialed — first-party
// origins are derived from the brand registry, and cloud is the only layer that
// answers CORS — while an origin nothing vouches for reads without credentials.
func TestSummaryCredentialsOnlyDerivedOriginsWithNoAllowlist(t *testing.T) {
	app := probeApp(t, nil)

	for origin, credentialed := range map[string]string{
		"https://insights.hanzo.ai": "true",
		"https://evil.example":      "",
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/o11y/summary", nil)
		req.Header.Set("Origin", origin)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("test: %v", err)
		}
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Fatalf("ACAO for %s = %q, want it reflected", origin, got)
		}
		if got := res.Header.Get("Access-Control-Allow-Credentials"); got != credentialed {
			t.Fatalf("ACAC for %s = %q, want %q", origin, got, credentialed)
		}
	}
}

// This process composes its own root, so the identity trust boundary is not
// inherited from cloud.Serve — it has to be installed here, and until it was,
// every X-* authority header on the wire reached o11y's own org scoping and admin
// gates verbatim. The chain must strip them whether or not a token validates.
func TestChainStripsClientSuppliedAuthority(t *testing.T) {
	app := newApp(&cloud.Config{}, cloud.Deps{})

	var seen struct{ org, user, admin string }
	app.Raw(http.MethodGet, "/v1/o11y/summary", func(c *zip.Ctx) error {
		seen.org, seen.user, seen.admin = c.Org(), c.User(), c.Header("X-User-IsAdmin")
		return c.JSON(200, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/o11y/summary", nil)
	req.Header.Set("X-User-Id", "u-forged")
	req.Header.Set("X-User-IsAdmin", "true")
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatalf("test: %v", err)
	}
	if seen.user != "" {
		t.Fatalf("a client-supplied X-User-Id survived the chain: %q", seen.user)
	}
	if seen.admin != "" {
		t.Fatalf("a client-supplied X-User-IsAdmin survived the chain: %q", seen.admin)
	}
}
