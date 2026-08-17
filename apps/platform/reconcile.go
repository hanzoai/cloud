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
	"errors"
	"time"

	"github.com/hanzoai/cloud"
)

const (
	// buildReconcileInterval is how often in-flight builds are checked.
	buildReconcileInterval = 10 * time.Second
	// buildDeadline bounds a single build+push. A deployment "building" longer
	// than this (Job stuck, node lost, TTL-cleaned) is failed honestly rather
	// than left pending forever.
	//
	// It is set ABOVE the observed range, not on it. At 20m it sat on top of how
	// long cloud's own image actually takes — three consecutive builds ran 15m,
	// 17m and 20m42s — so the release became a coin flip, and losing the toss cost
	// more than the wait: the build had already pushed its image, so failing the
	// wait discarded a good image AND burned that version number permanently
	// (a claim folds published image tags in, by design, so the number is never
	// reused). A deadline exists to catch a Job that is STUCK; it should not be
	// close enough to a healthy build to fire on one.
	//
	// The expected direction is DOWN, not up: the build cache now lives in the
	// registry (buildFrontendCmd), so the Go compiles that took 11 minutes from
	// cold should mostly become cache hits. This bound is the stuck-Job catch, not
	// a target.
	buildDeadline = 30 * time.Minute
)

// runBuildReconciler ticks reconcileBuilds until ctx is cancelled (Shutdown).
func runBuildReconciler(s *cloud.Service[state], ctx context.Context) {
	t := time.NewTicker(buildReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcileBuilds(s, ctx)
			reconcileDirectBuilds(s, ctx)
		}
	}
}

// reconcileDirectBuilds records the outcome of builds that have no deployment.
//
// A build made through POST /v1/runner is not attached to a Deployment, and
// reconcileBuild returns early for anything whose Source is not "git" — so
// nothing advanced these rows and every one of them stayed "queued" for the life
// of the row, whether its Job had pushed an image, failed, or never been
// scheduled at all.
//
// The cost was not cosmetic. GET /v1/builds could not answer the only question it
// is asked, because a build that SUCCEEDED and a build that could not be
// scheduled read identically. Six builds sat unschedulable for five days looking
// exactly like six in flight, which is why nobody saw it. This closes that: the
// row now says what happened.
//
// It only ever writes a TERMINAL status from the Job's own terminal state. A
// cluster that is briefly unreachable, or a Job still running, leaves the row
// exactly as it is and re-drives next tick — the same rule the deployment path
// follows, because a build must never be called failed for a control-plane blip.
// A Job that has been TTL-cleaned away is the one case where absence is an
// answer: the row cannot be resolved from the cluster any more, so past the
// deadline it is failed honestly rather than left pending forever.
func reconcileDirectBuilds(s *cloud.Service[state], ctx context.Context) {
	if s.State.k8s.ready() != nil {
		return
	}
	builds, err := s.State.store.ListUnfinishedBuilds(ctx)
	if err != nil {
		s.Log.Warn("reconcile: list unfinished builds", "err", err)
		return
	}
	for _, b := range builds {
		if b.DeploymentID != "" {
			continue // the deployment path owns this one
		}
		done, succeeded, jErr := s.State.k8s.jobResult(ctx, b.JobName)
		switch {
		case jErr != nil:
			// Includes a TTL-deleted Job (NotFound). Only the deadline decides,
			// never a transient read.
			if time.Now().Unix()-b.CreatedAt > int64(buildDeadline.Seconds()) {
				finishDirectBuild(s, ctx, b, "failed")
			}
		case !done:
			// Still running. A Job that outlives the deadline is stuck, not slow.
			if time.Now().Unix()-b.CreatedAt > int64(buildDeadline.Seconds()) {
				finishDirectBuild(s, ctx, b, "failed")
			}
		case succeeded:
			finishDirectBuild(s, ctx, b, "succeeded")
		default:
			finishDirectBuild(s, ctx, b, "failed")
		}
	}
}

// finishDirectBuild writes one terminal status. Its only job is to make the row
// tell the truth, so a write that fails is logged and retried next tick rather
// than silently dropped — a build whose outcome cannot be recorded is exactly the
// state this function exists to end.
func finishDirectBuild(s *cloud.Service[state], ctx context.Context, b Build, status string) {
	b.Status, b.UpdatedAt = status, time.Now().Unix()
	if err := s.State.store.UpdateBuild(ctx, b); err != nil {
		s.Log.Warn("reconcile: record build outcome", "build", b.ID, "status", status, "err", err)
		return
	}
	s.Log.Info("build finished", "build", b.ID, "image", b.Image, "status", status)
}

// reconcileBuilds advances every "building" deployment one step. Cluster
// unreachable ⇒ no-op this tick (try again next); no build is ever failed just
// because the control plane briefly lost the apiserver.
func reconcileBuilds(s *cloud.Service[state], ctx context.Context) {
	if s.State.k8s.ready() != nil {
		return
	}
	deps, err := s.State.store.ListBuildingDeployments(ctx)
	if err != nil {
		s.Log.Warn("reconcile: list building deployments", "err", err)
		return
	}
	for _, d := range deps {
		reconcileBuild(s, ctx, d)
	}
}

// reconcileBuild advances one "building" deployment: waits for its Job, then on
// success (once the tenant's operator RBAC is ready) applies the Service CR. Every
// TRANSIENT condition — cluster briefly unreachable, Job not yet finished, or the
// tenant's RoleBinding still provisioning (errTenantProvisioning) — leaves the
// deployment "building" and re-drives on the next tick; only a genuine build failure
// or the elapsed deadline records an honest terminal error. Crucially, a slow tenant
// onboarding is NOT failed permanently (there is no client to retry a git build) and
// is NOT waited on in-line (which would head-of-line-block other orgs' go-lives).
func reconcileBuild(s *cloud.Service[state], ctx context.Context, d Deployment) {
	if d.Source != "git" || d.BuildID == "" {
		return // only git builds pass through "building"
	}
	overdue := time.Now().Unix()-d.CreatedAt > int64(buildDeadline.Seconds())

	b, err := s.State.store.GetBuild(ctx, d.Org, d.BuildID)
	if err != nil || b.JobName == "" {
		if overdue {
			failBuild(s, ctx, d, b, "build record/job missing past deadline")
		}
		return
	}

	done, succeeded, jErr := s.State.k8s.jobResult(ctx, b.JobName)
	if jErr != nil {
		// Job not found (TTL-cleaned) or transient apiserver error. Only give up
		// once the deadline has passed; otherwise wait for the next tick.
		if overdue {
			failBuild(s, ctx, d, b, "build job not found past deadline: "+jErr.Error())
		}
		return
	}
	if !done {
		if overdue {
			failBuild(s, ctx, d, b, "build exceeded deadline")
		}
		return
	}
	if !succeeded {
		failBuild(s, ctx, d, b, "build job failed")
		return
	}

	// Build succeeded — resolve the app and apply the Service CR with the built
	// image (the ONE deploy mechanic, identical to deployImage). The app carries
	// its project NAME (app.ProjectID), which is the operator part-of label — no
	// separate project lookup needed.
	app, err := s.State.store.GetApplicationByID(ctx, d.Org, d.ApplicationID)
	if err != nil {
		s.Log.Warn("reconcile: get application", "org", d.Org, "dep", d.ID, "err", err)
		return
	}

	// The tenant namespace must exist and its operator RBAC must have landed before
	// the Service CR can be written. Unlike the synchronous image path (which BLOCKS
	// up to ~45s so a single client deploy succeeds), the reconciler does ONE
	// non-blocking readiness probe (creating the namespace if absent, which is what
	// triggers the operator to project cloud-api's RoleBinding). If RBAC has not yet
	// landed, leave the deployment "building" and re-drive next tick — a slow/wedged
	// tenant onboarding then never HEAD-OF-LINE-BLOCKS other orgs' go-lives and never
	// permanently fails the git build. Give up only once the build deadline elapses.
	ns := tenantNamespace(d.Org)
	ready, provErr := s.State.k8s.ensureTenantReady(ctx, ns, d.Org)
	if provErr != nil {
		if overdue {
			failBuild(s, ctx, d, b, "prepare tenant namespace past deadline: "+provErr.Error())
		}
		return // transient cluster error — retry next tick
	}
	if !ready {
		if overdue {
			failBuild(s, ctx, d, b, "tenant RBAC still provisioning past deadline")
		}
		return // stay "building"; the operator's RoleBinding lands before the next tick
	}

	// Build succeeded — write the operator Service CR and advance the app to live
	// as ONE per-app-serialized, version-monotonic step (shared with deployImage).
	// Under a per-app lock, applyLive re-checks supersession against the current
	// live pointer, applies the (built-image) CR only if this build is NOT
	// superseded, and finalizes live — so an OLDER build whose Job finished LATE
	// can never leave the live Service CR image lagging the recorded live version
	// (MED-1 monotonicity + RED LOW-1 joint ordering).
	now := time.Now().Unix()
	_, tag := splitImageRef(d.Image)
	advanced, superseded, err := applyLive(s, ctx, d.Org, app.ProjectID, app, d, tag, d.Image, now)
	if superseded {
		// A newer version is already live: this build's image is fine but its
		// (older) CR must NOT be written — record it terminally and stop.
		supersedeBuild(s, ctx, d, b)
		return
	}
	if err != nil {
		// Defense in depth: the pre-check above confirmed RBAC was ready, but if the
		// apply still surfaced a provisioning error (a readiness flap between probe and
		// write), treat it as TRANSIENT — leave the deployment "building" and re-drive
		// next tick, never a permanent fail — unless the deadline has already elapsed.
		if errors.Is(err, errTenantProvisioning) && !overdue {
			return
		}
		failBuild(s, ctx, d, b, "apply Service CR: "+err.Error())
		return
	}

	b.Status, b.UpdatedAt = "succeeded", now
	if uErr := s.State.store.UpdateBuild(ctx, b); uErr != nil {
		s.Log.Warn("reconcile: finalize build", "build", b.ID, "err", uErr)
	}
	meterBuild(s, b, now) // the Job ran to completion → bill its wall-clock minutes.
	d.Status, d.UpdatedAt = "deploying", now
	if uErr := s.State.store.UpdateDeployment(ctx, d); uErr != nil {
		s.Log.Warn("reconcile: finalize deployment", "dep", d.ID, "err", uErr)
	}
	if advanced {
		s.Log.Info("build reconciled → deployed (git)",
			"org", d.Org, "app", app.Slug, "ns", tenantNamespace(d.Org), "image", d.Image, "dep", d.ID)
	} else {
		s.Log.Info("build reconciled but superseded at finalize (newer version already live)",
			"org", d.Org, "app", app.Slug, "dep", d.ID, "version", d.Version)
	}
}

// buildSuperseded reports whether a NEWER deployment is already the app's live
// deployment, so this (older) build must not overwrite it. The live version is
// resolved from app.CurrentDeploy; an empty pointer, a pointer to THIS deployment,
// or a dangling pointer is not superseded (there is nothing newer to protect).
func buildSuperseded(s *cloud.Service[state], ctx context.Context, d Deployment, app Application) (bool, error) {
	if app.CurrentDeploy == "" || app.CurrentDeploy == d.ID {
		return false, nil
	}
	live, err := s.State.store.GetDeployment(ctx, d.Org, d.ApplicationID, app.CurrentDeploy)
	if errors.Is(err, errNotFound) {
		return false, nil // dangling live pointer — don't block progress
	}
	if err != nil {
		return false, err
	}
	return d.Version < live.Version, nil
}

// supersedeBuild records a build that succeeded but was overtaken by a newer
// deployment already live: the image is REAL (build → succeeded), but the
// deployment is terminal "superseded" and the app is left untouched. Honest —
// the app view keeps showing the newer version live; this one never regressed it.
func supersedeBuild(s *cloud.Service[state], ctx context.Context, d Deployment, b Build) {
	now := time.Now().Unix()
	if b.ID != "" {
		b.Status, b.UpdatedAt = "succeeded", now
		_ = s.State.store.UpdateBuild(ctx, b)
		meterBuild(s, b, now) // the Job still ran to completion → bill its minutes.
	}
	d.Status, d.Message, d.UpdatedAt = "superseded", "a newer deployment went live before this build finished", now
	_ = s.State.store.UpdateDeployment(ctx, d)
	s.Log.Info("build superseded (newer version already live)",
		"org", d.Org, "dep", d.ID, "version", d.Version)
}

// failBuild records the honest failure across build + deployment + app. Never
// fabricates a success; the app view surfaces "error" and the message.
func failBuild(s *cloud.Service[state], ctx context.Context, d Deployment, b Build, msg string) {
	now := time.Now().Unix()
	if b.ID != "" {
		b.Status, b.UpdatedAt = "failed", now
		_ = s.State.store.UpdateBuild(ctx, b)
	}
	d.Status, d.Message, d.UpdatedAt = "error", msg, now
	_ = s.State.store.UpdateDeployment(ctx, d)
	if app, err := s.State.store.GetApplicationByID(ctx, d.Org, d.ApplicationID); err == nil {
		app.Status, app.UpdatedAt = "error", now
		_ = s.State.store.UpdateApplication(ctx, app)
	}
	s.Log.Error("build failed (git)", "org", d.Org, "dep", d.ID, "reason", msg)
}
