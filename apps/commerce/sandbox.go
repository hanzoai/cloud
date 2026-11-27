package commerce

import (
	"github.com/hanzoai/commerce/models/organization"

	"github.com/hanzoai/cloud/sdk/network"
)

// WHICH BOOKS AN OPERATION WRITES, answered in one place.
//
// A ledger address is (org, subject, currency, test) and the last part is not a
// mode this package may leave unsaid — sandbox money and real money are
// physically separate files, so writing the wrong one puts a tenant's grant in
// books the gate spends the other kind of money from.
//
// On mainnet the answer is the caller's, unchanged: the org's posture decides
// whether its charges are real, and that is the whole point of the posture.
//
// Off mainnet there is no other answer to give. A test network has its own chain
// and its own coin and no rail to a card, so live books there would be an account
// nobody can settle — `network.SandboxOnly` is what makes them unreachable rather
// than merely unasked for. Note it can only ever force sandbox: a deployment
// cannot turn a caller's sandbox charge into a real one, which is the direction
// that would cost somebody money.
func sandboxed(test bool) bool { return test || network.SandboxOnly() }

// born is the kind a new org's commerce record is created in, and it is the
// network's answer, not an org's.
//
// On mainnet a customer exists to pay, so an org is born live: the card it is
// asked for is charged, and the plan it buys is a plan it paid for. Born test,
// the checkout took a card against the sandbox and granted nothing real, which
// is a sale nobody made.
//
// Off mainnet it is test, the only kind the posture switch there will hold. An
// org that exists already keeps the kind its record states; moving it stays the
// platform's one explicit act.
func born() organization.Kind {
	if network.SandboxOnly() {
		return organization.Test
	}
	return organization.Live
}

// ceiling is the most any org transacts in, and it is the network's answer too.
//
// On mainnet there is no cap: the org's record decides, as its posture does.
// Off mainnet every org is held at test whatever its record says, so a record
// marked live — restored, copied, or flipped by a path this check never saw —
// still charges the sandbox and writes the sandbox books.
func ceiling() organization.Kind {
	if network.SandboxOnly() {
		return organization.Test
	}
	return organization.Live
}
