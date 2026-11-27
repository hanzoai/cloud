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

	"github.com/hanzoai/cloud/sdk/principal"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// siteHost is the public host key a published project binds and resolves on: the
// BARE `<slug>`. The published edge is `<slug>.<apex>` (one DNS label), because a
// k8s wildcard Ingress host and a Let's Encrypt wildcard cert each match exactly
// ONE label. The bare slug is therefore a GLOBAL, first-come namespace, shared
// with the platform's apps (the PK on site_hosts enforces one holder per name),
// claimed when the project is created: a second org asking for the same slug is
// refused the create with a 409. This is the exact string bound into site_hosts
// (insert, and onPublish for a project made before that) AND the key the
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
// claimHost claims the public host for a published site (first-come per
// (org,slug), idempotent for the owner) and returns the public URL the project
// actually owns plus an optional note when the subdomain is taken or reserved.
func claimHost(s *cloud.Service[state], ctx context.Context, org string, p *Project) (live, note string) {
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
	return live, note
}

func onPublish(s *cloud.Service[state], ctx context.Context, org string, p *Project) (live, note string) {
	live, note = claimHost(s, ctx, org, p)
	purgeEdge(s, ctx, p)
	return live, note
}

// purgeTag flushes the edge cache-tag site-<namespace>-<slug> for a project's site,
// the tag every edge that serves the site's prefix stamps on its pages. It is
// the ONE place that derives the cache-tag and issues the purge, so every purge
// site — deploy (onPublish), domain-bind (setDomains), delete (del), and the
// dedicated POST /v1/project/:slug/purge — shares one tag and one failure policy.
// Best-effort by construction: PurgeTags is a warn-only no-op when the edge (CF) is
// unconfigured, and a purge miss is logged, never fatal — the S3 origin keeps
// serving and the edge self-heals when the short HTML TTL lapses.
func purgeTag(s *cloud.Service[state], ctx context.Context, p Project) {
	if err := s.State.edge.PurgeTags(ctx, sites.CacheTag(cmp.Or(p.Namespace, p.Org), p.Slug)); err != nil {
		s.Log.Warn("edge cache-tag purge failed (continuing)", "org", p.Org, "namespace", p.Namespace, "slug", p.Slug, "err", err)
	}
}

// purgeEdge purges the project's edge cache-tag and stamps LastPurgeAt = now. It is
// the shared core of the two content-freshness paths — the go-live side effects
// (onPublish) and the dedicated purge handler — so purge + stamp happen ONE way. It
// does NOT persist: the caller writes p in its own UpdateProject.
func purgeEdge(s *cloud.Service[state], ctx context.Context, p *Project) {
	purgeTag(s, ctx, *p)
	p.LastPurgeAt = time.Now().Unix()
}

// PurgeProject flushes the site's edge cache without redeploying anything.
//
// It invalidates the edge cache-tag `site-<namespace>-<slug>` and stamps `lastPurgeAt`
// (unix seconds), and it NEVER writes or deletes the S3 origin — the live build
// keeps serving; only stale copies held at the edge drop, so the next request
// re-fetches the current artifact from origin. Idempotent, and an edge that is
// unconfigured or failing is not fatal: `lastPurgeAt` is still stamped and the
// answer is still the updated project.
//
// Scope: a validated principal is required (401 without one) and the project is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) purge(ctx context.Context, in *projectsRef) (*projectsProject, error) {
	_, _, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	purgeEdge(o.s, ctx, &p)
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
// deploy write-path — the tar-artifact path (deployArtifact) and both /v1/project/sites
// paths — funnels through here, so versioning, the S3 write, host binding, the
// lifecycle emit, and the status transitions live in exactly one place (DRY).
// source records how the artifact was produced ("upload" | "generated" | "deploy").
// On an upload failure it marks the deployment "error" and returns the error
// WITHOUT touching the project — a failed deploy never flips a live site. It charges
// nothing either way: a site is billed for what its org stores, and the org is
// re-measured once the new files are in place.
func publishSite(s *cloud.Service[state], ctx context.Context, org string, p Project, st *site, source string) (Deployment, error) {
	now := time.Now().Unix()
	version, err := s.State.store.NextVersion(ctx, p.ID)
	if err != nil {
		return Deployment{}, fmt.Errorf("version: %w", err)
	}
	id := genID("dep")
	d := Deployment{
		ID: id, ProjectID: p.ID, Org: org, Version: version, Status: "uploading",
		Source: source, Bucket: s.State.blob.bucket, Prefix: sitePrefix(p.Namespace, p.Slug),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.InsertDeployment(ctx, d); err != nil {
		return Deployment{}, fmt.Errorf("persist deployment: %w", err)
	}

	prefix, files, total, upErr := s.State.blob.uploadSite(ctx, p.Namespace, p.Slug, p.CacheControl, st)
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
	// The actual edge purge runs AFTER UpdateProject commits the new live pointer
	// so Cloudflare never re-fetches a stale release between purge and DB commit.
	live, note := claimHost(s, ctx, org, &p)
	p.LastPurgeAt = time.Now().Unix()
	d.Status, d.LiveURL, d.Message, d.Prefix, d.Files, d.Bytes, d.UpdatedAt = "live", live, note, prefix, files, total, time.Now().Unix()
	if err := s.State.store.UpdateDeployment(ctx, d); err != nil {
		return d, fmt.Errorf("finalize deployment: %w", err)
	}
	emitProjectLifecycle(ctx, cloud.LifecycleDeployLive, org, p, d, p.Slug+" live ("+live+note+")")

	p.Status, p.LiveURL, p.CurrentDeploy, p.Bucket, p.UpdatedAt = "live", live, d.ID, s.State.blob.bucket, time.Now().Unix()
	refresh(s, org)
	if err := s.State.store.UpdateProject(ctx, p); err != nil {
		return d, fmt.Errorf("finalize project: %w", err)
	}
	purgeTag(s, ctx, p)
	keep(s, org, p, d)
	return d, nil
}

// deploy uploads a BUILT site as one archive and serves it immediately.
//
// The request body is a zip OR tar(.gz) of the built site (index.html at the
// root, or a single wrapper directory that holds it), sent either as a multipart
// file part (a browser <input type=file>) or as the raw body (a curl one-liner).
// It unpacks to OUR S3 under "<org>/<slug>/" and records a live deployment.
//
// It stays UNTYPED because its body is BYTES. zip decodes every non-empty typed
// body with jsonenc.Unmarshal before the handler runs, so an In on this route
// would answer a zip archive with 400 — the conversion would not mis-describe
// the route, it would break it. Its shape is declared instead, at its
// registration, with openapi.Register + openapi.Binary, so the archive request
// and the deployment it answers with both reach the document and the SDKs.
//
// It is ONE operation. It used to be two, disambiguated by Content-Type: an
// `application/json` body enqueued a deployment and answered 202 instead. Two
// operations at one address is what kept the whole route untypeable — a typed op
// declares ONE input and ONE success status — so the enqueue is its own typed
// operation now, POST .../deployments (startDeployment), and this route answers
// only 200 for the archive it just published.
func deploy(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return principal.Refused(c)
	}
	p, err := loadProject(s, c.Context(), org, slugParam(c))
	if err != nil {
		return err
	}

	// The storage gate BEFORE any deploy work: the upload itself is free, and an
	// org past the free storage tier that cannot pay for what it holds is 402 (503
	// when commerce is unreachable) with nothing uploaded.
	if gErr := gateHosting(s, c, org); gErr != nil {
		return cloud.DenyResource(c, gErr)
	}
	return deployArtifact(s, c, org, p)
}

// projectsDeployStart is the body of a deployment start: which build this is.
type projectsDeployStart struct {
	// Slug is the site to deploy, from the path.
	Slug string `json:"slug"`
	// Commit is the git sha this build was produced from, recorded on the
	// deployment so a released site can be traced back to its source. Optional.
	//
	// It is the ONLY field here, and deliberately: the predecessor also accepted
	// `source` and `branch`. `source` was the Content-Type discriminator this
	// split removed. `branch` was accepted and DISCARDED — there is no branch
	// column on a deployment, and the lifecycle event derives the branch from the
	// project's own linked one — so declaring it would publish a settable field
	// that does nothing into the document, every generated SDK and the MCP input
	// schema. A field that is read by nothing is not described as if it were.
	//
	// `url:"-"` because zip binds the query string OVER a decoded body, so
	// without it a `?commit=` the caller never sent would outrank the one it did.
	Commit string `json:"commit" url:"-"`
}

// StartDeployment opens a deployment and hands back a short-lived, prefix-scoped
// grant to write its bytes straight to object storage. Answers 202.
//
// This is the path for a site too large to send as one archive: a real export is
// hundreds of megabytes against a 16 MiB body limit, so the bytes deliberately do
// NOT pass through the API. The answer carries `bucket`, `prefix` and `upload` —
// a presigned POST policy that S3 itself confines to this site's prefix
// (starts-with `<org>/<slug>/`), expires in 30 minutes and bounds each object.
// So a build writes its own files and holds no standing bucket credential; there
// is nothing to rotate and nothing that leaks between tenants. Never guess the
// prefix — it is server-derived, and a guessed one lands where nothing is served.
//
// The deployment is `queued` until POST .../deployments/{id}/complete flips it
// live (or error). That completion is also where DELETION happens: the grant
// authorizes writes only, so a build cannot remove a file, and cloud reconciles
// the prefix against the `keys` manifest the completion carries. A build that
// dies before completing leaves the deployment queued rather than a half-live
// site.
//
// The grant is on the 202 and NOWHERE else — it is never stored and never
// replayed on a later read, so it cannot outlive the build it was minted for. A
// deployment whose grant could not be minted is still created and still
// completable; it simply carries no `upload`, and a caller with no other way to
// write should treat that as the failure it is.
//
// Billing: the deployment is free. The one gate is storage, and it runs BEFORE
// anything is created: an org past the free storage tier that cannot pay a month of
// what it holds is 402, and 503 when commerce is unreachable. What the build uploads
// is billed as storage from the hourly sweep that finds it, never as a deploy.
//
// Scope: a validated principal is required (401 without one) and the site is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) startDeployment(ctx context.Context, in *projectsDeployStart) (*projectsDeployment, error) {
	c, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	s := o.s
	if gErr := gateHosting(s, c, org); gErr != nil {
		return nil, cloud.Denied(gErr)
	}
	now := time.Now().Unix()
	version, err := s.State.store.NextVersion(ctx, p.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "version: %v", err)
	}
	id := genID("dep")
	d := Deployment{
		ID: id, ProjectID: p.ID, Org: org, Version: version, Status: "queued",
		Source: "git", Commit: strings.TrimSpace(in.Commit), Bucket: s.State.blob.bucket,
		Prefix: sitePrefix(p.Namespace, p.Slug), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.InsertDeployment(ctx, d); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist deployment: %v", err)
	}
	// A live site stays live while its next build runs: the edge serves only a
	// live project, so marking it building answered 404 on its hosts until the
	// completion. The deployment row says queued.
	if p.Status != "live" {
		p.Status, p.UpdatedAt = "building", now
		if err := s.State.store.UpdateProject(ctx, p); err != nil {
			s.Log.Warn("set building failed (continuing)", "slug", p.Slug, "err", err)
		}
	}
	emitProjectLifecycle(ctx, cloud.LifecycleBuildStarted, org, p, d, "building "+p.Slug)

	view := toDeployment(d)
	grant, gErr := mintGrant(ctx, s.State.blob, d.Prefix, time.Now())
	if gErr != nil {
		s.Log.Warn("mint upload grant failed (deployment still queued)", "slug", p.Slug, "err", gErr)
	}
	view.Upload = grant
	return &view, nil
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
	ID string `json:"id"`
	// Status is how the build ended: `live` if it succeeded, `error` if it did not.
	// Nothing else is accepted.
	Status string `json:"status"`
	// Commit is the revision that was built, recorded on the deployment.
	Commit string `json:"commit"`
	// LiveURL is a HINT at the address the site should serve at. The public host is
	// claimed by cloud first, so this can refine the URL a deployment reports but
	// can never assert a subdomain another tenant holds.
	LiveURL string `json:"liveUrl"`
	// Message is what happened, in words — on an error completion, why it failed.
	Message string `json:"message"`
	// Files is how many objects CI published.
	Files int `json:"files"`
	// Bytes is their total size in bytes.
	Bytes int64 `json:"bytes"`
	// Keys is the whole release, RELATIVE to the deployment prefix. A live
	// completion that names it is refused with 409 while any of these keys is
	// absent from the store, and the prefix is then reconciled against it: an
	// upload grant authorizes writes only, so CI cannot remove a file, and cloud
	// deletes what this list does not name instead (grant.go). Omit it and nothing
	// is checked or deleted — the prefix only grows, the pre-grant behaviour.
	Keys []string `json:"keys,omitempty"`
	// CopyFrom names another site of the same org whose prefix already holds the
	// bytes of Copy. A host that shares a build with another differs from it by a
	// page or two, so CI uploads those and asks for the rest to be copied inside
	// the object store instead of sent again over the runner's link. A site the
	// org does not hold is a 404. Read only on a live completion that names keys.
	CopyFrom string `json:"copyFrom,omitempty"`
	// Copy lists the keys to take from CopyFrom's prefix, relative to it, copied
	// before the release is checked. Each must also be named in Keys, which stays
	// the whole release, and be a clean relative path; anything else is a 400.
	Copy []string `json:"copy,omitempty"`
}

// CompleteDeployment is the CI completion hook that flips a queued git
// deployment to live (or error) once CI has synced the built site to S3.
//
// `status` must be `live` or `error`. On a LIVE completion the public host is
// claimed FIRST, so the deployment reports the URL it actually OWNS — a
// CI-supplied `liveUrl` is a hint that can refine that URL but can never assert
// a subdomain another tenant holds. `keys` is the whole release, relative to the
// deployment prefix: cloud reconciles the prefix against it so a page deleted
// from the build actually stops serving. A fingerprinted asset the release drops
// stays up to 36 hours, so a tab opened on the previous release can still load
// its chunks. Omit `keys` and nothing is deleted — the prefix only grows.
// Reconciliation runs only on a live completion (pruning against a failed
// build's manifest would delete the site the last good build is still serving)
// and is best-effort, so a stale leftover never turns a successful deploy into a
// 500. No completion is charged: the site is billed for the storage it holds,
// and a live completion re-measures its org.
//
// A live completion that names `keys` is not taken on trust. The objects `copy`
// names are first copied from the `copyFrom` site's prefix, then the prefix is
// listed, and while any key of the manifest is absent the answer is 409 naming
// how many and the first of them. A store that cannot be listed is a 503, and a
// copy that fails is a 502 naming the key. Each of those leaves the deployment
// queued — a release that cannot be proven whole is never marked live — so the
// publisher uploads what is missing and completes again.
//
// Scope: a validated principal is required (401 without one). CI authenticates
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
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status != "live" && status != "error" {
		return nil, zip.ErrBadRequest("status must be live or error")
	}
	e := ending{
		live: status == "live", commit: in.Commit, hint: in.LiveURL, message: in.Message,
		files: in.Files, bytes: in.Bytes, keys: in.Keys,
	}
	if e.live && len(in.Keys) > 0 {
		if e.held, err = o.whole(ctx, org, d, in); err != nil {
			return nil, err
		}
	}
	d, err = land(s, ctx, org, p, d, e)
	if err != nil {
		return nil, err
	}
	out := toDeployment(d)
	return &out, nil
}

// whole proves a live completion's release is in the store before anything marks
// it live, and answers the listing that proved it. It copies what the completion
// asked to have copied, lists the prefix once, and refuses while any key of the
// manifest is absent. Nothing here ends the deployment, so every refusal leaves
// it queued for the publisher to finish and complete again.
//
// The listing it answers is the one land reconciles, so a completion lists the
// prefix once.
func (o ops) whole(ctx context.Context, org string, d Deployment, in *projectsComplete) ([]projectsObject, error) {
	keep := named(in.Keys)
	var from string
	if strings.TrimSpace(in.CopyFrom) != "" && len(in.Copy) > 0 {
		src, err := o.s.State.store.GetProject(ctx, org, slugOf(in.CopyFrom))
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("copyFrom names no site of this org: " + slugOf(in.CopyFrom))
		}
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "get copyFrom: %v", err)
		}
		for _, k := range in.Copy {
			if !validKey(k) || !keep[k] {
				return nil, zip.ErrBadRequest(fmt.Sprintf("copy key %q must be a clean relative path that keys also names", k))
			}
		}
		from = sitePrefix(src.Namespace, src.Slug)
	}
	cli := o.s.State.blob.store
	if from != "" {
		if _, err := copyKeys(ctx, cli, d.Bucket, from, d.Prefix, in.Copy); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "copy from %s: %v", from, err)
		}
	}
	held, err := listPrefix(ctx, cli, d.Bucket, d.Prefix)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage: %v", err)
	}
	if missing := absent(in.Keys, held); len(missing) > 0 {
		shown := strings.Join(missing[:min(len(missing), 20)], ", ")
		if len(missing) > 20 {
			shown += ", …"
		}
		return nil, zip.ErrConflict(fmt.Sprintf("%d of the release's %d keys are not in the store, so deployment %s stays queued; upload them and complete again: %s",
			len(missing), len(keep), d.ID, shown))
	}
	return held, nil
}

// named is the set of keys a completion names, each read the one way a key is
// read here: surrounding space and a leading slash dropped, an empty one ignored.
func named(keys []string) map[string]bool {
	keep := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k = relative(k); k != "" {
			keep[k] = true
		}
	}
	return keep
}

// relative is a manifest key as it names an object under the prefix.
func relative(k string) string { return strings.TrimPrefix(strings.TrimSpace(k), "/") }

// absent is the keys of a manifest that held does not list, in the manifest's own
// order and each once.
func absent(keys []string, held []projectsObject) []string {
	have := make(map[string]bool, len(held))
	for _, o := range held {
		have[o.Key] = true
	}
	var missing []string
	for _, k := range keys {
		if k = relative(k); k != "" && !have[k] {
			have[k] = true // named once, however often the manifest repeats it
			missing = append(missing, k)
		}
	}
	return missing
}

// ending is how a deployment built outside this process ended: CI's completion,
// or a sandbox's (build.go).
type ending struct {
	live            bool
	commit, message string
	// hint is a URL the builder suggests the site serves at. It can refine the
	// URL a deployment reports but never assert a subdomain another tenant holds.
	hint  string
	files int
	bytes int64
	// keys are the objects uploaded, relative to the deployment's prefix; the
	// prefix is reconciled against them on a live ending.
	keys []string
	// held is the prefix as a completion listed it to prove keys present. The
	// reconcile tidies it rather than listing again; nil lists afresh.
	held []projectsObject
}

// land ends deployment d of project p as e says, and is the one place a
// deployment built elsewhere goes live or fails: the host is claimed before the
// URL is stamped, the prefix is reconciled against what was uploaded, the
// project moves only when this deployment owns it, and the lifecycle hears of
// it. It charges nothing: a live ending re-measures the org, whose storage is
// what the site is billed for.
func land(s *cloud.Service[state], ctx context.Context, org string, p Project, d Deployment, e ending) (Deployment, error) {
	now := time.Now().Unix()
	from := d.Status
	d.Status = "error"
	if e.live {
		d.Status = "live"
	}
	d.UpdatedAt = now
	if c := strings.TrimSpace(e.commit); c != "" {
		d.Commit = c
	}
	d.Message = strings.TrimSpace(e.message)
	d.Files, d.Bytes = e.files, e.bytes
	if e.live {
		// Claim the host first, so the deployment reports the URL it OWNS — the
		// same rule publishSite follows. A builder's hint is a hint only: it cannot
		// assert a subdomain another tenant holds. Edge purge fires AFTER
		// EndDeployment, prefix reconciliation, and UpdateProject commit so a raced
		// ending never triggers a spurious purge and Cloudflare never re-caches the
		// previous release.
		live, note := claimHost(s, ctx, org, &p)
		if live != "" {
			if hint := strings.TrimSpace(e.hint); hint != "" {
				live = hint
			}
		} else if d.Message == "" {
			d.Message = note
		}
		d.LiveURL = live
	}
	switch err := s.State.store.EndDeployment(ctx, d, from); {
	case errors.Is(err, errConflict):
		return Deployment{}, zip.ErrConflict("projects: deployment " + d.ID + " was ended by another ending first")
	case err != nil:
		return Deployment{}, zip.Errorf(http.StatusInternalServerError, "update deployment: %v", err)
	}

	// Reconcile the prefix against what the builder says it uploaded, so a deleted
	// page actually stops serving. Only on a LIVE ending: pruning against the
	// manifest of a build that failed would delete the site the previous good
	// build is still serving. Best-effort — the new content is already up, and a
	// stale leftover must not turn a successful deploy into a 500.
	if e.live && len(e.keys) > 0 {
		keep := named(e.keys)
		cli := s.State.blob.store
		var err error
		removed := 0
		switch {
		case e.held != nil:
			removed, err = tidy(ctx, cli, d.Bucket, d.Prefix, e.held, keep)
		default:
			removed, err = reconcilePrefix(ctx, cli, d.Bucket, d.Prefix, keep)
		}
		if err != nil {
			s.Log.Warn("reconcile failed (new content is live; stale files remain)", "slug", p.Slug, "err", err)
		} else if removed > 0 {
			s.Log.Info("reconciled site prefix", "slug", p.Slug, "prefix", d.Prefix, "removed", removed)
		}
	}
	if e.live {
		refresh(s, org)
	}

	p.UpdatedAt = now
	if e.live {
		p.Status, p.LiveURL, p.CurrentDeploy, p.LastPurgeAt = "live", d.LiveURL, d.ID, time.Now().Unix()
		// The build wrote the legacy mutable prefix, so — exactly as in
		// publishSite — going live there takes the pointer back from any release.
		// (claimHost already ran above, where the owned URL was decided.)
		p.CurrentRelease = ""
	} else if failureOwnsProject(p.CurrentDeploy, d.ID) && p.Status != "live" && p.CurrentRelease == "" {
		// A project that serves — a release is a site even with no deployment
		// pointed at — stays up, and one whose newer build is running reads
		// building until that build ends.
		newer, err := s.State.store.Building(ctx, org, p.ID, d.Version)
		if err != nil {
			return Deployment{}, zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
		if !newer {
			p.Status = "error"
		}
	}
	if err := s.State.store.UpdateProject(ctx, p); err != nil {
		return Deployment{}, zip.Errorf(http.StatusInternalServerError, "update project: %v", err)
	}
	if e.live {
		purgeTag(s, ctx, p)
		keep(s, org, p, d)
		emitProjectLifecycle(ctx, cloud.LifecycleDeployLive, org, p, d, p.Slug+" live ("+d.LiveURL+")")
	} else {
		emitProjectLifecycle(ctx, cloud.LifecycleDeployFailed, org, p, d, p.Slug+": "+cmp.Or(d.Message, "deploy failed"))
	}
	return d, nil
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
// Scope: a validated principal is required (401 without one) and the project is
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
// Scope: a validated principal is required (401 without one). Both the project
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

// projectsObjects is everything a deployment's prefix holds.
type projectsObjects struct {
	// Objects is every object under the prefix in the store's key order: empty,
	// never absent, when the prefix holds nothing.
	Objects []projectsObject `json:"objects"`
}

// ListObjects returns every object under a deployment's prefix as the object
// store itself records it: each key relative to the prefix, its size in bytes
// and its ETag.
//
// It is what lets a publisher upload only what changed. The publisher diffs its
// export against this listing — the store's own record, never a manifest kept
// from an earlier run — and uploads the files that differ or are absent; an
// object written in one part carries the MD5 of its bytes as its ETag, so a local
// digest is the whole comparison. A file the diff wrongly skips is still caught:
// the completion refuses a release while any key of its manifest is absent.
//
// The listing is whole or refused. A store that is unconfigured or fails while
// listing answers 503 naming the cause, never a partial list.
//
// Scope: a validated principal is required (401 without one). Both the project
// and the deployment are resolved within that principal's org, so a deployment
// of another project — or of another tenant — is a 404.
func (o ops) listObjects(ctx context.Context, in *projectsDeploymentRef) (*projectsObjects, error) {
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
	cli := o.s.State.blob.store
	held, err := listPrefix(ctx, cli, d.Bucket, d.Prefix)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage: %v", err)
	}
	return &projectsObjects{Objects: held}, nil
}

// genID returns "<prefix>_<22-char-url-safe-token>" (96 bits of entropy).
// genID mints this package's ids: sixteen random bytes in base64url, which is the
// shape its rows already carry — shorter than the hex mint.ID makes, and not
// interchangeable with it for that reason.
//
// No error. crypto/rand.Read fills the buffer or panics; since Go 1.24 it cannot
// report a short read, so there was never a failure for a caller to handle.
func genID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
}
