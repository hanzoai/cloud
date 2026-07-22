package projects

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
	"github.com/zap-proto/zip"
)

// siteHost is the public host key a published project binds and resolves on: the
// BARE `<slug>`. The published edge is `<slug>.<apex>` (one DNS label), because a
// k8s wildcard Ingress host and a Let's Encrypt wildcard cert each match exactly
// ONE label — a two-label `<slug>.<org>.<apex>` neither routes nor gets TLS, so it
// can never actually serve. The bare slug is therefore a GLOBAL, first-come
// namespace (the PK on site_hosts enforces one owner per host); a second org
// publishing the same slug is refused the subdomain and serves at its S3 URL
// only. This is the exact string bound into site_hosts (onPublish) AND the key the
// sites edge resolves after stripping the apex (sites.siteSlug), so bind and
// resolve agree — and it matches TestSiteHostBindingIsFirstComeAndTenantSafe,
// which has always asserted bare-host first-come.
func siteHost(org, slug string) string { return slug }

// onPublish runs the go-live side effects for a project whose new build just
// landed at its S3 prefix: it claims the org-scoped public host (first-come per
// (org,slug), idempotent for the owner) and purges the Cloudflare edge by
// cache-tag so the new publish is instantly live at the edge. Both are
// best-effort — an unconfigured/failing CF token must NOT fail the deploy (the
// site is already live at its S3 URL). It stamps LastPurgeAt on the project (the
// caller persists it in the same UpdateProject that flips status to live).
func onPublish(s *cloud.Service[state], ctx context.Context, org string, p *Project) {
	now := time.Now().Unix()
	host := siteHost(org, p.Slug)
	if err := s.State.store.BindHost(ctx, host, org, p.Slug, now); err != nil {
		switch {
		case errors.Is(err, errHostTaken):
			s.Log.Warn("subdomain already claimed by another project (serving at S3 URL only)", "org", org, "slug", p.Slug, "host", host)
		case errors.Is(err, errReservedHost):
			s.Log.Warn("subdomain is a reserved label; not bound (serving at S3 URL only)", "org", org, "slug", p.Slug, "host", host)
		default:
			s.Log.Warn("bind host failed (continuing)", "org", org, "slug", p.Slug, "host", host, "err", err)
		}
	}
	if err := s.State.cf.PurgeTags(ctx, sites.CacheTag(org, p.Slug)); err != nil {
		s.Log.Warn("cloudflare purge failed (continuing)", "org", org, "slug", p.Slug, "err", err)
	}
	p.LastPurgeAt = now
}

// siteURL is the canonical public URL of a deployed site: the pretty bare host
// https://<slug>.<apex> the sites edge (clients/sites) serves from S3 — the ONE
// host that actually routes + gets a wildcard cert (see siteHost). It is the ONE
// live-URL form on both the Deployment and the Project — a redeploy to the same
// slug returns the SAME URL because slug and apex are stable. `org` is retained in
// the signature (the caller has it; kept for symmetry with siteHost) but does not
// appear in the servable host.
func siteURL(s *cloud.Service[state], _org, slug string) string {
	return "https://" + slug + "." + s.State.apex
}

// publishSite is the ONE shared deploy core: given a parsed site artifact and its
// target project, it versions and records a deployment, uploads the files to S3,
// flips the deployment and project "live" at the pretty <slug>.<apex> host, and
// runs the go-live side effects (first-come host binding + edge purge). Every
// deploy write-path — the tar-artifact path (deployArtifact) and both /v1/sites
// paths — funnels through here, so versioning, the S3 write, host binding, the
// lifecycle emit, and the status transitions live in exactly one place (DRY).
// source records how the artifact was produced ("upload" | "generated" | "deploy").
// On an upload failure it marks the deployment "error" and returns the error
// WITHOUT touching the project or billing — a failed deploy is never billed and
// never flips a live site.
func publishSite(s *cloud.Service[state], ctx context.Context, org string, p Project, st *site, source string) (Deployment, error) {
	now := time.Now().Unix()
	version, err := s.State.store.NextVersion(ctx, p.ID)
	if err != nil {
		return Deployment{}, fmt.Errorf("version: %w", err)
	}
	id, err := genID("dep")
	if err != nil {
		return Deployment{}, fmt.Errorf("rng: %w", err)
	}
	d := Deployment{
		ID: id, ProjectID: p.ID, Org: org, Version: version, Status: "uploading",
		Source: source, Bucket: s.State.blob.bucket, Prefix: sitePrefix(org, p.Slug),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.InsertDeployment(ctx, d); err != nil {
		return Deployment{}, fmt.Errorf("persist deployment: %w", err)
	}

	prefix, files, total, upErr := s.State.blob.uploadSite(ctx, org, p.Slug, p.CacheControl, st)
	if upErr != nil {
		d.Status = "error"
		d.Message = upErr.Error()
		d.UpdatedAt = time.Now().Unix()
		_ = s.State.store.UpdateDeployment(ctx, d)
		s.Log.Error("deploy upload failed", "org", org, "slug", p.Slug, "err", upErr)
		emitProjectLifecycle(ctx, cloud.LifecycleDeployFailed, org, p, d, p.Slug+": "+upErr.Error())
		return d, fmt.Errorf("upload failed: %w", upErr)
	}

	live := siteURL(s, org, p.Slug)
	d.Status, d.LiveURL, d.Prefix, d.Files, d.Bytes, d.UpdatedAt = "live", live, prefix, files, total, time.Now().Unix()
	if err := s.State.store.UpdateDeployment(ctx, d); err != nil {
		return d, fmt.Errorf("finalize deployment: %w", err)
	}
	emitProjectLifecycle(ctx, cloud.LifecycleDeployLive, org, p, d, p.Slug+" live ("+live+")")

	p.Status, p.LiveURL, p.CurrentDeploy, p.Bucket, p.UpdatedAt = "live", live, d.ID, s.State.blob.bucket, time.Now().Unix()
	// A full-artifact deploy REPLACES the site at its legacy mutable prefix, so it
	// takes the serving pointer back from any active release (releaseSpace is a
	// sibling of that prefix, so the release objects themselves survive and remain
	// re-activatable). Leaving a stale pointer here would serve the old release
	// instead of the artifact just uploaded.
	p.CurrentRelease = ""
	onPublish(s, ctx, org, &p)
	if err := s.State.store.UpdateProject(ctx, p); err != nil {
		return d, fmt.Errorf("finalize project: %w", err)
	}
	return d, nil
}

// deploy ships a project live. Two modes, one endpoint:
//
//   - Artifact (default): the request body is a zip OR tar(.gz) of the BUILT site
//     (must contain index.html at the root, or a single wrapper directory that
//     does). It arrives as either a multipart file upload (a browser <input
//     type=file>) or the raw request body (a curl one-liner). The handler unpacks
//     it to OUR S3 under "<org>/<slug>/", marks the bucket public-read, and
//     records a "live" deployment. This is the builder/console one-click deploy
//     (small artifacts, bounded by the app/gateway BodyLimit) — no CI round-trip.
//
//   - Git (Content-Type: application/json, {"source":"git", ...}): records a
//     "queued" deployment and returns 202. CI (the reusable build workflow)
//     checks out the linked repo, builds it, syncs dist/ to the SAME S3 prefix,
//     then calls .../deployments/:id/complete to flip it live. This is the
//     "link repo → build (CI, never local) → deploy" path for large sites.
func deploy(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.State.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}

	// Fail-closed hosting gate BEFORE any deploy work (both modes are billable):
	// an unfunded org is 402, an unreachable commerce is 503, and nothing is
	// uploaded or enqueued. The debit lands later — after the work succeeds.
	fee, gErr := gateHosting(s, c)
	if gErr != nil {
		return cloud.DenyResource(c, gErr)
	}

	if strings.Contains(strings.ToLower(c.Header("Content-Type")), "application/json") {
		// Git/CI path: enqueue now (gated), debit on the CI completion that flips
		// the deployment live — never on a queued/failed build.
		return deployGit(s, c, org, p)
	}
	if err := deployArtifact(s, c, org, p); err != nil {
		return err // failed deploy — surface it, do NOT bill failed work.
	}
	meterDeploy(s, c, fee)
	return nil
}

type gitDeployReq struct {
	Source string `json:"source"`
	Commit string `json:"commit"`
	Branch string `json:"branch"`
}

func deployGit(s *cloud.Service[state], c *zip.Ctx, org string, p Project) error {
	var body gitDeployReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if p.RepoURL == "" {
		return zip.ErrBadRequest("project has no linked repo; link a repo or deploy an artifact")
	}
	now := time.Now().Unix()
	version, err := s.State.store.NextVersion(c.Context(), p.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "version: %v", err)
	}
	id, err := genID("dep")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	d := Deployment{
		ID: id, ProjectID: p.ID, Org: org, Version: version, Status: "queued",
		Source: "git", Commit: strings.TrimSpace(body.Commit), Bucket: s.State.blob.bucket,
		Prefix: sitePrefix(org, p.Slug), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.InsertDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}
	p.Status = "building"
	p.UpdatedAt = now
	if err := s.State.store.UpdateProject(c.Context(), p); err != nil {
		s.Log.Warn("set building failed (continuing)", "slug", p.Slug, "err", err)
	}
	emitProjectLifecycle(c.Context(), cloud.LifecycleBuildStarted, org, p, d, "building "+p.Slug)
	return c.JSON(http.StatusAccepted, toDeploymentView(d))
}

// emitProjectLifecycle fans a site-deploy transition onto the cloud lifecycle
// stream so the git-lifecycle reactors (Slack-notify) can post about it. Repo is
// derived from the project's linked RepoURL (the native repo name a subscription
// keys on); a project deploying an uploaded artifact with no linked repo carries an
// empty Repo and routes to nothing. A site has no git project sub-scope, so Project
// is the org-level "" the git store keys org-level repos under. Best-effort +
// detached inside EmitLifecycle.
func emitProjectLifecycle(ctx context.Context, kind cloud.LifecycleKind, org string, p Project, d Deployment, detail string) {
	branch := strings.TrimSpace(p.RepoBranch)
	if branch == "" {
		branch = "main"
	}
	cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
		Kind: kind, Org: org, Project: "", Repo: cloud.RepoFromCloneURL(p.RepoURL), Branch: branch,
		After: strings.TrimSpace(d.Commit), DeployID: d.ID, Detail: detail,
	})
}

func deployArtifact(s *cloud.Service[state], c *zip.Ctx, org string, p Project) error {
	if !s.State.blob.configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage not configured (set S3_ADMIN_*)")
	}
	raw, err := readArtifactBody(c)
	if err != nil {
		return err
	}
	st, err := walkArtifact(raw)
	if err != nil {
		return zip.ErrBadRequest("invalid artifact: " + err.Error())
	}
	d, err := publishSite(s, c.Context(), org, p, st, "upload")
	if err != nil {
		// An upload failure marks the deployment "error"; anything else is a store
		// finalize failure. Preserve the historical status codes: 502 upstream vs 500.
		if d.Status == "error" {
			return zip.Errorf(http.StatusBadGateway, "%v", err)
		}
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return c.JSON(http.StatusOK, toDeploymentView(d))
}

// artifactFields are the multipart form field names an upload may use for the
// site archive, tried in order. "file" is the conventional default; the others
// are accepted so a client that names the part "artifact"/"site"/"zip" still works.
var artifactFields = []string{"file", "artifact", "site", "zip"}

// readArtifactBody returns the deploy artifact bytes from EITHER a multipart file
// upload (a browser <input type=file> — the field named in artifactFields) or the
// raw request body (a curl one-liner streaming the archive directly). Both paths
// are bounded by maxTotalBytes (and the framework BodyLimit). Multipart is what a
// browser upload posts; raw body is the CLI/API path. One deploy endpoint, both
// ergonomics; the format (zip vs tar.gz) is sniffed later by walkArtifact.
func readArtifactBody(c *zip.Ctx) ([]byte, error) {
	if strings.Contains(strings.ToLower(c.Header("Content-Type")), "multipart/form-data") {
		var fh *multipart.FileHeader
		for _, field := range artifactFields {
			if f, err := c.Fiber().FormFile(field); err == nil && f != nil {
				fh = f
				break
			}
		}
		if fh == nil {
			return nil, zip.ErrBadRequest("multipart upload has no file part (expected field 'file')")
		}
		if fh.Size > maxTotalBytes {
			return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "artifact exceeds %d bytes", maxTotalBytes)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "open upload: %v", err)
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(io.LimitReader(f, maxTotalBytes+1))
		if err != nil {
			return nil, zip.Errorf(http.StatusBadRequest, "read upload: %v", err)
		}
		if int64(len(data)) > maxTotalBytes {
			return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "artifact exceeds %d bytes", maxTotalBytes)
		}
		if len(data) == 0 {
			return nil, zip.ErrBadRequest("empty upload; attach a zip or tar.gz of the built site")
		}
		return data, nil
	}
	raw := c.Body()
	if len(raw) == 0 {
		return nil, zip.ErrBadRequest("empty artifact; send a zip or tar.gz of the built site")
	}
	return raw, nil
}

type completeReq struct {
	Status  string `json:"status"` // live | error
	Commit  string `json:"commit"`
	LiveURL string `json:"liveUrl"`
	Message string `json:"message"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// completeDeployment is the CI completion hook for the git path: after CI syncs
// the built site to S3 it flips the queued deployment to live (or error). It is
// org-scoped like every other route; CI authenticates with an org-scoped token
// through the gateway, so the X-Org-Id binds the call to the right tenant.
func completeDeployment(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.State.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	d, err := s.State.store.GetDeployment(c.Context(), org, p.ID, strings.TrimSpace(c.Param("id")))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}
	var body completeReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "live" && status != "error" {
		return zip.ErrBadRequest("status must be live or error")
	}
	now := time.Now().Unix()
	d.Status = status
	d.UpdatedAt = now
	if body.Commit != "" {
		d.Commit = strings.TrimSpace(body.Commit)
	}
	d.Message = strings.TrimSpace(body.Message)
	d.Files, d.Bytes = body.Files, body.Bytes
	if status == "live" {
		d.LiveURL = strings.TrimSpace(body.LiveURL)
		if d.LiveURL == "" {
			d.LiveURL = siteURL(s, org, p.Slug)
		}
	}
	if err := s.State.store.UpdateDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update deployment: %v", err)
	}

	p.UpdatedAt = now
	if status == "live" {
		p.Status, p.LiveURL, p.CurrentDeploy = "live", d.LiveURL, d.ID
		// CI synced the built site to the legacy mutable prefix, so — exactly as in
		// publishSite — going live there takes the pointer back from any release.
		p.CurrentRelease = ""
		onPublish(s, c.Context(), org, &p)
	} else {
		p.Status = "error"
	}
	if err := s.State.store.UpdateProject(c.Context(), p); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update project: %v", err)
	}
	if status == "live" {
		emitProjectLifecycle(c.Context(), cloud.LifecycleDeployLive, org, p, d, p.Slug+" live ("+d.LiveURL+")")
		// Bill the git/CI path HERE — this is where the deploy actually goes live. The
		// enqueue (deployGit, via deploy's gate) already passed the gate; a "live"
		// completion is the one billable success, an "error" completion bills nothing.
		meterDeploy(s, c, cloud.ResourceFeeCents(deployFeeEnvPrefix, deployKind))
	} else {
		emitProjectLifecycle(c.Context(), cloud.LifecycleDeployFailed, org, p, d, p.Slug+": "+nonEmptyStr(d.Message, "deploy failed"))
	}
	return c.JSON(http.StatusOK, toDeploymentView(d))
}

// nonEmptyStr returns s trimmed, or fallback when blank.
func nonEmptyStr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func listDeployments(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.State.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	rows, err := s.State.store.ListDeployments(c.Context(), org, p.ID)
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
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.State.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	d, err := s.State.store.GetDeployment(c.Context(), org, p.ID, strings.TrimSpace(c.Param("id")))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}
	return c.JSON(http.StatusOK, toDeploymentView(d))
}

// genID returns "<prefix>_<22-char-url-safe-token>" (96 bits of entropy).
func genID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b), nil
}
