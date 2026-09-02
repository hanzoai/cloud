// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"net/http"
	"strings"
	"testing"
)

// anon_capability_test.go — the TRUST-LEVEL invariant, proven at EVERY endpoint.
//
// The property under test is one sentence: a request that presented NO credential gets
// the anonymous PROJECTION, no matter which endpoint resolved its tenant. It used to
// hold on exactly one endpoint (/v1/event) and fail on the rest, because the decision
// was written once per endpoint instead of once per trust level:
//
//   - captureTenant's last resort turned the request Host into a REAL brand org
//     (cloud.BrandForHostOK ⇒ 'hanzo' / 'lux' / 'zoo') at FULL CaptureEvent capability,
//     so every endpoint but /v1/event let anyone on the internet inject revenue,
//     orders, personId and groupId into a brand's partition — the partition
//     /v1/event/overview, /top, /v1/event/campaign and the GTM funnel
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
// unchanged — only the endpoint's answer is honest about it now (answer, event.go).

// commerceWire is the attack payload on the canonical/Segment wire: every field that
// poisons a revenue lens or binds an event to someone else's identity, in one event.
const commerceWire = `{"batch":[{"type":"event","event":"order_completed",` +
	`"revenue":999999,"productId":"plan_enterprise","quantity":7,"currency":"USD",` +
	`"groupId":"victim-team","personId":"victim-person","distinctId":"attacker",` +
	`"properties":{"injected":"yes"}}]}`

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

// The site-host carve is deleted, so "a Host header may not reach the write core" is
// no longer a rule this endpoint enforces — there is no site-host endpoint. apps/sites'
// TestSiteHostNeverIngests pins that a site host serves bytes and is terminal.

// TestAnonIdentity_RefusedAtEveryDoor: `identify` and `group` are the two kinds that
// bind an event to a named person and a named group. A caller nobody vouched for may
// write neither, on any endpoint — the fields are gone AND the kinds are dropped, which
// is belt and braces on purpose (the projection is the load-bearing half).
func TestAnonIdentity_RefusedAtEveryDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	// Each endpoint is probed in ITS OWN wire (identifyFor/groupFor, doors_test.go). The
	// bodies used to be two canonical-wire literals applied to every endpoint, which only
	// worked while every endpoint spoke that wire: the team endpoint accepts a bare
	// ARRAY and answers an object body 400, so a shared literal measured decoder
	// tolerance rather than the projection. Both refusals are 4xx now and nothing is
	// stored either way, but "refused because the kind is not writable anonymously"
	// (401) and "refused because the body is the wrong shape" (400) are different facts,
	// and this test is about the first one — which is exactly what asserting the CODE
	// pins.
	for _, pick := range []func(*testing.T, door) string{identifyFor, groupFor} {
		for _, d := range doors {
			body := pick(t, d)
			code, got := doHost(t, app, d.path, "", "", "hanzo.ai", body)
			refusedAnon(t, "anonymous "+body+" on "+d.path, code, got)
		}
	}
}

// TestAnonymousRefusedOnEveryDoor: a keyless beacon is refused on every endpoint, with
// no switch to turn it back on. It used to be ACCEPTED into a reserved tenant and
// answered 200 — the switch that governed it defaulted ON, so the silent-accept was
// the shipped behaviour and only an operator who knew the flag existed could stop it.
// Attribution is the key now, so there is nothing left to gate.
func TestAnonymousRefusedOnEveryDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, d := range doors {
		code, body := doHost(t, app, d.path, "", "", "hanzo.ai", pageviewFor(t, d))
		if code != http.StatusUnauthorized {
			t.Errorf("anonymous %s = %d (%s), want 401", d.path, code, body)
		}
		if !strings.Contains(string(body), "ingest_key_required") {
			t.Errorf("anonymous %s body = %s, want the ingest_key_required code", d.path, body)
		}
	}
}
