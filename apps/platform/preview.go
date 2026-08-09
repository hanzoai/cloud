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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// ── preview (per-branch deployment) ─────────────────────────────────────────────

// previewReq is a branch preview to deploy. Branch and Image carry `url:"-"`
// because the URL addresses the PARENT app and this route has never taken the
// branch or the artifact off the query string.
type previewReq struct {
	// Project is the project the parent application lives under, from the path.
	Project string `json:"project"`
	// App is the parent application's slug, from the path.
	App string `json:"app"`
	// Branch is the branch to preview; defaults to the parent app's branch.
	Branch string `json:"branch" url:"-"`
	// Image is the already-built image ref to deploy. Required — a preview never
	// builds.
	Image string `json:"image" url:"-"`
}

// previewView is the preview response: the branch's live URL, the branch it maps,
// the preview app slug, and the recorded deployment.
type previewView struct {
	// URL is the preview's live HTTPS address.
	URL string `json:"url"`
	// Branch is the branch this preview maps.
	Branch string `json:"branch"`
	// App is the preview application's own slug, `<app>-<branch>`.
	App string `json:"app"`
	// Deployment is the deployment the preview recorded.
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

// preview puts a branch on its own URL.
//
// It deploys an already-built `image` to a per-branch preview and answers its URL,
// the branch, the preview's slug and the deployment. The preview is a FIRST-CLASS
// application named `<app>-<branch>` in the same project and tenant namespace, with
// its own default host — so it is completely isolated from production while reusing
// the same deploy mechanic. Re-previewing a branch converges that same target in
// place rather than stacking another one.
//
// It carries NO environment variables, deliberately: a preview never inherits
// production's secrets. It also does not build — `image` is required and must
// already exist, and `branch` defaults to the parent app's. A branch that does not
// resolve to a valid slug distinct from the parent's is 400. Requires a validated
// principal; 403 without one.
func (o ops) preview(ctx context.Context, body *previewReq) (*previewView, error) {
	s := o.s
	c, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	proj, parent, err := loadApp(s, ctx, org, body.Project, body.App)
	if err != nil {
		return nil, err
	}
	branch := cmp.Or(strings.TrimSpace(body.Branch), strings.TrimSpace(parent.RepoBranch))
	if branch == "" {
		return nil, zip.ErrBadRequest("branch is required")
	}
	image := strings.TrimSpace(body.Image)
	if image == "" {
		return nil, zip.ErrBadRequest("image is required (preview deploys an already-built image)")
	}
	slug := previewSlug(parent.Slug, branch)
	if !slugRE.MatchString(slug) || slug == parent.Slug {
		return nil, zip.ErrBadRequest("branch does not resolve to a valid, distinct preview slug; use a shorter branch or app name")
	}
	repo, tag := splitImageRef(image)
	now := time.Now().Unix()

	a, err := ensurePreviewApp(s, ctx, org, proj, parent, slug, branch, repo, tag, now)
	if err != nil {
		return nil, err
	}

	depID, version, err := nextDeployment(s, ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "allocate deployment: %v", err)
	}
	d, status, derr := deployTagCore(s, ctx, org, proj, a, depID, version, now, image, tag, "image", "", s.State.k8s.ready())
	if derr != nil {
		return nil, zip.Errorf(status, "%s", derr.Error())
	}
	s.Log.Info("preview deployed", "org", org, "app", parent.Slug, "branch", branch, "preview", slug,
		"ns", tenantNamespace(org), "image", image, "actor", c.User(), "requestID", c.RequestID())
	return &previewView{
		URL:        "https://" + defaultHost(s, org, slug),
		Branch:     branch,
		App:        slug,
		Deployment: toDeploymentView(d),
	}, nil
}

// ensurePreviewApp get-or-creates the branch-preview Application (image-source) in
// the parent's project, converging it to the requested image on re-preview. It
// reuses the app store + seedDefaultDomain (the preview's own default host is a
// structural org-subtree host, so no ownership proof is needed) — no parallel
// container. The preview inherits the parent's port/repo metadata but carries NO
// env, keeping it isolated from prod secrets.
func ensurePreviewApp(s *cloud.Service[state], ctx context.Context, org, projectID string, parent Application, slug, branch, repo, tag string, now int64) (Application, error) {
	a, err := s.State.store.GetApplication(ctx, org, projectID, slug)
	switch {
	case err == nil:
		a.Source, a.BuildType, a.ImageRepo, a.ImageTag = "image", "image", repo, tag
		a.RepoBranch, a.Status, a.UpdatedAt = branch, "deploying", now
		if uErr := s.State.store.UpdateApplication(ctx, a); uErr != nil {
			return Application{}, zip.Errorf(http.StatusInternalServerError, "persist preview app: %v", uErr)
		}
		return a, nil
	case !errors.Is(err, errNotFound):
		return Application{}, zip.Errorf(http.StatusInternalServerError, "get preview app: %v", err)
	}

	id := genID("app")
	domainsJSON, _ := json.Marshal(seedDefaultDomain(s, org, slug, nil))
	a = Application{
		ID: id, Org: org, ProjectID: projectID, Slug: slug,
		Name: parent.Name + " (" + branch + ")", Description: "Preview of " + parent.Slug + "@" + branch,
		Environment: "preview", Source: "image", ImageRepo: repo, ImageTag: tag, BuildType: "image",
		RepoURL: parent.RepoURL, RepoBranch: branch, RepoProvider: parent.RepoProvider,
		Port: portOr(parent.Port), Replicas: 1, EnvJSON: "[]", DomainsJSON: string(domainsJSON),
		Status: "deploying", Namespace: tenantNamespace(org), CreatedAt: now, UpdatedAt: now,
	}
	if cErr := s.State.store.CreateApplication(ctx, a); cErr != nil {
		if errors.Is(cErr, errConflict) {
			// Lost a create race — read the winner and converge it to this image.
			if a2, e2 := s.State.store.GetApplication(ctx, org, projectID, slug); e2 == nil {
				a2.ImageRepo, a2.ImageTag, a2.RepoBranch, a2.UpdatedAt = repo, tag, branch, now
				if uErr := s.State.store.UpdateApplication(ctx, a2); uErr != nil {
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

// promoteReq names the already-built artifact to make the app's production
// release. Both selectors carry `url:"-"`: the URL addresses the app, and the
// artifact has never been a query parameter.
type promoteReq struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
	// DeploymentID promotes that deployment's exact built image. One of this and
	// Tag is required.
	DeploymentID string `json:"deploymentId" url:"-"`
	// Tag promotes an image tag, resolved the same way a deploy resolves one.
	Tag string `json:"tag" url:"-"`
}

// promote promotes an already-built release to the app.
//
// It redeploys an image that already exists — named either by `deploymentId`, which
// promotes that deployment's exact built image, or by `tag`, resolved the same way
// a deploy resolves one. One of the two is required; neither is 400.
//
// Promotion never builds. A deployment that carries no built image cannot be
// promoted and is 400, and a deployment id outside this app is 404. It runs through
// the same deploy core as everything else, so it takes a NEW version number and is
// subject to the same per-org concurrency cap. Requires a validated principal; 403
// without one.
func (o ops) promote(ctx context.Context, body *promoteReq) (*deploymentView, error) {
	s := o.s
	c, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	proj, a, err := loadApp(s, ctx, org, body.Project, body.App)
	if err != nil {
		return nil, err
	}
	image, tag, source, commit, err := resolvePromotionTarget(s, ctx, org, a, body.DeploymentID, body.Tag)
	if err != nil {
		return nil, err
	}
	return redeploy(s, ctx, c, org, proj, a, image, tag, source, commit, "promoted", "image", image)
}

// resolvePromotionTarget resolves (image, tag, source, commit) for a promote from
// either an explicit prior deployment (its exact built image) or a tag (resolved
// like a normal deploy: <imageRepo>:<tag> for image apps, the per-tenant build ref
// for git apps). Returns a mapped HTTP error otherwise.
func resolvePromotionTarget(s *cloud.Service[state], ctx context.Context, org string, a Application, deploymentID, tag string) (image, rtag, source, commit string, herr error) {
	deploymentID = strings.TrimSpace(deploymentID)
	tag = strings.TrimSpace(tag)
	if deploymentID != "" {
		d, err := s.State.store.GetDeployment(ctx, org, a.ID, deploymentID)
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
		return d.Image, t, cmp.Or(d.Source, a.Source), d.Commit, nil
	}
	if tag != "" {
		img, t := imageForTag(s, org, a, tag)
		return img, t, a.Source, "", nil
	}
	return "", "", "", "", zip.ErrBadRequest("promote requires deploymentId or tag")
}

// imageForTag resolves a bare tag to a full image ref the SAME way the deploy path
// does: an image-source app builds <imageRepo>:<tag>; a git-source app (no image
// repo) builds the per-tenant build ref ghcr.io/hanzoai/tenant-<org>/<app>:<tag>.
func imageForTag(s *cloud.Service[state], org string, a Application, tag string) (image, rtag string) {
	tag = strings.TrimSpace(tag)
	if a.Source == "git" || a.ImageRepo == "" {
		return s.State.k8s.buildImageRef(org, a.Slug, shortTag(tag)), tag
	}
	return a.ImageRepo + ":" + tag, tag
}

// ── rollback (redeploy a prior image) ───────────────────────────────────────────

// rollbackReq names the release to return to, or nothing at all — an empty body
// rolls back to the previous release.
type rollbackReq struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
	// DeploymentID is the deployment to redeploy. Omit it to return to the
	// previous release.
	DeploymentID string `json:"deploymentId" url:"-"`
}

// rollback goes back to the previous release.
//
// It redeploys a prior image: the one named by `deploymentId`, or — with no body —
// the newest earlier deployment that carries a real built image and did not error,
// skipping the release currently live. An app with nothing earlier to return to is
// 400.
//
// A rollback is a deploy of an old image, not a rewind: it takes a NEW version
// number and appends to the history rather than erasing what came after. Both
// lookups are scoped to this app and org, so another tenant's image can never be
// rolled in. Requires a validated principal; 403 without one.
func (o ops) rollback(ctx context.Context, body *rollbackReq) (*deploymentView, error) {
	s := o.s
	c, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	proj, a, err := loadApp(s, ctx, org, body.Project, body.App)
	if err != nil {
		return nil, err
	}

	target, err := resolveRollbackTarget(s, ctx, org, a, strings.TrimSpace(body.DeploymentID))
	if err != nil {
		return nil, err
	}
	image := strings.TrimSpace(target.Image)
	if image == "" {
		return nil, zip.ErrBadRequest("target deployment has no image to roll back to")
	}
	_, tag := splitImageRef(image)
	return redeploy(s, ctx, c, org, proj, a, image, tag, cmp.Or(target.Source, a.Source), target.Commit,
		"rolled back", "toVersion", target.Version, "image", image)
}

// resolveRollbackTarget picks the deployment to roll back to: the one named by
// deploymentID (scoped to this app), or the previous release. Both reads are
// org+app scoped, so a caller can never roll another tenant's image in.
func resolveRollbackTarget(s *cloud.Service[state], ctx context.Context, org string, a Application, deploymentID string) (Deployment, error) {
	if deploymentID != "" {
		d, err := s.State.store.GetDeployment(ctx, org, a.ID, deploymentID)
		if errors.Is(err, errNotFound) {
			return Deployment{}, zip.ErrNotFound("deployment not found")
		}
		if err != nil {
			return Deployment{}, zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
		}
		return d, nil
	}
	d, err := previousDeployment(s, ctx, org, a)
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
func previousDeployment(s *cloud.Service[state], ctx context.Context, org string, a Application) (Deployment, error) {
	rows, err := s.State.store.ListDeployments(ctx, org, a.ID)
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
func redeploy(s *cloud.Service[state], ctx context.Context, c *zip.Ctx, org, project string, a Application, image, tag, source, commit, logMsg string, logKV ...any) (*deploymentView, error) {
	now := time.Now().Unix()
	depID, version, err := nextDeployment(s, ctx, a.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "allocate deployment: %v", err)
	}
	d, status, derr := deployTagCore(s, ctx, org, project, a, depID, version, now, image, tag, source, commit, s.State.k8s.ready())
	if derr != nil {
		return nil, zip.Errorf(status, "%s", derr.Error())
	}
	s.Log.Info(logMsg, append([]any{"org", org, "app", a.Slug, "ns", tenantNamespace(org),
		"actor", c.User(), "requestID", c.RequestID()}, logKV...)...)
	v := toDeploymentView(d)
	return &v, nil
}
