// runner.go — POST /v1/runner: the native, privileged build endpoint.
//
// This is the no-GitHub-builders build trigger that `hanzo build`, the
// git-push-to-deploy hook, and cloud's own self-release all call. It replaces
// the old /v1/arcd surface: one native build API on the runner fabric.
//
// It differs from the tenant path (/v1/platform/.../deploy, which FORCES a
// per-tenant image ref): a /v1/runner build is PRIVILEGED — the caller supplies
// the output image — so it is gated two ways:
//   - a shared build-callback token (constant-time), and
//   - an image-ref allowlist restricted to the org registries we own,
//
// so a leaked token can never push to an arbitrary registry.
package platform

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// runnerBuildReq mirrors the CLI BuildReq (cli/platform.go). repo + image are
// required; the rest are optional build knobs.
type runnerBuildReq struct {
	Repo         string `json:"repo"`
	SHA          string `json:"sha"`
	Image        string `json:"image"`
	Branch       string `json:"branch,omitempty"`
	Ref          string `json:"ref,omitempty"`
	Dockerfile   string `json:"dockerfile,omitempty"`
	Context      string `json:"context,omitempty"`
	DockerTarget string `json:"dockerTarget,omitempty"`
	OS           string `json:"os,omitempty"`
	Arch         string `json:"arch,omitempty"`
	OrgID        string `json:"organizationId,omitempty"`
	// Release requests native release semantics for cloud's self-publish: compute
	// the next version, build+push ghcr.io/hanzoai/cloud, smoke it, then tag (the
	// receipt) and notify universe. It owns its output image (release.go).
	Release bool `json:"release,omitempty"`
}

// runnerBuildResp is the 202 acceptance (matches the CLI BuildJob).
type runnerBuildResp struct {
	BuildJobID string `json:"buildJobId"`
	Status     string `json:"status"`
	RunnerPool string `json:"runnerPool"`
	Image      string `json:"image"`
	Target     string `json:"target,omitempty"`
}

// allowedImageRegistries bounds where a privileged /v1/runner build may push.
// Only the org registries we own — a build token cannot push elsewhere.
// registry.hanzo.ai is the self-hosted fleet registry (the native CI/CD home);
// ghcr stays during the migration as the public mirror.
var allowedImageRegistries = []string{
	"registry.hanzo.ai/hanzoai/",
	"registry.hanzo.ai/luxfi/",
	"registry.hanzo.ai/zooai/",
	"ghcr.io/hanzoai/",
	"ghcr.io/luxfi/",
	"ghcr.io/zooai/",
}

func imageAllowed(image string) bool {
	for _, p := range allowedImageRegistries {
		if strings.HasPrefix(image, p) {
			return true
		}
	}
	return false
}

// stripBearer returns the token from an "Authorization: Bearer <tok>" header.
func stripBearer(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return h
}

// runnerBuild serves POST /v1/runner — enqueue a native, privileged build.
func runnerBuild(s *cloud.Service[state], c *zip.Ctx) error {
	// Auth: shared build-callback token, constant-time. Fail closed if unset —
	// no token configured means no privileged builds, never an open endpoint.
	want := strings.TrimSpace(getenv("PLATFORM_BUILD_CALLBACK_TOKEN", ""))
	if want == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "runner build unavailable: no build-callback token configured")
	}
	got := stripBearer(c.Header("Authorization"))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return zip.ErrForbidden("invalid build token")
	}

	var req runnerBuildReq
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("decode build request: " + err.Error())
	}
	req.Repo = strings.TrimSpace(req.Repo)
	req.Image = strings.TrimSpace(req.Image)

	// Release path: cloud self-publishes ghcr.io/hanzoai/cloud with native release
	// semantics (compute version → build → smoke → tag → notify). It computes its
	// own owned image, so it runs before the caller-supplied-image checks below.
	if req.Release {
		return startRelease(s, c, req)
	}

	ref := firstNonEmpty(strings.TrimSpace(req.SHA), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Branch), "main")
	if req.Repo == "" || req.Image == "" {
		return zip.ErrBadRequest("repo and image are required")
	}
	if !imageAllowed(req.Image) {
		return zip.ErrForbidden("image must push to an owned registry (ghcr.io/{hanzoai,luxfi,zooai}/*)")
	}

	bldID, err := genID("bld")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}

	jobName, err := s.State.k8s.launchDirectBuild(c.Context(), req.Repo, ref, req.Image, strings.TrimSpace(req.Dockerfile), bldID)
	if err != nil {
		return zip.Errorf(deployErrStatus(err), "launch build: %v", err)
	}

	// Record the build (org "platform" — a fabric-owned direct build, not
	// tenant-scoped). Best-effort: a record miss must not fail a launched build.
	now := time.Now().Unix()
	b := Build{ID: bldID, Org: firstNonEmpty(req.OrgID, platformBuildOrg), Status: "queued", Image: req.Image, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(c.Context(), b); err != nil {
		s.Log.Warn("runner build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner build launched", "job", jobName, "image", req.Image, "ref", ref, "repo", req.Repo)

	return c.JSON(http.StatusAccepted, runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Image: req.Image, Target: strings.TrimSpace(req.DockerTarget),
	})
}
