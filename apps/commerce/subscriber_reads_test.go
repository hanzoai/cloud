// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// THE ADDRESSES A SUBSCRIBER'S OWN ACCOUNT PAGE CALLS MUST BE MOUNTED HERE.
//
// A handler existing is not a route existing, and the gap between the two has
// now cost three separate features, each failing the same silent way:
//
//	/v1/billing/credits       a customer with grants saw an empty list
//	/v1/billing/tier          every PAYING customer served the free rate limit
//	/v1/billing/usage/rollup  nothing could say what plan you are on
//
// Every one of them was written, reviewed, vendored and never reachable.
// commerce owns its own api.Route() bundle that registers them, that bundle is
// behind //go:build cloud, and it is not compiled into this binary — so the
// handlers ship and the addresses answer 404. A 404 from an API a client
// swallows reads as "no data", which is why all three looked like empty state
// rather than like a fault, and why none of them was noticed by looking.
//
// So the guard is the LIST, not the mechanism: these are what a subscriber's
// account surface asks for, and if one stops being answerable the page it feeds
// goes quietly blank again. Adding a row here is how a new surface says which
// question it depends on.
//
// WHAT IT WATCHES MOVED WITH THE FOLD, and the failure now has two halves. The
// ADDRESSES are apps/billing's, and its own guard drives the live router to
// prove each is mounted and refuses. This half is the other one, and it is the
// one nothing else can see: an endpoint relaying an op that NOBODY PUBLISHES
// answers 503, which reads like an outage rather than like a missing registration — the
// same "no data" that made the original three invisible, wearing a different
// status. So this asserts the OPS exist on the plane, in the process that owns
// the rows, which is the half that would go missing here.
func TestASubscriberCanReadTheirOwnPlan(t *testing.T) {
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	// The registrations exactly as Mount makes them. A test that assembled a
	// different set would prove only that the set it invented is complete.
	exposePosture()
	exposePlan()
	exposeSale(riskGate(luxlog.New("subscriber-reads")))
	exposeGrants()

	published := map[string]bool{}
	for _, op := range cloud.Plane().Commands() {
		published[op.OperationID] = true
	}
	if len(published) == 0 {
		t.Fatal("the plane published no ops at all — this guard is watching nothing")
	}

	for _, r := range []struct{ op, answers string }{
		{plane.BillingPlans, "which plans exist, and what each costs"},
		{plane.BillingSubscriptions, "which plan this customer actually holds"},
		{plane.BillingRollup, "how much of that plan is left, and the wallet beside it"},
		{plane.BillingTier, "the rate this customer is served at"},
		{plane.BillingCredits, "the prepaid balance they bought"},
	} {
		if !published[r.op] {
			t.Errorf("%s is not published, so nothing can answer %s — the money endpoint "+
				"relays this by name and an op with no publisher answers 503, which reads "+
				"like an outage rather than like a registration nobody made", r.op, r.answers)
		}
	}
}
