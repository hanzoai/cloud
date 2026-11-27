// Package forge is the client for the deployment's code forge — the Forgejo
// instance (git.hanzo.ai beside api.hanzo.ai) that holds the estate's issues and
// milestones.
//
// It exists because those work items have ONE home. The forge is where an issue
// is filed, labelled, assigned and closed; a second copy in another store would
// be a second answer to "what is the state of this work", and the two would
// drift. So nothing here mirrors or writes through to a local table: every read
// is a read OF the forge, and every write is a write TO it.
//
// The one thing held between a read and the next is a bounded STALENESS WINDOW
// on the two list endpoints that cost seconds rather than milliseconds — see
// cache.go, which states why that is not the drifting copy this paragraph
// refuses, and why its key must carry the actor. No write is cached, and a
// cached answer is only ever replaced by the forge's own.
//
// # The wire
//
// The forge answers its REST API at /v1 — NOT /api/v1, which 404s. That is the
// one fact most likely to be mis-remembered from upstream documentation, so it
// is stated once, here, as [API], and never spelled again.
//
// # The two credentials, and why there is only one
//
// A caller of this package is a REQUEST from a user, but the credential is the
// DEPLOYMENT's: one machine token, read from KMS (never an env file, never a
// browser-side PAT, never a per-user OAuth grant this process would have to
// custody). A per-user token would mean N secrets to rotate, revoke and leak;
// one token means one.
//
// One token would ordinarily mean one identity, and therefore one permission
// set — the machine's — applied to every user's request. That is the trap, and
// [Client.As] is the way out: Forgejo's Sudo lets an authorized token ACT AS a
// named user, and it DROPS PRIVILEGE to that user rather than merely relabelling
// the actor. Measured against this deployment's forge:
//
//	token alone,          GET /v1/repos/hanzo-private/patents  → 200
//	token + Sudo: deploy, GET /v1/repos/hanzo-private/patents  → 404
//	anonymous,            GET /v1/repos/hanzo-private/patents  → 404
//
// The sudoed request is byte-identical to the anonymous one: the forge's own
// ACL, not this client's good intentions, decided what came back. That is what
// makes one credential safe to hold. It also makes a write ATTRIBUTABLE — the
// issue's actor is the human, not a shared bot — which is the audit property a
// shared machine identity otherwise destroys.
//
// # Fail closed
//
// Sudo is not optional on a user-facing call and there is no "unsudoed
// fallback": [Client.As] with an empty login returns a client whose every call
// refuses ([ErrNoActor]), because falling back to the raw machine token on a
// missing actor is exactly the escalation this design exists to prevent. A
// login the forge does not know 404s, which surfaces as [ErrUnknownActor]
// rather than an empty result — an empty board and "you have no forge account"
// are different answers and must not be spelled the same way.
package forge
