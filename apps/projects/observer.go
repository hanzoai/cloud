package projects

import (
	"context"
	"sync"
)

// DeployObserver receives one notification whenever a site goes live. It is the
// seam the (separately-landed) agent-sessions lane hooks to record a "site
// deployed + URL" session event, WITHOUT projects taking any hard dependency on
// that lane: projects only ever calls this interface. The default is nil — a
// no-op — so a deployment that never registers an observer is unaffected.
type DeployObserver interface {
	OnDeploy(ctx context.Context, org, slug, url, deploymentID string)
}

var (
	observerMu sync.RWMutex
	observer   DeployObserver
)

// SetDeployObserver registers the package-level deploy observer (nil clears it).
// It is safe for concurrent use; the last writer wins. Wiring is intentionally
// package-level (not per-service) because there is one mounted projects surface per
// binary and the sessions lane wires the observer at startup.
func SetDeployObserver(o DeployObserver) {
	observerMu.Lock()
	observer = o
	observerMu.Unlock()
}

// notifyDeploy fires the registered observer for a freshly published site. It is
// a no-op (never panics) when no observer is registered, and it passes the
// canonical pretty URL + deployment id so the observer records the same identity
// the API returned.
func notifyDeploy(ctx context.Context, org, slug string, d Deployment) {
	observerMu.RLock()
	o := observer
	observerMu.RUnlock()
	if o == nil {
		return
	}
	o.OnDeploy(ctx, org, slug, d.LiveURL, d.ID)
}
