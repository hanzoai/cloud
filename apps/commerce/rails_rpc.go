// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// rails_rpc.go — the two top-up rails that are not a card: crypto custody and
// bank wire, over the internal plane.
//
// Neither mints anything here. The crypto op issues a per-payer custody address
// and the wire op renders the receiving brand's own bank details; balance moves
// only when a chain confirmation or a bank receipt is settled, elsewhere. So
// these are reads and an address issue, and they carry no risk screen — there is
// no charge to screen.
//
// WHAT THEY DO CARRY is the payer. Both put the caller's billing subject into
// something a stranger will later act on — a custody address that credits one
// wallet, a payment reference that names one payer on a bank statement — so a
// subject that was not the caller's would credit the wrong account with money
// that really arrived. The door resolves it; these never read one off a body.

import (
	"context"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposeRails publishes the crypto and wire ops. Mount calls it.
func exposeRails() {
	zip.Post[struct{}, plane.CryptoOptions](cloud.Plane(), "/billing/crypto/options", planeCryptoOptions,
		zip.WithOperationID(plane.BillingCryptoOptions),
		zip.WithSummary("Chains and tokens the crypto rail accepts"))
	zip.Post[plane.CryptoMintIn, plane.CryptoDeposit](cloud.Plane(), "/billing/crypto/mint", planeCryptoMint,
		zip.WithOperationID(plane.BillingCryptoMint),
		zip.WithSummary("Issue a deposit address for one payer and asset"))
	zip.Post[plane.CryptoDepositIn, plane.CryptoDeposit](cloud.Plane(), "/billing/crypto/deposit", planeCryptoDeposit,
		zip.WithOperationID(plane.BillingCryptoDeposit),
		zip.WithSummary("Read one deposit intent back"))
	zip.Post[plane.WireIn, plane.WireInstructions](cloud.Plane(), "/billing/wire", planeWire,
		zip.WithOperationID(plane.BillingWire),
		zip.WithSummary("Receiving bank details for a wire top-up"))
}

// Answers which chains and tokens the crypto rail accepts.
//
// It is the intersection of two facts, not a config list: an asset is offered
// only if something is WATCHING it and the custody processor supports it. An
// address nobody watches credits nobody, so offering one would take a customer's
// money and lose it. A rail with nothing configured is 503, not an empty menu.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCryptoOptions(ctx context.Context, _ *struct{}) (*plane.CryptoOptions, error) {
	out, err := commercebilling.GetCryptoOptions(ctx)
	if err != nil {
		return nil, zip.Errorf(503, "crypto deposits not configured")
	}
	return &plane.CryptoOptions{Chains: out.Chains, Tokens: out.Tokens}, nil
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
func planeCryptoMint(ctx context.Context, in *plane.CryptoMintIn) (*plane.CryptoDeposit, error) {
	org, err := payingOrg(ctx, "crypto deposit")
	if err != nil {
		return nil, err
	}
	d, derr := commercebilling.CreateCryptoDeposit(ctx, org, in.Payer, in.Chain, in.Token, in.AmountCents)
	if derr != nil {
		switch {
		case commercebilling.IsDepositUnsupported(derr):
			return nil, zip.Errorf(400, "%v", derr)
		case commercebilling.IsDepositRefused(derr):
			// 503 rather than 502, and the difference is what a customer reads:
			// an edge replaces an origin 502 with its own interstitial, so the
			// message never arrives. It is also the honest code — the rail is
			// unavailable, nothing here is a broken gateway.
			return nil, zip.Errorf(503, "%v", derr)
		default:
			return nil, zip.Errorf(500, "failed to record deposit intent")
		}
	}
	return cryptoRow(d), nil
}

// Reads one deposit intent back — pending, confirming, or succeeded.
//
// Caller-scoped: an intent belonging to another payer answers as a MISS, exactly
// as an id that names nothing, so a guessed id cannot confirm that somebody
// else's deposit exists.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCryptoDeposit(ctx context.Context, in *plane.CryptoDepositIn) (*plane.CryptoDeposit, error) {
	org, err := payingOrg(ctx, "crypto deposit")
	if err != nil {
		return nil, err
	}
	d, derr := commercebilling.GetCryptoDeposit(ctx, org, in.Payer, in.ID)
	if derr != nil {
		return nil, zip.Errorf(404, "deposit not found")
	}
	return cryptoRow(d), nil
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
func planeWire(ctx context.Context, in *plane.WireIn) (*plane.WireInstructions, error) {
	if _, err := payingOrg(ctx, "wire"); err != nil {
		return nil, err
	}
	w, werr := commercebilling.WireFor(ctx, in.Host, in.Payer, kmsFrom(ctx))
	if werr != nil {
		return nil, zip.Errorf(503, "wire transfer not configured")
	}
	return &plane.WireInstructions{
		BankName: w.BankName, BankAddress: w.BankAddress,
		AccountNumber: w.AccountNumber, RoutingNumber: w.RoutingNumber,
		SwiftCode: w.SwiftCode, IBAN: w.IBAN,
		AccountName: w.AccountName, Memo: w.Memo, Reference: w.Reference,
	}, nil
}

// cryptoRow moves one intent onto the wire. AddressTag keeps its omitempty and
// keeps its "0": the tag is decimal text, so the first one ever issued is not
// empty, and a payment to a pooled address with no tag names nobody.
func cryptoRow(d commercebilling.CryptoDeposit) *plane.CryptoDeposit {
	return &plane.CryptoDeposit{
		ID: d.ID, Status: d.Status, Chain: d.Chain, Token: d.Token,
		DepositAddress: d.DepositAddress, AddressTag: d.AddressTag,
		ExpiresAt: d.ExpiresAt,
	}
}
