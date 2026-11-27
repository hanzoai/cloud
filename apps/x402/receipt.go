package x402

// receipt.go holds what is this rail's own, as distinct from the protocol: the
// settlement record it keeps for its ledger, the refusal reasons the specification
// has no word for, and the small helpers its ledger settlement uses. The protocol
// itself — wire types, header codec, verification, signing — is the one in
// github.com/hanzoai/commerce/payment/x402, imported here as protocol.

import (
	"strings"
	"time"

	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"

	protocol "github.com/hanzoai/commerce/payment/x402"
)

// Reasons with no entry in the specification: it has no word for "you have not
// paid yet" (the ordinary first leg of the flow), for a spent nonce, for a
// resource with nothing to pay, for a payer who may not pay, or for the rail being
// unable to enforce at all. They ride the same fields the specification's reasons
// do, and a client that does not know one treats it as any other refusal.
const (
	reasonRequired      = "payment_required"
	reasonUnpriced      = "unpriced"
	reasonReplay        = "nonce_replayed"
	reasonUnbillable    = "unbillable"
	reasonSupport       = "support_session"
	reasonPayee         = "payee_unavailable"
	reasonUnavailable   = "x402_unavailable"
	reasonUnenforceable = "x402_unenforceable"
)

// reasonOf is the reason an error carries, and the specification's
// unexpected-settle reason for anything that is not a verification failure.
func reasonOf(err error) string { return protocol.ReasonOf(err, protocol.ReasonSettleError) }

// encode renders v as a header value. A value that cannot be encoded is logged and
// leaves the header empty: the three wire types hold only strings, numbers and
// the extension JSON a client sent, so the failure is a bad extension, not a state
// worth refusing a payment over.
func encode(v any) string {
	s, err := protocol.EncodeHeader(v)
	if err != nil {
		if l := luxlog.Default(); l != nil {
			l.Warn("x402: header not encoded", "err", err)
		}
	}
	return s
}

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
	// Category is what the payment was for — service, goods, transfer or royalty
	// — as the price table said it: a tool call is a service, a job is what its
	// parties agreed.
	Category string `json:"category,omitempty"`
	// SettledAt is when this settlement was CLAIMED, in unix seconds — the moment
	// the authorization was accepted, which is also the moment the time window it
	// carried stopped applying. A settlement finished later by reconciliation
	// keeps this instant.
	SettledAt int64 `json:"settledAt"`
}

// nowUnix is the clock, indirected so tests can pin time.
var nowUnix = func() int64 { return time.Now().Unix() }

// atTime is the instant protocol.InWindow reads, taken from the same clock.
func atTime() time.Time { return time.Unix(nowUnix(), 0) }

// untilOf is when an authorization that has passed protocol.Verify stops being
// acceptable, unix seconds. Verify read every field strictly, so a failure here
// cannot happen; it reads as zero, the earliest instant, which every window check
// refuses.
func untilOf(a protocol.Authorization) int64 {
	p, err := a.Parse()
	if err != nil {
		return 0
	}
	return int64(p.ValidBefore)
}

func keccak(parts ...[]byte) [32]byte {
	var out [32]byte
	copy(out[:], crypto.Keccak256(parts...))
	return out
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
