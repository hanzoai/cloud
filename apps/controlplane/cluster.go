//go:build controlplane

package controlplane

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/luxfi/consensus/protocol/quasar"
	qpulsar "github.com/luxfi/consensus/protocol/quasar/pulsar"
	pulsarlib "github.com/luxfi/pulsar/pkg/pulsar"
)

// Cluster is the in-process N-voter harness: a shared in-memory transport, a
// shared background nonce pool (stub NonceMPC), a stub-DKG keygen issuing one
// sealed share per pod, the validator registry, and N voters each with their
// own RSM. It is the composition root for the simulation and is exported so the
// red-team can drive it and inject faults.
//
// STUB BOUNDARIES exercised here (increment 2): NonceMPC (the nonce pool is
// pre-generated in memory), DKG (shares are seed-derived, not from a real
// distributed key generation), transport (in-memory bus), share custody
// (in-memory, not KMS-sealed).
type Cluster struct {
	n      int
	quorum int
	voters []*Voter
	byName map[string]*Voter
	bus    *memBus
	reg    *ValidatorRegistry
	pool   qpulsar.NonceCertPool
	cfg    RoundConfig
}

func itob(i int) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	return b[:]
}

// newNoncePool builds the shared background nonce pool (stub NonceMPC output):
// `count` nonce certs with non-zero transcript roots and a pool root committing
// to them.
func newNoncePool(seed []byte, count int) *qpulsar.MemNonceCertPool {
	certs := make([]pulsarlib.NonceCert, count)
	h := sha256.New()
	h.Write([]byte("cp-nonce-pool-root"))
	for i := 0; i < count; i++ {
		var nid, tr [32]byte
		copy(nid[:], expandBytes(32, "cp-nonce-id", seed, itob(i)))
		copy(tr[:], expandBytes(32, "cp-nonce-tr", seed, itob(i)))
		certs[i] = pulsarlib.NonceCert{
			Mode:                pulsarlib.ModeP65,
			NonceID:             nid,
			NonceTranscriptRoot: tr,
		}
		h.Write(nid[:])
		h.Write(tr[:])
	}
	var root [32]byte
	copy(root[:], h.Sum(nil))
	return qpulsar.NewMemNonceCertPool(certs, root)
}

// NewCluster builds an N-voter cluster with the default strict-PQ round config.
// Panics only on a registration invariant violation (a programmer error in the
// harness wiring), never on adversary input.
func NewCluster(n int) *Cluster {
	return NewClusterWithConfig(n, DefaultRoundConfig())
}

// NewClusterWithConfig builds an N-voter cluster with an explicit round config.
func NewClusterWithConfig(n int, cfg RoundConfig) *Cluster {
	quorum := bftQuorum(n)
	// Fail closed on unsafe sizing: the no-fork property rests on the quorum
	// intersection bound, so a bad (N, quorum, f) must never construct a cluster.
	if err := checkQuorumSafety(n, quorum, bftFaultTolerance(n)); err != nil {
		panic(fmt.Sprintf("controlplane: cluster construction: %v", err))
	}
	seed := []byte(fmt.Sprintf("cp-cluster-seed-%d", n))
	pool := newNoncePool(seed, 16)
	reg := NewValidatorRegistry()

	names := make([]string, n)
	nodes := make([]pulsarlib.NodeID, n)
	shares := make([]Share, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("cloud-%d", i)
		names[i] = name
		custody := NewMemCustody(seed, name, uint32(i+1)) // 1-based party index
		share := custody.Open()
		shares[i] = share
		nodes[i] = share.Node
		if err := reg.Register(share); err != nil {
			panic(fmt.Sprintf("controlplane: harness registration: %v", err))
		}
	}

	bus := NewMemBus(nodes)
	c := &Cluster{
		n:      n,
		quorum: quorum,
		byName: map[string]*Voter{},
		bus:    bus,
		reg:    reg,
		pool:   pool,
		cfg:    cfg,
	}
	for i := 0; i < n; i++ {
		v := newVoter(names[i], shares[i], cfg, pool, quorum, n, names, reg, bus, NewStubComposer(), NewMemControlDB())
		c.voters = append(c.voters, v)
		c.byName[names[i]] = v
	}
	return c
}

// N is the voter count.
func (c *Cluster) N() int { return c.n }

// Quorum is the BFT >2/3 quorum.
func (c *Cluster) Quorum() int { return c.quorum }

// FaultTolerance is the max faults the cluster tolerates.
func (c *Cluster) FaultTolerance() int { return bftFaultTolerance(c.n) }

// Voters returns all voters (alive or not).
func (c *Cluster) Voters() []*Voter { return c.voters }

// Voter returns a voter by name.
func (c *Cluster) Voter(name string) *Voter { return c.byName[name] }

// Bus returns the in-memory transport (for red-team message injection).
func (c *Cluster) Bus() *memBus { return c.bus }

// Registry returns the validator registry.
func (c *Cluster) Registry() *ValidatorRegistry { return c.reg }

// Pool returns the shared nonce pool.
func (c *Cluster) Pool() qpulsar.NonceCertPool { return c.pool }

// Config returns the round config.
func (c *Cluster) Config() RoundConfig { return c.cfg }

// Kill marks a voter crashed (a crash / partition fault).
func (c *Cluster) Kill(name string) {
	if v := c.byName[name]; v != nil {
		c.bus.SetAlive(v.Node, false)
	}
}

// Revive marks a voter live again.
func (c *Cluster) Revive(name string) {
	if v := c.byName[name]; v != nil {
		c.bus.SetAlive(v.Node, true)
	}
}

func (c *Cluster) alive(v *Voter) bool { return c.bus.isAlive(v.Node) }

func (c *Cluster) aliveVoters() []*Voter {
	var out []*Voter
	for _, v := range c.voters {
		if c.alive(v) {
			out = append(out, v)
		}
	}
	return out
}

// NextHeight is the next height to finalize (one above the alive voters' state).
func (c *Cluster) NextHeight() uint64 {
	for _, v := range c.aliveVoters() {
		return v.rsm.Height() + 1
	}
	return 1
}

// ParentRoot is the chain-extension anchor for the next block (all converged
// alive voters agree).
func (c *Cluster) ParentRoot() [32]byte {
	for _, v := range c.aliveVoters() {
		return v.parentRoot()
	}
	return [32]byte{}
}

// NewBlock builds a candidate block at the next height for a round, extending
// the current converged state.
func (c *Cluster) NewBlock(proposer string, round uint32, ops []PlacementOp) *PlacementBlock {
	return &PlacementBlock{
		Height:     c.NextHeight(),
		Round:      round,
		Proposer:   proposer,
		ParentRoot: c.ParentRoot(),
		Ops:        ops,
	}
}

// proposerFor selects the round's proposer by deterministic rotation.
func (c *Cluster) proposerFor(height uint64, round uint32) string {
	idx := int((height-1)+uint64(round)) % c.n
	return c.voters[idx].Name
}

// ----------------------------------------------------------------------------
// Synchronous phased round execution.
// ----------------------------------------------------------------------------

func (c *Cluster) resetAll() {
	for _, v := range c.voters {
		v.resetRound()
	}
}

// deliverProposals feeds each alive voter the block blockFor returns for it
// (nil ⇒ that voter receives no proposal). An OnProposal error means the voter
// abstains (honest policy refusal or a mismatched/forked proposal).
func (c *Cluster) deliverProposals(blockFor func(v *Voter) *PlacementBlock) {
	for _, v := range c.aliveVoters() {
		if b := blockFor(v); b != nil {
			_ = v.OnProposal(b)
		}
	}
}

func (c *Cluster) barrierPhase() {
	for _, v := range c.aliveVoters() {
		v.collectRound1()
	}
	for _, v := range c.aliveVoters() {
		v.crossBarrier()
	}
}

func (c *Cluster) round2Phase() {
	for _, v := range c.aliveVoters() {
		v.emitRound2()
	}
	for _, v := range c.aliveVoters() {
		v.collectRound2()
	}
}

func (c *Cluster) finalizePhase() int {
	n := 0
	for _, v := range c.aliveVoters() {
		if v.tryFinalize() {
			n++
		}
	}
	return n
}

// RunRound runs one full synchronous round with per-voter proposal delivery
// (blockFor), returning the number of voters that finalized. Exported so the
// red-team can drive equivocation (different blocks to different voters) and
// interleave message injection with the phases via the lower-level voter methods.
func (c *Cluster) RunRound(blockFor func(v *Voter) *PlacementBlock) int {
	c.resetAll()
	c.deliverProposals(blockFor)
	c.barrierPhase()
	c.round2Phase()
	return c.finalizePhase()
}

// HeightResult reports the outcome of finalizing one height.
type HeightResult struct {
	Finalized bool // true iff a quorum finalized (never a forced finalize)
	Round     uint32
	Voters    int // how many voters finalized
	Block     *PlacementBlock
	Cert      *quasar.QuasarCert
}

// RunHeight drives the ceremony for the next height with honest proposers,
// rotating the proposer on stall. It finalizes as soon as a quorum of voters
// commit; if no round reaches quorum after cycling every proposer, it SAFE-HALTS
// (Finalized=false) — never a forced finalize.
func (c *Cluster) RunHeight(ops []PlacementOp) HeightResult {
	h := c.NextHeight()
	for r := uint32(0); int(r) < c.n; r++ {
		proposer := c.proposerFor(h, r)
		if pv := c.byName[proposer]; pv == nil || !c.alive(pv) {
			continue // dead proposer — rotate
		}
		block := c.NewBlock(proposer, r, ops)
		n := c.RunRound(func(*Voter) *PlacementBlock { return block })
		if n >= c.quorum {
			var cert *quasar.QuasarCert
			for _, v := range c.aliveVoters() {
				if fin := v.Finalized(); fin != nil {
					cert = fin.Cert
					break
				}
			}
			return HeightResult{Finalized: true, Round: r, Voters: n, Block: block, Cert: cert}
		}
	}
	return HeightResult{Finalized: false}
}

// Converged reports whether every alive voter is at the same height with the
// same state hash — the cross-voter agreement invariant.
func (c *Cluster) Converged() bool {
	var h uint64
	var hash string
	first := true
	for _, v := range c.aliveVoters() {
		vh := v.rsm.Height()
		vhash := string(v.rsm.StateHash())
		if first {
			h, hash, first = vh, vhash, false
			continue
		}
		if vh != h || vhash != hash {
			return false
		}
	}
	return true
}
