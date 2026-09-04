// Package x402 is pay-per-request over HTTP 402: quote a price, take the payment,
// serve the resource. It speaks x402 PROTOCOL VERSION 2 (the CAIP-2 / PAYMENT-*
// header generation), as specified by
// github.com/x402-foundation/x402/specs/x402-specification-v2.md and its HTTP
// transport binding in specs/transports-v2/http.md.
//
// The full cycle is challenge → the client pays → payload submitted → verify →
// serve → settle, native to the Hanzo cloud binary.
//
// FLOW. A priced resource answers 402 with a PaymentRequired carrying one or more
// PaymentRequirements (what to pay, in what asset, on what network, to whom). The
// client signs an EIP-3009 transferWithAuthorization over exactly those terms and
// retries with the PAYMENT-SIGNATURE header. The subsystem VERIFIES the EIP-712
// signature (secp256k1 recovery via luxfi/crypto — the SAME primitive the wallets
// custody signs with), rejects a REPLAYED authorization (nonce dedup), SETTLES
// exactly once (payer debit through the metering spine so paid usage appears in
// billing/usage like any metered spend, plus a credit to the recipient wallet's
// ledger), and serves — answering with a SettlementResponse on PAYMENT-RESPONSE.
//
// WHAT IS ON THE WIRE IS THE SPEC'S, NOT OURS. Every field name, header name and
// error reason in this file is the one the specification prints, because the wire
// is a contract with other people's clients: an @x402/fetch or x402[httpx] client
// that has never heard of Hanzo must be able to pay us, and our private nouns
// would make that impossible. Internal Go names stay idiomatic only where they do
// not leak (Terms, Settlement, gate).
//
// CLIENTS. The marketplace registry (another subsystem) owns the mapping
// resource→Terms (price + recipient wallet); x402 only enforces it (Registry +
// Publish). Recipient resolution rides the wallets subsystem
// (wallet.ResolvePaymentTarget). On-chain broadcast of the authorization is a
// Settler client; the LIVE default is ledger settlement.
package x402

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/luxfi/crypto"
)

const (
	// Version is the x402 protocol version this subsystem speaks. It is a NUMBER on
	// the wire (`x402Version: 2`), and it is the only thing that decides how a
	// message is read — there is no configuration switch for the protocol version,
	// because a wire whose shape depends on an operator's flag is two wires.
	Version = 2

	// SchemeExact is the payment scheme: the buyer authorizes EXACTLY the advertised
	// amount. It is the only scheme this rail implements.
	SchemeExact = "exact"

	// TransferEIP3009 is the exact-scheme EVM asset transfer method: the token's own
	// transferWithAuthorization, signed off-chain (EIP-712). It is the spec's default
	// when `extra.assetTransferMethod` is absent, and the only one we verify.
	TransferEIP3009 = "eip3009"

	// The three wire headers, all carrying BASE64-ENCODED JSON. They are DEFINED in
	// plane, because the settlement now crosses a process boundary and the process
	// that holds the request — where the payload arrives and the challenge has to be
	// written — is not the process that holds this package. Naming them there lets
	// the tool plane lift a payload and set a challenge without linking a payment
	// subsystem it deliberately knows nothing about; naming them HERE too would be
	// two spellings of one wire, which is the bug where a client pays and the server
	// never sees it.

	// HeaderPaymentRequired carries the PaymentRequired on a 402 response.
	HeaderPaymentRequired = client.HeaderPaymentRequired
	// HeaderPaymentSignature carries the client's PaymentPayload on the retry.
	HeaderPaymentSignature = client.HeaderPaymentSignature
	// HeaderPaymentResponse carries the SettlementResponse on the answered request.
	HeaderPaymentResponse = client.HeaderPaymentResponse

	// DefaultMaxTimeoutSeconds is the default `maxTimeoutSeconds` advertised on a
	// challenge — how long the client has to complete the payment.
	DefaultMaxTimeoutSeconds = 300
)

// The error reasons the specification defines (x402-specification-v2 §9). They are
// the vocabulary a client matches on, so they are spelled here exactly once and
// never invented per call site. Reasons with no spec entry are ours and say so.
const (
	reasonValidAfter    = "invalid_exact_evm_payload_authorization_valid_after"
	reasonValidBefore   = "invalid_exact_evm_payload_authorization_valid_before"
	reasonValueMismatch = "invalid_exact_evm_payload_authorization_value_mismatch"
	reasonSignature     = "invalid_exact_evm_payload_signature"
	reasonRecipient     = "invalid_exact_evm_payload_recipient_mismatch"
	reasonNetwork       = "invalid_network"
	reasonPayload       = "invalid_payload"
	reasonScheme        = "invalid_scheme"
	reasonVersion       = "invalid_x402_version"
	reasonSettle        = "unexpected_settle_error"

	// Ours: the spec has no reason for "you have not paid yet" (it is the ordinary
	// first leg of the flow), for a spent nonce, or for the rail being unable to
	// enforce at all.
	reasonRequired      = "payment_required"
	reasonReplay        = "nonce_replayed"
	reasonUnbillable    = "unbillable"
	reasonPayee         = "payee_unavailable"
	reasonUnavailable   = "x402_unavailable"
	reasonUnenforceable = "x402_unenforceable"
)

// PaymentRequired is the 402 challenge: what the resource is, and every way it may
// be paid for. It is carried BASE64-ENCODED on the PAYMENT-REQUIRED header, which
// the HTTP transport names as its canonical location.
type PaymentRequired struct {
	X402Version int                   `json:"x402Version"`
	Error       string                `json:"error,omitempty"`
	Resource    ResourceInfo          `json:"resource"`
	Accepts     []PaymentRequirements `json:"accepts"`
	Extensions  Extensions            `json:"extensions,omitempty"`
}

// ResourceInfo describes the protected resource. URL is the resource's identity —
// for a priced ROUTE the request path, for a priced TOOL its `tool:` id — which is
// also the key the price table and the settlement row are written under.
type ResourceInfo struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// PaymentRequirements is ONE acceptable way to pay: the scheme, the network, and
// the exact amount of the exact asset that must reach exactly this payee.
type PaymentRequirements struct {
	Scheme            string `json:"scheme"`
	Network           string `json:"network"` // CAIP-2, e.g. "eip155:36963"
	Amount            string `json:"amount"`  // atomic units of Asset, decimal string
	Asset             string `json:"asset"`   // EIP-3009 token contract
	PayTo             string `json:"payTo"`   // recipient wallet address
	MaxTimeoutSeconds int64  `json:"maxTimeoutSeconds"`
	Extra             *Extra `json:"extra,omitempty"`
}

// Extra is the exact-scheme EVM `extra`: the token's EIP-712 domain, which is what
// makes a signature verifiable at all, plus the transfer method it was signed for.
//
// The domain lives on the CHALLENGE rather than in this process's config because
// the client signs over what it was OFFERED. A server that verified against its own
// config instead would accept a signature the client never made over these terms.
type Extra struct {
	AssetTransferMethod string `json:"assetTransferMethod,omitempty"`
	Name                string `json:"name"`
	Version             string `json:"version"`
}

// PaymentPayload is the client's payment, carried BASE64-ENCODED on the
// PAYMENT-SIGNATURE header. Accepted is the client ECHOING which of the offered
// requirements it chose; it is checked against ours and never trusted as the terms.
type PaymentPayload struct {
	X402Version int                 `json:"x402Version"`
	Resource    *ResourceInfo       `json:"resource,omitempty"`
	Accepted    PaymentRequirements `json:"accepted"`
	Payload     ExactPayload        `json:"payload"`
	Extensions  Extensions          `json:"extensions,omitempty"`
}

// ExactPayload is the exact-scheme EVM `payload`: the signature and the parameters
// needed to reconstruct the message it signed.
type ExactPayload struct {
	Signature     string        `json:"signature"`
	Authorization Authorization `json:"authorization"`
}

// Authorization is the EIP-3009 TransferWithAuthorization. Nonce is a client-chosen
// 32-byte value and is the REPLAY ANCHOR — on-chain the token contract itself
// refuses a second transfer for one (from, nonce), and this rail enforces the same
// pair off-chain so a ledger settlement inherits the identical guarantee.
type Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  epoch  `json:"validAfter"`
	ValidBefore epoch  `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

// SettlementResponse is what a request answers with once payment has been settled —
// or failed to — carried BASE64-ENCODED on the PAYMENT-RESPONSE header. The spec
// requires it on BOTH the success and the failure leg.
//
// Transaction is the identifier of the transaction that settled this payment on
// whichever rail settled it: the chain's tx hash when the authorization is
// broadcast, and the deterministic settlement id when it is settled on the ledger
// (the live default). Either way it is the handle that finds this settlement again,
// which is the one thing the field is for; an empty string means nothing settled.
type SettlementResponse struct {
	Success     bool       `json:"success"`
	ErrorReason string     `json:"errorReason,omitempty"`
	Payer       string     `json:"payer,omitempty"`
	Transaction string     `json:"transaction"`
	Network     string     `json:"network"`
	Amount      string     `json:"amount,omitempty"`
	Extensions  Extensions `json:"extensions,omitempty"`
}

// Extensions is the protocol's extension map, carried through VERBATIM. We
// advertise none, so this exists to preserve what a client sends rather than to
// interpret it: dropping an unknown extension silently is how a client that thinks
// it negotiated something discovers otherwise only in production.
type Extensions map[string]json.RawMessage

// Receipt is the settlement record this subsystem's OWN api answers with at
// GET /v1/x402/settlements/:id. It is deliberately not the wire type: the wire
// carries the spec's SettlementResponse, which has no room for the payer org or the
// resource, and those are exactly what a tenant reading its own settlements needs.
type Receipt struct {
	// ID is the settle-once key: "x402_" + keccak(from|nonce) in hex. It is
	// DERIVED, not minted, so a client that re-submits the same authorization
	// addresses the same settlement and is served again for free rather than
	// charged twice. It is also the id GET /v1/x402/settlements/:id takes.
	ID string `json:"id"`
	// Resource is what was paid for, in the same spelling the price table and the
	// challenge used: the request path for a priced route, "tool:<id>" for a
	// priced tool.
	Resource string `json:"resource"`
	// Payer is the payer ORG — the tenant whose ledger was debited — and not an
	// address. It is the org the request was authenticated as, so it answers who
	// is billed, which the payer address alone cannot.
	Payer string `json:"payer"`
	// From is the payer's EVM address: the account that signed the EIP-3009
	// authorization, recovered from the signature rather than taken on trust.
	From string `json:"from"`
	// Payee is the recipient's EVM address — the `payTo` the challenge advertised
	// and the authorization named. A payment to any other address never settles.
	Payee string `json:"payee"`
	// PayeeOrg is the tenant that owns the recipient wallet, resolved at
	// settlement. It is who got PAID, as Payer is who paid.
	PayeeOrg string `json:"payeeOrg"`
	// Amount is what actually moved, as an exact 18-decimal-place USD string. It
	// is NOT the atomic-unit figure the client signed: the challenge quotes the
	// asset's own units (USDC's 6 dp) and truncates to fit them, while the ledger
	// moves this exact value.
	Amount string `json:"amount"`
	// Nonce is the client-chosen nonce from the authorization, hex — up to 32
	// bytes, left-padded to the contract's bytes32. It is the replay anchor: the
	// token contract refuses a second on-chain transfer for one (from, nonce), and
	// this rail refuses a second settlement for the same pair, so a ledger
	// settlement inherits the identical guarantee.
	Nonce string `json:"nonce"`
	// Network is the CAIP-2 identifier the payment was settled under, e.g.
	// "eip155:36963". Its eip155 reference is the chain id in the EIP-712 domain
	// the payer signed, so it is not a label — changing it invalidates the
	// signature.
	Network string `json:"network"`
	// SettledVia is which rail moved the money: "ledger", the live default, or
	// "chain" when the authorization is broadcast. Those two values and no others.
	SettledVia string `json:"settledVia"`
	// TxHash is the chain transaction hash, present only for a "chain"
	// settlement. Empty on a ledger settlement — that is the normal case today,
	// and it means the money moved without a chain, not that it failed. The
	// wire's PAYMENT-RESPONSE `transaction` falls back to ID when this is empty.
	TxHash string `json:"txHash,omitempty"`
	// SettledAt is when this settlement was CLAIMED, in unix seconds — the moment
	// the authorization was accepted, which is also the moment the time window it
	// carried stopped applying. A settlement finished later by reconciliation
	// keeps this instant.
	SettledAt int64 `json:"settledAt"`
}

// epoch is a unix-seconds timestamp. The specification prints it as a JSON STRING
// ("1740672089"), so that is what it encodes to; it decodes from either spelling
// because implementations differ on the type and a payment that fails to parse is
// a payment lost to a quoting convention.
type epoch int64

func (e epoch) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(strconv.FormatInt(int64(e), 10))), nil
}

func (e *epoch) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*e = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("x402: %s is not unix seconds: %w", string(b), err)
	}
	*e = epoch(n)
	return nil
}

// ── the header codec ──────────────────────────────────────────────────────────

// EncodeHeader renders v as the base64 JSON an x402 v2 header carries. Standard
// (padded) base64 is what the specification's own examples decode as.
//
// It is exported for the same reason [Sign] is: a client has to put a
// PaymentPayload on PAYMENT-SIGNATURE, and a codec it cannot reach would leave the
// signing half of this package unusable from outside it.
func EncodeHeader(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeHeader reads a base64 JSON header value into v. It accepts unpadded and
// URL-safe base64 too: those are the spellings a client library reaches for by
// accident, and every one of them is unambiguous here.
func DecodeHeader(header string, v any) error {
	s := strings.TrimSpace(header)
	if s == "" {
		return fmt.Errorf("x402: empty header")
	}
	raw, err := decodeBase64(s)
	if err != nil {
		return fmt.Errorf("x402: header is not base64: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("x402: header is not %T: %w", v, err)
	}
	return nil
}

func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not base64")
}

// ParsePayment decodes a PaymentPayload from a PAYMENT-SIGNATURE header value.
func ParsePayment(header string) (*PaymentPayload, error) {
	var p PaymentPayload
	if err := DecodeHeader(header, &p); err != nil {
		return nil, &Invalid{Reason: reasonPayload, Detail: err.Error()}
	}
	a := p.Payload.Authorization
	if strings.TrimSpace(a.From) == "" || strings.TrimSpace(a.Nonce) == "" ||
		strings.TrimSpace(p.Payload.Signature) == "" {
		return nil, &Invalid{Reason: reasonPayload,
			Detail: "payload.authorization.from, payload.authorization.nonce and payload.signature are required"}
	}
	return &p, nil
}

// ── CAIP-2 ────────────────────────────────────────────────────────────────────

// ChainID is the EIP-155 chain id a CAIP-2 network identifier names.
//
// The network is ONE value on the wire — "eip155:36963" — and the chain id is read
// out of it rather than carried beside it. They were two fields once, which is one
// fact with two spellings and therefore one fact that can disagree with itself:
// a challenge whose network said one chain and whose chainId said another signs an
// EIP-712 domain no client can reproduce.
func ChainID(network string) (int64, error) {
	ns, ref, err := parseCAIP2(network)
	if err != nil {
		return 0, err
	}
	if ns != "eip155" {
		return 0, &Invalid{Reason: reasonNetwork,
			Detail: fmt.Sprintf("network %q is %q, and this rail settles EIP-3009 on eip155 only", network, ns)}
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id <= 0 {
		return 0, &Invalid{Reason: reasonNetwork,
			Detail: fmt.Sprintf("network %q has no eip155 chain id", network)}
	}
	return id, nil
}

// parseCAIP2 splits a chain-agnostic network id into its namespace and reference,
// per CAIP-2: namespace is [-a-z0-9]{3,8}, reference is [-_a-zA-Z0-9]{1,32}.
func parseCAIP2(network string) (namespace, reference string, err error) {
	s := strings.TrimSpace(network)
	before, after, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", &Invalid{Reason: reasonNetwork, Detail: fmt.Sprintf(
			"network %q is not CAIP-2 (namespace:reference, e.g. eip155:8453)", network)}
	}
	namespace, reference = before, after
	if !caip2Token(namespace, 3, 8, false) || !caip2Token(reference, 1, 32, true) {
		return "", "", &Invalid{Reason: reasonNetwork,
			Detail: fmt.Sprintf("network %q is not a well-formed CAIP-2 identifier", network)}
	}
	return namespace, reference, nil
}

func caip2Token(s string, min, max int, ref bool) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		case ref && (r >= 'A' && r <= 'Z' || r == '_'):
		default:
			return false
		}
	}
	return true
}

// ── verification ──────────────────────────────────────────────────────────────

// Invalid is a verification failure carrying the specification's own error reason,
// which is what the wire reports and what a client matches on.
type Invalid struct {
	Reason string
	Detail string
}

func (e *Invalid) Error() string { return e.Reason + ": " + e.Detail }

func invalid(reason, format string, args ...any) *Invalid {
	return &Invalid{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// reasonOf reports the spec error reason an error carries, defaulting to the
// unexpected-settle reason for anything that is not a verification failure.
func reasonOf(err error) string {
	if inv, ok := errors.AsType[*Invalid](err); ok {
		return inv.Reason
	}
	return reasonSettle
}

// Verify checks everything about pay that is true or false FOREVER: that it speaks
// this protocol version, that the terms it echoes are the terms we offered, and
// that the signature recovers to the payer it claims.
//
// It deliberately does NOT check the time window. That is [InWindow], and it is
// separate because the two answers have different lifetimes: a bad signature is bad
// for all time, while an expired authorization was VALID when we accepted it. The
// flow needs to tell those apart to finish a settlement it already started (see
// settle), and a single Verify that folded them together made "we took your money
// five minutes ago" indistinguishable from "this was never a payment".
func Verify(req PaymentRequirements, pay PaymentPayload) error {
	if pay.X402Version != Version {
		return invalid(reasonVersion, "x402Version %d, want %d", pay.X402Version, Version)
	}
	// PARAMETER MATCHING (spec §6.1.2 step 5). The client echoes the requirements it
	// chose, and every field of that echo is checked against the ones we ACTUALLY
	// offered — most of all Extra, which is the EIP-712 domain: a payload that could
	// name its own token name, version, asset or chain would be signing a message of
	// its own devising and would verify perfectly against itself.
	if err := sameRequirements(req, pay.Accepted); err != nil {
		return err
	}
	a := pay.Payload.Authorization
	if !addressEqual(a.To, req.PayTo) {
		return invalid(reasonRecipient, "authorized payee %s, required %s", a.To, req.PayTo)
	}
	if strings.TrimSpace(a.Value) != strings.TrimSpace(req.Amount) {
		return invalid(reasonValueMismatch, "authorized %s, required %s", a.Value, req.Amount)
	}
	digest, err := eip712Digest(req, a)
	if err != nil {
		return err
	}
	signer, err := recoverSigner(digest, pay.Payload.Signature)
	if err != nil {
		return err
	}
	if !addressEqual(signer, a.From) {
		return invalid(reasonSignature, "recovered %s, claimed %s", signer, a.From)
	}
	return nil
}

// InWindow reports whether an authorization may be ACCEPTED at now — the one check
// whose answer changes with the clock.
func InWindow(a Authorization, now int64) error {
	if now < int64(a.ValidAfter) {
		return invalid(reasonValidAfter, "authorization is not valid until %d (now %d)", a.ValidAfter, now)
	}
	if now > int64(a.ValidBefore) {
		return invalid(reasonValidBefore, "authorization expired at %d (now %d)", a.ValidBefore, now)
	}
	return nil
}

// sameRequirements checks the client's echoed terms against the ones we offered.
func sameRequirements(want, got PaymentRequirements) error {
	if got.Scheme != want.Scheme {
		return invalid(reasonScheme, "scheme %q, offered %q", got.Scheme, want.Scheme)
	}
	if got.Network != want.Network {
		return invalid(reasonNetwork, "network %q, offered %q", got.Network, want.Network)
	}
	if strings.TrimSpace(got.Amount) != strings.TrimSpace(want.Amount) {
		return invalid(reasonValueMismatch, "amount %q, offered %q", got.Amount, want.Amount)
	}
	if !addressEqual(got.Asset, want.Asset) {
		return invalid(reasonPayload, "asset %q, offered %q", got.Asset, want.Asset)
	}
	if !addressEqual(got.PayTo, want.PayTo) {
		return invalid(reasonRecipient, "payTo %q, offered %q", got.PayTo, want.PayTo)
	}
	if extraOf(got) != extraOf(want) {
		return invalid(reasonPayload, "extra %+v, offered %+v", extraOf(got), extraOf(want))
	}
	return nil
}

// extraOf is the requirement's EIP-712 domain as a comparable value, with the
// spec's default filled in — an absent assetTransferMethod MEANS eip3009, so a
// client that omits it and a server that states it have agreed, not disagreed.
func extraOf(r PaymentRequirements) Extra {
	e := Extra{}
	if r.Extra != nil {
		e = *r.Extra
	}
	if e.AssetTransferMethod == "" {
		e.AssetTransferMethod = TransferEIP3009
	}
	return e
}

// Sign is the CLIENT half of the protocol and the exact mirror of Verify: it
// produces the PaymentPayload a payer submits on PAYMENT-SIGNATURE, bound to
// exactly the requirements it was challenged with.
//
// It lives here, beside Verify, because the EIP-712 encoding is ONE encoding: a
// signer that wrote it out a second time would be free to drift from the verifier
// and would fail only in production, where a real payer's signature stops
// recovering. One encoding, two directions.
func Sign(req PaymentRequirements, key *ecdsa.PrivateKey, nonce string, validAfter, validBefore int64) (PaymentPayload, error) {
	if key == nil {
		return PaymentPayload{}, fmt.Errorf("x402: sign requires a key")
	}
	a := Authorization{
		From:  crypto.PubkeyToAddress(key.PublicKey).Hex(),
		To:    req.PayTo,
		Value: req.Amount,
		// The window is the CLIENT's to state, within the maxTimeoutSeconds it was
		// offered; the server only ever checks it.
		ValidAfter: epoch(validAfter), ValidBefore: epoch(validBefore), Nonce: nonce,
	}
	digest, err := eip712Digest(req, a)
	if err != nil {
		return PaymentPayload{}, err
	}
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return PaymentPayload{}, fmt.Errorf("x402: sign: %w", err)
	}
	return PaymentPayload{
		X402Version: Version,
		Accepted:    req,
		Payload:     ExactPayload{Signature: "0x" + hex.EncodeToString(sig), Authorization: a},
	}, nil
}

// eip712Digest computes the EIP-712 typed-data hash a client signs for an EIP-3009
// TransferWithAuthorization, bound to the token's domain (name, version, chainId,
// verifyingContract=asset). Every input comes off the REQUIREMENTS, so the digest
// is a function of the terms and of nothing else. Keccak is luxfi/crypto's — the
// ONE hash the binary uses.
func eip712Digest(req PaymentRequirements, a Authorization) ([32]byte, error) {
	chainID, err := ChainID(req.Network)
	if err != nil {
		return [32]byte{}, err
	}
	extra := extraOf(req)
	if extra.AssetTransferMethod != TransferEIP3009 {
		return [32]byte{}, invalid(reasonScheme,
			"assetTransferMethod %q; this rail verifies %q only", extra.AssetTransferMethod, TransferEIP3009)
	}
	domain := domainSeparator(extra.Name, extra.Version, chainID, req.Asset)

	typeHash := keccak([]byte(
		"TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"))

	value, ok := new(big.Int).SetString(strings.TrimSpace(a.Value), 10)
	if !ok {
		return [32]byte{}, invalid(reasonPayload, "value %q is not an integer", a.Value)
	}
	nonce, err := nonce32(a.Nonce)
	if err != nil {
		return [32]byte{}, err
	}

	var enc []byte
	enc = append(enc, typeHash[:]...)
	enc = append(enc, pad32(addrBytes(a.From))...)
	enc = append(enc, pad32(addrBytes(a.To))...)
	enc = append(enc, pad32(value.Bytes())...)
	enc = append(enc, pad32(big.NewInt(int64(a.ValidAfter)).Bytes())...)
	enc = append(enc, pad32(big.NewInt(int64(a.ValidBefore)).Bytes())...)
	enc = append(enc, nonce[:]...)
	structHash := keccak(enc)

	var in []byte
	in = append(in, 0x19, 0x01)
	in = append(in, domain[:]...)
	in = append(in, structHash[:]...)
	return keccak(in), nil
}

// domainSeparator hashes the EIP-712 domain for an EIP-3009 token.
func domainSeparator(name, version string, chainID int64, asset string) [32]byte {
	typeHash := keccak([]byte(
		"EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash := keccak([]byte(name))
	versionHash := keccak([]byte(version))
	var enc []byte
	enc = append(enc, typeHash[:]...)
	enc = append(enc, nameHash[:]...)
	enc = append(enc, versionHash[:]...)
	enc = append(enc, pad32(big.NewInt(chainID).Bytes())...)
	enc = append(enc, pad32(addrBytes(asset))...)
	return keccak(enc)
}

// recoverSigner recovers the signer address from an EIP-712 digest and a 65-byte
// [R||S||V] signature via luxfi/crypto (secp256k1), normalizing V to {0,1}.
func recoverSigner(digest [32]byte, sigHex string) (string, error) {
	sig, err := hexBytes(sigHex)
	if err != nil {
		return "", invalid(reasonSignature, "signature is not hex: %v", err)
	}
	if len(sig) != 65 {
		return "", invalid(reasonSignature, "signature must be 65 bytes, got %d", len(sig))
	}
	norm := make([]byte, 65)
	copy(norm, sig)
	if norm[64] >= 27 { // EIP-155/legacy V=27/28 → luxfi/crypto wants 0/1
		norm[64] -= 27
	}
	if norm[64] > 1 {
		return "", invalid(reasonSignature, "invalid recovery id %d", norm[64])
	}
	pub, err := crypto.SigToPub(digest[:], norm)
	if err != nil {
		return "", invalid(reasonSignature, "recovery failed: %v", err)
	}
	return crypto.PubkeyToAddress(*pub).Hex(), nil
}

// ── small pure helpers ────────────────────────────────────────────────────────

func keccak(parts ...[]byte) [32]byte {
	var out [32]byte
	copy(out[:], crypto.Keccak256(parts...))
	return out
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func hexBytes(s string) ([]byte, error) { return hex.DecodeString(trim0x(s)) }

func addrBytes(addr string) []byte {
	b, _ := hex.DecodeString(trim0x(addr))
	return b
}

func nonce32(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hexBytes(s)
	if err != nil {
		return out, invalid(reasonPayload, "nonce is not hex: %v", err)
	}
	if len(b) == 0 || len(b) > 32 {
		return out, invalid(reasonPayload, "nonce must be 1..32 bytes, got %d", len(b))
	}
	copy(out[32-len(b):], b)
	return out, nil
}

func trim0x(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

// addressEqual compares two hex addresses case-insensitively, ignoring 0x.
func addressEqual(a, b string) bool {
	return strings.EqualFold(trim0x(a), trim0x(b))
}

// nowUnix is the clock, indirected so tests can pin time.
var nowUnix = func() int64 { return time.Now().Unix() }
