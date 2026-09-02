package billing

// rails.go serves the two top-up rails of /v1/billing that are not a card:
// crypto custody and bank wire.
//
// Neither moves money. The crypto endpoint issues a per-payer custody address and
// the wire endpoint renders the receiving brand's bank details; balance changes
// only when a chain confirmation or a bank receipt is settled, in the process
// that holds the ledger. So there is no charge here and nothing to screen.
//
// What both DO carry is the payer, into something a stranger acts on later — an
// address that credits one wallet, a reference that names one payer on a bank
// statement. It is resolved here, from the validated principal, and a body value
// can never steer it. A subject that was not the caller's would credit the wrong
// account with money that really arrived, which is the one failure on this file
// that cannot be undone by retrying.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountRails registers the crypto and wire routes. Called from routes.
func mountRails(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/billing/crypto/options", o.cryptoOptions)
	zip.Post(zapp, "/v1/billing/crypto/deposit", o.mintCryptoDeposit)
	zip.Get(zapp, "/v1/billing/crypto/deposit/:id", o.cryptoDeposit)
	zip.Get(zapp, "/v1/billing/wire", o.wire)
}

// Answers which chains and tokens the crypto rail accepts — what an asset picker
// renders.
//
// It is the intersection of two live facts rather than a configured list: an
// asset appears only if something is WATCHING it and the custody processor
// supports it. An address nobody watches credits nobody, so offering one would
// take a customer's money and lose it. A rail with nothing armed answers 503,
// not an empty menu — "no rail" and "no assets" are different, and only one of
// them means try again later.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) cryptoOptions(ctx context.Context, _ *cloud.Unit) (*plane.CryptoOptions, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "crypto options", func(ctx context.Context) (*plane.CryptoOptions, error) {
		return commercepeer.BillingCryptoOptions(ctx)
	})
}

// Issues a deposit address the caller can send crypto to, on the asset they ask
// for.
//
// The address credits the CALLER'S own wallet and nobody else's: the payer is
// the validated principal, never a body value. Asking again reuses the caller's
// open intent rather than minting a second address, so a refresh cannot spray
// key generations — and a payer who sent to the address they saw earlier is
// still credited.
//
// No balance moves here. The chain watcher credits on real confirmations, so
// what comes back is an address and a status, not a receipt.
//
// An asset this rail cannot mint on is 400 — ask for another. A rail that is
// shut for that asset is 503 — nothing sent now can be credited.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) mintCryptoDeposit(ctx context.Context, in *cryptoAsset) (*plane.CryptoDeposit, error) {
	if err := account.CSRF(ctx); err != nil {
		return nil, err
	}
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "crypto deposit", func(ctx context.Context) (*plane.CryptoDeposit, error) {
		return commercepeer.BillingCryptoMint(ctx, &plane.CryptoMintIn{
			Payer: subject, Chain: in.Chain, Token: in.Token, AmountCents: in.AmountCents,
		})
	})
}

// cryptoAsset is which asset a deposit address is wanted for. There is no payer
// field: the credited wallet is the caller's own, resolved server-side, so a
// body value could only ever name a wallet the caller does not hold.
type cryptoAsset struct {
	// Chain is the network to receive on. Empty takes the rail's default.
	Chain string `json:"chain,omitempty"`
	// Token is the asset on that chain. Empty takes the chain's native one.
	Token string `json:"token,omitempty"`
	// AmountCents is what the payer intends to send, for the record. Optional —
	// the credit is what actually arrives, never what was announced.
	AmountCents int64 `json:"amountCents,omitempty"`
}

// Reads one of the caller's own deposit intents back — pending, confirming, or
// succeeded.
//
// An intent belonging to another payer answers 404, exactly as an id that names
// nothing, so a guessed id cannot confirm that somebody else's deposit exists.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) cryptoDeposit(ctx context.Context, in *depositRef) (*plane.CryptoDeposit, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "crypto deposit", func(ctx context.Context) (*plane.CryptoDeposit, error) {
		return commercepeer.BillingCryptoDeposit(ctx, &plane.CryptoDepositIn{Payer: subject, ID: in.ID})
	})
}

// depositRef names one deposit intent.
type depositRef struct {
	// ID is the deposit intent id.
	ID string `json:"id"`
}

// Answers where to send a wire top-up: the receiving bank details, with the
// caller's own payment reference.
//
// The account is the SERVING BRAND'S — resolved from the host the customer is
// paying on, so paying on one brand never shows another's bank — and the
// reference carries the caller's billing key, which is how an arriving wire
// names who it credits. Nothing mints here; a receipt is settled by an operator
// once the bank confirms it.
//
// It is all-or-nothing: no configured account is 503 rather than a partial form,
// because nobody can wire to three fields out of five.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) wire(ctx context.Context, _ *cloud.Unit) (*plane.WireInstructions, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	// The HOST, not a brand: the table that maps one to the other is the store's,
	// and X-Forwarded-Host is what the customer actually typed — the ingress
	// overwrites Host on the way in, so reading that alone would resolve every
	// brand to the cluster's own name.
	host := ""
	if c, ok := cloud.Request(ctx); ok {
		if fwd := strings.TrimSpace(c.Header("X-Forwarded-Host")); fwd != "" {
			host = fwd
		} else {
			host = c.Fiber().Hostname()
		}
	}
	return ask(ctx, org, "wire", func(ctx context.Context) (*plane.WireInstructions, error) {
		return commercepeer.BillingWire(ctx, &plane.WireIn{Host: host, Payer: subject})
	})
}
