//go:build controlplane

package controlplane

import (
	"bytes"
	"testing"
)

// assignOp is a simple valid op used across the happy/liveness paths.
func assignOp(shard, holder string) PlacementOp {
	return PlacementOp{Kind: OpAssignLease, Resource: shard, Holder: holder, LeaseID: holder + ":L1"}
}

// TestHappyPath_SevenHonest — 7 honest voters reach a valid aggregated cert and
// every voter independently applies the block, converging byte-for-byte.
func TestHappyPath_SevenHonest(t *testing.T) {
	c := NewCluster(7)
	if c.Quorum() != 5 || c.FaultTolerance() != 2 {
		t.Fatalf("N=7 should be quorum=5 f=2, got quorum=%d f=%d", c.Quorum(), c.FaultTolerance())
	}

	res := c.RunHeight([]PlacementOp{assignOp("shard-A", "cloud-0")})
	if !res.Finalized {
		t.Fatal("7 honest voters must finalize")
	}
	if res.Voters != 7 {
		t.Fatalf("all 7 alive voters should finalize, got %d", res.Voters)
	}
	if res.Cert == nil || !res.Cert.Verify(nil) {
		t.Fatal("finalized cert must pass the triple-gate")
	}
	if res.Cert.Validators < c.Quorum() {
		t.Fatalf("cert signer count %d < quorum %d", res.Cert.Validators, c.Quorum())
	}

	// Every voter applied the op deterministically and converged.
	if !c.Converged() {
		t.Fatal("voters diverged after finalize")
	}
	want, _ := res.Cert.MarshalBinary()
	for _, v := range c.Voters() {
		if v.RSM().Height() != 1 {
			t.Fatalf("%s at height %d, want 1", v.Name, v.RSM().Height())
		}
		if got := v.RSM().State().ShardWriter["shard-A"]; got != "cloud-0" {
			t.Fatalf("%s shard-A writer = %q, want cloud-0", v.Name, got)
		}
		// All voters independently composed the identical cert (no trusted
		// aggregator).
		got, _ := v.Finalized().Cert.MarshalBinary()
		if !bytes.Equal(got, want) {
			t.Fatalf("%s composed a divergent cert", v.Name)
		}
	}
}

// TestLiveness_DropTwo_StillFinalizes — dropping 2 of 7 leaves exactly the
// quorum (5); finality still completes.
func TestLiveness_DropTwo_StillFinalizes(t *testing.T) {
	c := NewCluster(7)
	c.Kill("cloud-1")
	c.Kill("cloud-2")

	res := c.RunHeight([]PlacementOp{assignOp("shard-A", "cloud-0")})
	if !res.Finalized {
		t.Fatal("5 alive == quorum, must finalize")
	}
	if res.Voters != 5 {
		t.Fatalf("exactly the 5 alive voters finalize, got %d", res.Voters)
	}
	if !c.Converged() {
		t.Fatal("alive voters diverged")
	}
	// The 2 dropped voters never advanced.
	for _, name := range []string{"cloud-1", "cloud-2"} {
		if h := c.Voter(name).RSM().Height(); h != 0 {
			t.Fatalf("dropped %s advanced to height %d", name, h)
		}
	}
}

// TestLiveness_DropThree_SafeHalt — dropping 3 of 7 falls below quorum. The
// ceremony SAFE-HALTS: no forced finalize, no crash, no state mutation.
func TestLiveness_DropThree_SafeHalt(t *testing.T) {
	c := NewCluster(7)
	c.Kill("cloud-1")
	c.Kill("cloud-2")
	c.Kill("cloud-3")

	res := c.RunHeight([]PlacementOp{assignOp("shard-A", "cloud-0")})
	if res.Finalized {
		t.Fatal("4 alive < quorum 5 — must SAFE HALT, never a forced finalize")
	}
	// No voter — alive or dead — mutated state.
	for _, v := range c.Voters() {
		if v.RSM().Height() != 0 {
			t.Fatalf("%s finalized under a sub-quorum (height %d)", v.Name, v.RSM().Height())
		}
		if v.Finalized() != nil {
			t.Fatalf("%s forced a finalize under a sub-quorum", v.Name)
		}
	}
}

// TestMultiHeight_Sequential — the RSM advances across several heights, applying
// each certified block in Finalized() order.
func TestMultiHeight_Sequential(t *testing.T) {
	c := NewCluster(7)
	for i, op := range []PlacementOp{
		assignOp("shard-A", "cloud-0"),
		assignOp("shard-B", "cloud-1"),
		{Kind: OpSetMembership, Node: "cloud-9", Member: true},
	} {
		res := c.RunHeight([]PlacementOp{op})
		if !res.Finalized {
			t.Fatalf("height %d must finalize", i+1)
		}
		if res.Block.Height != uint64(i+1) {
			t.Fatalf("finalized height %d, want %d", res.Block.Height, i+1)
		}
	}
	st := c.Voter("cloud-0").RSM().State()
	if st.ShardWriter["shard-A"] != "cloud-0" || st.ShardWriter["shard-B"] != "cloud-1" {
		t.Fatal("multi-height state not accumulated")
	}
	if !st.Membership["cloud-9"] {
		t.Fatal("membership op not applied")
	}
	if !c.Converged() || c.Voter("cloud-0").RSM().Height() != 3 {
		t.Fatal("voters not converged at height 3")
	}
}
