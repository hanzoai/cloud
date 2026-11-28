package x402

// metered.go — the platform's own flat fees, sold over x402.
//
// A metered route charges a flat fee per act through the prepaid balance gate: one
// function invoke, one agent run, one provisioned resource. The same act is sold
// here for the same fee to a caller that pays per request, so an agent that speaks
// x402 can read the price, sign for it and be served, and an unfunded caller the
// gate refuses is told in x402's own terms what the act costs.
//
// THE PRICE IS THE ROUTE'S OWN KNOB. Each fee is read with cloud.ResourceFeeCents
// from the knob client.MeteredFee names, which is the call the product makes to
// gate and debit the same act, in the same process environment. So the rail and
// the gate cannot quote two numbers for one act, and a fee an operator sets to
// zero is free on both.
//
// THE PAYEE IS THE PLATFORM. A metered fee is Hanzo's revenue, so it is paid into
// the platform's wallet: CLOUD_X402_PLATFORM_WALLET in the org
// CLOUD_X402_PLATFORM_ORG (default hanzo), resolved through wallets like any payee.
// With no wallet named the rail sells no metered fee at all: each route keeps its
// balance gate alone and its refusal carries no challenge. That is the switch.
//
// ONE PAYMENT BUYS ONE ACT. A priced resource elsewhere is served again for a
// payment already settled, which is right for a resource that is the same thing
// every time and wrong for an act: replaying one signed fee would run a function
// as often as the header was sent. A metered fee's terms say Once, and a settled
// authorization presented again is refused as a replay.
//
// The table and the challenge are public, so a client can price an act before it
// is refused for one: GET /v1/x402/prices lists every fee for sale, and
// GET /v1/x402/prices/:product answers one fee's 402 the way its route would.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/apps/wallet"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/spec"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"
)

const (
	// envPlatformOrg names the org whose wallet is paid a metered fee.
	envPlatformOrg = "CLOUD_X402_PLATFORM_ORG"
	// envPlatformWallet names that wallet. Unset sells no metered fee.
	envPlatformWallet = "CLOUD_X402_PLATFORM_WALLET"
	// defaultPlatformOrg is the platform's own org.
	defaultPlatformOrg = "hanzo"
)

// The price table and one fee's challenge are read with no credential: a client
// prices an act before it has an account to be refused with.
func init() {
	spec.Open("/v1/x402/prices", http.MethodGet)
	spec.Open("/v1/x402/prices/:product", http.MethodGet)
}

// platformPayee is the wallet a metered fee is paid into. An empty wallet means
// the rail sells no metered fee.
func platformPayee() (org, walletID string) {
	return strings.TrimSpace(env.Or(envPlatformOrg, defaultPlatformOrg)), strings.TrimSpace(env.Or(envPlatformWallet, ""))
}

// meteredTerms prices a metered fee. metered is false for a resource that is not
// one, which the rest of the table answers; priced is false for a fee that is
// free — its knob is zero — or that nobody is paid for here, because no platform
// wallet is named, and for a name in the metered key space that no fee answers to,
// which nobody sells and the marketplace is never asked about.
func meteredTerms(resource string) (terms Terms, metered, priced bool) {
	if !client.Metered(resource) {
		return Terms{}, false, false
	}
	f, ok := client.MeteredOf(resource)
	if !ok {
		return Terms{}, true, false
	}
	var cents int64
	if f.DefaultZero {
		cents = cloud.FeeCents(f.EnvPrefix, f.Kind, 0)
	} else {
		cents = cloud.ResourceFeeCents(f.EnvPrefix, f.Kind)
	}
	org, wallet := platformPayee()
	if cents <= 0 || org == "" || wallet == "" {
		return Terms{}, true, false
	}
	return Terms{
		Amount:            money.FromCents(cents),
		RecipientOrg:      org,
		RecipientWalletID: wallet,
		Category:          client.CategoryService,
		Once:              true,
	}, true, true
}

// platformTarget resolves the platform wallet a metered fee is paid into, for
// terms that name it.
func platformTarget(s *cloud.Service[state], ctx context.Context, terms Terms) (wallet.PaymentTarget, error) {
	target, ok := payeeOf(ctx, terms.RecipientOrg, terms.RecipientWalletID)
	if !ok {
		s.Log.Error("x402: platform wallet unresolved", "org", terms.RecipientOrg, "wallet", terms.RecipientWalletID)
		return wallet.PaymentTarget{}, zip.Errorf(http.StatusServiceUnavailable, "the platform wallet is not resolvable")
	}
	return target, nil
}

// quote states a metered fee's challenge with no payer: the terms a refused call
// of its route carries, for a caller that has not been refused yet. payee is the
// platform wallet when the caller already resolved it; nil resolves it here.
func quote(s *cloud.Service[state], ctx context.Context, f client.MeteredFee, payee *wallet.PaymentTarget) (protocol.PaymentRequirements, Terms, error) {
	terms, _, priced := meteredTerms(f.Resource())
	if !priced {
		return protocol.PaymentRequirements{}, Terms{}, zip.ErrNotFound(f.Product + " is not sold over x402 here")
	}
	if payee == nil {
		target, err := platformTarget(s, ctx, terms)
		if err != nil {
			return protocol.PaymentRequirements{}, Terms{}, err
		}
		payee = &target
	}
	req, err := requirements(s.State.cfg, terms, payee.Address)
	if err != nil {
		return protocol.PaymentRequirements{}, Terms{}, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	return req, terms, nil
}

// resourceInfo describes a priced resource on its challenge: its id, and for a
// metered fee what one payment buys.
func resourceInfo(resource string) protocol.ResourceInfo {
	info := protocol.ResourceInfo{URL: resource}
	if f, ok := client.MeteredOf(resource); ok {
		info.Description, info.MimeType = f.Summary, "application/json"
	}
	return info
}

// priceRow is one fee for sale.
type priceRow struct {
	// Resource is the fee's x402 resource: what a challenge names and a payment
	// is settled for.
	Resource string `json:"resource"`
	// Product names the fee: function.invoke, agent.run, provision.<kind>.
	Product string `json:"product"`
	// Method is the method of the route the fee buys one call of.
	Method string `json:"method"`
	// Path is that route. A payment is sent there, on PAYMENT-SIGNATURE, never
	// here.
	Path string `json:"path"`
	// Summary says what one payment buys.
	Summary string `json:"summary"`
	// Price is the fee in US dollars, exactly.
	Price string `json:"price"`
	// Accepts is how the fee may be paid, as the route's challenge states it.
	Accepts []protocol.PaymentRequirements `json:"accepts"`
}

// priceTable is every metered fee x402 sells here. It is never null.
type priceTable struct {
	// X402Version is the protocol version the terms are stated in.
	X402Version int `json:"x402Version"`
	// Prices is one row per fee for sale; a fee that is free or not sold here is
	// absent.
	Prices []priceRow `json:"prices"`
}

func row(f client.MeteredFee, req protocol.PaymentRequirements, terms Terms) priceRow {
	return priceRow{
		Resource: f.Resource(), Product: f.Product, Method: f.Method, Path: f.Path,
		Summary: f.Summary, Price: terms.Amount.String(), Accepts: []protocol.PaymentRequirements{req},
	}
}

// Prices lists the flat fees this deployment sells per request over x402: one
// function invoke, one agent run, one provisioned resource of each kind. Each row
// is the route's own fee knob, paid into the platform's wallet, and carries the
// exact terms that route's 402 challenge states, so a client can sign before it
// is refused.
//
// A payment is made on the route itself: the client calls it with the signed
// PAYMENT-SIGNATURE header, is served once, and reads the receipt on
// PAYMENT-RESPONSE. The payer must be signed in, and the payment is drawn from
// its Hanzo wallet, which must cover the fee. The payment is settled only once the
// call is done and billable, so a call that fails costs nothing. A fee whose knob
// is zero is free and is not listed; with no platform wallet configured the table
// is empty.
func (o ops) prices(ctx context.Context, _ *struct{}) (*priceTable, error) {
	out := &priceTable{X402Version: protocol.Version, Prices: []priceRow{}}
	var payee *wallet.PaymentTarget // every fee pays the one platform wallet: resolved once
	for _, f := range client.MeteredFees() {
		terms, _, priced := meteredTerms(f.Resource())
		if !priced {
			continue
		}
		if payee == nil {
			target, err := platformTarget(o.s, ctx, terms)
			if err != nil {
				return nil, err
			}
			payee = &target
		}
		req, terms, err := quote(o.s, ctx, f, payee)
		if err != nil {
			return nil, err
		}
		out.Prices = append(out.Prices, row(f, req, terms))
	}
	return out, nil
}

// productRef names one fee by its product.
type productRef struct {
	// Product is the fee's product name, e.g. function.invoke or provision.sql.
	Product string `json:"product"`
}

// quoted is one fee's challenge, as data.
type quoted struct {
	// Error says the fee is paid on its route, and how.
	Error string `json:"error"`
	// Fee is the fee's row, as the price table lists it.
	Fee priceRow `json:"fee"`
}

// Price answers one fee's x402 challenge: always 402, with the PAYMENT-REQUIRED
// header the fee's route sends an unfunded caller, and the same row the price
// table lists as the body.
//
// It sells nothing itself. A payment sent here is never settled; it is made on
// the route the row names, where it buys one call. An unknown product, or one
// not sold here, is 404.
func (o ops) price(ctx context.Context, in *productRef) (*quoted, error) {
	f, ok := client.MeteredOf(client.MeteredFee{Product: strings.TrimSpace(in.Product)}.Resource())
	if !ok {
		return nil, zip.ErrNotFound("no metered fee named " + in.Product)
	}
	req, terms, err := quote(o.s, ctx, f, nil)
	if err != nil {
		return nil, err
	}
	msg := "pay on " + f.Method + " " + f.Path + " with PAYMENT-SIGNATURE"
	if c, live := cloud.Request(ctx); live {
		c.SetHeader(protocol.HeaderPaymentRequired, encode(&protocol.PaymentRequired{
			X402Version: protocol.Version,
			Error:       msg,
			Resource:    resourceInfo(f.Resource()),
			Accepts:     []protocol.PaymentRequirements{req},
		}))
	}
	return &quoted{Error: msg, Fee: row(f, req, terms)}, nil
}
