// SPDX-License-Identifier: Apache-2.0 OR MIT

package sovereignty

import (
	"crypto/aes"
	"crypto/cipher"
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

	"github.com/luxfi/crypto/secp256k1"
	"golang.org/x/crypto/sha3"
)

// secp256k1HalfN is floor(N / 2) for secp256k1, used to enforce EIP-2 low-S
// signature non-malleability consistently across CGO_ENABLED=0 and cgo builds.
var secp256k1HalfN = func() *big.Int {
	n := secp256k1.S256().Params().N
	return new(big.Int).Rsh(new(big.Int).Set(n), 1)
}()

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

// KeyPair holds an ephemeral runtime secp256k1 signing key and its EIP-55 EVM address.
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
		return nil, fmt.Errorf("sovereignty: generate secp256k1 key: %w", err)
	}
	return keyPairFromPrivate(priv)
}

// KeyPairFromSeed deterministically derives an ephemeral secp256k1 keypair from
// domain-separated bytes for reproducible benchmarks without hardcoding secrets.
func KeyPairFromSeed(seed []byte) (*KeyPair, error) {
	if len(seed) == 0 {
		return nil, errors.New("sovereignty: empty key seed")
	}
	for counter := uint64(0); counter < 1024; counter++ {
		var ctr [8]byte
		binary.BigEndian.PutUint64(ctr[:], counter)
		digest := Keccak256Bytes([]byte("sovereignty:seed:v1:"), seed, ctr[:])
		priv, err := secp256k1.ToPrivateKey(digest[:])
		if err == nil {
			return keyPairFromPrivate(priv)
		}
	}
	return nil, errors.New("sovereignty: failed to derive secp256k1 key from seed")
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

// SignDigest produces a 65-byte recoverable secp256k1 ECDSA signature ([R || S || V]).
func (k *KeyPair) SignDigest(digest [32]byte) (string, error) {
	if k == nil || k.priv == nil {
		return "", errors.New("sovereignty: nil keypair")
	}
	sig, err := secp256k1.Sign(digest[:], k.priv.Bytes())
	if err != nil {
		return "", fmt.Errorf("sovereignty: sign digest: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("sovereignty: unexpected signature length %d", len(sig))
	}
	return "0x" + hex.EncodeToString(sig), nil
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

// AddressFromUncompressedPubKey derives an EIP-55 checksummed EVM address.
func AddressFromUncompressedPubKey(uncompressed []byte) (string, error) {
	if len(uncompressed) != 65 || uncompressed[0] != 0x04 {
		return "", errors.New("sovereignty: expected 65-byte uncompressed secp256k1 public key")
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

// RecoverSignerAddress recovers the EIP-55 EVM address from a 32-byte digest and
// a 65-byte recoverable secp256k1 signature hex string. Rejects high-S signatures.
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

// --- EIP-712 Typed Data Struct Hashes & Signing Helpers ---

func providerAdStructHash(ad ProviderAdvertisement) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte("ProviderAdvertisement(string providerId,address operatorAddress,string endpointUrl,string modelsCsv,uint256 pricePer1kAtomic,uint256 capacityRpm,int64 issuedAt,int64 expiresAt)"))
	opWord, err := encodeAddressWord(ad.OperatorAddress)
	if err != nil {
		return [32]byte{}, err
	}
	models := append([]string(nil), ad.ModelIDs...)
	sort.Strings(models)
	idHash := Keccak256Bytes([]byte(ad.ProviderID))
	urlHash := Keccak256Bytes([]byte(ad.EndpointURL))
	modelsHash := Keccak256Bytes([]byte(strings.Join(models, ",")))
	priceWord := encodeUint64Word(ad.PricePer1KAtomic)
	capWord := encodeUint64Word(uint64(ad.CapacityRPM))
	issuedWord := encodeInt64Word(ad.IssuedAt)
	expWord := encodeInt64Word(ad.ExpiresAt)
	return Keccak256Bytes(typeHash[:], idHash[:], opWord[:], urlHash[:], modelsHash[:], priceWord[:], capWord[:], issuedWord[:], expWord[:]), nil
}

// SignProviderAdvertisement signs ad with kp under domain.
func SignProviderAdvertisement(domain EIP712Domain, kp *KeyPair, ad *ProviderAdvertisement) error {
	ad.OperatorAddress = kp.Address()
	sh, err := providerAdStructHash(*ad)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	ad.Signature = sig
	return nil
}

// VerifyProviderAdvertisement verifies the EIP-712 signature on ad.
func VerifyProviderAdvertisement(domain EIP712Domain, ad ProviderAdvertisement) error {
	if ad.ProviderID == "" || ad.EndpointURL == "" {
		return fmt.Errorf("%w: provider advertisement missing id or endpoint", ErrInvalidRequest)
	}
	sh, err := providerAdStructHash(ad)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), ad.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, ad.OperatorAddress) {
		return fmt.Errorf("%w: expected %s, got %s", ErrSignerMismatch, ad.OperatorAddress, signer)
	}
	return nil
}

func modelManifestStructHash(m ModelManifest) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte("ModelManifest(string modelId,string version,string license,string digestSha256,int64 sizeBytes,string mirrorsCsv,address publisherAddress,int64 issuedAt)"))
	pubWord, err := encodeAddressWord(m.PublisherAddress)
	if err != nil {
		return [32]byte{}, err
	}
	mirrors := append([]string(nil), m.Mirrors...)
	sort.Strings(mirrors)
	idHash := Keccak256Bytes([]byte(m.ModelID))
	verHash := Keccak256Bytes([]byte(m.Version))
	licHash := Keccak256Bytes([]byte(m.License))
	digHash := Keccak256Bytes([]byte(strings.ToLower(m.DigestSHA256)))
	sizeWord := encodeInt64Word(m.SizeBytes)
	mirHash := Keccak256Bytes([]byte(strings.Join(mirrors, ",")))
	issuedWord := encodeInt64Word(m.IssuedAt)
	return Keccak256Bytes(typeHash[:], idHash[:], verHash[:], licHash[:], digHash[:], sizeWord[:], mirHash[:], pubWord[:], issuedWord[:]), nil
}

// SignModelManifest signs a content-addressed ModelManifest under domain.
func SignModelManifest(domain EIP712Domain, kp *KeyPair, m *ModelManifest) error {
	m.PublisherAddress = kp.Address()
	sh, err := modelManifestStructHash(*m)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	m.Signature = sig
	return nil
}

// VerifyModelManifest verifies the EIP-712 publisher signature on m.
func VerifyModelManifest(domain EIP712Domain, m ModelManifest) error {
	if m.ModelID == "" || len(m.DigestSHA256) != 64 {
		return fmt.Errorf("%w: model manifest missing id or 64-hex SHA-256 digest", ErrInvalidRequest)
	}
	sh, err := modelManifestStructHash(m)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), m.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, m.PublisherAddress) {
		return fmt.Errorf("%w: expected %s, got %s", ErrSignerMismatch, m.PublisherAddress, signer)
	}
	return nil
}

func executionReceiptStructHash(r InferenceExecutionReceipt) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte("InferenceExecutionReceipt(string jobId,string providerId,address providerAddress,string modelId,string modelDigestSha256,string inputDigestSha256,string outputDigestSha256,uint256 tokensGenerated,int64 executedAt,string verificationClass)"))
	provWord, err := encodeAddressWord(r.ProviderAddress)
	if err != nil {
		return [32]byte{}, err
	}
	jobHash := Keccak256Bytes([]byte(r.JobID))
	pidHash := Keccak256Bytes([]byte(r.ProviderID))
	modHash := Keccak256Bytes([]byte(r.ModelID))
	mdigHash := Keccak256Bytes([]byte(strings.ToLower(r.ModelDigestSHA256)))
	idigHash := Keccak256Bytes([]byte(strings.ToLower(r.InputDigestSHA256)))
	odigHash := Keccak256Bytes([]byte(strings.ToLower(r.OutputDigestSHA256)))
	tokWord := encodeUint64Word(uint64(r.TokensGenerated))
	execWord := encodeInt64Word(r.ExecutedAt)
	clsHash := Keccak256Bytes([]byte(r.VerificationClass))
	return Keccak256Bytes(typeHash[:], jobHash[:], pidHash[:], provWord[:], modHash[:], mdigHash[:], idigHash[:], odigHash[:], tokWord[:], execWord[:], clsHash[:]), nil
}

// SignExecutionReceipt signs r with the executing provider's secp256k1 key.
func SignExecutionReceipt(domain EIP712Domain, kp *KeyPair, r *InferenceExecutionReceipt) error {
	r.ProviderAddress = kp.Address()
	sh, err := executionReceiptStructHash(*r)
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

// VerifyExecutionReceiptOffline performs completely offline cryptographic and
// deterministic recomputation verification of an InferenceExecutionReceipt.
func VerifyExecutionReceiptOffline(domain EIP712Domain, req InferenceRequest, modelBlob []byte, r InferenceExecutionReceipt) error {
	if r.JobID != req.JobID || r.ModelID != req.ModelID {
		return fmt.Errorf("%w: receipt job/model mismatch", ErrInvalidReceipt)
	}
	actualModelDigest := SHA256Hex(modelBlob)
	if !strings.EqualFold(actualModelDigest, req.ModelDigestSHA256) || !strings.EqualFold(actualModelDigest, r.ModelDigestSHA256) {
		return fmt.Errorf("%w: model digest mismatch", ErrModelDigestMismatch)
	}
	expectedInputDigest := ComputeRequestInputDigest(req)
	if !strings.EqualFold(expectedInputDigest, r.InputDigestSHA256) {
		return fmt.Errorf("%w: input digest mismatch", ErrInvalidReceipt)
	}
	sh, err := executionReceiptStructHash(r)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), r.Signature)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReceipt, err)
	}
	if !SameAddress(signer, r.ProviderAddress) {
		return fmt.Errorf("%w: receipt signer %s != declared provider %s", ErrInvalidReceipt, signer, r.ProviderAddress)
	}
	expectedOut, expectedOutDigest, expectedTokens := ExecuteDeterministicInference(modelBlob, req.Prompt, req.Seed, req.MaxTokens)
	if r.TokensGenerated != expectedTokens || r.OutputText != expectedOut || !strings.EqualFold(r.OutputDigestSHA256, expectedOutDigest) {
		return fmt.Errorf("%w: deterministic recomputation output mismatch", ErrInvalidReceipt)
	}
	return nil
}

func paymentAuthStructHash(p PaymentAuthorization) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte("PaymentAuthorization(string paymentId,string jobId,address payerAddress,address payeeAddress,uint256 amountAtomic,uint256 nonce,int64 validBefore,uint256 chainId)"))
	payerWord, err := encodeAddressWord(p.PayerAddress)
	if err != nil {
		return [32]byte{}, err
	}
	payeeWord, err := encodeAddressWord(p.PayeeAddress)
	if err != nil {
		return [32]byte{}, err
	}
	pidHash := Keccak256Bytes([]byte(p.PaymentID))
	jidHash := Keccak256Bytes([]byte(p.JobID))
	amtWord := encodeUint64Word(p.AmountAtomic)
	nonceWord := encodeUint64Word(p.Nonce)
	validWord := encodeInt64Word(p.ValidBefore)
	chainWord := encodeUint64Word(p.ChainID)
	return Keccak256Bytes(typeHash[:], pidHash[:], jidHash[:], payerWord[:], payeeWord[:], amtWord[:], nonceWord[:], validWord[:], chainWord[:]), nil
}

// SignPaymentAuthorization signs a portable EIP-712 payment authorization.
func SignPaymentAuthorization(domain EIP712Domain, kp *KeyPair, p *PaymentAuthorization) error {
	p.PayerAddress = kp.Address()
	if p.ChainID == 0 {
		p.ChainID = domain.ChainID
	}
	sh, err := paymentAuthStructHash(*p)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	p.Signature = sig
	return nil
}

// VerifyPaymentAuthorization verifies the EIP-712 payer signature on p.
func VerifyPaymentAuthorization(domain EIP712Domain, p PaymentAuthorization) error {
	if p.PaymentID == "" || p.JobID == "" || p.AmountAtomic == 0 {
		return fmt.Errorf("%w: payment authorization missing required fields", ErrInvalidRequest)
	}
	if p.ChainID != domain.ChainID {
		return fmt.Errorf("%w: chainId %d != domain %d", ErrInvalidRequest, p.ChainID, domain.ChainID)
	}
	sh, err := paymentAuthStructHash(p)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), p.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, p.PayerAddress) {
		return fmt.Errorf("%w: expected %s, got %s", ErrSignerMismatch, p.PayerAddress, signer)
	}
	return nil
}

func settlementReceiptStructHash(s SettlementReceipt) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte("SettlementReceipt(string paymentId,string jobId,string facilitatorId,address facilitatorAddress,address payerAddress,address payeeAddress,uint256 amountAtomic,int64 settledAt,string prevLedgerHash,string ledgerHash)"))
	facWord, err := encodeAddressWord(s.FacilitatorAddress)
	if err != nil {
		return [32]byte{}, err
	}
	payerWord, err := encodeAddressWord(s.PayerAddress)
	if err != nil {
		return [32]byte{}, err
	}
	payeeWord, err := encodeAddressWord(s.PayeeAddress)
	if err != nil {
		return [32]byte{}, err
	}
	pidHash := Keccak256Bytes([]byte(s.PaymentID))
	jidHash := Keccak256Bytes([]byte(s.JobID))
	fidHash := Keccak256Bytes([]byte(s.FacilitatorID))
	amtWord := encodeUint64Word(s.AmountAtomic)
	setWord := encodeInt64Word(s.SettledAt)
	prevHash := Keccak256Bytes([]byte(s.PrevLedgerHash))
	curHash := Keccak256Bytes([]byte(s.LedgerHash))
	return Keccak256Bytes(typeHash[:], pidHash[:], jidHash[:], fidHash[:], facWord[:], payerWord[:], payeeWord[:], amtWord[:], setWord[:], prevHash[:], curHash[:]), nil
}

// SignSettlementReceipt signs s with the facilitator's secp256k1 key.
func SignSettlementReceipt(domain EIP712Domain, kp *KeyPair, s *SettlementReceipt) error {
	s.FacilitatorAddress = kp.Address()
	sh, err := settlementReceiptStructHash(*s)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	s.Signature = sig
	return nil
}

// VerifySettlementReceipt verifies a facilitator's EIP-712 settlement signature.
func VerifySettlementReceipt(domain EIP712Domain, s SettlementReceipt) error {
	sh, err := settlementReceiptStructHash(s)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), s.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, s.FacilitatorAddress) {
		return fmt.Errorf("%w: expected %s, got %s", ErrSignerMismatch, s.FacilitatorAddress, signer)
	}
	return nil
}

func governanceVoteStructHash(v GovernanceVote) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte("GovernanceVote(string proposalId,string action,string payload,address voterAddress,uint256 approve,int64 votedAt)"))
	voterWord, err := encodeAddressWord(v.VoterAddress)
	if err != nil {
		return [32]byte{}, err
	}
	var approve uint64
	if v.Approve {
		approve = 1
	}
	pidHash := Keccak256Bytes([]byte(v.ProposalID))
	actHash := Keccak256Bytes([]byte(v.Action))
	payHash := Keccak256Bytes([]byte(v.Payload))
	appWord := encodeUint64Word(approve)
	atWord := encodeInt64Word(v.VotedAt)
	return Keccak256Bytes(typeHash[:], pidHash[:], actHash[:], payHash[:], voterWord[:], appWord[:], atWord[:]), nil
}

// SignGovernanceVote signs an independent community governance vote.
func SignGovernanceVote(domain EIP712Domain, kp *KeyPair, v *GovernanceVote) error {
	v.VoterAddress = kp.Address()
	sh, err := governanceVoteStructHash(*v)
	if err != nil {
		return err
	}
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	v.Signature = sig
	return nil
}

// VerifyGovernanceVote verifies a signed governance vote.
func VerifyGovernanceVote(domain EIP712Domain, v GovernanceVote) error {
	sh, err := governanceVoteStructHash(v)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), v.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, v.VoterAddress) {
		return fmt.Errorf("%w: expected %s, got %s", ErrSignerMismatch, v.VoterAddress, signer)
	}
	return nil
}

// --- User-Sovereign AES-256-GCM Encrypted Data Envelopes ---

// GenerateUserDataKey creates a fresh 32-byte non-custodial user data key at runtime.
func GenerateUserDataKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("sovereignty: generate user data key: %w", err)
	}
	return key, nil
}

func deriveEnvelopeAESKey(userKey []byte, salt []byte, ownerAddress, snapshotID string) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("hip-1351:user-vault-aes256gcm:v1:"))
	_, _ = h.Write(userKey)
	_, _ = h.Write(salt)
	_, _ = h.Write([]byte(strings.ToLower(ownerAddress)))
	_, _ = h.Write([]byte(snapshotID))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func envelopeStructHash(e EncryptedDataEnvelope) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte("EncryptedDataEnvelope(string snapshotId,address ownerAddress,string algorithm,string saltHex,string nonceHex,string ciphertextSha256,string plaintextSha256,int64 createdAt)"))
	ownerWord, err := encodeAddressWord(e.OwnerAddress)
	if err != nil {
		return [32]byte{}, err
	}
	sidHash := Keccak256Bytes([]byte(e.SnapshotID))
	algHash := Keccak256Bytes([]byte(e.Algorithm))
	saltHash := Keccak256Bytes([]byte(e.SaltHex))
	nonceHash := Keccak256Bytes([]byte(e.NonceHex))
	ctHash := Keccak256Bytes([]byte(SHA256Hex([]byte(e.CiphertextHex))))
	ptHash := Keccak256Bytes([]byte(strings.ToLower(e.PlaintextSHA256)))
	atWord := encodeInt64Word(e.CreatedAt)
	return Keccak256Bytes(typeHash[:], sidHash[:], ownerWord[:], algHash[:], saltHash[:], nonceHash[:], ctHash[:], ptHash[:], atWord[:]), nil
}

// EncryptUserData encrypts plaintext client-side with AES-256-GCM under userKey
// and signs the resulting portable envelope with ownerKP. Zero custodial escrow.
func EncryptUserData(
	domain EIP712Domain,
	ownerKP *KeyPair,
	userKey []byte,
	snapshotID string,
	plaintext []byte,
	mirrors []string,
	allowedOperators []string,
	createdAt int64,
) (EncryptedDataEnvelope, error) {
	if ownerKP == nil || len(userKey) != 32 || snapshotID == "" {
		return EncryptedDataEnvelope{}, fmt.Errorf("%w: invalid encrypt parameters", ErrInvalidRequest)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return EncryptedDataEnvelope{}, err
	}
	aesKey := deriveEnvelopeAESKey(userKey, salt, ownerKP.Address(), snapshotID)
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return EncryptedDataEnvelope{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return EncryptedDataEnvelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return EncryptedDataEnvelope{}, err
	}
	aad := []byte(strings.ToLower(ownerKP.Address()) + "|" + snapshotID)
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)

	env := EncryptedDataEnvelope{
		SnapshotID:         snapshotID,
		OwnerAddress:       ownerKP.Address(),
		Algorithm:          "AES-256-GCM+SHA256-KDF",
		SaltHex:            hex.EncodeToString(salt),
		NonceHex:           hex.EncodeToString(nonce),
		CiphertextHex:      hex.EncodeToString(ciphertext),
		PlaintextSHA256:    SHA256Hex(plaintext),
		PlaintextSizeBytes: len(plaintext),
		CapabilityManifest: DataCapabilityManifest{
			OwnerAddress:     ownerKP.Address(),
			ExportFormat:     "hip-1351-encrypted-envelope/v1",
			PortableMirrors:  append([]string(nil), mirrors...),
			AllowedOperators: append([]string(nil), allowedOperators...),
			CustodialEscrow:  false,
		},
		CreatedAt: createdAt,
	}
	sh, err := envelopeStructHash(env)
	if err != nil {
		return EncryptedDataEnvelope{}, err
	}
	sig, err := ownerKP.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return EncryptedDataEnvelope{}, err
	}
	env.OwnerSignature = sig
	return env, nil
}

// DecryptUserData verifies the owner's EIP-712 envelope signature, authenticates
// AES-256-GCM ciphertext with userKey, and verifies the recovered SHA-256 digest.
// Fails closed with ErrUnauthorizedDataAccess on wrong/missing key or tampering.
func DecryptUserData(domain EIP712Domain, userKey []byte, env EncryptedDataEnvelope) ([]byte, error) {
	if len(userKey) != 32 {
		return nil, fmt.Errorf("%w: missing or wrong-length 32-byte sovereign user key", ErrUnauthorizedDataAccess)
	}
	sh, err := envelopeStructHash(env)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorizedDataAccess, err)
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), env.OwnerSignature)
	if err != nil || !SameAddress(signer, env.OwnerAddress) {
		return nil, fmt.Errorf("%w: invalid owner envelope signature", ErrUnauthorizedDataAccess)
	}
	salt, err := hex.DecodeString(env.SaltHex)
	if err != nil || len(salt) != 16 {
		return nil, fmt.Errorf("%w: invalid salt", ErrUnauthorizedDataAccess)
	}
	nonce, err := hex.DecodeString(env.NonceHex)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid nonce", ErrUnauthorizedDataAccess)
	}
	ciphertext, err := hex.DecodeString(env.CiphertextHex)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid ciphertext", ErrUnauthorizedDataAccess)
	}
	aesKey := deriveEnvelopeAESKey(userKey, salt, env.OwnerAddress, env.SnapshotID)
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w: invalid gcm nonce length", ErrUnauthorizedDataAccess)
	}
	aad := []byte(strings.ToLower(env.OwnerAddress) + "|" + env.SnapshotID)
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: AES-GCM authentication failed", ErrUnauthorizedDataAccess)
	}
	if !strings.EqualFold(SHA256Hex(plaintext), env.PlaintextSHA256) {
		return nil, fmt.Errorf("%w: decrypted plaintext digest mismatch", ErrUnauthorizedDataAccess)
	}
	return plaintext, nil
}

// VerifyEncryptedEnvelope verifies an EncryptedDataEnvelope's non-custodial
// capability manifest and owner EIP-712 signature offline.
func VerifyEncryptedEnvelope(domain EIP712Domain, env EncryptedDataEnvelope) error {
	if strings.TrimSpace(env.SnapshotID) == "" {
		return fmt.Errorf("%w: empty snapshotId", ErrInvalidRequest)
	}
	if env.CapabilityManifest.CustodialEscrow {
		return fmt.Errorf("%w: custodial escrow must be false", ErrInvalidRequest)
	}
	sh, err := envelopeStructHash(env)
	if err != nil {
		return err
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), env.OwnerSignature)
	if err != nil || !SameAddress(signer, env.OwnerAddress) {
		return ErrInvalidSignature
	}
	return nil
}

// --- Fork Bundle Integrity & Signing ---

func computeForkBundleDigest(b NetworkForkBundle) (string, error) {
	clone := b
	clone.BundleSHA256 = ""
	clone.OperatorSignature = ""
	raw, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	return SHA256Hex(raw), nil
}

// SignForkBundle computes the canonical SHA-256 digest of a NetworkForkBundle and
// signs it with the exporting community operator's secp256k1 key.
func SignForkBundle(domain EIP712Domain, kp *KeyPair, b *NetworkForkBundle) error {
	b.OperatorAddress = kp.Address()
	digest, err := computeForkBundleDigest(*b)
	if err != nil {
		return err
	}
	b.BundleSHA256 = digest
	typeHash := Keccak256Bytes([]byte("NetworkForkBundle(string protocolVersion,string operatorId,address operatorAddress,string bundleSha256,int64 exportedAt)"))
	opWord, err := encodeAddressWord(b.OperatorAddress)
	if err != nil {
		return err
	}
	pvHash := Keccak256Bytes([]byte(b.ProtocolVersion))
	oidHash := Keccak256Bytes([]byte(b.OperatorID))
	bdHash := Keccak256Bytes([]byte(strings.ToLower(b.BundleSHA256)))
	expWord := encodeInt64Word(b.ExportedAt)
	sh := Keccak256Bytes(typeHash[:], pvHash[:], oidHash[:], opWord[:], bdHash[:], expWord[:])
	sig, err := kp.SignDigest(domain.TypedDataDigest(sh))
	if err != nil {
		return err
	}
	b.OperatorSignature = sig
	return nil
}

// VerifyForkBundleOffline verifies the bundle digest, operator signature, and
// every signed provider advertisement, model manifest and artifact blob,
// settlement receipt, governance vote, and owner-signed vault envelope
// contained inside the fork bundle completely offline.
func VerifyForkBundleOffline(domain EIP712Domain, b NetworkForkBundle) error {
	if b.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol version %q", ErrForkBundleCorrupted, b.ProtocolVersion)
	}
	expectedDigest, err := computeForkBundleDigest(b)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrForkBundleCorrupted, err)
	}
	if !strings.EqualFold(expectedDigest, b.BundleSHA256) {
		return fmt.Errorf("%w: bundle SHA-256 mismatch", ErrForkBundleCorrupted)
	}
	typeHash := Keccak256Bytes([]byte("NetworkForkBundle(string protocolVersion,string operatorId,address operatorAddress,string bundleSha256,int64 exportedAt)"))
	opWord, err := encodeAddressWord(b.OperatorAddress)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrForkBundleCorrupted, err)
	}
	pvHash := Keccak256Bytes([]byte(b.ProtocolVersion))
	oidHash := Keccak256Bytes([]byte(b.OperatorID))
	bdHash := Keccak256Bytes([]byte(strings.ToLower(b.BundleSHA256)))
	expWord := encodeInt64Word(b.ExportedAt)
	sh := Keccak256Bytes(typeHash[:], pvHash[:], oidHash[:], opWord[:], bdHash[:], expWord[:])
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), b.OperatorSignature)
	if err != nil || !SameAddress(signer, b.OperatorAddress) {
		return fmt.Errorf("%w: invalid operator signature", ErrForkBundleCorrupted)
	}
	for _, p := range b.Providers {
		if err := VerifyProviderAdvertisement(domain, p); err != nil {
			return fmt.Errorf("%w: provider %s: %v", ErrForkBundleCorrupted, p.ProviderID, err)
		}
	}
	verifiedBlobs := make(map[string]int64, len(b.ModelBlobs))
	for digest, blob := range b.ModelBlobs {
		normDigest := strings.ToLower(strings.TrimSpace(digest))
		actual := SHA256Hex(blob)
		if normDigest == "" || !strings.EqualFold(actual, normDigest) {
			return fmt.Errorf("%w: model blob %s digest mismatch (got %s)", ErrForkBundleCorrupted, digest, actual)
		}
		verifiedBlobs[normDigest] = int64(len(blob))
	}
	for _, m := range b.Models {
		if err := VerifyModelManifest(domain, m); err != nil {
			return fmt.Errorf("%w: model %s: %v", ErrForkBundleCorrupted, m.ModelID, err)
		}
		normDigest := strings.ToLower(strings.TrimSpace(m.DigestSHA256))
		size, ok := verifiedBlobs[normDigest]
		if !ok || size != m.SizeBytes {
			return fmt.Errorf("%w: model %s missing verified artifact blob %s", ErrForkBundleCorrupted, m.ModelID, normDigest)
		}
	}
	prevHash := "genesis"
	for _, s := range b.SettlementLedger {
		if s.PrevLedgerHash != prevHash {
			return fmt.Errorf("%w: settlement ledger chain broken at %s", ErrForkBundleCorrupted, s.PaymentID)
		}
		if err := VerifySettlementReceipt(domain, s); err != nil {
			return fmt.Errorf("%w: settlement %s: %v", ErrForkBundleCorrupted, s.PaymentID, err)
		}
		prevHash = s.LedgerHash
	}
	for _, g := range b.Governance {
		seenVoters := make(map[string]struct{}, len(g.Votes))
		for _, v := range g.Votes {
			if err := VerifyGovernanceVote(domain, v); err != nil {
				return fmt.Errorf("%w: governance vote %s: %v", ErrForkBundleCorrupted, g.ProposalID, err)
			}
			if v.ProposalID != g.ProposalID || v.Action != g.Action || v.Payload != g.Payload {
				return fmt.Errorf("%w: governance vote %s conflicts with proposal contents", ErrForkBundleCorrupted, g.ProposalID)
			}
			normVoter, err := NormalizeAddress(v.VoterAddress)
			if err != nil {
				return fmt.Errorf("%w: governance voter address %s: %v", ErrForkBundleCorrupted, v.VoterAddress, err)
			}
			if _, dup := seenVoters[normVoter]; dup {
				return fmt.Errorf("%w: duplicate governance voter %s on %s", ErrForkBundleCorrupted, normVoter, g.ProposalID)
			}
			seenVoters[normVoter] = struct{}{}
		}
	}
	seenSnapshots := make(map[string]string, len(b.EncryptedVault))
	for _, env := range b.EncryptedVault {
		if err := VerifyEncryptedEnvelope(domain, env); err != nil {
			return fmt.Errorf("%w: vault envelope %s: %v", ErrForkBundleCorrupted, env.SnapshotID, err)
		}
		if prevOwner, exists := seenSnapshots[env.SnapshotID]; exists && !SameAddress(prevOwner, env.OwnerAddress) {
			return fmt.Errorf("%w: vault snapshot %s has conflicting owners", ErrForkBundleCorrupted, env.SnapshotID)
		}
		seenSnapshots[env.SnapshotID] = env.OwnerAddress
	}
	return nil
}

// --- Deterministic Verifiable Inference Kernel ---

var deterministicVocab = []string{
	"sovereign", "mesh", "verified", "quorum", "peer", "digest", "route", "settle",
	"compute", "mirror", "cipher", "epoch", "state", "receipt", "ledger", "proof",
	"open", "portable", "fork", "signal", "node", "block", "token", "stream",
	"zero", "trust", "anchor", "relay", "guard", "vault", "valid", "exit",
}

// ComputeRequestInputDigest computes the canonical SHA-256 digest of an
// InferenceRequest's deterministic execution inputs.
func ComputeRequestInputDigest(req InferenceRequest) string {
	var seedBuf [8]byte
	binary.BigEndian.PutUint64(seedBuf[:], req.Seed)
	var tokBuf [4]byte
	binary.BigEndian.PutUint32(tokBuf[:], uint32(req.MaxTokens))
	return SHA256Hex(
		[]byte("hip-1351:infer-input:v1:"),
		[]byte(req.ModelID),
		[]byte(strings.ToLower(req.ModelDigestSHA256)),
		[]byte(req.Prompt),
		seedBuf[:],
		tokBuf[:],
	)
}

// ExecuteDeterministicInference runs a deterministic, independently recomputable
// SHA-256 keyed token generation kernel bound to the exact model artifact bytes,
// prompt, and seed. Any byte change in the model artifact or prompt changes the
// resulting output digest with cryptographic probability.
func ExecuteDeterministicInference(modelBlob []byte, prompt string, seed uint64, maxTokens int) (string, string, int) {
	if maxTokens <= 0 {
		maxTokens = 32
	}
	if maxTokens > 1024 {
		maxTokens = 1024
	}
	modelDigest := sha256.Sum256(modelBlob)
	var seedBuf [8]byte
	binary.BigEndian.PutUint64(seedBuf[:], seed)

	state := sha256.New()
	_, _ = state.Write([]byte("hip-1351:deterministic-kernel:v1:"))
	_, _ = state.Write(modelDigest[:])
	_, _ = state.Write([]byte(prompt))
	_, _ = state.Write(seedBuf[:])
	cur := state.Sum(nil)

	tokens := make([]string, maxTokens)
	for i := 0; i < maxTokens; i++ {
		var idxBuf [4]byte
		binary.BigEndian.PutUint32(idxBuf[:], uint32(i))
		h := sha256.New()
		_, _ = h.Write(cur)
		_, _ = h.Write(idxBuf[:])
		cur = h.Sum(nil)
		wordIdx := int(binary.BigEndian.Uint32(cur[:4])) % len(deterministicVocab)
		tokens[i] = fmt.Sprintf("%s#%02x", deterministicVocab[wordIdx], cur[4])
	}
	out := strings.Join(tokens, " ")
	return out, SHA256Hex([]byte(out)), maxTokens
}
