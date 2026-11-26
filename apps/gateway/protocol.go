// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package gateway implements the HIP-1350 permissionless Enso inference gateway
// and stateless multi-provider scheduling protocol.
//
// It imports only github.com/zap-proto/zip, github.com/valyala/fasthttp,
// github.com/luxfi/crypto/secp256k1, github.com/luxfi/log,
// golang.org/x/crypto/sha3, and the Go standard library — never its host
// application.
package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"

	"github.com/luxfi/crypto/secp256k1"
	"golang.org/x/crypto/sha3"
)

const (
	// ProtocolVersion identifies the wire and typed-data protocol version.
	ProtocolVersion = "enso-gateway/v1"

	// ReceiptVerificationClass explicitly declares the trust primitive used by
	// ExecutionReceipt: a secp256k1/EIP-712 signed provider commitment binding
	// request, quote, model digest, and SHA-256 output digest. It is never
	// conflated with hardware TEE attestation or economic dispute games.
	ReceiptVerificationClass = "cryptographic_signed_commitment_sha256"

	// DefaultChainID is the default EIP-712 chain ID for off-chain gateway
	// quotes and receipts when no deployment-specific chain ID is supplied.
	DefaultChainID uint64 = 36963

	// ZeroVerifyingContract is the standard off-chain EIP-712 sentinel address.
	ZeroVerifyingContract = "0x0000000000000000000000000000000000000000"
)

// Sentinel errors returned by protocol verification, scheduling, and HTTP ops.
var (
	ErrInvalidRequest      = errors.New("gateway: invalid request")
	ErrInvalidSignature    = errors.New("gateway: invalid signature")
	ErrSignerMismatch      = errors.New("gateway: signer mismatch")
	ErrInvalidQuote        = errors.New("gateway: invalid quote")
	ErrQuoteExpired        = errors.New("gateway: quote expired")
	ErrInvalidReceipt      = errors.New("gateway: invalid receipt")
	ErrOutputHashMismatch  = errors.New("gateway: output hash mismatch")
	ErrReplayNonce         = errors.New("gateway: replay nonce detected")
	ErrNoHealthyProviders  = errors.New("gateway: no healthy providers available")
	ErrProviderUnavailable = errors.New("gateway: provider unavailable")
	ErrGatewayDisabled     = errors.New("gateway: gateway operator disabled")
	ErrDirectoryCorrupted  = errors.New("gateway: directory snapshot corrupted")
)

var (
	secp256k1Order = new(big.Int).Set(secp256k1.S256().Params().N)
	secp256k1HalfN = new(big.Int).Rsh(new(big.Int).Set(secp256k1Order), 1)
)

// EIP712Domain scopes typed-data signatures for worker records, quotes,
// execution receipts, and directory snapshots.
type EIP712Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// DefaultDomain returns the standard HIP-1350 EIP-712 signing domain.
func DefaultDomain() EIP712Domain {
	return EIP712Domain{
		Name:              "Hanzo Enso Gateway",
		Version:           "1",
		ChainID:           DefaultChainID,
		VerifyingContract: ZeroVerifyingContract,
	}
}

// DomainSeparator computes the EIP-712 domain separator hash.
func (d EIP712Domain) DomainSeparator() [32]byte {
	typeHash := Keccak256Bytes([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash := Keccak256Bytes([]byte(d.Name))
	verHash := Keccak256Bytes([]byte(d.Version))

	var chainWord [32]byte
	binary.BigEndian.PutUint64(chainWord[24:], d.ChainID)

	var addrWord [32]byte
	if raw, err := decodeAddressBytes(d.VerifyingContract); err == nil {
		copy(addrWord[12:], raw[:])
	}
	return Keccak256Bytes(typeHash[:], nameHash[:], verHash[:], chainWord[:], addrWord[:])
}

// TypedDataDigest computes keccak256("\x19\x01" || domainSeparator || structHash).
func (d EIP712Domain) TypedDataDigest(structHash [32]byte) [32]byte {
	sep := d.DomainSeparator()
	return Keccak256Bytes([]byte{0x19, 0x01}, sep[:], structHash[:])
}

// KeyPair holds an ephemeral runtime secp256k1 signing key and its EIP-55 address.
type KeyPair struct {
	priv               *secp256k1.PrivateKey
	uncompressedPubKey []byte
	compressedPubKey   []byte
	address            string
}

// GenerateKeyPair creates a fresh cryptographically random secp256k1 keypair.
func GenerateKeyPair() (*KeyPair, error) {
	priv, err := secp256k1.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("gateway: generate secp256k1 key: %w", err)
	}
	return keyPairFromPrivate(priv)
}

// KeyPairFromSeed deterministically derives an ephemeral secp256k1 keypair from
// domain-separated input bytes for reproducible tests and benchmarks.
func KeyPairFromSeed(seed []byte) (*KeyPair, error) {
	if len(seed) == 0 {
		return nil, errors.New("gateway: empty key seed")
	}
	for counter := uint64(0); counter < 1024; counter++ {
		var ctr [8]byte
		binary.BigEndian.PutUint64(ctr[:], counter)
		digest := Keccak256Bytes([]byte("enso-gateway:seed:v1:"), seed, ctr[:])
		priv, err := secp256k1.ToPrivateKey(digest[:])
		if err == nil {
			return keyPairFromPrivate(priv)
		}
	}
	return nil, errors.New("gateway: failed to derive secp256k1 key from seed")
}

func keyPairFromPrivate(priv *secp256k1.PrivateKey) (*KeyPair, error) {
	ec := priv.ToECDSA()
	uncompressed := make([]byte, 65)
	uncompressed[0] = 0x04
	copy(uncompressed[1:33], secp256k1.PaddedBigBytes(ec.PublicKey.X, 32))
	copy(uncompressed[33:65], secp256k1.PaddedBigBytes(ec.PublicKey.Y, 32))
	compressed := secp256k1.CompressPubkey(ec.PublicKey.X, ec.PublicKey.Y)

	addr, err := AddressFromUncompressedPubKey(uncompressed)
	if err != nil {
		return nil, err
	}
	return &KeyPair{
		priv:               priv,
		uncompressedPubKey: uncompressed,
		compressedPubKey:   compressed,
		address:            addr,
	}, nil
}

// Address returns the EIP-55 checksummed EVM address.
func (k *KeyPair) Address() string { return k.address }

// CompressedPubKeyHex returns the 0x-prefixed 33-byte compressed public key hex.
func (k *KeyPair) CompressedPubKeyHex() string {
	return "0x" + hex.EncodeToString(k.compressedPubKey)
}

// SignDigest produces a 65-byte EIP-2 low-S normalized recoverable secp256k1
// ECDSA signature ([R || S || V]).
func (k *KeyPair) SignDigest(digest [32]byte) (string, error) {
	if k == nil || k.priv == nil {
		return "", errors.New("gateway: nil keypair")
	}
	sig, err := secp256k1.Sign(digest[:], k.priv.Bytes())
	if err != nil {
		return "", fmt.Errorf("gateway: sign digest: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("gateway: unexpected signature length %d", len(sig))
	}
	// Normalize S to EIP-2 low-S (S <= floor(N/2)) and adjust recovery parity.
	sVal := new(big.Int).SetBytes(sig[32:64])
	if sVal.Cmp(secp256k1HalfN) > 0 {
		lowS := new(big.Int).Sub(secp256k1Order, sVal)
		copy(sig[32:64], secp256k1.PaddedBigBytes(lowS, 32))
		switch sig[64] {
		case 0, 1:
			sig[64] ^= 1
		case 27, 28:
			sig[64] = ((sig[64] - 27) ^ 1) + 27
		}
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// MalleateSignatureHighS flips S to N - S (and flips recovery ID parity) to
// produce a mathematically valid but EIP-2 non-canonical high-S signature for
// adversarial testing.
func MalleateSignatureHighS(sigHex string) (string, error) {
	sig, err := decodeHex(sigHex)
	if err != nil || len(sig) != 65 {
		return "", fmt.Errorf("%w: signature must be 65 bytes", ErrInvalidSignature)
	}
	out := make([]byte, 65)
	copy(out, sig)
	sVal := new(big.Int).SetBytes(out[32:64])
	highS := new(big.Int).Sub(secp256k1Order, sVal)
	copy(out[32:64], secp256k1.PaddedBigBytes(highS, 32))
	switch out[64] {
	case 0, 1:
		out[64] ^= 1
	case 27, 28:
		out[64] = ((out[64] - 27) ^ 1) + 27
	}
	return "0x" + hex.EncodeToString(out), nil
}

// Keccak256Bytes returns the 32-byte legacy Ethereum Keccak-256 digest.
func Keccak256Bytes(parts ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		_, _ = h.Write(p)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// SHA256Hex returns the lowercase hex SHA-256 digest of all concatenated parts.
func SHA256Hex(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RandomNonceHex returns a cryptographically random 16-byte hex nonce.
func RandomNonceHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("gateway: generate nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// AddressFromUncompressedPubKey derives an EIP-55 checksummed EVM address.
func AddressFromUncompressedPubKey(uncompressed []byte) (string, error) {
	if len(uncompressed) != 65 || uncompressed[0] != 0x04 {
		return "", errors.New("gateway: expected 65-byte uncompressed secp256k1 public key")
	}
	digest := Keccak256Bytes(uncompressed[1:])
	return ToChecksumAddress(digest[12:]), nil
}

// ToChecksumAddress encodes a 20-byte EVM address with an EIP-55 checksum.
func ToChecksumAddress(addr []byte) string {
	lower := hex.EncodeToString(addr)
	hash := Keccak256Bytes([]byte(lower))
	hashHex := hex.EncodeToString(hash[:])
	var b strings.Builder
	b.Grow(42)
	b.WriteString("0x")
	for i := 0; i < len(lower); i++ {
		c := lower[i]
		if c >= 'a' && c <= 'f' && hashHex[i] >= '8' {
			b.WriteByte(c - 32)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// NormalizeAddress validates and returns the canonical EIP-55 EVM address.
func NormalizeAddress(addr string) (string, error) {
	raw, err := decodeAddressBytes(addr)
	if err != nil {
		return "", err
	}
	return ToChecksumAddress(raw[:]), nil
}

// SameAddress compares two EVM addresses after canonical EIP-55 normalization.
func SameAddress(a, b string) bool {
	na, errA := NormalizeAddress(a)
	nb, errB := NormalizeAddress(b)
	return errA == nil && errB == nil && na == nb
}

func decodeAddressBytes(addr string) ([20]byte, error) {
	var out [20]byte
	raw, err := decodeHex(addr)
	if err != nil || len(raw) != 20 {
		return out, fmt.Errorf("%w: invalid EVM address %q", ErrInvalidRequest, addr)
	}
	copy(out[:], raw)
	return out, nil
}

func decodeHex(s string) ([]byte, error) {
	trimmed := strings.TrimSpace(s)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	trimmed = strings.TrimPrefix(trimmed, "0X")
	return hex.DecodeString(trimmed)
}

// RecoverSignerAddress recovers the EIP-55 EVM address from a 32-byte digest
// and a 65-byte recoverable secp256k1 signature hex string. Strictly rejects
// EIP-2 high-S and zero-S signatures.
func RecoverSignerAddress(digest [32]byte, sigHex string) (string, error) {
	sig, err := decodeHex(sigHex)
	if err != nil || len(sig) != 65 {
		return "", fmt.Errorf("%w: signature must be 65 bytes", ErrInvalidSignature)
	}
	rVal := new(big.Int).SetBytes(sig[0:32])
	sVal := new(big.Int).SetBytes(sig[32:64])
	if rVal.Sign() <= 0 || rVal.Cmp(secp256k1Order) >= 0 {
		return "", fmt.Errorf("%w: invalid R value", ErrInvalidSignature)
	}
	if sVal.Sign() <= 0 || sVal.Cmp(secp256k1HalfN) > 0 {
		return "", fmt.Errorf("%w: non-canonical high-S or zero S value", ErrInvalidSignature)
	}
	normalized := make([]byte, 65)
	copy(normalized, sig)
	switch normalized[64] {
	case 0, 1:
	case 27, 28:
		normalized[64] -= 27
	default:
		return "", fmt.Errorf("%w: invalid recovery id %d", ErrInvalidSignature, normalized[64])
	}

	pub, err := secp256k1.RecoverPubkey(digest[:], normalized)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	addr, err := AddressFromUncompressedPubKey(pub)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	return addr, nil
}

func encodeUint64Word(v uint64) [32]byte {
	var w [32]byte
	binary.BigEndian.PutUint64(w[24:], v)
	return w
}

func encodeInt64Word(v int64) [32]byte {
	var w [32]byte
	if v < 0 {
		for i := 0; i < 24; i++ {
			w[i] = 0xff
		}
	}
	binary.BigEndian.PutUint64(w[24:], uint64(v))
	return w
}

func encodeAddressWord(addr string) ([32]byte, error) {
	var w [32]byte
	raw, err := decodeAddressBytes(addr)
	if err != nil {
		return w, err
	}
	copy(w[12:], raw[:])
	return w, nil
}

// --- Open Protocol Types: Worker Discovery, Signed Quotes & Execution Receipts ---

// ModelSpec describes an open-weights model served by a worker along with its
// content-addressed weight/config digest.
type ModelSpec struct {
	ModelID          string `json:"modelId"`
	ModelDigest      string `json:"modelDigest"`
	MaxContextTokens int    `json:"maxContextTokens"`
	License          string `json:"license"`
}

// WorkerRecord is a self-signed provider advertisement published to any
// permissionless discovery directory or gossip peer.
type WorkerRecord struct {
	ProtocolVersion    string      `json:"protocolVersion"`
	ProviderID         string      `json:"providerId"`
	OperatorAddress    string      `json:"operatorAddress"`
	EndpointURL        string      `json:"endpointUrl"`
	Region             string      `json:"region"`
	Models             []ModelSpec `json:"models"`
	PriceMicroUSDPer1M uint64      `json:"priceMicroUsdPer1m"`
	AdvertisedTTFTMs   int64       `json:"advertisedTtftMs"`
	CapacityRPM        int         `json:"capacityRpm"`
	IssuedAt           int64       `json:"issuedAt"`
	ExpiresAt          int64       `json:"expiresAt"`
	Signature          string      `json:"signature"`
}

// FindModel returns the ModelSpec matching modelID, if served by this worker.
func (w WorkerRecord) FindModel(modelID string) (ModelSpec, bool) {
	for _, m := range w.Models {
		if m.ModelID == modelID {
			return m, true
		}
	}
	return ModelSpec{}, false
}

func modelSpecStructHash(m ModelSpec) [32]byte {
	typeHash := Keccak256Bytes([]byte(
		"ModelSpec(string modelId,string modelDigest,int64 maxContextTokens,string license)",
	))
	idHash := Keccak256Bytes([]byte(m.ModelID))
	digestHash := Keccak256Bytes([]byte(m.ModelDigest))
	ctxWord := encodeInt64Word(int64(m.MaxContextTokens))
	licHash := Keccak256Bytes([]byte(m.License))
	return Keccak256Bytes(typeHash[:], idHash[:], digestHash[:], ctxWord[:], licHash[:])
}

func canonicalModelsDigest(models []ModelSpec) [32]byte {
	cp := append([]ModelSpec(nil), models...)
	sort.Slice(cp, func(i, j int) bool {
		if cp[i].ModelID != cp[j].ModelID {
			return cp[i].ModelID < cp[j].ModelID
		}
		if cp[i].ModelDigest != cp[j].ModelDigest {
			return cp[i].ModelDigest < cp[j].ModelDigest
		}
		if cp[i].MaxContextTokens != cp[j].MaxContextTokens {
			return cp[i].MaxContextTokens < cp[j].MaxContextTokens
		}
		return cp[i].License < cp[j].License
	})
	buf := make([]byte, 0, len(cp)*32)
	for _, m := range cp {
		h := modelSpecStructHash(m)
		buf = append(buf, h[:]...)
	}
	return Keccak256Bytes(buf)
}

func workerRecordStructHash(rec WorkerRecord) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"WorkerRecord(string protocolVersion,string providerId,address operatorAddress,string endpointUrl,string region,bytes32 modelsDigest,uint256 priceMicroUsdPer1m,int64 advertisedTtftMs,uint256 capacityRpm,int64 issuedAt,int64 expiresAt)",
	))
	opWord, err := encodeAddressWord(rec.OperatorAddress)
	if err != nil {
		return [32]byte{}, err
	}
	protoHash := Keccak256Bytes([]byte(rec.ProtocolVersion))
	idHash := Keccak256Bytes([]byte(rec.ProviderID))
	urlHash := Keccak256Bytes([]byte(rec.EndpointURL))
	regionHash := Keccak256Bytes([]byte(rec.Region))
	modelsHash := canonicalModelsDigest(rec.Models)
	priceWord := encodeUint64Word(rec.PriceMicroUSDPer1M)
	ttftWord := encodeInt64Word(rec.AdvertisedTTFTMs)
	capWord := encodeUint64Word(uint64(rec.CapacityRPM))
	issuedWord := encodeInt64Word(rec.IssuedAt)
	expWord := encodeInt64Word(rec.ExpiresAt)
	return Keccak256Bytes(
		typeHash[:],
		protoHash[:],
		idHash[:],
		opWord[:],
		urlHash[:],
		regionHash[:],
		modelsHash[:],
		priceWord[:],
		ttftWord[:],
		capWord[:],
		issuedWord[:],
		expWord[:],
	), nil
}

// SignWorkerRecord signs rec with kp under domain.
func SignWorkerRecord(domain EIP712Domain, kp *KeyPair, rec *WorkerRecord) error {
	if rec == nil || kp == nil {
		return fmt.Errorf("%w: nil worker record or keypair", ErrInvalidRequest)
	}
	if rec.ProtocolVersion == "" {
		rec.ProtocolVersion = ProtocolVersion
	}
	rec.OperatorAddress = kp.Address()
	sh, err := workerRecordStructHash(*rec)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	rec.Signature = sig
	return nil
}

// VerifyWorkerRecord verifies the EIP-712 signature, low-S canonicality,
// required metadata, and expiry of rec offline.
func VerifyWorkerRecord(domain EIP712Domain, rec WorkerRecord, nowUnix int64) error {
	if rec.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported worker protocol version %q", ErrInvalidRequest, rec.ProtocolVersion)
	}
	if rec.ProviderID == "" || rec.EndpointURL == "" || len(rec.Models) == 0 {
		return fmt.Errorf("%w: worker record missing providerId, endpointUrl, or models", ErrInvalidRequest)
	}
	for _, m := range rec.Models {
		if m.ModelID == "" || m.ModelDigest == "" {
			return fmt.Errorf("%w: worker model entry missing modelId or modelDigest", ErrInvalidRequest)
		}
	}
	if rec.IssuedAt > rec.ExpiresAt || (nowUnix > 0 && nowUnix > rec.ExpiresAt) {
		return fmt.Errorf("%w: worker record expired", ErrQuoteExpired)
	}
	sh, err := workerRecordStructHash(rec)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), rec.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, rec.OperatorAddress) {
		return fmt.Errorf("%w: worker record signer %s != operator %s", ErrSignerMismatch, signer, rec.OperatorAddress)
	}
	return nil
}

// InferenceQuote is a binding, EIP-712 signed price/SLO offer from a worker.
type InferenceQuote struct {
	QuoteID            string `json:"quoteId"`
	ProtocolVersion    string `json:"protocolVersion"`
	ProviderID         string `json:"providerId"`
	ProviderAddress    string `json:"providerAddress"`
	ModelID            string `json:"modelId"`
	ModelDigest        string `json:"modelDigest"`
	PriceMicroUSDPer1M uint64 `json:"priceMicroUsdPer1m"`
	MaxTTFTMs          int64  `json:"maxTtftMs"`
	MaxTokens          int    `json:"maxTokens"`
	IssuedAt           int64  `json:"issuedAt"`
	ExpiresAt          int64  `json:"expiresAt"`
	Nonce              string `json:"nonce"`
	RequestHash        string `json:"requestHash"`
	Signature          string `json:"signature"`
}

func quoteStructHash(q InferenceQuote) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"InferenceQuote(string quoteId,string protocolVersion,string providerId,address providerAddress,string modelId,string modelDigest,uint256 priceMicroUsdPer1m,int64 maxTtftMs,uint256 maxTokens,int64 issuedAt,int64 expiresAt,string nonce,string requestHash)",
	))
	provWord, err := encodeAddressWord(q.ProviderAddress)
	if err != nil {
		return [32]byte{}, err
	}
	quoteIDHash := Keccak256Bytes([]byte(q.QuoteID))
	protoHash := Keccak256Bytes([]byte(q.ProtocolVersion))
	providerIDHash := Keccak256Bytes([]byte(q.ProviderID))
	modelIDHash := Keccak256Bytes([]byte(q.ModelID))
	modelDigestHash := Keccak256Bytes([]byte(q.ModelDigest))
	priceWord := encodeUint64Word(q.PriceMicroUSDPer1M)
	ttftWord := encodeInt64Word(q.MaxTTFTMs)
	maxTokWord := encodeUint64Word(uint64(q.MaxTokens))
	issuedWord := encodeInt64Word(q.IssuedAt)
	expWord := encodeInt64Word(q.ExpiresAt)
	nonceHash := Keccak256Bytes([]byte(q.Nonce))
	reqHash := Keccak256Bytes([]byte(q.RequestHash))

	return Keccak256Bytes(
		typeHash[:],
		quoteIDHash[:],
		protoHash[:],
		providerIDHash[:],
		provWord[:],
		modelIDHash[:],
		modelDigestHash[:],
		priceWord[:],
		ttftWord[:],
		maxTokWord[:],
		issuedWord[:],
		expWord[:],
		nonceHash[:],
		reqHash[:],
	), nil
}

// QuoteHash returns the 0x-prefixed EIP-712 typed-data digest of q.
func QuoteHash(domain EIP712Domain, q InferenceQuote) (string, error) {
	sh, err := quoteStructHash(q)
	if err != nil {
		return "", err
	}
	digest := domain.TypedDataDigest(sh)
	return "0x" + hex.EncodeToString(digest[:]), nil
}

// SignQuote signs an InferenceQuote using the worker's secp256k1 keypair.
func SignQuote(domain EIP712Domain, kp *KeyPair, q *InferenceQuote) error {
	if q == nil || kp == nil {
		return fmt.Errorf("%w: nil quote or keypair", ErrInvalidQuote)
	}
	if q.ProtocolVersion == "" {
		q.ProtocolVersion = ProtocolVersion
	}
	q.ProviderAddress = kp.Address()
	sh, err := quoteStructHash(*q)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	q.Signature = sig
	return nil
}

// VerifyQuote verifies an InferenceQuote offline: schema validity, expiry,
// EIP-2 low-S signature canonicality, and signer address binding.
func VerifyQuote(domain EIP712Domain, q InferenceQuote, nowUnix int64) error {
	if q.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: invalid protocol version %q", ErrInvalidQuote, q.ProtocolVersion)
	}
	if q.QuoteID == "" || q.ProviderID == "" || q.ModelID == "" || q.ModelDigest == "" || q.Nonce == "" {
		return fmt.Errorf("%w: missing required quote fields", ErrInvalidQuote)
	}
	if q.MaxTokens <= 0 || q.MaxTTFTMs < 0 {
		return fmt.Errorf("%w: invalid token or TTFT bounds", ErrInvalidQuote)
	}
	if q.IssuedAt > q.ExpiresAt {
		return fmt.Errorf("%w: quote issuedAt > expiresAt", ErrInvalidQuote)
	}
	if nowUnix > 0 && nowUnix > q.ExpiresAt {
		return fmt.Errorf("%w: now=%d > expiresAt=%d", ErrQuoteExpired, nowUnix, q.ExpiresAt)
	}
	sh, err := quoteStructHash(q)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), q.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, q.ProviderAddress) {
		return fmt.Errorf("%w: quote signer %s != provider %s", ErrSignerMismatch, signer, q.ProviderAddress)
	}
	return nil
}

// ExecutionReceipt is a provider-signed cryptographic commitment binding the
// request hash, quote hash, model digest, output hash, token counts, and
// observed latency. A gateway may optionally countersign GatewaySignature.
type ExecutionReceipt struct {
	ReceiptID         string  `json:"receiptId"`
	ProtocolVersion   string  `json:"protocolVersion"`
	VerificationClass string  `json:"verificationClass"`
	GatewayID         string  `json:"gatewayId"`
	GatewayOperator   string  `json:"gatewayOperator"`
	ProviderID        string  `json:"providerId"`
	ProviderAddress   string  `json:"providerAddress"`
	ModelID           string  `json:"modelId"`
	ModelDigest       string  `json:"modelDigest"`
	QuoteID           string  `json:"quoteId"`
	QuoteHash         string  `json:"quoteHash"`
	QuoteNonce        string  `json:"quoteNonce"`
	RequestHash       string  `json:"requestHash"`
	OutputHash        string  `json:"outputHash"`
	PromptTokens      int     `json:"promptTokens"`
	CompletionTokens  int     `json:"completionTokens"`
	TotalTokens       int     `json:"totalTokens"`
	TTFTMs            float64 `json:"ttftMs"`
	TotalLatencyMs    float64 `json:"totalLatencyMs"`
	CompletedAt       int64   `json:"completedAt"`
	Signature         string  `json:"signature"`
	GatewayAddress    string  `json:"gatewayAddress,omitempty"`
	GatewaySignature  string  `json:"gatewaySignature,omitempty"`
}

func receiptStructHash(r ExecutionReceipt) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"ExecutionReceipt(string receiptId,string protocolVersion,string verificationClass,string gatewayId,string providerId,address providerAddress,string modelId,string modelDigest,string quoteId,string quoteHash,string quoteNonce,string requestHash,string outputHash,uint256 promptTokens,uint256 completionTokens,uint256 totalTokens,int64 ttftMicros,int64 totalLatencyMicros,int64 completedAt)",
	))
	provWord, err := encodeAddressWord(r.ProviderAddress)
	if err != nil {
		return [32]byte{}, err
	}
	receiptIDHash := Keccak256Bytes([]byte(r.ReceiptID))
	protoHash := Keccak256Bytes([]byte(r.ProtocolVersion))
	classHash := Keccak256Bytes([]byte(r.VerificationClass))
	gwIDHash := Keccak256Bytes([]byte(r.GatewayID))
	provIDHash := Keccak256Bytes([]byte(r.ProviderID))
	modelIDHash := Keccak256Bytes([]byte(r.ModelID))
	modelDigestHash := Keccak256Bytes([]byte(r.ModelDigest))
	quoteIDHash := Keccak256Bytes([]byte(r.QuoteID))
	quoteHashHash := Keccak256Bytes([]byte(r.QuoteHash))
	quoteNonceHash := Keccak256Bytes([]byte(r.QuoteNonce))
	reqHashHash := Keccak256Bytes([]byte(r.RequestHash))
	outHashHash := Keccak256Bytes([]byte(r.OutputHash))
	promptWord := encodeUint64Word(uint64(r.PromptTokens))
	compWord := encodeUint64Word(uint64(r.CompletionTokens))
	totalWord := encodeUint64Word(uint64(r.TotalTokens))
	ttftMicrosWord := encodeInt64Word(int64(r.TTFTMs * 1000))
	latMicrosWord := encodeInt64Word(int64(r.TotalLatencyMs * 1000))
	completedWord := encodeInt64Word(r.CompletedAt)

	return Keccak256Bytes(
		typeHash[:],
		receiptIDHash[:],
		protoHash[:],
		classHash[:],
		gwIDHash[:],
		provIDHash[:],
		provWord[:],
		modelIDHash[:],
		modelDigestHash[:],
		quoteIDHash[:],
		quoteHashHash[:],
		quoteNonceHash[:],
		reqHashHash[:],
		outHashHash[:],
		promptWord[:],
		compWord[:],
		totalWord[:],
		ttftMicrosWord[:],
		latMicrosWord[:],
		completedWord[:],
	), nil
}

// ReceiptHash returns the 0x-prefixed EIP-712 typed-data digest of r.
func ReceiptHash(domain EIP712Domain, r ExecutionReceipt) (string, error) {
	sh, err := receiptStructHash(r)
	if err != nil {
		return "", err
	}
	digest := domain.TypedDataDigest(sh)
	return "0x" + hex.EncodeToString(digest[:]), nil
}

// SignReceipt signs an ExecutionReceipt with the provider's secp256k1 keypair.
func SignReceipt(domain EIP712Domain, kp *KeyPair, r *ExecutionReceipt) error {
	if r == nil || kp == nil {
		return fmt.Errorf("%w: nil receipt or keypair", ErrInvalidReceipt)
	}
	if r.ProtocolVersion == "" {
		r.ProtocolVersion = ProtocolVersion
	}
	if r.VerificationClass == "" {
		r.VerificationClass = ReceiptVerificationClass
	}
	r.ProviderAddress = kp.Address()
	sh, err := receiptStructHash(*r)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	r.Signature = sig
	return nil
}

func gatewayAttestationStructHash(r ExecutionReceipt) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"GatewayReceiptAttestation(string receiptId,string gatewayId,string gatewayOperator,address gatewayAddress,string quoteHash,string outputHash,string providerSignature)",
	))
	gwWord, err := encodeAddressWord(r.GatewayAddress)
	if err != nil {
		return [32]byte{}, err
	}
	rcptHash := Keccak256Bytes([]byte(r.ReceiptID))
	gwIDHash := Keccak256Bytes([]byte(r.GatewayID))
	gwOpHash := Keccak256Bytes([]byte(r.GatewayOperator))
	quoteHash := Keccak256Bytes([]byte(r.QuoteHash))
	outputHash := Keccak256Bytes([]byte(r.OutputHash))
	sigHash := Keccak256Bytes([]byte(r.Signature))
	return Keccak256Bytes(
		typeHash[:],
		rcptHash[:],
		gwIDHash[:],
		gwOpHash[:],
		gwWord[:],
		quoteHash[:],
		outputHash[:],
		sigHash[:],
	), nil
}

// SignGatewayAttestation attaches an independent gateway countersignature to r
// without altering the provider's signature.
func SignGatewayAttestation(domain EIP712Domain, kp *KeyPair, r *ExecutionReceipt) error {
	if r == nil || kp == nil {
		return fmt.Errorf("%w: nil receipt or gateway keypair", ErrInvalidReceipt)
	}
	r.GatewayAddress = kp.Address()
	sh, err := gatewayAttestationStructHash(*r)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	r.GatewaySignature = sig
	return nil
}

// VerifyGatewayAttestation verifies the optional gateway countersignature on r.
func VerifyGatewayAttestation(domain EIP712Domain, r ExecutionReceipt) error {
	if r.GatewayAddress == "" || r.GatewaySignature == "" {
		return fmt.Errorf("%w: missing gateway attestation", ErrInvalidReceipt)
	}
	sh, err := gatewayAttestationStructHash(r)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), r.GatewaySignature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, r.GatewayAddress) {
		return fmt.Errorf("%w: gateway signer %s != %s", ErrSignerMismatch, signer, r.GatewayAddress)
	}
	return nil
}

// ComputeOutputHash returns the canonical SHA-256 commitment over completion text.
func ComputeOutputHash(content string) string {
	return "sha256:" + SHA256Hex([]byte(content))
}

// VerifyReceipt performs full offline cryptographic verification of an
// ExecutionReceipt against its originating InferenceQuote (if non-nil),
// expectedRequestHash (if non-empty), and actual responseContent (always
// checked, including when responseContent is empty).
func VerifyReceipt(
	domain EIP712Domain,
	r ExecutionReceipt,
	expectedQuote *InferenceQuote,
	expectedRequestHash string,
	responseContent string,
) error {
	return verifyReceipt(domain, r, expectedQuote, expectedRequestHash, &responseContent)
}

// VerifyReceiptWithoutContent performs offline cryptographic verification of an
// ExecutionReceipt against its originating InferenceQuote (if non-nil) and
// expectedRequestHash (if non-empty) without comparing r.OutputHash against a
// completion body. Callers that hold the returned completion text must use
// VerifyReceipt instead so empty/truncated outputs are verified.
func VerifyReceiptWithoutContent(
	domain EIP712Domain,
	r ExecutionReceipt,
	expectedQuote *InferenceQuote,
	expectedRequestHash string,
) error {
	return verifyReceipt(domain, r, expectedQuote, expectedRequestHash, nil)
}

func verifyReceipt(
	domain EIP712Domain,
	r ExecutionReceipt,
	expectedQuote *InferenceQuote,
	expectedRequestHash string,
	responseContent *string,
) error {
	if r.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: invalid receipt protocol version %q", ErrInvalidReceipt, r.ProtocolVersion)
	}
	if r.VerificationClass != ReceiptVerificationClass {
		return fmt.Errorf("%w: unsupported verification class %q", ErrInvalidReceipt, r.VerificationClass)
	}
	if r.ReceiptID == "" || r.ProviderID == "" || r.ModelID == "" || r.ModelDigest == "" ||
		r.QuoteID == "" || r.QuoteHash == "" || r.QuoteNonce == "" || r.RequestHash == "" || r.OutputHash == "" {
		return fmt.Errorf("%w: missing required receipt fields", ErrInvalidReceipt)
	}
	if r.PromptTokens < 0 || r.CompletionTokens < 0 || r.PromptTokens+r.CompletionTokens != r.TotalTokens {
		return fmt.Errorf("%w: inconsistent token accounting (%d + %d != %d)",
			ErrInvalidReceipt, r.PromptTokens, r.CompletionTokens, r.TotalTokens)
	}

	sh, err := receiptStructHash(r)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReceipt, err)
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), r.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, r.ProviderAddress) {
		return fmt.Errorf("%w: receipt signer %s != provider %s", ErrSignerMismatch, signer, r.ProviderAddress)
	}

	if expectedQuote != nil {
		qh, err := QuoteHash(domain, *expectedQuote)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidQuote, err)
		}
		if r.QuoteID != expectedQuote.QuoteID || r.QuoteHash != qh || r.QuoteNonce != expectedQuote.Nonce {
			return fmt.Errorf("%w: receipt does not match bound quote", ErrInvalidReceipt)
		}
		if !SameAddress(r.ProviderAddress, expectedQuote.ProviderAddress) || r.ProviderID != expectedQuote.ProviderID {
			return fmt.Errorf("%w: receipt provider differs from quote provider", ErrSignerMismatch)
		}
		if r.ModelID != expectedQuote.ModelID || r.ModelDigest != expectedQuote.ModelDigest {
			return fmt.Errorf("%w: receipt model differs from quote model", ErrInvalidReceipt)
		}
		if expectedQuote.RequestHash != "" && r.RequestHash != expectedQuote.RequestHash {
			return fmt.Errorf("%w: receipt request hash (%s) does not match bound quote request hash (%s)",
				ErrInvalidReceipt, r.RequestHash, expectedQuote.RequestHash)
		}
		if expectedQuote.MaxTokens <= 0 || r.CompletionTokens > expectedQuote.MaxTokens {
			return fmt.Errorf("%w: receipt completion tokens (%d) exceeds bound quote maxTokens (%d)",
				ErrInvalidReceipt, r.CompletionTokens, expectedQuote.MaxTokens)
		}
	}

	if expectedRequestHash != "" && r.RequestHash != expectedRequestHash {
		return fmt.Errorf("%w: request hash mismatch (%s != %s)", ErrInvalidReceipt, r.RequestHash, expectedRequestHash)
	}

	if responseContent != nil {
		actual := ComputeOutputHash(*responseContent)
		if r.OutputHash != actual {
			return fmt.Errorf("%w: receipt=%s actual=%s", ErrOutputHashMismatch, r.OutputHash, actual)
		}
	}

	if r.GatewaySignature != "" {
		if err := VerifyGatewayAttestation(domain, r); err != nil {
			return err
		}
	}
	return nil
}

// --- Replay Nonce Cache (Local Stateless Per-Gateway Guard) ---

// NonceCache rejects duplicate quote nonces locally within their TTL window.
// Gateways do not share mutable session state; each gateway enforces replay
// protection locally over the quotes it brokers.
type NonceCache struct {
	mu   sync.Mutex
	seen map[string]int64
}

// NewNonceCache constructs an empty NonceCache.
func NewNonceCache() *NonceCache {
	return &NonceCache{seen: make(map[string]int64)}
}

// Consume records nonce until expiresAt or returns ErrReplayNonce if already
// used. Expired nonces are lazily pruned.
func (c *NonceCache) Consume(nonce string, expiresAt int64, nowUnix int64) error {
	if nonce == "" {
		return fmt.Errorf("%w: empty nonce", ErrInvalidQuote)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) > 4096 {
		for k, exp := range c.seen {
			if exp < nowUnix {
				delete(c.seen, k)
			}
		}
	}
	if _, exists := c.seen[nonce]; exists {
		return fmt.Errorf("%w: %s", ErrReplayNonce, nonce)
	}
	c.seen[nonce] = expiresAt
	return nil
}

// --- Decentralized Multi-Peer Worker Discovery Directory ---

// DirectorySnapshot is a signed, portable export of a PeerDirectory for
// gossip sync, migration, or independent gateway forking.
type DirectorySnapshot struct {
	ProtocolVersion string         `json:"protocolVersion"`
	PeerID          string         `json:"peerId"`
	OperatorAddress string         `json:"operatorAddress"`
	ExportedAt      int64          `json:"exportedAt"`
	Workers         []WorkerRecord `json:"workers"`
	SnapshotSHA256  string         `json:"snapshotSha256"`
	Signature       string         `json:"signature"`
}

func computeSnapshotSHA256(snap DirectorySnapshot) (string, error) {
	cp := append([]WorkerRecord(nil), snap.Workers...)
	sort.Slice(cp, func(i, j int) bool {
		return cp[i].ProviderID < cp[j].ProviderID
	})
	payload := struct {
		ProtocolVersion string         `json:"protocolVersion"`
		PeerID          string         `json:"peerId"`
		OperatorAddress string         `json:"operatorAddress"`
		ExportedAt      int64          `json:"exportedAt"`
		Workers         []WorkerRecord `json:"workers"`
	}{
		ProtocolVersion: snap.ProtocolVersion,
		PeerID:          snap.PeerID,
		OperatorAddress: snap.OperatorAddress,
		ExportedAt:      snap.ExportedAt,
		Workers:         cp,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return SHA256Hex(raw), nil
}

func snapshotStructHash(snap DirectorySnapshot) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"DirectorySnapshot(string protocolVersion,string peerId,address operatorAddress,int64 exportedAt,string snapshotSha256)",
	))
	opWord, err := encodeAddressWord(snap.OperatorAddress)
	if err != nil {
		return [32]byte{}, err
	}
	protoHash := Keccak256Bytes([]byte(snap.ProtocolVersion))
	peerHash := Keccak256Bytes([]byte(snap.PeerID))
	exportedWord := encodeInt64Word(snap.ExportedAt)
	snapHash := Keccak256Bytes([]byte(snap.SnapshotSHA256))
	return Keccak256Bytes(
		typeHash[:],
		protoHash[:],
		peerHash[:],
		opWord[:],
		exportedWord[:],
		snapHash[:],
	), nil
}

// VerifyDirectorySnapshot verifies a DirectorySnapshot and every embedded
// WorkerRecord offline.
func VerifyDirectorySnapshot(domain EIP712Domain, snap DirectorySnapshot, nowUnix int64) error {
	if snap.ProtocolVersion != ProtocolVersion || snap.PeerID == "" {
		return fmt.Errorf("%w: invalid snapshot metadata", ErrDirectoryCorrupted)
	}
	expectedHash, err := computeSnapshotSHA256(snap)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDirectoryCorrupted, err)
	}
	if snap.SnapshotSHA256 != expectedHash {
		return fmt.Errorf("%w: digest mismatch", ErrDirectoryCorrupted)
	}
	sh, err := snapshotStructHash(snap)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDirectoryCorrupted, err)
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), snap.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, snap.OperatorAddress) {
		return fmt.Errorf("%w: snapshot signer %s != operator %s", ErrSignerMismatch, signer, snap.OperatorAddress)
	}
	seenProviders := make(map[string]string, len(snap.Workers))
	for _, w := range snap.Workers {
		if err := VerifyWorkerRecord(domain, w, nowUnix); err != nil {
			return fmt.Errorf("%w: worker %s: %v", ErrDirectoryCorrupted, w.ProviderID, err)
		}
		if prevOp, exists := seenProviders[w.ProviderID]; exists && !SameAddress(prevOp, w.OperatorAddress) {
			return fmt.Errorf("%w: worker %s has conflicting operators (%s vs %s)", ErrDirectoryCorrupted, w.ProviderID, prevOp, w.OperatorAddress)
		}
		seenProviders[w.ProviderID] = w.OperatorAddress
	}
	return nil
}

// PeerDirectory stores verified WorkerRecords discovered via direct
// registration or multi-peer gossip, with zero centralized registry dependency.
type PeerDirectory struct {
	mu      sync.RWMutex
	domain  EIP712Domain
	workers map[string]WorkerRecord
}

// NewPeerDirectory initializes an empty PeerDirectory for domain.
func NewPeerDirectory(domain EIP712Domain) *PeerDirectory {
	return &PeerDirectory{
		domain:  domain,
		workers: make(map[string]WorkerRecord),
	}
}

// Domain returns the EIP-712 domain enforced by this directory.
func (d *PeerDirectory) Domain() EIP712Domain { return d.domain }

// RegisterWorker validates and inserts (or updates) a signed WorkerRecord.
// Replacement records for an existing ProviderID must be signed by the same
// cryptographic operator identity to prevent unauthorized provider hijacking.
func (d *PeerDirectory) RegisterWorker(rec WorkerRecord, nowUnix int64) error {
	if err := VerifyWorkerRecord(d.domain, rec, nowUnix); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.workers[rec.ProviderID]; ok {
		if !SameAddress(existing.OperatorAddress, rec.OperatorAddress) {
			return fmt.Errorf("%w: provider %s is owned by operator %s, cannot be replaced by %s",
				ErrSignerMismatch, rec.ProviderID, existing.OperatorAddress, rec.OperatorAddress)
		}
		if existing.IssuedAt > rec.IssuedAt {
			return nil
		}
	}
	d.workers[rec.ProviderID] = rec
	return nil
}

// RemoveWorker deletes a provider entry by ID.
func (d *PeerDirectory) RemoveWorker(providerID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.workers, providerID)
}

// ListWorkers returns all non-expired workers sorted deterministically by ProviderID.
func (d *PeerDirectory) ListWorkers(nowUnix int64) []WorkerRecord {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]WorkerRecord, 0, len(d.workers))
	for _, w := range d.workers {
		if nowUnix > 0 && nowUnix > w.ExpiresAt {
			continue
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ProviderID < out[j].ProviderID
	})
	return out
}

// DiscoverForModel returns all active workers serving modelID.
func (d *PeerDirectory) DiscoverForModel(modelID string, nowUnix int64) []WorkerRecord {
	all := d.ListWorkers(nowUnix)
	out := make([]WorkerRecord, 0, len(all))
	for _, w := range all {
		if _, ok := w.FindModel(modelID); ok {
			out = append(out, w)
		}
	}
	return out
}

// MergeRecords verifies and merges a batch of gossiped WorkerRecords, returning
// the count of accepted records and the count of rejected invalid records.
func (d *PeerDirectory) MergeRecords(records []WorkerRecord, nowUnix int64) (accepted int, rejected int) {
	for _, rec := range records {
		if err := d.RegisterWorker(rec, nowUnix); err != nil {
			rejected++
			continue
		}
		accepted++
	}
	return accepted, rejected
}

// MergeGossip synchronizes verified records from peer into d.
func (d *PeerDirectory) MergeGossip(peer *PeerDirectory, nowUnix int64) (accepted int, rejected int) {
	if peer == nil {
		return 0, 0
	}
	return d.MergeRecords(peer.ListWorkers(nowUnix), nowUnix)
}

// ExportSnapshot produces a signed, content-hashed DirectorySnapshot.
func (d *PeerDirectory) ExportSnapshot(kp *KeyPair, peerID string, nowUnix int64) (DirectorySnapshot, error) {
	if kp == nil || peerID == "" {
		return DirectorySnapshot{}, fmt.Errorf("%w: nil keypair or empty peerId", ErrInvalidRequest)
	}
	snap := DirectorySnapshot{
		ProtocolVersion: ProtocolVersion,
		PeerID:          peerID,
		OperatorAddress: kp.Address(),
		ExportedAt:      nowUnix,
		Workers:         d.ListWorkers(nowUnix),
	}
	digest, err := computeSnapshotSHA256(snap)
	if err != nil {
		return DirectorySnapshot{}, err
	}
	snap.SnapshotSHA256 = digest
	sh, err := snapshotStructHash(snap)
	if err != nil {
		return DirectorySnapshot{}, err
	}
	sig, err := kp.SignDigest(d.domain.TypedDataDigest(sh))
	if err != nil {
		return DirectorySnapshot{}, err
	}
	snap.Signature = sig
	return snap, nil
}

// ImportSnapshot verifies snap offline and merges all workers into d.
func (d *PeerDirectory) ImportSnapshot(snap DirectorySnapshot, nowUnix int64) error {
	if err := VerifyDirectorySnapshot(d.domain, snap, nowUnix); err != nil {
		return err
	}
	for _, w := range snap.Workers {
		if err := d.RegisterWorker(w, nowUnix); err != nil {
			return err
		}
	}
	return nil
}

// --- OpenAI-Compatible Request & Response Wire Types ---

// ChatMessage matches the OpenAI chat message object.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest matches the OpenAI /v1/chat/completions payload with
// optional stateless routing hints.
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	User        string        `json:"user,omitempty"`
	Strategy    string        `json:"strategy,omitempty"`
}

// ChatCompletionChoice matches the OpenAI choice object.
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatCompletionUsage matches the OpenAI token usage object.
type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// RouteMetadata reports transparent per-request routing telemetry returned by
// any compatible Enso gateway.
type RouteMetadata struct {
	GatewayID                   string   `json:"gateway_id"`
	GatewayOperator             string   `json:"gateway_operator"`
	OperatorType                string   `json:"operator_type"`
	Strategy                    string   `json:"strategy"`
	ProviderID                  string   `json:"provider_id"`
	CandidatesEvaluated         int      `json:"candidates_evaluated"`
	Attempts                    int      `json:"attempts"`
	FailedProviders             []string `json:"failed_providers,omitempty"`
	FailoverOccurred            bool     `json:"failover_occurred"`
	QuoteCacheHit               bool     `json:"quote_cache_hit"`
	QuoteRefreshMs              float64  `json:"quote_refresh_ms"`
	VerifyLatencyMs             float64  `json:"verify_latency_ms"`
	GatewayFeeBps               uint64   `json:"gateway_fee_bps"`
	ProviderPriceMicroUSDPer1M  uint64   `json:"provider_price_micro_usd_per_1m"`
	EffectivePriceMicroUSDPer1M uint64   `json:"effective_price_micro_usd_per_1m"`
}

// ChatCompletionResponse is OpenAI-compatible on all standard fields while
// attaching the verifiable ExecutionReceipt, originating InferenceQuote, and
// transparent RouteMetadata.
type ChatCompletionResponse struct {
	ID          string                 `json:"id"`
	Object      string                 `json:"object"`
	Created     int64                  `json:"created"`
	Model       string                 `json:"model"`
	Choices     []ChatCompletionChoice `json:"choices"`
	Usage       ChatCompletionUsage    `json:"usage"`
	EnsoQuote   *InferenceQuote        `json:"enso_quote,omitempty"`
	EnsoReceipt *ExecutionReceipt      `json:"enso_receipt,omitempty"`
	EnsoRoute   *RouteMetadata         `json:"enso_route,omitempty"`
}

// ModelObject matches an OpenAI /v1/models list item.
type ModelObject struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	ModelDigest string `json:"model_digest,omitempty"`
	License     string `json:"license,omitempty"`
}

// ModelsListResponse matches the OpenAI /v1/models response envelope.
type ModelsListResponse struct {
	Object string        `json:"object"`
	Data   []ModelObject `json:"data"`
}

// CanonicalRequestHash computes a deterministic SHA-256 commitment over the
// prompt-bearing fields of a ChatCompletionRequest.
func CanonicalRequestHash(req ChatCompletionRequest) string {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 128
	}
	canon := struct {
		Model       string        `json:"model"`
		Messages    []ChatMessage `json:"messages"`
		MaxTokens   int           `json:"max_tokens"`
		Temperature float64       `json:"temperature"`
		User        string        `json:"user,omitempty"`
	}{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   maxTok,
		Temperature: req.Temperature,
		User:        req.User,
	}
	raw, _ := json.Marshal(canon)
	return "sha256:" + SHA256Hex(raw)
}
