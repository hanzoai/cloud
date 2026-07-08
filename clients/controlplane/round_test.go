//go:build controlplane

package controlplane

import "testing"

// TestRoundContext_DeterministicAndBinding — the same block under the same
// voter set yields the same digest and session; a block differing in any op
// yields a different digest AND session (the equivocation / replay binding).
func TestRoundContext_DeterministicAndBinding(t *testing.T) {
	cfg := DefaultRoundConfig()
	pool := newNoncePool([]byte("t"), 8)
	voters := []string{"a", "b", "c"}

	b1 := &PlacementBlock{Height: 1, Round: 0, Proposer: "a", Ops: []PlacementOp{assignOp("s", "a")}}
	rc1, err := NewRoundContext(cfg, b1, voters, pool)
	if err != nil {
		t.Fatalf("round context: %v", err)
	}
	rc1b, _ := NewRoundContext(cfg, b1, voters, pool)
	if rc1.Digest != rc1b.Digest || rc1.SessionID != rc1b.SessionID {
		t.Fatal("round context not deterministic")
	}

	// Different ops -> different payload root -> different digest + session.
	b2 := &PlacementBlock{Height: 1, Round: 0, Proposer: "a", Ops: []PlacementOp{assignOp("s", "b")}}
	rc2, _ := NewRoundContext(cfg, b2, voters, pool)
	if rc1.Digest == rc2.Digest {
		t.Fatal("distinct ops must produce distinct digests")
	}
	if rc1.SessionID == rc2.SessionID {
		t.Fatal("distinct ops must produce distinct sessions (equivocation binding)")
	}

	// Different round -> different digest + session (stale-round replay defense).
	b3 := &PlacementBlock{Height: 1, Round: 1, Proposer: "a", Ops: []PlacementOp{assignOp("s", "a")}}
	rc3, _ := NewRoundContext(cfg, b3, voters, pool)
	if rc1.Digest == rc3.Digest || rc1.SessionID == rc3.SessionID {
		t.Fatal("distinct rounds must produce distinct digest/session")
	}
}

// TestRoundContext_RefusesZeroSecurityField — ComputeRoundDigest fails closed on
// a zero security-relevant field (here, a zero network id), so the ceremony can
// never sign under a degenerate digest.
func TestRoundContext_RefusesZeroSecurityField(t *testing.T) {
	cfg := DefaultRoundConfig()
	cfg.NetworkID = 0
	pool := newNoncePool([]byte("t"), 4)
	b := &PlacementBlock{Height: 1, Round: 0, Proposer: "a", Ops: []PlacementOp{assignOp("s", "a")}}
	if _, err := NewRoundContext(cfg, b, []string{"a", "b"}, pool); err == nil {
		t.Fatal("zero network id must be refused")
	}
}

// TestRoundContext_EmptyNoncePoolRefused — an empty background nonce pool is a
// fail-closed condition (the real signer would fall back to Corona).
func TestRoundContext_EmptyNoncePoolRefused(t *testing.T) {
	cfg := DefaultRoundConfig()
	pool := newNoncePool([]byte("t"), 0)
	b := &PlacementBlock{Height: 1, Round: 0, Proposer: "a", Ops: []PlacementOp{assignOp("s", "a")}}
	if _, err := NewRoundContext(cfg, b, []string{"a", "b"}, pool); err == nil {
		t.Fatal("empty nonce pool must be refused")
	}
}

// TestBFTQuorum — the >2/3 quorum and fault tolerance across sizes.
func TestBFTQuorum(t *testing.T) {
	cases := []struct{ n, quorum, f int }{
		{1, 1, 0},
		{3, 3, 0},
		{4, 3, 1},
		{7, 5, 2},
		{10, 7, 3},
		{100, 67, 33},
	}
	for _, tc := range cases {
		if q := bftQuorum(tc.n); q != tc.quorum {
			t.Errorf("bftQuorum(%d) = %d, want %d", tc.n, q, tc.quorum)
		}
		if f := bftFaultTolerance(tc.n); f != tc.f {
			t.Errorf("bftFaultTolerance(%d) = %d, want %d", tc.n, f, tc.f)
		}
		// Two quorums must intersect in > f voters (safety).
		if 2*tc.quorum <= tc.n {
			t.Errorf("N=%d: 2*quorum=%d <= N — quorums do not intersect", tc.n, 2*tc.quorum)
		}
	}
}
