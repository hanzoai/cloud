//go:build controlplane

package controlplane

import (
	"errors"
	"testing"
)

// shardState builds a pre-state where `shard` is written by `writer` under a
// held lease.
func shardState(shard, writer string) *PlacementState {
	s := NewPlacementState()
	s.ShardWriter[shard] = writer
	s.Leases[shard] = Lease{Holder: writer, LeaseID: writer + ":writer"}
	s.Membership[writer] = true
	return s
}

// TestPolicy_ShardReassign_Unauthorized — reassigning a LIVE shard writer with
// neither a lease-release nor a proven-dead predecessor is refused. This is the
// load-bearing invariant: it prevents two live pods writing one secret shard.
func TestPolicy_ShardReassign_Unauthorized(t *testing.T) {
	pre := shardState("shard-X", "cloud-1")
	b := &PlacementBlock{Ops: []PlacementOp{{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-2"}}}
	if err := CheckInvariants(pre, b); !errors.Is(err, ErrShardReassignUnauthorized) {
		t.Fatalf("want ErrShardReassignUnauthorized, got %v", err)
	}
}

// TestPolicy_ShardReassign_WithRelease — a reassign authorized by a lease-release
// of the predecessor in the same block is admitted.
func TestPolicy_ShardReassign_WithRelease(t *testing.T) {
	pre := shardState("shard-X", "cloud-1")
	b := &PlacementBlock{Ops: []PlacementOp{
		{Kind: OpReleaseLease, Resource: "shard-X", Holder: "cloud-1", LeaseID: "cloud-1:writer"},
		{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-2"},
	}}
	if err := CheckInvariants(pre, b); err != nil {
		t.Fatalf("release+reassign should be admitted, got %v", err)
	}
}

// TestPolicy_ShardReassign_ProvenDead — a reassign of a proven-dead predecessor
// is admitted (the graceful release path is impossible for a dead node).
func TestPolicy_ShardReassign_ProvenDead(t *testing.T) {
	pre := shardState("shard-X", "cloud-1")
	pre.ProvenDead["cloud-1"] = true
	b := &PlacementBlock{Ops: []PlacementOp{{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-2"}}}
	if err := CheckInvariants(pre, b); err != nil {
		t.Fatalf("proven-dead reassign should be admitted, got %v", err)
	}
}

// TestPolicy_ShardReassign_StalePredecessor — a reassign naming a writer other
// than the shard's actual current writer is refused (forged handoff).
func TestPolicy_ShardReassign_StalePredecessor(t *testing.T) {
	pre := shardState("shard-X", "cloud-1")
	pre.ProvenDead["cloud-9"] = true // even proven-dead, cloud-9 is not the writer
	b := &PlacementBlock{Ops: []PlacementOp{{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-9", To: "cloud-2"}}}
	if err := CheckInvariants(pre, b); !errors.Is(err, ErrShardReassignStalePredecessor) {
		t.Fatalf("want ErrShardReassignStalePredecessor, got %v", err)
	}
}

// TestPolicy_LeaseSteal_Refused — assigning a resource held by a different live
// holder without releasing it is refused; releasing another holder's lease is
// refused.
func TestPolicy_LeaseSteal_Refused(t *testing.T) {
	pre := shardState("res", "cloud-1")
	steal := &PlacementBlock{Ops: []PlacementOp{{Kind: OpAssignLease, Resource: "res", Holder: "cloud-2", LeaseID: "L2"}}}
	if err := CheckInvariants(pre, steal); !errors.Is(err, ErrLeaseSteal) {
		t.Fatalf("want ErrLeaseSteal, got %v", err)
	}
	forged := &PlacementBlock{Ops: []PlacementOp{{Kind: OpReleaseLease, Resource: "res", Holder: "cloud-2"}}}
	if err := CheckInvariants(pre, forged); !errors.Is(err, ErrForgedRelease) {
		t.Fatalf("want ErrForgedRelease, got %v", err)
	}
}

// TestPolicy_HonestVotersRefuseInvariantViolation — end to end, honest voters
// refuse to sign an invariant-violating block, so it never reaches quorum and
// the protected state is unchanged.
func TestPolicy_HonestVotersRefuseInvariantViolation(t *testing.T) {
	c := NewCluster(7)
	// Height 1: legitimately make cloud-1 the writer of shard-X.
	if res := c.RunHeight([]PlacementOp{assignOp("shard-X", "cloud-1")}); !res.Finalized {
		t.Fatal("setup height must finalize")
	}
	// Height 2: an unauthorized reassign of the LIVE writer.
	res := c.RunHeight([]PlacementOp{{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-2"}})
	if res.Finalized {
		t.Fatal("honest voters must refuse an invariant-violating reassign")
	}
	// The shard writer is unchanged and no voter advanced past height 1.
	for _, v := range c.Voters() {
		if got := v.RSM().State().ShardWriter["shard-X"]; got != "cloud-1" {
			t.Fatalf("%s shard-X writer changed to %q", v.Name, got)
		}
		if v.RSM().Height() != 1 {
			t.Fatalf("%s advanced past the refused block (height %d)", v.Name, v.RSM().Height())
		}
	}
}
