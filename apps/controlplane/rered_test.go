//go:build controlplane

package controlplane

import "testing"

// Re-red adversarial verification of the blue fix pass. The original red found
// the double-write class and its three class-A siblings; blue closed them. These
// tests attack the FIXES themselves, hunting for a residual path to two live
// writers — in particular via op COMPOSITION inside a single block, which the
// original exploit suite exercised only as separate blocks or via one op. They
// state the class-A closure as an end-to-end invariant, not just a unit gate.

// liveWriterCluster finalizes a first height that makes `writer` the live writer
// of `shard`, returning the running N=7 cluster. No node is proven dead.
func liveWriterCluster(t *testing.T, shard, writer string) *Cluster {
	t.Helper()
	c := NewCluster(7)
	if res := c.RunHeight([]PlacementOp{assignOp(shard, writer)}); !res.Finalized {
		t.Fatalf("setup height must finalize")
	}
	for _, v := range c.Voters() {
		if got := v.RSM().State().ShardWriter[shard]; got != writer {
			t.Fatalf("%s: setup writer = %q, want %q", v.Name, got, writer)
		}
	}
	return c
}

// assertStillSoleLiveWriter checks that every honest voter still records exactly
// `writer` on `shard` and that no impostor was seated — the double-write
// invariant, checked across the whole converged voter set.
func assertStillSoleLiveWriter(t *testing.T, c *Cluster, shard, writer, impostor string) {
	t.Helper()
	for _, v := range c.Voters() {
		st := v.RSM().State()
		if got := st.ShardWriter[shard]; got != writer {
			t.Fatalf("%s: shard %q writer = %q, want %q (double-write / displacement)", v.Name, shard, got, writer)
		}
		// The Leases mirror must agree — a desync would be a second authority.
		if l, held := st.Leases[shard]; !held || l.Holder != writer {
			t.Fatalf("%s: lease mirror for %q = %+v held=%v, want holder %q", v.Name, shard, l, held, writer)
		}
		if st.ShardWriter[shard] == impostor {
			t.Fatalf("%s: impostor %q seated on %q", v.Name, impostor, shard)
		}
	}
}

// TestRered_NoDoubleWriter_SingleBlockCompositions — the crown-jewel end-to-end
// property. Through the FULL byzantine ceremony (not just CheckInvariants), NO
// single-block op composition can displace or shadow a LIVE writer. Every hostile
// composition an honest proposer could pack into one block is refused, and the
// live writer is unchanged across all voters.
func TestRered_NoDoubleWriter_SingleBlockCompositions(t *testing.T) {
	const shard, live, impostor = "shard-X", "cloud-1", "cloud-2"

	hostile := map[string][]PlacementOp{
		"bare-reassign-live": {
			{Kind: OpReassignShardWriter, Resource: shard, From: live, To: impostor},
		},
		"release-then-reassign": {
			{Kind: OpReleaseLease, Resource: shard, Holder: live, LeaseID: live + ":L1"},
			{Kind: OpReassignShardWriter, Resource: shard, From: live, To: impostor},
		},
		"release-then-assign": {
			{Kind: OpReleaseLease, Resource: shard, Holder: live, LeaseID: live + ":L1"},
			assignOp(shard, impostor),
		},
		"membership-remove-then-reassign": {
			{Kind: OpSetMembership, Node: live, Member: false},
			{Kind: OpReassignShardWriter, Resource: shard, From: live, To: impostor},
		},
		"membership-remove-then-release-then-assign": {
			{Kind: OpSetMembership, Node: live, Member: false},
			{Kind: OpReleaseLease, Resource: shard, Holder: live, LeaseID: live + ":L1"},
			assignOp(shard, impostor),
		},
		"assign-steal-live": {
			assignOp(shard, impostor),
		},
	}

	for name, ops := range hostile {
		t.Run(name, func(t *testing.T) {
			c := liveWriterCluster(t, shard, live)
			if res := c.RunHeight(ops); res.Finalized {
				t.Fatalf("hostile composition %q finalized — a live writer was displaced", name)
			}
			assertStillSoleLiveWriter(t, c, shard, live, impostor)
			// No voter advanced past the setup height.
			for _, v := range c.Voters() {
				if v.RSM().Height() != 1 {
					t.Fatalf("%s advanced to height %d on a refused block", v.Name, v.RSM().Height())
				}
			}
		})
	}
}

// TestRered_ProvenDeadHandoffIsSingleValued — once the out-of-band failure
// detector proves the predecessor dead, a block that reassigns the SAME shard
// twice (an adversarial proposer packing redundant reassigns) still yields a
// single-valued writer: the state map holds exactly one holder, never two. This
// proves apply-ordering under the pre-state check can never manufacture a second
// concurrent writer even on the authorized path.
func TestRered_ProvenDeadHandoffIsSingleValued(t *testing.T) {
	rsm := NewPlacementRSM(NewMemControlDB())
	if err := rsm.Apply(extend(rsm, []PlacementOp{assignOp("shard-X", "cloud-1")})); err != nil {
		t.Fatalf("setup: %v", err)
	}
	rsm.State().ProvenDead["cloud-1"] = true // out-of-band proof of death

	b := extend(rsm, []PlacementOp{
		{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-2"},
		{Kind: OpReassignShardWriter, Resource: "shard-X", From: "cloud-1", To: "cloud-3"},
	})
	if err := rsm.Apply(b); err != nil {
		t.Fatalf("authorized double-reassign of a dead predecessor should apply, got %v", err)
	}
	st := rsm.State()
	w, held := st.ShardWriter["shard-X"]
	if !held {
		t.Fatal("shard lost its writer after handoff")
	}
	if w != "cloud-3" {
		t.Fatalf("writer = %q, want last-write-wins cloud-3", w)
	}
	// The lease mirror must equal the single writer — no residual second authority.
	if l := st.Leases["shard-X"]; l.Holder != "cloud-3" {
		t.Fatalf("lease mirror = %q, want cloud-3 (mirror desync = second authority)", l.Holder)
	}
}

// TestRered_AssignThenReleaseSameBlock_NoOrphanWriter — assigning then releasing
// the same fresh resource in one block leaves NO dangling writer: apply ordering
// is transactional and the mirror maps end consistent (both empty). Guards
// against a release path that clears Leases but strands ShardWriter.
func TestRered_AssignThenReleaseSameBlock_NoOrphanWriter(t *testing.T) {
	rsm := NewPlacementRSM(NewMemControlDB())
	b := extend(rsm, []PlacementOp{
		assignOp("res", "cloud-1"),
		{Kind: OpReleaseLease, Resource: "res", Holder: "cloud-1", LeaseID: "cloud-1:L1"},
	})
	if err := rsm.Apply(b); err != nil {
		t.Fatalf("assign+release of a fresh resource should apply, got %v", err)
	}
	st := rsm.State()
	if w, held := st.ShardWriter["res"]; held {
		t.Fatalf("orphan shard writer %q after assign+release (mirror desync)", w)
	}
	if _, held := st.Leases["res"]; held {
		t.Fatal("orphan lease after assign+release")
	}
}
