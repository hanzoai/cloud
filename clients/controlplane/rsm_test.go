//go:build controlplane

package controlplane

import (
	"bytes"
	"testing"
)

// TestRSM_ContiguousOrderingEnforced — the RSM applies strictly one height at a
// time: no gaps, no replays.
func TestRSM_ContiguousOrderingEnforced(t *testing.T) {
	rsm := NewPlacementRSM(NewMemControlDB())
	b1 := &PlacementBlock{Height: 1, Ops: []PlacementOp{assignOp("s", "a")}}
	if err := rsm.Apply(b1); err != nil {
		t.Fatalf("apply height 1: %v", err)
	}
	if rsm.Height() != 1 {
		t.Fatalf("height = %d, want 1", rsm.Height())
	}
	// Replay height 1 -> refused.
	if err := rsm.Apply(b1); err == nil {
		t.Fatal("replay of height 1 must be refused")
	}
	// Gap to height 3 -> refused.
	if err := rsm.Apply(&PlacementBlock{Height: 3, Ops: []PlacementOp{assignOp("t", "b")}}); err == nil {
		t.Fatal("gap to height 3 must be refused")
	}
	// Contiguous height 2 -> admitted.
	if err := rsm.Apply(&PlacementBlock{Height: 2, Ops: []PlacementOp{assignOp("t", "b")}}); err != nil {
		t.Fatalf("apply height 2: %v", err)
	}
}

// TestRSM_FailSecureOnInvalidOps — even reaching Apply, a block whose ops
// violate an invariant is refused and leaves state untouched (defense in depth:
// the policy gate is re-run at apply, not trusted from consensus).
func TestRSM_FailSecureOnInvalidOps(t *testing.T) {
	rsm := NewPlacementRSM(NewMemControlDB())
	if err := rsm.Apply(&PlacementBlock{Height: 1, Ops: []PlacementOp{assignOp("shard-X", "cloud-1")}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	pre := rsm.StateHash()
	bad := &PlacementBlock{Height: 2, Ops: []PlacementOp{{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-2"}}}
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
// block sequence reach byte-identical state.
func TestRSM_DeterministicConvergence(t *testing.T) {
	seq := []*PlacementBlock{
		{Height: 1, Ops: []PlacementOp{assignOp("a", "n0")}},
		{Height: 2, Ops: []PlacementOp{assignOp("b", "n1")}},
		{Height: 3, Ops: []PlacementOp{{Kind: OpReleaseLease, Resource: "a", Holder: "n0"}}},
	}
	r1 := NewPlacementRSM(NewMemControlDB())
	r2 := NewPlacementRSM(NewMemControlDB())
	for _, b := range seq {
		if err := r1.Apply(b); err != nil {
			t.Fatalf("r1 apply height %d: %v", b.Height, err)
		}
	}
	for _, b := range seq {
		if err := r2.Apply(b); err != nil {
			t.Fatalf("r2 apply height %d: %v", b.Height, err)
		}
	}
	if !bytes.Equal(r1.StateHash(), r2.StateHash()) {
		t.Fatal("independent RSMs diverged on the same sequence")
	}
	// The release removed the shard writer for "a".
	if _, held := r1.State().ShardWriter["a"]; held {
		t.Fatal("release did not clear the shard writer")
	}
}
