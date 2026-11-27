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
	"strings"

	"github.com/hanzoai/account"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
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
		// the same reason: one resolver, so the two cannot drift, which is precisely
		// what the comment on readerOrg warns about.
		//
		// It is not a widening: readerOrg admits a validated principal and nothing
		// else, and the subject stays server-resolved below — a caller still cannot
		// ask about somebody else's wallet by putting a name in the body.
		org, ok = readerOrg(c)
	}
	if !ok {
		return "", "", zip.ErrUnauthorized("sign in to view finance")
	}
	return org, subjectFor(c, org), nil
}

// orgAdmin refuses act to anyone but an admin of the org the request acts in or
// the platform SuperAdmin — cloud.Admin, the one gate. The store's own gate admits
// any authenticated member, so this is the difference; the reads beside these acts
// stay member-open.
//
//   - A spend cap is a financial safety control, and both directions of getting
//     it wrong are expensive: a member who can delete the org's cap has unbounded
//     spend, and a member who can set a one-cent enforcing cap has an org-wide 402.
//   - An invoice is the merchant's demand on a customer, and collecting one spends
//     that customer's credits and prepaid balance. In the shared signup org every
//     member is a stranger to the next, so a member who could raise and collect an
//     invoice naming a neighbour could spend the neighbour's wallet.
//   - Auto-reload charges the ORG's default card off-session, with nobody there.
//     It is one rule per org, so even a member billed from a wallet of their own
//     is setting the org's rule, not theirs.
//
// It runs in the HANDLER rather than on the route, for the reason the risk screen
// does: a typed op is reached by four projections and only the handler is the
// point all four pass through. The bits it reads are the unforgeable ones the
// identity boundary mints, never a client header.
func orgAdmin(ctx context.Context, act string) error {
	c, ok := cloud.Request(ctx)
	if !ok || !principal.Validated(c) {
		return principal.Unauthenticated()
	}
	if administers(c) {
		return nil
	}
	return askAnAdmin(c, act)
}

// administers is cloud.Admin — the platform, or an admin of the org the request
// acts in — with the one org where an org admin is not an admin of its money:
// the shared signup org. Its members are strangers who each pay from a wallet of
// their own, and its account is the platform's, so acting on either is acting
// across customers, which is the platform's alone (owner == "admin"). An admin
// of that org holds no more there than any member does.
func administers(c *zip.Ctx) bool {
	a := cloud.AuthorityOf(c)
	if cloud.Super.Admits(a) {
		return true
	}
	org, _ := principal.Org(c)
	return cloud.Admin.Admits(a) && !strings.EqualFold(org, account.SignupOrg)
}

// askAnAdmin is the refusal a member reads: what only an admin of the org may do,
// and who to ask. It is a 403 problem whose detail is that one plain sentence.
func askAnAdmin(c *zip.Ctx, act string) error {
	org, ok := principal.Org(c)
	if !ok {
		org = "this organization"
	}
	if strings.EqualFold(org, account.SignupOrg) {
		// Its admins are not admins of its money (administers), so naming them
		// would send the caller to someone who cannot help either.
		return zip.ErrForbidden("In " + org + ", only a platform admin can " + strings.Replace(act, " its ", " the ", 1) + ".")
	}
	return zip.ErrForbidden("Only an admin of " + org + " can " + act + ". Ask an admin to do it.")
}

// The acts askAnAdmin names in its refusal.
const (
	capsAct      = "change its spend caps"
	invoicesAct  = "act on its invoices"
	planAct      = "change its plan"
	cancelAct    = "cancel its plan"
	resumeAct    = "resume its plan"
	seatsAct     = "assign its seats"
	resizeAct    = "change its seats"
	reloadAct    = "change its auto-reload"
	cardsAct     = "change its cards"
	removeAct    = "remove its cards"
	chargeOnFile = "charge its card on file"
)

// standing is what a money write may move, resolved once from the caller's own
// credential: the ledger and wallet this request bills (payerOf), whether the
// caller holds the org's own authority, and whether the wallet is their own.
type standing struct {
	org, subject string
	// all is an admin of the org or the platform: the org's own account and every
	// wallet in it.
	all bool
	// own is a wallet of the caller's own — a person account in the shared signup
	// org — rather than the org's pool.
	own bool
}

// ownWallet reports a wallet that is the caller's own person account inside the
// org's ledger — a member of the shared signup org paying for themselves — and
// nothing else: not the org's pool, not a project's account, not another
// person's. An unnamed wallet is nobody's.
func ownWallet(c *zip.Ctx, org, subject string) bool {
	return subject != "" && subject == account.Person(org, meter.Name(c)).Subject()
}

// steward resolves a write that moves a wallet's money, and refuses a caller who
// may move none of it.
//
// The org's own account — its plan, its cards, a charge on its card on file — is
// its admins' to change (administers), and the platform's. A member billed from
// a wallet of their own changes that wallet and nothing else, which is the rule
// commerce applies to a sale (api/billing subscribe_card.go). A member billed
// from the org's pool holds nothing, and is told who does. A personal org is an
// org of one whose owner is its admin, so its owner passes as the admin.
func steward(c *zip.Ctx, act string) (standing, error) {
	org, subject, err := payerOf(c)
	if err != nil {
		return standing{}, err
	}
	if subject == "" {
		// A credential in the signup org that names no person resolves no
		// wallet: it is unattributed, not a member to refuse.
		return standing{}, principal.Unauthenticated()
	}
	s := standing{
		org:     org,
		subject: subject,
		all:     administers(c),
		own:     ownWallet(c, org, subject),
	}
	if !s.all && !s.own {
		return standing{}, askAnAdmin(c, act)
	}
	return s, nil
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
	// Provider names the explicit payment processor ("square" or "paypal") when
	// SourceID is a provider-specific order/token (e.g. a PayPal v2 Order ID).
	Provider string `json:"provider,omitempty" url:"-"`
	// IdempotencyKey is the caller's own retry key, from the X-Idempotency-Key
	// header. Sending the same key twice settles ONE charge and returns the first
	// receipt, which is what makes a retried top-up safe. Omit it and the store
	// derives one over the stable facts in a window, which is what a browser
	// sending no header has always relied on.
	IdempotencyKey string `json:"-" header:"X-Idempotency-Key"`
	// Phone and Billing are what the buyer typed in the payment form; their name and
	// email are the identity's, never a field here. They come after every older
	// field so the plane layout of those fields holds across a rollout.
	Phone   string                 `json:"billingPhone,omitempty" url:"-"`
	Billing *client.BillingAddress `json:"billingAddress,omitempty" url:"-"`
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
// the first receipt. An org selected with X-Org-Id that the sign-in does not list
// is 409, and nothing is charged.
func (o ops) topupToken(ctx context.Context, in *topupIn) (*client.Charged, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := opened(c); err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "topup", func(ctx context.Context) (*client.Charged, error) {
		return commercepeer.BillingTopupCard(ctx, &client.CardIn{
			SourceID:       in.SourceID,
			AmountCents:    in.AmountCents,
			Currency:       in.Currency,
			Subject:        subject,
			IdempotencyKey: retryKey(c),
			Provider:       in.Provider,
			Name:           callerName(c),
			Email:          callerEmail(c),
			Phone:          strings.TrimSpace(in.Phone),
			Billing:        in.Billing,
		})
	})
}

// paypalOrderIn asks for a server-priced PayPal REST v2 order for either a
// bounded credit top-up (when PlanID is empty) or a plan purchase (when PlanID
// is set).
type paypalOrderIn struct {
	// AmountCents is the top-up amount in whole cents when PlanID is empty.
	AmountCents int64 `json:"amountCents,omitempty" url:"-"`
	// PlanID is the plan slug when buying a plan's first period; empty for a top-up.
	PlanID string `json:"planId,omitempty" url:"-"`
	// StoreID scopes the plan catalog when set.
	StoreID string `json:"storeId,omitempty" url:"-"`
	// Quantity is the seat count for a per-seat plan.
	Quantity int `json:"quantity,omitempty" url:"-"`
	// Lines are a team's additional seat types bought on the same subscription.
	Lines []client.SaleLine `json:"lines,omitempty" url:"-"`
	// Level selects which published price level of the plan to buy.
	Level int `json:"level,omitempty" url:"-"`
	// Interval selects the billing term ("month" or "year").
	Interval string `json:"interval,omitempty" url:"-"`
	// Currency is the ISO-4217 code (default usd).
	Currency string `json:"currency,omitempty" url:"-"`
	// Coupon is an optional plan coupon code for a monthly subscription.
	Coupon string `json:"coupon,omitempty" url:"-"`
	// BalanceCents is an organization's optional starting usage balance.
	BalanceCents int64 `json:"balanceCents,omitempty" url:"-"`
	// IdempotencyKey is the caller's retry key from X-Idempotency-Key.
	IdempotencyKey string `json:"-" header:"X-Idempotency-Key"`
}

// CreatePayPalOrder opens a server-priced PayPal REST v2 order for either a
// bounded credit top-up (intent CAPTURE) or a plan purchase's first period
// (intent AUTHORIZE).
//
// No balance or subscription row is minted here: the browser hands the returned
// `orderId` to the PayPal Smart Buttons SDK for buyer approval, then settles
// the payment through POST /v1/billing/topup/token or
// POST /v1/billing/subscribe/card with `provider: "paypal"`.
func (o ops) paypalOrder(ctx context.Context, in *paypalOrderIn) (*client.PayPalOrderOut, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := opened(c); err != nil {
		return nil, err
	}
	var (
		callCtx      = ctx
		org, subject string
		chosen       bool
	)
	if principal.Validated(c) {
		if strings.TrimSpace(in.PlanID) != "" {
			st, serr := steward(c, planAct)
			if serr != nil {
				return nil, serr
			}
			org, subject = st.org, st.subject
		} else {
			org, subject, err = payerOf(c)
			if err != nil {
				return nil, err
			}
		}
		chosen = chose(c, org)
	} else {
		if strings.TrimSpace(c.Header("Authorization")) != "" {
			return nil, principal.Unauthenticated()
		}
		planID := strings.TrimSpace(in.PlanID)
		if planID == "" || in.BalanceCents != 0 {
			return nil, principal.Unauthenticated()
		}
		callCtx, org, subject = guestSale(c, planID, in.Interval)
	}
	return ask(callCtx, org, "paypal order", func(ctx context.Context) (*client.PayPalOrderOut, error) {
		return commercepeer.BillingPayPalOrder(ctx, &client.PayPalOrderIn{
			Host:           c.Host(),
			AmountCents:    in.AmountCents,
			PlanID:         in.PlanID,
			StoreID:        in.StoreID,
			Quantity:       in.Quantity,
			Lines:          in.Lines,
			Level:          in.Level,
			Interval:       in.Interval,
			Currency:       in.Currency,
			Coupon:         in.Coupon,
			BalanceCents:   in.BalanceCents,
			Subject:        subject,
			IdempotencyKey: retryKey(c),
			Chosen:         chosen,
		})
	})
}

// TopupWithSavedCard charges a card the caller already saved and credits the
// balance. Same receipt and the same retry safety as the token endpoint; the only
// difference is which card, so a caller topping up from a saved method never
// re-enters one.
//
// The org's card on file is charged only by an admin of the org or the platform;
// a member is refused with 403 and told to ask an admin. A member billed from a
// wallet of their own charges that wallet's card. An org selected with X-Org-Id that
// the sign-in does not list is 409, and nothing is charged.
func (o ops) topupSaved(ctx context.Context, in *topupIn) (*client.Charged, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := opened(c); err != nil {
		return nil, err
	}
	// The card on file is the wallet's: the org's is charged by its admins.
	s, err := steward(c, chargeOnFile)
	if err != nil {
		return nil, err
	}
	return ask(ctx, s.org, "topup", func(ctx context.Context) (*client.Charged, error) {
		return commercepeer.BillingTopup(ctx, &client.SavedCardIn{
			MethodID:       in.MethodID,
			AmountCents:    in.AmountCents,
			Currency:       in.Currency,
			Subject:        s.subject,
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
	if err := orgAdmin(ctx, capsAct); err != nil {
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
	if _, err := ask(ctx, org, "drop cap", func(ctx context.Context) (*client.Dropped, error) {
		return commercepeer.BillingAlertDrop(ctx, &client.AlertRef{Subject: subject, ID: in.ID})
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
func (o ops) rechargeAll(ctx context.Context, _ *cloud.Unit) (*client.Recharge, error) {
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
	return ask(ctx, org, "recharge", func(ctx context.Context) (*client.Recharge, error) {
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
// is not something this operation can reach. The org's cards are removed only
// by an admin of the org or the platform; a member is refused with 403 and told
// to ask an admin. A platform or service caller
// detaches on the subject's behalf, and that authority is decided HERE, where the
// credential is, and travels as a value: authority decided twice is authority
// that eventually disagrees with itself.
//
// The card is vaulted at the processor, so what goes is our token for it.
func (o ops) detachMethod(ctx context.Context, in *methodRef) (*client.Detachment, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := steward(c, removeAct)
	if err != nil {
		return nil, err
	}
	return ask(ctx, s.org, "detach method", func(ctx context.Context) (*client.Detachment, error) {
		return commercepeer.BillingMethodDetach(ctx, &client.MethodRef{
			ID:         in.ID,
			Subject:    s.subject,
			Privileged: principal.Super(c),
		})
	})
}

// detachPortalMethod removes one card or account the caller has saved, at the
// address a hosted checkout reaches it by. One set of rows, two spellings: a card
// detached at either is gone from both, because there is one store behind them.
func (o ops) detachPortalMethod(ctx context.Context, in *methodRef) (*client.Detachment, error) {
	return o.detachMethod(ctx, in)
}
