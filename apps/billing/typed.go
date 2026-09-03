package billing

// typed.go is the ONE place this package's typed ops reach for the request, and
// the only file here that calls cloud.Request. A typed op — func(ctx, *In)
// (*Out, error) — receives its decoded input and a context and nothing else, so
// the facts below are resolved here and nowhere else:
//
//   - the TENANT comes off the context (principal.OrgFrom, parked by
//     cloud.Bridge, which the composer installs — never this package). It is
//     never an In field: an In field is caller-supplied, so a tenant key read
//     from one is a cross-tenant read the caller asserted for itself.
//   - the wallet SUBJECT is the payer half of the money address, resolved by
//     the ONE rule (principal.Subject over the minted X-User-Name + the signed
//     billing_account claim) — headers only the request carries.
//   - the USER for the per-account breakdown is the validated X-User-Id, which
//     the org alone does not carry.
//
// Every resolver fails closed off the HTTP path: no request means no attested
// payer and no wallet to read, so an op refuses rather than inventing one.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op into zipdoc_gen.go — the only
// way that prose reaches the published document, the MCP tool list and the CLI.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the mounted service into the typed handlers, exactly as the raw
// handlers reach it through cloud.Handle.
type ops struct{ s *cloud.Service[state] }

// payer resolves the caller of a finance read: the validated org (the ledger)
// and the wallet subject within it. The org comes off the context; the subject
// needs the request, because the payer rides headers (X-User-Name, the signed
// billing_account claim) the org does not carry. The 401 is the same answer
// every finance read has always given an absent identity.
func payer(ctx context.Context) (org, subject string, err error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		// Off the HTTP path there is no credential to resolve a wallet from.
		return "", "", zip.ErrUnauthorized("sign in to view finance")
	}
	org, ok = principal.OrgFrom(ctx)
	if !ok {
		// A TRUSTED SERVICE names its tenant instead of carrying a session, and
		// readerOrg is where that rule lives — the same one `balance` reads by, for
		// the same reason. Every app is its own child PROCESS, so ai cannot see an
		// in-process reader hook and asks over HTTP with COMMERCE_SERVICE_TOKEN;
		// that request has no session, so OrgFrom alone is empty and the read was
		// refused. balance already resolved this and the copy here did not follow,
		// which is precisely the drift the comment on readerOrg warns about.
		//
		// It is not a widening: readerOrg admits a validated principal first and a
		// service token ONLY when it names an org, and the subject stays
		// server-resolved below — a caller still cannot ask about somebody else's
		// wallet by putting a name in the body.
		org, ok = readerOrg(c)
	}
	if !ok {
		return "", "", zip.ErrUnauthorized("sign in to view finance")
	}
	return org, subjectFor(c, org), nil
}

// caller resolves the (org, user) pair the per-account breakdown is scoped to.
// The user is the validated X-User-Id — guaranteed non-empty once the org
// resolved, exactly as the raw handler relied on — and it needs the request
// because the org alone does not carry it.
func caller(ctx context.Context) (org, user string, err error) {
	org, err = principal.Acting(ctx)
	if err != nil {
		return "", "", err
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", zip.ErrUnauthorized("sign in to view billing")
	}
	return org, c.User(), nil
}

// ── the card endpoints and the sweep ─────────────────────────────────────────
//
// FOUR of this package's nine own operations. The other five are BYTE RELAYS and
// a two-status replay, named in untypedByDesign (typed_wire_test.go) with the
// wire that keeps each raw — the line to hold when adding one is that a route
// whose answer is COMMERCE'S OWN BYTES cannot be typed without this package
// inventing a shape for somebody else's ledger, which is the second definition
// the plane exists to avoid.

// requestOf is the *zip.Ctx a money op needs, off the context. Three facts here
// come off the REQUEST and not off the org alone: the PAYER (an org pools at the
// org while a per-member org resolves per user, which is principal.Subject), the
// caller's retry key (X-Idempotency-Key), and platform authority for the sweep.
//
// FAILS CLOSED off the HTTP path. A charge with no attested caller has no payer,
// and inventing one would bill somebody who never asked.
func requestOf(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	return c, nil
}

// topupIn is what both top-up endpoints take. They differ in WHICH card: one
// takes a fresh single-use token, the other a card the subject already saved.
type topupIn struct {
	// SourceID is a single-use card token from the payment form, for the token
	// endpoint. It is vaulted as part of the charge, so a caller never holds card
	// numbers and this service never sees one.
	SourceID string `json:"sourceId,omitempty" url:"-"`
	// MethodID names a card the subject already saved, for the saved-card endpoint.
	MethodID string `json:"paymentMethodId,omitempty" url:"-"`
	// AmountCents is how much to charge, in cents of Currency. Required.
	AmountCents int64 `json:"amountCents" url:"-"`
	// Currency is the ISO-4217 code to charge in. Empty takes the deployment's
	// own default.
	Currency string `json:"currency,omitempty" url:"-"`
	// IdempotencyKey is the caller's own retry key, from the X-Idempotency-Key
	// header. Sending the same key twice settles ONE charge and returns the first
	// receipt, which is what makes a retried top-up safe. Omit it and the store
	// derives one over the stable facts in a window, which is what a browser
	// sending no header has always relied on.
	IdempotencyKey string `json:"-" header:"X-Idempotency-Key"`
}

// TopupWithToken charges a single-use card token and credits the caller's
// balance.
//
// The token comes from the payment form and is vaulted as part of the charge, so
// no card number reaches this service and none is stored here. The receipt names
// the ledger entry, the new balance, and the PROCESSOR's own reference — which is
// the only field that proves money moved at the gateway rather than only in our
// ledger.
//
// Retry-safe on X-Idempotency-Key: the same key settles one charge and returns
// the first receipt.
func (o ops) topupToken(ctx context.Context, in *topupIn) (*plane.Charged, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "topup", func(ctx context.Context) (*plane.Charged, error) {
		return commercepeer.BillingTopupCard(ctx, &plane.CardIn{
			SourceID:       in.SourceID,
			AmountCents:    in.AmountCents,
			Currency:       in.Currency,
			Subject:        subject,
			IdempotencyKey: retryKey(c),
		})
	})
}

// TopupWithSavedCard charges a card the caller already saved and credits the
// balance. Same receipt and the same retry safety as the token endpoint; the only
// difference is which card, so a caller topping up from a saved method never
// re-enters one.
func (o ops) topupSaved(ctx context.Context, in *topupIn) (*plane.Charged, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "topup", func(ctx context.Context) (*plane.Charged, error) {
		return commercepeer.BillingTopup(ctx, &plane.SavedCardIn{
			MethodID:       in.MethodID,
			AmountCents:    in.AmountCents,
			Currency:       in.Currency,
			Subject:        subject,
			IdempotencyKey: retryKey(c),
		})
	})
}

// alertRef addresses one spend cap.
type alertRef struct {
	// ID is the cap to remove, from the path.
	ID string `json:"id"`
}

// DropAlert removes one of the caller's spend caps and answers 204.
//
// Removing a cap RAISES what the org may spend, so it takes the same authority
// setting one does. The caps that remain still bind: this drops one, never the
// whole policy.
func (o ops) dropAlert(ctx context.Context, in *alertRef) (*cloud.Unit, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	if err := capAdmin(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	if _, err := ask(ctx, org, "drop cap", func(ctx context.Context) (*plane.Dropped, error) {
		return commercepeer.BillingAlertDrop(ctx, &plane.AlertRef{Subject: subject, ID: in.ID})
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

// RechargeAll sweeps every org's auto-recharge and answers what it did.
//
// PLATFORM AUTHORITY ONLY. It charges saved cards across every tenant, so an org
// owner reaching it could sweep-charge the estate; a caller without it is
// refused before anything is charged.
//
// The answer explains a sweep that charged nobody as readily as one that
// charged: it names how many orgs were considered and how many needed charging,
// with a row each.
func (o ops) rechargeAll(ctx context.Context, _ *cloud.Unit) (*plane.Recharge, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	if !mayMint(ctx) {
		return nil, zip.ErrForbidden("platform authority required to run the recharge sweep")
	}
	return ask(ctx, org, "recharge", func(ctx context.Context) (*plane.Recharge, error) {
		return commercepeer.BillingRecharge(ctx)
	})
}

// methodRef addresses one saved payment method.
type methodRef struct {
	// ID is the saved method to detach, from the path.
	ID string `json:"id"`
}

// DetachMethod removes one card or account the caller has saved.
//
// It detaches only the CALLER'S own — the wallet this request bills from,
// resolved server-side — so an id belonging to another customer of the same org
// is not something this operation can reach. A platform or service caller
// detaches on the subject's behalf, and that authority is decided HERE, where the
// credential is, and travels as a value: authority decided twice is authority
// that eventually disagrees with itself.
//
// The card is vaulted at the processor, so what goes is our token for it.
func (o ops) detachMethod(ctx context.Context, in *methodRef) (*plane.Detachment, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "detach method", func(ctx context.Context) (*plane.Detachment, error) {
		return commercepeer.BillingMethodDetach(ctx, &plane.MethodRef{
			ID:         in.ID,
			Subject:    subject,
			Privileged: principal.IsSuperAdmin(c),
		})
	})
}

// DetachPortalMethod is DetachMethod at the address a hosted checkout addresses
// it by. One set of rows, two spellings: a card detached at either is gone from
// both, because there is one store behind them.
func (o ops) detachPortalMethod(ctx context.Context, in *methodRef) (*plane.Detachment, error) {
	return o.detachMethod(ctx, in)
}
