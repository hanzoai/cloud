// SPDX-License-Identifier: Apache-2.0 OR MIT

package escrow

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/luxfi/crypto/secp256k1"
	"golang.org/x/crypto/sha3"
)

// secp256k1HalfN is floor(N / 2) for secp256k1, used to enforce EIP-2 low-S
// non-malleability consistently across pure-Go (!cgo) and cgo builds.
var secp256k1HalfN = func() *big.Int {
	n := secp256k1.S256().Params().N
	return new(big.Int).Rsh(new(big.Int).Set(n), 1)
}()

// EIP712Domain separates escrow signatures by protocol name, version, chain ID,
// and verifying contract address to prevent cross-chain and cross-app replay.
type EIP712Domain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// DefaultDomain returns the canonical deterministic local test-chain domain for HIP-1350 escrow.
func DefaultDomain() EIP712Domain {
	return EIP712Domain{
		Name:              "Hanzo Compute Escrow",
		Version:           "1",
		ChainID:           31337,
		VerifyingContract: "0xE5c0000000000000000000000000000000000017",
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

// TypedDataDigest computes keccak256("\x19\x01" || domainSeparator || structHash).
func (d EIP712Domain) TypedDataDigest(structHash [32]byte) [32]byte {
	sep := d.DomainSeparator()
	return Keccak256Bytes([]byte{0x19, 0x01}, sep[:], structHash[:])
}

// KeyPair holds an ephemeral secp256k1 signing key and its EIP-55 EVM address.
type KeyPair struct {
	priv               *secp256k1.PrivateKey
	uncompressedPubKey []byte
	compressedPubKey   []byte
	address            string
}

// GenerateKeyPair creates a fresh cryptographically random secp256k1 keypair at runtime.
func GenerateKeyPair() (*KeyPair, error) {
	priv, err := secp256k1.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("escrow: generate secp256k1 key: %w", err)
	}
	return keyPairFromPrivate(priv)
}

// KeyPairFromSeed deterministically derives an ephemeral secp256k1 keypair from arbitrary
// domain-separated bytes for reproducible benchmarks without hardcoding secret keys.
func KeyPairFromSeed(seed []byte) (*KeyPair, error) {
	if len(seed) == 0 {
		return nil, errors.New("escrow: empty key seed")
	}
	for counter := uint64(0); counter < 1024; counter++ {
		var ctr [8]byte
		binary.BigEndian.PutUint64(ctr[:], counter)
		digest := Keccak256Bytes([]byte("escrow:seed:v1:"), seed, ctr[:])
		priv, err := secp256k1.ToPrivateKey(digest[:])
		if err == nil {
			return keyPairFromPrivate(priv)
		}
	}
	return nil, errors.New("escrow: failed to derive valid secp256k1 key from seed")
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

// CompressedPubKeyHex returns the 0x-prefixed 33-byte compressed secp256k1 public key hex.
func (k *KeyPair) CompressedPubKeyHex() string {
	return "0x" + hex.EncodeToString(k.compressedPubKey)
}

// SignDigest produces a 65-byte recoverable secp256k1 ECDSA signature ([R || S || V], V in {0,1}).
func (k *KeyPair) SignDigest(digest [32]byte) (string, error) {
	if k == nil || k.priv == nil {
		return "", errors.New("escrow: nil keypair")
	}
	sig, err := secp256k1.Sign(digest[:], k.priv.Bytes())
	if err != nil {
		return "", fmt.Errorf("escrow: sign digest: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("escrow: unexpected signature length %d", len(sig))
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// Keccak256Bytes returns the 32-byte legacy Ethereum Keccak-256 digest of all inputs concatenated.
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

// AddressFromUncompressedPubKey derives an EIP-55 checksummed EVM address from a 65-byte public key.
func AddressFromUncompressedPubKey(uncompressed []byte) (string, error) {
	if len(uncompressed) != 65 || uncompressed[0] != 0x04 {
		return "", errors.New("escrow: expected 65-byte uncompressed secp256k1 public key")
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

// NormalizeAddress validates and returns the canonical EIP-55 form of an EVM address.
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

func decodeBytes32(s string) ([32]byte, error) {
	var out [32]byte
	raw, err := decodeHex(s)
	if err != nil || len(raw) != 32 {
		return out, fmt.Errorf("%w: expected 32-byte hex digest %q", ErrInvalidRequest, s)
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

// RecoverSignerAddress recovers the EIP-55 EVM address from a 32-byte digest and a 65-byte
// recoverable secp256k1 signature hex string. Rejects malleable high-S signatures (EIP-2).
func RecoverSignerAddress(digest [32]byte, sigHex string) (string, error) {
	sig, err := decodeHex(sigHex)
	if err != nil || len(sig) != 65 {
		return "", fmt.Errorf("%w: signature must be 65 bytes", ErrInvalidSignature)
	}
	sVal := new(big.Int).SetBytes(sig[32:64])
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

// QuoteStructHash computes the canonical EIP-712 struct hash for a JobQuote.
func QuoteStructHash(q JobQuote) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"JobQuote(string jobId,address buyer,address provider,string asset,uint256 jobPriceAtomic,uint256 providerBondAtomic,uint256 challengeBondAtomic,uint256 slaPenaltyAtomic,uint256 challengerRewardBps,int256 deadline,int256 challengeWindowSec,bytes32 inputDigest,string verificationClass,string verificationSpec,uint256 estimatedTokens,uint256 nonce)",
	))
	buyerW, err := encodeAddressWord(q.Buyer)
	if err != nil {
		return [32]byte{}, err
	}
	provW, err := encodeAddressWord(q.Provider)
	if err != nil {
		return [32]byte{}, err
	}
	inputD, err := decodeBytes32(q.InputDigest)
	if err != nil {
		return [32]byte{}, err
	}

	jobIDHash := Keccak256Bytes([]byte(q.JobID))
	assetHash := Keccak256Bytes([]byte(q.Asset))
	priceW := encodeUint64Word(q.JobPriceAtomic)
	provBondW := encodeUint64Word(q.ProviderBondAtomic)
	chalBondW := encodeUint64Word(q.ChallengeBondAtomic)
	slaW := encodeUint64Word(q.SLAPenaltyAtomic)
	rewardW := encodeUint64Word(uint64(q.ChallengerRewardBps))
	deadlineW := encodeInt64Word(q.Deadline)
	windowW := encodeInt64Word(q.ChallengeWindowSec)
	classHash := Keccak256Bytes([]byte(q.VerificationClass))
	specHash := Keccak256Bytes([]byte(q.VerificationSpec))
	tokensW := encodeUint64Word(q.EstimatedTokens)
	nonceW := encodeUint64Word(q.Nonce)

	return Keccak256Bytes(
		typeHash[:],
		jobIDHash[:],
		buyerW[:],
		provW[:],
		assetHash[:],
		priceW[:],
		provBondW[:],
		chalBondW[:],
		slaW[:],
		rewardW[:],
		deadlineW[:],
		windowW[:],
		inputD[:],
		classHash[:],
		specHash[:],
		tokensW[:],
		nonceW[:],
	), nil
}

// SignJobQuote computes QuoteHash and attaches Provider's EIP-712 signature.
func SignJobQuote(domain EIP712Domain, kp *KeyPair, q *JobQuote) error {
	if kp == nil || q == nil {
		return ErrInvalidRequest
	}
	q.Provider = kp.Address()
	sh, err := QuoteStructHash(*q)
	if err != nil {
		return err
	}
	digest := domain.TypedDataDigest(sh)
	sig, err := kp.SignDigest(digest)
	if err != nil {
		return err
	}
	q.QuoteHash = "0x" + hex.EncodeToString(digest[:])
	q.ProviderSignature = sig
	return nil
}

// VerifyJobQuote verifies the EIP-712 signature on a JobQuote and checks QuoteHash consistency.
func VerifyJobQuote(domain EIP712Domain, q JobQuote) error {
	sh, err := QuoteStructHash(q)
	if err != nil {
		return err
	}
	digest := domain.TypedDataDigest(sh)
	expectedHash := "0x" + hex.EncodeToString(digest[:])
	if q.QuoteHash != "" && !strings.EqualFold(q.QuoteHash, expectedHash) {
		return fmt.Errorf("%w: quote hash mismatch", ErrInvalidRequest)
	}
	signer, err := RecoverSignerAddress(digest, q.ProviderSignature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, q.Provider) {
		return fmt.Errorf("%w: quote signer %s != provider %s", ErrSignerMismatch, signer, q.Provider)
	}
	return nil
}

// AcceptanceStructHash computes the EIP-712 struct hash for a buyer's QuoteAcceptance.
func AcceptanceStructHash(a QuoteAcceptance) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"QuoteAcceptance(string jobId,bytes32 quoteHash,address buyer,uint256 jobPriceAtomic,int256 acceptedAt)",
	))
	qHash, err := decodeBytes32(a.QuoteHash)
	if err != nil {
		return [32]byte{}, err
	}
	buyerW, err := encodeAddressWord(a.Buyer)
	if err != nil {
		return [32]byte{}, err
	}
	jobIDHash := Keccak256Bytes([]byte(a.JobID))
	priceW := encodeUint64Word(a.JobPriceAtomic)
	accW := encodeInt64Word(a.AcceptedAt)
	return Keccak256Bytes(typeHash[:], jobIDHash[:], qHash[:], buyerW[:], priceW[:], accW[:]), nil
}

// SignQuoteAcceptance signs buyer acceptance of a JobQuote via EIP-712.
func SignQuoteAcceptance(domain EIP712Domain, kp *KeyPair, a *QuoteAcceptance) error {
	if kp == nil || a == nil {
		return ErrInvalidRequest
	}
	a.Buyer = kp.Address()
	sh, err := AcceptanceStructHash(*a)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	a.BuyerSignature = sig
	return nil
}

// VerifyQuoteAcceptance verifies the buyer's EIP-712 signature on QuoteAcceptance.
func VerifyQuoteAcceptance(domain EIP712Domain, a QuoteAcceptance) error {
	sh, err := AcceptanceStructHash(a)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), a.BuyerSignature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, a.Buyer) {
		return fmt.Errorf("%w: acceptance signer %s != buyer %s", ErrSignerMismatch, signer, a.Buyer)
	}
	return nil
}

// ClaimStructHash computes the EIP-712 struct hash for a provider's CompletionClaim.
func ClaimStructHash(c CompletionClaim) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"CompletionClaim(string jobId,bytes32 quoteHash,address provider,bytes32 outputDigest,bytes32 executionProofHash,uint256 tokensProcessed,int256 completedAt,int256 challengeDeadline)",
	))
	qHash, err := decodeBytes32(c.QuoteHash)
	if err != nil {
		return [32]byte{}, err
	}
	provW, err := encodeAddressWord(c.Provider)
	if err != nil {
		return [32]byte{}, err
	}
	outD, err := decodeBytes32(c.OutputDigest)
	if err != nil {
		return [32]byte{}, err
	}
	jobIDHash := Keccak256Bytes([]byte(c.JobID))
	proofHash := Keccak256Bytes([]byte(c.ExecutionProof))
	tokW := encodeUint64Word(c.TokensProcessed)
	compW := encodeInt64Word(c.CompletedAt)
	chalW := encodeInt64Word(c.ChallengeDeadline)
	return Keccak256Bytes(typeHash[:], jobIDHash[:], qHash[:], provW[:], outD[:], proofHash[:], tokW[:], compW[:], chalW[:]), nil
}

// SignCompletionClaim signs a provider's CompletionClaim via EIP-712.
func SignCompletionClaim(domain EIP712Domain, kp *KeyPair, c *CompletionClaim) error {
	if kp == nil || c == nil {
		return ErrInvalidRequest
	}
	c.Provider = kp.Address()
	sh, err := ClaimStructHash(*c)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	c.ProviderSignature = sig
	return nil
}

// VerifyCompletionClaim verifies the provider's EIP-712 signature on CompletionClaim.
func VerifyCompletionClaim(domain EIP712Domain, c CompletionClaim) error {
	sh, err := ClaimStructHash(c)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), c.ProviderSignature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, c.Provider) {
		return fmt.Errorf("%w: claim signer %s != provider %s", ErrSignerMismatch, signer, c.Provider)
	}
	return nil
}

// EvidenceDigest deterministically hashes the caller-supplied ChallengeEvidence fields
// for EIP-712 signing, independent of the engine-derived ProofDomain classification tag.
func EvidenceDigest(ev ChallengeEvidence) ([32]byte, error) {
	canon := ChallengeEvidence{
		ViolationType:        ev.ViolationType,
		CanonicalInput:       ev.CanonicalInput,
		ObservedOutput:       ev.ObservedOutput,
		ExpectedOutputDigest: ev.ExpectedOutputDigest,
		ConflictingClaim:     ev.ConflictingClaim,
		SubjectiveNote:       ev.SubjectiveNote,
	}
	raw, err := json.Marshal(canon)
	if err != nil {
		return [32]byte{}, err
	}
	return Keccak256Bytes([]byte("escrow:evidence:v1:"), raw), nil
}

// ChallengeStructHash computes the EIP-712 struct hash for a ChallengeSubmission.
func ChallengeStructHash(ch ChallengeSubmission) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"ChallengeSubmission(string jobId,address challenger,uint256 challengeBondAtomic,int256 submittedAt,string violationType,bytes32 evidenceHash)",
	))
	chalW, err := encodeAddressWord(ch.Challenger)
	if err != nil {
		return [32]byte{}, err
	}
	evHash, err := EvidenceDigest(ch.Evidence)
	if err != nil {
		return [32]byte{}, err
	}
	if ch.EvidenceHash != "" {
		provided, decErr := decodeBytes32(ch.EvidenceHash)
		if decErr != nil || provided != evHash {
			return [32]byte{}, fmt.Errorf("%w: evidence hash mismatch", ErrInvalidRequest)
		}
	}
	jobIDHash := Keccak256Bytes([]byte(ch.JobID))
	bondW := encodeUint64Word(ch.ChallengeBondAtomic)
	subW := encodeInt64Word(ch.SubmittedAt)
	vHash := Keccak256Bytes([]byte(ch.Evidence.ViolationType))
	return Keccak256Bytes(typeHash[:], jobIDHash[:], chalW[:], bondW[:], subW[:], vHash[:], evHash[:]), nil
}

// SignChallengeSubmission sets EvidenceHash and attaches Challenger's EIP-712 signature.
func SignChallengeSubmission(domain EIP712Domain, kp *KeyPair, ch *ChallengeSubmission) error {
	if kp == nil || ch == nil {
		return ErrInvalidRequest
	}
	ch.Challenger = kp.Address()
	evHash, err := EvidenceDigest(ch.Evidence)
	if err != nil {
		return err
	}
	ch.EvidenceHash = "0x" + hex.EncodeToString(evHash[:])
	sh, err := ChallengeStructHash(*ch)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	ch.ChallengerSignature = sig
	return nil
}

// VerifyChallengeSubmission verifies the challenger's EIP-712 signature on ChallengeSubmission.
func VerifyChallengeSubmission(domain EIP712Domain, ch ChallengeSubmission) error {
	sh, err := ChallengeStructHash(ch)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), ch.ChallengerSignature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, ch.Challenger) {
		return fmt.Errorf("%w: challenge signer %s != challenger %s", ErrSignerMismatch, signer, ch.Challenger)
	}
	return nil
}
