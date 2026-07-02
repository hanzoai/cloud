// reconcile.go — the git build→deploy handoff (formerly deploy.go's "phase 2").
//
// deployGit launches an in-cluster BuildKit Job and lands the deployment
// "building"; it does NOT block the HTTP request waiting for the build. This
// reconciler is the ONE owner of what happens next: it periodically scans every
// deployment still "building", checks its Job, and on success applies the
// operator Service CR with the built image — the SAME applyService the
// image-source path uses — flipping build→succeeded, deployment→deploying,
// app→live. The operator then reconciles the rollout.
//
// Why a reconciler and not a per-deploy goroutine: state lives in the store, not
// in memory, so a cloud restart mid-build RESUMES cleanly (the next tick re-reads
// "building" rows). It is idempotent (applyService is create-or-update; a row is
// advanced off "building" once handled) and org-scoped (every write targets
// tenant-<row.Org>, derived from the row, never a request value).
package platform

import (
	"context"
	"time"
)

const (
	// buildReconcileInterval is how often in-flight builds are checked.
	buildReconcileInterval = 10 * time.Second
	// buildDeadline bounds a single build+push. A deployment "building" longer
	// than this (Job stuck, node lost, TTL-cleaned) is failed honestly rather
	// than left pending forever.
	buildDeadline = 20 * time.Minute
)

// runBuildReconciler ticks reconcileBuilds until ctx is cancelled (Shutdown).
func (s *svc) runBuildReconciler(ctx context.Context) {
	t := time.NewTicker(buildReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reconcileBuilds(ctx)
		}
	}
}

// reconcileBuilds advances every "building" deployment one step. Cluster
// unreachable ⇒ no-op this tick (try again next); no build is ever failed just
// because the control plane briefly lost the apiserver.
func (s *svc) reconcileBuilds(ctx context.Context) {
	if s.k8s.ready() != nil {
		return
	}
	deps, err := s.store.ListBuildingDeployments(ctx)
	if err != nil {
		s.log.Warn("reconcile: list building deployments", "err", err)
		return
	}
	for _, d := range deps {
		s.reconcileBuild(ctx, d)
	}
}

// reconcileBuild advances one "building" deployment: waits for its Job, then on
// success applies the Service CR, on failure/deadline records the honest error.
func (s *svc) reconcileBuild(ctx context.Context, d Deployment) {
	if d.Source != "git" || d.BuildID == "" {
		return // only git builds pass through "building"
	}
	overdue := time.Now().Unix()-d.CreatedAt > int64(buildDeadline.Seconds())

	b, err := s.store.GetBuild(ctx, d.Org, d.BuildID)
	if err != nil || b.JobName == "" {
		if overdue {
			s.failBuild(ctx, d, b, "build record/job missing past deadline")
		}
		return
	}

	done, succeeded, jErr := s.k8s.jobResult(ctx, b.JobName)
	if jErr != nil {
		// Job not found (TTL-cleaned) or transient apiserver error. Only give up
		// once the deadline has passed; otherwise wait for the next tick.
		if overdue {
			s.failBuild(ctx, d, b, "build job not found past deadline: "+jErr.Error())
		}
		return
	}
	if !done {
		if overdue {
			s.failBuild(ctx, d, b, "build exceeded deadline")
		}
		return
	}
	if !succeeded {
		s.failBuild(ctx, d, b, "build job failed")
		return
	}

	// Build succeeded — resolve app + project and apply the Service CR with the
	// built image (the ONE deploy mechanic, identical to deployImage).
	app, err := s.store.GetApplicationByID(ctx, d.Org, d.ApplicationID)
	if err != nil {
		s.log.Warn("reconcile: get application", "org", d.Org, "dep", d.ID, "err", err)
		return
	}
	proj, err := s.store.GetProjectByID(ctx, d.Org, app.ProjectID)
	if err != nil {
		s.log.Warn("reconcile: get project", "org", d.Org, "dep", d.ID, "err", err)
		return
	}
	if err := s.k8s.applyService(ctx, d.Org, proj.Slug, app, d.Image); err != nil {
		s.failBuild(ctx, d, b, "apply Service CR: "+err.Error())
		return
	}

	now := time.Now().Unix()
	b.Status, b.UpdatedAt = "succeeded", now
	if uErr := s.store.UpdateBuild(ctx, b); uErr != nil {
		s.log.Warn("reconcile: finalize build", "build", b.ID, "err", uErr)
	}
	d.Status, d.UpdatedAt = "deploying", now
	if uErr := s.store.UpdateDeployment(ctx, d); uErr != nil {
		s.log.Warn("reconcile: finalize deployment", "dep", d.ID, "err", uErr)
	}
	_, tag := splitImageRef(d.Image)
	app.Status, app.CurrentDeploy, app.ImageTag, app.Namespace, app.UpdatedAt = "live", d.ID, tag, tenantNamespace(d.Org), now
	if uErr := s.store.UpdateApplication(ctx, app); uErr != nil {
		s.log.Warn("reconcile: finalize application", "app", app.Slug, "err", uErr)
	}
	s.log.Info("build reconciled → deployed (git)",
		"org", d.Org, "app", app.Slug, "ns", tenantNamespace(d.Org), "image", d.Image, "dep", d.ID)
}

// failBuild records the honest failure across build + deployment + app. Never
// fabricates a success; the app view surfaces "error" and the message.
func (s *svc) failBuild(ctx context.Context, d Deployment, b Build, msg string) {
	now := time.Now().Unix()
	if b.ID != "" {
		b.Status, b.UpdatedAt = "failed", now
		_ = s.store.UpdateBuild(ctx, b)
	}
	d.Status, d.Message, d.UpdatedAt = "error", msg, now
	_ = s.store.UpdateDeployment(ctx, d)
	if app, err := s.store.GetApplicationByID(ctx, d.Org, d.ApplicationID); err == nil {
		app.Status, app.UpdatedAt = "error", now
		_ = s.store.UpdateApplication(ctx, app)
	}
	s.log.Error("build failed (git)", "org", d.Org, "dep", d.ID, "reason", msg)
}
