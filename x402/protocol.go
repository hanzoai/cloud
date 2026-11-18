// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package x402 implements permissionless HTTP 402 wallet-native inference
// payments, ERC-3009-style non-custodial transfer authorizations, multi-facilitator
// settlement, replay protection, and receipt reconciliation (HIP-1350, #16).
//
// References:
//   - HTTP x402 Payment Required protocol (Apache-2.0)
//   - EIP-3009: Transfer With Authorization (CC0-1.0)
package x402

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ProtocolVersion = 1
	HeaderPayment   = "X-Payment"
	HeaderReceipt   = "X-Payment-Response"
	HeaderIdemKey   = "Idempotency-Key"
)

// LedgerStatus represents the deterministic lifecycle of an x402 payment.
type LedgerStatus string

const (
	StatusAuthorized      LedgerStatus = "authorized"
	StatusSettled         LedgerStatus = "settled"
	StatusInferenceFailed LedgerStatus = "inference_failed"
	StatusReversed        LedgerStatus = "reversed"
)

// Quote is an exact, provider-signed stablecoin price offer returned in an
// HTTP 402 challenge. Neither Hanzo registration nor wallet custody is required.
type Quote struct {
	QuoteID           string   `json:"quote_id"`
	ProviderID        string   `json:"provider_id"`
	PayTo             string   `json:"pay_to"`
	Asset             string   `json:"asset"`
	ChainID           string   `json:"chain_id"`
	ExactAmountAtomic uint64   `json:"exact_amount_atomic"`
	MaxTokens         uint64   `json:"max_tokens"`
	ResourcePath      string   `json:"resource_path"`
	RequestBodyHash   string   `json:"request_body_hash"`
	ExpiresAtUnix     int64    `json:"expires_at_unix"`
	Facilitators      []string `json:"facilitators"`
	ProviderPubKeyHex string   `json:"provider_pubkey_hex"`
	ProviderSigHex    string   `json:"provider_sig_hex"`
}

// CanonicalBytes returns the deterministic domain-separated message signed by
// the provider over a Quote.
func (q Quote) CanonicalBytes() []byte {
	facs := strings.Join(q.Facilitators, ",")
	msg := fmt.Sprintf(
		"hanzo-x402-quote/v1|id=%s|provider=%s|to=%s|asset=%s|chain=%s|amount=%d|tokens=%d|path=%s|reqhash=%s|exp=%d|fac=%s",
		q.QuoteID, q.ProviderID, q.PayTo, q.Asset, q.ChainID,
		q.ExactAmountAtomic, q.MaxTokens, q.ResourcePath, q.RequestBodyHash,
		q.ExpiresAtUnix, facs,
	)
	sum := sha256.Sum256([]byte(msg))
	return sum[:]
}

// SignQuote signs q in-place using the provider's Ed25519 private key.
func SignQuote(q *Quote, priv ed25519.PrivateKey) {
	pub := priv.Public().(ed25519.PublicKey)
	q.ProviderPubKeyHex = hex.EncodeToString(pub)
	sig := ed25519.Sign(priv, q.CanonicalBytes())
	q.ProviderSigHex = hex.EncodeToString(sig)
}

// VerifyQuoteSignature checks the provider's cryptographic signature against
// expectedPubKey (when non-empty).
func VerifyQuoteSignature(q Quote, expectedPubKey ed25519.PublicKey) error {
	pubBytes, err := hex.DecodeString(q.ProviderPubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return errors.New("invalid provider public key encoding")
	}
	if len(expectedPubKey) == ed25519.PublicKeySize && !ed25519.PublicKey(pubBytes).Equal(expectedPubKey) {
		return errors.New("quote signer does not match provider key")
	}
	sigBytes, err := hex.DecodeString(q.ProviderSigHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return errors.New("invalid provider quote signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), q.CanonicalBytes(), sigBytes) {
		return errors.New("invalid provider quote signature")
	}
	return nil
}

// PaymentAuthorization models an ERC-3009 TransferWithAuthorization signed
// directly by the customer's self-custodial wallet.
type PaymentAuthorization struct {
	Version         int    `json:"x402_version"`
	Quote           Quote  `json:"quote"`
	FacilitatorID   string `json:"facilitator_id"`
	From            string `json:"from"`
	To              string `json:"to"`
	Asset           string `json:"asset"`
	ChainID         string `json:"chain_id"`
	ValueAtomic     uint64 `json:"value_atomic"`
	ValidAfterUnix  int64  `json:"valid_after_unix"`
	ValidBeforeUnix int64  `json:"valid_before_unix"`
	Nonce           string `json:"nonce"`
	IdempotencyKey  string `json:"idempotency_key"`
	RequestBodyHash string `json:"request_body_hash"`
	PayerPubKeyHex  string `json:"payer_pubkey_hex"`
	SignatureHex    string `json:"signature_hex"`
}

// ERC3009Digest computes the domain-separated TransferWithAuthorization digest
// binding chain, token asset, sender, recipient, exact value, validity window,
// random 32-byte nonce, quote ID, and request body hash.
func (a PaymentAuthorization) ERC3009Digest() []byte {
	msg := fmt.Sprintf(
		"ERC3009:TransferWithAuthorization/v1|chain=%s|asset=%s|from=%s|to=%s|value=%d|after=%d|before=%d|nonce=%s|quote=%s|reqhash=%s",
		a.ChainID, a.Asset, a.From, a.To, a.ValueAtomic,
		a.ValidAfterUnix, a.ValidBeforeUnix, a.Nonce,
		a.Quote.QuoteID, a.RequestBodyHash,
	)
	sum := sha256.Sum256([]byte(msg))
	return sum[:]
}

// SignAuthorization signs auth using the customer's Ed25519 wallet key.
func SignAuthorization(auth *PaymentAuthorization, payerPriv ed25519.PrivateKey) {
	pub := payerPriv.Public().(ed25519.PublicKey)
	auth.PayerPubKeyHex = hex.EncodeToString(pub)
	sig := ed25519.Sign(payerPriv, auth.ERC3009Digest())
	auth.SignatureHex = hex.EncodeToString(sig)
}

// VerifyAuthorization validates quote integrity, exact price terms, validity
// bounds, request body binding, and the payer's non-custodial signature.
func VerifyAuthorization(auth PaymentAuthorization, providerPub ed25519.PublicKey, reqBodyHash string, nowUnix int64) error {
	if auth.Version != ProtocolVersion {
		return fmt.Errorf("unsupported x402 version %d", auth.Version)
	}
	if err := VerifyQuoteSignature(auth.Quote, providerPub); err != nil {
		return fmt.Errorf("quote verification: %w", err)
	}
	if nowUnix >= auth.Quote.ExpiresAtUnix {
		return errors.New("quote expired")
	}
	if auth.From == "" || auth.Nonce == "" || len(auth.Nonce) < 16 {
		return errors.New("missing payer address or 32-byte nonce")
	}
	if auth.To != auth.Quote.PayTo {
		return errors.New("authorization recipient does not match quote pay_to")
	}
	if auth.Asset != auth.Quote.Asset || auth.ChainID != auth.Quote.ChainID {
		return errors.New("authorization asset or chain_id mismatch")
	}
	if auth.ValueAtomic != auth.Quote.ExactAmountAtomic {
		return fmt.Errorf("authorization value %d does not match exact quote %d", auth.ValueAtomic, auth.Quote.ExactAmountAtomic)
	}
	if reqBodyHash != "" && (auth.RequestBodyHash != reqBodyHash || auth.Quote.RequestBodyHash != reqBodyHash) {
		return errors.New("request body hash mismatch against signed quote/authorization")
	}
	if nowUnix < auth.ValidAfterUnix || nowUnix >= auth.ValidBeforeUnix {
		return errors.New("authorization outside [valid_after, valid_before) window")
	}

	allowedFac := false
	for _, fid := range auth.Quote.Facilitators {
		if fid == auth.FacilitatorID {
			allowedFac = true
			break
		}
	}
	if !allowedFac {
		return fmt.Errorf("facilitator %q not accepted by quote", auth.FacilitatorID)
	}

	pubBytes, err := hex.DecodeString(auth.PayerPubKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return errors.New("invalid payer public key")
	}
	// Bind From address deterministically to payer public key prefix when formatted as 0x<hex>.
	expectedAddr := DeriveWalletAddress(ed25519.PublicKey(pubBytes))
	if !strings.EqualFold(auth.From, expectedAddr) {
		return errors.New("payer from address does not match signing public key")
	}
	sigBytes, err := hex.DecodeString(auth.SignatureHex)
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		return errors.New("invalid payer signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), auth.ERC3009Digest(), sigBytes) {
		return errors.New("invalid payer ERC-3009 authorization signature")
	}
	return nil
}

// DeriveWalletAddress derives a canonical 20-byte 0x-prefixed wallet identifier
// from an Ed25519 public key so non-custodial ownership is self-certifying.
func DeriveWalletAddress(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "0x" + hex.EncodeToString(h[:20])
}

// HashRequestBody returns the hex SHA-256 of raw HTTP request bytes.
func HashRequestBody(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// EncodePaymentHeader serializes a signed PaymentAuthorization for X-Payment.
func EncodePaymentHeader(auth PaymentAuthorization) (string, error) {
	raw, err := json.Marshal(auth)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DecodePaymentHeader parses a base64-encoded X-Payment header.
func DecodePaymentHeader(hdr string) (PaymentAuthorization, error) {
	var auth PaymentAuthorization
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(hdr))
	if err != nil {
		return auth, fmt.Errorf("decode X-Payment base64: %w", err)
	}
	if err := json.Unmarshal(raw, &auth); err != nil {
		return auth, fmt.Errorf("decode X-Payment JSON: %w", err)
	}
	return auth, nil
}

// PaymentRequiredResponse is the HTTP 402 body returned to unauthenticated
// wallet-native callers.
type PaymentRequiredResponse struct {
	X402Version int    `json:"x402_version"`
	Error       string `json:"error"`
	Quote       Quote  `json:"quote"`
}
