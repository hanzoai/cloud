// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/luxfi/crypto"
)

// QuoteVersion pins the canonical domain-separated hash format of a signed quote.
const QuoteVersion = "x402-quote/v1"

// QuoteRequest supplies the terms a provider binds into a SignedPriceQuote.
type QuoteRequest struct {
	Resource        string `json:"resource"`
	Model           string `json:"model,omitempty"`
	MaxTokens       int    `json:"maxTokens,omitempty"`
	RequestBodyHash string `json:"requestBodyHash"`
	Network         string `json:"network"`
	Asset           string `json:"asset"`
	AmountAtomic    string `json:"amountAtomic"`
	PayTo           string `json:"payTo"`
	TTLSeconds      int64  `json:"ttlSeconds,omitempty"`
}

// SignedPriceQuote is an exact price quote signed by the provider's secp256k1
// key and bound to the SHA-256 digest of the request body. Neither a facilitator
// nor an untrusted proxy can alter price, payee, model, token budget, expiry, or
// request payload without invalidating ECDSA recovery.
type SignedPriceQuote struct {
	QuoteID         string `json:"quoteId"`
	Version         string `json:"version"`
	Resource        string `json:"resource"`
	Model           string `json:"model,omitempty"`
	MaxTokens       int    `json:"maxTokens,omitempty"`
	RequestBodyHash string `json:"requestBodyHash"`
	Network         string `json:"network"`
	Asset           string `json:"asset"`
	AmountAtomic    string `json:"amountAtomic"`
	PayTo           string `json:"payTo"`
	IssuedAt        int64  `json:"issuedAt"`
	ExpiresAt       int64  `json:"expiresAt"`
	ProviderAddress string `json:"providerAddress"`
	Signature       string `json:"signature"`
}

var quoteTypeHash = crypto.Keccak256([]byte(
	"SignedPriceQuote(string version,string quoteId,string resource,string model,uint256 maxTokens,string requestBodyHash,string network,address asset,uint256 amountAtomic,address payTo,uint256 issuedAt,uint256 expiresAt,address providerAddress)",
))

var quoteDomainHash = crypto.Keccak256([]byte("HanzoX402SignedPriceQuote:v1"))

// HashRequestBody computes the canonical SHA-256 commitment over a request body.
func HashRequestBody(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Digest computes the deterministic Keccak-256 domain-separated digest over
// every signed field of q. Any single-bit mutation changes the digest.
func (q SignedPriceQuote) Digest() ([32]byte, error) {
	var out [32]byte
	if q.Version != QuoteVersion {
		return out, fmt.Errorf("x402: unsupported quote version %q", q.Version)
	}
	if q.QuoteID == "" || q.Resource == "" || q.Network == "" || q.AmountAtomic == "" {
		return out, errors.New("x402: quote missing required fields")
	}
	asset, err := protocol.Address(q.Asset)
	if err != nil {
		return out, fmt.Errorf("x402: quote asset: %w", err)
	}
	payTo, err := protocol.Address(q.PayTo)
	if err != nil {
		return out, fmt.Errorf("x402: quote payTo: %w", err)
	}
	provider, err := protocol.Address(q.ProviderAddress)
	if err != nil {
		return out, fmt.Errorf("x402: quote providerAddress: %w", err)
	}
	amt, ok := new(big.Int).SetString(q.AmountAtomic, 10)
	if !ok || amt.Sign() <= 0 || amt.BitLen() > 256 {
		return out, fmt.Errorf("x402: quote invalid amountAtomic %q", q.AmountAtomic)
	}
	if q.MaxTokens < 0 || q.IssuedAt < 0 || q.ExpiresAt <= q.IssuedAt {
		return out, errors.New("x402: quote has invalid token limit or validity window")
	}

	var buf []byte
	buf = append(buf, quoteTypeHash...)
	buf = append(buf, crypto.Keccak256([]byte(q.Version))...)
	buf = append(buf, crypto.Keccak256([]byte(q.QuoteID))...)
	buf = append(buf, crypto.Keccak256([]byte(q.Resource))...)
	buf = append(buf, crypto.Keccak256([]byte(q.Model))...)
	buf = append(buf, padUint256(new(big.Int).SetInt64(int64(q.MaxTokens)))...)
	buf = append(buf, crypto.Keccak256([]byte(q.RequestBodyHash))...)
	buf = append(buf, crypto.Keccak256([]byte(q.Network))...)
	buf = append(buf, padAddress(asset[:])...)
	buf = append(buf, padUint256(amt)...)
	buf = append(buf, padAddress(payTo[:])...)
	buf = append(buf, padUint256(new(big.Int).SetInt64(q.IssuedAt))...)
	buf = append(buf, padUint256(new(big.Int).SetInt64(q.ExpiresAt))...)
	buf = append(buf, padAddress(provider[:])...)

	structHash := crypto.Keccak256(buf)
	copy(out[:], crypto.Keccak256([]byte{0x19, 0x01}, quoteDomainHash, structHash))
	return out, nil
}

// ECDSAQuoteSigner signs and verifies SignedPriceQuote instances using a
// provider's secp256k1 ECDSA key.
type ECDSAQuoteSigner struct {
	Key        *ecdsa.PrivateKey
	DefaultTTL time.Duration
	Now        func() time.Time
}

// NewECDSAQuoteSigner creates a quote signer backed by a secp256k1 key.
func NewECDSAQuoteSigner(key *ecdsa.PrivateKey, defaultTTL time.Duration) (*ECDSAQuoteSigner, error) {
	if key == nil {
		return nil, errors.New("x402: quote signer requires a secp256k1 key")
	}
	if defaultTTL <= 0 {
		defaultTTL = 60 * time.Second
	}
	return &ECDSAQuoteSigner{Key: key, DefaultTTL: defaultTTL}, nil
}

func (s *ECDSAQuoteSigner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ProviderAddress returns the lowercase 0x-prefixed Ethereum address for s.Key.
func (s *ECDSAQuoteSigner) ProviderAddress() string {
	return strings.ToLower(crypto.PubkeyToAddress(s.Key.PublicKey).Hex())
}

// SignQuote builds and signs an exact price quote bound to req.
func (s *ECDSAQuoteSigner) SignQuote(_ context.Context, req QuoteRequest) (SignedPriceQuote, error) {
	if s.Key == nil {
		return SignedPriceQuote{}, errors.New("x402: quote signer key is nil")
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return SignedPriceQuote{}, fmt.Errorf("x402: quote id: %w", err)
	}
	now := s.now().UTC().Unix()
	ttl := int64(s.DefaultTTL.Seconds())
	if req.TTLSeconds > 0 {
		ttl = req.TTLSeconds
	}
	if ttl <= 0 {
		ttl = 60
	}
	q := SignedPriceQuote{
		QuoteID:         "q_" + hex.EncodeToString(idBytes[:]),
		Version:         QuoteVersion,
		Resource:        req.Resource,
		Model:           req.Model,
		MaxTokens:       req.MaxTokens,
		RequestBodyHash: req.RequestBodyHash,
		Network:         req.Network,
		Asset:           strings.ToLower(req.Asset),
		AmountAtomic:    strings.TrimSpace(req.AmountAtomic),
		PayTo:           strings.ToLower(req.PayTo),
		IssuedAt:        now,
		ExpiresAt:       now + ttl,
		ProviderAddress: s.ProviderAddress(),
	}
	digest, err := q.Digest()
	if err != nil {
		return SignedPriceQuote{}, err
	}
	sig, err := crypto.Sign(digest[:], s.Key)
	if err != nil {
		return SignedPriceQuote{}, fmt.Errorf("x402: sign quote: %w", err)
	}
	canonicalSig, err := protocol.Canonical(protocol.Hex(sig))
	if err != nil {
		return SignedPriceQuote{}, fmt.Errorf("x402: canonical quote signature: %w", err)
	}
	q.Signature = canonicalSig
	return q, nil
}

// VerifyQuote checks that quote was signed by this provider, is unexpired at
// now, and matches reqBodyHash.
func (s *ECDSAQuoteSigner) VerifyQuote(quote SignedPriceQuote, reqBodyHash string, now time.Time) error {
	return VerifySignedQuoteStandalone(quote, s.ProviderAddress(), reqBodyHash, now)
}

// VerifySignedQuoteStandalone verifies a SignedPriceQuote without requiring the
// provider's private key, allowing agents and third-party auditors to check
// quote authenticity independently.
func VerifySignedQuoteStandalone(quote SignedPriceQuote, expectedProvider string, reqBodyHash string, now time.Time) error {
	if quote.Signature == "" {
		return &protocol.Error{Reason: protocol.ReasonSignature, Message: "signed quote missing signature"}
	}
	if now.IsZero() {
		now = time.Now()
	}
	ts := now.UTC().Unix()
	if ts < quote.IssuedAt {
		return &protocol.Error{Reason: protocol.ReasonValidAfter, Message: "signed quote not yet valid"}
	}
	if ts >= quote.ExpiresAt {
		return &protocol.Error{Reason: protocol.ReasonValidBefore, Message: "signed quote expired"}
	}
	if reqBodyHash != "" && quote.RequestBodyHash != reqBodyHash {
		return &protocol.Error{
			Reason:  protocol.ReasonInvalidRequirements,
			Message: fmt.Sprintf("signed quote requestBodyHash %q does not match request %q", quote.RequestBodyHash, reqBodyHash),
		}
	}
	digest, err := quote.Digest()
	if err != nil {
		return &protocol.Error{Reason: protocol.ReasonInvalidRequirements, Message: err.Error()}
	}
	recovered, err := protocol.Recover(digest, quote.Signature)
	if err != nil {
		return &protocol.Error{Reason: protocol.ReasonSignature, Message: fmt.Sprintf("quote signature invalid: %v", err)}
	}
	declared, err := protocol.Address(quote.ProviderAddress)
	if err != nil {
		return &protocol.Error{Reason: protocol.ReasonSignature, Message: "quote providerAddress invalid"}
	}
	if recovered != declared {
		return &protocol.Error{
			Reason:  protocol.ReasonSignature,
			Message: fmt.Sprintf("quote signer %s does not match declared provider %s", protocol.Hex(recovered[:]), quote.ProviderAddress),
		}
	}
	if expectedProvider != "" {
		want, err := protocol.Address(expectedProvider)
		if err != nil {
			return &protocol.Error{Reason: protocol.ReasonSignature, Message: "expected provider address invalid"}
		}
		if recovered != want {
			return &protocol.Error{
				Reason:  protocol.ReasonSignature,
				Message: fmt.Sprintf("quote signer %s does not match expected provider %s", protocol.Hex(recovered[:]), expectedProvider),
			}
		}
	}
	return nil
}

// AttachQuoteExtension inserts q into an x402 v2 Extensions map under ExtensionSignedQuote.
func AttachQuoteExtension(ext protocol.Extensions, q SignedPriceQuote) (protocol.Extensions, error) {
	raw, err := json.Marshal(q)
	if err != nil {
		return nil, fmt.Errorf("x402: marshal signed quote: %w", err)
	}
	out := make(protocol.Extensions, len(ext)+1)
	for k, v := range ext {
		out[k] = v
	}
	out[ExtensionSignedQuote] = raw
	return out, nil
}

// ExtractQuoteExtension reads a SignedPriceQuote from an x402 v2 Extensions map.
func ExtractQuoteExtension(ext protocol.Extensions) (SignedPriceQuote, bool, error) {
	if len(ext) == 0 {
		return SignedPriceQuote{}, false, nil
	}
	raw, ok := ext[ExtensionSignedQuote]
	if !ok || len(raw) == 0 {
		return SignedPriceQuote{}, false, nil
	}
	var q SignedPriceQuote
	if err := json.Unmarshal(raw, &q); err != nil {
		return SignedPriceQuote{}, true, fmt.Errorf("x402: decode signed quote extension: %w", err)
	}
	return q, true, nil
}

func padAddress(b []byte) []byte {
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func padUint256(v *big.Int) []byte {
	out := make([]byte, 32)
	if v != nil && v.Sign() > 0 {
		v.FillBytes(out)
	}
	return out
}
