// orphans.go — reaping apps whose project is gone.
//
// A project belongs to IAM and is deleted there. Platform's apps hang off the
// project NAME, so a delete on the IAM side leaves this side holding apps under a
// project that no longer exists: unreachable through the API (requireProject 404s)
// but still deployed, still holding a Service CR, a KMSSecret and a volume, and
// still costing the tenant money.
//
// The obvious fix is to cascade from platform's own DELETE route, and that is what
// this replaces. It only ever worked when the project happened to be deleted
// THROUGH platform — which is the wrong condition, since the project is not
// platform's to delete. Reaping on the app side instead makes the outcome
// independent of who did the deleting and where.
//
// Idempotent and org-scoped like the build reconciler: every write targets
// tenant-<row.Org>, derived from the row and never from a request. Fail-safe on
// read: a project store that cannot answer reaps NOTHING, because "IAM is
// unavailable" and "the project is gone" must never be the same signal.
package platform

import (
	"context"
	"time"

	"github.com/hanzoai/cloud"
)

// orphanReapInterval is how often the app tree is checked against IAM. Slow on
// purpose: an orphan costs money but breaks nothing, and this walks every org.
const orphanReapInterval = 5 * time.Minute

func runOrphanReaper(s *cloud.Service[state], ctx context.Context) {
	t := time.NewTicker(orphanReapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reapOrphans(s, ctx)
		}
	}
}

// reapOrphans deletes every app whose IAM project no longer exists, tearing down
// what the app owns in the cluster on the way out.
func reapOrphans(s *cloud.Service[state], ctx context.Context) {
	apps, err := s.State.store.AllApplications(ctx)
	if err != nil {
		s.Log.Warn("orphan reap: cannot list apps", "err", err)
		return
	}
	// One existence check per (org,project), not per app: an org with fifty apps in
	// one project is one question, asked once.
	type scope struct{ org, project string }
	seen := map[scope]bool{}
	for _, a := range apps {
		sc := scope{a.Org, a.ProjectID}
		alive, asked := seen[sc]
		if !asked {
			ok, err := s.State.projects.Exists(ctx, a.Org, a.ProjectID)
			if err != nil {
				// Fail SAFE: an unreachable project store is not evidence of
				// deletion. Skip this scope entirely and try again next tick.
				s.Log.Warn("orphan reap: project store unavailable; reaping nothing for this scope",
					"org", a.Org, "project", a.ProjectID, "err", err)
				seen[sc] = true // treat as alive for this pass
				continue
			}
			alive = ok
			seen[sc] = ok
		}
		if alive {
			continue
		}
		s.Log.Info("orphan reap: project is gone, removing its app",
			"org", a.Org, "project", a.ProjectID, "app", a.Slug)
		if err := s.State.k8s.deleteService(ctx, a.Org, a.Slug); err != nil {
			s.Log.Warn("orphan reap: teardown app CR failed (continuing)", "org", a.Org, "app", a.Slug, "err", err)
		}
		if err := s.State.k8s.deleteKMSSecret(ctx, a.Org, a.Slug); err != nil {
			s.Log.Warn("orphan reap: teardown KMSSecret failed (continuing)", "org", a.Org, "app", a.Slug, "err", err)
		}
		// The row goes last: while it exists the app is still reapable, so a
		// failure above is retried on the next tick rather than lost. The VOLUME
		// is deliberately left behind — see deleteService — because a deleted
		// project must not silently destroy a tenant's data.
		if _, _, err := s.State.store.DeleteApplication(ctx, a.Org, a.ProjectID, a.Slug); err != nil {
			s.Log.Warn("orphan reap: delete app row failed", "org", a.Org, "app", a.Slug, "err", err)
		}
	}
}
