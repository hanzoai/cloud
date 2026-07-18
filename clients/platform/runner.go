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
	"github.com/hanzoai/cloud/clients/principal"
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

// runnerTokenOK reports whether the request carries the shared build-callback
// token, compared in constant time. An UNSET server secret can never match (a
// zero-length secret would otherwise ConstantTimeCompare-equal a zero-length
// header) — the token path simply does not authorize, and the endpoint stays
// available via the IAM path rather than ever accepting an empty credential.
func runnerTokenOK(c *zip.Ctx) bool {
	want := strings.TrimSpace(getenv("PLATFORM_BUILD_CALLBACK_TOKEN", ""))
	if want == "" {
		return false
	}
	got := stripBearer(c.Header("Authorization"))
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// runnerIAMAdmin reports whether the request carries a validated IAM principal
// that is an admin (of its own org via the IAM `isAdmin` bit, or a platform
// SuperAdmin) within a resolvable org. It reads ONLY the principal.* accessors —
// the output of the ONE identity verifier — which read authority headers that
// SanitizeIdentity strips on ingress and re-mints solely from a signature-verified
// JWT, so the signal is unforgeable off-gateway. A validated but non-admin member
// is refused here; the owned-registry allowlist still bounds the image either way.
func runnerIAMAdmin(c *zip.Ctx) bool {
	if !principal.Validated(c) {
		return false
	}
	if _, ok := principal.Org(c); !ok {
		return false
	}
	return principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)
}

// runnerBuild serves POST /v1/runner — enqueue a native, privileged build.
func runnerBuild(s *cloud.Service[state], c *zip.Ctx) error {
	// Auth — ONE of two credentials, never a third:
	//   (1) the shared build-callback token (constant-time): the MACHINE path
	//       (git-push-to-deploy, cloud self-release, the operator). A user never
	//       holds it. Or
	//   (2) a validated IAM principal who is an admin (the IAM `isAdmin` bit, or a
	//       platform SuperAdmin): the `hanzo build` USER path, so ONE IAM login
	//       authorizes a build with no separate build token. principal.Validated is
	//       true ONLY when the identity boundary (the gateway / cloud's own
	//       SanitizeIdentity) minted X-User-Id from a signature-verified JWT and
	//       re-minted X-User-IsOrgAdmin from its `isAdmin` claim — every authority
	//       header is STRIPPED on ingress and re-injected only from validated
	//       claims, so an off-gateway forge cannot fake it, and a plain member
	//       (no admin bit) is refused.
	// Both paths are bounded by the SAME owned-registry allowlist below, so neither
	// can push outside the registries we own.
	viaToken := runnerTokenOK(c)
	viaIAM := !viaToken && runnerIAMAdmin(c)
	if !viaToken && !viaIAM {
		return zip.ErrForbidden("invalid build token")
	}

	var req runnerBuildReq
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("decode build request: " + err.Error())
	}
	req.Repo = strings.TrimSpace(req.Repo)
	req.Image = strings.TrimSpace(req.Image)

	// Release self-publishes ghcr.io/hanzoai/cloud (compute version → build → smoke
	// → tag → notify) — a fabric operation reserved to the MACHINE token. An
	// interactive login, even an admin, never cuts a cloud release.
	if req.Release {
		if !viaToken {
			return zip.ErrForbidden("release builds require the platform build token")
		}
		return startRelease(s, c, req)
	}

	ref := firstNonEmpty(strings.TrimSpace(req.SHA), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Branch), "main")
	if req.Repo == "" || req.Image == "" {
		return zip.ErrBadRequest("repo and image are required")
	}
	if !imageAllowed(req.Image) {
		return zip.ErrForbidden("image must push to an owned registry (ghcr.io/{hanzoai,luxfi,zooai}/*)")
	}

	// Attribute the build to an org. On the IAM path the org is the caller's
	// VALIDATED org, never a client-named one: default organizationId to it, and
	// refuse a foreign org unless the caller is a platform SuperAdmin (who may act
	// cross-org). The machine-token path keeps its explicit organizationId.
	buildOrg := strings.TrimSpace(req.OrgID)
	if viaIAM {
		callerOrg, _ := principal.Org(c)
		switch {
		case buildOrg == "":
			buildOrg = callerOrg
		case buildOrg != callerOrg && !principal.IsSuperAdmin(c):
			return zip.ErrForbidden("organizationId must be your own org")
		}
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
	b := Build{ID: bldID, Org: firstNonEmpty(buildOrg, platformBuildOrg), Status: "queued", Image: req.Image, JobName: jobName, CreatedAt: now, UpdatedAt: now}
	if err := s.State.store.InsertBuild(c.Context(), b); err != nil {
		s.Log.Warn("runner build record insert failed (build already launched)", "job", jobName, "err", err)
	}
	s.Log.Info("runner build launched", "job", jobName, "image", req.Image, "ref", ref, "repo", req.Repo)

	return c.JSON(http.StatusAccepted, runnerBuildResp{
		BuildJobID: bldID, Status: "queued", RunnerPool: "32g", Image: req.Image, Target: strings.TrimSpace(req.DockerTarget),
	})
}
