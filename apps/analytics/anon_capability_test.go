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
// on exactly one door (/v1/event) and fail on the rest, because the decision was
// written once per door instead of once per trust level:
//
//   - captureTenant's last resort turned the request Host into a REAL brand org
//     (cloud.BrandForHostOK ⇒ 'hanzo' / 'lux' / 'zoo') at FULL CaptureEvent capability,
//     so every door but /v1/event let anyone on the internet inject revenue, orders,
//     personId and groupId into a brand's partition — the partition
//     /v1/analytics/overview, /top, /v1/analytics/campaign and the GTM funnel
//     (clients/guide) all read.
//   - the published-site carve called the full-capability core with ZERO credential and
//     the site's real org, so the same injection worked against any customer's org by
//     setting a Host header.
//
// The observable, as everywhere in this package: 503 ⇒ the event was ADMITTED and
// reached requireDatastore (no warehouse in the harness) — i.e. it would have become a
// row. 401 `ingest_key_required` (refusedAnon, door_honesty_test.go) ⇒ the projection
// refused EVERY event before the write core. 403 ⇒ refused at the gate. So "must not
// become a row" is exactly "must not 503".
//
// That middle observable used to be `200 {accepted:0,dropped:N}`, and the 200 was the
// bug: a caller that lost everything read success. The refusal it records here is
// unchanged — only the door's answer is honest about it now (answer, event.go).

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
	resp, err := app.Test(req)
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

// TestAnonCommerce_RefusedOnEveryBrandHost: the fallback keyed on the white-label
// registry, so every brand Domain was its own injection target into its own real org.
// None of them buys capability now.
func TestAnonCommerce_RefusedOnEveryBrandHost(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, host := range []string{"hanzo.ai", "api.hanzo.ai", "lux.network", "zoo.ngo", "pars.network", "bootno.de"} {
		code, body := doHost(t, app, "/v1/event", "", "", host, commerceWire)
		refusedAnon(t, "anonymous commerce on brand host "+host+" (a Host header must not name a tenant)",
			code, body)
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
	for _, door := range doors {
		code, body := postHostBody(t, app, "yadota.hanzo.app", door.path, commerceFor(t, door))
		if code == http.StatusServiceUnavailable {
			t.Errorf("site-host POST %s: reached the write core at FULL capability — "+
				"a Host header alone let a stranger write revenue/groupId/personId into the site's org", door.path)
			continue
		}
		refusedAnon(t, "site-host POST "+door.path, code, body)
	}
}

// TestAnonCommerce_RefusedOnBoundCustomDomain: the carve fires for a bound custom
// domain too, so that door needed the same drop.
func TestAnonCommerce_RefusedOnBoundCustomDomain(t *testing.T) {
	roomyRate(t)
	app := carveApp(t, "yadota")
	code, body := postHostBody(t, app, "yadota.tech", "/v1/event", commerceWire)
	if code == http.StatusServiceUnavailable {
		t.Fatalf("custom-domain beacon reached the write core at FULL capability")
	}
	refusedAnon(t, "custom-domain anonymous commerce", code, body)
}

// TestAnonIdentity_RefusedAtEveryDoor: `identify` and `group` are the two kinds that
// bind an event to a named person and a named group. A caller nobody vouched for may
// write neither, on any door — the fields are gone AND the kinds are dropped, which is
// belt and braces on purpose (the projection is the load-bearing half).
func TestAnonIdentity_RefusedAtEveryDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	// Each door is probed in ITS OWN wire (identifyFor/groupFor, doors_test.go). The
	// bodies used to be two canonical-wire literals applied to every door, which only
	// worked while every door spoke that wire: the team door accepts a bare ARRAY and
	// answers an object body 400, so a shared literal measured decoder tolerance rather
	// than the projection. Both refusals are 4xx now and nothing is stored either way,
	// but "refused because the kind is not writable anonymously" (401) and "refused
	// because the body is the wrong shape" (400) are different facts, and this test is
	// about the first one — which is exactly what asserting the CODE pins.
	for _, pick := range []func(*testing.T, door) string{identifyFor, groupFor} {
		for _, d := range doors {
			body := pick(t, d)
			code, got := doHost(t, app, d.path, "", "", "hanzo.ai", body)
			refusedAnon(t, "anonymous "+body+" on "+d.path, code, got)
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
	for _, d := range doors {
		if code, body := doHost(t, app, d.path, "", "", "hanzo.ai", pageviewFor(t, d)); code != http.StatusForbidden {
			t.Errorf("public-capture-off anonymous %s = %d (%s), want 403", d.path, code, body)
		}
	}
}
