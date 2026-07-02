package projectsvc

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/zip"
)

// deploy ships a project live. Two modes, one endpoint:
//
//   - Artifact (default): the request body is a tar or tar.gz of the BUILT site
//     (must contain index.html at the root). The handler unpacks it to OUR S3
//     under "<org>/<slug>/", marks the bucket public-read, and records a "live"
//     deployment. This is the builder's one-click deploy (small artifacts,
//     bounded by the app/gateway BodyLimit) — no CI round-trip needed.
//
//   - Git (Content-Type: application/json, {"source":"git", ...}): records a
//     "queued" deployment and returns 202. CI (the reusable build workflow)
//     checks out the linked repo, builds it, syncs dist/ to the SAME S3 prefix,
//     then calls .../deployments/:id/complete to flip it live. This is the
//     "link repo → build (CI, never local) → deploy" path for large sites.
func (s *svc) deploy(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}

	if strings.Contains(strings.ToLower(c.Header("Content-Type")), "application/json") {
		return s.deployGit(c, org, p)
	}
	return s.deployArtifact(c, org, p)
}

type gitDeployReq struct {
	Source string `json:"source"`
	Commit string `json:"commit"`
	Branch string `json:"branch"`
}

func (s *svc) deployGit(c *zip.Ctx, org string, p Project) error {
	var body gitDeployReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if p.RepoURL == "" {
		return zip.ErrBadRequest("project has no linked repo; link a repo or deploy an artifact")
	}
	now := time.Now().Unix()
	version, err := s.store.NextVersion(c.Context(), p.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "version: %v", err)
	}
	id, err := genID("dep")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	d := Deployment{
		ID: id, ProjectID: p.ID, Org: org, Version: version, Status: "queued",
		Source: "git", Commit: strings.TrimSpace(body.Commit), Bucket: s.blob.bucket,
		Prefix: sitePrefix(org, p.Slug), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}
	p.Status = "building"
	p.UpdatedAt = now
	if err := s.store.UpdateProject(c.Context(), p); err != nil {
		s.log.Warn("set building failed (continuing)", "slug", p.Slug, "err", err)
	}
	return c.JSON(http.StatusAccepted, toDeploymentView(d))
}

func (s *svc) deployArtifact(c *zip.Ctx, org string, p Project) error {
	if !s.blob.configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "S3 not configured (set S3_ADMIN_*)")
	}
	raw := c.Body()
	if len(raw) == 0 {
		return zip.ErrBadRequest("empty artifact; send a tar or tar.gz of the built site")
	}
	st, err := walkTarGz(bytes.NewReader(raw))
	if err != nil {
		return zip.ErrBadRequest("invalid artifact: " + err.Error())
	}

	now := time.Now().Unix()
	version, err := s.store.NextVersion(c.Context(), p.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "version: %v", err)
	}
	id, err := genID("dep")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	d := Deployment{
		ID: id, ProjectID: p.ID, Org: org, Version: version, Status: "uploading",
		Source: "upload", Bucket: s.blob.bucket, Prefix: sitePrefix(org, p.Slug),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}

	prefix, files, total, upErr := s.blob.uploadSite(c.Context(), org, p.Slug, st)
	if upErr != nil {
		d.Status = "error"
		d.Message = upErr.Error()
		d.UpdatedAt = time.Now().Unix()
		_ = s.store.UpdateDeployment(c.Context(), d)
		s.log.Error("deploy upload failed", "org", org, "slug", p.Slug, "err", upErr)
		return zip.Errorf(http.StatusBadGateway, "upload failed: %v", upErr)
	}

	live := s.blob.liveURL(org, p.Slug)
	d.Status, d.LiveURL, d.Prefix, d.Files, d.Bytes, d.UpdatedAt = "live", live, prefix, files, total, time.Now().Unix()
	if err := s.store.UpdateDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "finalize deployment: %v", err)
	}

	p.Status, p.LiveURL, p.CurrentDeploy, p.Bucket, p.UpdatedAt = "live", live, d.ID, s.blob.bucket, time.Now().Unix()
	if err := s.store.UpdateProject(c.Context(), p); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "finalize project: %v", err)
	}
	return c.JSON(http.StatusOK, toDeploymentView(d))
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
func (s *svc) completeDeployment(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	d, err := s.store.GetDeployment(c.Context(), org, p.ID, strings.TrimSpace(c.Param("id")))
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
			d.LiveURL = s.blob.liveURL(org, p.Slug)
		}
	}
	if err := s.store.UpdateDeployment(c.Context(), d); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update deployment: %v", err)
	}

	p.UpdatedAt = now
	if status == "live" {
		p.Status, p.LiveURL, p.CurrentDeploy = "live", d.LiveURL, d.ID
	} else {
		p.Status = "error"
	}
	if err := s.store.UpdateProject(c.Context(), p); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update project: %v", err)
	}
	return c.JSON(http.StatusOK, toDeploymentView(d))
}

func (s *svc) listDeployments(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	rows, err := s.store.ListDeployments(c.Context(), org, p.ID)
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
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, slugParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	d, err := s.store.GetDeployment(c.Context(), org, p.ID, strings.TrimSpace(c.Param("id")))
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
