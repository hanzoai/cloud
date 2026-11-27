// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// rails_rpc.go — the top-up rails that are not a card: crypto custody, USDC
// through Circle Gateway, and bank wire, over the internal plane.
//
// The crypto op issues a per-payer custody address and the wire op renders the
// receiving brand's own bank details; balance moves only when a chain
// confirmation or a bank receipt is settled, elsewhere. The Gateway op credits,
// and only what Circle Gateway has LOCKED of the payer's own USDC — there is no
// card behind it to decline or claw back, so none of these carries a risk
// screen.
//
// WHAT THEY ALL CARRY is the payer. Each puts the caller's billing subject into
// something that decides whose money it is — a custody address that credits one
// wallet, a payment reference that names one payer on a bank statement, a
// Gateway nonce issued to one payer — so a subject that was not the caller's
// would credit the wrong account with money that really arrived. The endpoint
// resolves it; these never read one off a body.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/billing/gateway"
	"github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeRails publishes the crypto and wire ops. Mount calls it.
func exposeRails() {
	cloud.Plane().Post("/billing/crypto/options", planeCryptoOptions,
		zip.WithOperationID(client.BillingCryptoOptions),
		zip.WithSummary("Chains and tokens the crypto rail accepts"))
	cloud.Plane().Post("/billing/crypto/mint", planeCryptoMint,
		zip.WithOperationID(client.BillingCryptoMint),
		zip.WithSummary("Issue a deposit address for one payer and asset"))
	cloud.Plane().Post("/billing/crypto/deposit", planeCryptoDeposit,
		zip.WithOperationID(client.BillingCryptoDeposit),
		zip.WithSummary("Read one deposit intent back"))
	cloud.Plane().Post("/billing/wire", planeWire,
		zip.WithOperationID(client.BillingWire),
		zip.WithSummary("Receiving bank details for a wire top-up"))
	cloud.Plane().Post("/billing/gateway/terms", planeGatewayTerms,
		zip.WithOperationID(client.BillingGatewayTerms),
		zip.WithSummary("Payment terms for a USDC top-up through Circle Gateway"))
	cloud.Plane().Post("/billing/gateway/submit", planeGatewaySubmit,
		zip.WithOperationID(client.BillingGatewaySubmit),
		zip.WithSummary("Take one payer's signed USDC payment to the treasury"))
	cloud.Plane().Post("/billing/gateway/read", planeGatewayRead,
		zip.WithOperationID(client.BillingGatewayRead),
		zip.WithSummary("Read one USDC top-up back"))
}

// Answers which chains and tokens the crypto rail accepts, and the networks a
// USDC payment may be made on through Circle Gateway.
//
// It is the intersection of two facts, not a config list: an asset is offered
// only if something is WATCHING it and the custody processor supports it. An
// address nobody watches credits nobody, so offering one would take a customer's
// money and lose it. With neither rail open it is 503, not an empty menu.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCryptoOptions(ctx context.Context, _ *cloud.Unit) (*client.CryptoOptions, error) {
	return nativeCryptoOptions(ctx)
}

// Issues a per-payer custody deposit address for one asset.
//
// It reuses the payer's own OPEN intent rather than minting a second, so a
// refresh cannot spray key generations at the signer fleet — and so a payer who
// sent to the address they were shown five minutes ago is still credited.
//
// Two refusals, and they mean opposite things to whoever is asking. An asset
// this rail cannot mint on is 400: ask for a different one. A rail that is SHUT
// for that asset is 503: nothing you send now can be credited, come back later.
// Collapsing them would tell a customer to retry something that will never work,
// or to give up on something that will.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCryptoMint(ctx context.Context, in *client.CryptoMintIn) (*client.CryptoDeposit, error) {
	org, err := orgOf(ctx, "crypto deposit")
	if err != nil {
		return nil, err
	}
	return nativeCryptoMint(ctx, org, in)
}

// Reads one deposit intent back — pending, confirming, or succeeded.
//
// Caller-scoped: an intent belonging to another payer answers as a MISS, exactly
// as an id that names nothing, so a guessed id cannot confirm that somebody
// else's deposit exists.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCryptoDeposit(ctx context.Context, in *client.CryptoDepositIn) (*client.CryptoDeposit, error) {
	org, err := orgOf(ctx, "crypto deposit")
	if err != nil {
		return nil, err
	}
	return nativeCryptoDeposit(ctx, org, in.Payer, in.ID)
}

// Renders the receiving bank details for a wire top-up.
//
// The account is the SERVING BRAND'S own — resolved from the host the customer
// is paying on, never from the caller — and the caller's billing key is rendered
// into the payment reference, which is how an arriving wire names who it
// credits. Nothing mints here; ops settle a receipt with the admin credit verb
// once the bank confirms.
//
// Every failure is one answer, because they are one fact to a customer: no brand
// row, no secrets and no account all mean there is nowhere to send the money. A
// half-filled form is not an alternative.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeWire(ctx context.Context, in *client.WireIn) (*client.WireInstructions, error) {
	org, err := orgOf(ctx, "wire")
	if err != nil {
		return nil, err
	}
	w, werr := commercebilling.ReadWireInstructions(ctx, org, in.Host, in.Payer)
	if werr != nil {
		if commercebilling.IsWireUnconfigured(werr) {
			return nil, zip.Errorf(503, "wire transfer not configured")
		}
		return nil, zip.Errorf(502, "wire instructions could not be read")
	}
	return &client.WireInstructions{
		BankName: w.BankName, BankAddress: w.BankAddress,
		AccountNumber: w.AccountNumber, RoutingNumber: w.RoutingNumber,
		SwiftCode: w.SwiftCode, IBAN: w.IBAN,
		AccountName: w.AccountName, Memo: w.Memo, Reference: w.Reference,
	}, nil
}

// cryptoRow moves one intent onto the wire. AddressTag keeps its omitempty and
// keeps its "0": the tag is decimal text, so the first one ever issued is not
// empty, and a payment to a pooled address with no tag names nobody.
func cryptoRow(d commercebilling.CryptoDeposit) *client.CryptoDeposit {
	return &client.CryptoDeposit{
		ID: d.ID, Status: d.Status, Chain: d.Chain, Token: d.Token,
		DepositAddress: d.DepositAddress, AddressTag: d.AddressTag,
		ExpiresAt:   d.ExpiresAt,
		AmountCents: d.AmountCents, MinimumCents: d.MinimumCents, FeeCents: d.FeeCents,
		RequiredConfirmations: d.RequiredConfirmations, Confirmations: d.Confirmations,
		CreditedCents:  d.CreditedCents,
		Instructions:   instructions(d),
		PlanID:         d.PlanID,
		SubscriptionID: d.SubscriptionID,
	}
}

// instructions is the deposit as one sentence a payer, or an agent paying, can act
// on: the token, the chain, the address and its tag, what credits, and when.
func instructions(d commercebilling.CryptoDeposit) string {
	token := strings.ToUpper(d.Token)
	if d.MinimumCents == 0 {
		return fmt.Sprintf("Nothing is watching %s on %s right now, so nothing sent to %s is credited until it is watched again; GET /v1/billing/crypto/options lists what is.",
			token, d.Chain, d.DepositAddress)
	}
	to := d.DepositAddress
	if d.AddressTag != "" {
		to += " with destination tag " + d.AddressTag + " (a payment without it is credited to nobody)"
	}
	terms := "deposits under " + dollars(d.MinimumCents) + " credit nothing"
	if d.FeeCents > 0 {
		terms += " and " + dollars(d.FeeCents) + " is deducted from each"
	}
	return fmt.Sprintf("Send only %s on %s to %s; %s. It is credited to your balance as cash once the transfer is %d blocks deep, and status moves pending → confirming → succeeded.",
		token, d.Chain, to, terms, d.RequiredConfirmations)
}

// dollars renders cents as $d.cc.
func dollars(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) }

// Answers the terms a payer signs a USDC top-up against: the treasury to pay,
// the networks and USDC contracts, the EIP-712 domain and types, the limits,
// and a nonce issued to this payer and no other.
//
// The payer is resolved by the endpoint and arrives here as a value; the nonce
// binds it, so a payment signed against these terms cannot be presented by
// anyone else.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeGatewayTerms(ctx context.Context, in *client.GatewayTermsIn) (*client.GatewayTerms, error) {
	org, err := orgOf(ctx, "gateway terms")
	if err != nil {
		return nil, err
	}
	t, err := gateway.Default().Terms(ctx, org, in.Payer)
	if err != nil {
		return nil, gatewayErr(err)
	}
	// The map is built first and marshalled into the reply's Types: a Go map
	// cannot cross the plane (zapenc refuses it at the peer hop), raw JSON
	// can. encoding/json orders a map's keys, so the bytes are deterministic,
	// and the pay page receives the same EIP-712 types object it always has.
	types := map[string][]client.GatewayField{}
	for name, fields := range t.Types {
		for _, f := range fields {
			types[name] = append(types[name], client.GatewayField{Name: f.Name, Type: f.Type})
		}
	}
	raw, err := json.Marshal(types)
	if err != nil {
		return nil, gatewayErr(err)
	}
	out := &client.GatewayTerms{
		Version: t.Version, Scheme: t.Scheme, PayTo: t.PayTo, Nonce: t.Nonce, Salt: t.Salt,
		MinCents: t.MinCents, MaxCents: t.MaxCents, MaxValidity: t.MaxValidity, Primary: t.Primary,
		Types: json.RawMessage(raw), Accepts: []client.GatewayAccept{},
	}
	for _, a := range t.Accepts {
		out.Accepts = append(out.Accepts, client.GatewayAccept{
			Network: a.Network, Asset: a.Asset, Symbol: a.Symbol, Decimals: a.Decimals,
			MinAmount: a.MinAmount, MaxAmount: a.MaxAmount, MinValidity: a.MinValidity,
			Domain: client.GatewayDomain{
				Name: a.Domain.Name, Version: a.Domain.Version, ChainID: a.Domain.ChainID,
				VerifyingContract: a.Domain.VerifyingContract,
			},
		})
	}
	return out, nil
}

// Takes one payer's signed USDC payment to the treasury, and answers what
// became of it.
//
// accepted means Circle Gateway LOCKED the payer's USDC and the payer is
// credited (or, past the unconfirmed-credit cap, will be when Gateway confirms
// it); pending means the outcome is not known yet and resolves on its own; a
// refusal carries Gateway's reason. Presenting the same payment again is the
// same payment.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeGatewaySubmit(ctx context.Context, in *client.GatewaySubmitIn) (*client.GatewayPayment, error) {
	org, err := orgOf(ctx, "gateway payment")
	if err != nil {
		return nil, err
	}
	v, err := gateway.Default().Submit(ctx, org, in.Payer, gateway.Submission{
		Network: in.Network, Salt: in.Salt, Signature: in.Signature,
		Authorization: x402.Authorization{
			From: in.Authorization.From, To: in.Authorization.To, Value: in.Authorization.Value,
			ValidAfter: in.Authorization.ValidAfter, ValidBefore: in.Authorization.ValidBefore,
			Nonce: in.Authorization.Nonce,
		},
	})
	if err != nil {
		return nil, gatewayErr(err)
	}
	return gatewayRow(v), nil
}

// Reads one USDC top-up back. A payment belonging to another payer answers as
// a MISS, exactly as an id that names nothing.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeGatewayRead(ctx context.Context, in *client.GatewayReadIn) (*client.GatewayPayment, error) {
	org, err := orgOf(ctx, "gateway payment")
	if err != nil {
		return nil, err
	}
	v, err := gateway.Default().Get(ctx, org, in.Payer, in.ID)
	if err != nil {
		return nil, gatewayErr(err)
	}
	return gatewayRow(v), nil
}

// gatewayErr carries a rail refusal across with its own status and reason. An
// error that is not a refusal is an internal failure: its text stays here.
func gatewayErr(err error) error {
	reason := gateway.ReasonOf(err)
	if reason == "" {
		return zip.Errorf(http.StatusInternalServerError, "the payment could not be recorded; present the same payment again")
	}
	return &zip.HTTPError{Status: gateway.StatusOf(err), Code: reason, Msg: err.Error()}
}

func gatewayRow(v gateway.View) *client.GatewayPayment {
	return &client.GatewayPayment{
		ID: v.ID, Status: v.Status, Network: v.Network, From: v.From, Value: v.Value,
		AmountCents: v.AmountCents, Credited: v.Credited, Reason: v.Reason,
		TxHash: v.TxHash, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}
