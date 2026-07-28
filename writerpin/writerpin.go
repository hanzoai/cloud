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
	"os"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

// ConsensusPin is the CROSS-CLUSTER pin: a k8s Lease cannot arbitrate between
// pods in different clusters (whose API server decides?), so HA that spans
// hanzo-k8s / lux-k8s / a cloud region needs real distributed agreement.
//
// Build it on luxfi/bft, which already provides the two primitives this needs:
//
//	bft.LeaderForRound(nodes []NodeID, r uint64) NodeID  // deterministic — no vote
//	bft.Quorum(n int) int                                // the agreeing majority
//
// Deterministic leader-per-round is the important one: every node computes the
// SAME writer for a round, so there is no election to get wrong. What remains is
// agreeing when the round advances, which is what the Epoch (bft.NewEpoch) is for.
//
// Do NOT hand-roll the election on top. That was tried here: a Raft-style vote
// over the same peer set took four rounds of real safety bugs (double-voting in a
// term, a leader that let its own lease go stale and voted itself out, a candidate
// counting votes from a term the cluster had left) and STILL produced two
// simultaneous holders under contention. Layering a bespoke election over a BFT
// library is reimplementing the thing the library exists to be.
//
// Until it is wired, Acquire fails closed with ErrNotImplemented so the caller
// falls back to SingleWriter and reports the gap. It never fabricates a pin.
type ConsensusPin struct {
	// NodeID, Peers, and the consensus.Engine handle would live here once wired.
}

// NewConsensusPin returns the unimplemented consensus pin.
func NewConsensusPin() *ConsensusPin { return &ConsensusPin{} }

func (*ConsensusPin) Kind() string { return "consensus(quasar)" }

func (*ConsensusPin) Acquire(context.Context) (Held, error) {
	return nil, ErrNotImplemented
}

// Resolve picks the pin to use — the ONE place the choice is made; callers depend
// only on the Pin interface.
//
// SingleWriter stays the DEFAULT, and that is deliberate. At replicas:1 Kubernetes
// is already the elector, so electing again over a Lease would add a dependency on
// the API server for no benefit: if it were unreachable the writer would refuse to
// start, trading a real outage for an imagined one.
//
// LeasePin is therefore OPT-IN, and becomes correct exactly when it becomes
// necessary — the moment a second pod exists. Set CLOUD_WRITER_LEASE=1 with
// POD_NAME and POD_NAMESPACE (both from the downward API) and the writer elects
// through coordination.k8s.io. Anything missing falls back to SingleWriter and
// says why, rather than half-configuring an election.
func Resolve() Pin {
	pin, _ := resolve(os.Getenv)
	return pin
}

// ResolveWithReason is Resolve plus the sentence explaining the choice, so boot
// can log WHICH pin is in force and why. A silent fallback is how a cluster ends
// up believing it is electing when it is not.
func ResolveWithReason() (Pin, string) { return resolve(os.Getenv) }

// resolve is the testable core: env in, pin + reason out, no I/O beyond building
// the in-cluster client when the lease is actually requested.
func resolve(getenv func(string) string) (Pin, string) {
	if !truthy(getenv("CLOUD_WRITER_LEASE")) {
		return NewSingleWriter(), "single-writer: CLOUD_WRITER_LEASE unset (correct at replicas:1 — Kubernetes is the elector)"
	}
	ns, name := getenv("POD_NAMESPACE"), getenv("POD_NAME")
	if ns == "" || name == "" {
		return NewSingleWriter(), "single-writer: CLOUD_WRITER_LEASE set but POD_NAMESPACE/POD_NAME missing (downward API not wired) — refusing to half-configure an election"
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return NewSingleWriter(), "single-writer: CLOUD_WRITER_LEASE set but not running in-cluster (" + err.Error() + ")"
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return NewSingleWriter(), "single-writer: CLOUD_WRITER_LEASE set but the kubernetes client would not build (" + err.Error() + ")"
	}
	lease := getenv("CLOUD_WRITER_LEASE_NAME")
	if lease == "" {
		lease = "cloud-writer"
	}
	pin, err := NewLeasePin(client, LeaseOptions{Namespace: ns, Name: lease, Identity: name})
	if err != nil {
		return NewSingleWriter(), "single-writer: lease pin rejected its own configuration (" + err.Error() + ")"
	}
	return pin, "lease(k8s): electing on " + ns + "/" + lease + " as " + name
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
