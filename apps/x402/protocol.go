// Package x402 is the Hanzo Cloud native x402 pay-per-use subsystem (HTTP 402):
// challenge → the client pays → proof submitted → verify → serve → settle.
//
// FLOW. A priced resource answers 402 with PaymentRequirements (what to pay + the
// recipient wallet). The client signs an ERC-3009 transferWithAuthorization over
// those terms and retries with the X-Payment header. The subsystem VERIFIES the
// EIP-712 signature (secp256k1 recovery via luxfi/crypto — the SAME primitive the
// wallets custody signs with), rejects a REPLAYED authorization (nonce dedup),
// SETTLES exactly once (payer debit through the metering spine so paid usage
// appears in billing/usage like any metered spend, plus a credit to the recipient
// wallet's ledger), and serves.
//
// SEAMS. The marketplace registry (another subsystem) owns the mapping
// resource→Terms (price + recipient wallet); x402 only enforces it (Registry +
// Publish). Recipient resolution rides the wallets subsystem
// (wallets.ResolvePaymentTarget). On-chain broadcast of the authorization is a
// Settler seam; the LIVE default is ledger settlement.
package x402

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/luxfi/crypto"
)

const (
	// Version is the x402 protocol version this subsystem speaks.
	Version = "1"

	// Scheme is the payment scheme: an ERC-3009 transferWithAuthorization the
	// client signs off-chain (EIP-712), settled on the ledger or on-chain.
	Scheme = "erc3009"

	// HeaderRequirements carries the PaymentRequirements on a 402 response.
	HeaderRequirements = "X-Payment-Required"
	// HeaderProof carries the client's signed Proof on the retry.
	HeaderProof = "X-Payment"
	// HeaderReceipt carries the settlement Receipt on a served (2xx) response.
	HeaderReceipt = "X-Payment-Receipt"

	// DefaultValidFor is the default authorization validity window, in seconds.
	DefaultValidFor = 300
)

// PaymentRequirements is the server's 402 challenge: exactly what the client must
// pay, and to whom, to access the resource. It is emitted both as the response
// body and the X-Payment-Required header.
type PaymentRequirements struct {
	Version  string `json:"version"`
	Scheme   string `json:"scheme"`
	Resource string `json:"resource"` // the priced resource id (canonical path)
	Network  string `json:"network"`
	ChainID  int64  `json:"chainId"`
	Token    string `json:"token"`    // ERC-3009 token contract (e.g. USDC)
	Payee    string `json:"payee"`    // recipient wallet address
	Amount   string `json:"amount"`   // token smallest-unit amount (decimal string)
	ValidFor int64  `json:"validFor"` // seconds the authorization may remain valid
}

// Proof is the client's signed ERC-3009 authorization — the "payment". Nonce is a
// client-chosen 32-byte value and is the REPLAY ANCHOR: an authorization settles
// at most once per (From, Nonce).
type Proof struct {
	From        string `json:"from"`        // payer address (recovers from Signature)
	To          string `json:"to"`          // must equal the requirements Payee
	Value       string `json:"value"`       // must equal the requirements Amount
	ValidAfter  int64  `json:"validAfter"`  // unix seconds
	ValidBefore int64  `json:"validBefore"` // unix seconds
	Nonce       string `json:"nonce"`       // 32-byte hex (0x optional)
	Signature   string `json:"signature"`   // 65-byte EIP-712 signature (hex)
}

// Receipt is the settlement record returned on the X-Payment-Receipt header and
// looked up via GET /v1/x402/settlements/:id.
type Receipt struct {
	ID         string `json:"id"`
	Resource   string `json:"resource"`
	Payer      string `json:"payer"` // payer ORG (the debited ledger)
	From       string `json:"from"`  // payer address
	Payee      string `json:"payee"` // recipient address
	PayeeOrg   string `json:"payeeOrg"`
	Amount     string `json:"amount"` // exact 18-dp USD (money.Amount string)
	Nonce      string `json:"nonce"`
	SettledVia string `json:"settledVia"` // "ledger" (live) | "chain" (seam)
	TxHash     string `json:"txHash,omitempty"`
	SettledAt  int64  `json:"settledAt"`
}

// MarshalHeader serializes v as a compact JSON header value.
func marshalHeader(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ParseProof decodes a Proof from the X-Payment header value.
func ParseProof(header string) (*Proof, error) {
	var p Proof
	if err := json.Unmarshal([]byte(header), &p); err != nil {
		return nil, fmt.Errorf("x402: invalid %s header: %w", HeaderProof, err)
	}
	if strings.TrimSpace(p.From) == "" || strings.TrimSpace(p.Signature) == "" || strings.TrimSpace(p.Nonce) == "" {
		return nil, fmt.Errorf("x402: incomplete payment: from, nonce, and signature are required")
	}
	return &p, nil
}

// expired reports whether now is past the authorization's validity window.
func (p Proof) expired(now int64) bool { return now > p.ValidBefore }

// notYetValid reports whether the authorization is not yet in its window.
func (p Proof) notYetValid(now int64) bool { return now < p.ValidAfter }

// TokenName / TokenVersion are the ERC-3009 EIP-712 domain fields. USDC uses
// ("USD Coin", "2"); operators pin them per token in Config.
const (
	DefaultTokenName    = "USD Coin"
	DefaultTokenVersion = "2"
)

// Verify checks that p is a well-formed, in-window, unaltered authorization for
// the requirements req: the To/Value match, the time window holds at now, and the
// EIP-712 signature recovers to p.From. It does NOT check replay (the store does)
// nor settle (the settler does) — one concern each.
func Verify(req PaymentRequirements, p Proof, tokenName, tokenVersion string, now int64) error {
	if !addressEqual(p.To, req.Payee) {
		return fmt.Errorf("x402: payee mismatch: authorized %s, required %s", p.To, req.Payee)
	}
	if strings.TrimSpace(p.Value) != strings.TrimSpace(req.Amount) {
		return fmt.Errorf("x402: amount mismatch: authorized %s, required %s", p.Value, req.Amount)
	}
	if p.notYetValid(now) {
		return fmt.Errorf("x402: authorization not yet valid")
	}
	if p.expired(now) {
		return fmt.Errorf("x402: authorization expired")
	}
	digest, err := eip712Digest(req, p, tokenName, tokenVersion)
	if err != nil {
		return err
	}
	signer, err := recoverSigner(digest, p.Signature)
	if err != nil {
		return err
	}
	if !addressEqual(signer, p.From) {
		return fmt.Errorf("x402: signer mismatch: recovered %s, claimed %s", signer, p.From)
	}
	return nil
}

// eip712Digest computes the EIP-712 typed-data hash a client signs for an ERC-3009
// TransferWithAuthorization, bound to the token's domain (name, version, chainId,
// verifyingContract=token). Keccak is luxfi/crypto's — the ONE hash the binary uses.
func eip712Digest(req PaymentRequirements, p Proof, tokenName, tokenVersion string) ([32]byte, error) {
	domain := domainSeparator(tokenName, tokenVersion, req.ChainID, req.Token)

	typeHash := keccak([]byte(
		"TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"))

	value, ok := new(big.Int).SetString(strings.TrimSpace(p.Value), 10)
	if !ok {
		return [32]byte{}, fmt.Errorf("x402: invalid value %q", p.Value)
	}
	nonce, err := nonce32(p.Nonce)
	if err != nil {
		return [32]byte{}, err
	}

	var enc []byte
	enc = append(enc, typeHash[:]...)
	enc = append(enc, pad32(addrBytes(p.From))...)
	enc = append(enc, pad32(addrBytes(p.To))...)
	enc = append(enc, pad32(value.Bytes())...)
	enc = append(enc, pad32(big.NewInt(p.ValidAfter).Bytes())...)
	enc = append(enc, pad32(big.NewInt(p.ValidBefore).Bytes())...)
	enc = append(enc, nonce[:]...)
	structHash := keccak(enc)

	var in []byte
	in = append(in, 0x19, 0x01)
	in = append(in, domain[:]...)
	in = append(in, structHash[:]...)
	return keccak(in), nil
}

// domainSeparator hashes the EIP-712 domain for an ERC-3009 token.
func domainSeparator(name, version string, chainID int64, token string) [32]byte {
	typeHash := keccak([]byte(
		"EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash := keccak([]byte(name))
	versionHash := keccak([]byte(version))
	var enc []byte
	enc = append(enc, typeHash[:]...)
	enc = append(enc, nameHash[:]...)
	enc = append(enc, versionHash[:]...)
	enc = append(enc, pad32(big.NewInt(chainID).Bytes())...)
	enc = append(enc, pad32(addrBytes(token))...)
	return keccak(enc)
}

// recoverSigner recovers the signer address from an EIP-712 digest and a 65-byte
// [R||S||V] signature via luxfi/crypto (secp256k1), normalizing V to {0,1}.
func recoverSigner(digest [32]byte, sigHex string) (string, error) {
	sig, err := hexBytes(sigHex)
	if err != nil {
		return "", fmt.Errorf("x402: signature not hex: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("x402: signature must be 65 bytes, got %d", len(sig))
	}
	norm := make([]byte, 65)
	copy(norm, sig)
	if norm[64] >= 27 { // EIP-155/legacy V=27/28 → luxfi/crypto wants 0/1
		norm[64] -= 27
	}
	if norm[64] > 1 {
		return "", fmt.Errorf("x402: invalid recovery id %d", norm[64])
	}
	pub, err := crypto.SigToPub(digest[:], norm)
	if err != nil {
		return "", fmt.Errorf("x402: signature recovery failed: %w", err)
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
		return out, fmt.Errorf("x402: nonce not hex: %w", err)
	}
	if len(b) == 0 || len(b) > 32 {
		return out, fmt.Errorf("x402: nonce must be 1..32 bytes, got %d", len(b))
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
