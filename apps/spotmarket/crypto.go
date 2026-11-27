// SPDX-License-Identifier: Apache-2.0 OR MIT

package spotmarket

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sort"

	"github.com/luxfi/crypto/secp256k1"
)

var (
	curveOrderN     = new(big.Int).Set(secp256k1.S256().Params().N)
	curveHalfOrderN = new(big.Int).Rsh(new(big.Int).Set(secp256k1.S256().Params().N), 1)
)

// KeyPair wraps a secp256k1 signing keypair with its 33-byte compressed public
// key representation.
type KeyPair struct {
	PrivateKey    *secp256k1.PrivateKey
	PubKeyBytes   []byte
	PubKeyHex     string
	ProviderLabel string
}

// GenerateKeyPair creates a fresh random secp256k1 keypair.
func GenerateKeyPair() (*KeyPair, error) {
	priv, err := secp256k1.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("spotmarket: generate secp256k1 key: %w", err)
	}
	pub := priv.PublicKey().Bytes()
	return &KeyPair{
		PrivateKey:  priv,
		PubKeyBytes: pub,
		PubKeyHex:   hex.EncodeToString(pub),
	}, nil
}

// DeriveKeyPair deterministically derives a valid secp256k1 keypair from an
// arbitrary domain-separated seed. Used for reproducible simulations and tests
// without embedding any static secret literals.
func DeriveKeyPair(domain string, seed uint64, index uint64) (*KeyPair, error) {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, ProtocolVersion)
	writeLenPrefixed(&buf, domain)
	writeUint64(&buf, seed)
	writeUint64(&buf, index)

	for attempt := uint64(0); attempt < 1024; attempt++ {
		h := sha256.New()
		h.Write(buf.Bytes())
		var a [8]byte
		binary.BigEndian.PutUint64(a[:], attempt)
		h.Write(a[:])
		sum := h.Sum(nil)

		d := new(big.Int).SetBytes(sum)
		if d.Sign() <= 0 || d.Cmp(curveOrderN) >= 0 {
			continue
		}
		priv, err := secp256k1.ToPrivateKey(sum)
		if err != nil {
			continue
		}
		pub := priv.PublicKey().Bytes()
		return &KeyPair{
			PrivateKey:  priv,
			PubKeyBytes: pub,
			PubKeyHex:   hex.EncodeToString(pub),
		}, nil
	}
	return nil, fmt.Errorf("spotmarket: failed to derive secp256k1 keypair")
}

// SignDigestLowS signs a 32-byte digest with secp256k1 and enforces EIP-2
// low-S canonical normalization (1 <= S <= N/2, V in {0, 1}).
func SignDigestLowS(priv *secp256k1.PrivateKey, digest [32]byte) ([65]byte, error) {
	var out [65]byte
	if priv == nil {
		return out, ErrInvalidSignature
	}
	sig, err := secp256k1.Sign(digest[:], priv.Bytes())
	if err != nil || len(sig) != 65 {
		return out, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	s := new(big.Int).SetBytes(sig[32:64])
	if s.Cmp(curveHalfOrderN) > 0 {
		// Normalize to low-S per EIP-2: S' = N - S, flip recovery parity bit.
		s.Sub(curveOrderN, s)
		sBytes := s.FillBytes(make([]byte, 32))
		copy(sig[32:64], sBytes)
		sig[64] ^= 1
	}
	copy(out[:], sig)
	return out, nil
}

// VerifyDigestLowS verifies a 65-byte [R || S || V] secp256k1 signature over a
// 32-byte digest against a 33-byte compressed public key. It fails closed on
// malformed keys/signatures, EIP-2 high-S malleation, curve verification
// failure, or recovery-key mismatch.
func VerifyDigestLowS(compressedPubKey []byte, digest [32]byte, sig []byte) error {
	if len(compressedPubKey) != 33 {
		return fmt.Errorf("%w: compressed pubkey must be 33 bytes", ErrInvalidSignature)
	}
	if len(sig) != 65 {
		return fmt.Errorf("%w: signature must be 65 bytes", ErrInvalidSignature)
	}
	if sig[64] > 1 {
		return fmt.Errorf("%w: invalid recovery id %d", ErrInvalidSignature, sig[64])
	}

	r := new(big.Int).SetBytes(sig[0:32])
	s := new(big.Int).SetBytes(sig[32:64])
	if r.Sign() <= 0 || r.Cmp(curveOrderN) >= 0 {
		return fmt.Errorf("%w: R out of range", ErrInvalidSignature)
	}
	if s.Sign() <= 0 || s.Cmp(curveOrderN) >= 0 {
		return fmt.Errorf("%w: S out of range", ErrInvalidSignature)
	}
	if s.Cmp(curveHalfOrderN) > 0 {
		return ErrHighSMalleableSignature
	}

	if !secp256k1.VerifySignature(compressedPubKey, digest[:], sig[:64]) {
		return ErrInvalidSignature
	}

	uncompressed, err := secp256k1.RecoverPubkey(digest[:], sig)
	if err != nil || len(uncompressed) != 65 || uncompressed[0] != 0x04 {
		return ErrInvalidSignature
	}
	rx := new(big.Int).SetBytes(uncompressed[1:33])
	ry := new(big.Int).SetBytes(uncompressed[33:65])
	recoveredCompressed := secp256k1.CompressPubkey(rx, ry)
	if !bytes.Equal(recoveredCompressed, compressedPubKey) {
		return ErrInvalidSignature
	}
	return nil
}

// MalleateSignatureHighS converts a valid low-S signature into its curve-valid
// high-S twin (S' = N - S, V' = V ^ 1). Used strictly in tests and adversarial
// simulations to prove EIP-2 malleability rejection.
func MalleateSignatureHighS(sigHex string) (string, error) {
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != 65 {
		return "", ErrInvalidSignature
	}
	s := new(big.Int).SetBytes(sig[32:64])
	if s.Cmp(curveHalfOrderN) <= 0 {
		s.Sub(curveOrderN, s)
		copy(sig[32:64], s.FillBytes(make([]byte, 32)))
		sig[64] ^= 1
	}
	return hex.EncodeToString(sig), nil
}

// OfferCommitment computes the deterministic domain-separated SHA-256 digest
// of an Offer. SupportedModels is sorted during hashing so encoding is
// canonical regardless of slice ordering.
func OfferCommitment(o Offer) [32]byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, "HIP1350:OFFER:V1")
	writeLenPrefixed(&buf, o.ProtocolVersion)
	writeLenPrefixed(&buf, o.OfferID)
	writeLenPrefixed(&buf, o.ProviderID)
	writeLenPrefixed(&buf, o.ProviderPubKeyHex)
	writeLenPrefixed(&buf, o.OperatorClass)

	// Hardware
	writeLenPrefixed(&buf, o.Hardware.GPUModel)
	writeInt64(&buf, int64(o.Hardware.GPUCount))
	writeInt64(&buf, int64(o.Hardware.AvailableVRAMGB))
	writeInt64(&buf, int64(o.Hardware.TotalVRAMGB))
	writeLenPrefixed(&buf, o.Hardware.Interconnect)
	writeUint64(&buf, math.Float64bits(o.Hardware.FP8TFLOPS))

	// Supported models (canonical sorted copy)
	models := append([]string(nil), o.SupportedModels...)
	sort.Strings(models)
	writeUint64(&buf, uint64(len(models)))
	for _, m := range models {
		writeLenPrefixed(&buf, m)
	}

	// Location
	writeLenPrefixed(&buf, o.Location.Region)
	writeLenPrefixed(&buf, o.Location.Jurisdiction)
	writeInt64(&buf, int64(o.Location.DatacenterTier))

	// Pricing
	writeInt64(&buf, o.Pricing.MicroUSDPer1MTokens)
	writeInt64(&buf, o.Pricing.MicroUSDPerGPUHour)
	writeInt64(&buf, o.Pricing.MinBillableTokens)

	// SLO
	writeInt64(&buf, o.SLO.TargetTTFTMs)
	writeUint64(&buf, math.Float64bits(o.SLO.MinThroughputTokPerSec))
	writeInt64(&buf, int64(o.SLO.UptimeBPS))
	writeInt64(&buf, o.SLO.CollateralMicroUSD)
	writeInt64(&buf, int64(o.SLO.SlashBPSOnBreach))

	writeInt64(&buf, int64(o.AvailableSlots))
	writeInt64(&buf, o.IssuedAtUnix)
	writeInt64(&buf, o.ExpiresAtUnix)
	writeUint64(&buf, o.Nonce)
	writeLenPrefixed(&buf, string(o.AttestationClass))
	writeLenPrefixed(&buf, o.AttestationQuoteHash)

	return sha256.Sum256(buf.Bytes())
}

// SignOffer constructs a cryptographically signed offer using secp256k1 low-S.
func SignOffer(priv *secp256k1.PrivateKey, offer Offer) (SignedOffer, error) {
	if offer.ProtocolVersion == "" {
		offer.ProtocolVersion = ProtocolVersion
	}
	if priv == nil {
		return SignedOffer{}, ErrInvalidSignature
	}
	pubHex := hex.EncodeToString(priv.PublicKey().Bytes())
	if offer.ProviderPubKeyHex == "" {
		offer.ProviderPubKeyHex = pubHex
	}
	if offer.AttestationClass == "" {
		offer.AttestationClass = VerificationCryptoSignature
	}
	// Canonicalize SupportedModels slice in-place on the signed copy.
	models := append([]string(nil), offer.SupportedModels...)
	sort.Strings(models)
	offer.SupportedModels = models

	digest := OfferCommitment(offer)
	sig, err := SignDigestLowS(priv, digest)
	if err != nil {
		return SignedOffer{}, err
	}
	return SignedOffer{
		Offer:             offer,
		CommitmentHex:     hex.EncodeToString(digest[:]),
		SignatureHex:      hex.EncodeToString(sig[:]),
		VerificationClass: VerificationCryptoSignature,
	}, nil
}

// VerifySignedOffer verifies the canonical commitment and EIP-2 low-S
// secp256k1 signature of a SignedOffer.
func VerifySignedOffer(so SignedOffer) error {
	if so.Offer.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol version %q", ErrInvalidOffer, so.Offer.ProtocolVersion)
	}
	digest := OfferCommitment(so.Offer)
	if hex.EncodeToString(digest[:]) != so.CommitmentHex {
		return ErrCommitmentMismatch
	}
	pubBytes, err := hex.DecodeString(so.Offer.ProviderPubKeyHex)
	if err != nil {
		return fmt.Errorf("%w: invalid pubkey hex", ErrInvalidSignature)
	}
	sigBytes, err := hex.DecodeString(so.SignatureHex)
	if err != nil {
		return fmt.Errorf("%w: invalid signature hex", ErrInvalidSignature)
	}
	return VerifyDigestLowS(pubBytes, digest, sigBytes)
}

// BidCommitment computes the deterministic domain-separated SHA-256 digest
// of a reverse-auction Bid.
func BidCommitment(b Bid) [32]byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, "HIP1350:BID:V1")
	writeLenPrefixed(&buf, b.ProtocolVersion)
	writeLenPrefixed(&buf, b.BidID)
	writeLenPrefixed(&buf, b.OrderID)
	writeLenPrefixed(&buf, b.OfferID)
	writeLenPrefixed(&buf, b.ProviderID)
	writeLenPrefixed(&buf, b.ProviderPubKeyHex)
	writeInt64(&buf, b.BidMicroUSDPer1MTokens)
	writeInt64(&buf, b.OfferedTokens)
	writeInt64(&buf, int64(b.CommittedVRAMGB))
	writeInt64(&buf, b.BondMicroUSD)
	writeInt64(&buf, b.IssuedAtUnix)
	writeInt64(&buf, b.ExpiresAtUnix)
	writeUint64(&buf, b.Nonce)
	return sha256.Sum256(buf.Bytes())
}

// SignBid constructs a cryptographically signed Bid using secp256k1 low-S.
func SignBid(priv *secp256k1.PrivateKey, bid Bid) (SignedBid, error) {
	if bid.ProtocolVersion == "" {
		bid.ProtocolVersion = ProtocolVersion
	}
	if priv == nil {
		return SignedBid{}, ErrInvalidSignature
	}
	if bid.ProviderPubKeyHex == "" {
		bid.ProviderPubKeyHex = hex.EncodeToString(priv.PublicKey().Bytes())
	}
	digest := BidCommitment(bid)
	sig, err := SignDigestLowS(priv, digest)
	if err != nil {
		return SignedBid{}, err
	}
	return SignedBid{
		Bid:               bid,
		CommitmentHex:     hex.EncodeToString(digest[:]),
		SignatureHex:      hex.EncodeToString(sig[:]),
		VerificationClass: VerificationCryptoSignature,
	}, nil
}

// VerifySignedBid verifies the canonical commitment and EIP-2 low-S secp256k1
// signature of a SignedBid.
func VerifySignedBid(sb SignedBid) error {
	if sb.Bid.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol version %q", ErrInvalidBid, sb.Bid.ProtocolVersion)
	}
	digest := BidCommitment(sb.Bid)
	if hex.EncodeToString(digest[:]) != sb.CommitmentHex {
		return ErrCommitmentMismatch
	}
	pubBytes, err := hex.DecodeString(sb.Bid.ProviderPubKeyHex)
	if err != nil {
		return fmt.Errorf("%w: invalid pubkey hex", ErrInvalidSignature)
	}
	sigBytes, err := hex.DecodeString(sb.SignatureHex)
	if err != nil {
		return fmt.Errorf("%w: invalid signature hex", ErrInvalidSignature)
	}
	return VerifyDigestLowS(pubBytes, digest, sigBytes)
}

// ReceiptCommitment computes the deterministic SHA-256 hash of a SettlementReceipt.
func ReceiptCommitment(r SettlementReceipt) [32]byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, "HIP1350:RECEIPT:V1")
	writeLenPrefixed(&buf, r.ReceiptID)
	writeLenPrefixed(&buf, r.LeaseID)
	writeLenPrefixed(&buf, r.OrderID)
	writeLenPrefixed(&buf, r.BidID)
	writeLenPrefixed(&buf, r.BuyerID)
	writeLenPrefixed(&buf, r.ProviderID)
	writeLenPrefixed(&buf, string(r.Status))
	writeInt64(&buf, r.AllocatedTokens)
	writeInt64(&buf, r.DeliveredTokens)
	writeInt64(&buf, r.AgreedMicroUSDPer1MTokens)
	writeInt64(&buf, r.BuyerEscrowLockedMicroUSD)
	writeInt64(&buf, r.ProviderPayoutMicroUSD)
	writeInt64(&buf, r.BuyerRefundMicroUSD)
	writeInt64(&buf, r.SlashedBondMicroUSD)
	writeInt64(&buf, r.BondReturnedMicroUSD)
	writeInt64(&buf, r.VerificationOverheadMicroUSD)
	writeLenPrefixed(&buf, string(r.VerificationClass))
	writeInt64(&buf, r.SettledAtUnix)
	return sha256.Sum256(buf.Bytes())
}

func writeLenPrefixed(buf *bytes.Buffer, s string) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(s)))
	buf.Write(l[:])
	buf.WriteString(s)
}

func writeInt64(buf *bytes.Buffer, v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	buf.Write(b[:])
}

func writeUint64(buf *bytes.Buffer, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	buf.Write(b[:])
}
