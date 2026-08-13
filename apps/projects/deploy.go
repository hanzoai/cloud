package projects

import (
	"cmp"
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
	"github.com/hanzoai/cloud/apps/sites"
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

// onPublish runs the go-live side effects after a deployment's build lands at the
// project's S3 origin prefix: it claims the public host (first-come per (org,slug),
// idempotent for the owner) and purges the edge cache-tag so the new build serves at
// the edge immediately. Neither failure fails the deploy — the bytes are live at the
// S3 origin either way. purgeEdge stamps LastPurgeAt; the caller persists it in the
// same UpdateProject that flips status to live.
//
// It returns the public URL the project ACTUALLY owns, and a note when there is
// none. The bare `<slug>` namespace is GLOBAL and first-come across every org
// (siteHost), so a second org publishing the same slug loses the subdomain — and
// used to be told its site was live at a URL serving the WINNER's content, because
// the URL was derived from the slug rather than from the binding that decides what
// serves. Ownership is the only thing that can answer "what is my URL", so the
// answer is minted HERE, from the bind result, and only errHostTaken/errReservedHost
// blank it: those two are DEFINITE proof the host is not ours, while a transient
// bind error proves nothing and must not blank a working site's URL.
func onPublish(s *cloud.Service[state], ctx context.Context, org string, p *Project) (live, note string) {
	host := siteHost(org, p.Slug)
	live = siteURL(s, org, p.Slug)
	if err := s.State.store.BindHost(ctx, host, org, p.Slug, time.Now().Unix()); err != nil {
		switch {
		case errors.Is(err, errHostTaken):
			live, note = "", host+"."+s.State.apex+" is already claimed by another project, so this site has no public subdomain — rename the project to publish it"
			s.Log.Warn("subdomain already claimed by another project (no public URL)", "org", org, "slug", p.Slug, "host", host)
		case errors.Is(err, errReservedHost):
			live, note = "", host+"."+s.State.apex+" is a reserved subdomain, so this site has no public subdomain — rename the project to publish it"
			s.Log.Warn("subdomain is a reserved label; not bound (no public URL)", "org", org, "slug", p.Slug, "host", host)
		default:
			s.Log.Warn("bind host failed (continuing)", "org", org, "slug", p.Slug, "host", host, "err", err)
		}
	}
	purgeEdge(s, ctx, org, p)
	return live, note
}

// purgeTag flushes the edge cache-tag site-<org>-<slug> for a project's site. It is
// the ONE place that derives the cache-tag and issues the purge, so every purge
// site — deploy (onPublish), domain-bind (setDomains), delete (del), and the
// dedicated POST /v1/projects/:slug/purge — shares one tag and one failure policy.
// Best-effort by construction: PurgeTags is a warn-only no-op when the edge (CF) is
// unconfigured, and a purge miss is logged, never fatal — the S3 origin keeps
// serving and the edge self-heals when the short HTML TTL lapses.
func purgeTag(s *cloud.Service[state], ctx context.Context, org, slug string) {
	if err := s.State.cf.PurgeTags(ctx, sites.CacheTag(org, slug)); err != nil {
		s.Log.Warn("edge cache-tag purge failed (continuing)", "org", org, "slug", slug, "err", err)
	}
}

// purgeEdge purges the project's edge cache-tag and stamps LastPurgeAt = now. It is
// the shared core of the two content-freshness paths — the go-live side effects
// (onPublish) and the dedicated purge handler — so purge + stamp happen ONE way. It
// does NOT persist: the caller writes p in its own UpdateProject.
func purgeEdge(s *cloud.Service[state], ctx context.Context, org string, p *Project) {
	purgeTag(s, ctx, org, p.Slug)
	p.LastPurgeAt = time.Now().Unix()
}

// PurgeProject flushes the site's edge cache without redeploying anything.
//
// It invalidates the edge cache-tag `site-<org>-<slug>` and stamps `lastPurgeAt`
// (unix seconds), and it NEVER writes or deletes the S3 origin — the live build
// keeps serving; only stale copies held at the edge drop, so the next request
// re-fetches the current artifact from origin. Idempotent, and an edge that is
// unconfigured or failing is not fatal: `lastPurgeAt` is still stamped and the
// answer is still the updated project.
//
// Scope: a validated principal is required (403 without one) and the project is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) purge(ctx context.Context, in *projectsRef) (*projectsProject, error) {
	_, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	purgeEdge(o.s, ctx, org, &p)
	p.UpdatedAt = time.Now().Unix()
	if err := o.s.State.store.UpdateProject(ctx, p); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	out := toProject(p)
	return &out, nil
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

	// A full-artifact deploy REPLACES the site at its legacy mutable prefix, so it
	// takes the serving pointer back from any active release (releaseSpace is a
	// sibling of that prefix, so the release objects themselves survive and remain
	// re-activatable). Leaving a stale pointer here would serve the old release
	// instead of the artifact just uploaded.
	p.CurrentRelease = ""
	// Claim the host BEFORE stamping the URL: the binding is what decides which
	// project a host serves, so it is the only honest source of "my live URL".
	live, note := onPublish(s, ctx, org, &p)
	d.Status, d.LiveURL, d.Message, d.Prefix, d.Files, d.Bytes, d.UpdatedAt = "live", live, note, prefix, files, total, time.Now().Unix()
	if err := s.State.store.UpdateDeployment(ctx, d); err != nil {
		return d, fmt.Errorf("finalize deployment: %w", err)
	}
	emitProjectLifecycle(ctx, cloud.LifecycleDeployLive, org, p, d, p.Slug+" live ("+live+note+")")

	p.Status, p.LiveURL, p.CurrentDeploy, p.Bucket, p.UpdatedAt = "live", live, d.ID, s.State.blob.bucket, time.Now().Unix()
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
	p, err := loadProject(s, c.Context(), org, slugParam(c))
	if err != nil {
		return err
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

	// Hand CI a prefix-scoped, short-lived write grant with the 202, so it needs no
	// bucket credential of its own (grant.go). Best-effort: a deployment whose
	// grant could not be minted is still queued and still completable — the caller
	// just has to have its own way to write, and sees no `upload` in the response.
	view := toDeployment(d)
	grant, gErr := mintGrant(c.Context(), s.State.blob, d.Prefix, time.Now())
	if gErr != nil {
		s.Log.Warn("mint upload grant failed (deployment still queued)", "slug", p.Slug, "err", gErr)
	}
	view.Upload = grant
	return c.JSON(http.StatusAccepted, view)
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
	return c.JSON(http.StatusOK, toDeployment(d))
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

type projectsComplete struct {
	// Slug is the project the deployment belongs to, from the path.
	Slug string `json:"slug"`
	// ID is the queued deployment to complete, from the path.
	ID      string `json:"id"`
	Status  string `json:"status"` // live | error
	Commit  string `json:"commit"`
	LiveURL string `json:"liveUrl"`
	Message string `json:"message"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	// Keys is the manifest CI just uploaded, RELATIVE to the deployment prefix. It
	// is what replaces `aws s3 sync --delete`: an upload grant authorizes writes
	// only, so CI cannot remove a file, and cloud reconciles the prefix against
	// this list instead (grant.go). Omit it and nothing is deleted — the prefix
	// only grows, which is the old pre-grant behaviour and a safe default.
	Keys []string `json:"keys,omitempty"`
}

// CompleteDeployment is the CI completion hook that flips a queued git
// deployment to live (or error) once CI has synced the built site to S3.
//
// `status` must be `live` or `error`. On a LIVE completion the public host is
// claimed FIRST, so the deployment reports the URL it actually OWNS — a
// CI-supplied `liveUrl` is a hint that can refine that URL but can never assert
// a subdomain another tenant holds. `keys` is the manifest CI just uploaded,
// relative to the deployment prefix: cloud reconciles the prefix against it so a
// page deleted from the build actually stops serving. Omit `keys` and nothing is
// deleted — the prefix only grows. Reconciliation runs only on a live completion
// (pruning against a failed build's manifest would delete the site the last good
// build is still serving) and is best-effort, so a stale leftover never turns a
// successful deploy into a 500. A live completion is also the one billable
// moment on the git path; an error completion bills nothing.
//
// Scope: a validated principal is required (403 without one). CI authenticates
// with an org-scoped token through the gateway, so the deployment is resolved
// within that principal's org and another tenant's slug or deployment id is a
// 404.
func (o ops) completeDeployment(ctx context.Context, in *projectsComplete) (*projectsDeployment, error) {
	c, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	s := o.s
	d, err := s.State.store.GetDeployment(ctx, org, p.ID, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}
	if err := requireBody(c); err != nil {
		return nil, err
	}
	body := in
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "live" && status != "error" {
		return nil, zip.ErrBadRequest("status must be live or error")
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
		// Claim the host first, so the git/CI path reports the URL it OWNS — the
		// same rule publishSite follows. A CI-supplied LiveURL is a hint only: it
		// cannot assert a subdomain another tenant holds.
		live, note := onPublish(s, ctx, org, &p)
		if live != "" {
			if hint := strings.TrimSpace(body.LiveURL); hint != "" {
				live = hint
			}
		} else if d.Message == "" {
			d.Message = note
		}
		d.LiveURL = live
	}
	if err := s.State.store.UpdateDeployment(ctx, d); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update deployment: %v", err)
	}

	// Reconcile the prefix against what CI says it uploaded, so a deleted page
	// actually stops serving. Only on a LIVE completion: pruning against the
	// manifest of a build that failed would delete the site the previous good
	// build is still serving. Best-effort — the new content is already up, and a
	// stale leftover must not turn a successful deploy into a 500.
	if status == "live" && len(body.Keys) > 0 {
		keep := make(map[string]bool, len(body.Keys))
		for _, k := range body.Keys {
			if k = strings.TrimPrefix(strings.TrimSpace(k), "/"); k != "" {
				keep[k] = true
			}
		}
		if cli, cErr := s.State.blob.client(); cErr != nil {
			s.Log.Warn("reconcile skipped (no s3 client)", "slug", p.Slug, "err", cErr)
		} else if removed, rErr := reconcilePrefix(ctx, cli, d.Bucket, d.Prefix, keep); rErr != nil {
			s.Log.Warn("reconcile failed (new content is live; stale files remain)", "slug", p.Slug, "err", rErr)
		} else if removed > 0 {
			s.Log.Info("reconciled site prefix", "slug", p.Slug, "prefix", d.Prefix, "removed", removed)
		}
	}

	p.UpdatedAt = now
	if status == "live" {
		p.Status, p.LiveURL, p.CurrentDeploy = "live", d.LiveURL, d.ID
		// CI synced the built site to the legacy mutable prefix, so — exactly as in
		// publishSite — going live there takes the pointer back from any release.
		// (onPublish already ran above, where the owned URL was decided.)
		p.CurrentRelease = ""
	} else if failureOwnsProject(p.CurrentDeploy, d.ID) {
		p.Status = "error"
	}
	if err := s.State.store.UpdateProject(ctx, p); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update project: %v", err)
	}
	if status == "live" {
		emitProjectLifecycle(ctx, cloud.LifecycleDeployLive, org, p, d, p.Slug+" live ("+d.LiveURL+")")
		// Bill the git/CI path HERE — this is where the deploy actually goes live. The
		// enqueue (deployGit, via deploy's gate) already passed the gate; a "live"
		// completion is the one billable success, an "error" completion bills nothing.
		meterDeploy(s, c, cloud.ResourceFeeCents(deployFeeEnvPrefix, deployKind))
	} else {
		emitProjectLifecycle(ctx, cloud.LifecycleDeployFailed, org, p, d, p.Slug+": "+cmp.Or(d.Message, "deploy failed"))
	}
	out := toDeployment(d)
	return &out, nil
}

// failureOwnsProject reports whether a FAILED deployment is entitled to mark the
// whole project broken. Only the deployment a project is actually pointing at can
// — plus the case where it points at nothing, because a first deploy that fails
// leaves a project that has genuinely never served.
//
// A FAILED DEPLOYMENT MUST NOT TAKE DOWN A SITE IT IS NOT SERVING. This was an
// unconditional `p.Status = "error"`, which made "broken" reachable from ANY
// deployment row the org could name, including one already superseded. Measured
// on hanzo-ai: v5 live and serving, completing v3 (a superseded probe) as error
// flipped the project to "error" and the host began 404ing while every byte of v5
// was still correct in the bucket.
//
// The ordinary production shape is the same bug with worse timing: a rebuild that
// fails while the PREVIOUS build is live. The old content is still served and the
// site is up, yet the project was marked broken — and the "report a failed build"
// step every CI workflow carries is exactly what would do it.
//
// The deployment row is still error either way and LifecycleDeployFailed still
// fires, so the failure stays visible where it belongs: on the deployment, not on
// the health of a site that is up.
func failureOwnsProject(currentDeploy, deployID string) bool {
	return currentDeploy == "" || currentDeploy == deployID
}

// ListDeployments returns a project's deploy history, newest version first.
//
// Every deploy of the project is a row — uploads, generated sites, and git/CI
// builds alike — carrying its version, status, source, commit, live URL, file
// count and byte count. The short-lived upload grant a queued git deployment was
// handed is NOT replayed here: it exists only on the 202 that minted it, so a
// grant cannot outlive its build by being fetched again.
//
// Scope: a validated principal is required (403 without one) and the project is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) listDeployments(ctx context.Context, in *projectsRef) (*projectsDeployments, error) {
	_, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListDeployments(ctx, org, p.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	out := make(projectsDeployments, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDeployment(d))
	}
	return &out, nil
}

// GetDeployment returns one deployment of a project by id.
//
// It is how a console follows a build: the status (`queued`, `uploading`,
// `live`, `error`), the message a failure left, and the URL and prefix it went
// live at. Like the history, it never replays the upload grant.
//
// Scope: a validated principal is required (403 without one). Both the project
// and the deployment are resolved within that principal's org, so a deployment
// of another project — or of another tenant — is a 404.
func (o ops) getDeployment(ctx context.Context, in *projectsDeploymentRef) (*projectsDeployment, error) {
	_, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	d, err := o.s.State.store.GetDeployment(ctx, org, p.ID, strings.TrimSpace(in.ID))
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("deployment not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get deployment: %v", err)
	}
	out := toDeployment(d)
	return &out, nil
}

// genID returns "<prefix>_<22-char-url-safe-token>" (96 bits of entropy).
func genID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b), nil
}
