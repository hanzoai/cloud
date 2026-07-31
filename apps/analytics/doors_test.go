// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
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

// wantDoors is the ingest surface as a CONTRACT: the exact set of doors, each with
// the WIRE and the ORIGIN TAG it is bound to, written out by hand on purpose.
// Everything else in this package derives from doors, so without one literal to
// compare against, deleting a door or smuggling one in would keep every derived test
// green. Changing this list is changing the public surface, and it should take an
// edit here to do it.
//
// It pins the whole TRIPLE, not just the path. A door is a path bound to a wire, and
// rebinding one is as much a surface change as adding a path: swap /v1/analytics onto
// decodeInsights and every canonical-wire beacon silently decodes to nothing, or
// relabel a door's source and the $source column — which is the sunset signal, and
// the only per-row record of which door a write came through — starts lying.
// /v1/insights/e was REMOVED as a door on 2026-07-31: a wire is a shape, and a shape
// does not earn a path. Its wire did not go away — decodeEvent tries the canonical
// decoder and falls back to decodeInsights when canonical yields nothing — and the
// ingress rewrite that fed it (insights-cloud-ingest-rewrite: insights.hanzo.ai
// /e,/batch,/capture) now replacePaths onto /v1/event, so every PostHog-wire caller
// keeps working through the one door. sourcePostHog therefore no longer appears here;
// its 1 lifetime row is a probe, not traffic.
var wantDoors = []door{
	{path: "/v1/event", decode: decodeEvent, source: sourceEvent},
	{path: "/v1/analytics", decode: decodeIngest, source: sourceCapture},
	{path: "/v1/analytics/batch", decode: decodeIngest, source: sourceCapture},
	{path: "/v1/tracker", decode: decodeIngest, source: sourceCapture},
	{path: "/v1/event/collect", decode: decodeTeam, source: sourceTeam},
}

// samePtr reports whether two func values are the SAME function, by code pointer.
// It is the ONE mechanism in this package for asking that question — not "behaves
// similarly on the inputs I thought to try", but "is the same function". Used for the
// wire bindings (sameWire) and for the write path's seam defaults, whose signatures
// differ, so the mechanism is untyped and each caller names its own question.
func samePtr(a, b any) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// sameWire asks samePtr's question about two decoders, typed: only a decode can be
// compared to a decode, which is what the doors contract needs.
func sameWire(a, b decode) bool { return samePtr(a, b) }

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

// ── a warehouse a test can read back ────────────────────────────────────────

// warehouse is a substituted datastore: it reports ready and records every batch
// INSERT, so a test can read the TENANT and the $source a lane actually wrote.
// Without it the pipeline stops at the readiness gate and every lane looks alike —
// a site-host beacon filed under the public tenant, or a stranger's payload filed
// under a customer's org, produce byte-identical responses.
type warehouse struct{ rows [][]any }

// fakeWarehouse substitutes the write path's two seams for this test and restores
// them after. It also clears the DDL latch, which is process-global: a real earlier
// test could otherwise leave it set and skip the CREATE, or this test could leave it
// set and make a later one skip a real one.
func fakeWarehouse(t *testing.T) *warehouse {
	t.Helper()
	w := &warehouse{}
	origReady, origExec := warehouseReady, warehouseExec
	eventsTableReady.Store(false)
	warehouseReady = func() bool { return true }
	warehouseExec = func(_ context.Context, stmt string, args ...any) error {
		if len(args) > 0 { // the DDL carries none; only INSERTs land here
			w.rows = append(w.rows, args)
		}
		_ = stmt
		return nil
	}
	t.Cleanup(func() {
		warehouseReady, warehouseExec = origReady, origExec
		eventsTableReady.Store(false)
	})
	return w
}

// TestWritePathSeamsDefaultToTheRealThing pins what fakeWarehouse and stubResolver
// SUBSTITUTE: that in production each of these vars holds the real dependency.
//
// A seam is a var, so it is exactly as easy to rebind at the DECLARATION as it is in a
// test. Rebinding warehouseExec to a func returning nil discards every INSERT while
// the caller still gets its 200 {accepted:N} receipt, and rebinding warehouseReady to
// `true` removes the gate that would otherwise turn that into an honest 503 — silent
// data loss behind a success receipt, and NOTHING else in this package notices,
// because every test that reads a written row installs its own fake first and every
// test that does not read one only ever asserts a status code. resolveKeyOrg is the
// same shape on the admission side: bound to a func returning ("", false) it fails
// closed, but bound to one returning ("acme", true) any key at all buys a real org.
//
// So the default is asserted directly, by code pointer (samePtr) rather than by
// behaviour — the point is the IDENTITY of the callee, and calling the real
// datastore/IAM to observe its behaviour is exactly what a unit test cannot do.
//
// It also holds the fakes honest in the other direction: every substitution in this
// package restores through t.Cleanup, so if one ever leaks past its test this
// assertion is what notices.
func TestWritePathSeamsDefaultToTheRealThing(t *testing.T) {
	for _, s := range []struct {
		name string
		got  any
		want any
	}{
		{"warehouseReady", warehouseReady, datastore.Ready},
		{"warehouseExec", warehouseExec, datastore.Exec},
		{"resolveKeyOrg", resolveKeyOrg, cloud.OrgForKey},
	} {
		if !samePtr(s.got, s.want) {
			t.Errorf("%s does not default to the real dependency — a substituted write seam "+
				"discards rows behind a 200 receipt, and a substituted key seam decides admission", s.name)
		}
	}
}

// col reads one column of one written row by NAME, so these tests bind to the
// schema's column list rather than to offsets that a new column would shift.
func (w *warehouse) col(t *testing.T, row int, name string) any {
	t.Helper()
	idx := -1
	for i, c := range eventColumns {
		if c == name {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no column %q in eventColumns", name)
	}
	flat := w.rows[row]
	if len(flat)%len(eventColumns) != 0 {
		t.Fatalf("row %d has %d args, not a multiple of %d columns", row, len(flat), len(eventColumns))
	}
	return flat[idx]
}

// tenants returns the tenant_id of every row written — the fact the site-host lane
// and the anonymous lane must disagree about, and the only place that disagreement
// is visible.
func (w *warehouse) tenants(t *testing.T) []string {
	t.Helper()
	out := make([]string, 0, len(w.rows))
	for i := range w.rows {
		s, _ := w.col(t, i, "tenant_id").(string)
		out = append(out, s)
	}
	return out
}

// sources returns each written row's properties.$source — the door it arrived
// through, and the signal the alias sunset is decided on.
func (w *warehouse) sources(t *testing.T) []string {
	t.Helper()
	out := make([]string, 0, len(w.rows))
	for i := range w.rows {
		raw, _ := w.col(t, i, "properties").(string)
		out = append(out, decodeProps(t, raw)["$source"].(string))
	}
	return out
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
	// The Hanzo Team SPA wire: a BARE ARRAY, epoch-millis timestamp, snake_case
	// distinct_id. navigation folds to the pageview kind (admitted anonymously);
	// customEvent folds to the bare `event` kind (dropped), which is what makes the
	// capability assertions on this door mean something rather than just reachability.
	teamPageview = `[{"event":"navigation","properties":{"path":"/pricing"},"timestamp":1750000000000,"distinct_id":"u"}]`
	teamCommerce = `[{"event":"customEvent","properties":{"event":"order_completed","revenue":999},"timestamp":1750000000000,"distinct_id":"u"}]`

	// The person- and group-BINDING kinds, per wire. These are the two an anonymous
	// caller must never store (publicKinds admits pageview and error only), and the
	// kind is expressed differently in each wire — so the door's own wire has to be
	// used, or the assertion tests the DECODER's tolerance instead of the projection.
	canonIdentify   = `{"batch":[{"type":"identify","distinctId":"victim","personId":"victim-person"}]}`
	canonGroup      = `{"batch":[{"type":"group","groupId":"victim-team"}]}`
	posthogIdentify = `{"event":"$identify","distinct_id":"victim","properties":{}}`
	posthogGroup    = `{"event":"$groupidentify","distinct_id":"victim","properties":{}}`
	teamIdentify    = `[{"event":"setUser","properties":{},"timestamp":1750000000000,"distinct_id":"victim"}]`
	teamGroup       = `[{"event":"setGroup","properties":{},"timestamp":1750000000000,"distinct_id":"victim"}]`
)

// identifyFor / groupFor give the door its OWN wire's person- / group-binding event,
// picking whichever candidate that door's decoder accepts and the projection refuses.
func identifyFor(t *testing.T, d door) string {
	return droppedWire(t, d, canonIdentify, posthogIdentify, teamIdentify)
}

func groupFor(t *testing.T, d door) string {
	return droppedWire(t, d, canonGroup, posthogGroup, teamGroup)
}

func pageviewFor(t *testing.T, d door) string {
	return admittedWire(t, d, canonPageview, posthogPage, teamPageview)
}

func commerceFor(t *testing.T, d door) string {
	return droppedWire(t, d, canonCommerce, posthogEvent, teamCommerce)
}

// ── the surface is one set ──────────────────────────────────────────────────

// TestIngestSurfaceIsExactlyTheContract anchors doors against the hand-written
// contract. It is the test that makes every other test in this file meaningful:
// they all derive their tables from doors, so they would happily stay green while
// the surface changed underneath them.
func TestIngestSurfaceIsExactlyTheContract(t *testing.T) {
	want := make([]string, len(wantDoors))
	for i, d := range wantDoors {
		want[i] = d.path
	}
	if !sameSet(doorPaths(), want) {
		t.Fatalf("ingest surface = %v, contract = %v — adding or removing a door is a\n"+
			"public surface change; update wantDoors deliberately", doorPaths(), want)
	}
	// Same paths; now the BINDING behind each one. A door whose wire or origin tag
	// moved is a changed door even though the path set is untouched.
	byPath := make(map[string]door, len(doors))
	for _, d := range doors {
		byPath[d.path] = d
	}
	for _, w := range wantDoors {
		got := byPath[w.path]
		if got.decode == nil {
			t.Errorf("door %s has no wire: a door is a path BOUND to a decoder", w.path)
			continue
		}
		if !sameWire(got.decode, w.decode) {
			t.Errorf("door %s is bound to a different wire than the contract names — "+
				"rebinding a door silently changes what every caller's body decodes to", w.path)
		}
		if got.source != w.source {
			t.Errorf("door %s source = %q, contract = %q — $source is the per-row record of "+
				"which door a write came through, and the alias sunset is decided on it",
				w.path, got.source, w.source)
		}
	}
}

// TestEveryDoorStampsItsOwnSource is the behavioral half of the source binding: the
// contract above pins the table, this pins that the value declared there is the value
// that reaches the ROW. Without it, source could be pinned in the table and dropped on
// the way to the warehouse and both halves would still look right.
//
// It quantifies over doors × HANDLERS, because a door has two of them and they stamp
// $source independently: ingest (the API host, via handle) and anon (the site host,
// which calls publicIngest directly). Driving only the ingest half left the anon half
// free to stamp a CONSTANT, and $source is precisely the signal the alias sunset is
// decided on — the documented rule is that a door may be retired when its $source
// volume reaches zero, so an anon lane that stamped 'event' for every door would read
// as "/v1/tracker is dead" while site-host callers were still beaconing it. The
// sunset is a delete-the-route decision made on this column; it has to be true on
// EVERY lane that writes it, not just the one a test happened to drive.
func TestEveryDoorStampsItsOwnSource(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		w := fakeWarehouse(t)
		app := mountApp(t)
		if code, body := doBody(t, app, http.MethodPost, d.path, "user-dave", "acme", pageviewFor(t, d)); code != http.StatusOK {
			t.Fatalf("door %s = %d (%s), want 200 (written to the fake warehouse)", d.path, code, body)
		}
		if got := w.sources(t); len(got) != 1 || got[0] != d.source {
			t.Errorf("door %s ingest lane wrote $source %v, want [%s]", d.path, got, d.source)
		}

		w = fakeWarehouse(t)
		site := carveApp(t, "hanzo")
		if code := postHost(t, site, "yadota.hanzo.app", d.path, pageviewFor(t, d), nil); code != http.StatusOK {
			t.Fatalf("site-host door %s = %d, want 200 (admitted and written)", d.path, code)
		}
		if got := w.sources(t); len(got) != 1 || got[0] != d.source {
			t.Errorf("door %s anon lane wrote $source %v, want [%s] — the sunset metric must name "+
				"the door the beacon actually arrived through, on this lane too", d.path, got, d.source)
		}
	}
}

// ── the site-host lane, which is the one that derives a tenant from a Host ───

// TestSiteHostLaneWritesTheResolvedSiteOrg is the tenant proof for the carve, and the
// reason the warehouse seam exists. Every declared door, POSTed to a LIVE site host,
// must write rows under the RESOLVED Site.Org — not the reserved public tenant, and
// not the org the request claims in a header or body.
//
// Paired failures, all of which used to pass unnoticed because the pipeline stopped at
// the readiness gate and every case answered 503: pass publicTenant instead of org and
// a customer's own site analytics land in a partition they cannot read; honour the
// caller's X-Org-Id and a stranger writes into any org they can name.
func TestSiteHostLaneWritesTheResolvedSiteOrg(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		w := fakeWarehouse(t)
		app := carveApp(t, "hanzo")
		if code := postHost(t, app, "yadota.hanzo.app", d.path, pageviewFor(t, d),
			map[string]string{"X-Org-Id": "attacker", "X-User-Id": "attacker-user"}); code != http.StatusOK {
			t.Fatalf("site-host door %s = %d, want 200 (admitted and written)", d.path, code)
		}
		got := w.tenants(t)
		if len(got) != 1 || got[0] != "hanzo" {
			t.Errorf("site-host door %s wrote tenants %v, want [hanzo] — the carve must file a "+
				"beacon under the RESOLVED Site.Org", d.path, got)
		}
		for _, g := range got {
			if g == publicTenant {
				t.Errorf("site-host door %s filed the site's own beacon under %q, where its owner "+
					"cannot read it", d.path, publicTenant)
			}
			if g == "attacker" {
				t.Errorf("site-host door %s took the tenant from the caller's header", d.path)
			}
		}
	}
}

// TestSiteHostLaneNeverConsultsHandle: on a site host the anonymous lane is reached
// DIRECTLY, and it has to be. sites.Middleware runs before the identity boundary, so
// X-User-Id / X-Org-Id there are still raw client headers that nothing has validated —
// exactly the shape SanitizeIdentity would have minted for a real bearer.
//
// So a request carrying them must still be PROJECTED. If door.anon consulted handle,
// those headers would resolve a principal and buy full capability, and the commerce
// payload would become a row under whatever org the caller named. The assertion is on
// the ROW, not the status: with a warehouse in place "admitted" is a 200 too, so a
// status check alone cannot tell the two lanes apart.
func TestSiteHostLaneNeverConsultsHandle(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		w := fakeWarehouse(t)
		app := carveApp(t, "hanzo")
		code := postHost(t, app, "yadota.hanzo.app", d.path, commerceFor(t, d),
			map[string]string{"X-User-Id": "user-dave", "X-Org-Id": "acme"})
		if code != http.StatusOK {
			t.Fatalf("site-host door %s with raw identity headers = %d, want 200", d.path, code)
		}
		if got := w.tenants(t); len(got) != 0 {
			t.Errorf("site-host door %s STORED a commerce payload under %v — the site-host lane "+
				"consulted handle, so unvalidated headers bought full capability", d.path, got)
		}
	}
}

// TestApiHostAnonymousLaneWritesThePublicTenant is the other half of the tenant pair:
// on an API host a credential-less caller is the RESERVED public tenant, whatever Host
// it used. Together with the site-host test above, this is what makes each lane's
// tenant a checked fact rather than a comment — one must be $public and the other must
// not, so a change that collapses them fails on one side or the other.
func TestApiHostAnonymousLaneWritesThePublicTenant(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		for _, host := range []string{"api.hanzo.ai", "hanzo.ai"} {
			w := fakeWarehouse(t)
			app := mountApp(t)
			if code, body := doHost(t, app, d.path, "", "", host, pageviewFor(t, d)); code != http.StatusOK {
				t.Fatalf("anonymous door %s on %q = %d (%s), want 200", d.path, host, code, body)
			}
			got := w.tenants(t)
			if len(got) != 1 || got[0] != publicTenant {
				t.Errorf("anonymous door %s on host %q wrote tenants %v, want [%s] — no Host names a tenant",
					d.path, host, got, publicTenant)
			}
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
