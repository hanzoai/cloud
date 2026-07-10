//go:build controlplane

package controlplane

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/luxfi/consensus/protocol/quasar"
	qpulsar "github.com/luxfi/consensus/protocol/quasar/pulsar"
	pulsarlib "github.com/luxfi/pulsar/pkg/pulsar"
)

// Committed is a finalized (block, cert) pair a voter has independently
// verified and applied.
type Committed struct {
	Block *PlacementBlock
	Cert  *quasar.QuasarCert
}

// errExternalCertVerifyNotImplemented is VerifyExternalCert's fail-closed
// return today: the cryptographic quasar verify it must call (VerifyUnderPolicy
// / VerifyWithRealKeys) does not exist yet in this package (increment-2). A
// missing implementation must never present as "verified"; erroring is the
// only safe default for a not-yet-built cryptographic gate.
var errExternalCertVerifyNotImplemented = errors.New(
	"controlplane: external-cert cryptographic verification is not implemented (increment-2); refusing to admit via the structural check")

// Voter is one pod's full ceremony participation: a single share (one signer),
// a policy gate, an independent verifier, and its own replicated state machine.
// A Voter never trusts another node's word — it recomputes the round digest and
// session, validates every leg, re-aggregates via the real Pulsar RoundSigner,
// re-verifies the cert, and only then applies.
//
// The round methods are stepped by the Cluster in synchronous phases; each
// phase models one message round with a timeout (quiescence after the phase).
// This keeps the harness deterministic while exercising the real barrier,
// quorum, dedup, proof-of-possession, and commit-reveal logic.
type Voter struct {
	Name     string
	Node     pulsarlib.NodeID
	Index    uint32
	signer   *Signer
	composer CertComposer
	reg      *ValidatorRegistry
	bus      Transport
	cfg      RoundConfig
	pool     qpulsar.NonceCertPool
	quorum   int
	voters   []string // canonical sorted voter names bound into every digest

	rsm *PlacementRSM

	// Per-round scratch (reset by resetRound before each round).
	rc            RoundContext
	block         *PlacementBlock
	accepted      bool
	barrierPassed bool
	r1self        pulsarlib.SignRound1
	committed     map[uint32][]byte            // partyID -> z-share commitment (session-bound)
	legs          map[uint32]pulsarlib.Partial // partyID -> validated leg
	pending       []Message                    // buffered msgs not yet eligible (rushing defense)

	finalized *Committed
}

func newVoter(name string, share Share, cfg RoundConfig, pool qpulsar.NonceCertPool, quorum, vsetSize int, voters []string, reg *ValidatorRegistry, bus Transport, composer CertComposer, db ControlDB) *Voter {
	cp := append([]string(nil), voters...)
	sort.Strings(cp)
	return &Voter{
		Name:     name,
		Node:     share.Node,
		Index:    share.Index,
		signer:   NewSigner(share, pool, quorum, vsetSize),
		composer: composer,
		reg:      reg,
		bus:      bus,
		cfg:      cfg,
		pool:     pool,
		quorum:   quorum,
		voters:   cp,
		rsm:      NewPlacementRSM(db),
	}
}

// RSM exposes the voter's state machine for convergence assertions.
func (v *Voter) RSM() *PlacementRSM { return v.rsm }

// Finalized reports what the voter committed this round (nil if it did not).
func (v *Voter) Finalized() *Committed { return v.finalized }

// parentRoot is the 32-byte chain-extension anchor: the truncated commitment to
// the voter's applied state. A proposal must name this exact parent or honest
// voters refuse it (fork prevention).
func (v *Voter) parentRoot() [32]byte {
	var pr [32]byte
	copy(pr[:], v.rsm.StateHash())
	return pr
}

// resetRound clears per-round scratch and drains any stale inbox from a prior
// round. The Cluster resets EVERY voter before any voter broadcasts, so live
// Round1 messages for the new round are never discarded.
func (v *Voter) resetRound() {
	v.accepted = false
	v.barrierPassed = false
	v.block = nil
	v.finalized = nil
	v.r1self = pulsarlib.SignRound1{}
	v.committed = map[uint32][]byte{}
	v.legs = map[uint32]pulsarlib.Partial{}
	v.pending = nil
	v.drain()
	v.pending = nil // discard stale cross-round messages
}

// OnProposal is the honest voter's entry point. It refuses (returns an error,
// abstaining from the round) unless the proposal extends this voter's chain,
// sits at the next height, and passes the policy invariant gate. On acceptance
// it computes the round digest + session itself, forms its Round1 commitment via
// the real Pulsar signer, records its own z-share commitment, and broadcasts
// Round1.
func (v *Voter) OnProposal(b *PlacementBlock) error {
	if b == nil {
		return errNilBlock
	}
	if b.Height != v.rsm.Height()+1 {
		return fmt.Errorf("controlplane: proposal height=%d, want %d", b.Height, v.rsm.Height()+1)
	}
	if b.ParentRoot != v.parentRoot() {
		return fmt.Errorf("controlplane: proposal parent-root mismatch (fork attempt)")
	}
	// Policy gate BEFORE the crypto: an honest voter never signs a block whose
	// placement ops violate control-plane invariants.
	if err := CheckInvariants(v.rsm.State(), b); err != nil {
		return err
	}
	// Recompute the round digest + session — never trust the proposer's word.
	rc, err := NewRoundContext(v.cfg, b, v.voters, v.pool)
	if err != nil {
		return err
	}
	r1, zcommit, err := v.signer.Commit(rc)
	if err != nil {
		return err
	}
	commitSig, err := v.signer.CommitPoP(rc, zcommit)
	if err != nil {
		return err
	}
	v.rc = rc
	v.block = b
	v.r1self = r1
	v.committed[v.Index] = zcommit
	v.accepted = true
	v.bus.Broadcast(Message{Kind: MsgRound1, Height: b.Height, Round: b.Round, From: v.Node, R1: r1, Commit: zcommit, CommitSig: commitSig})
	return nil
}

// sessionMatch binds a message to this voter's round. A message committing to a
// different session/nonce (e.g. a leg for an equivocated block) is not this
// round's and is ignored — the structural equivocation defense.
func (v *Voter) sessionMatch(sid, nid [32]byte) bool {
	return sid == v.rc.SessionID && nid == v.rc.NonceID
}

// drain pulls all currently-available inbox messages into the pending buffer.
func (v *Voter) drain() {
	ch := v.bus.Inbox(v.Node)
	for {
		select {
		case m := <-ch:
			v.pending = append(v.pending, m)
		default:
			return
		}
	}
}

// collectRound1 records the session-bound Round1 commitments and BUFFERS any
// Round2 that arrived early (a rushing voter). Early Round2 is never processed
// here — it waits for the barrier, denying the rusher any adaptive advantage.
func (v *Voter) collectRound1() {
	if !v.accepted {
		return
	}
	v.drain()
	var keep []Message
	for _, m := range v.pending {
		switch {
		case m.Kind == MsgRound1 && v.sessionMatch(m.R1.SessionID, m.R1.NonceID):
			// Authenticate the commitment to its claimed sender (proof-of-
			// possession) before counting it toward the barrier — otherwise one
			// node forges a quorum of spoofed commitments and defeats
			// commit-before-reveal (red_exploits_test #3).
			if idx, ok := v.reg.IndexOf(m.From); ok {
				if _, seen := v.committed[idx]; !seen && len(m.Commit) > 0 &&
					v.reg.VerifyPoP(m.From, round1TBS(m.R1.SessionID, m.R1.NonceID, idx, m.Commit), m.CommitSig) {
					v.committed[idx] = m.Commit
				}
			}
		case m.Kind == MsgRound2:
			keep = append(keep, m) // buffer until after the barrier
		}
	}
	v.pending = keep
}

// barrierReached reports whether a quorum of Round1 commitments is locked. The
// ALL-Round1 barrier: no Round2 share is revealed or processed until a quorum
// of commitments is fixed, so no voter can adapt its share to others'.
func (v *Voter) barrierReached() bool { return len(v.committed) >= v.quorum }

// crossBarrier latches the barrier for this round.
func (v *Voter) crossBarrier() { v.barrierPassed = v.barrierReached() }

// emitRound2 reveals this voter's share (post-barrier only) via the real Pulsar
// Round2 and self-ingests it before broadcasting, so the voter counts its own
// leg toward quorum.
func (v *Voter) emitRound2() {
	if !v.accepted || !v.barrierPassed {
		return
	}
	p, err := v.signer.Reveal(v.rc, v.r1self)
	if err != nil {
		return
	}
	v.ingestLeg(p, v.Node)
	v.bus.Broadcast(Message{Kind: MsgRound2, Height: v.block.Height, Round: v.block.Round, From: v.Node, R2: p})
}

// ingestLeg is the byzantine leg gate. A leg counts toward quorum only if it is
// bound to this round, authored by the registered holder of its claimed party
// index, carries a valid proof-of-possession, opens the author's Round1
// commitment, and is not a duplicate. Every rejection is a real defense:
//
//	session mismatch    -> equivocation / cross-round replay
//	author/index or PoP -> rogue / unregistered / index-spoofed / forged leg
//	no commitment        -> reveal without commit
//	bad commit-open      -> a swapped share (rushing / equivocation)
//	duplicate partyID    -> aggregation double-count
func (v *Voter) ingestLeg(p pulsarlib.Partial, author pulsarlib.NodeID) {
	if !v.sessionMatch(p.SessionID, p.NonceID) {
		return
	}
	idx, ok := v.reg.IndexOf(author)
	if !ok || idx != p.PartyID {
		return // unregistered/rogue author, or a party-index spoof
	}
	if !v.reg.VerifyPoP(author, partialTBS(p), p.AuthSig) {
		return // forged or lifted proof-of-possession
	}
	wc, committed := v.committed[p.PartyID]
	if !committed || !bytes.Equal(commitZ(p.SessionID, p.PartyID, p.ZShare), wc) {
		return // reveal without commit, or a share that does not open the commitment
	}
	if _, seen := v.legs[p.PartyID]; seen {
		return // dedup: first valid leg per party wins
	}
	v.legs[p.PartyID] = p
}

// collectRound2 processes revealed shares — but only after the barrier. Buffered
// early shares and freshly-arrived ones flow through the same ingestLeg gate.
func (v *Voter) collectRound2() {
	if !v.accepted || !v.barrierPassed {
		return
	}
	v.drain()
	var keep []Message
	for _, m := range v.pending {
		if m.Kind == MsgRound2 {
			v.ingestLeg(m.R2, m.From)
		} else {
			keep = append(keep, m)
		}
	}
	v.pending = keep
}

// selfComposedCert is the typed seam between CertComposer.Compose (the ONLY
// producer in this package) and verifyOwnCertStructure (its ONLY consumer).
// The wrapped field is unexported and there is no exported constructor other
// than newSelfComposedCert, called only from a CertComposer.Compose
// implementation in THIS package. An externally-received cert — bytes off the
// wire on a future recovery/light-client/gossip path, deserialized into a
// bare *quasar.QuasarCert — has NO way to become one of these: it would have
// to be re-wrapped by code inside this package, and no such code exists or may
// be added without also satisfying CertComposer (i.e. without re-running the
// real ceremony). This makes "an external cert reached the structural check"
// a type error, not a code-review discipline — see
// TestExternalCert_MustNotAdmitThroughStructuralCheck (external_cert_test.go)
// which locks the invariant from a black-box (exported-surface-only) vantage.
type selfComposedCert struct {
	cert *quasar.QuasarCert
}

func newSelfComposedCert(cert *quasar.QuasarCert) selfComposedCert {
	return selfComposedCert{cert: cert}
}

// verifyOwnCertStructure is the structural well-formedness gate for a cert the
// voter COMPOSED ITSELF (never a peer's — enforced by the selfComposedCert
// type, not just by convention). The published QuasarCert.Verify confirms the
// triple (BLS+Corona+MLDSARollup) is present and the signer count is
// consistent — it does NOT verify signatures. This is sound HERE only because
// every leg was already cryptographically gated in ingestLeg and the cert is
// locally composed. An EXTERNALLY-received cert (a future recovery/light-client
// path) MUST NOT be accepted through this structural check — it must go through
// VerifyExternalCert, which routes to the cryptographic quasar verify
// (VerifyWithRealKeys / VerifyUnderPolicy), increment-2. Accepting an external
// cert here would be forgeable (red_exploits #8) — and is now also a type
// error, since VerifyExternalCert's caller only ever holds a bare
// *quasar.QuasarCert, never a selfComposedCert.
func verifyOwnCertStructure(sc selfComposedCert, voters []string) bool {
	return sc.cert != nil && sc.cert.Verify(voters)
}

// VerifyExternalCert is the ONLY seam a future external-cert path (recovery,
// light-client, gossip — none exist yet in this package, see doc.go
// increment-2) may call to admit a cert this process did not compose itself.
// It is intentionally UNIMPLEMENTED today rather than silently falling back to
// verifyOwnCertStructure: it always fails closed. Increment-2 real
// implementation MUST route to the cryptographic
// github.com/luxfi/consensus/protocol/quasar VerifyUnderPolicy /
// VerifyWithRealKeys — never QuasarCert.Verify (the structural check), and
// must never construct a selfComposedCert from the input (that type's zero
// exported constructor is exactly what prevents this).
func VerifyExternalCert(cert *quasar.QuasarCert, voters []string) error {
	return errExternalCertVerifyNotImplemented
}

// tryFinalize aggregates the collected legs via the real Pulsar Finalize
// (canonical signer-set + z aggregation), composes the triple-PQ cert,
// INDEPENDENTLY verifies it (trusting no external cert), and applies the block
// through the fail-secure RSM gate. Returns false — never a forced finalize —
// whenever quorum is not met or any gate fails.
func (v *Voter) tryFinalize() bool {
	if !v.accepted || len(v.legs) < v.quorum {
		return false
	}
	ids := make([]uint32, 0, len(v.legs))
	for id := range v.legs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	legs := make([]pulsarlib.Partial, 0, len(ids))
	for _, id := range ids {
		legs = append(legs, v.legs[id])
	}

	cc, err := v.signer.Aggregate(v.rc, v.r1self, legs)
	if err != nil {
		return false
	}
	sc, err := v.composer.Compose(v.rc, cc)
	if err != nil {
		return false
	}
	// Structural well-formedness of the SELF-COMPOSED cert (NOT a cryptographic
	// verify — see verifyOwnCertStructure). Sound only because every leg was
	// already gated in ingestLeg and this cert is locally composed; the
	// selfComposedCert type is what makes "an external cert reached this check"
	// impossible to even construct, not just disallowed by convention.
	if !verifyOwnCertStructure(sc, v.voters) {
		return false
	}
	if err := v.rsm.Apply(v.block); err != nil {
		return false // fail-secure: invariant/ordering/persistence gate
	}
	v.finalized = &Committed{Block: v.block, Cert: sc.cert}
	return true
}
