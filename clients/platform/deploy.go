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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

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

func (s *svc) deploy(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	project, a, herr := s.loadApp(c, org)
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
	clusterErr := s.k8s.ready()

	now := time.Now().Unix()
	version, err := s.store.NextVersion(c.Context(), a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "version: %v", err)
	}
	depID, err := genID("dep")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}

	switch a.Source {
	case "image":
		return s.deployImage(c, org, project.Slug, a, depID, version, now, body, clusterErr)
	case "git":
		return s.deployGit(c, org, a, depID, version, now, body, clusterErr)
	default:
		return zip.ErrBadRequest("application has an unknown source; recreate it with source git|image")
	}
}

// deployImage applies the operator Service CR with the requested image tag and
// lands the deployment "deploying" (the operator finishes the rollout async).
func (s *svc) deployImage(c *zip.Ctx, org, project string, a Application, depID string, version int, now int64, body deployReq, clusterErr error) error {
	// (L1) Bound concurrent in-flight deploys for this org: applyLive below may park
	// up to ~45s in waitForTenantRBAC on a cold-start / wedged operator, so a cap
	// prevents one org piling up held request goroutines (mirrors the git build cap).
	// Over-cap is a retryable THROTTLE, not a deploy failure — refuse with 429 BEFORE
	// recording any attempt, since nothing was tried cluster-side.
	if !s.deployGate.acquire(org, s.k8s.limits.maxConcurrentDeploys()) {
		return zip.Errorf(http.StatusTooManyRequests, "too many concurrent deploys for this org; retry shortly")
	}
	defer s.deployGate.release(org)

	tag := firstNonEmpty(strings.TrimSpace(body.Tag), a.ImageTag, "latest")
	image := a.ImageRepo + ":" + tag

	d := Deployment{
		ID: depID, Org: org, ApplicationID: a.ID, Version: version, Status: "deploying",
		Source: "image", Image: image, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}

	if clusterErr != nil {
		return s.failDeployment(c, &d, a, http.StatusServiceUnavailable, "cluster unavailable: "+clusterErr.Error())
	}

	// Write the CR and advance the app to live as ONE per-app-serialized,
	// version-monotonic step (shared with the git build reconciler): the CR write
	// is jointly ordered with the live finalize under a per-app lock so two
	// concurrent deploys can never leave the live Service CR image lagging the
	// recorded live version (RED LOW-1). The operator reconciles the rollout.
	advanced, superseded, err := s.applyLive(c.Context(), org, project, a, d, tag, image, time.Now().Unix())
	if err != nil {
		return s.failDeployment(c, &d, a, deployErrStatus(err), "apply Service CR: "+err.Error())
	}
	if superseded || !advanced {
		s.log.Info("deploy superseded by a newer concurrent deploy (app already at a newer version)",
			"org", org, "app", a.Slug, "version", version)
	}
	s.log.Info("deployed (image)", "org", org, "app", a.Slug, "ns", tenantNamespace(org), "image", image,
		"actor", c.User(), "requestID", c.RequestID())
	return c.JSON(http.StatusAccepted, toDeploymentView(d))
}

// deployGit creates a build record, launches an in-cluster BuildKit Job, and
// lands the deployment "building". Completion (apply CR with the built image) is
// the phase-2 watcher.
func (s *svc) deployGit(c *zip.Ctx, org string, a Application, depID string, version int, now int64, body deployReq, clusterErr error) error {
	if strings.TrimSpace(a.RepoURL) == "" {
		return zip.ErrBadRequest("git application has no repo URL")
	}
	ref := firstNonEmpty(strings.TrimSpace(body.Commit), a.RepoBranch, "main")
	image := s.k8s.buildImageRef(org, a.Slug, shortTag(ref))

	bldID, err := genID("bld")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	b := Build{ID: bldID, Org: org, ApplicationID: a.ID, DeploymentID: depID, Status: "queued", Image: image, CreatedAt: now, UpdatedAt: now}
	if err := s.store.InsertBuild(c.Context(), b); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist build: %v", err)
	}
	d := Deployment{
		ID: depID, Org: org, ApplicationID: a.ID, Version: version, Status: "building",
		Source: "git", Commit: ref, Image: image, BuildID: bldID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}

	if clusterErr != nil {
		b.Status = "failed"
		b.UpdatedAt = time.Now().Unix()
		_ = s.store.UpdateBuild(c.Context(), b)
		return s.failDeployment(c, &d, a, http.StatusServiceUnavailable, "cluster unavailable: "+clusterErr.Error())
	}

	jobName, err := s.k8s.launchBuildJob(c.Context(), org, a, image, ref, bldID)
	if err != nil {
		b.Status = "failed"
		b.UpdatedAt = time.Now().Unix()
		_ = s.store.UpdateBuild(c.Context(), b)
		return s.failDeployment(c, &d, a, deployErrStatus(err), "launch build job: "+err.Error())
	}

	b.Status, b.JobName, b.LogsRef, b.UpdatedAt = "building", jobName, "job/"+jobName, time.Now().Unix()
	if err := s.store.UpdateBuild(c.Context(), b); err != nil {
		s.log.Warn("update build failed (continuing)", "build", b.ID, "err", err)
	}
	a.Status, a.Namespace, a.UpdatedAt = "building", tenantNamespace(org), time.Now().Unix()
	if err := s.store.UpdateApplication(c.Context(), a); err != nil {
		s.log.Warn("finalize app failed (continuing)", "app", a.Slug, "err", err)
	}
	s.log.Info("build launched (git)", "org", org, "app", a.Slug, "job", jobName, "image", image,
		"actor", c.User(), "requestID", c.RequestID())
	return c.JSON(http.StatusAccepted, toDeploymentView(d))
}

// failDeployment records the honest failure on the deployment + app and returns
// the mapped HTTP error. No fabricated success ever reaches the caller.
func (s *svc) failDeployment(c *zip.Ctx, d *Deployment, a Application, status int, msg string) error {
	d.Status = "error"
	d.Message = msg
	d.UpdatedAt = time.Now().Unix()
	_ = s.store.UpdateDeployment(c.Context(), *d)
	a.Status = "error"
	a.UpdatedAt = time.Now().Unix()
	_ = s.store.UpdateApplication(c.Context(), a)
	s.log.Error("deploy failed", "org", d.Org, "app", a.Slug, "status", status, "reason", msg)
	return zip.Errorf(status, "deploy failed: %s", msg)
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

func (s *svc) stop(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	if err := s.k8s.ready(); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}
	if err := s.k8s.scaleService(c.Context(), org, a.Slug, 0); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("application is not deployed (no Service CR to stop)")
		}
		return zip.Errorf(http.StatusBadGateway, "scale to zero: %v", err)
	}
	a.Status, a.UpdatedAt = "stopped", time.Now().Unix()
	if err := s.store.UpdateApplication(c.Context(), a); err != nil {
		s.log.Warn("persist stop failed (continuing)", "app", a.Slug, "err", err)
	}
	return c.JSON(http.StatusOK, toAppView(a))
}

func (s *svc) start(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	if err := s.k8s.ready(); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}
	if err := s.k8s.scaleService(c.Context(), org, a.Slug, max1(a.Replicas)); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("application is not deployed (no Service CR to start)")
		}
		return zip.Errorf(http.StatusBadGateway, "scale up: %v", err)
	}
	a.Status, a.UpdatedAt = "live", time.Now().Unix()
	if err := s.store.UpdateApplication(c.Context(), a); err != nil {
		s.log.Warn("persist start failed (continuing)", "app", a.Slug, "err", err)
	}
	return c.JSON(http.StatusOK, toAppView(a))
}

// ── deployment history + logs ────────────────────────────────────────────────

func (s *svc) listDeployments(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	rows, err := s.store.ListDeployments(c.Context(), org, a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	out := make([]deploymentView, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDeploymentView(d))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *svc) getDeployment(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	d, err := s.store.GetDeployment(c.Context(), org, a.ID, strings.TrimSpace(c.Param("id")))
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
func (s *svc) deploymentLogs(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	d, err := s.store.GetDeployment(c.Context(), org, a.ID, strings.TrimSpace(c.Param("id")))
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
		if b, bErr := s.store.GetBuild(c.Context(), org, d.BuildID); bErr == nil {
			var streamed bool
			lines, streamed = s.buildLogContext(c.Context(), d, b, lines)
			if streamed {
				source = "build"
			}
		}
	}

	// Once an app is (or is going) live, ALSO surface the running app pod's logs —
	// the runtime signal the user needs after a deploy. Appended after the build
	// context so a git deploy shows both build and runtime when both exist.
	if a.Status == "live" || a.Status == "deploying" || d.Source == "image" {
		if logs, ok := s.k8s.appLogs(c.Context(), org, a.Slug); ok {
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
