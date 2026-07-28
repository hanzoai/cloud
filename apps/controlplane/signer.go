//go:build controlplane

package controlplane

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"math/bits"
	"time"

	"github.com/luxfi/consensus/protocol/quasar"
	qpulsar "github.com/luxfi/consensus/protocol/quasar/pulsar"
	pulsarlib "github.com/luxfi/pulsar/pkg/pulsar"
)

// ML-DSA-65 (ModeP65) shape: the z-vector is L·N coefficients at 4 bytes each.
const (
	mldsa65L    = 5
	mldsa65N    = 256
	zshareBytes = mldsa65L * mldsa65N * 4 // 5120
)

func init() {
	// mustHarnessOnly fires FIRST, at package-import time — before the line
	// below ever runs. Merely importing this package registers a forgeable
	// global PartialZVerifier into luxfi/pulsar's process-wide registry; that
	// must be impossible outside a go-test harness, so the guard sits ahead of
	// the registration itself, not just ahead of Cluster/Signer construction.
	mustHarnessOnly("package import (RegisterPartialZVerifier)")
	// The REAL PulsarRoundSigner.Round2 calls pulsarlib.VerifyZPartial, which
	// delegates the zero-knowledge correctness check to a registered
	// PartialZVerifier and FAILS CLOSED if none is set. The sound lattice ZK
	// verifier is not yet shipped by luxfi/pulsar (documented-unshipped crypto
	// core), so — exactly as the published package's own tests do — we register
	// an accept-bindings verifier. The PUBLIC bindings (party/session/nonce/z)
	// are still enforced unconditionally by VerifyZPartial; only the ZK residual
	// check is stubbed. STUB BOUNDARY: swap for the real verifier in increment 2.
	pulsarlib.RegisterPartialZVerifier(acceptBindingsOnly{})
}

// acceptBindingsOnly is the increment-1 stub PartialZVerifier (unshipped crypto
// core). It accepts the ZK residual while VerifyZPartial still enforces the
// public bindings.
type acceptBindingsOnly struct{}

func (acceptBindingsOnly) VerifyPartial(*pulsarlib.Partial, []byte, []byte, []byte) error {
	return nil
}

// stubSignatureCore is the increment-1 stub qpulsar.SignatureCore (the FIPS-204
// ML-DSA assembly is a documented-unshipped luxfi/pulsar crypto core). It emits
// a deterministic, non-empty signature bound to the session and the aggregated
// z-sum + signer bitmap, so the Pulsar leg varies by round (replay resistance)
// and is byte-identical across honest voters. STUB BOUNDARY: swap for the real
// Boundary-Cleared/Carry-Elimination signer in increment 2.
type stubSignatureCore struct{}

func (stubSignatureCore) AssembleSignature(
	session qpulsar.PulsarSession,
	_ pulsarlib.NonceCert,
	agg pulsarlib.Aggregate,
) (pulsarlib.Signature, error) {
	sid := session.SessionID()
	h := sha256.New()
	h.Write([]byte("cp-pulsar-core"))
	h.Write(sid[:])
	h.Write(agg.ZSum)
	h.Write(agg.SignerBitmap)
	return pulsarlib.Signature{Mode: pulsarlib.ModeP65, Bytes: h.Sum(nil)}, nil
}

// Signer is one voter's Pulsar cert-profile signer. It wraps the REAL
// quasar/pulsar.PulsarRoundSigner — which owns the load-bearing round
// orchestration (canonical non-grindable nonce selection, canonical signer-set
// selection, z aggregation, ConsensusCert accountability) — and adds the
// orchestration-layer concerns the pulsar primitive leaves to consensus: the
// z-share commitment for the anti-rush barrier and the per-leg identity
// proof-of-possession.
//
// STUB BOUNDARIES (increment 2): the shared nonce Pool models the background
// NonceMPC (in-memory here); the z-share is a deterministic derivation of the
// sealed share (the real z-share is s_i-dependent from a DKG); the two crypto
// cores registered above (SignatureCore, PartialZVerifier) are unshipped.
type Signer struct {
	share     Share
	pool      qpulsar.NonceCertPool
	threshold int
	vsetSize  int
}

// NewSigner builds a voter's signer over the shared background nonce pool.
// Fail-closed: refuses outside a go-test harness while the crypto is stub
// (mustHarnessOnly) — see containment.go.
func NewSigner(share Share, pool qpulsar.NonceCertPool, threshold, vsetSize int) *Signer {
	mustHarnessOnly("NewSigner")
	return &Signer{share: share, pool: pool, threshold: threshold, vsetSize: vsetSize}
}

// Party is this voter's threshold party index.
func (s *Signer) Party() uint32 { return s.share.Index }

// roundSigner constructs the REAL per-round PulsarRoundSigner from the round
// context. Cheap; built per round because the session binds height/round/block.
func (s *Signer) roundSigner(rc RoundContext) *qpulsar.PulsarRoundSigner {
	return &qpulsar.PulsarRoundSigner{
		Session:          rc.Session,
		Pool:             s.pool,
		Threshold:        s.threshold,
		ValidatorSetSize: s.vsetSize,
		L:                mldsa65L,
		Core:             stubSignatureCore{},
	}
}

// zshare is this voter's deterministic z-share for the round: a 5120-byte
// (ML-DSA-65 L·N·4) vector derived from the sealed share secret and the round
// nonce. Unpredictable without the sealed secret, bound to the round nonce.
func (s *Signer) zshare(rc RoundContext) []byte {
	return expandBytes(zshareBytes, "cp-share-z", s.share.secret[:], rc.NonceID[:])
}

// CommitPoP authenticates this voter's Round1 z-share commitment with its
// identity proof-of-possession, so peers can attribute the commitment to a
// distinct registered sender. The ALL-Round1 barrier counts only authenticated
// commitments, so a single node cannot forge a quorum of them (anti-rush).
func (s *Signer) CommitPoP(rc RoundContext, zcommit []byte) ([]byte, error) {
	return signPoP(s.share.idKey, round1TBS(rc.SessionID, rc.NonceID, s.share.Index, zcommit))
}

// Commit runs the REAL Round1 (binding the canonical, non-grindable shared
// nonce to the session) and returns this voter's SignRound1 plus a binding
// commitment to the z-share it will reveal. The commitment is the
// orchestration-layer anti-rush lock: it is broadcast in Round1 and opened in
// Round2, so a rushing voter cannot swap its share after the barrier.
func (s *Signer) Commit(rc RoundContext) (pulsarlib.SignRound1, []byte, error) {
	rs := s.roundSigner(rc)
	r1, err := rs.Round1(rc.SessionID, rc.NonceID, rc.Nonce)
	if err != nil {
		return pulsarlib.SignRound1{}, nil, err
	}
	return r1, commitZ(rc.SessionID, s.share.Index, s.zshare(rc)), nil
}

// Reveal runs the REAL Round2 (emitting the proof-carrying z partial, with the
// public bindings enforced by pulsar.VerifyZPartial) and authenticates the leg
// with this voter's identity proof-of-possession (the consensus-layer origin
// binding the pulsar primitive leaves to the orchestrator).
func (s *Signer) Reveal(rc RoundContext, r1 pulsarlib.SignRound1) (pulsarlib.Partial, error) {
	rs := s.roundSigner(rc)
	p, err := rs.Round2(r1, pulsarlib.PartialInput{PartyID: s.share.Index, ZShare: s.zshare(rc)})
	if err != nil {
		return pulsarlib.Partial{}, err
	}
	p.Author = s.share.Node
	authSig, err := signPoP(s.share.idKey, partialTBS(p))
	if err != nil {
		return pulsarlib.Partial{}, err
	}
	p.AuthSig = authSig
	return p, nil
}

// Aggregate runs the REAL Finalize over the collected legs: canonical
// (anti-grind) signer-set selection, z aggregation, and the ConsensusCert
// accountability artifact, with the stub SignatureCore threading a deterministic
// Pulsar leg in. Deterministic in the legs, so every honest voter that holds the
// same legs produces a byte-identical cert.
func (s *Signer) Aggregate(rc RoundContext, r1 pulsarlib.SignRound1, legs []pulsarlib.Partial) (pulsarlib.ConsensusCert, error) {
	rs := s.roundSigner(rc)
	_, cc, err := rs.Finalize(r1, legs)
	return cc, err
}

// commitZ is the binding (and, given the sealed-secret entropy in the z-share,
// hiding) commitment to a revealed share, domain-separated by session and party:
// H("cp-commit" || sessionID || partyID || z). Broadcast in Round1, opened in
// Round2; collision resistance stops opening it with a different share, and the
// session/party binding stops replaying a commitment across sessions or parties
// to seed a fake barrier entry (red_exploits #4 hardening).
func commitZ(sid [32]byte, partyID uint32, z []byte) []byte {
	h := sha256.New()
	h.Write([]byte("cp-commit"))
	h.Write(sid[:])
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], partyID)
	h.Write(idx[:])
	h.Write(z)
	return h.Sum(nil)
}

// expandBytes deterministically expands a seed into n bytes via SHA-512 in
// counter mode (domain-separated).
func expandBytes(n int, tag string, parts ...[]byte) []byte {
	out := make([]byte, 0, n)
	var ctr uint32
	for len(out) < n {
		h := sha512.New()
		h.Write([]byte(tag))
		var c [4]byte
		binary.BigEndian.PutUint32(c[:], ctr)
		h.Write(c[:])
		for _, p := range parts {
			h.Write(p)
		}
		out = append(out, h.Sum(nil)...)
		ctr++
	}
	return out[:n]
}

// ----------------------------------------------------------------------------
// Cert composition seam.
// ----------------------------------------------------------------------------

// CertComposer turns the aggregated Pulsar leg (from the REAL Finalize) into a
// full triple-PQ QuasarCert. The stub produces a structurally-valid cert that
// passes the PUBLISHED QuasarCert.Verify triple-gate (BLS + Corona +
// MLDSARollup present, signer count consistent).
//
// Compose returns selfComposedCert, not a bare *quasar.QuasarCert — the typed
// seam that makes verifyOwnCertStructure's input provably local (see driver.go).
//
// STUB BOUNDARY (increment 2+): the real composer runs the Corona / Magnetar /
// BLS threshold ceremonies over the same round digest and calls
// quasar.ComposePolaris over the four real legs; verification then moves from
// the structural QuasarCert.Verify to the cryptographic VerifyWithRealKeys —
// the real composer must keep returning selfComposedCert so that move is the
// ONLY thing that changes at the driver.go call site.
type CertComposer interface {
	Compose(rc RoundContext, cc pulsarlib.ConsensusCert) (selfComposedCert, error)
}

type stubComposer struct{}

// NewStubComposer returns the increment-1 stub composer. Fail-closed: refuses
// outside a go-test harness while the crypto is stub (mustHarnessOnly) — see
// containment.go.
func NewStubComposer() CertComposer {
	mustHarnessOnly("NewStubComposer")
	return stubComposer{}
}

func legBytes(tag string, digest quasar.RoundDigest, extra ...[]byte) []byte {
	h := sha256.New()
	h.Write([]byte(tag))
	h.Write(digest[:])
	for _, e := range extra {
		h.Write(e)
	}
	return h.Sum(nil)
}

// Compose binds every leg to the round digest, so a cert composed for one round
// digest cannot be presented for another (the legs differ). Deterministic in
// (digest, aggregated Pulsar leg, signer bitmap): all honest voters compose
// byte-identical certs.
func (stubComposer) Compose(rc RoundContext, cc pulsarlib.ConsensusCert) (selfComposedCert, error) {
	validators := bitmapPopcount(cc.SignerBitmap)
	cert := &quasar.QuasarCert{
		BLS:         legBytes("cp-bls", rc.Digest, cc.SignerBitmap),
		Corona:      legBytes("cp-corona", rc.Digest, cc.SignerBitmap),
		Pulsar:      cc.Signature.Bytes,
		Magnetar:    legBytes("cp-magnetar", rc.Digest, cc.SignerBitmap),
		MLDSARollup: legBytes("cp-mldsa", rc.Digest, cc.Signature.Bytes),
		Epoch:       rc.Epoch,
		Finality:    time.Unix(int64(rc.Height), 0).UTC(), // deterministic, not wall-clock
		Validators:  validators,
	}
	return newSelfComposedCert(cert), nil
}

// bitmapPopcount counts set bits in a signer bitmap (= distinct signers).
func bitmapPopcount(bitmap []byte) int {
	n := 0
	for _, b := range bitmap {
		n += bits.OnesCount8(b)
	}
	return n
}
