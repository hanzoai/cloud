package billing

// agent.go serves the headless purchase surface of /v1/billing: what is for sale and
// how it is paid, a checkout an agent opens, changes and completes in JSON, the order
// it confirms, a stablecoin top-up, a fresh shared payment token for a past-due invoice
// or a subscription, and a bank wire reconciled to a wallet.
//
// THE SUBJECT IS RESOLVED HERE, from the caller's own credential, and never read off
// the request: a buyer opens, reads and pays only their own checkouts. Commerce owns
// the rest — the price, the charge, the deposit and the credit.
//
// The catalog and the discovery document are public. They name no tenant and read no
// subject, so a caller with no credential is served: they are the one thing an agent
// reads before it has a token. The edge keeps one copy of each (publicCatalog).

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
)

// completionStatuses are the statuses a completion is answered with: complete, waiting
// on the buyer, and the refusals commerce chooses. A status commerce chose outside this
// set is answered as the class it is in (answered).
var completionStatuses = []int{
	http.StatusOK, http.StatusAccepted, http.StatusBadRequest, http.StatusPaymentRequired,
	http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity,
	http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable,
}

// mountAgent registers the headless purchase endpoints. Called from routes.
func mountAgent(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	// Raw, as the plan catalog is: the body is the catalog commerce rendered, forwarded whole.
	g.Raw(http.MethodGet, "/catalog", cloud.Handle(o.s, catalog))
	// Raw as well: the document carries a map (its links), and a map cannot cross the plane typed.
	g.Raw(http.MethodGet, "/discovery", cloud.Handle(o.s, discovery))
	g.Post("/checkouts", o.checkoutOpen, zip.WithStatus(http.StatusCreated))
	g.Get("/checkouts/:id", o.checkout)
	g.Patch("/checkouts/:id", o.checkoutUpdate)
	g.Post("/checkouts/:id/complete", o.checkoutComplete, zip.WithStatus(completionStatuses...))
	g.Get("/orders/:id", o.order)
	g.Post("/topup/crypto", o.topupCrypto)
	g.Post("/invoices/:id/agent-token", o.invoiceToken, zip.WithStatus(http.StatusOK, http.StatusPaymentRequired))
	g.Post("/subscriptions/:id/agent-token", o.subscriptionToken, zip.WithStatus(http.StatusOK, http.StatusPaymentRequired))
	g.Post("/wire/reconcile", o.wireReconcile)
}

func init() {
	spec.Describe("/v1/billing/catalog", http.MethodGet,
		"What is for sale in dollars and how it is paid for",
		"Answers the plans on sale on the host's brand, the credit packs, and the payment types "+
			"the org's processors take now (`shared_payment_token`, and `stablecoin` with the "+
			"networks a deposit may use), as `{version, currency, plans, creditPacks, payment, links}`.\n\n"+
			"PUBLIC. A caller with no credential is served, and the catalog is the brand the request "+
			"host names; one shared copy serves every anonymous reader. A caller with a credential is "+
			"answered for its own org's processors and is never served the shared copy.")
}

// catalog answers the public catalog, for the org the caller acts in when it has one.
func catalog(s *cloud.Service[state], c *zip.Ctx) error {
	// No credential is no org: cloud.For with an empty org is what an unauthenticated
	// caller has, and the host names the brand whose catalog is on sale.
	org, _ := principal.Org(c)
	out, err := ask(c.Forward(), org, "catalog", func(ctx context.Context) (*client.Rendered, error) {
		return commercepeer.BillingCatalog(ctx, &client.CatalogIn{Host: c.Host()})
	})
	if err != nil {
		return err
	}
	return document(c, http.StatusOK, out.Body)
}

// Answers where the purchase surface is and what it speaks: its version, the paths of its
// parts, the checkout's verbs in order, the payment types a checkout completes with, and
// how a subscription is renewed by an agent.
//
// PUBLIC. A caller with no credential is served, and one shared copy serves every
// anonymous reader.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func discovery(s *cloud.Service[state], c *zip.Ctx) error {
	out, err := ask(c.Forward(), "", "discovery", func(ctx context.Context) (*client.Rendered, error) {
		return commercepeer.BillingDiscovery(ctx)
	})
	if err != nil {
		return err
	}
	return document(c, http.StatusOK, out.Body)
}

// checkoutIn opens a checkout.
type checkoutIn struct {
	// Items is the one thing the checkout sells: USD credit, or a plan's first period.
	Items []client.AgentItem `json:"items"`
}

// Opens a checkout for exactly one item, USD credit (`kind: "credit"`, `usdCents`) or a
// plan's first period (`kind: "plan"`, `plan`, `interval`), and answers it priced as the
// sale prices it. Nothing is charged: a checkout is paid by completing it.
//
// The checkout is the caller's own, and only the caller reads, changes or pays it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) checkoutOpen(ctx context.Context, in *checkoutIn) (*checkoutView, error) {
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
	out, err := ask(ctx, org, "checkout", func(ctx context.Context) (*client.AgentCheckout, error) {
		return commercepeer.BillingCheckoutOpen(ctx, &client.CheckoutOpenIn{Subject: subject, Host: c.Host(), Items: in.Items})
	})
	if err != nil {
		return nil, err
	}
	return viewOf(out), nil
}

// checkoutView is a checkout as the browser reads it: the plane's checkout and the paths of
// its parts. The paths are made here from its id, because a map cannot cross the plane typed.
type checkoutView struct {
	client.AgentCheckout
	// Links are the paths of this checkout: `self` reads it, `complete` pays it.
	Links map[string]string `json:"links"`
}

// viewOf is c with its links.
func viewOf(c *client.AgentCheckout) *checkoutView {
	return &checkoutView{AgentCheckout: *c, Links: map[string]string{
		"self":     "/v1/billing/checkouts/" + c.ID,
		"complete": "/v1/billing/checkouts/" + c.ID + "/complete",
	}}
}

// checkoutRef names one checkout, from the path.
type checkoutRef struct {
	// ID is the checkout, as the open call answered it.
	ID string `json:"id"`
}

// Answers where one of the caller's own checkouts stands: open, pending, complete or
// failed, with the next step while it waits on the buyer and the order once it is
// complete. A checkout waiting on a stablecoin deposit is completed here once the deposit
// has been credited. A checkout that is not the caller's is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) checkout(ctx context.Context, in *checkoutRef) (*checkoutView, error) {
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	out, err := ask(ctx, org, "checkout", func(ctx context.Context) (*client.AgentCheckout, error) {
		return commercepeer.BillingCheckout(ctx, &client.CheckoutRef{Subject: subject, ID: in.ID})
	})
	if err != nil {
		return nil, err
	}
	return viewOf(out), nil
}

// checkoutChange replaces a checkout's item.
type checkoutChange struct {
	// ID is the checkout, from the path.
	ID string `json:"id"`
	// Items is the one thing the checkout sells now.
	Items []client.AgentItem `json:"items"`
}

// Replaces the item of one of the caller's checkouts that is still open, or failed, and
// prices it again. A checkout that is pending or complete can no longer be changed (409).
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) checkoutUpdate(ctx context.Context, in *checkoutChange) (*checkoutView, error) {
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
	out, err := ask(ctx, org, "checkout update", func(ctx context.Context) (*client.AgentCheckout, error) {
		return commercepeer.BillingCheckoutUpdate(ctx, &client.CheckoutUpdateIn{Subject: subject, Host: c.Host(), ID: in.ID, Items: in.Items})
	})
	if err != nil {
		return nil, err
	}
	return viewOf(out), nil
}

// completeIn pays a checkout.
type completeIn struct {
	// ID is the checkout, from the path.
	ID string `json:"id"`
	// Key is the Idempotency-Key header: it binds the charge to this checkout, so completing
	// twice under one key is one purchase. Required.
	Key string `json:"-" url:"-" header:"Idempotency-Key"`
	// Payment is how the checkout is paid.
	Payment client.AgentPay `json:"payment"`
}

// completed is a completion as the browser reads it: the checkout, answered under the
// status the attempt earned.
type completed struct {
	*checkoutView
	status int
}

// StatusCode is the status commerce chose, or the class of one the document does not name.
func (c *completed) StatusCode() int { return answered(c.status) }

// answered is status when a completion may be answered with it, else its class: a server
// fault is 502 and any other refusal 400.
func answered(status int) int {
	switch {
	case slices.Contains(completionStatuses, status):
		return status
	case status >= 500:
		return http.StatusBadGateway
	}
	return http.StatusBadRequest
}

// Pays one of the caller's checkouts with a shared payment token the buyer granted the
// agent (`{"payment":{"type":"shared_payment_token","token":"spt_..."}}`) or with a
// stablecoin deposit (`{"payment":{"type":"stablecoin","network":"base"}}`), and answers
// the checkout under the status the attempt earned.
//
// 200 is complete, with the order. 202 waits on the buyer, and `next` says what to do: send
// the deposit, or take the bank's step. A refusal is 402 for a payment that was declined and
// 400, 404, 409, 422 or 429 for the rest, always with `error.code` to branch on.
//
// The `Idempotency-Key` header is required. It binds the charge to this checkout, the buyer
// and the key, so completing twice is one purchase; the same key answers the same order, and
// another key on a checkout already paid is 409.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) checkoutComplete(ctx context.Context, in *completeIn) (*completed, error) {
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
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = retryKey(c)
	}
	done, err := ask(ctx, org, "checkout complete", func(ctx context.Context) (*client.CheckoutCompletion, error) {
		return commercepeer.BillingCheckoutComplete(ctx, &client.CheckoutCompleteIn{
			Subject: subject, Host: c.Host(), ID: in.ID, Key: key, Payment: in.Payment,
		})
	})
	if err != nil {
		return nil, err
	}
	return &completed{checkoutView: viewOf(&done.Checkout), status: done.Status}, nil
}

// Confirms a completed checkout: the order, what it cost, what the wallet was credited or
// the subscription it opened, and for a subscription what is left on the token it renews on.
// A checkout that is not complete has no order yet (404), and one that is not the caller's
// is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) order(ctx context.Context, in *checkoutRef) (*client.AgentOrder, error) {
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	org, subject, err := payerOf(c)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "order", func(ctx context.Context) (*client.AgentOrder, error) {
		return commercepeer.BillingOrder(ctx, &client.CheckoutRef{Subject: subject, ID: in.ID})
	})
}

// cryptoTopupIn opens a stablecoin deposit.
type cryptoTopupIn struct {
	// AmountCents is the dollars of credit to buy, in cents.
	AmountCents int64 `json:"amountCents"`
	// Network is the network to deposit on; the first the account takes when absent.
	Network string `json:"network,omitempty"`
}

// Opens a stablecoin deposit for a top-up of the caller's own wallet and answers where to
// send it, per network. Nothing is credited by this call: the wallet gets the dollars only
// when the deposit is confirmed on chain and the payment reported succeeded, once. Refused
// unless the account can take stablecoins now.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) topupCrypto(ctx context.Context, in *cryptoTopupIn) (*client.CryptoTopup, error) {
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
	return ask(ctx, org, "crypto topup", func(ctx context.Context) (*client.CryptoTopup, error) {
		return commercepeer.BillingTopupCrypto(ctx, &client.CryptoTopupIn{
			Subject: subject, AmountCents: in.AmountCents, Network: strings.TrimSpace(in.Network), IdempotencyKey: retryKey(c),
		})
	})
}

// tokenIn supplies a shared payment token.
type tokenIn struct {
	// ID is the invoice or the subscription, from the path.
	ID string `json:"id"`
	// Token is the shared payment token (spt_...) the buyer granted.
	Token string `json:"token"`
}

// Pays one of the caller's past-due renewals with a fresh shared payment token. The token
// becomes the subscription's payment method when it carries a recurring interval that
// covers the renewal, and the invoice is charged on it now.
//
// 200 is paid. 402 is declined, with `error.code` to branch on, and the subscription keeps
// the method it had. An invoice that is not the caller's is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) invoiceToken(ctx context.Context, in *tokenIn) (*client.AgentPayment, error) {
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
	return ask(ctx, org, "invoice token", func(ctx context.Context) (*client.AgentPayment, error) {
		return commercepeer.BillingInvoiceToken(ctx, &client.AgentTokenIn{Subject: subject, ID: in.ID, Token: in.Token})
	})
}

// Replaces the token one of the caller's subscriptions renews on with a fresh one that
// covers the next renewal, before the renewal is attempted, and answers what is left on it.
//
// 200 is accepted. 402 is declined, with `error.code` to branch on, and the subscription
// keeps the token it had. A subscription that is not the caller's is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) subscriptionToken(ctx context.Context, in *tokenIn) (*client.TokenSupplied, error) {
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
	return ask(ctx, org, "subscription token", func(ctx context.Context) (*client.TokenSupplied, error) {
		return commercepeer.BillingSubscriptionToken(ctx, &client.AgentTokenIn{Subject: subject, ID: in.ID, Token: in.Token})
	})
}

// wireReconcileIn credits a wire.
type wireReconcileIn struct {
	// Reference is the reference the payer was shown beside the bank details (WIRE-...).
	Reference string `json:"reference"`
	// AmountCents is what the bank received, in cents.
	AmountCents int64 `json:"amountCents"`
	// BankReference is the bank's own reference for the transfer.
	BankReference string `json:"bankReference,omitempty"`
}

// Credits the wallet that holds a wire's reference with the cents the bank received, once.
// A reference nobody was given credits nothing (404), a repeat posts nothing and answers
// `credited: false`, and a repeat for another amount is 409.
//
// SuperAdmin only. Every reconcile is on the audit trail as wire.reconciled, naming who
// attested the receipt, and recorded by commerce as a billing event.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) wireReconcile(ctx context.Context, in *wireReconcileIn) (*client.WireCredit, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	c, err := requestOf(ctx)
	if err != nil {
		return nil, err
	}
	if !principal.Super(c) {
		return nil, zip.ErrForbidden("wire reconcile: SuperAdmin required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to reconcile a wire")
	}
	by := strings.TrimSpace(c.UserEmail())
	if by == "" {
		by = strings.TrimSpace(c.User())
	}
	out, err := ask(ctx, org, "wire reconcile", func(ctx context.Context) (*client.WireCredit, error) {
		return commercepeer.BillingWireReconcile(ctx, &client.WireReconcileIn{
			Reference: in.Reference, AmountCents: in.AmountCents, BankReference: strings.TrimSpace(in.BankReference), By: by,
		})
	})
	wireAudit(c, o.s.State.trail, org, in, out, err)
	return out, err
}

// wireAudit puts one wire reconcile on the audit trail as wire.reconciled, filed under
// the org the SuperAdmin acted in: who attested which reference for how much, and whether
// it credited or was a repeat, or the refusal they were answered with. It is recorded after
// the fact and cannot fail the credit: the money has moved by then. A row the trail does not
// take is logged.
func wireAudit(c *zip.Ctx, trail *audit.Recorder, org string, in *wireReconcileIn, out *client.WireCredit, fault error) {
	if trail == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    "wire.reconciled",
		Resource:  audit.Resource{Type: "wire", ID: strings.ToUpper(strings.TrimSpace(in.Reference))},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: principal.Super(c)},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  geo.IP(c),
		RequestID: c.RequestID(),
	}
	terms := map[string]any{"amountCents": in.AmountCents, "bankReference": strings.TrimSpace(in.BankReference)}
	if out != nil {
		terms["subject"], terms["credited"] = out.Subject, out.Credited
	}
	if fault != nil {
		status := http.StatusInternalServerError
		if he, ok := fault.(*zip.HTTPError); ok {
			status = he.Status
		}
		rec.Outcome = audit.Outcome{Result: "error", Status: status, Reason: fault.Error()}
	}
	b, _ := json.Marshal(terms)
	rec.After = audit.Redact(b)
	if _, err := trail.Append(c.Context(), rec); err != nil {
		luxlog.Default().Error("a wire reconcile was not put on the audit trail", "org", org, "reference", in.Reference, "err", err)
	}
}
