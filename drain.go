package cloud

// drain.go is the graceful-drain readiness signal that makes a rolling upgrade lose no
// request. On SIGTERM the process flips to draining and /readyz returns 503, so Kubernetes
// marks the pod NotReady: it is removed from Service endpoints AND — via the live
// membership source's terminating/ready gate (membership_k8s.go) — from every peer's
// writer election. An org this pod owned is then re-elected to a live successor that
// hydrates its latest fenced snapshot (M3), BEFORE this pod stops serving. The process
// stays UP through terminationGracePeriodSeconds to drain in-flight requests and ship
// final state (OrgStore.CloseAll, ship-before-close), so no acknowledged write is lost.
//
// Liveness (/healthz) stays 200 throughout — we want the process alive to finish the
// drain; only readiness (/readyz) flips, the standard K8s "stop new traffic, keep me
// running to finish" contract.

import (
	"sync/atomic"
	"time"
)

// draining flips true when a graceful shutdown begins. Read lock-free by the /readyz
// handler on the ops listener.
var draining atomic.Bool

// SetDraining marks the process draining (readiness → NotReady). Idempotent.
func SetDraining() { draining.Store(true) }

// Draining reports whether a graceful shutdown is in progress.
func Draining() bool { return draining.Load() }

// shardDrainGrace is how long a draining pod stays serving (NotReady) so peers observe it
// leave the membership and re-elect its orgs to live successors before it tears down. It
// is a few membership-refresh intervals (membership_k8s.go refreshes at 2s), covered by
// terminationGracePeriodSeconds. Applied only when sharding is active (multi-pod); a
// single-pod deployment has no successor to hand off to, so it drains immediately.
const shardDrainGrace = 6 * time.Second
