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
	// a secret-shard writer may not be reassigned unless its predecessor either
	// released the write lease in the same block or is proven dead. Moving a
	// LIVE writer without a release would let two pods write one secret shard,
	// collapsing KMS share isolation.
	ErrShardReassignUnauthorized = errors.New("controlplane: shard-writer reassign without lease-release or proven-dead predecessor")
	// ErrShardReassignStalePredecessor rejects a reassign that names a writer
	// other than the shard's actual current writer — a forged handoff.
	ErrShardReassignStalePredecessor = errors.New("controlplane: shard-writer reassign names a non-current writer")
	// ErrLeaseSteal rejects assigning a resource that is currently held by a
	// different, live holder without releasing it first.
	ErrLeaseSteal = errors.New("controlplane: assign over a live lease without release")
	// ErrForgedRelease rejects releasing a lease not held by the named holder.
	ErrForgedRelease = errors.New("controlplane: release of a lease not held by the named holder")
	// ErrEmptyReassignTarget rejects a reassign with an empty or self target.
	ErrEmptyReassignTarget = errors.New("controlplane: shard-writer reassign has empty or self target")
)

// isProvenDead reports whether a node is proven dead in the pre-state: either
// explicitly proven dead or a non-member (a removed node cannot hold a live
// write lease).
func isProvenDead(pre *PlacementState, node string) bool {
	if pre.ProvenDead[node] {
		return true
	}
	if member, ok := pre.Membership[node]; ok && !member {
		return true
	}
	return false
}

// blockReleases reports whether the block contains a release of `holder`'s
// lease on `resource` — the authorizing lease-release for a graceful handoff.
func blockReleases(b *PlacementBlock, resource, holder string) bool {
	for _, op := range b.Ops {
		if op.Kind == OpReleaseLease && op.Resource == resource && op.Holder == holder {
			return true
		}
	}
	return false
}

// CheckInvariants is the SINGLE control-plane policy gate. It validates a whole
// block against the pre-block state (the state the voter/RSM holds before the
// block). It is called in two places — an honest voter runs it BEFORE signing
// (Round1), and the RSM runs it again BEFORE apply — so the same rule enforces
// consensus admission and fail-secure application. A nil return means every op
// is admissible; a non-nil error names the first violation.
//
// Checks run against the pre-state (not incrementally mutated), so a block that
// pairs a release with a reassign of the same shard is coherent: the pre-state
// still shows the predecessor as the writer, and the release in the block
// authorizes the handoff.
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
			if !isProvenDead(pre, op.From) && !blockReleases(b, op.Resource, op.From) {
				return fmt.Errorf("%w: op[%d] shard=%q from=%q", ErrShardReassignUnauthorized, i, op.Resource, op.From)
			}

		case OpAssignLease:
			cur, held := pre.Leases[op.Resource]
			if held && cur.Holder != op.Holder && !isProvenDead(pre, cur.Holder) && !blockReleases(b, op.Resource, cur.Holder) {
				return fmt.Errorf("%w: op[%d] resource=%q current=%q new=%q", ErrLeaseSteal, i, op.Resource, cur.Holder, op.Holder)
			}

		case OpReleaseLease:
			cur, held := pre.Leases[op.Resource]
			if held && cur.Holder != op.Holder {
				return fmt.Errorf("%w: op[%d] resource=%q holder=%q current=%q", ErrForgedRelease, i, op.Resource, op.Holder, cur.Holder)
			}

		case OpSetMembership:
			// Membership changes are governance-level and not gated in
			// increment 1; a stranded shard is recovered via proven-dead
			// reassign, which the shard invariant above authorizes.
		}
	}
	return nil
}
