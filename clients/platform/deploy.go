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
//     (paas-in-cloud.md §4) — until then the git path stops honestly at
//     "building" with the real Job reference, never a fabricated "live".
//
// Every handler is org-scoped (s.tenant) and every cluster write targets
// tenant-<org> derived from the validated org — never a request value.
package platform

import (
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

type deployReq struct {
	Commit string `json:"commit"` // git commit/ref to build (git-source)
	Tag    string `json:"tag"`    // image tag to deploy (image-source)
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

func deploy(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	project, a, herr := loadApp(s, c, org)
	if herr != nil {
		return herr
	}
	var body deployReq
	if len(c.Body()) > 0 {
		if err := c.Bind(&body); err != nil {
			return err
		}
	}

	// Fail closed if the cluster is unreachable — but still record an honest
	// "error" deployment so the history reflects the attempt.
	clusterErr := s.State.k8s.ready()

	now := time.Now().Unix()
	depID, version, err := nextDeployment(s, c.Context(), a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "allocate deployment: %v", err)
	}

	switch a.Source {
	case "image":
		return deployImage(s, c, org, project, a, depID, version, now, body, clusterErr)
	case "git":
		return deployGit(s, c, org, a, depID, version, now, body, clusterErr)
	default:
		return zip.ErrBadRequest("application has an unknown source; recreate it with source git|image")
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
	depID, err := genID("dep")
	if err != nil {
		return "", 0, fmt.Errorf("rng: %w", err)
	}
	return depID, version, nil
}

// deployImage resolves the image-source app's target tag and hands off to the ONE
// image-deploy core (deployTagCore), mapping its (deployment, status) result onto
// the /deploy response — 202 + the deployment view on success, the honest status +
// message on failure.
func deployImage(s *cloud.Service[state], c *zip.Ctx, org, project string, a Application, depID string, version int, now int64, body deployReq, clusterErr error) error {
	tag := firstNonEmpty(strings.TrimSpace(body.Tag), a.ImageTag, "latest")
	image := a.ImageRepo + ":" + tag
	d, status, err := deployTagCore(s, c.Context(), org, project, a, depID, version, now, image, tag, "image", "", clusterErr)
	if err != nil {
		return zip.Errorf(status, "%s", err.Error())
	}
	s.Log.Info("deployed (image)", "org", org, "app", a.Slug, "ns", tenantNamespace(org), "image", image,
		"actor", c.User(), "requestID", c.RequestID())
	return c.JSON(status, toDeploymentView(d))
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
func deployGit(s *cloud.Service[state], c *zip.Ctx, org string, a Application, depID string, version int, now int64, body deployReq, clusterErr error) error {
	d, jobName, status, err := startGitBuild(s, c.Context(), org, a, depID, version, now, body.Commit, clusterErr)
	if err != nil {
		return zip.Errorf(status, "%s", err.Error())
	}
	s.Log.Info("build launched (git)", "org", org, "app", a.Slug, "job", jobName, "image", d.Image,
		"actor", c.User(), "requestID", c.RequestID())
	return c.JSON(status, toDeploymentView(d))
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
	ref := firstNonEmpty(strings.TrimSpace(commit), a.RepoBranch, "main")
	image := s.State.k8s.buildImageRef(org, a.Slug, shortTag(ref))

	bldID, err := genID("bld")
	if err != nil {
		return Deployment{}, "", http.StatusInternalServerError, fmt.Errorf("rng: %v", err)
	}
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
		Branch: firstNonEmpty(a.RepoBranch, "main"), After: d.Commit,
		DeployID: d.ID, Detail: detail,
	})
}

// failDeployment records the honest failure on the deployment + app and returns
// the mapped HTTP error. No fabricated success ever reaches the caller.
func failDeployment(s *cloud.Service[state], c *zip.Ctx, d *Deployment, a Application, status int, msg string) error {
	failDeploymentCtx(s, c.Context(), d, a, status, msg)
	return zip.Errorf(status, "deploy failed: %s", msg)
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

func stop(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := loadApp(s, c, org)
	if herr != nil {
		return herr
	}
	if err := s.State.k8s.ready(); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}
	if err := s.State.k8s.scaleService(c.Context(), org, a.Slug, 0); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("application is not deployed (no Service CR to stop)")
		}
		return zip.Errorf(http.StatusBadGateway, "scale to zero: %v", err)
	}
	a.Status, a.UpdatedAt = "stopped", time.Now().Unix()
	if err := s.State.store.UpdateApplication(c.Context(), a); err != nil {
		s.Log.Warn("persist stop failed (continuing)", "app", a.Slug, "err", err)
	}
	return c.JSON(http.StatusOK, toAppView(a))
}

func start(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := loadApp(s, c, org)
	if herr != nil {
		return herr
	}
	if err := s.State.k8s.ready(); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}
	if err := s.State.k8s.scaleService(c.Context(), org, a.Slug, max1(a.Replicas)); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("application is not deployed (no Service CR to start)")
		}
		return zip.Errorf(http.StatusBadGateway, "scale up: %v", err)
	}
	a.Status, a.UpdatedAt = "live", time.Now().Unix()
	if err := s.State.store.UpdateApplication(c.Context(), a); err != nil {
		s.Log.Warn("persist start failed (continuing)", "app", a.Slug, "err", err)
	}
	// Reset the compute watermark to now so the meter bills only THIS live span,
	// never the stopped gap the app just resumed from (FinalizeLive does the same for
	// the deploy→live path).
	if err := s.State.store.StampComputeMeter(c.Context(), a.Org, a.ID, a.UpdatedAt); err != nil {
		s.Log.Warn("stamp compute meter failed (continuing)", "app", a.Slug, "err", err)
	}
	return c.JSON(http.StatusOK, toAppView(a))
}

// ── deployment history + logs ────────────────────────────────────────────────

func listDeployments(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := loadApp(s, c, org)
	if herr != nil {
		return herr
	}
	rows, err := s.State.store.ListDeployments(c.Context(), org, a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	out := make([]deploymentView, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDeploymentView(d))
	}
	return c.JSON(http.StatusOK, out)
}

func getDeployment(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := loadApp(s, c, org)
	if herr != nil {
		return herr
	}
	d, err := s.State.store.GetDeployment(c.Context(), org, a.ID, strings.TrimSpace(c.Param("id")))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}
	return c.JSON(http.StatusOK, toDeploymentView(d))
}

// deploymentLogs returns REAL logs for a deployment: the recorded status timeline
// PLUS the live pod logs streamed from the cluster — the build pod's logs while a
// git build runs, and the running app pod's logs once deployed. Every cluster read
// is org-scoped (build logs by the deterministic job-name label in the build ns; app
// logs from tenant-<org>) and time-boxed; when a pod is not yet present or the
// cluster is unreachable it degrades to the recorded timeline and says so honestly —
// it NEVER fabricates log content. The `source` field tells the console what the
// `logs` body is (build|app|none) so it can label the pane.
func deploymentLogs(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := loadApp(s, c, org)
	if herr != nil {
		return herr
	}
	d, err := s.State.store.GetDeployment(c.Context(), org, a.ID, strings.TrimSpace(c.Param("id")))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
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
		if b, bErr := s.State.store.GetBuild(c.Context(), org, d.BuildID); bErr == nil {
			var streamed bool
			lines, streamed = buildLogContext(s, c.Context(), d, b, lines)
			if streamed {
				source = "build"
			}
		}
	}

	// Once an app is (or is going) live, ALSO surface the running app pod's logs —
	// the runtime signal the user needs after a deploy. Appended after the build
	// context so a git deploy shows both build and runtime when both exist.
	if a.Status == "live" || a.Status == "deploying" || d.Source == "image" {
		if logs, ok := s.State.k8s.appLogs(c.Context(), org, a.Slug); ok {
			lines = append(lines, "── app logs ("+tenantNamespace(org)+"/"+a.Slug+") ──", logs)
			source = "app"
		}
	}

	if d.Message != "" {
		lines = append(lines, "message: "+d.Message)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deploymentId": d.ID,
		"source":       source,
		"logs":         strings.Join(lines, "\n"),
	})
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
