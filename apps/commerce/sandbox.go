package commerce

import "github.com/hanzoai/cloud"

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
// nobody can settle — `cloud.SandboxOnly` is what makes them unreachable rather
// than merely unasked for. Note it can only ever force sandbox: a deployment
// cannot turn a caller's sandbox charge into a real one, which is the direction
// that would cost somebody money.
func sandboxed(test bool) bool { return test || cloud.SandboxOnly() }
