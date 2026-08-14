// deploy.go — the deploy lifecycle for /v1/platform: build (arcd BuildKit) +
// deploy (operator Service CR) + start/stop + deployment history/logs.
//
// Two source kinds, ONE deploy mechanic (write the operator Service CR into
// tenant-<org>; the operator reconciles):
//
//   - source=image — no build. The CR is applied immediately with the requested
//     image tag; the operator rolls it. This is the fully end-to-end path.
//   - source=git   — a build is required. An in-cluster BuildKit Job is launched
//     (client-go, the arcd model) to build the repo and push the per-tenant
//     image; the deployment lands "building". The build watcher that flips
//     "building"→"live" by applying the CR with the built image is phase 2
//     (platform-in-cloud §4) — until then the git path stops honestly at
//     "building" with the real Job reference, never a fabricated "live".
//
// Every handler is org-scoped (s.tenant) and every cluster write targets
// tenant-<org> derived from the validated org — never a request value.

package platform

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// deployReq is what to deploy. Commit and Tag carry `url:"-"` because the URL
// addresses the APP and this route has never taken the artifact off the query
// string; without the opt-out `?tag=other` would silently redeploy something the
// body did not ask for.
type deployReq struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
	// Commit is the git commit or ref to build, for a git-source app. Defaults to
	// the app's branch.
	Commit string `json:"commit" url:"-"`
	// Tag is the image tag to deploy, for an image-source app. Defaults to the
	// app's tag, then `latest`.
	Tag string `json:"tag" url:"-"`
}

// inflightGate bounds concurrent in-flight SYNCHRONOUS image deploys PER ORG (L1).
// The git build path is already capped in the cluster (countActiveBuilds →
// errTooManyBuilds → 429); the image path has no build Job to count, so cloud-api
// counts in-flight deploys itself. It exists because deployImage's applyLive may
// park up to ~45s in waitForTenantRBAC on a cold-start / wedged operator, and
// without a cap a single validated org could pile up that many held request
// goroutines. Fail-closed and retryable: over-cap refuses with 429, never proceeds
// unbounded. Process-local (per replica), which is exactly the goroutine-pile-up
// boundary each replica needs; the map is pruned to zero entries so keys never grow
// unbounded.
type inflightGate struct {
	mu sync.Mutex
	n  map[string]int
}

// acquire reserves one in-flight slot for org, or reports false when org is already
// at max (caller must 429). Balanced by exactly one release on the success path.
func (g *inflightGate) acquire(org string, max int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == nil {
		g.n = map[string]int{}
	}
	if g.n[org] >= max {
		return false
	}
	g.n[org]++
	return true
}

// release returns org's slot; pruning the key at zero keeps the map bounded.
func (g *inflightGate) release(org string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n[org] <= 1 {
		delete(g.n, org)
		return
	}
	g.n[org]--
}

// deploy deploys the app — building it first if it comes from git.
//
// It starts a new, monotonically versioned deployment of the app and answers 202
// with the deployment record. A 202 is an ACCEPTED deployment, not a live one.
//
// An IMAGE app deploys the tag you name (falling back to the app's tag, then
// `latest`) by writing its operator Service CR; the operator reconciles it to
// running. A GIT app launches an in-cluster BuildKit Job at `commit` — or the app's
// branch — and comes back in `building`; the Service CR is applied later, by the
// reconciler, once the Job succeeds. The reconciler is restart-safe, so a build in
// flight survives a cloud restart.
//
// Deploys are bounded per org: over the concurrent-deploy cap is 429 and NOTHING is
// recorded, so a rejected deploy leaves no phantom in the history. An unreachable
// cluster is 503 but still records an honest `error` deployment, because a deploy
// that was attempted and failed must not be indistinguishable from one never made.
// Every other failure is likewise recorded in its real terminal state.
//
// This is metered work: a git build is billed to the org's ledger in wall-clock
// build minutes once the Job finishes, and the running deployment is billed for its
// compute per tick for as long as it stays live. Requires a validated principal; 403
// without one, and everything is written into that org's own `tenant-<org>`
// namespace.
func (o ops) deploy(ctx context.Context, body *deployReq) (*deploymentView, error) {
	s := o.s
	c, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	project, a, err := loadApp(s, ctx, org, body.Project, body.App)
	if err != nil {
		return nil, err
	}

	// Fail closed if the cluster is unreachable — but still record an honest
	// "error" deployment so the history reflects the attempt.
	clusterErr := s.State.k8s.ready()

	now := time.Now().Unix()
	depID, version, err := nextDeployment(s, ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "allocate deployment: %v", err)
	}

	switch a.Source {
	case "image":
		return deployImage(s, ctx, c, org, project, a, depID, version, now, *body, clusterErr)
	case "git":
		return deployGit(s, ctx, c, org, a, depID, version, now, *body, clusterErr)
	default:
		return nil, zip.ErrBadRequest("application has an unknown source; recreate it with source git|image")
	}
}

// nextDeployment allocates the (id, version) for a new deployment of app appID —
// the next monotonic per-app version plus a fresh deployment id. ONE place, shared
// by the deploy handler and the preview/promote/rollback flows (preview.go).
func nextDeployment(s *cloud.Service[state], ctx context.Context, appID string) (string, int, error) {
	version, err := s.State.store.NextVersion(ctx, appID)
	if err != nil {
		return "", 0, fmt.Errorf("version: %w", err)
	}
	depID := genID("dep")
	return depID, version, nil
}

// deployImage resolves the image-source app's target tag and hands off to the ONE
// image-deploy core (deployTagCore), mapping its (deployment, status) result onto
// the /deploy response — 202 + the deployment view on success, the honest status +
// message on failure.
func deployImage(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, org, project string, a Application, depID string, version int, now int64, body deployReq, clusterErr error) (*deploymentView, error) {
	tag := cmp.Or(strings.TrimSpace(body.Tag), a.ImageTag, "latest")
	image := a.ImageRepo + ":" + tag
	d, status, err := deployTagCore(s, ctx, org, project, a, depID, version, now, image, tag, "image", "", clusterErr)
	if err != nil {
		return nil, zip.Errorf(status, "%s", err.Error())
	}
	s.Log.Info("deployed (image)", "org", org, "app", a.Slug, "ns", tenantNamespace(org), "image", image,
		"actor", c.User(), "requestID", c.RequestID())
	v := toDeploymentView(d)
	return &v, nil
}

// deployTagCore is the ctx-only core that deploys an ALREADY-BUILT image ref
// (repo:tag) to app a as one new versioned deployment, through the ONE CR writer
// (applyLive). It is the shared mechanic behind the image deploy (deployImage) AND
// the Vercel-style flows in preview.go (preview / promote / rollback), so none of
// them duplicate the deploy gate, the deployment record, or the Service-CR write.
//
// It bounds concurrent in-flight deploys per org (the L1 gate) BEFORE recording
// anything — over-cap is a retryable 429, never a recorded attempt — then inserts
// the "deploying" deployment and applies the Service CR + finalizes live as ONE
// per-app-serialized, version-monotonic step (RED LOW-1). Every failure is recorded
// in its honest terminal state (failDeploymentCtx) and returned as a (deployment,
// HTTP status, error) triple the HTTP caller maps to its own surface. source/commit
// are stamped onto the row so the history reflects what was deployed (image | git,
// and the git ref when rolling back a built commit).
func deployTagCore(s *cloud.Service[state], ctx context.Context, org, project string, a Application, depID string, version int, now int64, image, tag, source, commit string, clusterErr error) (Deployment, int, error) {
	if !s.State.deployGate.acquire(org, s.State.k8s.limits.maxConcurrentDeploys()) {
		return Deployment{}, http.StatusTooManyRequests, fmt.Errorf("too many concurrent deploys for this org; retry shortly")
	}
	defer s.State.deployGate.release(org)

	d := Deployment{
		ID: depID, Org: org, ApplicationID: a.ID, Version: version, Status: "deploying",
		Source: source, Commit: commit, Image: image, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.InsertDeployment(ctx, d); err != nil {
		return Deployment{}, http.StatusInternalServerError, fmt.Errorf("persist deployment: %v", err)
	}
	if clusterErr != nil {
		msg := "cluster unavailable: " + clusterErr.Error()
		failDeploymentCtx(s, ctx, &d, a, http.StatusServiceUnavailable, msg)
		return d, http.StatusServiceUnavailable, fmt.Errorf("deploy failed: %s", msg)
	}
	advanced, superseded, err := applyLive(s, ctx, org, project, a, d, tag, image, time.Now().Unix())
	if err != nil {
		status := deployErrStatus(err)
		msg := "apply Service CR: " + err.Error()
		failDeploymentCtx(s, ctx, &d, a, status, msg)
		return d, status, fmt.Errorf("deploy failed: %s", msg)
	}
	if superseded || !advanced {
		s.Log.Info("deploy superseded by a newer concurrent deploy (app already at a newer version)",
			"org", org, "app", a.Slug, "version", version)
	}
	return d, http.StatusAccepted, nil
}

// deployGit is the HTTP deploy for a git-source app: it maps the shared build
// core (startGitBuild) onto the /v1/platform/.../deploy response — 202 + the
// deployment view on success, the honest status + message on failure.
func deployGit(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, org string, a Application, depID string, version int, now int64, body deployReq, clusterErr error) (*deploymentView, error) {
	d, jobName, status, err := startGitBuild(s, ctx, org, a, depID, version, now, body.Commit, clusterErr)
	if err != nil {
		return nil, zip.Errorf(status, "%s", err.Error())
	}
	s.Log.Info("build launched (git)", "org", org, "app", a.Slug, "job", jobName, "image", d.Image,
		"actor", c.User(), "requestID", c.RequestID())
	v := toDeploymentView(d)
	return &v, nil
}

// startGitBuild is the ctx-only core of a git deploy: persist the build +
// deployment "building", launch the in-cluster BuildKit Job, and flip the app to
// "building". The phase-2 reconciler applies the Service CR once the Job succeeds.
//
// It is the ONE build-launch path — shared by the HTTP deploy (deployGit) and the
// git-push-to-deploy trigger (buildFromPush in push.go). No *zip.Ctx: every failure
// is recorded in its honest terminal state via failDeploymentCtx and returned as a
// pre-formatted (message, HTTP status) pair the caller maps to its own surface.
func startGitBuild(s *cloud.Service[state], ctx context.Context, org string, a Application, depID string, version int, now int64, commit string, clusterErr error) (Deployment, string, int, error) {
	if strings.TrimSpace(a.RepoURL) == "" {
		return Deployment{}, "", http.StatusBadRequest, fmt.Errorf("git application has no repo URL")
	}
	ref := cmp.Or(strings.TrimSpace(commit), a.RepoBranch, "main")
	image := s.State.k8s.buildImageRef(org, a.Slug, shortTag(ref))

	bldID := genID("bld")
	b := Build{ID: bldID, Org: org, ApplicationID: a.ID, DeploymentID: depID, Status: "queued", Image: image, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(ctx, b); err != nil {
		return Deployment{}, "", http.StatusInternalServerError, fmt.Errorf("persist build: %v", err)
	}
	d := Deployment{
		ID: depID, Org: org, ApplicationID: a.ID, Version: version, Status: "building",
		Source: "git", Commit: ref, Image: image, BuildID: bldID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.InsertDeployment(ctx, d); err != nil {
		return Deployment{}, "", http.StatusInternalServerError, fmt.Errorf("persist deployment: %v", err)
	}

	if clusterErr != nil {
		b.Status, b.UpdatedAt = "failed", time.Now().Unix()
		_ = s.State.store.UpdateBuild(ctx, b)
		msg := "cluster unavailable: " + clusterErr.Error()
		failDeploymentCtx(s, ctx, &d, a, http.StatusServiceUnavailable, msg)
		return d, "", http.StatusServiceUnavailable, fmt.Errorf("deploy failed: %s", msg)
	}

	jobName, err := s.State.k8s.launchBuildJob(ctx, org, a, image, ref, bldID)
	if err != nil {
		b.Status, b.UpdatedAt = "failed", time.Now().Unix()
		_ = s.State.store.UpdateBuild(ctx, b)
		status := deployErrStatus(err)
		msg := "launch build job: " + err.Error()
		failDeploymentCtx(s, ctx, &d, a, status, msg)
		return d, "", status, fmt.Errorf("deploy failed: %s", msg)
	}

	b.Status, b.JobName, b.LogsRef, b.UpdatedAt = "building", jobName, "job/"+jobName, time.Now().Unix()
	if err := s.State.store.UpdateBuild(ctx, b); err != nil {
		s.Log.Warn("update build failed (continuing)", "build", b.ID, "err", err)
	}
	a.Status, a.Namespace, a.UpdatedAt = "building", tenantNamespace(org), time.Now().Unix()
	if err := s.State.store.UpdateApplication(ctx, a); err != nil {
		s.Log.Warn("finalize app failed (continuing)", "app", a.Slug, "err", err)
	}
	emitDeployLifecycle(ctx, cloud.LifecycleBuildStarted, org, a, d, "building "+a.Slug+" ("+image+")")
	return d, jobName, http.StatusAccepted, nil
}

// emitDeployLifecycle fans a deploy transition onto the cloud lifecycle stream so
// the git-lifecycle reactors (Slack-notify) can post about it. Repo is derived from
// the app's RepoURL — the native repo name a subscription keys on; an app with no
// repo URL (an image app) carries an empty Repo and routes to nothing. Project is
// the app's IAM project (the git repo's sub-scope), normalized so the reserved
// "default" maps to the empty (org-level) scope the git store uses — so a deploy
// notification routes to the SAME (org,project,repo) a subscription was created
// under. Best-effort + detached inside EmitLifecycle, so it never affects the deploy.
func emitDeployLifecycle(ctx context.Context, kind cloud.LifecycleKind, org string, a Application, d Deployment, detail string) {
	project := a.ProjectID
	if project == "default" {
		project = ""
	}
	cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
		Kind: kind, Org: org, Project: project, Repo: cloud.RepoFromCloneURL(a.RepoURL),
		Branch: cmp.Or(a.RepoBranch, "main"), After: d.Commit,
		DeployID: d.ID, Detail: detail,
	})
}

// failDeploymentCtx is the ctx-only store write behind failDeployment: it flips the
// deployment + app to "error" with the reason. Shared so the push trigger records
// the same honest failure state without an HTTP context.
func failDeploymentCtx(s *cloud.Service[state], ctx context.Context, d *Deployment, a Application, status int, msg string) {
	d.Status = "error"
	d.Message = msg
	d.UpdatedAt = time.Now().Unix()
	_ = s.State.store.UpdateDeployment(ctx, *d)
	a.Status = "error"
	a.UpdatedAt = time.Now().Unix()
	_ = s.State.store.UpdateApplication(ctx, a)
	s.Log.Error("deploy failed", "org", d.Org, "app", a.Slug, "status", status, "reason", msg)
	emitDeployLifecycle(ctx, cloud.LifecycleDeployFailed, d.Org, a, *d, a.Slug+": "+msg)
}

// deployErrStatus maps a cluster/build error to an honest HTTP status. A tenant
// at its concurrent-build ceiling is 429 (retryable client condition, MED-3); a
// brand-new tenant whose operator RBAC has not yet landed is 503 (retryable,
// self-heals on the operator's next reconcile — waitForTenantRBAC); an invalid
// build input is 400 (client must fix repo.url/dockerfile/ref, CRIT-1);
// RBAC/forbidden and other cluster errors surface as 502 (the cloud SA needs a
// grant or the cluster is unhappy).
func deployErrStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, errTooManyBuilds):
		return http.StatusTooManyRequests
	case errors.Is(err, errTenantProvisioning):
		return http.StatusServiceUnavailable
	case strings.Contains(err.Error(), "invalid build input"):
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

// ── start / stop ─────────────────────────────────────────────────────────────

// stop stops an app without deleting it.
//
// It scales the app's Service to zero replicas and marks it stopped, answering the
// updated application. Nothing else is removed — the record, its env, its domains
// and its deployment history all survive, and /start brings it back at the same
// replica count.
//
// An app that is not deployed has no Service CR to scale and is 404. An
// unreachable cluster is 503 and a cluster that refuses the scale is 502. Because
// the pods stop, so does the compute metering. Requires a validated principal; 403
// without one.
func (o ops) stop(ctx context.Context, in *appRef) (*appView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	if err := s.State.k8s.ready(); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}
	if err := s.State.k8s.scaleService(ctx, org, a.Slug, 0); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("application is not deployed (no Service CR to stop)")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "scale to zero: %v", err)
	}
	a.Status, a.UpdatedAt = "stopped", time.Now().Unix()
	if err := s.State.store.UpdateApplication(ctx, a); err != nil {
		s.Log.Warn("persist stop failed (continuing)", "app", a.Slug, "err", err)
	}
	v := toAppView(a)
	return &v, nil
}

// start starts a stopped app back up.
//
// It scales the app's Service back to its configured replica count and marks it
// live, answering the updated application. It does not redeploy: the image already
// on the Service CR is what comes back.
//
// The billing watermark is reset to now as part of starting, so the org is charged
// for THIS live span and never for the gap the app spent stopped. An app with no
// Service CR is 404, an unreachable cluster is 503, and a cluster that refuses the
// scale is 502. Requires a validated principal; 403 without one.
func (o ops) start(ctx context.Context, in *appRef) (*appView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	if err := s.State.k8s.ready(); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}
	if err := s.State.k8s.scaleService(ctx, org, a.Slug, max1(a.Replicas)); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("application is not deployed (no Service CR to start)")
		}
		return nil, zip.Errorf(http.StatusBadGateway, "scale up: %v", err)
	}
	a.Status, a.UpdatedAt = "live", time.Now().Unix()
	if err := s.State.store.UpdateApplication(ctx, a); err != nil {
		s.Log.Warn("persist start failed (continuing)", "app", a.Slug, "err", err)
	}
	// Reset the compute watermark to now so the meter bills only THIS live span,
	// never the stopped gap the app just resumed from (FinalizeLive does the same for
	// the deploy→live path).
	if err := s.State.store.StampComputeMeter(ctx, a.Org, a.ID, a.UpdatedAt); err != nil {
		s.Log.Warn("stamp compute meter failed (continuing)", "app", a.Slug, "err", err)
	}
	v := toAppView(a)
	return &v, nil
}

// ── deployment history + logs ────────────────────────────────────────────────

// deploymentList is one app's deployment history as a list answers it — a bare
// JSON array, named so the document can describe it.
type deploymentList []deploymentView

// listDeployments returns an app's deployment history.
//
// It lists every deployment recorded for one of the caller org's applications,
// newest version first, each with its version, status, source, commit and image.
// Failed and superseded attempts are included — that is the point of a history.
// Requires a validated principal; 403 without one.
func (o ops) listDeployments(ctx context.Context, in *appRef) (*deploymentList, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.ListDeployments(ctx, org, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	out := make(deploymentList, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDeploymentView(d))
	}
	return &out, nil
}

// getDeployment returns one deployment of one app.
//
// It returns a single deployment by id, scoped to the named application of the
// caller's org — so an id belonging to another app or another tenant is 404, not a
// read. Requires a validated principal; 403 without one.
func (o ops) getDeployment(ctx context.Context, in *deploymentRef) (*deploymentView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	d, err := s.State.store.GetDeployment(ctx, org, a.ID, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}
	v := toDeploymentView(d)
	return &v, nil
}

// deployLogs is what one deployment's log read answers: the recorded timeline plus
// whatever live pod output was reachable, and which pod that output came from.
type deployLogs struct {
	// DeploymentID is the deployment these logs belong to.
	DeploymentID string `json:"deploymentId"`
	// Source says which pod the log body carries — `build`, `app`, or `none` when
	// neither pod was reachable — so a console can label the pane honestly.
	Source string `json:"source"`
	// Logs is the recorded status timeline followed by the streamed pod output,
	// newline-separated.
	Logs string `json:"logs"`
}

// deploymentLogs returns real logs for a deployment — the build's, then the app's.
//
// It returns the deployment's recorded status timeline together with LIVE pod logs
// pulled from the cluster: the build pod's output while a git build is running, and
// the running app's output once it is deployed. The `source` field says which of
// the two the body is — `build`, `app` or `none` — so a console can label the pane
// honestly.
//
// It never fabricates log content. When no pod exists yet, or the cluster is
// unreachable, it degrades to the recorded timeline and says so. Every cluster read
// is confined to the caller org's own namespaces and time-boxed. Requires a
// validated principal; 403 without one.
func (o ops) deploymentLogs(ctx context.Context, in *deploymentRef) (*deployLogs, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	d, err := s.State.store.GetDeployment(ctx, org, a.ID, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}

	// Recorded timeline (always real, always present).
	var lines []string
	lines = append(lines, fmt.Sprintf("deployment %s v%d source=%s status=%s", d.ID, d.Version, d.Source, d.Status))
	if d.Image != "" {
		lines = append(lines, "image: "+d.Image)
	}

	// source classifies which pod's logs the `logs` body carries so the console can
	// label the pane. A git deployment shows the BUILD pod's logs (the interesting
	// signal while it's building or if it failed); an image/live deployment shows the
	// APP pod's logs. "none" when neither pod is reachable yet.
	source := "none"
	if d.Source == "git" && d.BuildID != "" {
		if b, bErr := s.State.store.GetBuild(ctx, org, d.BuildID); bErr == nil {
			var streamed bool
			lines, streamed = buildLogContext(s, ctx, d, b, lines)
			if streamed {
				source = "build"
			}
		}
	}

	// Once an app is (or is going) live, ALSO surface the running app pod's logs —
	// the runtime signal the user needs after a deploy. Appended after the build
	// context so a git deploy shows both build and runtime when both exist.
	if a.Status == "live" || a.Status == "deploying" || d.Source == "image" {
		if logs, ok := s.State.k8s.appLogs(ctx, org, a.Slug); ok {
			lines = append(lines, "── app logs ("+tenantNamespace(org)+"/"+a.Slug+") ──", logs)
			source = "app"
		}
	}

	if d.Message != "" {
		lines = append(lines, "message: "+d.Message)
	}
	return &deployLogs{DeploymentID: d.ID, Source: source, Logs: strings.Join(lines, "\n")}, nil
}

// shortTag derives a short, image-tag-safe token from a git ref/commit.
func shortTag(ref string) string {
	ref = strings.ToLower(strings.TrimSpace(ref))
	var b strings.Builder
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		out = "build"
	}
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}
