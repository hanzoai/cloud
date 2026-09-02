package billing

// cards.go serves the three endpoints that charge a card, and the sweep that
// charges them on a schedule: top up with a fresh token, top up with a card on
// file, buy a plan, and recharge every org that has fallen below its own
// threshold.
//
// THE SUBJECT IS RESOLVED HERE, from the caller's own credential, and is never
// read off the request. That is the whole isolation property of this file: a
// client- or proxy-set selector is what once let the same customer top up one
// wallet on a charge and a different one on the next, stranding the credit off
// the key their usage draws from. One identity, one key.
//
// NO CALLER NAMES A PRICE for a plan. The charge is the catalog's price at the
// level asked for, and there is no field an amount could be written in — so
// underpaying is not a check that can be forgotten, it is a request that cannot
// be expressed. A top-up does carry an amount, and it is bounded server-side
// where the money moves, because a scripted caller never passes a console's cap.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountCards registers the card endpoints and the sweep. Called from routes.
func mountCards(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Post(zapp, "/v1/billing/topup/token", o.topupToken)
	zip.Post(zapp, "/v1/billing/topup", o.topupSaved)
	// UNTYPED BY DESIGN — subscribe answers TWO statuses over two different
	// bodies, and one of them is VERBATIM: a replayed sale returns the sealed
	// bytes the first attempt sent (200), a fresh one the sale it just made (201).
	// A typed op declares one Out, and re-marshalling a sealed replay is no longer
	// the same bytes. See untypedByDesign.
	app.Post("/v1/billing/subscribe/card", cloud.Handle(o.s, subscribeWithCard))
	zip.Post(zapp, "/v1/billing/recharge/run-all", o.rechargeAll)
	// THE RULE THE SWEEP ABOVE ACTS ON. It shipped without these for months: the
	// sweep recharged "every org below its own threshold" and no customer could set
	// a threshold, so it fired for nobody. A schedule with no way to opt into it is
	// a feature that exists only in its own log line.
	zip.Get(zapp, "/v1/billing/recharge", o.autoRecharge)
	zip.Put(zapp, "/v1/billing/recharge", o.setAutoRecharge)
}

// The prose for all four is declared here rather than lifted off the handlers:
// the two top-ups and the sweep answer the store's own receipt, and the sale is
// the one that stays RAW, because it answers TWO shapes — a fresh receipt at 201,
// or the sealed body of an identical earlier sale replayed verbatim at 200, which
// no single declared response can be.
func init() {
	openapi.Describe("/v1/billing/topup/token", http.MethodPost,
		"Add funds with a single-use card token",
		"Charges a card token from the browser's payment SDK and credits the caller's "+
			"prepaid wallet — the cold-customer path, where nothing has to be saved "+
			"first.\n\n"+
			"The wallet credited is the CALLER'S OWN, resolved from their signed "+
			"identity. It is never a value in the request: a client-set selector is how "+
			"a customer once topped up one account while their usage drew from "+
			"another.\n\n"+
			"`X-Idempotency-Key` makes a retry safe. With one, a repeat replays the first "+
			"result; without one, the same amount from the same subject inside a short "+
			"window does too. The key reaches the processor as well as our own guard, so "+
			"the charge is exactly-once at the gateway even if our guard store is down.\n\n"+
			"The amount is bounded server-side. A decline is 402 and nothing is credited.")

	openapi.Describe("/v1/billing/topup", http.MethodPost,
		"Add funds with a card already on file",
		"Charges a saved card and credits the caller's prepaid wallet.\n\n"+
			"The method must belong to the caller: one that does not is NOT FOUND rather "+
			"than refused, so an id cannot be probed for existence. A saved row whose "+
			"card is no longer chargeable is 422 — add the card again — which is a "+
			"different thing to do than a decline (402) or a bad amount (400).\n\n"+
			"Retries behave exactly as they do for a token top-up: same key, same replay, "+
			"same exactly-once at the processor.")

	openapi.Describe("/v1/billing/subscribe/card", http.MethodPost,
		"Buy a plan with a card",
		"Vaults the card (or reuses one already on file), charges the plan's FIRST "+
			"period at the catalog price, and opens the subscription — one act, all of "+
			"it server-side.\n\n"+
			"There is NO AMOUNT in the request. `level` picks which of the plan's "+
			"published prices to buy at — an index, never a number — so what the card is "+
			"charged is decided by the catalog and underpaying cannot be expressed.\n\n"+
			"A fresh sale answers 201 with the receipt. An identical retry answers 200 "+
			"with the FIRST sale's body, byte for byte, so a client cannot read a replay "+
			"as a second subscription having been opened. A caller already on a paid plan "+
			"is 409 rather than charged again.")

	openapi.Describe("/v1/billing/recharge/run-all", http.MethodPost,
		"Recharge every org that has fallen below its threshold",
		"Sweeps every organization and, for those with auto-recharge on whose available "+
			"balance has dropped below their own threshold, charges the default card and "+
			"credits the balance.\n\n"+
			"It charges cards across EVERY tenant, so it is platform authority only — "+
			"never an org owner, who could otherwise sweep-charge saved cards estate-"+
			"wide. Its caller is a schedule, not a person.\n\n"+
			"`orgs` is the population considered, not the row count: that difference is "+
			"how a reader tells 'nobody was below threshold' from 'the sweep never ran'. "+
			"One org's failure is reported in its own row and does not stop the rest.")
}

// subscribeWithCard buys a plan.
//
// It answers TWO shapes and keeps them apart, which is why it is raw: a fresh
// sale is 201 with the receipt, and an identical retry is 200 carrying the first
// sale's sealed body verbatim. Re-rendering that body would hand a retrying
// client different bytes for the same completed act.
func subscribeWithCard(s *cloud.Service[state], c *zip.Ctx) error {
	if err := account.CSRF(c.Context()); err != nil {
		return err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return err
	}
	var body struct {
		SourceID string `json:"sourceId,omitempty"`
		MethodID string `json:"paymentMethodId,omitempty"`
		PlanID   string `json:"planId"`
		StoreID  string `json:"storeId,omitempty"`
		Quantity int    `json:"quantity,omitempty"`
		Currency string `json:"currency,omitempty"`
		Level    int    `json:"level,omitempty"`
	}
	if berr := c.Bind(&body); berr != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	sold, aerr := ask(c.Context(), org, "subscribe", func(ctx context.Context) (*plane.Sold, error) {
		return commercepeer.BillingSubscribe(ctx, &plane.SaleIn{
			SourceID:       body.SourceID,
			MethodID:       body.MethodID,
			PlanID:         body.PlanID,
			Subject:        subject,
			StoreID:        body.StoreID,
			Quantity:       body.Quantity,
			Level:          body.Level,
			Currency:       body.Currency,
			Email:          callerEmail(c),
			IdempotencyKey: retryKey(c),
		})
	})
	if aerr != nil {
		return aerr
	}
	if len(sold.Replayed) > 0 {
		// The sealed body IS the answer, verbatim — the same bytes the first
		// attempt sent, not a second rendering of them.
		return document(c, http.StatusOK, sold.Replayed)
	}
	b, merr := json.Marshal(sold.Sale)
	if merr != nil {
		return zip.Errorf(http.StatusBadGateway, "subscribe: could not read the sale")
	}
	return document(c, http.StatusCreated, b)
}

// retryKey is the caller's own retry key, or empty for the store's windowed
// derivation over the stable facts — which is what a browser sending no header
// has always relied on.
func retryKey(c *zip.Ctx) string { return strings.TrimSpace(c.Header("X-Idempotency-Key")) }

// Reads the caller's auto-reload rule: top the balance up by `amountCents`
// whenever it falls below `thresholdCents`, charging the card on file
// off-session. It is the same setting every prepaid AI account calls auto-reload.
//
// An org that has never set one reads as disabled with zeroes rather than as an
// error — "no rule" answers the question — and `stored` is how a caller tells
// never-configured from deliberately-off.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) autoRecharge(ctx context.Context, _ *noInput) (*plane.AutoRecharge, error) {
	org, _, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "auto-recharge", func(ctx context.Context) (*plane.AutoRecharge, error) {
		return commercepeer.BillingAutoRecharge(ctx)
	})
}

// Sets the caller's auto-reload rule, and answers with the rule as stored.
//
// ENABLING REQUIRES A CARD ON FILE (400), because the sweep charges off-session:
// a rule naming no chargeable method is a promise the schedule cannot keep. A
// non-positive amount and a negative threshold are refused the same way, each
// naming the field that was wrong.
//
// The rule is the caller's OWN. The org comes from the validated principal and
// the body names none, so there is no field a write could be steered through onto
// another tenant's schedule.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) setAutoRecharge(ctx context.Context, in *plane.AutoRechargeEdit) (*plane.AutoRecharge, error) {
	// The control is asked IN THE OPERATION, not on the route: MCP, the call plane
	// and the CLI reach this op without touching route middleware, and a browser
	// authenticates this surface from an ambient session cookie that any origin's
	// request carries. See changesSomething in change_control_test.go.
	if err := account.CSRF(ctx); err != nil {
		return nil, err
	}
	org, _, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "set auto-recharge", func(ctx context.Context) (*plane.AutoRecharge, error) {
		return commercepeer.BillingAutoRechargeSet(ctx, in)
	})
}
