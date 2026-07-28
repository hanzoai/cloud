// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// anon_capability_test.go — the TRUST-LEVEL invariant, proven at EVERY door.
//
// The property under test is one sentence: a request that presented NO credential gets
// the anonymous PROJECTION, no matter which door resolved its tenant. It used to hold
// on exactly one door (/v1/event) and fail on three, because the decision was written
// once per door instead of once per trust level:
//
//   - captureTenant's last resort turned the request Host into a REAL brand org
//     (cloud.BrandForHostOK ⇒ 'hanzo' / 'lux' / 'zoo') at FULL CaptureEvent capability,
//     so /v1/analytics{,/batch}, /v1/tracker and /v1/insights/e let anyone on the
//     internet inject revenue, orders, personId and groupId into a brand's partition —
//     the partition /v1/analytics/overview, /top, /v1/analytics/campaign and the GTM
//     funnel (clients/guide) all read.
//   - the published-site carve called the full-capability core with ZERO credential and
//     the site's real org, so the same injection worked against any customer's org by
//     setting a Host header.
//
// The observable, as everywhere in this package: 503 ⇒ the event was ADMITTED and
// reached requireDatastore (no warehouse in the harness) — i.e. it would have become a
// row. 200 with {accepted:0,dropped:N} ⇒ the projection refused it before the write
// core. 403 ⇒ refused at the gate. So "must not become a row" is exactly "must not 503".

// commerceWire is the attack payload on the canonical/Segment wire: every field that
// poisons a revenue lens or binds an event to someone else's identity, in one event.
const commerceWire = `{"batch":[{"type":"event","event":"order_completed",` +
	`"revenue":999999,"productId":"plan_enterprise","quantity":7,"currency":"USD",` +
	`"groupId":"victim-team","personId":"victim-person","distinctId":"attacker",` +
	`"properties":{"injected":"yes"}}]}`

// commercePostHog is the same attack on the PostHog wire. That adapter maps no
// revenue/groupId/personId columns, but the EVENT NAME is what the commerce lenses
// count (`countIf(event = 'order_completed') AS orders`), and the whole property bag
// rides along — so an unattested caller naming the event is already the poisoning.
const commercePostHog = `{"event":"order_completed","distinct_id":"attacker",` +
	`"properties":{"$current_url":"https://hanzo.ai/pricing","injected":"yes"}}`

// pageviewWire is what a real logged-out visitor emits — the traffic that must KEEP
// working on every door, so the fix is a capability drop and not a feature deletion.
const pageviewWire = `{"batch":[{"type":"pageview","distinctId":"anon-1","path":"/pricing"}]}`

// postHostBody is postHost (hostcarve_test.go) with the response body returned, so a
// site-host case can assert the honest {accepted,dropped} receipt and not just a status.
func postHostBody(t *testing.T, app *zip.App, host, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s%s: %v", host, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// roomyRate installs anonymous counters big enough that no capability test can be
// masked by a 429 from a bucket another test in this package already spent. The rate
// cap itself is pinned by TestPublic_RateLimited / TestPublic_PeerCeiling.
func roomyRate(t *testing.T) { tightenPublicRate(t, 1_000_000, 1_000_000) }

// ── the defect: a credential-less caller reaching FULL capability ────────────

// TestAnonCommerce_RefusedAtEveryAPIDoor is the headline. For EVERY /v1 ingest door, on
// a RECOGNIZED BRAND HOST and with no credential of any kind, the commerce payload must
// not become a row.
//
// Before the fix, the three doors that resolved their own tenant (/v1/analytics,
// /v1/analytics/batch, /v1/tracker via captureTenant, and /v1/insights/e via the same)
// answered 503 — admitted, at full capability, into tenant 'hanzo'.
func TestAnonCommerce_RefusedAtEveryAPIDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, door := range []struct{ path, body string }{
		{"/v1/event", commerceWire},
		{"/v1/ingest", commerceWire},
		{"/v1/analytics", commerceWire},
		{"/v1/analytics/batch", commerceWire},
		{"/v1/tracker", commerceWire},
		{"/v1/insights/e", commercePostHog},
	} {
		// hanzo.ai is a brand Domain, so this Host is exactly what the deleted
		// fallback resolved to the real 'hanzo' org.
		code, body := doHost(t, app, door.path, "", "", "hanzo.ai", door.body)
		if code == http.StatusServiceUnavailable {
			t.Errorf("POST %s (anonymous, brand host): reached the write core at FULL capability — "+
				"a credential-less caller can write revenue/groupId/personId into a real org", door.path)
			continue
		}
		if code != http.StatusOK {
			t.Errorf("POST %s (anonymous, brand host) = %d (%s), want 200 with an all-dropped receipt",
				door.path, code, body)
			continue
		}
		if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
			t.Errorf("POST %s (anonymous, brand host) receipt = %+v, want accepted:0 dropped:1", door.path, r)
		}
	}
}

// TestAnonCommerce_RefusedOnEveryBrandHost: the fallback keyed on the white-label
// registry, so every brand Domain was its own injection target into its own real org.
// None of them buys capability now.
func TestAnonCommerce_RefusedOnEveryBrandHost(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, host := range []string{"hanzo.ai", "api.hanzo.ai", "lux.network", "zoo.ngo", "pars.network", "bootno.de"} {
		code, body := doHost(t, app, "/v1/analytics", "", "", host, commerceWire)
		if code != http.StatusOK {
			t.Errorf("anonymous commerce on brand host %q = %d (%s), want 200 all-dropped "+
				"(a Host header must not name a tenant)", host, code, body)
			continue
		}
		if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
			t.Errorf("brand host %q receipt = %+v, want accepted:0 dropped:1", host, r)
		}
	}
}

// TestAnonCommerce_RefusedAtSiteHostDoor: the published-site carve is the second door
// that reached full capability with no credential. It ran BEFORE the identity boundary
// (serve.go mounts sites at 241, IdentityMiddleware at 267), so nothing there could
// vouch for a caller — yet it wrote into the site's REAL org whatever the body said.
// Anyone could aim it at any customer's org with a Host header.
//
// Before the fix all three paths answered 503 (admitted at full capability).
func TestAnonCommerce_RefusedAtSiteHostDoor(t *testing.T) {
	roomyRate(t)
	app := carveApp(t, "yadota")
	for _, door := range []struct{ path, body string }{
		{"/v1/event", commerceWire},
		{"/v1/analytics", commerceWire},
		{"/v1/analytics/batch", commerceWire},
		{"/v1/insights/e", commercePostHog},
	} {
		code, body := postHostBody(t, app, "yadota.hanzo.app", door.path, door.body)
		if code == http.StatusServiceUnavailable {
			t.Errorf("site-host POST %s: reached the write core at FULL capability — "+
				"a Host header alone let a stranger write revenue/groupId/personId into the site's org", door.path)
			continue
		}
		if code != http.StatusOK {
			t.Errorf("site-host POST %s = %d (%s), want 200 with an all-dropped receipt", door.path, code, body)
			continue
		}
		if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
			t.Errorf("site-host POST %s receipt = %+v, want accepted:0 dropped:1", door.path, r)
		}
	}
}

// TestAnonCommerce_RefusedOnBoundCustomDomain: the carve fires for a bound custom
// domain too, so that door needed the same drop.
func TestAnonCommerce_RefusedOnBoundCustomDomain(t *testing.T) {
	roomyRate(t)
	app := carveApp(t, "yadota")
	code, body := postHostBody(t, app, "yadota.tech", "/v1/analytics", commerceWire)
	if code == http.StatusServiceUnavailable {
		t.Fatalf("custom-domain beacon reached the write core at FULL capability")
	}
	if code != http.StatusOK {
		t.Fatalf("custom-domain anonymous commerce = %d (%s), want 200 all-dropped", code, body)
	}
	if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
		t.Fatalf("custom-domain receipt = %+v, want accepted:0 dropped:1", r)
	}
}

// TestAnonIdentity_RefusedAtEveryDoor: `identify` and `group` are the two kinds that
// bind an event to a named person and a named group. A caller nobody vouched for may
// write neither, on any door — the fields are gone AND the kinds are dropped, which is
// belt and braces on purpose (the projection is the load-bearing half).
func TestAnonIdentity_RefusedAtEveryDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, body := range []string{
		`{"batch":[{"type":"identify","distinctId":"victim","personId":"victim-person"}]}`,
		`{"batch":[{"type":"group","groupId":"victim-team"}]}`,
	} {
		for _, path := range []string{"/v1/event", "/v1/ingest", "/v1/analytics", "/v1/analytics/batch", "/v1/tracker"} {
			code, got := doHost(t, app, path, "", "", "hanzo.ai", body)
			if code != http.StatusOK {
				t.Errorf("anonymous %s on %s = %d (%s), want 200 all-dropped", body, path, code, got)
				continue
			}
			if r := receipt(t, got); r.Accepted != 0 || r.Dropped != 1 {
				t.Errorf("anonymous %s on %s receipt = %+v, want accepted:0 dropped:1", body, path, r)
			}
		}
	}
}

// ── what the fix must NOT break ─────────────────────────────────────────────

// TestAnonPageview_StillAdmittedAtEveryDoor: the point of the anonymous lane is that
// real logged-out traffic lands. A pageview must still be ADMITTED (503 = reached the
// warehouse) on every door — the change is a capability drop, not a refusal.
func TestAnonPageview_StillAdmittedAtEveryDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, path := range []string{"/v1/event", "/v1/ingest", "/v1/analytics", "/v1/analytics/batch", "/v1/tracker"} {
		if code, body := doHost(t, app, path, "", "", "hanzo.ai", pageviewWire); code != http.StatusServiceUnavailable {
			t.Errorf("anonymous pageview on %s = %d (%s), want 503 (admitted, datastore down)", path, code, body)
		}
	}
	if code, body := doHost(t, app, "/v1/insights/e", "", "", "hanzo.ai",
		`{"event":"$pageview","distinct_id":"anon-1","properties":{"$pathname":"/pricing"}}`); code != http.StatusServiceUnavailable {
		t.Errorf("anonymous PostHog pageview = %d (%s), want 503 (admitted)", code, body)
	}
}

// TestSiteHostPageview_StillLandsForTheSite is the feature pin: a published site's OWN
// beacon is the reason the carve exists. It must still be admitted on every carve path,
// so the site's owner keeps reading their own pageviews in their own org. (The org the
// carve hands the lane is the resolved Site.Org — pinned in clients/sites'
// TestMiddlewareAnalyticsCarveSlugHost, which asserts the exact argument.)
func TestSiteHostPageview_StillLandsForTheSite(t *testing.T) {
	roomyRate(t)
	app := carveApp(t, "yadota")
	for _, path := range []string{"/v1/event", "/v1/analytics", "/v1/analytics/batch"} {
		if code, body := postHostBody(t, app, "yadota.hanzo.app", path, pageviewWire); code != http.StatusServiceUnavailable {
			t.Errorf("site-host pageview on %s = %d (%s), want 503 (admitted for the site's org)", path, code, body)
		}
	}
	if code, body := postHostBody(t, app, "yadota.hanzo.app", "/v1/insights/e",
		`{"event":"$pageview","distinct_id":"anon-1"}`); code != http.StatusServiceUnavailable {
		t.Errorf("site-host PostHog pageview = %d (%s), want 503 (admitted for the site's org)", code, body)
	}
}

// TestCredentialed_KeepsFullCapabilityAtEveryDoor is the additivity proof across the
// doors the fix touched: the projection is for callers nobody vouched for, and a
// validated principal is untouched by it. The SAME commerce payload that is dropped
// anonymously above is admitted whole with a bearer, on every door.
func TestCredentialed_KeepsFullCapabilityAtEveryDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, path := range []string{"/v1/event", "/v1/ingest", "/v1/analytics", "/v1/analytics/batch", "/v1/tracker"} {
		if code, body := doBody(t, app, http.MethodPost, path, "user-dave", "acme", commerceWire); code != http.StatusServiceUnavailable {
			t.Errorf("bearer commerce on %s = %d (%s), want 503 (admitted at full capability)", path, code, body)
		}
	}
	if code, body := doBody(t, app, http.MethodPost, "/v1/insights/e", "user-dave", "acme", commercePostHog); code != http.StatusServiceUnavailable {
		t.Errorf("bearer commerce on /v1/insights/e = %d (%s), want 503 (admitted)", code, body)
	}
}

// TestKeyedCaller_KeepsFullCapabilityAtEveryDoor: the same for a resolvable ingest key
// — the credential a bearer-less SDK presents. Every door consults the SAME credential
// set now (eventTenant), so a pk- works on the deprecated aliases exactly as on the
// canonical door, and it carries full capability.
func TestKeyedCaller_KeepsFullCapabilityAtEveryDoor(t *testing.T) {
	roomyRate(t)
	stubResolver(t, func(key string) (string, bool) {
		if key == "pk-acme" || key == "hk-acme" {
			return "acme", true
		}
		return "", false
	})
	app := mountApp(t)
	for _, path := range []string{"/v1/event", "/v1/ingest", "/v1/analytics", "/v1/analytics/batch", "/v1/tracker"} {
		if code := postKeyed(t, app, path, "hanzo.ai", commerceWire,
			map[string]string{"x-hanzo-ingest-key": "pk-acme"}); code != http.StatusServiceUnavailable {
			t.Errorf("pk- commerce on %s = %d, want 503 (admitted at full capability)", path, code)
		}
		if code := postKeyed(t, app, path, "hanzo.ai", commerceWire,
			map[string]string{"x-api-key": "hk-acme"}); code != http.StatusServiceUnavailable {
			t.Errorf("api-key commerce on %s = %d, want 503 (admitted at full capability)", path, code)
		}
	}
}

// TestPresentedKey_StillFailsClosedAtEveryDoor: unifying admission must not soften the
// fail-closed rule. A PRESENTED credential that does not resolve is refused on EVERY
// door — never downgraded into the anonymous lane, which would hide a misconfigured key
// in a partition its owner cannot read.
func TestPresentedKey_StillFailsClosedAtEveryDoor(t *testing.T) {
	roomyRate(t)
	stubResolver(t, func(string) (string, bool) { return "", false })
	app := mountApp(t)
	for _, path := range []string{"/v1/event", "/v1/ingest", "/v1/analytics", "/v1/analytics/batch", "/v1/tracker", "/v1/insights/e"} {
		if code := postKeyed(t, app, path, "hanzo.ai", pageviewWire,
			map[string]string{"x-api-key": "hk-nosuch"}); code != http.StatusForbidden {
			t.Errorf("unresolvable api key on %s = %d, want 403 (fail closed, never anonymous)", path, code)
		}
		if code := postKeyed(t, app, path, "hanzo.ai", pageviewWire,
			map[string]string{"x-hanzo-ingest-key": "pk-nosuch"}); code != http.StatusForbidden {
			t.Errorf("unresolvable pk- on %s = %d, want 403 (fail closed, never anonymous)", path, code)
		}
	}
}

// TestPublicCaptureOff_RefusesEveryAnonymousDoor: CLOUD_ANALYTICS_PUBLIC_CAPTURE is the
// ONE anonymous-capture switch and it still governs every door — including the two that
// used to route around the anonymous lane entirely (and therefore around this flag's
// only enforcement point).
func TestPublicCaptureOff_RefusesEveryAnonymousDoor(t *testing.T) {
	t.Setenv(publicCaptureEnv, "off")
	roomyRate(t)
	app := mountApp(t)
	for _, path := range []string{"/v1/event", "/v1/ingest", "/v1/analytics", "/v1/analytics/batch", "/v1/tracker", "/v1/insights/e"} {
		if code, body := doHost(t, app, path, "", "", "hanzo.ai", pageviewWire); code != http.StatusForbidden {
			t.Errorf("public-capture-off anonymous %s = %d (%s), want 403", path, code, body)
		}
	}
}
