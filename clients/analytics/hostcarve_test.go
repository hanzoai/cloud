// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// liveResolver is a sites.Resolver that knows exactly ONE published site — the
// slug key "yadota" and the bound custom host "yadota.tech" — and returns its live
// Site whose Org the carve must force onto every ingested beacon. Any other key is
// an honest miss (found=false), exactly as the real projects store behaves, so a
// stray external host is NOT mistaken for a bound custom domain.
type liveResolver struct{ org string }

func (r liveResolver) Resolve(_ context.Context, key string) (sites.Site, bool, error) {
	switch key {
	case "yadota", "yadota.tech":
		return sites.Site{Org: r.org, Slug: "yadota", Bucket: "b", Prefix: r.org + "/yadota", Status: "live"}, true, nil
	default:
		return sites.Site{}, false, nil
	}
}

// carveApp mounts analytics (which installs the site-host ingest carve via
// sites.SetAnalyticsHostHandler) BEHIND the sites host-router middleware, then
// points the resolver at one live Site. A POST to the site host is intercepted by
// the middleware and forced to Site.Org; a POST to any other host falls through to
// the normal /v1/analytics route.
func carveApp(t *testing.T, org string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	srv := sites.New(sites.Config{
		Apex:        "hanzo.app",
		Reserved:    []string{"app", "api", "admin"},
		SelfDomains: []string{"hanzo.ai", "hanzo.app"},
	}, luxlog.New("test"))
	app.Use(srv.Middleware())
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	sites.SetResolver(liveResolver{org: org})
	t.Cleanup(func() {
		sites.SetResolver(nil)
		sites.SetAnalyticsHostHandler(nil)
	})
	return app
}

func postHost(t *testing.T, app *zip.App, host, path, body string, hdr map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s%s: %v", host, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestMount_HostCarve_ForcesSiteOrg is the end-to-end proof: Mount wires the carve,
// and a beacon POST to a LIVE site host is ingested as the site's Org even though
// the request carries a forged org (body + X-Org-Id) and NO validated principal.
// The discriminator is 503-not-403: the SAME shape 403s on the normal route
// (TestCapture_ForgedOrgWithoutBearerForbidden / unknown-host), but here it passes
// the tenant gate (org forced from the host) and stops only at the datastore-down
// 503 — proving the org is server-supplied from the host, never the caller/body.
func TestMount_HostCarve_ForcesSiteOrg(t *testing.T) {
	app := carveApp(t, "hanzo")

	// Segment/beacon wire on /v1/analytics + its /batch alias.
	for _, p := range []string{"/v1/analytics", "/v1/analytics/batch"} {
		code := postHost(t, app, "yadota.hanzo.app", p,
			`{"batch":[{"type":"event","event":"signup_completed"}],"org":"attacker","properties":{"space":"attacker"}}`,
			map[string]string{"X-Org-Id": "attacker"})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("beacon POST %s want 503 (ingested as site org, datastore down), got %d", p, code)
		}
	}

	// PostHog wire on /v1/insights/e.
	code := postHost(t, app, "yadota.hanzo.app", "/v1/insights/e",
		`{"event":"$pageview","distinct_id":"d","properties":{"space":"attacker"}}`,
		map[string]string{"X-Org-Id": "attacker"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("insights beacon want 503 (ingested as site org), got %d", code)
	}
}

// TestMount_HostCarve_EmptyBatchOK: an empty beacon batch on the site host is an
// honest 200 (zero counts) BEFORE the datastore is consulted — proving the carve
// decodes and funnels through the ONE write core without any principal.
func TestMount_HostCarve_EmptyBatchOK(t *testing.T) {
	app := carveApp(t, "hanzo")
	if code := postHost(t, app, "yadota.hanzo.app", "/v1/analytics", `{"batch":[]}`, nil); code != http.StatusOK {
		t.Fatalf("empty beacon batch want 200, got %d", code)
	}
}

// TestMount_HostCarve_CustomDomainForcesOrg: the carve fires for a bound custom
// domain too, forcing that Site's Org.
func TestMount_HostCarve_CustomDomainForcesOrg(t *testing.T) {
	app := carveApp(t, "yadota")
	code := postHost(t, app, "yadota.tech", "/v1/analytics",
		`{"batch":[{"type":"pageview"}]}`, map[string]string{"X-Org-Id": "attacker"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("custom-domain beacon want 503 (ingested as site org), got %d", code)
	}
}

// TestMount_HostCarve_GetNotHijacked: a GET on the site host is NOT ingest — it is
// served as static (storage unconfigured here ⇒ 503 from the serve path), never
// routed to the ingest carve; the read-lens surface is untouched.
func TestMount_HostCarve_GetNotHijacked(t *testing.T) {
	app := carveApp(t, "hanzo")
	req := httptest.NewRequest(http.MethodGet, "http://yadota.hanzo.app/v1/analytics/overview", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// It must have reached the static serve, tagged X-Hanzo-Site — not the ingest
	// carve (which would 200 the empty body) and not the API pipeline.
	if resp.Header.Get("X-Hanzo-Site") != "yadota" {
		t.Fatalf("GET did not reach the static serve (X-Hanzo-Site=%q, status=%d)", resp.Header.Get("X-Hanzo-Site"), resp.StatusCode)
	}
}

// TestMount_HostCarve_NonSiteHostUsesNormalGate: on a NON-site host the middleware
// Continues and the normal /v1/analytics route runs — so an anonymous unknown-host
// beacon is refused 403 by captureTenant (the carve did not fire). This pins that
// the forced-org path is host-scoped and does NOT weaken the normal ingest gate.
func TestMount_HostCarve_NonSiteHostUsesNormalGate(t *testing.T) {
	app := carveApp(t, "hanzo")
	code := postHost(t, app, "evil.example.com", "/v1/analytics",
		`{"batch":[{"type":"pageview"}]}`, map[string]string{"X-Org-Id": "attacker"})
	if code != http.StatusForbidden {
		t.Fatalf("anonymous unknown-host beacon on the normal route want 403, got %d", code)
	}
}

// TestMount_HostCarve_DisabledWhenPublicCaptureOff: with public capture off the
// carve is NOT installed, so a beacon POST to the site host falls to the static
// serve and 405s (unchanged from before the fix).
func TestMount_HostCarve_DisabledWhenPublicCaptureOff(t *testing.T) {
	t.Setenv(publicCaptureEnv, "off")
	app := carveApp(t, "hanzo")
	code := postHost(t, app, "yadota.hanzo.app", "/v1/analytics",
		`{"batch":[{"type":"pageview"}]}`, nil)
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("public-capture-off site beacon want 405 (carve not installed), got %d", code)
	}
}
