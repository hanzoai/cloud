// Copyright © 2026 Hanzo AI. MIT License.

// Package mintauth is the ONE structural money-mint invariant for the commerce
// ledger: a write that MINTS spendable balance (credits the gateway-honored
// IAM-user wallet without a funded source) is refused unless its persistence
// context is authorized to mint.
//
// The invariant lives at the datastore write sink (datastore.Datastore.Put calls
// Enforce), NOT sprinkled across HTTP route gates. That is deliberate — every
// prior round of the money audit found ANOTHER route that reached a mint sink
// (billing/deposit, /zap billing.deposit, /allotment/grant, and then
// /v1/transaction). Gating routes is whack-a-mole; gating the SINK is closed
// form. No handler — present or future, on any route, in any package — can mint
// spendable balance without passing through an authorization, because the ledger
// layer itself refuses the write.
//
// Two orthogonal context markers compose the invariant:
//
//   - Gate  (WithGate/Gated): set ONCE at the HTTP→datastore boundary
//     (Organization.Namespaced, for every inbound *gin.Context), because ANY
//     inbound principal is potentially an untrusted org-level admin. A context
//     that never crosses that boundary — a background cron, a migration, a unit
//     test building a plain context — is UNGATED and mints freely, so the fix is
//     backward-compatible with every internal and test flow.
//
//   - Grant (WithAuthorized/Authorized): set ONLY by a proven mint authority —
//     MayMintMoney success (the internal service token or a platform global
//     admin, via PlatformOnly / the ZAP mint gate / the transaction mint gate),
//     a settled external payment (top-up charge, provider webhook settlement),
//     or a server-fixed / subscription-clamped grant (welcome credit, monthly
//     allotment). This is the capability that satisfies the Gate.
//
// Enforce refuses iff the write mints (Guarded.MintRequiresAuthorization) AND the
// context is Gated AND NOT Authorized. Fail-closed.
package mintauth

import (
	"context"
	"errors"
)

// ErrNotAuthorized is returned by Enforce when a spendable-balance mint is
// attempted through a mint-gated context that carries no mint authorization.
// It is a fail-closed refusal at the ledger layer, independent of any route gate.
var ErrNotAuthorized = errors.New(
	"commerce: minting spendable balance requires mint authorization " +
		"(internal service token, platform global admin, settled payment, or server-fixed grant)")

type gateKey struct{}
type grantKey struct{}

// WithGate marks ctx as mint-gated: a spendable-balance mint flowing through it
// MUST also be authorized (WithAuthorized) or the ledger sink refuses the write.
// Idempotent. Set at the HTTP→datastore boundary for every inbound request.
func WithGate(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if Gated(ctx) {
		return ctx
	}
	return context.WithValue(ctx, gateKey{}, true)
}

// Gated reports whether ctx crossed the HTTP boundary and is therefore subject
// to mint enforcement. An ungated context (background job, migration, test) is
// not enforced.
func Gated(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(gateKey{}).(bool)
	return v
}

// WithAuthorized marks ctx as authorized to mint spendable balance. Idempotent.
// Set ONLY after establishing a proven mint authority (see package doc). This is
// the ONE way an HTTP request grants itself the capability the Gate demands.
func WithAuthorized(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if Authorized(ctx) {
		return ctx
	}
	return context.WithValue(ctx, grantKey{}, true)
}

// Authorized reports whether ctx carries mint authorization.
func Authorized(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(grantKey{}).(bool)
	return v
}

// Guarded is implemented by ledger entities that can MINT spendable balance. The
// datastore write sink asks the entity whether THIS instance is a spendable mint
// (its money-semantics live with the entity, e.g. models/transaction), and only
// then requires authorization. Non-ledger entities do not implement it and are
// never enforced.
type Guarded interface {
	// MintRequiresAuthorization reports whether persisting this entity mints
	// spendable balance and therefore demands an authorized (or ungated) context.
	MintRequiresAuthorization() bool
}

// Require is THE mint-authority policy in one place: it refuses (ErrNotAuthorized)
// exactly when the context is mint-gated (an inbound HTTP principal) and NOT
// authorized. An ungated context (background cron, migration, unit test) or an
// authorized one passes. Both the ledger write sink (Enforce, below) and the
// chain-backed mint service (treasury.Mint) call it, so "who may mint" is
// defined once and cannot drift between the two enforcement points.
func Require(ctx context.Context) error {
	if !Gated(ctx) || Authorized(ctx) {
		return nil
	}
	return ErrNotAuthorized
}

// Enforce is THE money-mint invariant, called at the datastore write sink for
// every entity Put. It refuses a spendable-balance mint when the context is
// mint-gated (an inbound HTTP principal) and not authorized. It is a no-op for
// non-ledger entities, for non-mint writes (withdraws, holds, reads), and for
// ungated contexts (crons, migrations, tests) — so it closes the mint hole
// without touching any legitimate internal flow.
func Enforce(ctx context.Context, val interface{}) error {
	g, ok := val.(Guarded)
	if !ok || !g.MintRequiresAuthorization() {
		return nil
	}
	return Require(ctx)
}
