//go:build controlplane

package controlplane

import (
	"bytes"
	"errors"
	"testing"

	pulsarlib "github.com/luxfi/pulsar/pkg/pulsar"
)

// TestSafety_Equivocation_NeitherFinalizes — a byzantine proposer sends block A
// to 3 voters and a conflicting block B to 4. Each honest voter binds its legs
// to its own session, so the two commitment sets never merge: with 3 and 4
// commitments and a quorum of 5, NEITHER digest reaches quorum. Two conflicting
// digests cannot both finalize (2·quorum = 10 > N = 7).
func TestSafety_Equivocation_NeitherFinalizes(t *testing.T) {
	c := NewCluster(7)
	h := c.NextHeight()
	parent := c.ParentRoot()
	blockA := &PlacementBlock{Height: h, Round: 0, Proposer: "cloud-0", ParentRoot: parent, Ops: []PlacementOp{assignOp("shard-A", "cloud-0")}}
	blockB := &PlacementBlock{Height: h, Round: 0, Proposer: "cloud-0", ParentRoot: parent, Ops: []PlacementOp{assignOp("shard-A", "cloud-6")}}
	if blockA.PayloadRoot() == blockB.PayloadRoot() {
		t.Fatal("conflicting blocks must differ in payload root")
	}

	aGroup := map[string]bool{"cloud-0": true, "cloud-1": true, "cloud-2": true}
	n := c.RunRound(func(v *Voter) *PlacementBlock {
		if aGroup[v.Name] {
			return blockA
		}
		return blockB
	})
	if n != 0 {
		t.Fatalf("equivocation must not finalize, %d voters finalized", n)
	}
	for _, v := range c.Voters() {
		if v.Finalized() != nil || v.RSM().Height() != 0 {
			t.Fatalf("%s finalized an equivocated block", v.Name)
		}
	}
}

// TestSafety_Equivocation_MajorityCommitsMinorityCannot — split 5/2. The
// majority (=quorum) commits its block; the minority (2 < quorum) cannot. At
// most ONE block ever finalizes — equivocation can never fork the chain.
func TestSafety_Equivocation_MajorityCommitsMinorityCannot(t *testing.T) {
	c := NewCluster(7)
	h := c.NextHeight()
	parent := c.ParentRoot()
	blockA := &PlacementBlock{Height: h, Round: 0, Proposer: "cloud-0", ParentRoot: parent, Ops: []PlacementOp{assignOp("shard-A", "cloud-0")}}
	blockB := &PlacementBlock{Height: h, Round: 0, Proposer: "cloud-0", ParentRoot: parent, Ops: []PlacementOp{assignOp("shard-A", "cloud-6")}}

	minority := map[string]bool{"cloud-5": true, "cloud-6": true}
	n := c.RunRound(func(v *Voter) *PlacementBlock {
		if minority[v.Name] {
			return blockB
		}
		return blockA
	})
	if n != 5 {
		t.Fatalf("majority quorum should finalize (5), got %d", n)
	}
	// Everyone who finalized committed block A (the majority block); nobody
	// finalized block B.
	for _, v := range c.Voters() {
		fin := v.Finalized()
		if minority[v.Name] {
			if fin != nil {
				t.Fatalf("minority %s finalized under sub-quorum", v.Name)
			}
			continue
		}
		if fin == nil || fin.Block.PayloadRoot() != blockA.PayloadRoot() {
			t.Fatalf("majority %s did not finalize block A", v.Name)
		}
	}
}

// TestSafety_OnePodOneShare — the registry refuses a node a second share
// (one-pod-two-shares) and refuses two nodes the same share slot.
func TestSafety_OnePodOneShare(t *testing.T) {
	seed := []byte("iso")
	reg := NewValidatorRegistry()
	if err := reg.Register(NewMemCustody(seed, "cloud-0", 1).Open()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	// Same node, second share index -> one-pod-two-shares refused.
	if err := reg.Register(NewMemCustody(seed, "cloud-0", 2).Open()); !errors.Is(err, ErrNodeHasShare) {
		t.Fatalf("second share for a pod: want ErrNodeHasShare, got %v", err)
	}
	// Different node, occupied share slot -> refused.
	if err := reg.Register(NewMemCustody(seed, "attacker", 1).Open()); !errors.Is(err, ErrShareIndexTaken) {
		t.Fatalf("occupied slot: want ErrShareIndexTaken, got %v", err)
	}
	if reg.Size() != 1 {
		t.Fatalf("registry admitted a duplicate: size %d", reg.Size())
	}
}

// TestSafety_RushingVoter_Buffered — a voter that reveals its Round2 share
// before the ALL-Round1 barrier gains no advantage: honest voters BUFFER the
// early share and refuse to process it until the barrier, and the finalized
// outcome is byte-identical to a clean run.
func TestSafety_RushingVoter_Buffered(t *testing.T) {
	c := NewCluster(7)
	block := c.NewBlock("cloud-0", 0, []PlacementOp{assignOp("shard-A", "cloud-0")})

	c.resetAll()
	c.deliverProposals(func(*Voter) *PlacementBlock { return block })

	// cloud-3 RUSHES: it reveals its real Round2 share now, before the barrier.
	rusher := c.Voter("cloud-3")
	earlyLeg, err := rusher.signer.Reveal(rusher.rc, rusher.r1self)
	if err != nil {
		t.Fatalf("rusher reveal: %v", err)
	}
	c.Bus().Broadcast(Message{Kind: MsgRound2, Height: block.Height, Round: 0, From: rusher.Node, R2: earlyLeg})

	// A victim collects Round1. The early Round2 must be buffered, NOT ingested.
	victim := c.Voter("cloud-0")
	victim.collectRound1()
	if _, seen := victim.legs[rusher.Index]; seen {
		t.Fatal("rushing share processed before the barrier — anti-rush broken")
	}

	// Finish the round normally.
	c.barrierPhase()
	c.round2Phase()
	if got := c.finalizePhase(); got < c.Quorum() {
		t.Fatalf("round should still finalize, got %d finalized", got)
	}

	// Determinism: the cert equals a clean, no-rush run at the same height/ops
	// (same cluster seed) — the rush changed nothing.
	clean := NewCluster(7)
	cleanRes := clean.RunHeight([]PlacementOp{assignOp("shard-A", "cloud-0")})
	if !cleanRes.Finalized {
		t.Fatal("clean run must finalize")
	}
	rushed, _ := victim.Finalized().Cert.MarshalBinary()
	cleanCert, _ := cleanRes.Cert.MarshalBinary()
	if !bytes.Equal(rushed, cleanCert) {
		t.Fatal("rushing altered the finalized cert — rusher gained bias")
	}
}

// TestSafety_DuplicateLeg_Deduped — a byzantine voter submitting the same leg
// twice is counted once (aggregation dedupe by party index).
func TestSafety_DuplicateLeg_Deduped(t *testing.T) {
	c := NewCluster(7)
	block := c.NewBlock("cloud-0", 0, []PlacementOp{assignOp("shard-A", "cloud-0")})
	c.resetAll()
	c.deliverProposals(func(*Voter) *PlacementBlock { return block })
	c.barrierPhase()

	byz := c.Voter("cloud-3")
	leg, err := byz.signer.Reveal(byz.rc, byz.r1self)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	victim := c.Voter("cloud-0")
	victim.barrierPassed = true
	before := len(victim.legs)
	victim.ingestLeg(leg, byz.Node)
	victim.ingestLeg(leg, byz.Node) // duplicate
	if got := len(victim.legs) - before; got != 1 {
		t.Fatalf("duplicate leg counted %d times, want 1", got)
	}
}

// TestSafety_RogueAndForgedLegs_Rejected — legs from an unregistered key, a
// spoofed party index, or a forged proof-of-possession are all rejected before
// counting toward quorum.
func TestSafety_RogueAndForgedLegs_Rejected(t *testing.T) {
	c := NewCluster(7)
	block := c.NewBlock("cloud-0", 0, []PlacementOp{assignOp("shard-A", "cloud-0")})
	c.resetAll()
	c.deliverProposals(func(*Voter) *PlacementBlock { return block })
	c.barrierPhase()

	victim := c.Voter("cloud-0")
	victim.barrierPassed = true
	honest := c.Voter("cloud-3")
	goodLeg, err := honest.signer.Reveal(honest.rc, honest.r1self)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}

	// 1. Rogue / unregistered author.
	rogue := goodLeg
	rogue.Author = NodeIDFromName("attacker")
	// 2. Party-index spoof: honest author, wrong claimed index.
	spoof := goodLeg
	spoof.PartyID = honest.Index + 1
	// 3. Forged proof-of-possession: honest author + index, tampered sig.
	forged := goodLeg
	forged.AuthSig = append([]byte(nil), goodLeg.AuthSig...)
	forged.AuthSig[0] ^= 0xFF

	for name, leg := range map[string]pulsarlib.Partial{"rogue": rogue, "index-spoof": spoof, "forged-pop": forged} {
		before := len(victim.legs)
		victim.ingestLeg(leg, leg.Author)
		if len(victim.legs) != before {
			t.Fatalf("%s leg was accepted", name)
		}
	}

	// The genuine leg IS accepted — the gate is not simply rejecting everything.
	before := len(victim.legs)
	victim.ingestLeg(goodLeg, goodLeg.Author)
	if len(victim.legs) != before+1 {
		t.Fatal("genuine leg was rejected")
	}
}
