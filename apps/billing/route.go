package billing

// route.go serves the three endpoints of a checkout that is routed between
// processors: which one takes this buyer, the Stripe intent a local method pays,
// and where that payment stands.
//
// THE SUBJECT IS RESOLVED HERE, from the caller's own credential, and never read off
// the request: a buyer routes, opens and reads only their own payments. Commerce owns
// the rest — the policy, the placement, the quote, the credit.

import (
	"context"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
)

// mountRoute registers the routed-checkout endpoints. Called from routes.
func mountRoute(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	g.Post("/route", o.route)
	g.Post("/topup/intent", o.topupIntent)
	g.Get("/topup/intent/:id", o.topupIntentState)
	g.Post("/topup/intent/:id/cancel", o.topupIntentCancel)
	g.Get("/quote", o.quote)
}

func init() {
	spec.Describe("/v1/billing/route", "POST",
		"Which processor takes my checkout",
		"Answers the processor that takes the caller's NEW card checkout and the methods its "+
			"card slot mounts, with the rule that decided and the seven-step reason chain.\n\n"+
			"The order is: a card saved at a processor stays on it; the merchant accounts that can "+
			"take the charge in the checkout's currency; the method the buyer chose; the admin's "+
			"rules; the buyer's placement, kept the same way every time; the weights; and a safe "+
			"fallback, taken only before a payment starts. The buyer's country chooses methods and "+
			"a default local currency; it never decides whether a processor is eligible. When an "+
			"amount is asked for, the answer carries the quote the buyer authorizes. The country is "+
			"the body's, else the edge's.")
	spec.Describe("/v1/billing/topup/intent", "POST",
		"Open a payment-element checkout for a top-up",
		"Opens a payment for the caller's own wallet in the currency the buyer chose, else their "+
			"country's default, and answers what the payment element mounts with. The price is made "+
			"by the policy's FX source and returned as `quote`; the amount the buyer confirms is "+
			"`quote.amount`, and the wallet is credited exactly `quote.usdCents`. A currency the "+
			"merchant or the chosen method cannot take is 400. Nothing is credited by this call: the "+
			"credit is posted once, when the processor reports the payment succeeded.")
	spec.Describe("/v1/billing/topup/intent/{id}", "GET",
		"Where my payment stands",
		"Answers created, pending, succeeded or canceled for one of the caller's own payments, and "+
			"whether the wallet holds the money. Show a payment as paid only when `credited` is true. "+
			"A payment that is not the caller's is 404.")
	spec.Describe("/v1/billing/topup/intent/{id}/cancel", "POST",
		"Cancel my unfinished payment",
		"Cancels one of the caller's own top-up payments that has not been confirmed, at the "+
			"processor, and frees its place under the cap of open payments. It is idempotent: a "+
			"payment already canceled answers canceled again. A payment that has succeeded, is "+
			"processing at the bank or is credited is 409, and one that is not the caller's is 404. "+
			"It moves no money and cancels nothing else.")
	spec.Describe("/v1/billing/quote", "GET",
		"The price of what I am about to buy",
		"Answers the price, in the buyer's currency, of a plan's first charge (`plan`, with "+
			"`level` and `interval`) or of an amount of credit (`usdCents`), before any sale. "+
			"`currency` is the buyer's choice and defaults from `country`. It is the displayed "+
			"rate; the sale locks its own quote and refuses with the new price when that differs "+
			"from what was shown.")
}

// country is the buyer's country: the request's own statement, else the edge's.
func country(c *zip.Ctx, stated string) string {
	for _, raw := range []string{stated, c.Header("CF-IPCountry"), c.Header("CloudFront-Viewer-Country"), c.Header("X-Vercel-IP-Country"), c.Header("X-Country-Code")} {
		if s := normalCountry(raw); s != "" {
			return s
		}
	}
	return ""
}

// normalCountry is raw as an ISO 3166-1 alpha-2 code, or "" when it is not one
// (the edge's XX and T1 pseudo-codes are not).
func normalCountry(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if len(s) != 2 || s == "XX" || s == "T1" || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return ""
	}
	return s
}

// routeIn asks where a checkout goes.
type routeIn struct {
	// Country is the buyer's ISO 3166-1 alpha-2 country; the edge's geo country when absent.
	Country string `json:"country,omitempty" url:"-"`
	// Currency is what the checkout is paid in; usd when absent.
	Currency string `json:"currency,omitempty" url:"-"`
	// AmountCents, when given, asks for the quote of that many dollars of credit.
	AmountCents int64 `json:"amountCents,omitempty" url:"-"`
	// Method is how the buyer chose to pay, when they have (card, cash_app, pix, ...).
	Method string `json:"method,omitempty" url:"-"`
}

// route answers which processor takes the caller's new checkout.
func (o ops) route(ctx context.Context, in *routeIn) (*client.Route, error) {
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
	return ask(ctx, org, "route", func(ctx context.Context) (*client.Route, error) {
		return commercepeer.BillingRoute(ctx, &client.RouteIn{Subject: subject, Country: country(c, in.Country), Currency: strings.TrimSpace(in.Currency), AmountCents: in.AmountCents, Method: strings.TrimSpace(in.Method)})
	})
}

// topupIntentIn opens a payment.
type topupIntentIn struct {
	// AmountCents is the dollars of credit to buy, in cents.
	AmountCents int64 `json:"amountCents" url:"-"`
	// Country is the buyer's ISO 3166-1 alpha-2 country; the edge's geo country when absent.
	Country string `json:"country,omitempty" url:"-"`
	// Currency is the currency the buyer chose to pay in; empty takes the country's default.
	Currency string `json:"currency,omitempty" url:"-"`
	// Method is the payment method the buyer chose, when they have.
	Method string `json:"method,omitempty" url:"-"`
	// Phone and Billing are what the buyer typed in the payment form.
	Phone   string                 `json:"billingPhone,omitempty" url:"-"`
	Billing *client.BillingAddress `json:"billingAddress,omitempty" url:"-"`
	// Optional plan fields when paying a plan's first period via bank transfer (ACH).
	PlanID   string `json:"planId,omitempty" url:"-"`
	Quantity int    `json:"quantity,omitempty" url:"-"`
	Level    int    `json:"level,omitempty" url:"-"`
	Interval string `json:"interval,omitempty" url:"-"`
	Coupon   string `json:"coupon,omitempty" url:"-"`
}

// topupIntent opens the caller's Stripe checkout for a top-up.
func (o ops) topupIntent(ctx context.Context, in *topupIntentIn) (*client.TopupIntentOut, error) {
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
	out, err := ask(ctx, org, "topup intent", func(ctx context.Context) (*client.TopupIntentOut, error) {
		return commercepeer.BillingTopupIntent(ctx, &client.TopupIntentIn{
			Subject: subject, Country: country(c, in.Country), AmountCents: in.AmountCents, IdempotencyKey: retryKey(c),
			Currency: strings.TrimSpace(in.Currency), Method: strings.TrimSpace(in.Method),
			Name: callerName(c), Email: callerEmail(c), Phone: strings.TrimSpace(in.Phone), Billing: in.Billing,
			PlanID: strings.TrimSpace(in.PlanID), Quantity: in.Quantity, Level: in.Level,
			Interval: strings.TrimSpace(in.Interval), Coupon: strings.TrimSpace(in.Coupon),
		})
	})
	if err != nil {
		billingRailMetrics.record("processor_intent", "open", "refused")
		return nil, err
	}
	billingRailMetrics.record("processor_intent", "open", "created")
	return out, nil
}

// intentRef names one payment, from the path.
type intentRef struct {
	// ID is the payment, as the open call answered it.
	ID string `json:"id"`
}

// topupIntentState says where one of the caller's own payments stands.
func (o ops) topupIntentState(ctx context.Context, in *intentRef) (*client.TopupIntentState, error) {
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	out, err := ask(ctx, org, "topup intent state", func(ctx context.Context) (*client.TopupIntentState, error) {
		return commercepeer.BillingTopupIntentState(ctx, &client.TopupIntentRef{Subject: subject, ID: in.ID})
	})
	if err != nil {
		billingRailMetrics.record("processor_intent", "state", "not_found")
		return nil, err
	}
	billingRailMetrics.record("processor_intent", "state", out.State)
	return out, nil
}

// topupIntentCancel ends one of the caller's own payments that is not yet confirmed.
func (o ops) topupIntentCancel(ctx context.Context, in *intentRef) (*client.TopupIntentCancelled, error) {
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
	out, err := ask(ctx, org, "topup intent cancel", func(ctx context.Context) (*client.TopupIntentCancelled, error) {
		return commercepeer.BillingTopupIntentCancel(ctx, &client.TopupIntentRef{Subject: subject, ID: in.ID})
	})
	if err != nil {
		billingRailMetrics.record("processor_intent", "cancel", "refused")
		return nil, err
	}
	billingRailMetrics.record("processor_intent", "cancel", out.State)
	return out, nil
}

// quoteQuery is what a buyer asks the price of, from the query string.
type quoteQuery struct {
	// Plan is the plan to price; absent when usdCents asks for credit.
	Plan string `json:"plan,omitempty" url:"plan,omitempty"`
	// Level selects the plan's published price level.
	Level int `json:"level,omitempty" url:"level,omitempty"`
	// Interval selects the billing term, month or year.
	Interval string `json:"interval,omitempty" url:"interval,omitempty"`
	// UsdCents asks for an amount of credit when no plan is named.
	UsdCents int64 `json:"usdCents,omitempty" url:"usdCents,omitempty"`
	// Country is the buyer's ISO 3166-1 alpha-2 country; the edge's geo country when absent.
	Country string `json:"country,omitempty" url:"country,omitempty"`
	// Currency is the currency the buyer chose; the country's default when absent.
	Currency string `json:"currency,omitempty" url:"currency,omitempty"`
	// Method is the payment method the buyer chose, when they have.
	Method string `json:"method,omitempty" url:"method,omitempty"`
}

// quote answers the price of what the caller is about to buy.
func (o ops) quote(ctx context.Context, in *quoteQuery) (*client.PriceQuote, error) {
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "quote", func(ctx context.Context) (*client.PriceQuote, error) {
		return commercepeer.BillingQuote(ctx, &client.QuoteIn{
			Subject: subject, Plan: strings.TrimSpace(in.Plan), Level: in.Level, Interval: strings.TrimSpace(in.Interval),
			UsdCents: in.UsdCents, Country: country(c, in.Country), Currency: strings.TrimSpace(in.Currency), Method: strings.TrimSpace(in.Method),
		})
	})
}
