package paas

// fleet.go — the in-process fleet-observation seam.
//
// The admin god-view (/v1/admin/products + the overview drift KPIs, clients/admin) needs the
// SAME operator-App-CR + Deployment observation the /v1/paas/apps board already computes
// (observeFleet → observeCR → drift.go). Rather than fork a SECOND k8s dynamic client and a
// SECOND drift model into the admin subsystem, paas PUBLISHES its observer here once at Mount
// and admin RESOLVES it at request time — the identical in-process-seam pattern
// finance.Current() (the money plane) and transport.SetApp (the commerce plane) use for
// a co-resident host handing a narrow capability to a sibling subsystem.
//
// Fail-closed: nil before Mount, or when the k8s client did not resolve (split deploy / no
// kubeconfig). The consumer treats nil / not-Ready as "fleet not observable here" and renders
// an honest-empty registry with the real reason, never status-theater.

import (
	"context"
	"sync"

	"github.com/hanzoai/cloud"
)

// Fleet is the read-only fleet observation a co-resident consumer folds over. Observe returns
// the whole platform fleet (every scanned namespace: hanzo/-testnet/-devnet) as AppView drift
// rows — declared vs running tag, operator-reconciled health/phase, and the drift verdict.
// Ready reports whether the k8s client resolved (else reason is the init error the /v1/paas/
// health route already surfaces).
type Fleet interface {
	Observe(ctx context.Context) ([]AppView, error)
	Ready() (ready bool, reason string)
}

var (
	fleetMu        sync.RWMutex
	publishedFleet Fleet
)

// PublishFleet records the process-wide fleet observer (called once at Mount; nil clears).
func PublishFleet(f Fleet) {
	fleetMu.Lock()
	publishedFleet = f
	fleetMu.Unlock()
}

// CurrentFleet returns the published fleet observer, or nil before Mount / in a split deploy
// where the PaaS control plane is not co-resident. A consumer treats nil as "fleet not
// co-resident" and renders an honest-empty registry.
func CurrentFleet() Fleet {
	fleetMu.RLock()
	defer fleetMu.RUnlock()
	return publishedFleet
}

// fleetObserver binds the seam to the mounted paas state (its dynamic k8s client). It is the
// ONLY implementation; it reuses observeFleet verbatim so the admin board and the
// /v1/paas/apps board can never disagree about what the fleet is.
type fleetObserver struct{ s *cloud.Service[state] }

// Observe reads the whole platform fleet across scanOrder() (prod-first). A nil client yields
// an empty fleet (Ready carries the reason); a per-namespace absence is non-fatal inside
// observeFleet, so the reachable namespaces still render.
func (f fleetObserver) Observe(ctx context.Context) ([]AppView, error) {
	if f.s.State.dyn == nil {
		return nil, nil
	}
	return observeFleet(f.s, ctx, scanOrder())
}

// Ready reports k8s reachability — the same fail-closed signal the /health route reports.
func (f fleetObserver) Ready() (bool, string) {
	if f.s.State.dyn == nil {
		return false, f.s.State.initErr
	}
	return true, ""
}
