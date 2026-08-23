// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/hanzoai/cloud/openapi"
)

// doors_test.go — the ingest SURFACE is one set, and these are its proofs.
//
// doors (event.go) is the only answer to "what is an ingest door"; the router derives
// from it, and the tests below hold that shut.
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
// rebinding one is as much a surface change as adding a path: swap /v1/event/insights/e onto
// decodeIngest and every PostHog beacon silently decodes to nothing, or relabel a
// door's source and the $source property — the only per-row record of which wire a
// write came in on — starts lying.
//
// TWO ENTRIES IS THE POINT: one door per WIRE. A path that merely renames a wire
// already served here is an alias, and the set below is what makes adding one an
// explicit act rather than a quiet convenience.
// ONE ENTRY IS THE POINT, and it is a stronger statement than the two that preceded
// it: a path per WIRE was still a path per SHAPE. /v1/event/insights/e was removed on
// 2026-07-31 and its wire kept — decodeEvent sniffs `distinct_id`/`api_key` and hands
// the PostHog body to decodeInsights — so the surface shrank without dropping a
// caller. sourcePostHog survives as a $source value on rows the old door wrote.
var wantDoors = []door{
	{path: "/v1/event", decode: decodeEvent, source: sourceEvent},
}

// samePtr reports whether two func values are the SAME function, by code pointer.
// It is the ONE mechanism in this package for asking that question — not "behaves
// similarly on the inputs I thought to try", but "is the same function". Used for the
// wire bindings (sameWire) and for the write path's client defaults, whose signatures
// differ, so the mechanism is untyped and each caller names its own question.
func samePtr(a, b any) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// sameWire asks samePtr's question about two decoders, typed: only a decode can be
// compared to a decode, which is what the doors contract needs.
func sameWire(a, b decode) bool { return samePtr(a, b) }

// retiredDoors are paths that WERE ingest doors and must now be gone from every
// surface — not routed, and not carved on a site host either. A door is not retired
// until it is absent from BOTH surfaces, which is the half that used to be forgotten.
//
//   - /v1/ingest was the publishable-key door; @hanzo/event 0.3.0 moved pk- onto
//     /v1/event and a fleet sweep found no remaining caller.
//   - /v1/analytics, /v1/analytics/batch and /v1/todo were name-aliases of the
//     canonical wire /v1/event already serves. @hanzo/capture, the one SDK that
//     named them, has no importer left in the fleet.
//
// /v1/todo is retired FROM THIS PACKAGE only, and this list is scoped to this
// package's own router. The path itself belongs to the todo product, which owns the prefix in the app manifest
// and keeps serving /v1/todo/projects/… — this app squatting the bare path is
// precisely what ends here. mountApp mounts it alone, so a 404 in this harness is
// the honest statement that it no longer answers there.
// Every entry is spelled as the address it HAD, because that is what a stale
// caller still sends. None of them is rewritten by the /v1/event fold — a
// retired path is retired at the spelling it was retired at.
var retiredDoors = []string{
	"/v1/ingest",
	// /v1/insights/e — the PostHog WIRE's own path. The wire is still served, on
	// /v1/event; only the second path is gone. insights.hanzo.ai's /e, /batch and
	// /capture reach it through the ingress rewrite, so no caller moved.
	"/v1/insights/e",
	"/v1/analytics", "/v1/analytics/batch", "/v1/todo",
}

func doorPaths() []string {
	p := make([]string, len(doors))
	for i, d := range doors {
		p[i] = d.path
	}
	return p
}

// ── a warehouse a test can read back ────────────────────────────────────────

// warehouse is a substituted datastore: it reports ready and records what the write
// path actually committed, so a test can read the TENANT and the $source a lane
// actually wrote. Without it the pipeline stops at the readiness gate and every lane
// looks alike — a site-host beacon filed under the public tenant, or a stranger's
// payload filed under a customer's org, produce byte-identical responses.
//
// facts is the ONE storage commit (the publish the sink lands in event.*); stmts is
// every statement that reached warehouseExec. The flip made stmts a NEGATIVE space on
// the ingest path — no DDL (the plane's owner is hanzoai/o11y) and no wide INSERT
// (hanzo.events' second projection is gone) — so tests assert it stays EMPTY, which
// is what pins "one write path" as a fact rather than a comment.
type warehouse struct {
	stmts []string
	rows  [][]any
	facts []fact
}

// fakeWarehouse substitutes the write path's THREE clients for this test and restores
// them after.
//
// It stops the SINK first. warehouseExec is a process-global var and the drain writes
// through it too, so a running consumer is a SECOND writer into the very slice this
// test is about to assert on — and it does not even need this process to have
// published anything, because a durable stream hands it whatever an earlier run left
// behind. A test that substitutes the write path has to OWN it.
func fakeWarehouse(t *testing.T) *warehouse {
	t.Helper()
	w := &warehouse{}
	stopSink()
	origReady, origExec, origPublish := warehouseReady, warehouseExec, publish
	warehouseReady = func() bool { return true }
	warehouseExec = func(_ context.Context, stmt string, args ...any) error {
		w.stmts = append(w.stmts, stmt)
		if len(args) > 0 {
			w.rows = append(w.rows, args)
		}
		return nil
	}
	publish = func(_ context.Context, facts []fact) error {
		w.facts = append(w.facts, facts...)
		return nil
	}
	t.Cleanup(func() {
		warehouseReady, warehouseExec, publish = origReady, origExec, origPublish
	})
	return w
}

// TestWritePathClientsDefaultToTheRealThing pins what fakeWarehouse and stubResolver
// SUBSTITUTE: that in production each of these vars holds the real dependency.
//
// A client is a var, so it is exactly as easy to rebind at the DECLARATION as it is in a
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
func TestWritePathClientsDefaultToTheRealThing(t *testing.T) {
	for _, s := range []struct {
		name string
		got  any
		want any
	}{
		{"warehouseReady", warehouseReady, datastore.Ready},
		{"warehouseExec", warehouseExec, datastore.Exec},
		{"publish", publish, publishToStream},
		{"resolveKeyOrg", resolveKeyOrg, cloud.OrgForKey},
	} {
		if !samePtr(s.got, s.want) {
			t.Errorf("%s does not default to the real dependency — a substituted write client "+
				"discards rows behind a 200 receipt, and a substituted key client decides admission", s.name)
		}
	}
}

// tenants returns the org of every fact committed — the only place the tenant a lane
// actually wrote is visible. The wide row died with hanzo.events, so the fact's own
// envelope is where the tenant stamp is read now.
func (w *warehouse) tenants(t *testing.T) []string {
	t.Helper()
	out := make([]string, 0, len(w.facts))
	for _, f := range w.facts {
		out = append(out, f.org)
	}
	return out
}

// sources returns each committed fact's attributes['$source'] — the door it arrived
// through, and the signal the alias sunset is decided on. The write core stamps it
// into properties and the plane normalizer carries it into the attributes map.
func (w *warehouse) sources(t *testing.T) []string {
	t.Helper()
	out := make([]string, 0, len(w.facts))
	for _, f := range w.facts {
		out = append(out, f.attributes["$source"])
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
// exactly one event the PROJECTION admits. Picking the body through the door's
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
	t.Fatalf("no candidate body is admitted by the projection on door %s", d.path)
	return ""
}

// droppedWire is the twin: exactly one decoded event the PROJECTION refuses (a
// commerce/custom kind), which is what proves capability rather than reachability.
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
	t.Fatalf("no candidate body is dropped by the projection on door %s", d.path)
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
// $source is the signal the alias sunset is decided on — a door may be retired when
// its volume reaches zero — so the value declared in the table has to be the value
// that reaches the column.
func TestEveryDoorStampsItsOwnSource(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		w := fakeWarehouse(t)
		app := mountApp(t)
		if code, body := doBody(t, app, http.MethodPost, d.path, "user-dave", "acme", pageviewFor(t, d)); code != http.StatusOK {
			t.Fatalf("door %s = %d (%s), want 200 (written to the fake warehouse)", d.path, code, body)
		}
		if got := w.sources(t); len(got) != 1 || got[0] != d.source {
			t.Errorf("door %s wrote $source %v, want [%s]", d.path, got, d.source)
		}
	}
}

// ── the tenant is the credential's, and a beacon without one writes nothing ──

// TestApiHostAnonymousWritesNothing: a credential-less caller is REFUSED on every
// door, whatever Host it used, and reaches the warehouse not at all. There is no
// reserved tenant to fall back to — a row lands in the org a credential named or it
// does not land.
func TestApiHostAnonymousWritesNothing(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	for _, d := range doors {
		for _, host := range []string{"api.hanzo.ai", "hanzo.ai"} {
			w := fakeWarehouse(t)
			app := mountApp(t)
			code, body := doHost(t, app, d.path, "", "", host, pageviewFor(t, d))
			refusedAnon(t, "anonymous door "+d.path+" on host "+host, code, body)
			if got := w.tenants(t); len(got) != 0 {
				t.Errorf("anonymous door %s on host %q wrote tenants %v, want none — a beacon "+
					"nobody can attribute must not reach the warehouse", d.path, host, got)
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
	// GetRoutes(true) drops the `use` entries — middleware, which fiber keeps in the
	// same stack as routes and reports under every method at the prefix it gates.
	// cloud.Bridge is one of those (compose installs it so a typed op can read the
	// validated org), and so is every middleware Serve installs app-wide, so an
	// unfiltered read has never been "the POST surface" in the real binary either. A
	// middleware is a passthrough, not a door: it dispatches nothing.
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method == http.MethodPost {
			posts = append(posts, r.Path)
		}
	}
	// The POST surface is the doors PLUS the two routes on this surface that are
	// registered by hand, each because it is NOT a door:
	//
	//   - /v1/event/{project}/envelope|store — the obs error wire the event door
	//     carries. It forwards to the o11y plane's installed consumer
	//     (cloud.ObsErrorIngest) and is DSN-authenticated there: a wire on the one
	//     event door, not a new door for handle to admit.
	//   - /v1/event/replay — the session-replay snapshot door (replay.go). A `doors` row is
	//     a wire that decodes to []CaptureEvent and flows through the ONE write core
	//     onto the event plane; a snapshot batch is an opaque rrweb recording
	//     produced to a different consumer on a different transport, and it lands no
	//     warehouse row at all. It cannot be a row here without either a decoder that
	//     returns nothing (a door that always drops) or a second meaning for
	//     CaptureEvent. It shares ADMISSION — eventTenant, and the same refusals —
	//     which is the part this file exists to hold shut, and replay_test.go
	//     quantifies that gate over it directly.
	want := append(doorPaths(), "/v1/event/:project/envelope", "/v1/event/:project/store", replayPath)
	if !sameSet(posts, want) {
		t.Fatalf("registered POST routes = %v, want doors + the obs error wire = %v — every other\n"+
			"ingest route must come from doors, and nothing else may be registered as a POST here", posts, want)
	}
}

// TestEveryDoorIsRoutedAndAdmits is the positive half on the API host: each declared
// door actually exists (never 404) and, for a credential that resolves, reaches the
// write core (503, no datastore in the harness).
func TestEveryDoorIsRoutedAndAdmits(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	for _, d := range doors {
		code, body := doBody(t, app, http.MethodPost, d.path, "user-dave", "acme", pageviewFor(t, d))
		if code == http.StatusNotFound {
			t.Errorf("door %s is declared but not routed (404)", d.path)
			continue
		}
		if code != http.StatusServiceUnavailable {
			t.Errorf("door %s pageview = %d (%s), want 503 (admitted, datastore down)", d.path, code, body)
		}
	}
}

// TestRetiredDoorIsGone is the deletion proof: a retired door must 404 on the API host
// and be absent from the door table.
func TestRetiredDoorIsGone(t *testing.T) {
	api := mountApp(t)
	for _, p := range retiredDoors {
		if code, body := doHost(t, api, p, "", "", "api.hanzo.ai", canonPageview); code != http.StatusNotFound {
			t.Errorf("retired door %s is still routed on the API host: %d (%s)", p, code, body)
		}
		for _, d := range doors {
			if d.path == p {
				t.Errorf("retired door %s is still declared in doors", p)
			}
		}
	}
}

// ── the gate, quantified over every door ────────────────────────────────────

// TestEveryDoorFailsClosedOnUnresolvableCredential is THE admission gate. A caller that
// PRESENTED a credential which does not resolve is refused 403 on every door — never
// downgraded, because a downgrade files a misconfigured key's events where its owner
// cannot read them.
func TestEveryDoorFailsClosedOnUnresolvableCredential(t *testing.T) {
	for _, d := range doors {
		app := mountApp(t)
		stubResolver(t, func(string) (string, bool) { return "", false })
		for _, hdr := range []map[string]string{
			{"x-api-key": "sk-nosuch"},
			{"Authorization": "Bearer pk-nosuch"},
			{"x-hanzo-ingest-key": "pk-nosuch"},
		} {
			if code := postKeyed(t, app, d.path, "hanzo.ai", commerceFor(t, d), hdr); code != http.StatusForbidden {
				t.Errorf("door %s with an unresolvable credential %v = %d, want 403 (fail closed)", d.path, hdr, code)
			}
		}
	}
}

// TestEveryDoorRefusesTheAnonymousCaller is the capability gate. With no credential of
// any kind, on a RECOGNIZED BRAND HOST, a commerce payload is refused — never stored,
// and never at full capability into a real org.
//
// 503 is the failure signal here, not the success one: it would mean the request
// reached the write core. Paired failure: give handle a host fallback and these turn 503.
func TestEveryDoorRefusesTheAnonymousCaller(t *testing.T) {
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
			refusedAnon(t, "door "+d.path+" on host "+host, code, body)
		}
	}
}

// TestEveryDoorAdmitsAValidatedPrincipal is the "the gate is not just a wall" half: a
// validated bearer keeps FULL capability on every door, so the commerce payload a
// credential-less caller is refused for is admitted here (503 = reached the write core).
func TestEveryDoorAdmitsAValidatedPrincipal(t *testing.T) {
	app := mountApp(t)
	for _, d := range doors {
		if code, body := doBody(t, app, http.MethodPost, d.path, "user-dave", "acme", commerceFor(t, d)); code != http.StatusServiceUnavailable {
			t.Errorf("door %s with a validated bearer = %d (%s), want 503 (admitted at full capability)", d.path, code, body)
		}
	}
}

// TestEveryDoorAdmitsAResolvedKey: the same for out-of-band keys — a resolvable sk-
// and a resolvable pk- both reach the write core at full capability on every door.
func TestEveryDoorAdmitsAResolvedKey(t *testing.T) {
	for _, d := range doors {
		app := mountApp(t)
		stubResolver(t, func(string) (string, bool) { return "acme", true })
		for _, hdr := range []map[string]string{
			{"x-api-key": "sk-good"},
			{"Authorization": "Bearer pk-good"},
		} {
			if code := postKeyed(t, app, d.path, "", commerceFor(t, d), hdr); code != http.StatusServiceUnavailable {
				t.Errorf("door %s with resolvable %v = %d, want 503 (admitted at full capability)", d.path, hdr, code)
			}
		}
	}
}

// TestEveryUntypedRouteDeclaresItsBodies is the projection half of the untyped
// ledger. A route that cannot be a typed op still owes the document its SHAPE: an
// untyped route with no declaration publishes an operationId and nothing else, which
// no SDK generator can tell apart from a route that takes no body and returns none —
// so `POST /v1/event`, the door every Hanzo product beacons to, shipped in every
// generated SDK as a call with nowhere to put the event.
//
// It quantifies over untypedByDesign, not over doors, because that ledger IS the set
// of operations zip's registry cannot describe — so a refusal added there tomorrow
// owes its bodies by construction rather than by someone remembering. And it reads
// the DOCUMENT, not the registry, because the document is what ships.
func TestEveryUntypedRouteDeclaresItsBodies(t *testing.T) {
	published := publishedOps(t)
	for key := range untypedByDesign {
		method, path, _ := strings.Cut(key, " ")
		op, ok := published[key]
		if !ok {
			t.Errorf("%s is not in the published document at all", key)
			continue
		}
		if method == http.MethodPost && op.RequestBody == nil {
			t.Errorf("%s publishes no requestBody — an SDK generated from this offers a write "+
				"with nowhere to put the payload", key)
		}
		if relayed[key] {
			if op.Responses != nil {
				t.Errorf("%s declares a response, but it relays another plane's handler verbatim "+
					"— publishing a shape here would be inventing one", key)
			}
			continue
		}
		// Any declared media type counts: an asset route answers JavaScript, not
		// JSON (openapi.Bytes), and requiring application/json here would force a
		// document that lies about what the handler sets.
		resp, ok := op.Responses["2XX"]
		if !ok || len(resp.Content) == 0 {
			t.Errorf("%s publishes no 2XX body schema", key)
			continue
		}
		for media, m := range resp.Content {
			if len(m.Schema) == 0 {
				t.Errorf("%s publishes a 2XX %s with no schema", key, media)
			}
		}
		_ = path
	}
}

// relayed names the untyped operations whose RESPONSE this package cannot state
// because it does not produce one: both Sentry doors hand the request to whatever
// cloud.ObsErrorIngest installed and copy that handler's answer back verbatim. Their
// REQUEST is still declarable — an opaque envelope stream, openapi.Binary — so the
// silence is exactly one half, and it is named rather than left to look like an
// oversight.
var relayed = map[string]bool{
	"POST /v1/event/{project}/envelope": true,
	"POST /v1/event/{project}/store":    true,
}

// publishedOp is the sliver of an operation the declaration gates read.
type publishedOp struct {
	RequestBody *struct {
		Content map[string]struct {
			Schema map[string]any `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema map[string]any `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
}

// publishedOps projects the live router the way every consumer reads it — through
// JSON, keyed "METHOD /path" exactly as untypedByDesign writes an address.
func publishedOps(t *testing.T) map[string]publishedOp {
	t.Helper()
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "event", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Paths map[string]map[string]publishedOp `json:"paths"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	ops := map[string]publishedOp{}
	for path, item := range published.Paths {
		for method, op := range item {
			ops[strings.ToUpper(method)+" "+path] = op
		}
	}
	return ops
}

// TestEveryDoorDeclaresItsPolymorphicWire is the doors-specific half: the canonical
// four accept three shapes on one path, and naming ONE of them would publish an
// ingest API that cannot batch — which is most of what @hanzo/event does.
func TestEveryDoorDeclaresItsPolymorphicWire(t *testing.T) {
	published := publishedOps(t)
	for _, d := range doors {
		op := published["POST "+d.path]
		if op.RequestBody == nil {
			t.Errorf("POST %s publishes no requestBody", d.path)
			continue
		}
		schema := op.RequestBody.Content["application/json"].Schema
		if _, poly := d.wire.(openapi.OneOf); !poly {
			if len(schema) == 0 {
				t.Errorf("POST %s declares an empty request schema", d.path)
			}
			continue
		}
		alts, _ := schema["oneOf"].([]any)
		// FOUR since /v1/event/insights/e was folded in: decodeEvent sniffs the wire, so the
		// one door accepts the three canonical shapes AND the PostHog body. Declaring
		// three would publish an ingest API that silently accepts a fourth.
		if len(alts) != 4 {
			t.Errorf("POST %s declares %d alternatives, want the 4 decodeEvent accepts "+
				"(Event, []Event, CaptureBatch, insightsBody)", d.path, len(alts))
		}
	}
}
