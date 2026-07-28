//go:build controlplane

package controlplane

import (
	"bytes"
	"testing"
)

// extend builds a block at the RSM's next height that correctly extends its
// current applied state — i.e. carries the ParentRoot the chain-extension gate
// now requires. Tests that exercise ordering/policy (not the parent-root gate
// itself) use this so their blocks are well-formed extensions.
func extend(r *PlacementRSM, ops []PlacementOp) *PlacementBlock {
	var parent [32]byte
	copy(parent[:], r.StateHash())
	return &PlacementBlock{Height: r.Height() + 1, ParentRoot: parent, Ops: ops}
}

// TestRSM_ContiguousOrderingEnforced — the RSM applies strictly one height at a
// time: no gaps, no replays.
func TestRSM_ContiguousOrderingEnforced(t *testing.T) {
	rsm := NewPlacementRSM(NewMemControlDB())
	b1 := extend(rsm, []PlacementOp{assignOp("s", "a")})
	if err := rsm.Apply(b1); err != nil {
		t.Fatalf("apply height 1: %v", err)
	}
	if rsm.Height() != 1 {
		t.Fatalf("height = %d, want 1", rsm.Height())
	}
	// Replay height 1 -> refused (height gate).
	if err := rsm.Apply(b1); err == nil {
		t.Fatal("replay of height 1 must be refused")
	}
	// Gap to height 3 -> refused (height gate).
	if err := rsm.Apply(&PlacementBlock{Height: 3, Ops: []PlacementOp{assignOp("t", "b")}}); err == nil {
		t.Fatal("gap to height 3 must be refused")
	}
	// Contiguous height 2 that extends current state -> admitted.
	if err := rsm.Apply(extend(rsm, []PlacementOp{assignOp("t", "b")})); err != nil {
		t.Fatalf("apply height 2: %v", err)
	}
}

// TestRSM_FailSecureOnInvalidOps — even reaching Apply, a block whose ops
// violate an invariant is refused and leaves state untouched (defense in depth:
// the policy gate is re-run at apply, not trusted from consensus). The block is
// a valid chain extension (correct ParentRoot) so it is the OP gate, not the
// parent-root gate, that rejects it.
func TestRSM_FailSecureOnInvalidOps(t *testing.T) {
	rsm := NewPlacementRSM(NewMemControlDB())
	if err := rsm.Apply(extend(rsm, []PlacementOp{assignOp("shard-X", "cloud-1")})); err != nil {
		t.Fatalf("setup: %v", err)
	}
	pre := rsm.StateHash()
	bad := extend(rsm, []PlacementOp{{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-2"}})
	if err := rsm.Apply(bad); err == nil {
		t.Fatal("apply of invariant-violating block must fail")
	}
	if rsm.Height() != 1 {
		t.Fatalf("state advanced past a refused block (height %d)", rsm.Height())
	}
	if !bytes.Equal(rsm.StateHash(), pre) {
		t.Fatal("refused block mutated state (not fail-secure)")
	}
}

// TestRSM_DeterministicConvergence — two independent RSMs applying the same
// op sequence reach byte-identical state.
func TestRSM_DeterministicConvergence(t *testing.T) {
	build := func() *PlacementRSM {
		r := NewPlacementRSM(NewMemControlDB())
		if err := r.Apply(extend(r, []PlacementOp{assignOp("a", "n0")})); err != nil {
			t.Fatalf("apply a: %v", err)
		}
		if err := r.Apply(extend(r, []PlacementOp{assignOp("b", "n1")})); err != nil {
			t.Fatalf("apply b: %v", err)
		}
		// Out-of-band failure detector marks n0 dead, so its lease becomes
		// releasable (increment-1: a LIVE holder's lease is immutable). Both RSMs
		// apply the same out-of-band fact, so they still converge byte-for-byte.
		r.State().ProvenDead["n0"] = true
		if err := r.Apply(extend(r, []PlacementOp{{Kind: OpReleaseLease, Resource: "a", Holder: "n0"}})); err != nil {
			t.Fatalf("apply release: %v", err)
		}
		return r
	}
	r1 := build()
	r2 := build()
	if !bytes.Equal(r1.StateHash(), r2.StateHash()) {
		t.Fatal("independent RSMs diverged on the same sequence")
	}
	// The release removed the shard writer for "a".
	if _, held := r1.State().ShardWriter["a"]; held {
		t.Fatal("release did not clear the shard writer")
	}
}
