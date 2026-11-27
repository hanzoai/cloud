// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// funds.go — THE PLACES MONEY, A PLAN OR A CAP CHANGES TELL THE DEPLOYMENT.
//
// A run refused for a usage limit or for want of funds waits (apps/coding park.go), and
// what it waits on is a change here: credits added to a wallet, a plan opened, a cap set,
// raised or dropped. The change is told ONCE, at the one place it is made, as a typed
// event on the coord bus (internal/funds) — never found by asking the balance, the plan
// or the cap on a timer.
//
//   - credit: the ledger's deposit (financeclient.SetDepositHook), the one place a
//     wallet's balance rises, whatever paid it: a card, a grant, a coupon, a comp.
//   - plan: a sale that opened a plan (planeSubscribe), one recorded outside checkout
//     (planeSubscriptionRecord), and a canceled one put back (planeSubscriptionReactivate).
//   - cap: the one write of a limit row (writeLimit, dropLimit). A limit on an org names
//     the org; a limit on a plan names the plan, since every org on the plan reads it and
//     the listener knows which of its waiting orgs are on it.
//
// A change that does not clear a refusal is told all the same: a listener asks the
// authority that refused, and a run that is still refused costs nothing. A change that
// names nobody is not told.

import (
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/sdk/coord"
)

// told says that kind changed for org.
func told(org, kind string) {
	if org == "" {
		return
	}
	funds.Tell(coord.Default(), funds.Event{Org: org, Kind: kind})
}

// toldCap says a limit row on scope (client.LimitScope's "org:<org>" or "plan:<slug>")
// changed: for the org it names, or for the plan, whose orgs the listener finds.
func toldCap(scope string) {
	kind, name, err := client.LimitScope(scope)
	switch {
	case err != nil:
		return
	case kind == client.LimitOrg:
		told(name, funds.Cap)
	default:
		funds.Tell(coord.Default(), funds.Event{Plan: name, Kind: funds.Cap})
	}
}
