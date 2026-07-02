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
	"time"

	"github.com/hanzoai/zip"
)

type deployReq struct {
	Commit string `json:"commit"` // git commit/ref to build (git-source)
	Tag    string `json:"tag"`    // image tag to deploy (image-source)
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
	if err := s.k8s.applyService(c.Context(), org, project, a, image); err != nil {
		return s.failDeployment(c, &d, a, deployErrStatus(err), "apply Service CR: "+err.Error())
	}

	// Success: the CR is written; the operator reconciles the rollout. Persist
	// the app's new state (image tag, current deployment, live).
	a.ImageTag, a.Status, a.CurrentDeploy, a.Namespace, a.UpdatedAt = tag, "live", d.ID, tenantNamespace(org), time.Now().Unix()
	if err := s.store.UpdateApplication(c.Context(), a); err != nil {
		s.log.Warn("finalize app failed (continuing)", "app", a.Slug, "err", err)
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
// at its concurrent-build ceiling is 429 (retryable client condition, MED-3); an
// invalid build input is 400 (client must fix repo.url/dockerfile/ref, CRIT-1);
// RBAC/forbidden and other cluster errors surface as 502 (the cloud SA needs a
// grant or the cluster is unhappy).
func deployErrStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, errTooManyBuilds):
		return http.StatusTooManyRequests
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

// deploymentLogs returns the build+deploy log context for a deployment. The
// first slice returns the recorded status timeline + the BuildKit Job reference;
// live pod-log streaming is phase 2. It never fabricates log content.
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
	var lines []string
	lines = append(lines, fmt.Sprintf("deployment %s v%d source=%s status=%s", d.ID, d.Version, d.Source, d.Status))
	if d.Image != "" {
		lines = append(lines, "image: "+d.Image)
	}
	if d.BuildID != "" {
		if b, bErr := s.store.GetBuild(c.Context(), org, d.BuildID); bErr == nil {
			lines = append(lines, fmt.Sprintf("build %s status=%s job=%s", b.ID, b.Status, b.JobName))
			if b.JobName != "" {
				lines = append(lines, "(live BuildKit Job logs stream in phase 2: kubectl -n "+s.k8s.buildNS+" logs job/"+b.JobName+")")
			}
		}
	}
	if d.Message != "" {
		lines = append(lines, "message: "+d.Message)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"deploymentId": d.ID,
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
