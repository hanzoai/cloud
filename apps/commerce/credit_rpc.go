// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/client"
	financeclient "github.com/hanzoai/cloud/finance"
	credit "github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// The CREDIT — the other side of a settlement, published for the same reason the
// gate and the debit are: the prepaid ledger has one writer and it is this process.
//
// finance_draw takes the payer's half; this credits. They are separate ops because
// they are separate acts with separate idempotency and separate authority, and
// folding them into one signed amount would make a sign error a transfer in the
// wrong direction.
//
// It exists for x402: a purchase moves money twice, out of the buyer's ledger and
// into the seller's, and until now the second half could only happen in a process
// that had the ledger linked — which the payment rail never does. A settlement that
// can debit but not credit is not a settlement, so this is what makes "both sides
// or neither" reachable from another binary.
//
// SAY WHAT THIS IS: the first op on the internal plane that CREATES money. The
// others move it out of a ledger, read it, or gate it; this one puts it in. The
// plane's whole boundary is the socket — 0700 in the fleet's own run dir, reachable
// only by the children the router spawned — and that was already the boundary
// protecting a secret read (client.KMSGet) and a debit (client.FinanceRecord). It is
// the same boundary and it is now carrying more weight. Narrowing it means peer
// credentials on the plane itself (SO_PEERCRED, per-op), which is a fleet-wide client
// and belongs to whoever owns it — not smuggled in behind a payment fix.

// exposeCredit publishes the ledger credit. Mount calls it.
func exposeCredit() {
	cloud.Plane().Post("/finance/credit", planeCredit,
		zip.WithOperationID(client.FinanceCredit),
		zip.WithSummary("Credit one subject's prepaid ledger, exactly once per ref"))
	cloud.Plane().Post("/finance/reverse", planeReverse,
		zip.WithOperationID(client.FinanceReverse),
		zip.WithSummary("Undo one deposit with its mirror entry, exactly once"))
	cloud.Plane().Post("/finance/draw", planeDraw,
		zip.WithOperationID(client.FinanceDraw),
		zip.WithSummary("Draw one payment the wallet must cover, exactly once per ref"))
	cloud.Plane().Post("/finance/hold", planeHold,
		zip.WithOperationID(client.FinanceHold),
		zip.WithSummary("Set one payment's amount aside in the wallet until it is made"))
	cloud.Plane().Post("/finance/return", planeReturn,
		zip.WithOperationID(client.FinanceReturn),
		zip.WithSummary("Return what one hold still keeps to the wallet"))
}

// Sets one payment's amount aside in the caller org's prepaid wallet: the payment
// is agreed now and made later — a job's, released when the work is — and the
// wallet must still hold it then. The money stays in the org's books, out of the
// wallet's spendable balance, until a draw under the same ref makes the payment
// from it or a return gives it back.
//
// A wallet that cannot cover the amount answers 402 and nothing moves. REF IS
// REQUIRED and is the idempotency key: the same hold again answers the first; a
// ref holding another amount, or one returned — whether it held first or this
// hold arrives after its return — is 409. The ORG comes from the CALLER, never
// the argument.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeHold(ctx context.Context, in *client.HoldIn) (*client.Holding, error) {
	org, err := callerOrg(ctx, "hold")
	if err != nil {
		return nil, err
	}
	amount, err := in.Amount.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("hold: " + err.Error())
	}
	if amount.Sign() <= 0 {
		return nil, zip.ErrBadRequest("hold: amount must be positive")
	}
	if in.Ref == "" {
		return nil, zip.ErrBadRequest("hold: ref is required (the idempotency key)")
	}
	h, err := holder("hold")
	if err != nil {
		return nil, err
	}
	entry, posted, herr := h.Hold(ctx, credit.UsageInput{
		Org: org, Subject: cmp.Or(in.Subject, org), Currency: amount.Currency().Code,
		Amount: credit.FromDecimal(amount.Decimal()), Model: in.Memo, Service: "x402", Ref: in.Ref,
	})
	switch {
	case errors.Is(herr, financeclient.ErrShortWallet):
		return nil, zip.ErrPaymentRequired(fmt.Sprintf("hold %s: %v", org, herr))
	case errors.Is(herr, financeclient.ErrRefReused), errors.Is(herr, financeclient.ErrReturned):
		return nil, zip.Errorf(http.StatusConflict, "hold: %v", herr)
	case herr != nil:
		return nil, zip.Errorf(500, "hold %s: %v", org, herr)
	}
	return &client.Holding{Entry: entry, Posted: posted}, nil
}

// Returns whatever one hold in the caller org's books still keeps to the wallet
// it came from, and answers how much: nothing once the payment was made or the
// hold already returned, so a second return moves nothing. The return is
// recorded even when nothing is held yet, so a hold under the ref that arrives
// after it — sent before the payment was given up, and slow on its way — is
// refused instead of setting the amount aside for nobody. The ORG comes from the
// CALLER, never the argument.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeReturn(ctx context.Context, in *client.ReturnIn) (*client.Returned, error) {
	org, err := callerOrg(ctx, "return")
	if err != nil {
		return nil, err
	}
	if in.Ref == "" {
		return nil, zip.ErrBadRequest("return: ref is required")
	}
	h, err := holder("return")
	if err != nil {
		return nil, err
	}
	back, rerr := h.Return(ctx, credit.UsageInput{Org: org, Subject: cmp.Or(in.Subject, org), Ref: in.Ref, Model: in.Memo})
	if rerr != nil {
		return nil, zip.Errorf(500, "return %s: %v", org, rerr)
	}
	return &client.Returned{Amount: client.Amount(back.Unwrap())}, nil
}

// holder is the ledger that can hold a payment, or the refusal naming why not.
func holder(what string) (financeclient.Holder, error) {
	fin, err := books(what)
	if err != nil {
		return nil, err
	}
	h, ok := fin.(financeclient.Holder)
	if !ok {
		return nil, fmt.Errorf("%s: this ledger cannot hold a payment", what)
	}
	return h, nil
}

// Draws one payment out of the caller org's prepaid wallet, all or nothing — the
// payer's half of a settlement, the twin of the credit below.
//
// A metered debit lands whatever the wallet holds, because the call it prices was
// admitted before it ran. A payment is decided before anything is sold, so it is
// checked and posted in one transaction here, where no concurrent spend can come
// between: a wallet that holds less answers 402 and nothing moves. It is posted as
// a payment rather than usage, so the spend cap and the usage page never count it
// as metered spend.
//
// REF IS REQUIRED and is the idempotency key: a second draw under it answers the
// first entry and moves nothing, even once the wallet has run dry; a ref already
// naming another amount is 409. The ORG comes from the CALLER, never the
// argument: a caller able to name the drawn org could spend someone else's books.
// An empty subject draws the org's own account.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeDraw(ctx context.Context, in *client.DrawIn) (*client.Drawn, error) {
	org, err := callerOrg(ctx, "draw")
	if err != nil {
		return nil, err
	}
	amount, err := in.Amount.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("draw: " + err.Error())
	}
	if amount.Sign() <= 0 {
		return nil, zip.ErrBadRequest("draw: amount must be positive")
	}
	if in.Ref == "" {
		return nil, zip.ErrBadRequest("draw: ref is required (the idempotency key)")
	}
	subject := in.Subject
	if subject == "" {
		subject = org
	}
	fin, err := books("draw")
	if err != nil {
		return nil, err
	}
	d, ok := fin.(financeclient.Drawer)
	if !ok {
		return nil, fmt.Errorf("draw: this ledger cannot draw a payment")
	}
	entry, posted, derr := d.Draw(ctx, credit.UsageInput{
		Org: org, Subject: subject, Currency: amount.Currency().Code,
		Amount: credit.FromDecimal(amount.Decimal()),
		Model:  in.Memo, Service: "x402", Ref: in.Ref,
	})
	switch {
	case errors.Is(derr, financeclient.ErrShortWallet):
		return nil, zip.ErrPaymentRequired(fmt.Sprintf("draw %s/%s: %v", org, subject, derr))
	case errors.Is(derr, financeclient.ErrRefReused):
		return nil, zip.Errorf(http.StatusConflict, "draw: %v", derr)
	case derr != nil:
		return nil, zip.Errorf(500, "draw %s/%s: %v", org, subject, derr)
	}
	return &client.Drawn{Entry: entry, Posted: posted}, nil
}

// Undoes one deposit in the caller org's books by posting its mirror — the same
// legs with their signs turned — so the wallet returns to where it was before the
// deposit and the pair stays in the books as the record.
//
// The ORG comes from the CALLER, never the argument. A second reversal of the same
// deposit is refused (409) naming the first; an entry the books do not hold is 404;
// usage, a reversal, or a deposit partly spent is refused (400) — a reversal never
// takes a wallet below zero.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeReverse(ctx context.Context, in *client.ReverseIn) (*client.Reversed, error) {
	org, err := callerOrg(ctx, "reverse")
	if err != nil {
		return nil, err
	}
	fin, err := books("reverse")
	if err != nil {
		return nil, err
	}
	rev, ok := fin.(financeclient.Reverser)
	if !ok {
		return nil, fmt.Errorf("reverse: this ledger cannot reverse an entry")
	}
	r, rerr := rev.Reverse(ctx, financeclient.ReverseInput{Org: org, Entry: in.Entry, Reason: in.Reason})
	switch {
	case errors.Is(rerr, financeclient.ErrNoEntry):
		return nil, zip.Errorf(404, "%v", rerr)
	case errors.Is(rerr, financeclient.ErrReversed):
		return nil, zip.Errorf(409, "%v", rerr)
	case errors.Is(rerr, financeclient.ErrNotDeposit), errors.Is(rerr, financeclient.ErrShortWallet):
		return nil, zip.Errorf(400, "%v", rerr)
	case rerr != nil:
		return nil, zip.Errorf(500, "reverse %s/%s: %v", org, in.Entry, rerr)
	}
	return &client.Reversed{
		ID: r.ID, Of: r.Of, Wallet: r.Wallet,
		Amount: client.Money{Decimal: r.Amount.String(), Currency: "USD"},
	}, nil
}

// Puts money INTO one subject's prepaid ledger — the seller's half of a
// settlement, and the only op on this plane that creates a balance rather than
// moving, reading or gating one.
//
// REF IS REQUIRED and is the idempotency key. Without it this op is a money
// printer: a settlement retries by construction, and a retry would credit twice.
// It is required rather than defaulted, because a default nobody chose is a key
// that collides. The amount must PARSE and must be positive — a credit is not a
// debit spelled with a sign.
//
// The ORG comes from the CALLER, never the argument: a caller able to name the
// credited org could pay itself out of someone else's books. An empty subject
// credits the org's own account. The amount is deposited as the EXACT decimal
// rather than its minor unit, because a settlement is routinely a fraction of a
// cent and rounding to the minor unit would round it to nothing.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCredit(ctx context.Context, in *client.CreditIn) (*client.Credited, error) {
	org, err := callerOrg(ctx, "credit")
	if err != nil {
		return nil, err
	}
	amount, err := in.Amount.Parse()
	if err != nil {
		return nil, zip.ErrBadRequest("credit: " + err.Error())
	}
	if amount.Sign() <= 0 {
		return nil, zip.ErrBadRequest("credit: amount must be positive")
	}
	if in.Ref == "" {
		return nil, zip.ErrBadRequest("credit: ref is required (the idempotency key)")
	}
	subject := in.Subject
	if subject == "" {
		subject = org
	}
	// Cash opens paid models: only a SuperAdmin recording a payment may name it.
	if in.Cash && !cloud.Who(ctx).Admin {
		return nil, zip.ErrForbidden("credit: cash is recorded by a SuperAdmin")
	}
	fin, err := books("credit")
	if err != nil {
		return nil, err
	}
	entryID, derr := fin.Deposit(ctx, credit.DepositInput{
		Org: org, Subject: subject, Currency: amount.Currency().Code,
		Amount: credit.FromDecimal(amount.Decimal()),
		Ref:    in.Ref, Notes: in.Notes, Tags: in.Tags, Cash: in.Cash,
	})
	if derr != nil {
		return nil, zip.Errorf(500, "credit %s/%s: %v", org, subject, derr)
	}
	// The entry id travels. Deposit has always returned it and this op used to
	// discard it, which left every cross-process credit unciteable: the caller
	// knew money had moved and could name nothing in the books that moved it.
	return &client.Credited{Amount: in.Amount, ID: entryID}, nil
}
