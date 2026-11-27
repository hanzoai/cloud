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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/account"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/internal/consent"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountCards registers the card endpoints and the sweep. Called from routes.
func mountCards(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	g.Post("/topup/token", o.topupToken)
	g.Post("/topup", o.topupSaved)
	g.Post("/paypal/order", o.paypalOrder)
	// UNTYPED BY DESIGN — subscribe answers TWO statuses over two different
	// bodies, and one of them is VERBATIM: a replayed sale returns the sealed
	// bytes the first attempt sent (200), a fresh one the sale it just made (201).
	// A typed op declares one Out, and re-marshalling a sealed replay is no longer
	// the same bytes. See untypedByDesign.
	g.Raw(http.MethodPost, "/subscribe/card", cloud.Handle(o.s, subscribeWithCard))
	g.Post("/recharge/run-all", o.rechargeAll)
	// THE RULE THE SWEEP ABOVE ACTS ON. It shipped without these for months: the
	// sweep recharged "every org below its own threshold" and no customer could set
	// a threshold, so it fired for nobody. A schedule with no way to opt into it is
	// a feature that exists only in its own log line.
	g.Get("/recharge", o.autoRecharge)
	g.Put("/recharge", o.setAutoRecharge)
}

// The prose for all four is declared here rather than lifted off the handlers:
// the two top-ups and the sweep answer the store's own receipt, and the sale is
// the one that stays RAW, because it answers TWO shapes — a fresh receipt at 201,
// or the sealed body of an identical earlier sale replayed verbatim at 200, which
// no single declared response can be.
func init() {
	spec.Describe("/v1/billing/topup/token", http.MethodPost,
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

	spec.Describe("/v1/billing/topup", http.MethodPost,
		"Add funds with a card already on file",
		"Charges a saved card and credits the caller's prepaid wallet.\n\n"+
			"The method must belong to the caller: one that does not is NOT FOUND rather "+
			"than refused, so an id cannot be probed for existence. A saved row whose "+
			"card is no longer chargeable is 422 — add the card again — which is a "+
			"different thing to do than a decline (402) or a bad amount (400).\n\n"+
			"Retries behave exactly as they do for a token top-up: same key, same replay, "+
			"same exactly-once at the processor.\n\n"+
			"The org's card on file is charged only by an admin of the org or the "+
			"platform: a member billed from the org's account is refused 403 with a "+
			"sentence naming who to ask. A member billed from a wallet of their own "+
			"charges that wallet's card.")

	spec.Describe("/v1/billing/subscribe/card", http.MethodPost,
		"Buy a plan with a card",
		"Vaults the card (or reuses one already on file), charges the plan's FIRST "+
			"period at the catalog price, and opens the subscription — one act, all of "+
			"it server-side.\n\n"+
			"There is NO AMOUNT in the request. `level` picks which of the plan's "+
			"published prices to buy at — an index, never a number — so what the card is "+
			"charged is decided by the catalog and underpaying cannot be expressed.\n\n"+
			"`interval` picks the term: \"month\" (the default) or \"year\", which charges "+
			"the plan's annual total now and renews yearly. A plan with no annual price "+
			"refuses \"year\".\n\n"+
			"`lines` are a team's other seat types, bought on the same subscription: "+
			"`[{\"planId\":\"team-premium\",\"quantity\":1}]` beside `planId` \"team\" and "+
			"`quantity` 1 is a team of one standard and one premium seat, charged both "+
			"seats' prices. A team is at least two members across every seat type and "+
			"at most 100; a line of a plan that is not a seat type of the team is 400 "+
			"before any card is charged.\n\n"+
			"`coupon` is a plan coupon code (case-insensitive) for a percent off the FIRST "+
			"month only; renewals bill the catalog price. It is a code, never an amount. "+
			"A code that is unknown, expired, used up, not for this plan, or sent with "+
			"annual billing or prepaid credit is 400, and one this org or this card has "+
			"already redeemed is 409 — before any card is charged, never a silent full-"+
			"price sale. The receipt names the coupon and its percent.\n\n"+
			"`wallet: true` says `sourceId` is a wallet's single-use token (Apple Pay, "+
			"Google Pay, Cash App Pay). The first period is authorized on the wallet, any "+
			"coupon is held against the org and the card behind it, and only then is the "+
			"payment captured; a coupon already used by either voids the hold and is 409. "+
			"Cash App Pay names no card, so a coupon with it is 400. The token is kept "+
			"nowhere, so the plan renews only once a card plan follows it: a plan's period "+
			"is charged to a card on file, never to the prepaid balance, which pays for "+
			"usage. A `sourceId` of \"balance\" or \"credits\" is 400 `plan_needs_card`.\n\n"+
			"Bought while the account holds a plan paid outside checkout (a wire, a comp) "+
			"or one the balance used to renew, the card plan starts when that plan ends: "+
			"the card is kept now and charged then, nothing is charged today, and the "+
			"receipt states `startsAt`, `startsCents` and `follows`.\n\n"+
			"`balanceCents` is an organization's starting usage balance, bought in the "+
			"same payment as the seats: `amountCents` is seats x price plus it, the "+
			"invoice holds the seats and the balance is credited to the organization's "+
			"prepaid balance (`balanceCents`, `balanceId` on the receipt). It is whole "+
			"dollars and at most the plan's `limits.balanceCeiling`; one that is not, a "+
			"plan that states no ceiling, or a balance sent with a wallet token or on a "+
			"plan that starts later is 400 before any card is charged. A plan whose limits "+
			"state how an organization orders it (spend limits, balance months, a "+
			"ceiling) is bought only for the organization, by its admin, on a request "+
			"that selects it with X-Org-Id; one that selects none is 400 (\"choose the "+
			"organization to buy for\") before any card is charged. Whether the buyer "+
			"administers it is asked of IAM at the sale, not read off the token: 403 when "+
			"IAM no longer names them an admin, 503 when it cannot answer.\n\n"+
			"`consent` is the choice the checkout page holds, as @hanzo/event spells it "+
			"(\"analytics,marketing,ads\"). The sale reaches an ad platform only under it. "+
			"It counts only from a browser (a request with Sec-Fetch-Site), and a browser "+
			"that sends GPC or DNT has allowed nothing, whatever the page says.\n\n"+
			"A fresh sale answers 201 with the receipt. An identical retry answers 200 "+
			"with the FIRST sale's body, byte for byte, so a client cannot read a replay "+
			"as a second subscription having been opened. A caller already on a paid plan "+
			"is 409 rather than charged again.\n\n"+
			"The org's plan is bought only by an admin of the org or the platform: a "+
			"member billed from the org's account is refused 403, before any card is "+
			"charged, with a sentence naming who to ask. A member billed from a wallet "+
			"of their own buys a plan for that wallet.")

	spec.Describe("/v1/billing/recharge/run-all", http.MethodPost,
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
//
// Every answer but a replay is put on the audit trail (saleAudit): the sale, or the
// refusal, whether this endpoint or commerce refused it.
func subscribeWithCard(s *cloud.Service[state], c *zip.Ctx) error {
	in := &client.SaleIn{}
	sold, err := sell(c, in)
	if err != nil {
		saleAudit(c, s.State.trail, in, nil, err)
		return err
	}
	if len(sold.Replayed) > 0 {
		// The sealed body IS the answer, verbatim — the same bytes the first
		// attempt sent, not a second rendering of them.
		return document(c, http.StatusOK, sold.Replayed)
	}
	saleAudit(c, s.State.trail, in, sold.Sale, nil)
	b, merr := json.Marshal(sold.Sale)
	if merr != nil {
		return zip.Errorf(http.StatusBadGateway, "subscribe: could not read the sale")
	}
	return document(c, http.StatusCreated, b)
}

// sell is one sale up to commerce's answer: who buys, what they asked for, read into
// in as it is learned so a refusal names what it refused, and the sale commerce made
// of it. Every refusal here answers before anything is charged.
func sell(c *zip.Ctx, in *client.SaleIn) (*client.Sold, error) {
	if err := cloud.CSRF(c.Context()); err != nil {
		return nil, err
	}
	if err := opened(c); err != nil {
		return nil, err
	}
	var body struct {
		SourceID string `json:"sourceId,omitempty"`
		MethodID string `json:"paymentMethodId,omitempty"`
		PlanID   string `json:"planId"`
		StoreID  string `json:"storeId,omitempty"`
		Quantity int    `json:"quantity,omitempty"`
		// Lines are a team's other seat types: [{planId, quantity}].
		Lines    []client.SaleLine `json:"lines,omitempty"`
		Currency string            `json:"currency,omitempty"`
		Level    int               `json:"level,omitempty"`
		// Interval is the term bought: "month" (the default) or "year".
		Interval string `json:"interval,omitempty"`
		// Country, QuotedAmount and QuotedCurrency: the buyer's country, and the
		// price they were shown in their currency (see client.SaleIn).
		Country        string `json:"country,omitempty"`
		QuotedAmount   int64  `json:"quotedAmount,omitempty"`
		QuotedCurrency string `json:"quotedCurrency,omitempty"`
		// Coupon is a plan coupon code for a percent off the first month.
		Coupon string `json:"coupon,omitempty"`
		// Wallet says sourceId is a wallet's single-use token (Apple Pay, Google
		// Pay, Cash App Pay, PayPal), which pays the first period itself.
		Wallet bool `json:"wallet,omitempty"`
		// Provider names the explicit wallet/card rail ("square" or "paypal") when
		// sourceId is a provider-specific order/token.
		Provider string `json:"provider,omitempty"`
		// Consent is the choice the checkout page holds, as @hanzo/event spells it.
		Consent *string `json:"consent,omitempty"`
		// BalanceCents is an organization's starting usage balance, bought in the
		// same payment: whole dollars, at most the plan's limits.balanceCeiling.
		BalanceCents int64 `json:"balanceCents,omitempty"`
		// BillingPhone and BillingAddress are what the buyer typed in the payment
		// form. The buyer's NAME and EMAIL are not here: they are the identity's.
		Phone   string                 `json:"billingPhone,omitempty"`
		Billing *client.BillingAddress `json:"billingAddress,omitempty"`
	}
	var (
		callCtx      context.Context
		org, subject string
		chosen       bool
	)
	if principal.Validated(c) {
		// A plan for the org is its admins' to buy; a member buys one only for a
		// wallet of their own (steward).
		st, err := steward(c, planAct)
		if err != nil {
			return nil, err
		}
		org, subject = st.org, st.subject
		in.Subject = subject
		if berr := c.Bind(&body); berr != nil {
			return nil, zip.ErrBadRequest("invalid request body")
		}
		callCtx = c.Forward()
		chosen = chose(c, org)
	} else {
		if strings.TrimSpace(c.Header("Authorization")) != "" || retryKey(c) == "" {
			return nil, principal.Unauthenticated()
		}
		if berr := c.Bind(&body); berr != nil {
			return nil, principal.Unauthenticated()
		}
		planID, sourceID := strings.TrimSpace(body.PlanID), strings.TrimSpace(body.SourceID)
		if planID == "" || sourceID == "" || sourceID == "balance" || strings.TrimSpace(body.MethodID) != "" || body.BalanceCents != 0 {
			return nil, principal.Unauthenticated()
		}
		callCtx, org, subject = guestSale(c, planID, sourceID)
		in.Subject = subject
	}
	*in = client.SaleIn{
		// The host the plan is bought on: the catalog is the serving brand's, and a
		// brand that sells none sells none here either (listPlans).
		Host:           c.Host(),
		SourceID:       body.SourceID,
		MethodID:       body.MethodID,
		PlanID:         body.PlanID,
		Subject:        subject,
		StoreID:        body.StoreID,
		Quantity:       body.Quantity,
		Lines:          body.Lines,
		Level:          body.Level,
		Interval:       body.Interval,
		Currency:       body.Currency,
		Email:          callerEmail(c),
		Name:           callerName(c),
		Phone:          strings.TrimSpace(body.Phone),
		Billing:        body.Billing,
		IdempotencyKey: retryKey(c),
		Coupon:         body.Coupon,
		Wallet:         body.Wallet,
		Provider:       body.Provider,
		// The checkout's own choice: nil off a browser, none under GPC or DNT.
		Consent:      consent.Consented(c, body.Consent),
		BalanceCents: body.BalanceCents,
		Chosen:       chosen,
		// Only the buyer's own statement of where they are: a sale with none stays in the
		// plan's currency, so an API caller is never billed in a currency by their edge.
		Country:        normalCountry(body.Country),
		QuotedAmount:   body.QuotedAmount,
		QuotedCurrency: strings.TrimSpace(body.QuotedCurrency),
	}
	return ask(callCtx, org, "subscribe", func(ctx context.Context) (*client.Sold, error) {
		return commercepeer.BillingSubscribe(ctx, in)
	})
}

// guestSale resolves a deterministic guest wallet in the shared signup org for a
// pay-first checkout that has not signed in yet; the buyer links the resulting
// subscription to their account on /pay/success via X-Subscription-Claim.
func guestSale(c *zip.Ctx, planID, key string) (context.Context, string, string) {
	org := account.SignupOrg
	seed := retryKey(c)
	if seed == "" {
		seed = strings.TrimSpace(planID) + ":" + strings.TrimSpace(key)
	}
	sum := sha256.Sum256([]byte("guest-sale:" + seed))
	guest := "guest-" + hex.EncodeToString(sum[:6])
	subject := account.Person(org, guest).Subject()
	ctx := zip.WithCaller(principal.WithActing(context.Background(), org), zip.Caller{
		Org:       org,
		Owner:     org,
		User:      guest,
		Name:      guest,
		RequestID: c.RequestID(),
	})
	return ctx, org, subject
}

// saleAudit puts one plan sale on the audit trail as billing.plan.sale, filed under
// the org the request acted in: who bought what for it — the plan, the term, the
// seats, what the card was charged and the starting balance it bought — or the
// refusal the buyer was answered with, whether this endpoint or commerce refused it.
// A request that selected an org it was not opened carries that selection. A
// SuperAdmin acting in another org is recorded with their own org as Home. A request
// that resolved no org is on the request trail alone.
//
// It is recorded after the fact and cannot fail the sale: by then the card may have
// been charged, and an answer withheld over a lost audit row is a customer told their
// payment failed when it did not. A row the trail does not take is logged.
func saleAudit(c *zip.Ctx, trail *audit.Recorder, in *client.SaleIn, s *client.Sale, fault error) {
	org, ok := principal.Org(c)
	if trail == nil || !ok {
		return
	}
	home := principal.Owner(c)
	if !principal.Super(c) || home == org {
		home = ""
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail(), Home: home},
		Action:    "billing.plan.sale",
		Resource:  audit.Resource{Type: "plan", ID: in.PlanID},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: principal.Super(c)},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusCreated},
		SourceIP:  geo.IP(c),
		RequestID: c.RequestID(),
	}
	terms := map[string]any{
		"org": org, "subject": in.Subject, "plan": in.PlanID, "interval": in.Interval,
		"quantity": in.Quantity, "balanceCents": in.BalanceCents,
	}
	if sel := principal.Unopened(c); sel != "" {
		terms["selected"] = sel
	}
	if s != nil {
		rec.Resource = audit.Resource{Type: "subscription", ID: s.SubscriptionID}
		terms["seats"], terms["amountCents"], terms["currency"] = s.Seats, s.AmountCents, s.Currency
		terms["invoice"], terms["balanceId"], terms["replaced"], terms["test"] = s.InvoiceID, s.BalanceID, s.Replaced, s.Test
	}
	if fault != nil {
		status := http.StatusInternalServerError
		var he *zip.HTTPError
		if errors.As(fault, &he) {
			status = he.Status
		}
		rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: fault.Error()}
	}
	b, _ := json.Marshal(terms)
	rec.After = audit.Redact(b)
	if _, err := trail.Append(c.Context(), rec); err != nil {
		luxlog.Default().Error("a plan sale was not put on the audit trail", "org", org, "plan", in.PlanID, "err", err)
	}
}

// opened refuses a charge for an org the caller selected (X-Org-Id) and the identity
// boundary did not open: the token lists no such org — one minted before the org
// was created, say — so the request would act in the caller's own org, and a plan
// with its seats and its balance, or a top-up, would land in that org instead.
// Nothing is charged.
func opened(c *zip.Ctx) error {
	if org := principal.Unopened(c); org != "" {
		return zip.Errorf(http.StatusConflict,
			"your sign-in does not list the organization %q yet: refresh your sign-in and try again; nothing was charged", org)
	}
	return nil
}

// chose reports whether the caller chose org, the org the sale is for: the request
// selected it with X-Org-Id and the identity boundary opened that selection (a
// selection it did not open is refused by opened), rather than the request acting in
// the caller's default org — whose X-Org-Id the boundary writes itself, so the
// header alone cannot say (principal.Defaulted). An organization's order is sold
// only when it did.
func chose(c *zip.Ctx, org string) bool {
	return !principal.Defaulted(c) && strings.TrimSpace(c.Header("X-Org-Id")) == org
}

// retryKey is the caller's own retry key, or empty for the store's windowed
// derivation over the stable facts — which is what a browser sending no header
// has always relied on.
func retryKey(c *zip.Ctx) string { return strings.TrimSpace(c.Header("X-Idempotency-Key")) }

// Reads the auto-reload rule of the wallet the caller pays from — the org's own
// account, or a person's wallet in the signup org: top that balance up by
// `amountCents` whenever it falls below `thresholdCents`, charging that payer's card
// on file off-session, at most `perDay` times in a UTC day (0 is no daily limit,
// which the payer's plan sets). It is the same setting every prepaid AI account
// calls auto-reload.
//
// An org that has never set one reads as disabled with zeroes rather than as an
// error — "no rule" answers the question — and `stored` is how a caller tells
// never-configured from deliberately-off.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) autoRecharge(ctx context.Context, _ *cloud.Unit) (*client.AutoRecharge, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "auto-recharge", func(ctx context.Context) (*client.AutoRecharge, error) {
		return commercepeer.BillingAutoRecharge(ctx, &client.AutoRechargeIn{Subject: subject})
	})
}

// Sets the auto-reload rule of the wallet the caller pays from, and answers with the
// rule as stored.
//
// The org's own account's rule is its admins' to change, and the platform's; a
// member billed from a wallet of their own (a person in the signup org) changes that
// wallet's rule and no other (403 otherwise, with the plain sentence "Only an admin
// of <org> can change its auto-reload. Ask an admin to do it."): the sweep charges
// the payer's default card with nobody there.
//
// ENABLING REQUIRES A CARD ON FILE (400), because the sweep charges off-session:
// a rule naming no chargeable method is a promise the schedule cannot keep. The
// amount is a top-up's own range, $5 to $5,000, and the balance it reloads below
// is $0 to $5,000; a value outside either is refused the same way, the range in
// the sentence. The sweep reloads at most `perDay` times in a UTC day.
//
// The rule is the payer's OWN. The org and the wallet come from the validated
// principal and the body names neither, so there is no field a write could be
// steered through onto another payer's schedule.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) setAutoRecharge(ctx context.Context, in *client.AutoRechargeEdit) (*client.AutoRecharge, error) {
	// The control is asked IN THE OPERATION, not on the route: MCP, the call plane
	// and the CLI reach this op without touching route middleware, and a browser
	// authenticates this surface from an ambient session cookie that any origin's
	// request carries. See changesSomething in change_control_test.go.
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	s, err := steward(c, reloadAct)
	if err != nil {
		return nil, err
	}
	return ask(ctx, s.org, "set auto-recharge", func(ctx context.Context) (*client.AutoRecharge, error) {
		return commercepeer.BillingAutoRechargeSet(ctx, &client.AutoRechargeSetIn{Subject: s.subject, Edit: *in})
	})
}
