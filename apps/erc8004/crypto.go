// SPDX-License-Identifier: Apache-2.0 OR MIT

package erc8004

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/luxfi/crypto/secp256k1"
	"golang.org/x/crypto/sha3"
)

// secp256k1HalfN is floor(N / 2) for secp256k1, used to reject malleable high-S signatures
// consistently across both pure-Go (!cgo) and libsecp256k1 (cgo) builds.
var secp256k1HalfN = func() *big.Int {
	n := secp256k1.S256().Params().N
	return new(big.Int).Rsh(new(big.Int).Set(n), 1)
}()

// EIP712Domain separates ERC-8004 signatures by protocol name, version, EVM chain ID,
// and verifying registry contract address so signatures cannot be replayed across chains.
type EIP712Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// DefaultDomain returns the standard deterministic local test-chain EIP-712 domain
// (chain ID 31337, canonical ERC-8004 registry contract address).
func DefaultDomain() EIP712Domain {
	return EIP712Domain{
		Name:              "ERC-8004 Agent Registry",
		Version:           "1",
		ChainID:           31337,
		VerifyingContract: "0x8004000000000000000000000000000000000001",
	}
}

// DomainSeparator computes the EIP-712 domain separator hash:
// keccak256(abi.encode(typeHash, keccak256(name), keccak256(version), chainId, verifyingContract)).
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

// TypedDataDigest computes the EIP-712 signing digest:
// keccak256("\x19\x01" || domainSeparator || structHash).
func (d EIP712Domain) TypedDataDigest(structHash [32]byte) [32]byte {
	sep := d.DomainSeparator()
	return Keccak256Bytes([]byte{0x19, 0x01}, sep[:], structHash[:])
}

// KeyPair holds an ephemeral secp256k1 signing key and its derived EVM identity.
type KeyPair struct {
	priv               *secp256k1.PrivateKey
	uncompressedPubKey []byte // 65 bytes: 0x04 || X(32) || Y(32)
	compressedPubKey   []byte // 33 bytes
	address            string // EIP-55 checksummed 0x-prefixed EVM address
}

// GenerateKeyPair creates a fresh cryptographically random secp256k1 keypair at runtime.
func GenerateKeyPair() (*KeyPair, error) {
	priv, err := secp256k1.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("erc8004: generate secp256k1 key: %w", err)
	}
	return keyPairFromPrivate(priv)
}

// KeyPairFromSeed derives a deterministic secp256k1 keypair from arbitrary domain-separated
// entropy (useful for reproducible test-chain benchmarks without hardcoding secret keys).
func KeyPairFromSeed(seed []byte) (*KeyPair, error) {
	if len(seed) == 0 {
		return nil, errors.New("erc8004: empty key seed")
	}
	for counter := uint64(0); counter < 1024; counter++ {
		var ctr [8]byte
		binary.BigEndian.PutUint64(ctr[:], counter)
		digest := Keccak256Bytes([]byte("erc8004:seed:v1:"), seed, ctr[:])
		priv, err := secp256k1.ToPrivateKey(digest[:])
		if err == nil {
			return keyPairFromPrivate(priv)
		}
	}
	return nil, errors.New("erc8004: failed to derive valid secp256k1 key from seed")
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

// Address returns the EIP-55 checksummed EVM address (0x-prefixed, 42 chars).
func (k *KeyPair) Address() string {
	return k.address
}

// CompressedPubKeyHex returns the 0x-prefixed 33-byte compressed secp256k1 public key hex.
func (k *KeyPair) CompressedPubKeyHex() string {
	return "0x" + hex.EncodeToString(k.compressedPubKey)
}

// UncompressedPubKeyHex returns the 0x-prefixed 65-byte uncompressed secp256k1 public key hex.
func (k *KeyPair) UncompressedPubKeyHex() string {
	return "0x" + hex.EncodeToString(k.uncompressedPubKey)
}

// SignDigest produces a 65-byte recoverable secp256k1 ECDSA signature ([R || S || V], V in {0,1})
// formatted as a 0x-prefixed hex string.
func (k *KeyPair) SignDigest(digest [32]byte) (string, error) {
	if k == nil || k.priv == nil {
		return "", errors.New("erc8004: nil keypair")
	}
	sig, err := secp256k1.Sign(digest[:], k.priv.Bytes())
	if err != nil {
		return "", fmt.Errorf("erc8004: sign digest: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("erc8004: unexpected signature length %d", len(sig))
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// Keccak256Bytes returns the 32-byte legacy Ethereum Keccak-256 hash of all inputs concatenated.
func Keccak256Bytes(parts ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		_, _ = h.Write(p)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Keccak256Hex returns the 0x-prefixed lowercase hex Keccak-256 digest.
func Keccak256Hex(parts ...[]byte) string {
	d := Keccak256Bytes(parts...)
	return "0x" + hex.EncodeToString(d[:])
}

// AddressFromUncompressedPubKey computes the EIP-55 checksummed EVM address from a
// 65-byte uncompressed secp256k1 public key (0x04 || X || Y).
func AddressFromUncompressedPubKey(uncompressed []byte) (string, error) {
	if len(uncompressed) != 65 || uncompressed[0] != 0x04 {
		return "", errors.New("erc8004: expected 65-byte uncompressed secp256k1 public key")
	}
	digest := Keccak256Bytes(uncompressed[1:])
	return ToChecksumAddress(digest[12:]), nil
}

// AddressFromCompressedPubKeyHex decompresses a 33-byte compressed secp256k1 pubkey hex
// string and returns its EIP-55 EVM address.
func AddressFromCompressedPubKeyHex(pubHex string) (string, error) {
	raw, err := decodeHex(pubHex)
	if err != nil {
		return "", fmt.Errorf("erc8004: decode compressed pubkey: %w", err)
	}
	if len(raw) != 33 {
		return "", fmt.Errorf("erc8004: expected 33-byte compressed pubkey, got %d", len(raw))
	}
	x, y := secp256k1.DecompressPubkey(raw)
	if x == nil || y == nil {
		return "", errors.New("erc8004: invalid compressed secp256k1 public key")
	}
	uncompressed := make([]byte, 65)
	uncompressed[0] = 0x04
	copy(uncompressed[1:33], secp256k1.PaddedBigBytes(x, 32))
	copy(uncompressed[33:65], secp256k1.PaddedBigBytes(y, 32))
	return AddressFromUncompressedPubKey(uncompressed)
}

// ToChecksumAddress formats a 20-byte EVM address with an EIP-55 mixed-case checksum.
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

// NormalizeAddress validates a 20-byte hex EVM address and returns its canonical EIP-55 form.
func NormalizeAddress(addr string) (string, error) {
	raw, err := decodeAddressBytes(addr)
	if err != nil {
		return "", err
	}
	return ToChecksumAddress(raw[:]), nil
}

// SameAddress compares two EVM addresses case-insensitively after validating hex format.
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

// RecoverSignerAddress recovers the EIP-55 EVM address and uncompressed public key from
// a 32-byte digest and a 65-byte recoverable secp256k1 signature hex string.
// Accepts V in {0, 1} or Ethereum {27, 28}, and rejects malleable high-S signatures.
func RecoverSignerAddress(digest [32]byte, sigHex string) (string, []byte, error) {
	sig, err := decodeHex(sigHex)
	if err != nil || len(sig) != 65 {
		return "", nil, fmt.Errorf("%w: signature must be 65 hex bytes", ErrInvalidSignature)
	}
	normalized := make([]byte, 65)
	copy(normalized, sig)
	if normalized[64] == 27 || normalized[64] == 28 {
		normalized[64] -= 27
	}
	if normalized[64] > 1 {
		return "", nil, fmt.Errorf("%w: invalid recovery id %d", ErrInvalidSignature, normalized[64])
	}

	// Reject zero R/S and high-S malleable signatures (EIP-2).
	r := new(big.Int).SetBytes(normalized[:32])
	s := new(big.Int).SetBytes(normalized[32:64])
	if r.Sign() <= 0 || s.Sign() <= 0 || s.Cmp(secp256k1HalfN) > 0 {
		return "", nil, fmt.Errorf("%w: non-canonical or malleable ECDSA scalar", ErrInvalidSignature)
	}

	pub, err := secp256k1.RecoverPubkey(digest[:], normalized)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	addr, err := AddressFromUncompressedPubKey(pub)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	return addr, pub, nil
}

// VerifySigner verifies that sigHex is a valid non-malleable secp256k1 signature over digest
// created by expectedAddr.
func VerifySigner(digest [32]byte, sigHex string, expectedAddr string) error {
	normExpected, err := NormalizeAddress(expectedAddr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnauthorizedSigner, err)
	}
	recovered, _, err := RecoverSignerAddress(digest, sigHex)
	if err != nil {
		return err
	}
	if recovered != normExpected {
		return fmt.Errorf("%w: recovered %s, want %s", ErrUnauthorizedSigner, recovered, normExpected)
	}
	return nil
}

// randomHexID generates a cryptographically random 16-byte 0x-prefixed hex identifier.
func randomHexID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "0x" + hex.EncodeToString(b[:])
}

// eip712HashString encodes a Solidity EIP-712 `string` value as keccak256(bytes(s)).
func eip712HashString(s string) [32]byte {
	return Keccak256Bytes([]byte(s))
}

// eip712WordAddress encodes an EVM `address` as a 32-byte big-endian ABI word (12 zero bytes || 20 address bytes).
func eip712WordAddress(addr string) [32]byte {
	var word [32]byte
	if raw, err := decodeAddressBytes(addr); err == nil {
		copy(word[12:], raw[:])
	}
	return word
}

// eip712WordUint64 encodes a `uint64` as a 32-byte big-endian ABI word.
func eip712WordUint64(v uint64) [32]byte {
	var word [32]byte
	binary.BigEndian.PutUint64(word[24:], v)
	return word
}

// eip712WordInt64 encodes a signed `int64` as a sign-extended 32-byte big-endian two's-complement ABI word.
func eip712WordInt64(v int64) [32]byte {
	var word [32]byte
	if v < 0 {
		for i := 0; i < 24; i++ {
			word[i] = 0xff
		}
	}
	binary.BigEndian.PutUint64(word[24:], uint64(v))
	return word
}

// eip712WordUint8 encodes a `uint8` as a 32-byte big-endian ABI word.
func eip712WordUint8(v uint8) [32]byte {
	var word [32]byte
	word[31] = v
	return word
}

// eip712EncodeStruct computes keccak256(typeHash || word_0 || ... || word_{n-1})
// per EIP-712 encodeData / hashStruct specification.
func eip712EncodeStruct(typeHash [32]byte, words ...[32]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(typeHash[:])
	for i := range words {
		_, _ = h.Write(words[i][:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// eip712HashStringSlice encodes a `string[]` per EIP-712 as keccak256(concat(keccak256(s_i))).
func eip712HashStringSlice(items []string) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, s := range items {
		elem := eip712HashString(s)
		_, _ = h.Write(elem[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// hashStrings deterministically hashes an ordered list of strings with length prefixing
// so concatenation ambiguities ("a","bc" vs "ab","c") are impossible (used for internal IDs).
func hashStrings(items ...string) [32]byte {
	h := sha3.NewLegacyKeccak256()
	var lenBuf [8]byte
	for _, s := range items {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(s))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

var endpointDescriptorTypeHash = Keccak256Bytes([]byte(
	"EndpointDescriptor(string protocol,string url,string version,string authScheme,bytes32 capabilitiesHash)",
))

func hashEndpoints(eps []EndpointDescriptor) [32]byte {
	h := sha3.NewLegacyKeccak256()
	for _, ep := range eps {
		capsHash := eip712HashStringSlice(ep.Capabilities)
		entry := eip712EncodeStruct(
			endpointDescriptorTypeHash,
			eip712HashString(ep.Protocol),
			eip712HashString(ep.URL),
			eip712HashString(ep.Version),
			eip712HashString(ep.AuthScheme),
			capsHash,
		)
		_, _ = h.Write(entry[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
