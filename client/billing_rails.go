// billing_rails.go holds native crypto custody, Circle Gateway USDC, and bank wire rail contracts.

package client

import (
	"encoding/json"
	"strings"
)

// ---- billing.crypto and billing.wire — the other two rails ----------------

const (
	BillingCryptoOptions = "billing_crypto_options"
	BillingCryptoMint    = "billing_crypto_mint"
	BillingCryptoDeposit = "billing_crypto_deposit"

	BillingWire = "billing_wire"
)

// CryptoOptions is the menu of what the crypto rails accept: the chains a payer
// may send to a deposit address on and the tokens they may send, the networks a
// signed USDC payment may be made on through Circle Gateway, and the pairs.
type CryptoOptions struct {
	Chains []string `json:"chains"`
	Tokens []string `json:"tokens"`
	// Assets is every (chain, token) pair the deposit-address rail accepts.
	// Chains and Tokens are its two axes: a chain and a token each on offer are
	// not a pair on offer, so a picker that crosses them can name an asset the
	// rail will refuse.
	Assets  []CryptoPair `json:"assets"`
	Gateway []string     `json:"gateway"`
}

// CryptoPair is one asset the deposit-address rail accepts: a token on a chain,
// with its watched contract, base-unit decimals, minimum creditable amount, fee,
// confirmation depth, and whether a destination tag is required.
type CryptoPair struct {
	// Chain is the network, as a deposit names it ("base").
	Chain string `json:"chain"`
	// Token is the asset on that chain, as a deposit names it ("usdc").
	Token string `json:"token"`
	// Contract is the on-chain token contract, SPL mint, jetton master, or issuer.
	Contract string `json:"contract,omitempty"`
	// Decimals is the token's base-unit precision (e.g. 6 for USDC).
	Decimals int `json:"decimals,omitempty"`
	// MinimumCents is the smallest deposit, in USD cents, that credits anything.
	MinimumCents int64 `json:"minimumCents,omitempty"`
	// MinAmount is the smallest deposit in raw token base units.
	MinAmount string `json:"minAmount,omitempty"`
	// FeeCents is deducted from each deposit before crediting.
	FeeCents int64 `json:"feeCents,omitempty"`
	// Confirmations is how many blocks deep a transfer must be before crediting.
	Confirmations int `json:"confirmations,omitempty"`
	// AddressTagRequired reports whether deposits on this chain share one address and require a destination tag.
	AddressTagRequired bool `json:"addressTagRequired,omitempty"`
}

// CryptoMintIn asks for a deposit address for one payer and one asset.
type CryptoMintIn struct {
	Payer       string `json:"payer"`
	Chain       string `json:"chain"`
	Token       string `json:"token"`
	AmountCents int64  `json:"amountCents,omitempty"`
	// PlanID, Quantity, Level, Interval and Coupon bind the deposit to a plan's first period.
	PlanID   string `json:"planId,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Level    int    `json:"level,omitempty"`
	Interval string `json:"interval,omitempty"`
	Coupon   string `json:"coupon,omitempty"`
}

// CryptoDepositIn reads one intent back. A foreign id is a miss, not a refusal,
// so a guessed id cannot confirm that somebody else's deposit exists.
type CryptoDepositIn struct {
	Payer string `json:"payer"`
	ID    string `json:"id"`
}

// CryptoDeposit is one deposit intent: where to send, what is credited, and how
// far the chain has got.
//
// AddressTag is the routing tag a payer MUST send with the payment on a chain
// where the address is shared. omitempty is load-bearing and safe: the tag is
// decimal text, so the first tag ever issued is "0" — not empty, and therefore
// sent. A payment to a pooled address with no tag names nobody and is credited
// to nobody.
//
// The fields after ExpiresAt were added after it, and stay after it: this type
// crosses the plane by field position.
type CryptoDeposit struct {
	// ID names the intent; read it back at GET /v1/billing/crypto/deposit/{id}.
	ID string `json:"id"`
	// Status is pending until a transfer to the address is seen, confirming while
	// the chain buries it, and succeeded once it is credited as cash in the
	// payer's balance (GET /v1/billing/balance, cash).
	Status string `json:"status"`
	// Chain is the network to send on.
	Chain string `json:"chain"`
	// Token is the asset to send; this intent credits that token alone.
	Token string `json:"token"`
	// DepositAddress is where to send it.
	DepositAddress string `json:"depositAddress"`
	// AddressTag is the destination tag the payment must carry, on a chain whose
	// address is shared. Absent where the address alone names the payer.
	AddressTag string `json:"addressTag,omitempty"`
	// ExpiresAt is when this address stops being handed out again (RFC3339). A
	// transfer that arrives after it is still credited.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// AmountCents is what the payer said they would send, in USD cents, when the
	// address was asked for: a record of intent, not a quote. What is credited is
	// what arrives. Absent when nothing was named.
	AmountCents int64 `json:"amountCents,omitempty"`
	// MinimumCents is the smallest deposit, in USD cents, that credits anything;
	// less credits nothing. Absent when nothing is watching this asset now, and
	// then nothing sent to it is credited.
	MinimumCents int64 `json:"minimumCents,omitempty"`
	// FeeCents is deducted from every deposit before it is credited: the cost of
	// sweeping it out of the custody address. Absent deducts nothing.
	FeeCents int64 `json:"feeCents,omitempty"`
	// RequiredConfirmations is how many blocks deep the transfer must be before it
	// is credited.
	RequiredConfirmations int `json:"requiredConfirmations"`
	// Confirmations is how deep the transfer being confirmed is now; zero until one
	// is seen.
	Confirmations int `json:"confirmations"`
	// CreditedCents is what the transfer this intent confirmed credited, in USD
	// cents, once it succeeded. A later transfer to the same address is credited
	// too, and shows in the balance rather than here.
	CreditedCents int64 `json:"creditedCents,omitempty"`
	// Instructions says, in one line, what to send where and when it is credited.
	Instructions string `json:"instructions"`
	// PlanID and SubscriptionID are set when the deposit pays a plan's first period.
	PlanID         string `json:"planId,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	// Contract is the exact watched token contract/mint this intent accepts.
	Contract string `json:"contract,omitempty"`
	// TxHash and LogIndex identify the on-chain transfer event being confirmed or credited.
	TxHash   string `json:"txHash,omitempty"`
	LogIndex uint64 `json:"logIndex,omitempty"`
	// SettlementState tracks progression: pending -> confirming -> credited -> swept (or expired).
	SettlementState string `json:"settlementState,omitempty"`
	// SweptTxHash records the treasury sweep transaction hash once swept.
	SweptTxHash string `json:"sweptTxHash,omitempty"`
}

// ---- billing.gateway — USDC through Circle Gateway ---------------------------

const (
	BillingGatewayTerms  = "billing_gateway_terms"
	BillingGatewaySubmit = "billing_gateway_submit"
	BillingGatewayRead   = "billing_gateway_read"
)

// GatewayTermsIn asks for payment terms for one payer. The nonce in the answer
// is issued to that payer and no other.
type GatewayTermsIn struct {
	Payer string `json:"payer"`
}

// GatewayField is one member of an EIP-712 struct type.
type GatewayField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// GatewayDomain is the EIP-712 domain a payment is signed under, in the shape
// viem and ethers sign with.
type GatewayDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           int64  `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// GatewayAccept is one network a payment may be signed on.
type GatewayAccept struct {
	Network     string        `json:"network"`
	Asset       string        `json:"asset"`
	Symbol      string        `json:"symbol"`
	Decimals    int           `json:"decimals"`
	MinAmount   string        `json:"minAmount"`
	MaxAmount   string        `json:"maxAmount"`
	MinValidity int64         `json:"minValiditySeconds"`
	Domain      GatewayDomain `json:"domain"`
}

// GatewayTerms is everything a payer needs to sign a USDC top-up: the treasury
// to pay, the networks, the EIP-712 domain and types, the limits, and a nonce
// issued to the caller with its salt.
//
// Types is raw JSON because a Go map cannot cross the plane (zapenc), while
// raw JSON can; the shape stays exactly the EIP-712 types map the pay page
// prints. The bytes are the map's own marshalling — encoding/json orders a
// map's keys, so they are deterministic and byte-identical to what the old
// map marshalled into. The fleet's precedent is crawl.metadata ("raw JSON
// publishes {} — 'any JSON', which is true; the bytes are identical either
// way").
type GatewayTerms struct {
	Version     int             `json:"x402Version"`
	Scheme      string          `json:"scheme"`
	PayTo       string          `json:"payTo"`
	Nonce       string          `json:"nonce"`
	Salt        string          `json:"salt"`
	MinCents    int64           `json:"minCents"`
	MaxCents    int64           `json:"maxCents"`
	MaxValidity int64           `json:"maxValiditySeconds"`
	Primary     string          `json:"primaryType"`
	Types       json.RawMessage `json:"types"`
	Accepts     []GatewayAccept `json:"accepts"`
}

// GatewayAuthorization is an EIP-3009 TransferWithAuthorization as signed.
type GatewayAuthorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

// GatewaySubmitIn is one payer's signed payment.
type GatewaySubmitIn struct {
	Payer         string               `json:"payer"`
	Network       string               `json:"network"`
	Salt          string               `json:"salt"`
	Authorization GatewayAuthorization `json:"authorization"`
	Signature     string               `json:"signature"`
}

// GatewayReadIn reads one payment back. A foreign id is a miss, exactly as an
// id that names nothing.
type GatewayReadIn struct {
	Payer string `json:"payer"`
	ID    string `json:"id"`
}

// GatewayPayment is one USDC top-up and its fate: pending, accepted, settled,
// failed or refused. Credited means the money is in the payer's account;
// Deferred means it will be once Gateway confirms the payment onchain.
type GatewayPayment struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Network     string `json:"network"`
	From        string `json:"from"`
	Value       string `json:"value"`
	AmountCents int64  `json:"amountCents"`
	Credited    bool   `json:"credited"`
	Deferred    bool   `json:"deferred,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Payment     string `json:"payment,omitempty"`
	TxHash      string `json:"txHash,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// WireIn asks for the receiving bank details a wire top-up should be sent to.
//
// Host is the address the customer is paying on. It travels as the HOST rather
// than as a brand slug because the table that reduces one to the other belongs
// to the store, and a caller that did the reduction would be keeping a second
// copy of which hostnames are whose. Payer goes into the payment reference,
// which is how an arriving wire names who it credits.
type WireIn struct {
	Host  string `json:"host"`
	Payer string `json:"payer"`
}

// WireInstructions is that account. It is all-or-nothing by design: a customer
// cannot wire to three fields out of five, so a half-empty form is refused
// upstream rather than rendered.
type WireInstructions struct {
	BankName      string `json:"bankName"`
	BankAddress   string `json:"bankAddress,omitempty"`
	AccountNumber string `json:"accountNumber"`
	RoutingNumber string `json:"routingNumber,omitempty"`
	SwiftCode     string `json:"swiftCode,omitempty"`
	IBAN          string `json:"iban,omitempty"`
	AccountName   string `json:"accountName"`
	Memo          string `json:"memo,omitempty"`
	Reference     string `json:"reference"`
}

// WireReference is the payment reference a payer is shown beside the bank details,
// and the one an arriving wire carries back on the bank statement: "TOPUP-" and the
// payer's billing key, upper-cased, every character outside [A-Z0-9] a hyphen
// ("hanzo/z" → "TOPUP-HANZO-Z"). Commerce renders it (api/billing GetWireInstructions);
// the admin console reads a statement's reference back to the account with it.
// apps/commerce TestWireReferenceIsCommerces holds the two renderings equal.
func WireReference(payer string) string {
	var b strings.Builder
	b.WriteString("TOPUP-")
	for _, r := range strings.ToUpper(payer) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

