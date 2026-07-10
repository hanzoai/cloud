//go:build controlplane

package controlplane

// cert.go — seam (c) crypto core: the REAL control-plane finality certificate.
//
// This is the independent-signature weighted-quorum certificate the design
// chose over the blocked threshold-Pulsar path (control-plane-increment-2.md
// §2c). Each pod signs the canonical quorum message INDEPENDENTLY with its own
// ML-DSA-65 identity key (seam a) under a DISTINCT cert context; the cert is a
// quasar.ConsensusCert carrying one EvidenceWeightedSigSet leg (a
// WeightedQuorumCert of N independent FIPS-204 signatures + a weighted-Merkle
// quorum). Verification is quasar.VerifyConsensusCert under a control-plane
// policy — the shipped, audited Gen-3 verifier. No DKG, no threshold aggregate,
// no unshipped luxfi/pulsar core: soundness rests only on stock FIPS-204 verify
// + the weighted-validator-set Merkle commitment.
//
// This file is self-contained crypto (independent of the ceremony wiring): it
// composes and verifies a cert from a position + the validator key set + a set
// of collected signatures. cert_test.go exercises it directly.

import (
	"errors"
	"fmt"
	"sort"

	"github.com/luxfi/consensus/config"
	"github.com/luxfi/consensus/protocol/quasar"
	"github.com/luxfi/crypto/mldsa"
	pulsar "github.com/luxfi/pulsar/pkg/pulsar"
)

const (
	// controlPlaneCertVersion pins the quasar.ConsensusCert envelope version the
	// control plane emits. Wire-stable; a quasar bump surfaces loudly as
	// ErrConsensusCertVersion (the value is bound into the domain message).
	controlPlaneCertVersion uint16 = 1

	// controlPlanePolicyID is the single control-plane cert policy the store
	// resolves. One posture, one policy — decomplected from the cert bytes.
	controlPlanePolicyID uint32 = 1

	// controlPlaneQCType names the certificate role (finality) bound into the
	// signed quorum message so a signature for one role cannot be replayed as
	// another.
	controlPlaneQCType uint8 = 0x03

	// mldsa65ParamByte is the ML-DSA-65 parameter-set wire byte
	// (config.SigSchemeID / QuorumSchemeMLDSA65). Bound into every validator
	// leaf and signer record.
	mldsa65ParamByte uint8 = 0x42
)

// certContext is the FIPS 204 §5.2 domain-separation context every control-plane
// CERT signature is produced and verified under. It is DELIBERATELY DISTINCT from
// popContext (RED finding R4): reusing the proof-of-possession context for cert
// signing would allow a PoP signature to be cross-protocol-confused with a cert
// signature. It is also the QuorumVerifierConfig.Context the weighted-sig-set
// verifier checks every ML-DSA record under (contextForScheme → cfg.Context), so
// signer and verifier are pinned to the same context by construction.
var certContext = []byte("hanzo/controlplane/cert/v1")

var (
	// ErrNoValidators rejects composing/verifying against an empty key set.
	ErrNoValidators = errors.New("controlplane: empty validator key set")
	// ErrSignerNotInSet rejects a collected signature from a node absent from
	// the validator key set (a rogue signer).
	ErrSignerNotInSet = errors.New("controlplane: signature from a node not in the validator set")
	// ErrInsufficientCertSigners rejects composing a cert below the quorum weight.
	ErrInsufficientCertSigners = errors.New("controlplane: collected signatures below quorum weight")
	// ErrCertPositionMismatch rejects a cert whose self-described position does
	// not match the block the caller expects it to certify. VerifyConsensusCert
	// pins the validator set + policy but NOT the caller's height/round/block, so
	// the caller MUST bind the cert to its expected position — otherwise a valid
	// cert for a DIFFERENT block/height/round would be accepted here.
	ErrCertPositionMismatch = errors.New("controlplane: cert position does not match the expected block")
	// ErrUnsafeCertFloor is the two-threshold self-defence (RED finding 1): the
	// cert quorum floor MUST be the byzantine-safe BFT quorum for the validator-
	// set size, NEVER a wallet-custody t = floor(n/2)+1. For n=5 that t is 3, and
	// 2*3 is not > N+f = 6, so a 3-of-5 cert could finalize two conflicting
	// blocks. The core enforces whatever floor it is handed AND refuses a
	// structurally unsafe one — so a mis-wired driver cannot get an unsafe cert
	// verified even if it passes the wrong threshold.
	ErrUnsafeCertFloor = errors.New("controlplane: cert quorum floor is not the byzantine-safe BFT quorum for the validator set (a wallet-custody t must never be a cert floor)")
)

// guardBFTFloor fails closed unless quorumWeight is a byzantine-safe cert floor
// for a set of n validators: at least the canonical BFT quorum (2n/3+1), no more
// than n, and a (n, quorum, f) triple that satisfies 2q > n+f. This is the
// self-defence RED finding 1 mandates — it is derived from len(keys), so a
// future driver that mis-wires the wallet-custody t (=3 for n=5) instead of the
// BFT quorum (=4) cannot get a cert composed OR verified.
func guardBFTFloor(n int, quorumWeight uint64) error {
	if n <= 0 {
		return ErrNoValidators
	}
	floor := bftQuorum(n)
	if quorumWeight < uint64(floor) {
		return fmt.Errorf("%w: quorum %d < BFT floor %d for N=%d", ErrUnsafeCertFloor, quorumWeight, floor, n)
	}
	if quorumWeight > uint64(n) {
		return fmt.Errorf("%w: quorum %d exceeds validator count %d", ErrUnsafeCertFloor, quorumWeight, n)
	}
	if err := checkQuorumSafety(n, int(quorumWeight), bftFaultTolerance(n)); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeCertFloor, err)
	}
	return nil
}

// certPosition is the consensus position a control-plane cert finalizes. It is
// the minimal, ceremony-independent input to compose/verify a cert, so this
// crypto core is testable in isolation.
type certPosition struct {
	NetworkID  uint32
	ChainID    uint32
	Epoch      uint64
	Height     uint64
	Round      uint32
	BlockHash  [32]byte // the block / value being finalized (inner ValueHash + envelope BlockHash)
	ParentRoot [32]byte // chain-extension anchor bound into the round digest
}

// validatorKey pairs a pod's node id with its ML-DSA-65 identity public key —
// the (id, key) the weighted-validator-set leaf commits to. Each pod carries
// unit voting weight; the quorum floor is a signer COUNT.
type validatorKey struct {
	Node pulsar.NodeID
	Pub  *mldsa.PublicKey
}

// sortedValidatorKeys returns the key set sorted strictly by node id (the
// canonical leaf order BuildWeightedValidatorSet imposes). Deterministic across
// all pods so every voter builds the byte-identical validator set.
func sortedValidatorKeys(keys map[pulsar.NodeID]*mldsa.PublicKey) []validatorKey {
	out := make([]validatorKey, 0, len(keys))
	for n, k := range keys {
		out = append(out, validatorKey{Node: n, Pub: k})
	}
	sort.Slice(out, func(i, j int) bool {
		return bytesLessNode(out[i].Node, out[j].Node)
	})
	return out
}

func bytesLessNode(a, b pulsar.NodeID) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// buildValidatorSet builds the weighted-validator-set commitment (the shipped
// quasar.WeightedValidatorSet) from the pod key set. Unit weights; ML-DSA-65
// parameter byte; key version 0 (rotation is seam-b's concern). Deterministic.
func buildValidatorSet(epoch uint64, keys map[pulsar.NodeID]*mldsa.PublicKey) (*quasar.WeightedValidatorSet, error) {
	if len(keys) == 0 {
		return nil, ErrNoValidators
	}
	sorted := sortedValidatorKeys(keys)
	leaves := make([]quasar.WeightedValidatorLeaf, 0, len(sorted))
	for _, vk := range sorted {
		leaves = append(leaves, quasar.WeightedValidatorLeaf{
			ValidatorID:    vk.Node,
			PublicKey:      vk.Pub.Bytes(),
			VotingWeight:   1,
			ParameterSetID: mldsa65ParamByte,
			KeyVersion:     0,
		})
	}
	return quasar.BuildWeightedValidatorSet(epoch, leaves)
}

// certEnvelope builds the round-digest envelope the quorum message is derived
// from. The posture axes come from the canonical StrictPQ profile, but the
// proof backend/format/verifier are pinned to the DIRECT weighted-quorum trust
// model (a cert produced under this backend cannot be re-presented under a STARK
// backend's envelope). The committee/group-key/signer roots are bound non-zero
// (ComputeRoundDigest refuses zero security-relevant inputs), deterministically
// derived from the set root so compose and verify agree byte-for-byte.
func certEnvelope(pos certPosition, vsetRoot [48]byte, quorumWeight uint64) quasar.QuorumMessageEnvelope {
	p := config.StrictPQProfile
	committee := root48("cp-cert-committee", vsetRoot[:])
	groupKey := root48("cp-cert-groupkey", vsetRoot[:])
	signerSet := root48("cp-cert-signerset", vsetRoot[:])
	return quasar.QuorumMessageEnvelope{
		ProfileID:       p.ProfileID,
		HashSuite:       p.HashSuiteID,
		IdentityScheme:  config.IdentitySchemeID(p.IdentitySchemeID),
		FinalityScheme:  p.FinalitySchemeID,
		ProofPolicy:     p.ProofPolicyID,
		ProofBackend:    config.ProofBackendDirectWeightedQuorum,
		ProofFormat:     config.ProofFormatDirectWeightedQuorumV1,
		VerifierID:      config.VerifierDirectWeightedQuorumPQ,
		EffectivePolicy: byte(p.ProfileID),
		NetworkID:       pos.NetworkID,

		ChainID:          pos.ChainID,
		Epoch:            pos.Epoch,
		Height:           pos.Height,
		Round:            pos.Round,
		ValueHash:        pos.BlockHash,
		QCType:           controlPlaneQCType,
		ValidatorSetRoot: vsetRoot,
		QuorumThreshold:  quorumWeight,

		ParentQBlockHash:   pos.ParentRoot,
		CommitteeRoot:      committee,
		GroupPublicKeyHash: groupKey,
		SignerSetCommit:    signerSet,
	}
}

// CertSigningMessage is the canonical quorum message every pod signs for this
// position + validator set + quorum. All pods derive it identically (the set
// root is a deterministic commitment to every pod's registered key), so each
// signs the SAME bytes independently. This is the value passed to
// mldsa PrivateKey.SignCtxDeterministic(msg, certContext).
func CertSigningMessage(pos certPosition, keys map[pulsar.NodeID]*mldsa.PublicKey, quorumWeight uint64) ([]byte, error) {
	vset, err := buildValidatorSet(pos.Epoch, keys)
	if err != nil {
		return nil, err
	}
	return quasar.QuorumConsensusMessage(certEnvelope(pos, vset.Root(), quorumWeight))
}

// signRecords assembles the per-signer QuorumSignerRecords for the collected
// signatures, attaching each signer's weighted-Merkle inclusion proof against
// the set root. A signature from a node absent from the set is a rogue signer
// (ErrSignerNotInSet) — refused at assembly so it can never reach the cert.
func signRecords(vset *quasar.WeightedValidatorSet, keys map[pulsar.NodeID]*mldsa.PublicKey, sigs map[pulsar.NodeID][]byte) ([]quasar.QuorumSignerRecord, error) {
	leaves := vset.Leaves() // canonical sorted order
	indexOf := make(map[pulsar.NodeID]int, len(leaves))
	for i := range leaves {
		var id pulsar.NodeID
		copy(id[:], leaves[i].ValidatorID[:])
		indexOf[id] = i
	}
	records := make([]quasar.QuorumSignerRecord, 0, len(sigs))
	for node, sig := range sigs {
		pub, ok := keys[node]
		if !ok {
			return nil, fmt.Errorf("%w: %x", ErrSignerNotInSet, node[:8])
		}
		idx, ok := indexOf[node]
		if !ok {
			return nil, fmt.Errorf("%w: %x", ErrSignerNotInSet, node[:8])
		}
		proof, err := vset.InclusionProof(idx)
		if err != nil {
			return nil, err
		}
		records = append(records, quasar.QuorumSignerRecord{
			ValidatorID:  node,
			PublicKey:    pub.Bytes(),
			VotingWeight: 1,
			Scheme:       quasar.QuorumSchemeMLDSA65,
			ParamSetID:   mldsa65ParamByte,
			KeyVersion:   0,
			MerklePath:   proof,
			Signature:    append([]byte(nil), sig...),
		})
	}
	return records, nil
}

// ComposeControlPlaneCert builds the REAL control-plane finality certificate: a
// quasar.ConsensusCert carrying one EvidenceWeightedSigSet leg over the collected
// independent ML-DSA-65 signatures. Permissionless and deterministic — no
// secrets, no randomness. Every honest voter holding the same (position, keys,
// signature set) composes the byte-identical cert.
func ComposeControlPlaneCert(pos certPosition, keys map[pulsar.NodeID]*mldsa.PublicKey, sigs map[pulsar.NodeID][]byte, quorumWeight uint64) (*quasar.ConsensusCert, error) {
	// Two-threshold self-defence (RED finding 1): refuse an unsafe floor derived
	// from the validator-set size, so a mis-wired wallet-custody t can never
	// compose a cert.
	if err := guardBFTFloor(len(keys), quorumWeight); err != nil {
		return nil, err
	}
	if uint64(len(sigs)) < quorumWeight {
		return nil, fmt.Errorf("%w: have %d need %d", ErrInsufficientCertSigners, len(sigs), quorumWeight)
	}
	vset, err := buildValidatorSet(pos.Epoch, keys)
	if err != nil {
		return nil, err
	}
	records, err := signRecords(vset, keys, sigs)
	if err != nil {
		return nil, err
	}
	wqc, err := quasar.BuildWeightedQuorumCert(quasar.QuorumCertParams{
		ChainID:          pos.ChainID,
		Epoch:            pos.Epoch,
		Height:           pos.Height,
		Round:            pos.Round,
		ValueHash:        pos.BlockHash,
		QCType:           controlPlaneQCType,
		ValidatorSetRoot: vset.Root(),
		QuorumThreshold:  quorumWeight,
	}, records)
	if err != nil {
		return nil, err
	}
	wqcBytes, err := wqc.MarshalBinary()
	if err != nil {
		return nil, err
	}
	p := config.StrictPQProfile
	cert := &quasar.ConsensusCert{
		Version:          controlPlaneCertVersion,
		Profile:          byte(p.ProfileID),
		ChainID:          pos.ChainID,
		Epoch:            pos.Epoch,
		Height:           pos.Height,
		Round:            pos.Round,
		BlockHash:        pos.BlockHash,
		ValidatorSetRoot: vset.Root(),
		PolicyID:         controlPlanePolicyID,
		RequiredLegsRoot: quasar.HashRequiredLegs(controlPlanePolicy{quorumWeight}.RequiredLegs()),
		AggregateWeight:  wqc.AggregateWeight,
		Evidence: []quasar.LegEvidence{{
			Leg:     quasar.LegSpec{Kind: quasar.LegPulsarMLDSA, ParamSetID: mldsa65ParamByte},
			Mode:    quasar.EvidenceWeightedSigSet,
			Payload: wqcBytes,
		}},
	}
	return cert, nil
}

// VerifyControlPlaneCert verifies a cert with the shipped, audited Gen-3
// verifier quasar.VerifyConsensusCert under the control-plane policy + the
// verifier-pinned validator set. NEVER the structural QuasarCert.Verify. Returns
// the verifier's typed error verbatim on failure.
func VerifyControlPlaneCert(pos certPosition, keys map[pulsar.NodeID]*mldsa.PublicKey, quorumWeight uint64, cert *quasar.ConsensusCert) error {
	if cert == nil {
		return quasar.ErrConsensusCertNil
	}
	// Bind the cert to the EXPECTED position (the caller's block) BEFORE the
	// cryptographic verify. A cert self-describes its position; without this a
	// perfectly valid cert for a different block/height/round would verify here.
	if cert.ChainID != pos.ChainID || cert.Epoch != pos.Epoch ||
		cert.Height != pos.Height || cert.Round != pos.Round ||
		cert.BlockHash != pos.BlockHash {
		return ErrCertPositionMismatch
	}
	// Two-threshold self-defence (RED finding 1): a mis-wired wallet-custody t
	// cannot verify even if a driver passes it here — the floor is re-derived
	// from the validator-set size and must be byzantine-safe.
	if err := guardBFTFloor(len(keys), quorumWeight); err != nil {
		return err
	}
	vset, err := buildValidatorSet(pos.Epoch, keys)
	if err != nil {
		return err
	}
	store := controlPlaneStore{policy: controlPlanePolicy{quorumWeight}}
	vs := &controlPlaneValidatorSet{set: vset, pos: pos, quorumWeight: quorumWeight}
	return quasar.VerifyConsensusCert(store, vs, cert)
}

// ----------------------------------------------------------------------------
// Policy + validator-set interface implementations (the decomplected inputs to
// quasar.VerifyConsensusCert). The control plane is its OWN policy domain: a
// small fixed committee whose only required leg is the independent-sig
// weighted-sig-set PQ leg. No threshold-sig, no classical, no STARK.
// ----------------------------------------------------------------------------

// controlPlanePolicy is the control-plane cert posture: exactly one required
// leg — LegPulsarMLDSA proven by EvidenceWeightedSigSet — at the BFT quorum
// weight floor. Classical and threshold-sig are forbidden.
type controlPlanePolicy struct {
	quorumWeight uint64
}

func (controlPlanePolicy) RequiredLegs() []quasar.LegSpec {
	return []quasar.LegSpec{{Kind: quasar.LegPulsarMLDSA, ParamSetID: mldsa65ParamByte}}
}

func (controlPlanePolicy) Allows(leg quasar.LegSpec, mode quasar.EvidenceMode, paramSet uint8) bool {
	return leg.Kind == quasar.LegPulsarMLDSA &&
		mode == quasar.EvidenceWeightedSigSet &&
		paramSet == mldsa65ParamByte
}

func (p controlPlanePolicy) ThresholdWeight() uint64 { return p.quorumWeight }

func (controlPlanePolicy) AllowsClassicalScheme(quasar.ClassicalScheme) bool { return false }

// controlPlaneStore resolves the single control-plane policy. The verifier loads
// policy from here, never from the cert (invariant I1).
type controlPlaneStore struct {
	policy controlPlanePolicy
}

func (s controlPlaneStore) Policy(_ uint32, _ uint64, policyID uint32) (quasar.ConsensusCertPolicy, error) {
	if policyID != controlPlanePolicyID {
		return nil, fmt.Errorf("controlplane: unknown policy id %d", policyID)
	}
	return s.policy, nil
}

// controlPlaneValidatorSet is the committed epoch validator set the verifier
// pins the cert against. It wraps the weighted set and supplies the weighted-
// sig-set verify axes (allowed schemes, the DISTINCT cert FIPS context, and the
// Direct-weighted-quorum envelope). No threshold group keys, no classical keys.
type controlPlaneValidatorSet struct {
	set          *quasar.WeightedValidatorSet
	pos          certPosition
	quorumWeight uint64
}

func (v *controlPlaneValidatorSet) Root() [48]byte { return v.set.Root() }
func (v *controlPlaneValidatorSet) Epoch() uint64  { return v.set.Epoch() }

func (v *controlPlaneValidatorSet) WeightedConfig() quasar.QuorumVerifierConfig {
	return quasar.QuorumVerifierConfig{
		AllowedSchemes: map[quasar.QuorumSchemeID]bool{quasar.QuorumSchemeMLDSA65: true},
		Context:        certContext,
		MinThreshold:   v.quorumWeight,
	}
}

func (v *controlPlaneValidatorSet) WeightedEnvelope() quasar.QuorumMessageEnvelope {
	return certEnvelope(v.pos, v.set.Root(), v.quorumWeight)
}

func (v *controlPlaneValidatorSet) ThresholdGroupKey(quasar.LegKind) (quasar.ThresholdGroupKey, bool) {
	return quasar.ThresholdGroupKey{}, false
}

func (v *controlPlaneValidatorSet) ClassicalAggregateKey(quasar.ClassicalScheme) ([]byte, bool) {
	return nil, false
}
