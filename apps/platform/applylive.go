// applylive.go — the ONE version-monotonic deploy mechanic shared by the
// image-source path (deployImage) and the git build reconciler.
//
// Both paths must write the operator Service CR (applyService) AND advance the
// app's live pointer (FinalizeLive). FinalizeLive is already an atomic,
// monotonic CAS so the recorded live version can never regress. The remaining
// gap (RED LOW-1) is the CR write itself: two concurrent deploys of the SAME
// app could interleave so that an OLDER deploy's applyService lands AFTER a
// NEWER one already went live, leaving the live Service CR image lagging the
// recorded live version. applyLive closes that gap by running
// supersede-check → applyService → FinalizeLive as one per-app-serialized
// critical section: an older deploy that loses the race is superseded and never
// writes its (older) CR. The section is the app's across every replica
// (lock.Keyed.Across), not only in this process: deploys of one app land on whichever
// replica the build finished on.

package platform

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/apps/sbom"
	"github.com/hanzoai/cloud/sdk"
)

// applyLive writes the operator Service CR and advances the app to live as one
// per-app-serialized, version-monotonic step. It re-reads the app under the lock
// so the supersede check sees the latest live pointer (not a snapshot taken
// before the lock), applies the CR only when this deployment is NOT superseded,
// and finalizes live under the same lock — so the live Service CR image can
// never lag the recorded live version, even under concurrent same-app deploys.
//
// Returns:
//   - superseded=true  → a newer version is already live; the CR was NOT written
//     and the app was NOT advanced (caller decides: record terminal / log).
//   - err!=nil         → the CR write (applyService) failed (hard failure).
//   - advanced=true    → this deployment is now the live version.
//   - all false, err nil → CR written but the live-pointer finalize soft-failed
//     (already logged); the operator still reconciles the applied CR.
func applyLive(s *cloud.Service[state], ctx context.Context, org, project string, app Application, d Deployment, tag, image string, now int64) (advanced, superseded bool, err error) {
	unlock, err := s.State.appLock.Across(ctx, s.State.store.db, "apply:"+app.ID)
	if err != nil {
		return false, false, fmt.Errorf("applyLive: another deploy of %s holds the app: %w", app.Slug, err)
	}
	defer unlock()

	// Re-read the app under the lock so supersession is evaluated against the
	// current live pointer, not a pre-lock snapshot.
	if fresh, e := s.State.store.GetApplicationByID(ctx, org, app.ID); e == nil {
		app = fresh
	}
	sup, e := buildSuperseded(s, ctx, d, app)
	if e != nil {
		// A supersede-check error must not silently drop the deploy: fall through
		// and apply — the FinalizeLive CAS still enforces DB monotonicity.
		s.Log.Warn("applyLive: supersede check failed (continuing)", "org", org, "app", app.Slug, "err", e)
	} else if sup {
		return false, true, nil
	}
	// The hosts render canonical: the app's own name, then its custom hosts.
	app.DomainsJSON = marshalHosts(appHosts(s, app.Slug, activeHosts(app.DomainsJSON)))
	if e := s.State.k8s.applyService(ctx, org, project, app, image); e != nil {
		return false, false, e
	}
	// Declare the app's secret-env sync alongside its Service CR (best-effort —
	// never fails the deploy; secrets show "pending" until the operator syncs).
	ensureSecretSync(s, ctx, org, app)
	adv, e := s.State.store.FinalizeLive(ctx, d, tag, tenantNamespace(org), now)
	if e != nil {
		s.Log.Warn("applyLive: finalize live failed (continuing)", "org", org, "app", app.Slug, "err", e)
		return false, false, nil
	}
	if adv {
		// Deploy-time SBOM prefetch: this image is now live, so pull its attached
		// CycloneDX SBOM from the registry and materialize components into the global
		// datastore ahead of the console asking. Best-effort and detached — a
		// registry miss/failure must never affect the deploy. Own context: the
		// deploy's ctx is about to end.
		go sbom.Prefetch(context.WithoutCancel(ctx), s.Log, image)
		// This deployment is now the live version — the ONE DeployLive funnel for
		// both the image path (deployTagCore) and the git build reconciler.
		emitDeployLifecycle(ctx, cloud.LifecycleDeployLive, org, app, d, app.Slug+" live ("+image+")")
	}
	return adv, false, nil
}
