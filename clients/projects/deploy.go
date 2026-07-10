package projects

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
	"github.com/zap-proto/zip"
)

// onPublish runs the go-live side effects for a project whose new build just
// landed at its S3 prefix: it claims the public subdomain (first-come, idempotent
// for the owner) and purges the Cloudflare edge by cache-tag so the new publish
// is instantly live at the edge. Both are best-effort — a host already owned by
// another project, or an unconfigured/failing CF token, must NOT fail the deploy
// (the site is already live at its S3 URL). It stamps LastPurgeAt on the project
// (the caller persists it in the same UpdateProject that flips status to live).
func onPublish(s *cloud.Service[state], ctx context.Context, org string, p *Project) {
	now := time.Now().Unix()
	if err := s.State.store.BindHost(ctx, p.Slug, org, p.Slug, now); err != nil {
		switch {
		case errors.Is(err, errHostTaken):
			s.Log.Warn("subdomain already claimed by another project (serving at S3 URL only)", "org", org, "slug", p.Slug)
		case errors.Is(err, errReservedHost):
			s.Log.Warn("subdomain is a reserved label; not bound (serving at S3 URL only)", "org", org, "slug", p.Slug)
		default:
			s.Log.Warn("bind host failed (continuing)", "org", org, "slug", p.Slug, "err", err)
		}
	}
	if err := s.State.cf.PurgeTags(ctx, sites.CacheTag(org, p.Slug)); err != nil {
		s.Log.Warn("cloudflare purge failed (continuing)", "org", org, "slug", p.Slug, "err", err)
	}
	p.LastPurgeAt = now
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

	if strings.Contains(strings.ToLower(c.Header("Content-Type")), "application/json") {
		return deployGit(s, c, org, p)
	}
	return deployArtifact(s, c, org, p)
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
	return c.JSON(http.StatusAccepted, toDeploymentView(d))
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
		ID: id, ProjectID: p.ID, Org: org, Version: version, Status: "uploading",
		Source: "upload", Bucket: s.State.blob.bucket, Prefix: sitePrefix(org, p.Slug),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.InsertDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}

	prefix, files, total, upErr := s.State.blob.uploadSite(c.Context(), org, p.Slug, p.CacheControl, st)
	if upErr != nil {
		d.Status = "error"
		d.Message = upErr.Error()
		d.UpdatedAt = time.Now().Unix()
		_ = s.State.store.UpdateDeployment(c.Context(), d)
		s.Log.Error("deploy upload failed", "org", org, "slug", p.Slug, "err", upErr)
		return zip.Errorf(http.StatusBadGateway, "upload failed: %v", upErr)
	}

	live := s.State.blob.liveURL(org, p.Slug)
	d.Status, d.LiveURL, d.Prefix, d.Files, d.Bytes, d.UpdatedAt = "live", live, prefix, files, total, time.Now().Unix()
	if err := s.State.store.UpdateDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "finalize deployment: %v", err)
	}

	p.Status, p.LiveURL, p.CurrentDeploy, p.Bucket, p.UpdatedAt = "live", live, d.ID, s.State.blob.bucket, time.Now().Unix()
	onPublish(s, c.Context(), org, &p)
	if err := s.State.store.UpdateProject(c.Context(), p); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "finalize project: %v", err)
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
			d.LiveURL = s.State.blob.liveURL(org, p.Slug)
		}
	}
	if err := s.State.store.UpdateDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update deployment: %v", err)
	}

	p.UpdatedAt = now
	if status == "live" {
		p.Status, p.LiveURL, p.CurrentDeploy = "live", d.LiveURL, d.ID
		onPublish(s, c.Context(), org, &p)
	} else {
		p.Status = "error"
	}
	if err := s.State.store.UpdateProject(c.Context(), p); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update project: %v", err)
	}
	return c.JSON(http.StatusOK, toDeploymentView(d))
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
