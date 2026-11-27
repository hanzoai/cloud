// Package planetest is the money peer a billing test bills against: commerce's
// half of the internal plane, on a real socket, recording every debit that crosses.
//
// THE DEBIT LEFT HTTP. It used to be a JSON POST to commerce's /v1/billing/usage,
// and metering.Usage.Ref — the ledger's idempotency key — is tagged `json:"-"` so
// that no request body anywhere can set it. json.Marshal therefore dropped it and
// every split-deploy debit reached the ledger anonymous, which broke the
// exactly-once contract the same client holds on its co-resident path. The debit
// now crosses the plane, where the act's name is a field of its own.
//
// The fakes in the app packages still answer the balance READ over HTTP, because
// that is where it still happens. They receive the usage DEBIT here.
//
// IT IS ONE PACKAGE RATHER THAN A COPY PER CALLER, for the reason iamtest states
// and this file then proved: nine packages hand-rolled a usage counter against the
// HTTP body, the debit moved to the plane, and all nine kept counting an endpoint
// nothing calls any more. Every one of them reported zero debits and every one of
// them was asserting its own fixture. One recorder means the next time the wire
// moves, it moves in one place and the tests that depend on it fail loudly instead
// of quietly passing.
//
// OBSERVING HERE GRANTS NOTHING. The socket is a per-test temp dir the test itself
// owns; no deployment binds or reads it.
package planetest
