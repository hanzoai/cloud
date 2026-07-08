//go:build controlplane

package controlplane

import (
	"errors"
	"fmt"
)

// Policy-gate errors. Each names the exact invariant a block violated so an
// honest voter's refusal (and the RSM's fail-secure rejection) is auditable.
var (
	// ErrShardReassignUnauthorized is the load-bearing control-plane invariant:
	// a secret-shard writer may not be reassigned unless its predecessor is
	// PROVEN DEAD by out-of-band evidence. Moving a LIVE writer would let two
	// pods write one secret shard, collapsing KMS share isolation.
	//
	// A same-block lease-release does NOT authorize displacing a live writer: a
	// release op is written by the (untrusted) proposer and carries no
	// authorization from the departing holder, so treating it as consent is a
	// forged-handoff double-write (red_exploits_test #1). Authenticated graceful
	// handoff (a holder self-resignation) and the KMS write-fence are increment-2
	// and require the real identity / KMS-revocation layer; increment-1 fails
	// closed — only proven death displaces a live writer.
	ErrShardReassignUnauthorized = errors.New("controlplane: shard-writer reassign of a live (not-proven-dead) predecessor")
	// ErrShardReassignStalePredecessor rejects a reassign that names a writer
	// other than the shard's actual current writer — a forged handoff.
	ErrShardReassignStalePredecessor = errors.New("controlplane: shard-writer reassign names a non-current writer")
	// ErrLeaseSteal rejects assigning a resource currently held by a different,
	// live (not-proven-dead) holder.
	ErrLeaseSteal = errors.New("controlplane: assign over a live lease")
	// ErrForgedRelease rejects releasing a lease not held by the named holder.
	ErrForgedRelease = errors.New("controlplane: release of a lease not held by the named holder")
	// ErrUnauthorizedRelease rejects releasing a LIVE (not-proven-dead) holder's
	// lease. A proposer-written release of a live holder would strand it and free
	// the shard for a second writer (the release+assign sibling of the forged-
	// release double-write). Only a proven-dead holder's lease is releasable in
	// increment-1; authenticated self-release is increment-2.
	ErrUnauthorizedRelease = errors.New("controlplane: release of a live (not-proven-dead) holder's lease")
	// ErrEmptyReassignTarget rejects a reassign with an empty or self target.
	ErrEmptyReassignTarget = errors.New("controlplane: shard-writer reassign has empty or self target")
)

// isProvenDead reports whether a node is proven dead in the pre-state. Only an
// explicit, evidence-set ProvenDead flag counts. Non-membership is NOT proof of
// death: an evidence-free OpSetMembership removal must not manufacture the sole
// authorization for seizing a live node's shard (red_exploits_test #2). On the
// increment-1 proposer-writable path NOTHING sets ProvenDead — it is set only
// out-of-band by a failure-detector proof (not yet wired), so a live writer's
// shard is immutable through consensus. This is the fail-closed posture.
func isProvenDead(pre *PlacementState, node string) bool {
	return pre.ProvenDead[node]
}

// CheckInvariants is the SINGLE control-plane policy gate. It validates a whole
// block against the pre-block state (the state the voter/RSM holds before the
// block). It is called in two places — an honest voter runs it BEFORE signing
// (Round1), and the RSM runs it again BEFORE apply — so the same rule enforces
// consensus admission and fail-secure application. A nil return means every op
// is admissible; a non-nil error names the first violation.
//
// Checks run against the pre-state (not incrementally mutated).
func CheckInvariants(pre *PlacementState, b *PlacementBlock) error {
	if b == nil {
		return errNilBlock
	}
	for i, op := range b.Ops {
		switch op.Kind {
		case OpReassignShardWriter:
			if op.To == "" || op.To == op.From {
				return fmt.Errorf("%w: op[%d] shard=%q from=%q to=%q", ErrEmptyReassignTarget, i, op.Resource, op.From, op.To)
			}
			cur, held := pre.ShardWriter[op.Resource]
			if !held || cur != op.From {
				return fmt.Errorf("%w: op[%d] shard=%q from=%q current=%q", ErrShardReassignStalePredecessor, i, op.Resource, op.From, cur)
			}
			// A live writer may only be displaced with proof of death. A
			// same-block release is deliberately NOT consulted here: the
			// proposer wrote it, not the holder, so it is not consent.
			if !isProvenDead(pre, op.From) {
				return fmt.Errorf("%w: op[%d] shard=%q from=%q", ErrShardReassignUnauthorized, i, op.Resource, op.From)
			}

		case OpAssignLease:
			cur, held := pre.Leases[op.Resource]
			if held && cur.Holder != op.Holder && !isProvenDead(pre, cur.Holder) {
				return fmt.Errorf("%w: op[%d] resource=%q current=%q new=%q", ErrLeaseSteal, i, op.Resource, cur.Holder, op.Holder)
			}

		case OpReleaseLease:
			cur, held := pre.Leases[op.Resource]
			if held && cur.Holder != op.Holder {
				return fmt.Errorf("%w: op[%d] resource=%q holder=%q current=%q", ErrForgedRelease, i, op.Resource, op.Holder, cur.Holder)
			}
			// A LIVE holder's lease is immutable: releasing it would strand the
			// holder and free the shard for a second live writer (release+assign
			// sibling of the forged-release double-write). Only a proven-dead
			// holder's lease is releasable in increment-1.
			if held && cur.Holder == op.Holder && !isProvenDead(pre, op.Holder) {
				return fmt.Errorf("%w: op[%d] resource=%q holder=%q", ErrUnauthorizedRelease, i, op.Resource, op.Holder)
			}

		case OpSetMembership:
			// Governance-level; not gated in increment 1. Critically, removal
			// does NOT mark the node proven-dead (see placement.go apply), so a
			// removal cannot manufacture shard-reassign authorization.
		}
	}
	return nil
}
