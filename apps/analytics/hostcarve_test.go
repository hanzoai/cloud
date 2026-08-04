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
	"github.com/hanzoai/cloud/apps/sites"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// liveResolver is a sites.Resolver that knows exactly ONE published site — the
// slug key "yadota" and the bound custom host "yadota.tech". Any other key is an
// honest miss (found=false), exactly as the real projects store behaves, so a stray
// external host is NOT mistaken for a bound custom domain.
//
// It answers the TWO lookups a Site can be found by SEPARATELY, because they are two
// different questions and the carve is required to ask the right one:
//
//   - unpinned (Resolve) — the bare key: an explicit custom-domain binding, or on the
//     multi-tenant apex the unique-live-slug-across-orgs fallback. Whoever owns that
//     slug answers.
//   - pinned (ResolveOrg) — the slug WITHIN a named org, which is the only lookup
//     allowed on our own first-party apex.
//
// Configuring the two with DIFFERENT orgs is the only thing that makes the pin
// observable at all: with one field both lookups returned the same Site, so swapping
// resolveLivePinned for resolveLive changed nothing any test could see.
type liveResolver struct {
	pinned   string // the org ResolveOrg answers for — the first-party owner
	unpinned string // the org Resolve answers for — whoever holds the bare slug
}

func (r liveResolver) Resolve(_ context.Context, key string) (sites.Site, bool, error) {
	switch key {
	case "yadota", "yadota.tech":
		return sites.Site{Org: r.unpinned, Slug: "yadota", Bucket: "b", Prefix: r.unpinned + "/yadota", Status: "live"}, true, nil
	default:
		return sites.Site{}, false, nil
	}
}

// ResolveOrg is the PINNED lookup: the slug within the named org, and nothing else.
// It answers only for r.pinned, so a first-party host can reach exactly one org's
// project — which is the property the pin exists for.
func (r liveResolver) ResolveOrg(_ context.Context, org, slug string) (sites.Site, bool, error) {
	if org == r.pinned && slug == "yadota" {
		return sites.Site{Org: org, Slug: "yadota", Bucket: "b", Prefix: org + "/yadota", Status: "live"}, true, nil
	}
	return sites.Site{}, false, nil
}

// siteHosts is the host policy every carve app here shares: the multi-tenant apex
// (where sites are the default) plus our own domains. firstPartyApp adds the opt-in
// first-party apex on top of it.
func siteHosts() sites.Config {
	return sites.Config{
		Apex:        "hanzo.app",
		Reserved:    []string{"app", "api", "admin"},
		SelfDomains: []string{"hanzo.ai", "hanzo.app"},
	}
}

// carveOn mounts analytics (which installs the site-host ingest carve via
// sites.SetAnalyticsHost) BEHIND the sites host-router middleware, under a given host
// policy and resolver. Everything downstream of the middleware is identical for both
// configurations below, so the host policy is the only variable under test.
func carveOn(t *testing.T, cfg sites.Config, r sites.Resolver) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	srv := sites.New(cfg, luxlog.New("test"))
	app.Use(srv.Middleware())
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	stopSink() // see mountApp: a test process holds no live consumer
	sites.SetResolver(r)
	t.Cleanup(func() {
		sites.SetResolver(nil)
		sites.SetAnalyticsHost(nil)
	})
	return app
}

// carveApp is the MULTI-TENANT apex: `<slug>.hanzo.app` and bound custom domains,
// where the bare-key lookup is the correct one, so both of the resolver's answers are
// the site's own org. A POST to the site host is intercepted by the middleware and
// forced to Site.Org; a POST to any other host falls through to the normal
// /v1/event route.
func carveApp(t *testing.T, org string) *zip.App {
	t.Helper()
	return carveOn(t, siteHosts(), liveResolver{pinned: org, unpinned: org})
}

// ownerOrg / squatterOrg are the two answers the first-party app's resolver gives for
// the SAME slug: the org that owns our first-party sites, and a customer who published
// a project under the same name. On the first-party apex only the first may ever be
// reached.
const (
	ownerOrg    = "hanzo"
	squatterOrg = "squatter"
)

// firstPartyApp is the FIRST-PARTY apex — our own opt-in sites on hanzo.ai — where the
// two lookups disagree: the pin yields ownerOrg and the bare slug yields squatterOrg.
func firstPartyApp(t *testing.T) *zip.App {
	t.Helper()
	cfg := siteHosts()
	cfg.FirstPartyApex = "hanzo.ai"
	cfg.FirstPartySites = []string{"yadota"}
	cfg.FirstPartyOrg = ownerOrg
	return carveOn(t, cfg, liveResolver{pinned: ownerOrg, unpinned: squatterOrg})
}

func postHost(t *testing.T, app *zip.App, host, path, body string, hdr map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test POST %s%s: %v", host, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestMount_HostCarve_IngestsForSiteOrg is the end-to-end proof: Mount wires the carve,
// and a page's OWN beacon POST to a LIVE site host is ingested for the site's Org even
// though the request carries a forged org (body + X-Org-Id) and NO validated principal.
// The discriminator is 503: the request passed the door and stopped only at the
// datastore-down 503, so the org came from the host and never from the caller/body.
//
// The kinds here are pageview and error, because that is what the carve admits. The
// carve runs BEFORE the identity boundary (serve.go: sites at 241, IdentityMiddleware
// at 267), so nothing on a site host can be vouched for and every beacon takes the
// ANONYMOUS lane — see TestMount_HostCarve_AnonymousCapabilityOnly for the other half.
func TestMount_HostCarve_IngestsForSiteOrg(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := carveApp(t, "hanzo")

	// Canonical wire on /v1/event.
	if code := postHost(t, app, "yadota.hanzo.app", canonDoor,
		`{"batch":[{"type":"pageview","path":"/pricing"}],"org":"attacker","properties":{"space":"attacker"}}`,
		map[string]string{"X-Org-Id": "attacker"}); code != http.StatusServiceUnavailable {
		t.Fatalf("beacon POST %s want 503 (ingested for the site org, datastore down), got %d", canonDoor, code)
	}

	// PostHog wire on the ONE door /v1/event (decodeEvent falls back to the PostHog decoder).
	code := postHost(t, app, "yadota.hanzo.app", "/v1/event",
		`{"event":"$pageview","distinct_id":"d","properties":{"space":"attacker"}}`,
		map[string]string{"X-Org-Id": "attacker"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("insights beacon want 503 (ingested for the site org), got %d", code)
	}
}

// TestMount_HostCarve_FirstPartyHostResolvesPinned is the pin, and it is asserted on
// the ROW because that is the only place the pin is visible.
//
// On our own apex a slug must resolve WITHIN our org (ResolveOrg over FirstPartyOrg),
// never by the unique-live-slug-across-orgs fallback. Resolve unpinned and a customer
// who published a project named `yadota` answers for `yadota.hanzo.ai`: their Site.Org
// becomes the tenant, so our first-party pages' beacons land in THEIR partition —
// readable by them, missing from ours. That is a cross-tenant attribution flip bought
// with nothing but a project name, and every status code on both sides of it is 200.
//
// Every OTHER test in this file runs on the multi-tenant apex, where firstParty is
// false and resolveLivePinned delegates straight to resolveLive — so before this test
// liveResolver.ResolveOrg was never called by this package at all (an unconditional
// panic in it left the whole suite green), and all three of the carve's
// resolveLivePinned call sites could be swapped to resolveLive with nothing going red.
func TestMount_HostCarve_FirstPartyHostResolvesPinned(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	w := fakeWarehouse(t)
	app := firstPartyApp(t)
	if code := postHost(t, app, "yadota.hanzo.ai", "/v1/event", canonPageview,
		map[string]string{"X-Org-Id": "attacker"}); code != http.StatusOK {
		t.Fatalf("first-party site beacon = %d, want 200 (carved and written)", code)
	}
	got := w.tenants(t)
	if len(got) != 1 || got[0] != ownerOrg {
		t.Fatalf("first-party host wrote tenants %v, want [%s]", got, ownerOrg)
	}
	if got[0] == squatterOrg {
		t.Errorf("the first-party host resolved UNPINNED: a customer's same-named project "+
			"answered for %s and now owns our beacons", "yadota.hanzo.ai")
	}
}

// TestMount_HostCarve_AnonymousCapabilityOnly is the other half, and the fix: the carve
// authorizes a TENANT from the host, never a CAPABILITY. It used to call the
// full-capability core with zero credential, so the same Host header that made a beacon
// land in a site's org also let a stranger write a custom event name, revenue, personId
// and groupId there. Now a credential-less beacon — which on a site host is every
// beacon — gets the anonymous projection, so a non-allowlisted kind is refused storage
// and the door says so (401) instead of answering success.
func TestMount_HostCarve_AnonymousCapabilityOnly(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := carveApp(t, "hanzo")
	code := postHost(t, app, "yadota.hanzo.app", canonDoor,
		`{"batch":[{"type":"event","event":"signup_completed","revenue":999,"groupId":"victim"}]}`,
		map[string]string{"X-Org-Id": "attacker"})
	if code != http.StatusUnauthorized {
		t.Fatalf("site-host custom event on %s want 401 (never stored, and said so), got %d", canonDoor, code)
	}
}

// TestMount_HostCarve_EmptyBatchOK: an empty beacon batch on the site host is an
// honest 200 (zero counts) BEFORE the datastore is consulted — proving the carve
// decodes and funnels through the ONE write core without any principal.
func TestMount_HostCarve_EmptyBatchOK(t *testing.T) {
	app := carveApp(t, "hanzo")
	if code := postHost(t, app, "yadota.hanzo.app", canonDoor, `{"batch":[]}`, nil); code != http.StatusOK {
		t.Fatalf("empty beacon batch want 200, got %d", code)
	}
}

// TestMount_HostCarve_CustomDomainCarves: the carve fires for a bound custom domain
// too — REACHABILITY, which is all a status code can show. It does not prove WHOSE org
// the beacon was filed under, and it used to be named as though it did.
//
// That fact is pinned where it is decided: sites.Middleware resolves the host, and
// clients/sites' TestMiddlewareAnalyticsCarveCustomDomain asserts the org handed to
// the carve handler is the resolved Site's and that the resolver saw the full host.
// Everything after that argument — publicIngest → the write core → tenant_id — is the
// same code for both host shapes and is pinned end-to-end on the slug host by
// TestSiteHostLaneWritesTheResolvedSiteOrg, so asserting the row again here would be a
// second place answering one question.
func TestMount_HostCarve_CustomDomainCarves(t *testing.T) {
	app := carveApp(t, "yadota")
	code := postHost(t, app, "yadota.tech", canonDoor,
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
	resp, err := app.Test(req)
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
// Continues and the normal /v1/event route runs — the carve did not fire, so the
// beacon gets the normal door's anonymous lane (the reserved public tenant) rather than
// any site's org. A pageview is admitted there (503) and a custom event is dropped, so
// the host-scoped carve neither leaks a site org off-host nor weakens the normal gate.
func TestMount_HostCarve_NonSiteHostUsesNormalGate(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := carveApp(t, "hanzo")
	if code := postHost(t, app, "evil.example.com", canonDoor,
		`{"batch":[{"type":"pageview"}]}`, map[string]string{"X-Org-Id": "attacker"}); code != http.StatusServiceUnavailable {
		t.Fatalf("anonymous unknown-host beacon want 503 (normal door's anonymous lane), got %d", code)
	}
	if code := postHost(t, app, "evil.example.com", canonDoor,
		`{"batch":[{"type":"event","event":"order_completed","revenue":99}]}`,
		map[string]string{"X-Org-Id": "attacker"}); code != http.StatusUnauthorized {
		t.Fatalf("anonymous unknown-host commerce want 401 (nothing stored), got %d", code)
	}
}

// TestMount_HostCarve_DisabledWhenPublicCaptureOff: with public capture off the
// carve is NOT installed, so a beacon POST to the site host falls to the static
// serve and 405s (unchanged from before the fix).
func TestMount_HostCarve_DisabledWhenPublicCaptureOff(t *testing.T) {
	t.Setenv(publicCaptureEnv, "off")
	app := carveApp(t, "hanzo")
	code := postHost(t, app, "yadota.hanzo.app", canonDoor,
		`{"batch":[{"type":"pageview"}]}`, nil)
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("public-capture-off site beacon want 405 (carve not installed), got %d", code)
	}
}
