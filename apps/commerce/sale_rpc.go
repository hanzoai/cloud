// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// sale_rpc.go — the three card endpoints and the sweep behind them, over the
// internal plane: top up a wallet with a fresh token or a saved card, buy a plan,
// and recharge every org that has fallen below its own threshold.
//
// ALL OF THESE MOVE MONEY, so all of them share two properties and neither is
// negotiable. The SUBJECT is resolved at the endpoint from the caller's own
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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"time"

	"github.com/hanzoai/authz"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercedatastore "github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/hanzoai/commerce/models/paymentmethod"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/models/subscription"
	"github.com/hanzoai/commerce/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/sdk"
)

// exposeSale publishes the card endpoints and the recharge sweep. Mount calls it.
//
// THE THREE CARD ENDPOINTS ARE SCREENED, through the same [screened] the agent's
// payment op goes through. That is the whole reason it is a generic: four copies
// of the composition is precisely the state that made the first screen a bound
// on one entrance with others beside it, and these three ARE the others. Each
// states what its input is worth and which wallet the billing endpoint resolved
// off the DECODED input, and what its answer settled off the RETURNED receipt,
// because over the agent plane a body reader and a response reader both see an
// envelope rather than a payment.
//
// The sweep is not screened and is not a hole: it is reached by a schedule with
// no request behind it, charges cards the customer already saved under their own
// standing instruction, and is refused at the endpoint to anything but platform
// authority.
//
// A plan sale is put on the audit trail by the billing endpoint that took it, which
// sees the refusals made before commerce is asked as well as commerce's answer.
func exposeSale(s screen) {
	sweeps = s
	cloud.Plane().Post("/billing/topup/card",
		screened(s, "/billing/topup/card", true,
			func(_ context.Context, in *client.CardIn) (int64, string) { return in.AmountCents, in.Currency },
			func(in *client.CardIn) string { return in.Subject },
			func(_ *client.CardIn, out *client.Charged) cleared {
				return cleared{ref: firstRefOf(out.ProcessorRef, out.TransactionID), id: out.TransactionID}
			}, func(out *client.Charged, cents int64) { out.BalanceCents = cents }, planeTopupCard),
		zip.WithOperationID(client.BillingTopupCard),
		zip.WithSummary("Charge a single-use card token and credit the wallet"))
	cloud.Plane().Post("/billing/topup",
		screened(s, "/billing/topup", true,
			func(_ context.Context, in *client.SavedCardIn) (int64, string) { return in.AmountCents, in.Currency },
			func(in *client.SavedCardIn) string { return in.Subject },
			func(_ *client.SavedCardIn, out *client.Charged) cleared {
				return cleared{ref: firstRefOf(out.ProcessorRef, out.TransactionID), id: out.TransactionID}
			}, func(out *client.Charged, cents int64) { out.BalanceCents = cents }, planeTopup),
		zip.WithOperationID(client.BillingTopup),
		zip.WithSummary("Charge a saved card and credit the wallet"))
	cloud.Plane().Post("/billing/subscribe",
		screened(s, "/billing/subscribe", false, saleWorth,
			func(in *client.SaleIn) string { return in.Subject },
			saleCleared, nil, planeSubscribe),
		zip.WithOperationID(client.BillingSubscribe),
		zip.WithSummary("Buy a plan with a card"))
	cloud.Plane().Post("/billing/paypal/order", planePayPalOrder,
		zip.WithOperationID(client.BillingPayPalOrder),
		zip.WithSummary("Open a server-priced PayPal order for a top-up or plan purchase"))
	cloud.Plane().Post("/billing/auto-recharge", planeAutoRecharge,
		zip.WithOperationID(client.BillingAutoRecharge),
		zip.WithSummary("A payer's auto-reload rule"))
	cloud.Plane().Post("/billing/auto-recharge/set", planeAutoRechargeSet,
		zip.WithOperationID(client.BillingAutoRechargeSet),
		zip.WithSummary("Set a payer's auto-reload rule"))
	cloud.Plane().Post("/billing/recharge", planeRecharge,
		zip.WithOperationID(client.BillingRecharge),
		zip.WithSummary("Recharge every org that has fallen below its threshold"))
	cloud.Plane().Post("/billing/renew", planeRenew,
		zip.WithOperationID(client.BillingRenew),
		zip.WithSummary("Renew every subscription that has fallen due"))
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
func planeTopupCard(ctx context.Context, in *client.CardIn) (*client.Charged, error) {
	org, err := orgOf(ctx, "topup")
	if err != nil {
		return nil, err
	}
	out, f := commercebilling.TakePayment(ctx, org, commercebilling.TakePaymentIn{
		SourceID:    in.SourceID,
		AmountCents: in.AmountCents,
		Currency:    in.Currency,
		Subject:     in.Subject,
		// The ledger the credit is posted to: the payer's, which only this host can
		// resolve from the caller's identity. Commerce posts it, once, itself.
		Ledger:         payerLedger(ctx, "/billing/topup/card", in.AmountCents, in.Currency, in.Subject),
		IdempotencyKey: in.IdempotencyKey,
		Provider:       in.Provider,
		Payer:          payerIn(in.Name, in.Email, in.Phone, in.Billing),
	})
	if f != nil {
		// The fault already carries the status the money contract asks for —
		// a decline is not a broken gateway — so it travels rather than being
		// re-derived from the message.
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return &client.Charged{
		TransactionID: out.TransactionID,
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
func planeTopup(ctx context.Context, in *client.SavedCardIn) (*client.Charged, error) {
	org, err := orgOf(ctx, "topup")
	if err != nil {
		return nil, err
	}
	out, terr := commercebilling.TopupCard(ctx, org, commercebilling.TopupCardIn{
		MethodID:       in.MethodID,
		AmountCents:    in.AmountCents,
		Currency:       in.Currency,
		Subject:        in.Subject,
		Ledger:         payerLedger(ctx, "/billing/topup", in.AmountCents, in.Currency, in.Subject),
		IdempotencyKey: in.IdempotencyKey,
		Description:    in.Description,
		Events:         eventsFrom(ctx),
	})
	if terr != nil {
		return nil, chargeFault(terr)
	}
	return &client.Charged{
		TransactionID: out.TransactionID,
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
// identical retry is the sealed body of the first one, verbatim. The endpoint owes
// a different status for each, and a retry answered as fresh would read as a
// second subscription having been opened.
//
// An organization's order is sold only to a buyer who chose the organization
// and administers it now (orderable); one who did not is refused before anything
// is charged.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscribe(ctx context.Context, in *client.SaleIn) (*client.Sold, error) {
	// A plan is bought where the catalog is on sale, and answered like a slug that
	// never existed anywhere else — before anything is resolved or charged.
	if !commercebilling.Sells(in.Host) {
		return nil, zip.ErrNotFound("plan not found")
	}
	org, err := orgOf(ctx, "subscribe")
	if err != nil {
		return nil, err
	}
	if err := orderable(ctx, in); err != nil {
		return nil, err
	}
	sold, serr := commercebilling.SubscribeCard(ctx, org, commercebilling.SubscribeIn{
		SourceID:       in.SourceID,
		MethodID:       in.MethodID,
		PlanID:         in.PlanID,
		Subject:        in.Subject,
		StoreID:        in.StoreID,
		Quantity:       in.Quantity,
		Lines:          saleLines(in.Lines),
		Level:          in.Level,
		Interval:       types.Interval(in.Interval),
		Currency:       in.Currency,
		Email:          in.Email,
		Name:           in.Name,
		Phone:          in.Phone,
		BillingAddress: billingAddress(in.Billing),
		IdempotencyKey: in.IdempotencyKey,
		Promo:          promoNow(ctx),
		Coupon:         in.Coupon,
		Wallet:         in.Wallet,
		Provider:       in.Provider,
		Events:         eventsFrom(ctx),
		BalanceCents:   in.BalanceCents,
		DeclineReload:  in.DeclineReload,
		Country:        in.Country,
		QuotedAmount:   in.QuotedAmount,
		QuotedCurrency: in.QuotedCurrency,
	})
	if serr != nil {
		return nil, saleFault(serr)
	}
	if len(sold.Replayed) > 0 {
		// The replay is the first answer, byte for byte: the sealed receipt rendered
		// as a fresh one is, so a retry cannot tell the two apart by its bytes.
		var prev commercebilling.Sale
		if err := json.Unmarshal(sold.Replayed, &prev); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "subscribe: the completed sale could not be read")
		}
		body, err := json.Marshal(saleView(&prev, org.TestMode()))
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "subscribe: the completed sale could not be read")
		}
		return &client.Sold{Replayed: body}, nil
	}
	told(org.Name, funds.Plan)
	mirrorOrgSubscriptions(org)
	mirrorOrgInvoices(org)
	mirrorOrgBook(org.Name)
	return &client.Sold{Sale: saleView(sold.Sale, org.TestMode())}, nil
}

// Opens a server-priced PayPal REST v2 order for either a bounded credit top-up
// (intent CAPTURE) or a plan subscription's first period (intent AUTHORIZE).
//
// No ledger entry or subscription row is written here; the order is settled only
// after the buyer approves it in PayPal and POST /v1/billing/topup/token or
// POST /v1/billing/subscribe/card captures it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planePayPalOrder(ctx context.Context, in *client.PayPalOrderIn) (*client.PayPalOrderOut, error) {
	if strings.TrimSpace(in.PlanID) != "" && !commercebilling.Sells(in.Host) {
		return nil, zip.ErrNotFound("plan not found")
	}
	org, err := orgOf(ctx, "paypal order")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.PlanID) != "" {
		if err := orderable(ctx, &client.SaleIn{PlanID: in.PlanID, Subject: in.Subject, Chosen: in.Chosen}); err != nil {
			return nil, err
		}
	}
	out, f := commercebilling.CreatePayPalOrder(ctx, org, commercebilling.PayPalOrderIn{
		AmountCents:    in.AmountCents,
		PlanID:         in.PlanID,
		StoreID:        in.StoreID,
		Quantity:       in.Quantity,
		Lines:          saleLines(in.Lines),
		Level:          in.Level,
		Interval:       types.Interval(in.Interval),
		Currency:       in.Currency,
		Coupon:         in.Coupon,
		BalanceCents:   in.BalanceCents,
		Subject:        in.Subject,
		IdempotencyKey: in.IdempotencyKey,
		Promo:          promoNow(ctx),
	})
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return &client.PayPalOrderOut{
		OrderID:     out.OrderID,
		AmountCents: out.AmountCents,
		Currency:    out.Currency,
		Intent:      out.Intent,
		Status:      out.Status,
		Environment: out.Environment,
		Test:        out.Test,
	}, nil
}

// orderable refuses an organization's order (a plan row whose limits state how one is
// ordered) unless the buyer chose the organization and administers it now. A request
// that selects none acts in the buyer's default org, so the plan, its seats and its
// balance would be bought for that org (400). A token says what was true when it was
// minted, so IAM is asked whether the buyer is an admin of the org today (403 when
// not, 503 when IAM cannot say). A row that cannot be read is the sale's to answer,
// as it prices from it.
func orderable(ctx context.Context, in *client.SaleIn) error {
	p := commerceplan.New(commerceplan.AuthorityDB(ctx))
	if p.GetById(strings.TrimSpace(in.PlanID)) != nil || !p.Limits.Ordered() {
		return nil
	}
	return admits(ctx, in.Chosen)
}

// admits refuses an organization's order unless the buyer chose the organization
// (chosen) and IAM says they administer it now: orderable's rule, and the whole
// rule for an order by invoice, which is an organization's whatever its row.
func admits(ctx context.Context, chosen bool) error {
	if !chosen {
		return &zip.HTTPError{Status: http.StatusBadRequest, Code: "org_required", Msg: "choose the organization to buy for: select it with X-Org-Id; nothing was charged"}
	}
	org := cloud.Who(ctx).Org
	admin, err := administers(ctx)
	switch {
	case err != nil:
		luxlog.Default().Warn("an organization's order could not confirm its buyer administers it", "org", org, "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "whether you administer %s could not be confirmed: try again; nothing was charged", org)
	case !admin:
		return zip.ErrForbidden(fmt.Sprintf("only an admin of %s buys its plan: ask an admin to buy it; nothing was charged", org))
	}
	return nil
}

// administers asks IAM whether the caller administers the org they act in, from its
// records rather than the caller's token. A variable so a test stands in for IAM;
// production never reassigns it.
var administers = func(ctx context.Context) (bool, error) {
	got, err := iampeer.IAMRoles(ctx)
	if err != nil {
		return false, err
	}
	return got != nil && got.Admin, nil
}

// saleView is a sale's receipt as this endpoint answers it, fresh or replayed.
func saleView(s *commercebilling.Sale, test bool) *client.Sale {
	out := &client.Sale{
		SubscriptionID:  s.SubscriptionID,
		InvoiceID:       s.InvoiceID,
		PlanID:          s.PlanID,
		Level:           s.Level,
		Interval:        string(s.Interval),
		PaymentMethodID: s.PaymentMethodID,
		AmountCents:     s.AmountCents,
		Currency:        s.Currency,
		Status:          s.Status,
		Test:            test,
		Coupon:          s.Coupon,
		CouponPercent:   s.CouponPercent,
		CouponMonths:    s.CouponMonths,
		// The subscription a sale opened is the one sale it is: a retry or a
		// webhook that reaches the same subscription states the same id.
		EventID:      s.SubscriptionID,
		BalanceCents: s.BalanceCents,
		BalanceID:    s.BalanceID,
		Seats:        s.Seats,
		Category:     s.Category,
		Replaced:     s.Replaced,
		StartsCents:  s.StartsCents,
		Follows:      s.Follows,
	}
	if s.StartsAt != nil {
		out.StartsAt = stamp(*s.StartsAt)
	}
	if r := s.Reload; r != nil {
		out.Reload = &client.SaleReload{State: r.State}
	}
	if s.Quote != nil {
		if q, err := into[client.PriceQuote](s.Quote); err == nil {
			out.Quote = q
		}
	}
	return out
}

// saleCleared is what a sale's answer says cleared, for the screen. A REPLAY
// settled nothing new: the money moved on the first attempt and was recorded then,
// and reporting a settlement here would teach the model one sale twice.
func saleCleared(in *client.SaleIn, out *client.Sold) cleared {
	if out.Sale == nil {
		return cleared{}
	}
	return cleared{
		ref: out.Sale.EventID, id: out.Sale.SubscriptionID,
		cents: out.Sale.AmountCents, currency: out.Sale.Currency,
		test: out.Sale.Test, moded: true,
		plan: out.Sale.PlanID, interval: out.Sale.Interval,
		coupon: out.Sale.Coupon, consent: in.Consent,
		goods: goodsOf(in, out.Sale),
	}
}

// saleWorth is what a sale charges, stated to the screen before the card is: the
// plan's catalog price at the level and period asked, times its seats, each other
// seat type at its own, and the starting balance bought in the same payment. It is
// the list charge, before a promotion or a coupon takes from it, so the screen
// weighs at most what the card can be charged and never less. A row this process
// cannot read leaves the balance alone stated; the sale prices from that row.
func saleWorth(ctx context.Context, in *client.SaleIn) (int64, string) {
	cents := max(in.BalanceCents, 0)
	db := commerceplan.AuthorityDB(ctx)
	p := commerceplan.New(db)
	if p.GetById(strings.TrimSpace(in.PlanID)) != nil {
		return cents, in.Currency
	}
	seats := int64(1)
	if p.PerSeat {
		seats = int64(max(in.Quantity, 1))
	}
	cents += listCents(p, in.Level, in.Interval) * seats
	for _, l := range in.Lines {
		lp := commerceplan.New(db)
		if lp.GetById(strings.TrimSpace(l.PlanID)) == nil {
			cents += listCents(lp, 0, in.Interval) * int64(max(l.Quantity, 1))
		}
	}
	return cents, cmp.Or(string(p.Currency), in.Currency)
}

// listCents is one seat of a plan row at a level and period, as a sale prices it: a
// year is the year's total, a month the level's price, and 0 is a price the row
// does not publish, which the sale refuses.
func listCents(p *commerceplan.Plan, level int, interval string) int64 {
	if types.Interval(interval) == types.Yearly {
		return int64(p.YearlyTotal())
	}
	price, err := p.LevelPrice(level)
	if err != nil {
		return 0
	}
	return int64(price)
}

// goodsOf is what a sale bought beside its money: the plan's ladder and the seats
// off the receipt, how it was paid off the request — a wallet token or a card —
// and whether it replaced a paid plan the account held.
func goodsOf(in *client.SaleIn, s *client.Sale) goods {
	out := goods{tab: s.Category, seats: s.Seats, balance: s.BalanceCents, method: "card", setup: "new"}
	if in.Wallet {
		out.method = "wallet"
	}
	if s.Replaced != "" {
		out.setup = "upgrade"
	}
	return out
}

// saleLines are a team's other seat types as commerce's sale reads them.
func saleLines(in []client.SaleLine) []commercebilling.LineIn {
	var out []commercebilling.LineIn
	for _, l := range in {
		out = append(out, commercebilling.LineIn{PlanID: l.PlanID, Quantity: l.Quantity})
	}
	return out
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
func planeRecharge(ctx context.Context, _ *cloud.Unit) (*client.Recharge, error) {
	run, err := commercebilling.RunAutoRecharge(ctx, eventsFrom(ctx))
	if err != nil {
		return nil, zip.Errorf(500, "failed to list organizations")
	}
	reloaded(run.Results)
	out := &client.Recharge{Orgs: run.Orgs, Charged: run.Charged}
	out.Results = make([]client.Recharged, 0, len(run.Results))
	for _, r := range run.Results {
		out.Results = append(out.Results, client.Recharged{
			OrgName: r.OrgName, UserID: r.UserId, Charged: r.Charged,
			AmountCents: r.AmountCents, BalanceCents: r.BalanceCents,
			TransactionID: r.TransactionId, Error: r.Error,
		})
	}
	return out, nil
}

// Renews every subscription whose period has ended, in every org.
//
// Each due period is invoiced and charged to the card on file — never to prepaid
// money, which pays for usage. A paid period moves the subscription on; an unpaid
// one leaves it past due, which confers no tier. An invoice already raised for a
// period is never charged again. A plan collected outside commerce whose period
// ran out with no later payment recorded ends (lapsed). No automated emails are
// sent before a bill is due.
//
// It is the schedule's to call: the cron app asks it by name as the platform, and
// any other caller is refused. One subscription failing is that subscription's
// row, never the sweep's; a row whose period ended more than one period ago is
// named and left alone rather than charged for every period since.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRenew(ctx context.Context, _ *cloud.Unit) (*client.Renewal, error) {
	if cloud.Who(ctx).Org != authz.AdminOrg {
		return nil, zip.ErrForbidden("renewal sweep: the platform runs it")
	}
	armSavedCards(ctx)
	run, err := commercebilling.RenewDue(ctx)
	if err != nil {
		return nil, zip.Errorf(500, "failed to list organizations")
	}
	renewed(run.Results)
	// The same sweep voids every order invoice unpaid past its due date and grace.
	expired(ctx)
	out := &client.Renewal{Orgs: run.Orgs, Due: run.Due, Renewed: run.Renewed, Lapsed: run.Lapsed}
	out.Results = make([]client.Renewed, 0, len(run.Results))
	for _, r := range run.Results {
		out.Results = append(out.Results, client.Renewed{
			OrgName: r.OrgName, UserID: r.UserId, SubscriptionID: r.SubscriptionId,
			InvoiceID: r.InvoiceId, Success: r.Success, Error: r.Error, Lapsed: r.Lapsed,
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

// chargeFault maps a saved-card charge's refusals onto the statuses this endpoint
// has always answered with.
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

// saleFault maps a sale's refusals onto the statuses this endpoint has always
// answered with, in the order the endpoint tested them: the sale's own classes
// first, then the two a saved card contributes. A refusal that names its code
// (commerce billing.Refusal) keeps it, so a page acts on the code and not the
// sentence; the plane carries the code, and the edge renders it unchanged.
func saleFault(err error) error {
	he := faultOf(err)
	if r, ok := errors.AsType[*commercebilling.Refusal](err); ok {
		he.Code = r.Code
	}
	return he
}

// faultOf is the status and sentence a sale's error answers.
func faultOf(err error) *zip.HTTPError {
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
	case commercebilling.IsSaleUnavailable(err):
		return zip.Errorf(503, "%v", err)
	case commercebilling.IsSaleUnsettled(err):
		return zip.Errorf(502, "%v", err)
	case commercebilling.IsMethodNotFound(err):
		return zip.Errorf(404, "payment method not found")
	case commercebilling.IsMethodUnchargeable(err):
		return zip.Errorf(422, "saved payment method has no chargeable card — add the card again")
	case commercebilling.IsTopupUncredited(err):
		return zip.Errorf(500, "the payment settled and its starting balance was not credited; contact support")
	}
	// What is left is the subscription create itself failing after the money
	// settled: a seat refusal is the caller's, anything else is ours.
	if commercebilling.IsSubscriptionRefused(err) {
		return zip.Errorf(400, "%v", err)
	}
	return zip.Errorf(500, "failed to create subscription")
}

// Reads one payer's auto-reload rule — the org's own account's, or a person's wallet
// in it — the threshold the sweep measures that payer's balance against, the amount
// it charges that payer's card when the balance falls below it, and the payer's
// daily bound, which its plan sets (0 is none).
//
// An org that never set one reads as DISABLED with zeroes rather than as an error:
// "no rule" is a true answer to "what is your rule", and `stored` carries the
// difference between never having set one and having turned one off.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeAutoRecharge(ctx context.Context, in *client.AutoRechargeIn) (*client.AutoRecharge, error) {
	org, err := orgOf(ctx, "auto-recharge")
	if err != nil {
		return nil, err
	}
	cfg, rerr := commercebilling.ReadAutoRecharge(ctx, org, in.Subject)
	if rerr != nil {
		return nil, zip.Errorf(502, "auto-recharge: %v", rerr)
	}
	return autoRechargeView(cfg), nil
}

// Sets one payer's auto-reload rule: the org's own account's, or a person's wallet in it.
//
// ENABLING REQUIRES A CARD ON FILE, because the sweep charges off-session and a
// rule that names no chargeable method is a promise the schedule cannot keep. The
// refusals a caller can provoke — an amount outside a top-up's own range, a
// threshold outside it, no default payment method — are the module's own
// (IsAutoRechargeRefused), mapped here to 400 so a caller learns which of its own
// values was wrong rather than reading a 500 and guessing.
//
// The rule is the payer's own: the org comes from the validated caller, and the
// subject, which the billing app resolves from the validated principal, must be the
// org's own account or a wallet in it, so a write cannot be steered onto another
// tenant's schedule.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeAutoRechargeSet(ctx context.Context, in *client.AutoRechargeSetIn) (*client.AutoRecharge, error) {
	org, err := orgOf(ctx, "set auto-recharge")
	if err != nil {
		return nil, err
	}
	cfg, werr := commercebilling.WriteAutoRecharge(ctx, org, in.Subject, commercebilling.AutoRechargeEdit{
		Enabled:        in.Edit.Enabled,
		ThresholdCents: in.Edit.ThresholdCents,
		AmountCents:    in.Edit.AmountCents,
		Currency:       in.Edit.Currency,
	})
	switch {
	case commercebilling.IsAutoRechargeRefused(werr):
		return nil, zip.Errorf(400, "%s", werr.Error())
	case werr != nil:
		return nil, zip.Errorf(502, "set auto-recharge: %v", werr)
	}
	return autoRechargeView(cfg), nil
}

// autoRechargeView is the ONE mapping from the module's record to the wire, so the
// read and the write cannot describe one rule two ways.
func autoRechargeView(cfg commercebilling.AutoRecharge) *client.AutoRecharge {
	return &client.AutoRecharge{
		Subject:         cfg.Subject,
		Enabled:         cfg.Enabled,
		ThresholdCents:  cfg.ThresholdCents,
		AmountCents:     cfg.AmountCents,
		Currency:        cfg.Currency,
		LastRechargedAt: cfg.LastRechargedAt,
		Stored:          cfg.Stored,
		PerDay:          cfg.PerDay,
	}
}

// payerLedger is the org whose books the caller's wallet is in, resolved the way the
// screen resolves it before the card is charged, so the credit commerce posts and the
// spend the gate draws name one wallet. It is "" when the caller resolves none, which
// the screen has already refused before a card was charged.
func payerLedger(ctx context.Context, path string, cents int64, currency, subject string) string {
	return observed(ctx, path, cents, currency, subject).ledger
}

// payerIn is who is paying, as commerce takes them: the name and email are the
// caller's own identity, the phone and address what the buyer typed.
func payerIn(name, email, phone string, a *client.BillingAddress) commercebilling.PayerIn {
	return commercebilling.PayerIn{Name: name, Email: email, Phone: phone, Address: billingAddress(a)}
}

// billingAddress is the buyer's address in commerce's type, nil when none.
func billingAddress(a *client.BillingAddress) *types.Address {
	if a == nil {
		return nil
	}
	out := &types.Address{Line1: a.Line1, Line2: a.Line2, City: a.City, State: a.State, PostalCode: a.PostalCode, Country: a.Country}
	if out.Empty() {
		return nil
	}
	return out
}

// armSavedCards schedules monthly card renewal at PeriodEnd (charging $0 before due)
// for any active wallet or processor-recorded card subscription whose subscriber
// already has a chargeable card vaulted on file, so their subscription renews
// automatically every month without pre-due emails or manual intervention.
func armSavedCards(ctx context.Context) {
	orgs := make([]*organization.Organization, 0)
	if _, err := organization.Query(commercedatastore.New(ctx)).GetAll(&orgs); err != nil {
		return
	}
	now := time.Now()
	for _, org := range orgs {
		subs, err := commercebilling.ListSubscriptions(ctx, org, "", "")
		if err != nil || len(subs) == 0 {
			continue
		}
		scheduled := map[string]bool{}
		for _, s := range subs {
			if s.Status == subscription.Scheduled && s.Test == org.TestMode() {
				scheduled[s.UserId] = true
			}
		}
		db := commercedatastore.New(org.Namespaced(ctx))
		for _, s := range subs {
			if s.Test != org.TestMode() || s.Status != subscription.Active || s.EndCancel || !s.PeriodEnd.After(now) || scheduled[s.UserId] {
				continue
			}
			wallet := s.Type != subscription.External && strings.EqualFold(strings.TrimSpace(s.DefaultPaymentMethod), "credits")
			extCard := s.Type == subscription.External && strings.TrimSpace(s.DefaultPaymentMethod) == "" &&
				(strings.EqualFold(strings.TrimSpace(s.ProviderType), "square") || strings.EqualFold(strings.TrimSpace(s.ProviderType), "stripe"))
			if !wallet && !extCard {
				continue
			}
			pm := defaultSavedCard(db, s.UserId)
			if pm == nil {
				continue
			}
			slug := s.Plan.Slug
			if slug == "" {
				slug = s.PlanId
			}
			if _, serr := commercebilling.Subscribe(ctx, org, commercebilling.SubscribeIn{
				Subject:        s.UserId,
				MethodID:       pm.Id(),
				PlanID:         slug,
				Quantity:       s.Quantity,
				Interval:       s.Plan.Interval,
				Provider:       strings.ToLower(strings.TrimSpace(pm.ProviderType)),
				IdempotencyKey: "arm-saved-card:" + s.Id(),
			}); serr == nil {
				scheduled[s.UserId] = true
			}
		}
	}
}

func defaultSavedCard(db *commercedatastore.Datastore, subject string) *paymentmethod.PaymentMethod {
	methods := make([]*paymentmethod.PaymentMethod, 0)
	if _, err := paymentmethod.Query(db).Filter("CustomerId=", subject).GetAll(&methods); err != nil || len(methods) == 0 {
		return nil
	}
	var fallback *paymentmethod.PaymentMethod
	for _, m := range methods {
		prov := strings.ToLower(strings.TrimSpace(m.ProviderType))
		if strings.TrimSpace(m.ProviderRef) == "" || (prov != "square" && prov != "stripe") {
			continue
		}
		if m.IsDefault {
			return m
		}
		if fallback == nil {
			fallback = m
		}
	}
	return fallback
}
