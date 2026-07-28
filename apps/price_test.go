// Copyright © 2026 Hanzo AI. MIT License.

package apps

// price_test.go — the gate that makes price part of declaring a route surface.
//
// WHAT IT REPLACES. The edge gate's price table ended in `return 0`, so a surface
// nobody priced was free forever. Over-billing is loud and under-billing is silent,
// which is why "metering is opt-in per path" survived so long: nothing could tell you
// it had happened. Now the price is a field on the spec, its zero value is Undeclared,
// and this file is what refuses Undeclared — in CI, on the diff that adds the routes,
// on the author who wrote them, instead of on a customer months later.
//
// WHY IT ENUMERATES Wire() AND NOT A LIST OF ITS OWN. Wire() IS the mount order and
// the mounted set; cloud.MountAll iterates exactly this slice. A hand-kept copy of it
// would stop covering everything added after it was written, which is the failure mode
// being fixed, not a way to fix it. Anything derived from Wire() cannot drift from
// Wire().
//
// WHAT IT DOES NOT COVER, precisely: MountSpec.Prefixes bounds a subsystem's
// MIDDLEWARE, not where it may register ROUTES — routes register at absolute paths
// anywhere, by design. A route a subsystem registers outside its own prefixes resolves
// to whichever declared prefix contains it, and since zen declares "/v1" every /v1 path
// resolves to something. So this gate guarantees "every declared surface has a price",
// not "every registered path is priced by the subsystem that registered it". Closing
// that would take recording registrations on the scope Router at mount time.

import (
	"testing"

	"github.com/hanzoai/cloud"
)

// declareWire installs the REAL composition root's declaration — every spec enabled —
// so the price a request resolves to is the price Wire() states. It does not mount
// anything: Declare is the pure indexing step MountAll runs first, which is the only
// reason this can be asked without 111 subsystems opening stores and dialling
// providers.
func declareWire(t *testing.T) []cloud.MountSpec {
	t.Helper()
	specs := Wire()
	enable := make([]string, 0, len(specs))
	for _, s := range specs {
		enable = append(enable, s.Name)
	}
	cloud.Declare(specs, &cloud.Config{Enable: enable})
	return specs
}

// TestPriceDeclared is THE gate. Every subsystem in the composition root must say what
// one request to its surface costs. cloud.Free is a perfectly good answer — it is a
// price, stated on purpose and reviewed in the diff. cloud.Undeclared is not an answer,
// and it is the zero value, so this fails on exactly the case that used to ship
// silently: somebody added a surface and never said what it costs.
func TestPriceDeclared(t *testing.T) {
	specs := Wire()
	if len(specs) == 0 {
		t.Fatal("Wire() is empty — this gate would pass by enumerating nothing")
	}
	for _, s := range specs {
		if !s.Price.Declared() {
			t.Errorf("subsystem %q declares no Price. Add one to its cloud.MountSpec in Wire():\n"+
				"  Price: cloud.Free    — this surface costs nothing, on purpose\n"+
				"  Price: cloud.Metered — a meter DOWNSTREAM of the edge owns the charge\n"+
				"  Price: 5             — the edge charges 5 cents per request\n"+
				"Free is a price. Undeclared is an unanswered question, and it is why an "+
				"unpriced route used to be free forever.", s.Name)
		}
	}
}

// TestPriceNamesEverySpecOnce guards the gate itself: it fails if two specs share a
// name, because then one declaration silently governs another surface's routes and
// TestPriceDeclared would be satisfied by a name it never actually priced.
func TestPriceNamesEverySpecOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Wire() {
		if seen[s.Name] {
			t.Errorf("two specs are named %q — a price declared for one governs the other's surface", s.Name)
		}
		seen[s.Name] = true
	}
}

// TestEdgeChargesNothingToday pins the CURRENT commercial posture against the REAL
// declaration: every surface is Free or Metered, so the edge gate charges nothing and
// every charge is owned downstream. It is the frozen "no behaviour changed" proof for
// moving the price out of DefaultPrice's table and into Wire().
//
// It is NOT a rule that the edge must stay at zero. The day a surface is priced at the
// edge, this test names it and whoever priced it writes the number here — which is the
// point: a change in what customers are charged cannot land without editing a test that
// states what they are charged.
func TestEdgeChargesNothingToday(t *testing.T) {
	for _, s := range declareWire(t) {
		if got := s.Price.Cents(); got != 0 {
			t.Errorf("subsystem %q now charges %dc at the edge (declared %s). "+
				"If that is intended, state it here — the edge charging a customer is not "+
				"something a test should be silent about.", s.Name, got, s.Price)
		}
	}
}

// TestDeletedSelfMeteredListStillPricesAtZero is the equivalence proof for removing
// DefaultPrice's own table. selfMeteredPrefixes named eight subtrees the edge had to
// charge 0 for or double-bill; deleting it moved that answer into each surface's
// declaration, and this asserts the ANSWER did not change for a single one of them.
//
// It asserts the price is DECLARED and costs 0, not merely that it costs 0. Undeclared
// also costs 0, so "the number is still zero" would be satisfied by a surface that
// stopped being owned by anything — which is the accidental zero this whole change
// exists to stop being invisible. Two of these paths make that concrete: nothing in
// this repo registers /v1/mcp or /v1/s3, so they resolve through zen's "/v1" catch-all
// rather than a spec of their own, and if that prefix ever narrows they go Undeclared
// while still charging nothing. This is what notices.
func TestDeletedSelfMeteredListStillPricesAtZero(t *testing.T) {
	declareWire(t)
	for _, p := range []string{
		"/v1/ai/chat/completions",
		"/v1/agents/x/run",
		"/v1/commerce/billing/usage",
		"/v1/o11y/ingest",
		"/v1/mcp/tools/call",
		"/v1/functions/x/invoke",
		"/v1/s3/bucket/key",
		"/v1/translate",
		"/v1/agent",
		"/v1/agent/presets",
	} {
		got := cloud.PriceOf(p)
		if !got.Declared() {
			t.Errorf("PriceOf(%q) = undeclared — this path was on the deleted "+
				"selfMeteredPrefixes list and no surface owns it any more. It still charges "+
				"nothing, but by accident rather than by decision.", p)
			continue
		}
		if cents := got.Cents(); cents != 0 {
			t.Errorf("PriceOf(%q) = %s (%dc) — this path was on the deleted "+
				"selfMeteredPrefixes list, so an edge charge here bills the same work twice", p, got, cents)
		}
	}
}

// TestPriceOfResolvesThroughTheRealDeclaration proves the wire is live end to end: a
// request path resolves, through the boot index, to the price its OWNING spec declared
// in Wire(). Without this, PriceOf could return Undeclared for everything and every
// other price test in the tree would still pass — vacuously, because Undeclared and
// Free both charge nothing.
func TestPriceOfResolvesThroughTheRealDeclaration(t *testing.T) {
	specs := declareWire(t)
	byName := map[string]cloud.Price{}
	for _, s := range specs {
		byName[s.Name] = s.Price
	}
	// One measured probe per family, keyed on what the surface IS rather than on a
	// prefix string: the point is that resolution lands on the right owner.
	for _, tc := range []struct{ path, owner string }{
		{"/v1/ai/chat/completions", "ai"},
		{"/v1/agents/x/run", "agents"},
		{"/v1/ml/predict", "ml"},
		{"/v1/kms/config", "kms"},
		{"/v1/o11y/query", "o11y"},
		{"/v1/flags/decide", "flags"},
	} {
		gotOwner := cloud.SubsystemOf(tc.path)
		if gotOwner != tc.owner {
			t.Errorf("SubsystemOf(%q) = %q, want %q — the price would come from the wrong declaration", tc.path, gotOwner, tc.owner)
			continue
		}
		want, ok := byName[tc.owner]
		if !ok {
			t.Fatalf("Wire() no longer has a spec named %q — retarget this probe", tc.owner)
		}
		if got := cloud.PriceOf(tc.path); got != want {
			t.Errorf("PriceOf(%q) = %s, but %q declares %s", tc.path, got, tc.owner, want)
		}
	}
}

// TestFreeSurfacesAreNotGated is the cross-check that keeps the ONE declaration honest
// against the live paywall. cloud.Billable decides whether a request needs standing
// before it runs; a surface that declares it costs NOTHING must not be sitting behind
// it. The two directions of the drift this catches:
//
//   - someone declares a metering surface Free — the declaration lies about money that
//     really moves, and the price stops being reviewable;
//   - someone adds a free surface to spend.go's meteredTrees — a 402 in front of
//     something nobody charges for, which is an outage, not a leak.
//
// Only Free is asserted. The reverse (Metered ⇒ gated) is deliberately NOT asserted:
// several metered surfaces gate through their own ResourceMeter instead of the coarse
// paywall, so requiring it would either mis-declare them or widen what the live gate
// refuses — a change to real traffic, which does not belong in a test.
func TestFreeSurfacesAreNotGated(t *testing.T) {
	for _, s := range declareWire(t) {
		if s.Price != cloud.Free {
			continue
		}
		for _, p := range cloud.MountPrefixes(s.Name, s.Prefixes) {
			if cloud.Billable("POST", p) {
				t.Errorf("%q declares cloud.Free but cloud.Billable(POST, %q) is true — "+
					"either money really moves here (declare cloud.Metered) or the paywall is "+
					"gating something nobody charges for (drop it from spend.go's meteredTrees)", s.Name, p)
			}
		}
	}
}
