//go:build controlplane

package controlplane

import "fmt"

// PlacementRSM applies certified placement blocks into control-plane state in
// Finalized() order, deterministically. It is orthogonal to PlacementState
// (pure state + mutation): the RSM owns ordering, the fail-secure policy gate,
// and persistence.
//
// Every honest voter holds its own PlacementRSM and applies the SAME sequence
// of certified blocks, so their states converge byte-for-byte (compare via
// StateHash). A block only reaches Apply after the driver has independently
// verified its aggregated cert — Apply is the last gate, not the first.
type PlacementRSM struct {
	state  *PlacementState
	db     ControlDB
	height uint64 // last applied height (0 ⇒ none applied yet)
}

// NewPlacementRSM returns an empty RSM backed by db.
func NewPlacementRSM(db ControlDB) *PlacementRSM {
	return &PlacementRSM{state: NewPlacementState(), db: db}
}

// Apply admits one certified block. It enforces, in order:
//
//  1. Contiguous chain extension — the block sits at exactly the next height AND
//     its ParentRoot commits to the current applied state. Gaps, replays, and
//     blocks that do not extend local state are refused (deterministic total
//     order; no double-apply; no fork at the apply boundary — red_exploits #4).
//  2. The policy invariant gate — the SAME CheckInvariants an honest voter ran
//     before signing, re-run here fail-secure so a block that somehow carried a
//     valid cert over invalid ops still never mutates state.
//  3. Transactional mutation + persistence — state is cloned, mutated, and
//     persisted before the RSM advances; a persistence failure leaves memory
//     and disk consistent (no divergence).
//
// The caller is responsible for having verified the block's aggregated cert;
// Apply never trusts an unverified block (the driver enforces that upstream).
func (r *PlacementRSM) Apply(b *PlacementBlock) error {
	if b == nil {
		return errNilBlock
	}
	if b.Height != r.height+1 {
		return fmt.Errorf("controlplane: out-of-order apply height=%d, want %d", b.Height, r.height+1)
	}
	// Chain-extension gate: the block must commit to THIS RSM's current state.
	// OnProposal checks this too, but Apply is the LAST gate and the one a future
	// recovery/gossip path will feed, so it must not trust height alone — a block
	// that does not extend local state must never mutate it (red_exploits #4).
	var parent [32]byte
	copy(parent[:], r.state.Snapshot())
	if b.ParentRoot != parent {
		return fmt.Errorf("controlplane: apply parent-root mismatch height=%d (block does not extend local state)", b.Height)
	}
	if err := CheckInvariants(r.state, b); err != nil {
		return err // fail-secure: reject invalid ops even under a valid cert
	}
	next := r.state.clone()
	next.apply(b)
	if err := r.db.Persist(b.Height, next.Snapshot()); err != nil {
		return err // fail-secure: do not advance memory ahead of durable state
	}
	r.state = next
	r.height = b.Height
	return nil
}

// Height is the last applied height (0 if none).
func (r *PlacementRSM) Height() uint64 { return r.height }

// State returns the live state. Callers must treat it as read-only.
func (r *PlacementRSM) State() *PlacementState { return r.state }

// StateHash is the canonical state commitment for cross-voter convergence
// checks.
func (r *PlacementRSM) StateHash() []byte { return r.state.Snapshot() }
