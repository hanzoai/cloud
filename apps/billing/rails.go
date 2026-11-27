package billing

// rails.go serves the top-up rails of /v1/billing that are not a card: crypto
// custody, USDC through Circle Gateway, and bank wire.
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
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/sdk"
)

// mountRails registers the crypto and wire routes. Called from routes.
func mountRails(app cloud.Router, o ops) {
	g := app.Group("/v1/billing")
	g.Get("/crypto/options", o.cryptoOptions)
	g.Post("/crypto/deposit", o.mintCryptoDeposit)
	g.Get("/crypto/deposit/:id", o.cryptoDeposit)
	g.Get("/crypto/gateway", o.gatewayTerms)
	g.Post("/crypto/gateway", o.gatewaySubmit)
	g.Get("/crypto/gateway/:id", o.gatewayRead)
	g.Get("/wire", o.wire)
}

// Answers the terms to sign a USDC top-up against, for an agent or a person
// paying from a wallet: the treasury to pay, the networks and USDC contracts,
// the EIP-712 domain and types, the limits, and a nonce issued to the caller.
//
// Sign TransferWithAuthorization {from, to: payTo, value, validAfter,
// validBefore, nonce} under one network's domain, then POST it here with the
// salt. The nonce is the caller's own: a payment signed against it credits the
// caller and cannot be presented by anyone else.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) gatewayTerms(ctx context.Context, _ *cloud.Unit) (*client.GatewayTerms, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "gateway terms", func(ctx context.Context) (*client.GatewayTerms, error) {
		return commercepeer.BillingGatewayTerms(ctx, &client.GatewayTermsIn{Payer: subject})
	})
}

// Takes a signed USDC payment from the caller's Circle Gateway balance to the
// treasury, and answers what became of it.
//
// accepted: Circle Gateway LOCKED the USDC and the caller is credited — or,
// once the caller holds $1,000 of credit Gateway has not confirmed, credited
// when Gateway confirms this payment (deferred). pending: the outcome is not
// known yet; read it back or present the same payment again — it resolves to
// one credit either way. A refusal is 4xx with Gateway's reason
// (insufficient_balance, invalid_signature, …) or ours (nonce_not_issued,
// recipient_mismatch, amount_below_minimum, …); 503 means the rail is shut.
//
// Who is credited is the caller, never a body value: the payer is resolved
// server-side, and the payment's own nonce must be the one issued to it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) gatewaySubmit(ctx context.Context, in *signedPayment) (*client.GatewayPayment, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "gateway payment", func(ctx context.Context) (*client.GatewayPayment, error) {
		return commercepeer.BillingGatewaySubmit(ctx, &client.GatewaySubmitIn{
			Payer: subject, Network: in.Network, Salt: in.Salt,
			Authorization: in.Authorization, Signature: in.Signature,
		})
	})
}

// signedPayment is a signed USDC payment as the caller presents it. There is
// no payer field: the credited account is the caller's own.
type signedPayment struct {
	// Network is the CAIP-2 network the payment was signed for, one of the
	// terms' accepts, e.g. "eip155:8453".
	Network string `json:"network"`
	// Salt is the salt the terms issued with the nonce.
	Salt string `json:"salt"`
	// Authorization is the EIP-3009 TransferWithAuthorization as signed: every
	// field a string, addresses and nonce 0x hex, value in USDC base units.
	Authorization client.GatewayAuthorization `json:"authorization"`
	// Signature is the 65-byte EIP-712 signature, 0x hex.
	Signature string `json:"signature"`
}

// Reads one of the caller's own USDC top-ups back: pending, accepted (credited
// or deferred), settled, failed or refused.
//
// A payment belonging to another payer answers 404, exactly as an id that names
// nothing, so a guessed id cannot confirm somebody else's payment exists.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) gatewayRead(ctx context.Context, in *gatewayRef) (*client.GatewayPayment, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "gateway payment", func(ctx context.Context) (*client.GatewayPayment, error) {
		return commercepeer.BillingGatewayRead(ctx, &client.GatewayReadIn{Payer: subject, ID: in.ID})
	})
}

// gatewayRef names one USDC top-up.
type gatewayRef struct {
	// ID is the top-up's id, from the path.
	ID string `json:"id"`
}

// Answers which chains and tokens the crypto rail accepts — what an asset picker
// renders — and the pairs themselves in assets: a chain and a token each on offer
// are not a pair on offer.
//
// It is the intersection of two live facts rather than a configured list: an
// asset appears only if something is WATCHING it and the custody processor
// supports it. An address nobody watches credits nobody, so offering one would
// take a customer's money and lose it. A rail with nothing armed answers 503,
// not an empty menu — "no rail" and "no assets" are different, and only one of
// them means try again later.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) cryptoOptions(ctx context.Context, _ *cloud.Unit) (*client.CryptoOptions, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "crypto options", func(ctx context.Context) (*client.CryptoOptions, error) {
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
// what comes back is an address, a status and the terms: the minimum that
// credits, any fee, the confirmations required, and one line of instructions.
// Once the transfer is that deep the deposit is credited as cash to the caller's
// balance (GET /v1/billing/balance, cash) and its status is succeeded.
//
// The asset is checked against GET /v1/billing/crypto/options before anything is
// minted. A chain, a token or a pair the rail does not accept is 400, and the
// answer lists the accepted chains, tokens and assets. A deposit rail with
// nothing watched is 503 — nothing sent now can be credited, and the Gateway
// rail or a card is the way to pay until it reopens.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) mintCryptoDeposit(ctx context.Context, in *cryptoAsset) (*client.CryptoDeposit, error) {
	if err := cloud.CSRF(ctx); err != nil {
		return nil, err
	}
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	offered, err := ask(ctx, org, "crypto options", func(ctx context.Context) (*client.CryptoOptions, error) {
		return commercepeer.BillingCryptoOptions(ctx)
	})
	if err != nil {
		return nil, err
	}
	if err := accepted(offered, in.Chain, in.Token); err != nil {
		return nil, err
	}
	return ask(ctx, org, "crypto deposit", func(ctx context.Context) (*client.CryptoDeposit, error) {
		return commercepeer.BillingCryptoMint(ctx, &client.CryptoMintIn{
			Payer: subject, Chain: in.Chain, Token: in.Token, AmountCents: in.AmountCents,
			PlanID: strings.TrimSpace(in.PlanID), Quantity: in.Quantity, Level: in.Level,
			Interval: strings.TrimSpace(in.Interval), Coupon: strings.TrimSpace(in.Coupon),
		})
	})
}

// accepted refuses an asset the menu does not offer, with the menu, so a caller
// can choose again without a second request. Only what the caller named is
// checked: an empty chain or token takes the rail's default, and the rail
// answers for that. An empty menu is a shut rail, which is 503 whatever was asked.
func accepted(menu *client.CryptoOptions, chain, token string) error {
	if len(menu.Assets) == 0 {
		return zip.Errorf(http.StatusServiceUnavailable,
			"crypto deposits are not being accepted right now; use a card, a signed USDC payment through Circle Gateway, or a bank transfer")
	}
	chain = strings.ToLower(strings.TrimSpace(chain))
	token = strings.ToLower(strings.TrimSpace(token))
	ok := (chain == "" || slices.Contains(menu.Chains, chain)) &&
		(token == "" || slices.Contains(menu.Tokens, token))
	if ok && chain != "" && token != "" {
		ok = slices.Contains(menu.Assets, client.CryptoPair{Chain: chain, Token: token})
	}
	if ok {
		return nil
	}
	var asked string
	switch {
	case token == "":
		asked = "chain " + chain
	case chain == "":
		asked = "token " + strings.ToUpper(token)
	default:
		asked = strings.ToUpper(token) + " on " + chain
	}
	pairs := make([]string, 0, len(menu.Assets))
	for _, a := range menu.Assets {
		pairs = append(pairs, strings.ToUpper(a.Token)+" on "+a.Chain)
	}
	return zip.ErrBadRequest(fmt.Sprintf("%s is not accepted; the crypto rail accepts %s", asked, strings.Join(pairs, ", "))).
		With(map[string]any{"chains": menu.Chains, "tokens": menu.Tokens, "assets": menu.Assets})
}

// cryptoAsset is which asset a deposit address is wanted for. There is no payer
// field: the credited wallet is the caller's own, resolved server-side, so a
// body value could only ever name a wallet the caller does not hold.
type cryptoAsset struct {
	// Chain is the network to receive on, one of GET /v1/billing/crypto/options
	// chains. Empty takes the rail's default.
	Chain string `json:"chain,omitempty"`
	// Token is the asset on that chain, one of the options' tokens. Empty takes the
	// rail's default.
	Token string `json:"token,omitempty"`
	// AmountCents is what the payer intends to send, for the record. Optional —
	// the credit is what actually arrives, never what was announced.
	AmountCents int64 `json:"amountCents,omitempty"`
	// Optional plan fields when paying a plan's first period with crypto.
	PlanID   string `json:"planId,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Level    int    `json:"level,omitempty"`
	Interval string `json:"interval,omitempty"`
	Coupon   string `json:"coupon,omitempty"`
}

// Reads one of the caller's own deposit intents back — pending, confirming, or
// succeeded.
//
// status is pending until a transfer to the address is seen, confirming while
// the chain buries it (confirmations counts up to requiredConfirmations), and
// succeeded once it is credited: creditedCents is then in the caller's balance
// as cash (GET /v1/billing/balance, cash). The answer is the same shape the mint
// returned, terms and instructions included, so polling it is all an agent needs.
//
// An intent belonging to another payer answers 404, exactly as an id that names
// nothing, so a guessed id cannot confirm that somebody else's deposit exists.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) cryptoDeposit(ctx context.Context, in *depositRef) (*client.CryptoDeposit, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "crypto deposit", func(ctx context.Context) (*client.CryptoDeposit, error) {
		return commercepeer.BillingCryptoDeposit(ctx, &client.CryptoDepositIn{Payer: subject, ID: in.ID})
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
func (o ops) wire(ctx context.Context, _ *cloud.Unit) (*client.WireInstructions, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	// The HOST, not a brand: the table that maps one to the other is the store's.
	host := ""
	if c, ok := cloud.Request(ctx); ok {
		host = c.Host()
	}
	return ask(ctx, org, "wire", func(ctx context.Context) (*client.WireInstructions, error) {
		return commercepeer.BillingWire(ctx, &client.WireIn{Host: host, Payer: subject})
	})
}
