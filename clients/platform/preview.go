// preview.go — the Vercel-defining release flows for /v1/platform, all built on
// the ONE deploy mechanic (deploy.go's deployTagCore → applyLive; write the
// operator Service CR, the operator reconciles). Nothing here re-implements a
// deployer or a CR writer:
//
//   - preview  — deploy an already-built image to a PER-BRANCH target that is a
//     first-class Application of its own (slug "<app>-<branch>", its own default
//     host "<app>-<branch>.<org>.<sitesHost>"), isolated from prod by a distinct
//     CR name + host in the SAME tenant-<org> namespace. Returns the preview URL.
//   - promote  — set the PROD app's image to an already-built tag or a prior
//     deployment's exact image (forward: pick an artifact, make it prod).
//   - rollback — redeploy a prior deployment's image (backward: the previous
//     release, resolved from the deployments store).
//
// Every handler is org-scoped through s.tenant and every cluster write targets
// tenant-<org> derived from the VALIDATED org — never a request value.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// ── preview (per-branch deployment) ─────────────────────────────────────────────

type previewReq struct {
	Branch string `json:"branch"`
	Image  string `json:"image"` // already-built image ref to deploy to the branch preview
}

// previewView is the preview response: the branch's live URL, the branch it maps,
// the preview app slug, and the recorded deployment.
type previewView struct {
	URL        string         `json:"url"`
	Branch     string         `json:"branch"`
	App        string         `json:"app"` // preview application slug
	Deployment deploymentView `json:"deployment"`
}

// previewSlug derives the branch-preview application's slug from the parent app
// slug and a branch: "<app>-<branchslug>", DNS-clamped to slugRE (≤40). Distinct
// from prod so preview and prod never share a CR name/host in the tenant namespace.
func previewSlug(app, branch string) string {
	b := slugify(branch)
	if b == "" {
		b = "preview"
	}
	slug := app + "-" + b
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	return slug
}

// preview deploys an already-built image to a per-branch preview target. The
// target is a first-class image-source Application (slug "<app>-<branch>") in the
// SAME project + tenant namespace as prod, with its own default host — so the
// preview is fully isolated (distinct CR name + ingress host) yet reuses every
// deploy/CR/tenant helper. Re-previewing the same branch converges it in place.
func (s *svc) preview(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	proj, parent, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	var body previewReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	branch := firstNonEmpty(strings.TrimSpace(body.Branch), strings.TrimSpace(parent.RepoBranch))
	if branch == "" {
		return zip.ErrBadRequest("branch is required")
	}
	image := strings.TrimSpace(body.Image)
	if image == "" {
		return zip.ErrBadRequest("image is required (preview deploys an already-built image)")
	}
	slug := previewSlug(parent.Slug, branch)
	if !slugRE.MatchString(slug) || slug == parent.Slug {
		return zip.ErrBadRequest("branch does not resolve to a valid, distinct preview slug; use a shorter branch or app name")
	}
	repo, tag := splitImageRef(image)
	now := time.Now().Unix()

	a, herr := s.ensurePreviewApp(c.Context(), org, proj.ID, parent, slug, branch, repo, tag, now)
	if herr != nil {
		return herr
	}

	depID, version, err := s.nextDeployment(c.Context(), a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "allocate deployment: %v", err)
	}
	d, status, derr := s.deployTagCore(c.Context(), org, proj.Slug, a, depID, version, now, image, tag, "image", "", s.k8s.ready())
	if derr != nil {
		return zip.Errorf(status, "%s", derr.Error())
	}
	s.log.Info("preview deployed", "org", org, "app", parent.Slug, "branch", branch, "preview", slug,
		"ns", tenantNamespace(org), "image", image, "actor", c.User(), "requestID", c.RequestID())
	return c.JSON(status, previewView{
		URL:        "https://" + s.defaultHost(org, slug),
		Branch:     branch,
		App:        slug,
		Deployment: toDeploymentView(d),
	})
}

// ensurePreviewApp get-or-creates the branch-preview Application (image-source) in
// the parent's project, converging it to the requested image on re-preview. It
// reuses the app store + seedDefaultDomain (the preview's own default host is a
// structural org-subtree host, so no ownership proof is needed) — no parallel
// container. The preview inherits the parent's port/repo metadata but carries NO
// env, keeping it isolated from prod secrets.
func (s *svc) ensurePreviewApp(ctx context.Context, org, projectID string, parent Application, slug, branch, repo, tag string, now int64) (Application, error) {
	a, err := s.store.GetApplication(ctx, org, projectID, slug)
	switch {
	case err == nil:
		a.Source, a.BuildType, a.ImageRepo, a.ImageTag = "image", "image", repo, tag
		a.RepoBranch, a.Status, a.UpdatedAt = branch, "deploying", now
		if uErr := s.store.UpdateApplication(ctx, a); uErr != nil {
			return Application{}, zip.Errorf(http.StatusInternalServerError, "persist preview app: %v", uErr)
		}
		return a, nil
	case !errors.Is(err, errNotFound):
		return Application{}, zip.Errorf(http.StatusInternalServerError, "get preview app: %v", err)
	}

	id, gErr := genID("app")
	if gErr != nil {
		return Application{}, zip.Errorf(http.StatusInternalServerError, "rng: %v", gErr)
	}
	domainsJSON, _ := json.Marshal(s.seedDefaultDomain(org, slug, nil))
	a = Application{
		ID: id, Org: org, ProjectID: projectID, Slug: slug,
		Name: parent.Name + " (" + branch + ")", Description: "Preview of " + parent.Slug + "@" + branch,
		Environment: "preview", Source: "image", ImageRepo: repo, ImageTag: tag, BuildType: "image",
		RepoURL: parent.RepoURL, RepoBranch: branch, RepoProvider: parent.RepoProvider,
		Port: portOr(parent.Port), Replicas: 1, EnvJSON: "[]", DomainsJSON: string(domainsJSON),
		Status: "deploying", Namespace: tenantNamespace(org), CreatedAt: now, UpdatedAt: now,
	}
	if cErr := s.store.CreateApplication(ctx, a); cErr != nil {
		if errors.Is(cErr, errConflict) {
			// Lost a create race — read the winner and converge it to this image.
			if a2, e2 := s.store.GetApplication(ctx, org, projectID, slug); e2 == nil {
				a2.ImageRepo, a2.ImageTag, a2.RepoBranch, a2.UpdatedAt = repo, tag, branch, now
				if uErr := s.store.UpdateApplication(ctx, a2); uErr != nil {
					return Application{}, zip.Errorf(http.StatusInternalServerError, "persist preview app: %v", uErr)
				}
				return a2, nil
			}
		}
		return Application{}, zip.Errorf(http.StatusInternalServerError, "persist preview app: %v", cErr)
	}
	return a, nil
}

// ── promote (make an already-built artifact the prod release) ────────────────────

type promoteReq struct {
	DeploymentID string `json:"deploymentId"`
	Tag          string `json:"tag"`
}

// promote sets the PROD app's image to an already-built target — a prior
// deployment's exact image (deploymentId) or a tag resolved the SAME way a normal
// deploy resolves one — and redeploys through the shared core. Reuses deploy.
func (s *svc) promote(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	proj, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	var body promoteReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	image, tag, source, commit, herr := s.resolvePromotionTarget(c.Context(), org, a, body.DeploymentID, body.Tag)
	if herr != nil {
		return herr
	}
	return s.redeploy(c, org, proj.Slug, a, image, tag, source, commit, "promoted", "image", image)
}

// resolvePromotionTarget resolves (image, tag, source, commit) for a promote from
// either an explicit prior deployment (its exact built image) or a tag (resolved
// like a normal deploy: <imageRepo>:<tag> for image apps, the per-tenant build ref
// for git apps). Returns a mapped HTTP error otherwise.
func (s *svc) resolvePromotionTarget(ctx context.Context, org string, a Application, deploymentID, tag string) (image, rtag, source, commit string, herr error) {
	deploymentID = strings.TrimSpace(deploymentID)
	tag = strings.TrimSpace(tag)
	if deploymentID != "" {
		d, err := s.store.GetDeployment(ctx, org, a.ID, deploymentID)
		if errors.Is(err, errNotFound) {
			return "", "", "", "", zip.ErrNotFound("deployment not found")
		}
		if err != nil {
			return "", "", "", "", zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
		}
		if strings.TrimSpace(d.Image) == "" {
			return "", "", "", "", zip.ErrBadRequest("deployment has no built image to promote")
		}
		_, t := splitImageRef(d.Image)
		return d.Image, t, firstNonEmpty(d.Source, a.Source), d.Commit, nil
	}
	if tag != "" {
		img, t := s.imageForTag(org, a, tag)
		return img, t, a.Source, "", nil
	}
	return "", "", "", "", zip.ErrBadRequest("promote requires deploymentId or tag")
}

// imageForTag resolves a bare tag to a full image ref the SAME way the deploy path
// does: an image-source app builds <imageRepo>:<tag>; a git-source app (no image
// repo) builds the per-tenant build ref ghcr.io/hanzoai/tenant-<org>/<app>:<tag>.
func (s *svc) imageForTag(org string, a Application, tag string) (image, rtag string) {
	tag = strings.TrimSpace(tag)
	if a.Source == "git" || a.ImageRepo == "" {
		return s.k8s.buildImageRef(org, a.Slug, shortTag(tag)), tag
	}
	return a.ImageRepo + ":" + tag, tag
}

// ── rollback (redeploy a prior image) ───────────────────────────────────────────

type rollbackReq struct {
	DeploymentID string `json:"deploymentId"`
}

// rollback redeploys a prior deployment's image: the one named by deploymentId, or
// — when none is given — the previous release resolved from the deployments store
// (the newest prior deployment carrying a real, already-built image). Rollback is
// just a deploy of the previous image, so it reuses the shared core.
func (s *svc) rollback(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	proj, a, herr := s.loadApp(c, org)
	if herr != nil {
		return herr
	}
	var body rollbackReq
	if len(c.Body()) > 0 {
		if err := c.Bind(&body); err != nil {
			return err
		}
	}

	target, herr := s.resolveRollbackTarget(c.Context(), org, a, strings.TrimSpace(body.DeploymentID))
	if herr != nil {
		return herr
	}
	image := strings.TrimSpace(target.Image)
	if image == "" {
		return zip.ErrBadRequest("target deployment has no image to roll back to")
	}
	_, tag := splitImageRef(image)
	return s.redeploy(c, org, proj.Slug, a, image, tag, firstNonEmpty(target.Source, a.Source), target.Commit,
		"rolled back", "toVersion", target.Version, "image", image)
}

// resolveRollbackTarget picks the deployment to roll back to: the one named by
// deploymentID (scoped to this app), or the previous release. Both reads are
// org+app scoped, so a caller can never roll another tenant's image in.
func (s *svc) resolveRollbackTarget(ctx context.Context, org string, a Application, deploymentID string) (Deployment, error) {
	if deploymentID != "" {
		d, err := s.store.GetDeployment(ctx, org, a.ID, deploymentID)
		if errors.Is(err, errNotFound) {
			return Deployment{}, zip.ErrNotFound("deployment not found")
		}
		if err != nil {
			return Deployment{}, zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
		}
		return d, nil
	}
	d, err := s.previousDeployment(ctx, org, a)
	if errors.Is(err, errNotFound) {
		return Deployment{}, zip.ErrBadRequest("no prior deployment to roll back to")
	}
	if err != nil {
		return Deployment{}, zip.Errorf(http.StatusInternalServerError, "resolve prior deployment: %v", err)
	}
	return d, nil
}

// previousDeployment resolves the release to roll BACK to: the most recent prior
// deployment (excluding the app's current live one) that carries a real,
// already-built image and did not error. History is newest-first (version DESC),
// so the first match is the immediately-previous release. errNotFound when there
// is nothing earlier to roll back to.
func (s *svc) previousDeployment(ctx context.Context, org string, a Application) (Deployment, error) {
	rows, err := s.store.ListDeployments(ctx, org, a.ID)
	if err != nil {
		return Deployment{}, err
	}
	for _, d := range rows {
		if d.ID == a.CurrentDeploy {
			continue // skip the current release — roll back to something earlier
		}
		if strings.TrimSpace(d.Image) == "" || d.Status == "error" || d.Status == "building" {
			continue // only a real, already-built prior release is a rollback target
		}
		return d, nil
	}
	return Deployment{}, errNotFound
}

// redeploy is the shared tail of promote + rollback: allocate a new deployment and
// deploy the resolved image through the ONE core, then map the result onto the
// deployment-view response. logMsg + logKV describe the action for the audit log.
func (s *svc) redeploy(c *zip.Ctx, org, project string, a Application, image, tag, source, commit, logMsg string, logKV ...any) error {
	now := time.Now().Unix()
	depID, version, err := s.nextDeployment(c.Context(), a.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "allocate deployment: %v", err)
	}
	d, status, derr := s.deployTagCore(c.Context(), org, project, a, depID, version, now, image, tag, source, commit, s.k8s.ready())
	if derr != nil {
		return zip.Errorf(status, "%s", derr.Error())
	}
	s.log.Info(logMsg, append([]any{"org", org, "app", a.Slug, "ns", tenantNamespace(org),
		"actor", c.User(), "requestID", c.RequestID()}, logKV...)...)
	return c.JSON(status, toDeploymentView(d))
}
