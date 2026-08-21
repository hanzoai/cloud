// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// sale_rpc.go — the three card doors and the sweep behind them, over the
// internal plane: top up a wallet with a fresh token or a saved card, buy a plan,
// and recharge every org that has fallen below its own threshold.
//
// ALL OF THESE MOVE MONEY, so all of them share two properties and neither is
// negotiable. The SUBJECT is resolved at the door from the caller's own
// credential and arrives as a value; nothing here re-derives an identity,
// because a store that chose a subject would be crediting an account nobody
// proved. And no caller names a PRICE: a plan is charged at its catalog price
// for the level asked for, so underpaying is not a check that can be forgotten,
// it is a request that cannot be expressed.
//
// The retry key is the other half of the money contract. It is derived from
// stable facts when the caller supplies none, and it reaches the processor as
// well as the local guard — so the charge is exactly-once at the gateway even
// when our own guard store is unavailable.

import (
	"context"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposeSale publishes the card doors and the recharge sweep. Mount calls it.
//
// THE THREE CARD DOORS ARE SCREENED, through the same [screened] the agent's
// payment op goes through. That is the whole reason it is a generic: four copies
// of the composition is precisely the state that made the first screen a bound
// on one entrance with others beside it, and these three ARE the others. Each
// states what its input is worth off the DECODED input and what its answer
// settled off the RETURNED receipt, because over the agent plane a body reader
// and a response reader both see an envelope rather than a payment.
//
// The sweep is not screened and is not a hole: it is reached by a schedule with
// no request behind it, charges cards the customer already saved under their own
// standing instruction, and is refused at the door to anything but platform
// authority.
func exposeSale(s screen) {
	zip.Post[plane.CardIn, plane.Charged](cloud.Plane(), "/billing/topup/card",
		screened(s, "/billing/topup/card",
			func(in *plane.CardIn) (int64, string) { return in.AmountCents, in.Currency },
			func(out *plane.Charged) (string, string) {
				return firstRefOf(out.ProcessorRef, out.TransactionID), out.TransactionID
			}, planeTopupCard),
		zip.WithOperationID(plane.BillingTopupCard),
		zip.WithSummary("Charge a single-use card token and credit the wallet"))
	zip.Post[plane.SavedCardIn, plane.Charged](cloud.Plane(), "/billing/topup",
		screened(s, "/billing/topup",
			func(in *plane.SavedCardIn) (int64, string) { return in.AmountCents, in.Currency },
			func(out *plane.Charged) (string, string) {
				return firstRefOf(out.ProcessorRef, out.TransactionID), out.TransactionID
			}, planeTopup),
		zip.WithOperationID(plane.BillingTopup),
		zip.WithSummary("Charge a saved card and credit the wallet"))
	zip.Post[plane.SaleIn, plane.Sold](cloud.Plane(), "/billing/subscribe",
		screened(s, "/billing/subscribe",
			// A sale carries NO amount — the price is the catalog's at the level
			// asked for — so the worth a sale states is what the card was actually
			// charged, which only the receipt knows. Zero on the way in is the
			// honest reading: the axis is blind here, and blind is not zero-risk,
			// which is why it is stated rather than inferred.
			func(in *plane.SaleIn) (int64, string) { return 0, in.Currency },
			func(out *plane.Sold) (string, string) {
				if out.Sale == nil {
					// A REPLAY settled nothing new: the money moved on the first
					// attempt and was recorded then. Reporting a settlement here
					// would teach the model one sale twice.
					return "", ""
				}
				return out.Sale.SubscriptionID, out.Sale.SubscriptionID
			}, planeSubscribe),
		zip.WithOperationID(plane.BillingSubscribe),
		zip.WithSummary("Buy a plan with a card"))
	zip.Post[struct{}, plane.Recharge](cloud.Plane(), "/billing/recharge", planeRecharge,
		zip.WithOperationID(plane.BillingRecharge),
		zip.WithSummary("Recharge every org that has fallen below its threshold"))
}

// Charges a single-use card token and credits the subject's wallet.
//
// This is the cold-customer path: nothing is saved first, so a caller with no
// card on file can add funds. The nonce is spent by the charge and is worthless
// to anyone who reads it afterwards.
//
// The bounds are enforced HERE and only here. A scripted or agent-driven request
// never passes through a console's client-side cap, so the floor and the ceiling
// have to bind where the money moves.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTopupCard(ctx context.Context, in *plane.CardIn) (*plane.Charged, error) {
	org, err := payingOrg(ctx, "topup")
	if err != nil {
		return nil, err
	}
	out, f := commercebilling.TakePayment(ctx, org, commercebilling.TakePaymentIn{
		SourceID:       in.SourceID,
		AmountCents:    in.AmountCents,
		Currency:       in.Currency,
		Subject:        in.Subject,
		IdempotencyKey: in.IdempotencyKey,
		KMS:            kmsFrom(ctx),
	})
	if f != nil {
		// The fault already carries the status the money contract asks for —
		// a decline is not a broken gateway — so it travels rather than being
		// re-derived from the message.
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return &plane.Charged{
		TransactionID: out.TransactionID,
		BalanceCents:  out.BalanceCents,
		Status:        out.Status,
		ProcessorRef:  out.ProcessorRef,
		Test:          out.Test,
	}, nil
}

// Charges a card the subject already saved and credits their wallet.
//
// The method must belong to the subject; one that does not is a MISS rather than
// a refusal, so a guessed id cannot confirm that somebody else's card exists.
// The description rides to the processor, where the customer reads it, which is
// why the caller says it rather than this side inventing a sentence.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTopup(ctx context.Context, in *plane.SavedCardIn) (*plane.Charged, error) {
	org, err := payingOrg(ctx, "topup")
	if err != nil {
		return nil, err
	}
	out, terr := commercebilling.TopupCard(ctx, org, commercebilling.TopupCardIn{
		MethodID:       in.MethodID,
		AmountCents:    in.AmountCents,
		Currency:       in.Currency,
		Subject:        in.Subject,
		IdempotencyKey: in.IdempotencyKey,
		Description:    in.Description,
		KMS:            kmsFrom(ctx),
		Events:         eventsFrom(ctx),
	})
	if terr != nil {
		return nil, chargeFault(terr)
	}
	return &plane.Charged{
		TransactionID: out.TransactionID,
		BalanceCents:  out.BalanceCents,
		Status:        out.Status,
	}, nil
}

// Buys a plan with a card — a fresh single-use token, which is vaulted first, or
// a card the subject already saved.
//
// The settled charge IS the mint authority, which is why this opens a paid-tier
// subscription without the gate that stops one being minted for free: money that
// arrived is the thing that gate was asking for.
//
// It answers TWO SHAPES and keeps them apart. A fresh sale is a receipt; an
// identical retry is the sealed body of the first one, verbatim. The door owes a
// different status for each, and a retry answered as fresh would read as a
// second subscription having been opened.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscribe(ctx context.Context, in *plane.SaleIn) (*plane.Sold, error) {
	org, err := payingOrg(ctx, "subscribe")
	if err != nil {
		return nil, err
	}
	sold, serr := commercebilling.SubscribeCard(ctx, org, commercebilling.SubscribeIn{
		SourceID:       in.SourceID,
		MethodID:       in.MethodID,
		PlanID:         in.PlanID,
		Subject:        in.Subject,
		StoreID:        in.StoreID,
		Quantity:       in.Quantity,
		Level:          in.Level,
		Currency:       in.Currency,
		Email:          in.Email,
		IdempotencyKey: in.IdempotencyKey,
		Promo:          promoNow(ctx),
		KMS:            kmsFrom(ctx),
		Events:         eventsFrom(ctx),
	})
	if serr != nil {
		return nil, saleFault(serr)
	}
	out := &plane.Sold{Replayed: sold.Replayed}
	if sold.Sale != nil {
		out.Sale = &plane.Sale{
			SubscriptionID:  sold.Sale.SubscriptionID,
			InvoiceID:       sold.Sale.InvoiceID,
			PlanID:          sold.Sale.PlanID,
			Level:           sold.Sale.Level,
			PaymentMethodID: sold.Sale.PaymentMethodID,
			AmountCents:     sold.Sale.AmountCents,
			Currency:        sold.Sale.Currency,
			Status:          sold.Sale.Status,
		}
	}
	return out, nil
}

// Sweeps every org and charges the default card of those whose balance has
// fallen below their own threshold.
//
// Its caller is a SCHEDULE, not a person — there is no request behind an
// off-session charge — which is also why it takes no retry key: one run-all
// request's header would be one key for every org it touches, so the second
// charge would replay the first one's receipt. Each org derives its own.
//
// Orgs is the POPULATION considered, not the row count. That difference is how a
// reader tells "nobody was below threshold" from "the sweep never ran".
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRecharge(ctx context.Context, _ *struct{}) (*plane.Recharge, error) {
	run, err := commercebilling.RunAutoRecharge(ctx, kmsFrom(ctx), eventsFrom(ctx))
	if err != nil {
		return nil, zip.Errorf(500, "failed to list organizations")
	}
	out := &plane.Recharge{Orgs: run.Orgs, Charged: run.Charged}
	out.Results = make([]plane.Recharged, 0, len(run.Results))
	for _, r := range run.Results {
		out.Results = append(out.Results, plane.Recharged{
			OrgName: r.OrgName, UserID: r.UserId, Charged: r.Charged,
			AmountCents: r.AmountCents, BalanceCents: r.BalanceCents,
			TransactionID: r.TransactionId, Error: r.Error,
		})
	}
	return out, nil
}

// firstRefOf answers the first non-empty reference. A processor reference proves
// money moved at the GATEWAY and is preferred; the ledger id is the fallback, so
// a settlement is never reported with nothing naming it.
func firstRefOf(refs ...string) string {
	for _, r := range refs {
		if r != "" {
			return r
		}
	}
	return ""
}

// chargeFault maps a saved-card charge's refusals onto the statuses this door has
// always answered with.
//
// Each is a different thing for the payer to DO — fix the request, name a card
// that exists, add the card again, wait, use another card, call support — which
// is why they are separate answers and not one "it failed". The default is 402:
// what is left after the named classes is the processor declining.
func chargeFault(err error) error {
	switch {
	case commercebilling.IsMethodRefused(err):
		return zip.Errorf(400, "%v", err)
	case commercebilling.IsMethodNotFound(err):
		return zip.Errorf(404, "payment method not found")
	case commercebilling.IsMethodUnchargeable(err):
		return zip.Errorf(422, "saved payment method has no chargeable card — add the card again")
	case commercebilling.IsTopupOutOfBounds(err):
		return zip.Errorf(400, "%v", err)
	case commercebilling.IsTopupGuardUnavailable(err):
		return zip.Errorf(503, "billing is temporarily unavailable; please retry")
	case commercebilling.IsTopupInFlight(err):
		return zip.Errorf(409, "top-up already in progress")
	case commercebilling.IsTopupNoProcessor(err):
		return zip.Errorf(422, "no payment processor available")
	case commercebilling.IsTopupUncredited(err):
		return zip.Errorf(500, "charge succeeded but balance credit failed; contact support")
	}
	return zip.Errorf(402, "%v", err)
}

// saleFault maps a sale's refusals onto the statuses this door has always
// answered with, in the order the door tested them: the sale's own classes
// first, then the two a saved card contributes.
func saleFault(err error) error {
	switch {
	case commercebilling.IsSaleRefused(err):
		return zip.Errorf(400, "%v", err)
	case commercebilling.IsSaleNotFound(err):
		return zip.Errorf(404, "%v", err)
	case commercebilling.IsSaleConflict(err):
		return zip.Errorf(409, "%v", err)
	case commercebilling.IsSaleUnchargeable(err):
		return zip.Errorf(422, "%v", err)
	case commercebilling.IsSaleDeclined(err):
		return zip.Errorf(402, "%v", err)
	case commercebilling.IsMethodNotFound(err):
		return zip.Errorf(404, "payment method not found")
	case commercebilling.IsMethodUnchargeable(err):
		return zip.Errorf(422, "saved payment method has no chargeable card — add the card again")
	}
	// What is left is the subscription create itself failing after the money
	// settled: a seat refusal is the caller's, anything else is ours.
	if commercebilling.IsSubscriptionRefused(err) {
		return zip.Errorf(400, "%v", err)
	}
	return zip.Errorf(500, "failed to create subscription")
}
