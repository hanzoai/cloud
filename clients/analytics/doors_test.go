// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"net/http"
	"testing"
)

// doors_test.go — the ingest SURFACE is one set, and these are its proofs.
//
// Three things used to answer "what is an ingest door" independently: the route
// table, sites' analyticsPaths literal, and a path switch inside the carve. They
// disagreed — /v1/tracker and /v1/ingest were routed doors sites did not name, so the
// same beacon was admitted on an API host and 405'd on a site host. doors (event.go)
// is now the only answer and both surfaces derive from it; the tests below hold that
// shut from both ends.
//
// Every gate assertion here is QUANTIFIED OVER doors rather than written against a
// path list, so a door added tomorrow inherits the whole contract instead of needing
// someone to remember to extend a table. The one deliberately hand-written list is
// wantDoors, which is the contract itself — the anchor that makes a silent surface
// change fail rather than pass.

// wantDoors is the ingest surface as a CONTRACT: the exact set of paths that may
// accept an event, written out by hand on purpose. Everything else in this package
// derives from doors, so without one literal to compare against, deleting a door or
// smuggling one in would keep every derived test green. Changing this list is
// changing the public surface, and it should take an edit here to do it.
var wantDoors = []string{
	"/v1/event",
	"/v1/insights/e",
	"/v1/analytics",
	"/v1/analytics/batch",
	"/v1/tracker",
}

// retiredDoors are paths that WERE ingest doors and must now be gone from every
// surface — not routed, and not carved on a site host either. /v1/ingest was the
// publishable-key door; @hanzo/event 0.3.0 moved pk- onto /v1/event and a fleet sweep
// found no remaining caller, so it was deleted. A door is not retired until it is
// absent from BOTH surfaces, which is the half that used to be forgotten.
var retiredDoors = []string{"/v1/ingest"}

// notDoors are paths that must never ingest: the read lenses, near-miss spellings, and
// the neighbouring subsystem's route. They are the paired negative for every positive
// below — widen the door lookup to a prefix, or give it a default case, and these go
// red.
//
// The last row is the deliberate strictness. c.Path() is the RAW request target —
// zip returns Fiber's path verbatim and nothing upstream unescapes or normalizes it
// (see resolveKey in clients/sites) — and the carve matches it BYTE-EXACTLY. So an
// encoded or denormalized spelling of a real door misses the carve and is served as
// static, even where Fiber's own router would still reach the door (POST /v1/event/
// routes on an API host and does not carve on a site host). That asymmetry is chosen,
// not overlooked: the carve hands a request a tenant derived from a Host, so it admits
// only the exact strings it was given, and every near-miss fails to the static serve.
// Normalizing here to match the router would widen a security-relevant exact set to
// chase a routing convenience — the same mistake as the prefix match this set replaced.
var notDoors = []string{
	"/v1/analytics/overview", "/v1/analytics/timeseries", "/v1/analytics/top",
	"/v1/analytics/health", "/v1/analytics/anything", "/v1/analytics/batch/extra",
	"/v1/eventx", "/v1/insights/e/extra", "/v1/insights/events", "/v1/tracker/projects",
	"/v1/%65vent", "/v1/event/", "//v1/event", "/v1/./event", "/v1/x/../event",
}

func doorPaths() []string {
	p := make([]string, len(doors))
	for i, d := range doors {
		p[i] = d.path
	}
	return p
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// admittedWire returns the body, from cands, that THIS door's own wire decodes into
// exactly one event the anonymous lane ADMITS. Picking the body through the door's
// real decoder + the real projection is what lets every test below quantify over
// doors without a per-wire lookup table beside it — the thing whose duplication
// caused the drift in the first place.
func admittedWire(t *testing.T, d door, cands ...string) string {
	t.Helper()
	for _, b := range cands {
		evs, err := d.decode([]byte(b))
		if err != nil {
			continue
		}
		if kept, _ := admitPublic(evs); len(kept) == 1 {
			return b
		}
	}
	t.Fatalf("no candidate body is admitted by the anonymous lane on door %s", d.path)
	return ""
}

// droppedWire is the twin: exactly one decoded event that the anonymous lane REFUSES
// (a commerce/custom kind), which is what proves capability rather than reachability.
func droppedWire(t *testing.T, d door, cands ...string) string {
	t.Helper()
	for _, b := range cands {
		evs, err := d.decode([]byte(b))
		if err != nil || len(evs) != 1 {
			continue
		}
		if kept, dropped := admitPublic(evs); len(kept) == 0 && dropped == 1 {
			return b
		}
	}
	t.Fatalf("no candidate body is dropped by the anonymous lane on door %s", d.path)
	return ""
}

const (
	canonPageview = `{"batch":[{"type":"pageview","path":"/pricing"}]}`
	posthogPage   = `{"event":"$pageview","distinct_id":"anon-1","properties":{"$pathname":"/pricing"}}`
	canonCommerce = `{"batch":[{"type":"event","event":"order_completed","revenue":999,"groupId":"victim","personId":"victim-person"}]}`
	posthogEvent  = `{"event":"order_completed","distinct_id":"d","properties":{"revenue":999}}`
)

func pageviewFor(t *testing.T, d door) string {
	return admittedWire(t, d, canonPageview, posthogPage)
}

func commerceFor(t *testing.T, d door) string {
	return droppedWire(t, d, canonCommerce, posthogEvent)
}

// ── the surface is one set ──────────────────────────────────────────────────

// TestIngestSurfaceIsExactlyTheContract anchors doors against the hand-written
// contract. It is the test that makes every other test in this file meaningful:
// they all derive their tables from doors, so they would happily stay green while
// the surface changed underneath them.
func TestIngestSurfaceIsExactlyTheContract(t *testing.T) {
	if !sameSet(doorPaths(), wantDoors) {
		t.Fatalf("ingest surface = %v, contract = %v — adding or removing a door is a\n"+
			"public surface change; update wantDoors deliberately", doorPaths(), wantDoors)
	}
	for _, d := range doors {
		if d.decode == nil || d.source == "" {
			t.Errorf("door %s is incomplete (decode=%v source=%q): a door is a path bound to a wire",
				d.path, d.decode != nil, d.source)
		}
	}
}

// TestRoutedPostSetIsExactlyTheDoors reads the REGISTERED route table back out of the
// app and proves the POST surface is the door set — nothing more, nothing less. This
// is what a hand-written app.Post beside the loop would trip, which is exactly how the
// three lists drifted apart before.
func TestRoutedPostSetIsExactlyTheDoors(t *testing.T) {
	app := mountApp(t)
	var posts []string
	for _, r := range app.Fiber().GetRoutes() {
		if r.Method == http.MethodPost {
			posts = append(posts, r.Path)
		}
	}
	if !sameSet(posts, doorPaths()) {
		t.Fatalf("registered POST routes = %v, declared doors = %v — every ingest route must\n"+
			"come from doors, and nothing else may be registered as a POST here", posts, doorPaths())
	}
}

// TestEveryDoorIsRoutedAndAdmits is the positive half on the API host: each declared
// door actually exists (never 404) and reaches the write core for an admissible
// anonymous event (503, no datastore in the harness).
func TestEveryDoorIsRoutedAndAdmits(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	for _, d := range doors {
		code, body := doHost(t, app, d.path, "", "", "api.hanzo.ai", pageviewFor(t, d))
		if code == http.StatusNotFound {
			t.Errorf("door %s is declared but not routed (404)", d.path)
			continue
		}
		if code != http.StatusServiceUnavailable {
			t.Errorf("door %s pageview = %d (%s), want 503 (admitted, datastore down)", d.path, code, body)
		}
	}
}

// TestRetiredDoorIsGoneFromBothSurfaces is the deletion proof, and it checks BOTH
// surfaces because deleting a route while leaving the carve entry (or the reverse) is
// the exact failure mode this whole change removes. A retired door must 404 on the API
// host and fall to the static serve (405) on a site host.
func TestRetiredDoorIsGoneFromBothSurfaces(t *testing.T) {
	api := mountApp(t)
	site := carveApp(t, "hanzo")
	for _, p := range retiredDoors {
		if code, body := doHost(t, api, p, "", "", "api.hanzo.ai", canonPageview); code != http.StatusNotFound {
			t.Errorf("retired door %s is still routed on the API host: %d (%s)", p, code, body)
		}
		if code := postHost(t, site, "yadota.hanzo.app", p, canonPageview, nil); code != http.StatusMethodNotAllowed {
			t.Errorf("retired door %s is still carved on a site host: %d (want 405, static serve)", p, code)
		}
		for _, d := range doors {
			if d.path == p {
				t.Errorf("retired door %s is still declared in doors", p)
			}
		}
	}
}

// ── the carve set IS the door set ───────────────────────────────────────────

// TestSiteHostCarvesExactlyTheDoors is the reconciliation proof. On a live site host
// every declared door is carved to the anonymous lane under the SITE's org, and no
// non-door is — so the routed set (pinned exactly above) and the carved set are the
// same set. Before, they were not: /v1/tracker routed here and 405'd there.
//
// The negative half is the paired failure: hand sites anything other than the doors,
// or let its lookup fall back to a default, and a notDoors path starts carving.
func TestSiteHostCarvesExactlyTheDoors(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		app := carveApp(t, "hanzo")
		// A forged org on the wire must not win — the tenant is the resolved Site's.
		if code := postHost(t, app, "yadota.hanzo.app", d.path, pageviewFor(t, d),
			map[string]string{"X-Org-Id": "attacker"}); code != http.StatusServiceUnavailable {
			t.Errorf("door %s on a site host = %d, want 503 (carved, ingested for the site org)", d.path, code)
		}
	}
	app := carveApp(t, "hanzo")
	for _, p := range notDoors {
		if code := postHost(t, app, "yadota.hanzo.app", p, canonPageview, nil); code != http.StatusMethodNotAllowed {
			t.Errorf("non-door %s carved on a site host: %d (want 405, static serve)", p, code)
		}
	}
}

// TestSiteHostCarveNeedsAResolvedSite: the carve is gated on a Site actually
// resolving, not merely on the host looking like one. An unresolvable slug host falls
// to the static serve on EVERY door — no door turns an unbacked Host into a tenant.
func TestSiteHostCarveNeedsAResolvedSite(t *testing.T) {
	app := carveApp(t, "hanzo") // the resolver knows only "yadota"
	for _, d := range doors {
		if code := postHost(t, app, "nosuchsite.hanzo.app", d.path, pageviewFor(t, d), nil); code == http.StatusServiceUnavailable {
			t.Errorf("door %s ingested on an UNRESOLVED site host — the carve must require a resolved Site", d.path)
		}
	}
}

// ── the gate, quantified over every door ────────────────────────────────────

// TestEveryDoorFailsClosedOnUnresolvableCredential is THE admission gate. A caller that
// PRESENTED a credential which does not resolve is refused on every door — never
// silently downgraded into the anonymous lane, where its events would land in a
// partition its owner cannot read.
//
// Paired failure: delete handle's `if presented(c)` branch and every door answers 200
// or 503 instead of 403, and this fails on all of them at once.
func TestEveryDoorFailsClosedOnUnresolvableCredential(t *testing.T) {
	for _, d := range doors {
		app := mountApp(t)
		stubResolver(t, func(string) (string, bool) { return "", false })
		for _, hdr := range []map[string]string{
			{"x-api-key": "hk-nosuch"},
			{"Authorization": "Bearer pk-nosuch"},
			{"x-hanzo-ingest-key": "pk-nosuch"},
		} {
			if code := postKeyed(t, app, d.path, "hanzo.ai", commerceFor(t, d), hdr); code != http.StatusForbidden {
				t.Errorf("door %s with an unresolvable credential %v = %d, want 403 (fail closed)", d.path, hdr, code)
			}
		}
	}
}

// TestEveryDoorProjectsTheAnonymousCaller is the capability gate. With no credential
// of any kind, on a RECOGNIZED BRAND HOST, a commerce payload must be dropped — never
// stored, and never at full capability into a real org.
//
// 503 is the failure signal here, not the success one: it would mean the request
// reached the write core unprojected. Paired failure: give handle a host fallback, or
// let admitPublic see the org, and these turn 503.
func TestEveryDoorProjectsTheAnonymousCaller(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	for _, d := range doors {
		for _, host := range []string{"hanzo.ai", "api.hanzo.ai"} {
			code, body := doHost(t, app, d.path, "", "", host, commerceFor(t, d))
			if code == http.StatusServiceUnavailable {
				t.Errorf("door %s on host %q reached the write core at FULL capability — a "+
					"credential-less caller must never write revenue/groupId/personId into a real org", d.path, host)
				continue
			}
			if code != http.StatusOK {
				t.Errorf("door %s on host %q = %d (%s), want 200 all-dropped", d.path, host, code, body)
				continue
			}
			if r := receipt(t, body); r.Accepted != 0 || r.Dropped != 1 {
				t.Errorf("door %s on host %q receipt = %+v, want accepted:0 dropped:1", d.path, host, r)
			}
		}
	}
}

// TestEveryDoorAdmitsAValidatedPrincipal is the "the gate is not just a wall" half: a
// validated bearer keeps FULL capability on every door, so the commerce payload the
// anonymous lane drops is admitted here (503 = reached the write core).
func TestEveryDoorAdmitsAValidatedPrincipal(t *testing.T) {
	app := mountApp(t)
	for _, d := range doors {
		if code, body := doBody(t, app, http.MethodPost, d.path, "user-dave", "acme", commerceFor(t, d)); code != http.StatusServiceUnavailable {
			t.Errorf("door %s with a validated bearer = %d (%s), want 503 (admitted at full capability)", d.path, code, body)
		}
	}
}

// TestEveryDoorAdmitsAResolvedKey: the same for out-of-band keys — a resolvable hk-
// and a resolvable pk- both reach the write core at full capability on every door.
func TestEveryDoorAdmitsAResolvedKey(t *testing.T) {
	for _, d := range doors {
		app := mountApp(t)
		stubResolver(t, func(string) (string, bool) { return "acme", true })
		for _, hdr := range []map[string]string{
			{"x-api-key": "hk-good"},
			{"Authorization": "Bearer pk-good"},
		} {
			if code := postKeyed(t, app, d.path, "", commerceFor(t, d), hdr); code != http.StatusServiceUnavailable {
				t.Errorf("door %s with resolvable %v = %d, want 503 (admitted at full capability)", d.path, hdr, code)
			}
		}
	}
}
