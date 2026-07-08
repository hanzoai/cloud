//go:build controlplane

package controlplane

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"github.com/luxfi/consensus/config"
	"github.com/luxfi/consensus/protocol/quasar"
	qpulsar "github.com/luxfi/consensus/protocol/quasar/pulsar"
	pulsarlib "github.com/luxfi/pulsar/pkg/pulsar"
)

// RoundContext binds one ceremony round to TWO real, published bindings:
//
//   - quasar.RoundDigest (via quasar.ComputeRoundDigest) — the block-level
//     subject that binds every consensus envelope axis, including the placement
//     block's payload root. Two blocks differing in any op differ here.
//   - qpulsar.PulsarSession — the threshold-sign session the real Pulsar
//     RoundSigner binds every leg to. Its 32-byte SessionID is the domain-
//     separated hash of chain/epoch/height/round/block/validator-set/joint-key/
//     DKG/committee/signer-set/nonce-pool. Every signer recomputes it and
//     refuses a mismatched session.
//
// Both make equivocation and cross-round/cross-policy replay structurally
// impossible rather than merely checked.
type RoundContext struct {
	Profile   *config.ChainSecurityProfile
	NetworkID uint32
	ChainID   uint32
	Epoch     uint64

	Height uint64
	Round  uint32

	Digest quasar.RoundDigest // block-level binding (real ComputeRoundDigest)

	Session   qpulsar.PulsarSession // consensus-round binding (real)
	SessionID [32]byte              // == Session.SessionID()
	NonceID   [32]byte              // canonical shared nonce id (from the pool)
	Nonce     pulsarlib.NonceCert   // canonical shared nonce cert (from the pool)

	Voters []string // canonical (sorted) voter node-name set bound into the digest
}

var errEmptyNoncePool = errors.New("controlplane: nonce pool empty (background NonceMPC produced no certs)")

// root48 hashes parts into a 48-byte root (SHA-384) with a domain tag.
func root48(tag string, parts ...[]byte) [48]byte {
	h := sha512.New384()
	h.Write([]byte(tag))
	for _, p := range parts {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(p)))
		h.Write(l[:])
		h.Write(p)
	}
	var out [48]byte
	copy(out[:], h.Sum(nil))
	return out
}

// hash32 hashes parts into a 32-byte root (SHA-256) with a domain tag.
func hash32(tag string, parts ...[]byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(tag))
	for _, p := range parts {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(p)))
		h.Write(l[:])
		h.Write(p)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// voterSetBytes is the canonical encoding of a voter set (sorted, length-
// prefixed) — the preimage of validator-set / committee / signer-set roots.
func voterSetBytes(voters []string) []byte {
	cp := append([]string(nil), voters...)
	sort.Strings(cp)
	var b []byte
	for _, v := range cp {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(v)))
		b = append(b, l[:]...)
		b = append(b, v...)
	}
	return b
}

// NewRoundContext computes the block-level digest AND the consensus-round
// session for a candidate block under a voter set + shared nonce pool. It
// returns an error if ComputeRoundDigest refuses a zero security field, if the
// session fails to validate, or if the nonce pool is empty.
func NewRoundContext(cfg RoundConfig, b *PlacementBlock, voters []string, pool qpulsar.NonceCertPool) (RoundContext, error) {
	profile := cfg.Profile
	payloadRoot := b.PayloadRoot()

	vsRoot := root48("cp-validator-set", voterSetBytes(voters))
	committeeRoot48 := vsRoot
	signerCommit := root48("cp-signer-set", voterSetBytes(voters))
	groupKeyHash := root48("cp-group-key", []byte(profile.ProfileName), voterSetBytes(voters))

	var backend config.ProofBackendID
	if len(profile.AllowedProofBackends) > 0 {
		backend = profile.AllowedProofBackends[0]
	}
	var format config.ProofFormatID
	if len(profile.AllowedProofFormats) > 0 {
		format = profile.AllowedProofFormats[0]
	}

	digest, err := quasar.ComputeRoundDigest(
		profile.ProfileID,
		profile.HashSuiteID,
		// IdentitySchemeID and the profile's SigSchemeID field share the 0x4x
		// ML-DSA encoding by design; convert to stay in lockstep with the
		// profile rather than hardcode a scheme byte.
		config.IdentitySchemeID(profile.IdentitySchemeID),
		profile.FinalitySchemeID,
		profile.ProofPolicyID,
		backend,
		format,
		cfg.VerifierID,
		byte(profile.ProfileID), // effective-policy byte (non-zero for strict-PQ)
		cfg.NetworkID,
		cfg.ChainID,
		cfg.Epoch,
		b.Height,
		b.Round,
		b.ParentRoot,
		payloadRoot,
		[48]byte{}, // da_root — not modeled in increment 1
		[48]byte{}, // source_state_root — not modeled
		[48]byte{}, // zchain_state_root — not modeled
		vsRoot,
		committeeRoot48,
		[48]byte{}, // dkg_transcript_root (digest side) — not modeled
		groupKeyHash,
		signerCommit,
	)
	if err != nil {
		return RoundContext{}, err
	}

	// Real consensus-round session binding. Every root is non-zero so
	// Session.Validate passes; each binds an independent replay axis.
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], cfg.ChainID)
	blockHash := hash32("cp-blockhash", payloadRoot[:])
	session := qpulsar.PulsarSession{
		ChainID:           hash32("cp-session-chain", u32[:]),
		Epoch:             cfg.Epoch,
		Height:            b.Height,
		Round:             uint64(b.Round),
		BlockHash:         blockHash,
		ValidatorSetRoot:  hash32("cp-session-vset", voterSetBytes(voters)),
		JointPKID:         cfg.JointPKID,
		DKGTranscriptRoot: cfg.DKGTranscriptRoot,
		CommitteeID:       hash32("cp-session-committee", voterSetBytes(voters)),
		SignerSetRoot:     hash32("cp-session-signerset", voterSetBytes(voters)),
		NoncePoolRoot:     pool.Root(),
		ProtocolVersion:   1,
	}
	if err := session.Validate(); err != nil {
		return RoundContext{}, err
	}
	sessionID := session.SessionID()

	if pool.Size() == 0 {
		return RoundContext{}, errEmptyNoncePool
	}
	idx := pulsarlib.CanonicalNonceIndex(sessionID, pool.Root(), pool.Size())
	nonce, ok := pool.At(idx)
	if !ok {
		return RoundContext{}, errEmptyNoncePool
	}

	cp := append([]string(nil), voters...)
	sort.Strings(cp)
	return RoundContext{
		Profile:   profile,
		NetworkID: cfg.NetworkID,
		ChainID:   cfg.ChainID,
		Epoch:     cfg.Epoch,
		Height:    b.Height,
		Round:     b.Round,
		Digest:    digest,
		Session:   session,
		SessionID: sessionID,
		NonceID:   nonce.NonceID,
		Nonce:     nonce,
		Voters:    cp,
	}, nil
}

// RoundConfig is the static ceremony configuration shared across rounds.
type RoundConfig struct {
	Profile           *config.ChainSecurityProfile
	VerifierID        config.VerifierID
	NetworkID         uint32
	ChainID           uint32
	Epoch             uint64
	JointPKID         [32]byte // committee joint ML-DSA key id (harness DKG constant)
	DKGTranscriptRoot [32]byte // DKG transcript commitment (harness DKG constant)
}

// DefaultRoundConfig is the canonical strict-PQ ceremony configuration: the
// published StrictPQProfile (IsPQ ⇒ triple-mode cert demanded) under the
// STARK-FRI-SHA3 PQ verifier, on the local control-plane network/chain ids,
// with non-zero harness DKG constants so the session validates.
func DefaultRoundConfig() RoundConfig {
	p := config.StrictPQProfile
	return RoundConfig{
		Profile:           &p,
		VerifierID:        config.VerifierLuxSTARKFRISHA3PQ,
		NetworkID:         0x48414e5a, // "HANZ" — non-zero control-plane network id
		ChainID:           0x43504c4e, // "CPLN" — non-zero control-plane chain id
		Epoch:             1,
		JointPKID:         hash32("cp-joint-pk", []byte("hanzo-control-plane")),
		DKGTranscriptRoot: hash32("cp-dkg-transcript", []byte("hanzo-control-plane")),
	}
}

// ----------------------------------------------------------------------------
// BFT quorum arithmetic.
// ----------------------------------------------------------------------------

// bftQuorum is the >2/3 supermajority: floor(2N/3)+1. For N=7 ⇒ 5, tolerating
// f=2 byzantine or crashed voters. Two quorums always intersect in ≥ f+1
// voters, so two conflicting digests at one height cannot both finalize.
func bftQuorum(n int) int { return 2*n/3 + 1 }

// bftFaultTolerance is floor((N-1)/3): the max byzantine/crash faults a set of
// N voters tolerates while preserving safety and liveness.
func bftFaultTolerance(n int) int { return (n - 1) / 3 }

// ErrUnsafeQuorum indicates a (N, quorum, f) triple that does not satisfy the
// byzantine safety+liveness bounds; construction fails closed rather than run a
// cluster that could fork or deadlock.
var ErrUnsafeQuorum = errors.New("controlplane: unsafe quorum parameters")

// checkQuorumSafety verifies the byzantine bounds for (n, quorum, f):
//
//	N ≥ 3f+1        — safety+liveness under f byzantine faults
//	quorum ≥ 2f+1   — every quorum contains a correct majority
//	2·quorum > N+f  — any two quorums intersect in ≥ f+1 nodes, so ≥1 correct
//	                  node is common: two conflicting digests cannot both finalize
//
// The 2q>N+f intersection margin at N=7 is exactly 1 (10 > 9), and that margin
// is the whole basis of the no-fork property (red_exploits #1). This guard makes
// any future change to the quorum rule or f fail closed instead of silently
// unsafe.
func checkQuorumSafety(n, quorum, f int) error {
	if f < 0 || n < 3*f+1 {
		return fmt.Errorf("%w: N=%d < 3f+1 for f=%d", ErrUnsafeQuorum, n, f)
	}
	if quorum < 2*f+1 {
		return fmt.Errorf("%w: quorum=%d < 2f+1 for f=%d", ErrUnsafeQuorum, quorum, f)
	}
	if 2*quorum <= n+f {
		return fmt.Errorf("%w: 2*quorum=%d <= N+f=%d (quorums need not intersect in a correct node)", ErrUnsafeQuorum, 2*quorum, n+f)
	}
	return nil
}
