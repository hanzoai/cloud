// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// agent_rpc.go — THE HEADLESS PURCHASE SURFACE an agent buys through: what is for sale,
// a checkout opened, changed and completed in JSON, the order it confirms, a stablecoin
// top-up, a shared payment token for a past-due invoice or a subscription, and a bank
// wire reconciled to a wallet.
//
// Commerce owns all of it: the price, the charge, the deposit and the credit. These ops
// are the plane's address for commerce's cores and hold no rule of their own. A buyer's
// op answers for the subject the billing endpoint resolved from the caller's credential,
// never one the request named. The catalog and the discovery document are public; the
// wire reconcile answers the platform's SuperAdmin only.
//
// The status of a completion rides in the answer, so a 202 that waits on the buyer and a
// 402 that refuses reach the browser as commerce chose them.

import (
	"context"
	"net/http"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/checkout"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/hanzoai/commerce/payment/processor"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeAgent publishes the headless purchase ops. Mount calls it.
func exposeAgent() {
	cloud.Plane().Post("/billing/catalog", planeCatalog,
		zip.WithOperationID(client.BillingCatalog),
		zip.WithSummary("What is for sale in dollars and how it is paid for"))
	cloud.Plane().Post("/billing/discovery", planeDiscovery,
		zip.WithOperationID(client.BillingDiscovery),
		zip.WithSummary("Where the purchase surface is and what it speaks"))
	cloud.Plane().Post("/billing/checkout/open", planeCheckoutOpen,
		zip.WithOperationID(client.BillingCheckoutOpen),
		zip.WithSummary("Open a checkout for one item"))
	cloud.Plane().Post("/billing/checkout/update", planeCheckoutUpdate,
		zip.WithOperationID(client.BillingCheckoutUpdate),
		zip.WithSummary("Replace the item of an open checkout"))
	cloud.Plane().Post("/billing/checkout", planeCheckout,
		zip.WithOperationID(client.BillingCheckout),
		zip.WithSummary("Where one of the buyer's checkouts stands"))
	cloud.Plane().Post("/billing/checkout/complete", planeCheckoutComplete,
		zip.WithOperationID(client.BillingCheckoutComplete),
		zip.WithSummary("Pay a checkout with a shared payment token or a stablecoin deposit"))
	cloud.Plane().Post("/billing/order", planeOrder,
		zip.WithOperationID(client.BillingOrder),
		zip.WithSummary("The order a completed checkout confirms"))
	cloud.Plane().Post("/billing/topup/crypto", planeTopupCrypto,
		zip.WithOperationID(client.BillingTopupCrypto),
		zip.WithSummary("Open a stablecoin deposit for a top-up"))
	cloud.Plane().Post("/billing/invoice/token", planeInvoiceToken,
		zip.WithOperationID(client.BillingInvoiceToken),
		zip.WithSummary("Pay a past-due renewal with a fresh shared payment token"))
	cloud.Plane().Post("/billing/subscription/token", planeSubscriptionToken,
		zip.WithOperationID(client.BillingSubscriptionToken),
		zip.WithSummary("Replace the token a subscription renews on before a renewal is due"))
	cloud.Plane().Post("/billing/wire/reconcile", planeWireReconcile,
		zip.WithOperationID(client.BillingWireReconcile),
		zip.WithSummary("Credit a wallet with a wire the bank received"))
}

// catalogOrg is the org whose processors the catalog lists: the caller's when it has one,
// else the brand the host names. A caller with no credential is served, and the brand is
// the host's, never a header's.
func catalogOrg(ctx context.Context, host string) (*organization.Organization, error) {
	if _, err := callerOrg(ctx, "catalog"); err == nil {
		return orgOf(ctx, "catalog")
	}
	if e := currentEmbedded(); e == nil || e.App() == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "catalog: commerce is not co-resident in this process")
	}
	org, err := commerceorg.Resolve(ctx, checkout.BrandSlugForHost(host))
	if err != nil {
		return nil, nil // a host no brand serves still has a catalog; it lists no processor
	}
	return org, nil
}

// Answers what is for sale in dollars and how it can be paid for: the plans on sale on the
// host's brand, the credit packs, and the payment types the org's processors take now,
// with the networks a stablecoin deposit may use.
//
// Public: an agent reads it before it has a credential. A caller with one is answered for
// its own org's processors.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCatalog(ctx context.Context, in *client.CatalogIn) (*client.Rendered, error) {
	org, err := catalogOrg(ctx, in.Host)
	if err != nil {
		return nil, err
	}
	out, err := commercebilling.ReadCatalog(ctx, org, in.Host)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the catalog is unavailable")
	}
	out.Links = billingLinks(out.Links)
	return rendered(out)
}

func billingLinks(links map[string]string) map[string]string {
	links = discoveryLinks(links)
	links["cryptoOptions"] = "/v1/billing/crypto/options"
	links["cryptoDeposit"] = "/v1/billing/crypto/deposit"
	links["cryptoDepositStatus"] = "/v1/billing/crypto/deposit/{id}"
	return links
}

// Answers where the purchase surface is and what it speaks: its version, the paths of its
// parts, the checkout's verbs in order, the payment types a checkout completes with, and
// how a subscription is renewed by an agent.
//
// Public: an agent reads it before it has a credential.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeDiscovery(_ context.Context, _ *cloud.Unit) (*client.Rendered, error) {
	out := commercebilling.Discovery{
		Version: "1", Currency: "usd",
		// The agent checkout protocols this deployment serves, by name, beside its own
		// paths: none until the operator configures one (agentic.go).
		Links: billingLinks(map[string]string{
			"catalog":   "/v1/billing/catalog",
			"discovery": "/v1/billing/discovery",
			"checkouts": "/v1/billing/checkouts",
			"orders":    "/v1/billing/orders",
		}),
		Lifecycle: []string{"POST /v1/billing/checkouts", "PATCH /v1/billing/checkouts/{id}", "POST /v1/billing/checkouts/{id}/complete", "GET /v1/billing/orders/{id}"},
		Payment:   []string{"shared_payment_token", "stablecoin"},
		Renewal:   "A subscription renews on a shared payment token whose usage limits carry a recurring interval and a maximum at least the plan's renewal amount. A renewal the token cannot cover leaves the subscription past due and announces renewal.action_required with the path to supply a fresh token: POST /v1/billing/invoices/{id}/agent-token.",
	}
	if processor.DisabledByPolicy(processor.Stripe) {
		out.Payment = []string{}
		out.Renewal = ""
	}
	return rendered(out)
}

// itemsOf is the plane's items as commerce's.
func itemsOf(items []client.AgentItem) ([]commercebilling.ItemIn, error) {
	out, err := into[[]commercebilling.ItemIn](items)
	if err != nil {
		return nil, err
	}
	return *out, nil
}

// Opens a checkout for exactly one item, USD credit or a plan's first period, and answers
// it priced as the sale prices it. Nothing is charged: a checkout is paid by completing it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCheckoutOpen(ctx context.Context, in *client.CheckoutOpenIn) (*client.AgentCheckout, error) {
	org, err := orgOf(ctx, "checkout")
	if err != nil {
		return nil, err
	}
	items, err := itemsOf(in.Items)
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.CreateCheckout(ctx, org, in.Subject, in.Host, items)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.AgentCheckout](v)
}

// Replaces the item of a checkout that is still open, or failed, and prices it again. A
// checkout that is pending or complete can no longer be changed (409).
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCheckoutUpdate(ctx context.Context, in *client.CheckoutUpdateIn) (*client.AgentCheckout, error) {
	org, err := orgOf(ctx, "checkout update")
	if err != nil {
		return nil, err
	}
	items, err := itemsOf(in.Items)
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.UpdateCheckout(ctx, org, in.Subject, in.Host, in.ID, items)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.AgentCheckout](v)
}

// Answers where one of the subject's own checkouts stands. One waiting on a stablecoin
// deposit is completed here once the deposit has been credited. A checkout that is not the
// subject's is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCheckout(ctx context.Context, in *client.CheckoutRef) (*client.AgentCheckout, error) {
	org, err := orgOf(ctx, "checkout read")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.ReadCheckout(ctx, org, in.Subject, in.ID)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.AgentCheckout](v)
}

// Pays a checkout with a shared payment token the buyer granted the agent, or with a
// stablecoin deposit, and answers the checkout with the status the attempt earned: 200
// complete, 202 waiting on the buyer with the next step, and a refusal under its own
// status with a code to branch on. The charge is bound to the checkout, the buyer and the
// idempotency key, so completing twice is one purchase.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCheckoutComplete(ctx context.Context, in *client.CheckoutCompleteIn) (*client.CheckoutCompletion, error) {
	org, err := orgOf(ctx, "checkout complete")
	if err != nil {
		return nil, err
	}
	pay, err := into[commercebilling.PaymentIn](in.Payment)
	if err != nil {
		return nil, err
	}
	done := commercebilling.CompleteCheckout(ctx, org, in.Subject, in.Host, in.ID, in.Key, *pay)
	v, err := into[client.AgentCheckout](done.View)
	if err != nil {
		return nil, err
	}
	return &client.CheckoutCompletion{Status: done.Status, Checkout: *v}, nil
}

// Answers the order a completed checkout confirms. A checkout that is not complete has no
// order yet (404), and one that is not the subject's is 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeOrder(ctx context.Context, in *client.CheckoutRef) (*client.AgentOrder, error) {
	org, err := orgOf(ctx, "order")
	if err != nil {
		return nil, err
	}
	o, f := commercebilling.ReadOrder(ctx, org, in.Subject, in.ID)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.AgentOrder](o)
}

// Opens a stablecoin deposit for a top-up and answers where to send it, per network.
// Nothing is credited here: the wallet gets the dollars only when the deposit is confirmed
// on chain and the payment reported succeeded, once. Refused unless the account can take
// stablecoins now.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTopupCrypto(ctx context.Context, in *client.CryptoTopupIn) (*client.CryptoTopup, error) {
	org, err := orgOf(ctx, "crypto topup")
	if err != nil {
		return nil, err
	}
	out, f := commercebilling.OpenCryptoTopup(ctx, org, commercebilling.CryptoTopupIn{
		Subject: in.Subject, AmountCents: in.AmountCents, Network: in.Network, IdempotencyKey: in.IdempotencyKey,
	})
	if f != nil {
		return nil, fail(f)
	}
	return into[client.CryptoTopup](out)
}

// Pays a past-due renewal with a fresh shared payment token. The token becomes the
// subscription's payment method when it carries a recurring interval that covers the
// renewal, and the invoice is charged on it now. A token that cannot pay it, or a card
// that declines, answers the reason under `declined`, and the subscription keeps the
// method it had.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeInvoiceToken(ctx context.Context, in *client.AgentTokenIn) (*client.AgentPayment, error) {
	org, err := orgOf(ctx, "invoice token")
	if err != nil {
		return nil, err
	}
	out, f := commercebilling.PayInvoiceWithToken(ctx, org, in.Subject, in.ID, in.Token)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.AgentPayment](out)
}

// Replaces the token a subscription renews on with a fresh one that covers the next
// renewal, before the renewal is attempted, and answers what is left on it. A token that
// does not cover the renewal is declined with a code, and the subscription keeps the one it had.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeSubscriptionToken(ctx context.Context, in *client.AgentTokenIn) (*client.TokenSupplied, error) {
	org, err := orgOf(ctx, "subscription token")
	if err != nil {
		return nil, err
	}
	room, aerr, f := commercebilling.SupplySubscriptionToken(ctx, org, in.Subject, in.ID, in.Token)
	switch {
	case f != nil:
		return nil, fail(f)
	case aerr != nil:
		return into[client.TokenSupplied](map[string]any{"status": "declined", "error": aerr})
	}
	return into[client.TokenSupplied](map[string]any{"status": "ok", "token": room})
}

// Credits the wallet that holds a wire's reference with the cents the bank received,
// once. The reference is the one the payer was shown; a reference nobody was given credits
// nothing (404), a repeat posts nothing, and a repeat for another amount is refused (409).
// Every reconcile is recorded as a billing event, naming who attested the receipt.
//
// SuperAdmin only.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeWireReconcile(ctx context.Context, in *client.WireReconcileIn) (*client.WireCredit, error) {
	if err := platformOnly(ctx, "wire reconcile"); err != nil {
		return nil, err
	}
	if e := currentEmbedded(); e == nil || e.App() == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "wire reconcile: commerce is not co-resident in this process")
	}
	out, f := commercebilling.ReconcileWire(ctx, in.Reference, in.AmountCents, in.BankReference, in.By)
	if f != nil {
		return nil, fail(f)
	}
	return into[client.WireCredit](out)
}
