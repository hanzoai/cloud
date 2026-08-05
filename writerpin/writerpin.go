// Package writerpin abstracts WHO holds the single-writer pin — the exclusive
// right to open the RWO stores for write. It is the dynamic counterpart to the
// static role package: role says "I am configured as the writer"; writerpin says
// "I actually hold the pin right now, and here is when I lose it."
//
// Exactly one process in the cluster may hold the pin at a time. This is the
// mechanism that lets a reader be promoted to writer on writer loss WITHOUT ever
// permitting two concurrent writers (which would double-open the ZapDB/SQLite
// stores and corrupt the audit chain's in-memory head).
//
// Two implementations:
//
//   - SingleWriter (DEFAULT, PRODUCTION-CORRECT TODAY): the writer runs as a
//     StatefulSet with replicas:1 + Recreate, so Kubernetes already guarantees at
//     most one writer pod. The pin is therefore held immediately and never lost.
//     This is not a fake — it is the correct pin for the current topology.
//
//   - ConsensusPin (STUB, NOT WIRED): leaderless election over luxfi/consensus
//     (Quasar) so the pin survives writer loss and a reader can be promoted
//     without a human. It is deliberately unimplemented and fails closed — it
//     NEVER hands out a pin it cannot back with real agreement. The bootstrap
//     falls back to SingleWriter and the gap is reported, not faked.
//
// This package imports nothing from cloud, so it is free of import cycles and
// unit-testable in isolation.
package writerpin

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by Pins that are not yet wired. It exists so the
// caller can detect the stub and fall back to SingleWriter explicitly rather than
// silently assuming a pin was granted.
var ErrNotImplemented = errors.New("writerpin: consensus election not implemented (falls back to SingleWriter; k8s StatefulSet replicas:1 guarantees a single writer)")

// Held represents a currently-held writer pin. The holder MUST watch Lost() and
// stop all writes (close the RWO stores) the instant it fires — losing the pin
// means another process may become the writer.
type Held interface {
	// Lost is closed when the pin is lost (lease expiry, partition, eviction).
	// A SingleWriter pin never loses; its Lost channel stays open forever.
	Lost() <-chan struct{}

	// Release relinquishes the pin. Idempotent. After Release, the holder must
	// not write. Safe to call from a defer.
	Release()
}

// Pin is the election surface. Acquire blocks until this process holds the pin
// (or ctx is cancelled). A writer calls Acquire before opening the RWO stores; a
// promoted reader calls Acquire before flipping to writer mode.
type Pin interface {
	// Acquire blocks until the pin is held or ctx is done. On success the caller
	// is the sole writer until Held.Lost() fires or Held.Release() is called.
	Acquire(ctx context.Context) (Held, error)

	// Kind identifies the implementation for logs/metrics.
	Kind() string
}

// --- SingleWriter: the production-correct default for replicas:1 -------------

// SingleWriter grants the pin immediately and never revokes it, reflecting the
// Kubernetes guarantee that the writer StatefulSet has exactly one pod. Use this
// until consensus election is wired.
type SingleWriter struct{}

// NewSingleWriter returns the default single-writer pin.
func NewSingleWriter() *SingleWriter { return &SingleWriter{} }

func (*SingleWriter) Kind() string { return "single-writer" }

func (*SingleWriter) Acquire(ctx context.Context) (Held, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &neverLost{lost: make(chan struct{})}, nil
}

// neverLost is a Held whose Lost() channel is never closed (until Release).
type neverLost struct{ lost chan struct{} }

func (h *neverLost) Lost() <-chan struct{} { return h.lost }
func (h *neverLost) Release() {
	select {
	case <-h.lost:
	default:
		close(h.lost)
	}
}

// --- ConsensusPin: honest stub over luxfi/consensus (Quasar) -----------------

// ConsensusPin will pin the writer via leaderless election over luxfi/consensus
// (Quasar): each candidate proposes itself and the DAG-BFT agreement decides the
// single writer, re-electing on loss. It is NOT implemented; Acquire fails closed
// with ErrNotImplemented so the caller falls back to SingleWriter and reports the
// gap. It never fabricates a pin.
type ConsensusPin struct {
	// NodeID, Peers, and the consensus.Engine handle would live here once wired.
}

// NewConsensusPin returns the unimplemented consensus pin.
func NewConsensusPin() *ConsensusPin { return &ConsensusPin{} }

func (*ConsensusPin) Kind() string { return "consensus(quasar)" }

func (*ConsensusPin) Acquire(context.Context) (Held, error) {
	return nil, ErrNotImplemented
}

// Resolve picks the pin to use. Until consensus election is wired it always
// returns SingleWriter (correct for replicas:1). When ConsensusPin is later
// implemented, this is the ONE place that flips the default — callers depend
// only on the Pin interface.
func Resolve() Pin { return NewSingleWriter() }
