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
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	//
	// Cloud's image runs 28 to 35 minutes while two of its builds share the
	// builder, so the bound is 45: above that range, and below the 50 minutes the
	// release's image step waits for the tag, so this verdict lands while
	// something still reads it. The step stops on a failed build, which makes a
	// bound under a healthy build a failed release with a good image behind it.
	buildDeadline = 45 * time.Minute
)

// runBuildReconciler ticks reconcileBuilds until ctx is cancelled (Shutdown).
func runBuildReconciler(s *cloud.Service[state], ctx context.Context) {
	lead := s.Coord.Lead("platform:builds")
	t := time.NewTicker(buildReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			lead.Do(ctx, func(ctx context.Context) {
				if err := s.State.k8s.dispatch(ctx); err != nil {
					s.Log.Warn("reconcile: dispatch builds", "err", err)
				}
				reconcileBuilds(s, ctx)
				reconcileDirectBuilds(s, ctx)
				reconcileLive(s, ctx)
				s.State.k8s.sleepBuilders(ctx)
			})
		}
	}
}

// reconcileLive moves a deployment from "deploying" to "live" once the Service its app
// is serving it through is seen Running and healthy. A cluster that is unreachable, a CR
// not yet Running or a pod not yet ready leaves the row as it is for the next tick.
func reconcileLive(s *cloud.Service[state], ctx context.Context) {
	if s.State.k8s.ready() != nil {
		return
	}
	deps, err := s.State.store.ListLiveDeploying(ctx)
	if err != nil {
		s.Log.Warn("reconcile: list deploying deployments", "err", err)
		return
	}
	for _, d := range deps {
		app, err := s.State.store.GetApplicationByID(ctx, d.Org, d.ApplicationID)
		if err != nil {
			continue
		}
		if phase, health := s.State.k8s.observeService(ctx, d.Org, app.Slug); !healthyLive(phase, health) {
			continue
		}
		d.Status, d.UpdatedAt = "live", time.Now().Unix()
		if err := s.State.store.UpdateDeployment(ctx, d); err != nil {
			s.Log.Warn("reconcile: mark deployment live", "org", d.Org, "app", app.Slug, "deployment", d.ID, "err", err)
		}
	}
}

// healthyLive reports whether what the cluster says of a Service is a deployment that
// is serving: the operator's phase Running and every desired replica ready.
func healthyLive(phase, health string) bool { return phase == "Running" && health == "green" }

// reconcileDirectBuilds records the outcome of builds that have no deployment.
//
// A build made through POST /v1/platform/runner is not attached to a Deployment, and
// reconcileBuild returns early for anything whose Source is not "git" — so
// nothing advanced these rows and every one of them stayed "queued" for the life
// of the row, whether its Job had pushed an image, failed, or never been
// scheduled at all.
//
// The cost was not cosmetic. GET /v1/platform/builds could not answer the only question it
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
		done, succeeded, job, jErr := directBuildResult(s, ctx, b)
		switch {
		case jErr != nil:
			// Includes a TTL-deleted Job (NotFound). Only the deadline decides,
			// never a transient read. A build with no Job because it is being asked again
			// is launched again (retry.go).
			switch {
			case time.Now().Unix()-b.CreatedAt > int64(stuckAfter(b).Seconds()):
				giveUp(s, ctx, b)
			case apierrors.IsNotFound(jErr) && b.Attempt > 0 && b.Request != "":
				askAgain(s, ctx, b)
			}
		case !done:
			// Waiting its turn, or running. A build that has run past the deadline is
			// stuck, not slow; one still waiting has not started and is neither.
			if overdue, queued := buildOverdue(s, ctx, b, stuckAfter(b)); overdue {
				giveUp(s, ctx, b)
			} else if !queued {
				markBuilding(s, ctx, b)
			}
		case succeeded:
			digest := s.State.k8s.jobDigest(ctx, job)
			finishDirectBuild(s, ctx, b, "succeeded", "", digest)
		default:
			reason := s.State.k8s.jobReason(ctx, job)
			if !retryFetch(s, ctx, b, reason) {
				finishDirectBuild(s, ctx, b, "failed", reason, "")
			}
		}
	}
}

// stuckAfter is how long a build may run before it is called stuck rather than
// slow. A build across several architectures is TWO phases — every architecture
// at once, then the index over them — and one deadline for both would call a
// healthy second phase a failure, in the record, while the image it is writing
// publishes anyway.
func stuckAfter(b Build) time.Duration {
	if len(b.Platforms) > 1 {
		return 2 * buildDeadline
	}
	return buildDeadline
}

// directBuildResult asks whether one direct build is finished, and advances it if
// it is a fan-out that is ready for its next step.
//
// A single-architecture build is its Job and nothing else. A build across several
// architectures is one Job per architecture and then an index Job that joins them,
// and the order is the contract: an index naming an image that is not published is
// a broken tag, so the join is launched only once every architecture has landed.
// The answer it returns has the same shape either way, so the outcome is still
// recorded in one place. job is the Job that decided it, which is where a failure
// is read from.
func directBuildResult(s *cloud.Service[state], ctx context.Context, b Build) (done, succeeded bool, job string, err error) {
	job = b.JobName
	// The index Job decides the build the moment it exists.
	done, succeeded, err = s.State.k8s.jobResult(ctx, job)
	if len(b.Platforms) < 2 || !apierrors.IsNotFound(err) {
		return done, succeeded, job, err
	}
	var landed []string
	waiting := false
	for _, p := range b.Platforms {
		half := buildJobName(b.ID, nativeArch[p])
		d, ok, jErr := s.State.k8s.jobResult(ctx, half)
		switch {
		case jErr != nil:
			return false, false, job, jErr
		case !d:
			waiting = true
		case !ok:
			// One architecture failed. There is nothing honest to join, and the half
			// that did build stays at its own tag to be looked at.
			return true, false, half, nil
		default:
			landed = append(landed, half)
		}
	}
	if waiting {
		// A landed half keeps its Job, which the join reads, and gives back its
		// pod: a finished pod holds its node's disk.
		if err := s.State.k8s.releasePods(ctx, landed...); err != nil {
			s.Log.Warn("reconcile: release a landed half's pod", "build", b.ID, "err", err)
		}
		return false, false, job, nil
	}
	if _, lErr := s.State.k8s.launchIndexBuild(ctx, b.Org, b.Image, b.ID, b.Platforms, b.Tags...); lErr != nil {
		if !apierrors.IsAlreadyExists(lErr) {
			return false, false, job, lErr
		}
		return false, false, job, nil // another replica got there first
	}
	s.Log.Info("build index launched", "build", b.ID, "image", b.Image, "tags", b.Tags, "platforms", b.Platforms)
	return false, false, job, nil
}

// buildOverdue is whether a build has run past limit, and whether it is still waiting its
// turn. The clock starts when its first Job does, so time in the line is not time spent: a
// build that waited forty minutes for a place is not stuck at its first minute running. A read
// that fails decides nothing, and a build whose Jobs are gone is timed from its record.
func buildOverdue(s *cloud.Service[state], ctx context.Context, b Build, limit time.Duration) (overdue, queued bool) {
	waiting, started, err := s.State.k8s.buildStarted(ctx, b.ID)
	switch {
	case err != nil:
		return false, false
	case waiting:
		return false, true
	case started.IsZero():
		started = time.Unix(b.CreatedAt, 0)
	}
	return time.Since(started) > limit, false
}

// markBuilding records that a build has left the line: its row says queued until then.
func markBuilding(s *cloud.Service[state], ctx context.Context, b Build) {
	if b.Status != "queued" {
		return
	}
	b.Status, b.UpdatedAt = "building", time.Now().Unix()
	if err := s.State.store.UpdateBuild(ctx, b); err != nil {
		s.Log.Warn("reconcile: record a build started", "build", b.ID, "err", err)
	}
}

// giveUp records a build failed at its deadline, after giving back the build slot
// of every Job it launched that never reached a node. A release that fails leaves
// the row unfinished, so the next tick tries both again: a row marked failed is
// never revisited, and a Job left behind would keep its slot.
func giveUp(s *cloud.Service[state], ctx context.Context, b Build) {
	n, err := s.State.k8s.releaseUnscheduled(ctx, b)
	if err != nil {
		s.Log.Warn("reconcile: release a stuck build's Jobs", "build", b.ID, "err", err)
		return
	}
	if n > 0 {
		s.Log.Info("reconcile: released an unscheduled build's slot", "build", b.ID, "jobs", n)
	}
	finishDirectBuild(s, ctx, b, "failed", stuckReason(s, ctx, b), "")
}

// stuckReason is why a build reached its deadline: what the cluster says of its
// Jobs (a pod waiting on a missing Secret names it), else the deadline itself.
func stuckReason(s *cloud.Service[state], ctx context.Context, b Build) string {
	jobs := []string{b.JobName}
	if len(b.Platforms) > 1 {
		for _, p := range b.Platforms {
			jobs = append(jobs, buildJobName(b.ID, nativeArch[p]))
		}
	}
	for _, j := range jobs {
		if r := s.State.k8s.jobReason(ctx, j); r != "" {
			return r
		}
	}
	// Nothing failed, so the Job is still waiting or still working, and which of
	// the two is the answer: a build that never got a node and one that ran out
	// of time on its last step are different problems with different owners.
	for _, j := range jobs {
		if st := s.State.k8s.jobState(ctx, j, time.Now()); st != "" {
			return fmt.Sprintf("no result within %s: %s", stuckAfter(b), st)
		}
	}
	return fmt.Sprintf("no result within %s", stuckAfter(b))
}

// finishDirectBuild writes one terminal status. Its only job is to make the row
// tell the truth, so a write that fails is logged and retried next tick rather
// than silently dropped — a build whose outcome cannot be recorded is exactly the
// state this function exists to end.
func finishDirectBuild(s *cloud.Service[state], ctx context.Context, b Build, status, reason, digest string) {
	b.Status, b.Reason, b.UpdatedAt = status, reason, time.Now().Unix()
	if digest != "" {
		b.Digest = digest
	}
	if err := s.State.store.UpdateBuild(ctx, b); err != nil {
		s.Log.Warn("reconcile: record build outcome", "build", b.ID, "status", status, "err", err)
		return
	}
	s.Log.Info("build finished", "build", b.ID, "image", b.Image, "status", status, "digest", b.Digest, "reason", reason,
		"seconds", b.UpdatedAt-b.CreatedAt, "jobs", buildTimes(s, ctx, b))
}

// buildTimes is each of a build's Jobs with how long it waited for a node and
// how long it ran — one per architecture of a fan-out, then its index — so a
// slow build says whether it queued or computed, and on which half.
func buildTimes(s *cloud.Service[state], ctx context.Context, b Build) string {
	var out []string
	for _, j := range buildJobs(b) {
		if t := s.State.k8s.jobTimes(ctx, j); t != "" {
			out = append(out, j+": "+t)
		}
	}
	return strings.Join(out, "; ")
}

// buildJobs are the Jobs one build runs, in the order it runs them: its one Job,
// or for a build across several architectures one per architecture and then the
// index that joins them.
func buildJobs(b Build) []string {
	if len(b.Platforms) < 2 {
		return []string{b.JobName}
	}
	var jobs []string
	for _, p := range b.Platforms {
		jobs = append(jobs, buildJobName(b.ID, nativeArch[p]))
	}
	return append(jobs, b.JobName)
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
	lost := time.Now().Unix()-d.CreatedAt > int64(buildDeadline.Seconds())

	b, err := s.State.store.GetBuild(ctx, d.Org, d.BuildID)
	if err != nil || b.JobName == "" {
		if lost {
			failBuild(s, ctx, d, b, "build record/job missing past deadline")
		}
		return
	}

	done, succeeded, jErr := s.State.k8s.jobResult(ctx, b.JobName)
	if jErr != nil {
		// Job not found (TTL-cleaned) or transient apiserver error. Only give up
		// once the deadline has passed; otherwise wait for the next tick.
		if lost {
			failBuild(s, ctx, d, b, "build job not found past deadline: "+jErr.Error())
		}
		return
	}
	if !done {
		overdue, queued := buildOverdue(s, ctx, b, buildDeadline)
		switch {
		case overdue:
			if _, err := s.State.k8s.releaseUnscheduled(ctx, b); err != nil {
				s.Log.Warn("reconcile: release a stuck build's Job", "build", b.ID, "err", err)
				return
			}
			failBuild(s, ctx, d, b, "build exceeded deadline")
		case !queued:
			markBuilding(s, ctx, b)
		}
		return
	}
	if !succeeded {
		failBuild(s, ctx, d, b, "build job failed")
		return
	}
	overdue, _ := buildOverdue(s, ctx, b, buildDeadline)

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
