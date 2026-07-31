// Package projects is the Hanzo Cloud projects control plane: the ONE
// org-scoped store of buildable/deployable sites, shared by every surface that
// shows a user's projects.
//
// Why it exists: hanzo.app (the builder) and console.hanzo.ai (the Projects
// module) must show the SAME projects for the same org. They do, because both
// call this one /v1/projects surface through the gateway, which mints the
// org (X-Org-Id) from the validated IAM JWT (HIP-0111). There is no second
// copy of project state anywhere — this SQLite-backed store is the source of
// truth; the builder keeps only per-project working state (chat, draft files)
// in Hanzo Base.
//
// Surface (all org-scoped; see CONTRACT.md — the published shape console
// consumes):
//
//	POST   /v1/projects                      create
//	GET    /v1/projects                      list (org)
//	GET    /v1/projects/:slug                get
//	PATCH  /v1/projects/:slug                update
//	DELETE /v1/projects/:slug                delete (+ purge S3 site)
//	POST   /v1/projects/:slug/deploy         deploy (tar body | git json)
//	POST   /v1/projects/:slug/purge          purge the edge cache-tag (no redeploy)
//	GET    /v1/projects/:slug/deployments    deploy history
//	GET    /v1/projects/:slug/deployments/:id one deployment
//	POST   /v1/projects/:slug/deployments/:id/complete  CI completion hook
//
// Sites surface (the surface-agnostic deploy_site capability, shared with agents):
//
//	POST   /v1/sites                         generate a responsive site from a brief + deploy
//	POST   /v1/sites/deploy                  deploy a raw file manifest (the deploy_site tool)
//	GET    /v1/sites                         list the org's live sites
//
// Releases (the server-side promote — see release.go; mirrored under
// /v1/platform/sites/:slug/…):
//
//	POST   /v1/sites/:slug/publish                      promote a build output + go live
//	POST   /v1/sites/:slug/releases                     promote only (no flip)
//	GET    /v1/sites/:slug/releases                     rollback menu, newest first
//	POST   /v1/sites/:slug/releases/:release/activate   flip the pointer (go live / roll back)
//
// Deploy pipeline: a deploy uploads the built static site to OUR S3
// (CLOUD_PROJECTS_BUCKET on s3.hanzo.ai) under "<org>/<slug>/", marks the
// bucket public-read, and records a live URL. The hanzoai/static container
// (the static-app image) serves the same bucket behind the gateway for a pretty
// host; GitHub export is an optional second step that never blocks going live.
package projects

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/base"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/fqdn"
	"github.com/zap-proto/zip"
)

// slugRE constrains a project slug to a DNS/identifier-safe token. The slug is
// the org-unique handle AND the S3 key segment AND part of the public URL, so
// this is the injection/traversal guard at the boundary.
var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// frameworks is the closed set of build hints the builder/CI understand. It
// never gates deploy (any artifact is just static files); it tells the pipeline
// how to BUILD a linked repo. "static" means "already built / no build step".
var frameworks = map[string]bool{
	"static": true, "vite": true, "next": true, "react": true,
	"astro": true, "svelte": true, "vue": true, "remix": true, "nuxt": true,
	// WebGL/WASM game engines. Declaring one is also the per-site opt-in for
	// cross-origin isolation (see crossOriginIsolated in sites.go): their
	// multithreaded builds need SharedArrayBuffer, so the site server serves them
	// with COOP/COEP.
	"unity": true, "unreal": true, "godot": true,
}

// state is projects' own data; the shared logger lives in the embedded cloud.Base,
// reached as s.Log.
type state struct {
	store *Store
	blob  *blobStore
	cf    *sites.Purger
	// operatorOrgs may bind a CUSTOM domain to their sites WITHOUT proving they
	// own it, in addition to a global admin — the platform operator (the
	// deployment's own brand org) manages customer DNS, so its bind is the vouch.
	// Env CLOUD_PLATFORM_OPERATOR_ORGS (comma-separated) overrides; default is the
	// brand org (hanzo). Every OTHER org self-serves: it claims the host pending
	// and proves control with the DNS challenge (domains.go).
	operatorOrgs map[string]bool
	// resolver reads the custom-domain ownership challenge (domains.go); nil ⇒ the
	// system resolver. Tests inject a fake so verification is deterministic.
	resolver fqdn.Resolver
	// ai generates static sites from a natural-language brief for POST /v1/sites.
	// It is the SAME shared inference client the agents surface uses (deps.AI) and
	// may be nil when no gateway is configured — buildSite then answers 503 honestly.
	ai cloud.AIClient
	// bill is the ONE per-org gate+meter for product:hosting (reuses deps.Metering,
	// the single commerce client). Every deploy entrypoint gates through it before
	// any work and debits once on success; nil/!Enabled() makes both no-ops so an
	// unconfigured deployment still deploys, just unbilled.
	bill *cloud.ResourceMeter
	// apex is the published-site zone (CLOUD_SITES_APEX, default hanzo.app). The
	// canonical live URL of every deployed site is https://<slug>.<apex>, the pretty
	// host the sites edge (clients/sites) serves — never a raw S3 URL.
	apex string
	// ensureSpace provisions a NEW project's Base data space (its form/forum/data
	// submissions collection) so it accepts submissions at /v1/base out of the box.
	// Wired at Mount to clients/base.EnsureSpace; overridable in tests. Best-effort:
	// see provisionSpace — a failure NEVER fails project creation. nil disables the
	// side effect entirely (space is provisioned lazily on first real use).
	ensureSpace func(ctx context.Context, org string) error
}

// mounted is the active service so Shutdown can release the store. The unified
// binary mounts one projects surface.
var mounted *cloud.Service[state]

// ---- HTTP response shapes (the published contract) ----

type siteRepo struct {
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type siteProject struct {
	ID                  string   `json:"id"`
	Org                 string   `json:"org"`
	Slug                string   `json:"slug"`
	Name                string   `json:"name"`
	Description         string   `json:"description,omitempty"`
	Repo                siteRepo `json:"repo"`
	Framework           string   `json:"framework"`
	Status              string   `json:"status"`
	LiveURL             string   `json:"liveUrl,omitempty"`
	Bucket              string   `json:"bucket,omitempty"`
	CurrentDeploymentID string   `json:"currentDeploymentId,omitempty"`
	// Cache is the site's edge-cache state: the HTML/document Cache-Control policy
	// in effect (TTL) and the last edge-purge time, so a console can show freshness.
	CacheControl string `json:"cacheControl,omitempty"`
	LastPurgeAt  int64  `json:"lastPurgeAt,omitempty"`
	// Analytics is the wired-by-default web-analytics flag (default true). It is the
	// value the app's static-builder reads as deployment.analytics to inject the
	// beacon. Space is the project's Base data space ("<org>/<slug>") a deployed
	// site posts form/forum/data submissions to under /v1/base.
	Analytics bool   `json:"analytics"`
	Space     string `json:"space,omitempty"`
	// ForkedFrom is the parent this project was forked from ("<org>/<slug>" of a
	// published project, or a catalog template slug) — the attribution edge a
	// gallery credits.
	ForkedFrom string `json:"forkedFrom,omitempty"`
	// Visibility is "public" or "private", and Hidden reports platform
	// moderation. Both are always present (never omitempty) so a consumer can
	// tell a real answer from "this API is too old to say" — and so a console
	// never renders a project as public because a field was missing.
	//
	// Authorship is deliberately absent: it is Org, above.
	Visibility   string `json:"visibility"`
	Hidden       bool   `json:"hidden"`
	HiddenReason string `json:"hiddenReason,omitempty"`
	// Upstream/License credit the third-party work this project was published
	// from, and the terms it carries. Omitted when nothing is declared: an absent
	// credit means "nobody has said", not "there is nothing to say".
	Upstream  string `json:"upstream,omitempty"`
	License   string `json:"license,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func toProjectView(p Project) siteProject {
	return siteProject{
		ID: p.ID, Org: p.Org, Slug: p.Slug, Name: p.Name, Description: p.Description,
		Repo:      siteRepo{URL: p.RepoURL, Branch: p.RepoBranch, Provider: p.RepoProvider},
		Framework: p.Framework, Status: p.Status, LiveURL: p.LiveURL, Bucket: p.Bucket,
		CurrentDeploymentID: p.CurrentDeploy, CacheControl: p.CacheControl, LastPurgeAt: p.LastPurgeAt,
		Analytics: p.Analytics, Space: p.SpaceId,
		ForkedFrom: p.ForkedFrom,
		Visibility: p.Visibility, Hidden: p.Hidden, HiddenReason: p.HiddenReason,
		Upstream: p.Upstream, License: p.License,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type siteDeployment struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	Commit    string `json:"commit,omitempty"`
	LiveURL   string `json:"liveUrl,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	Message   string `json:"message,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	// Upload is the prefix-scoped, short-lived S3 write grant handed to CI with a
	// queued git deployment, so it needs no bucket credential (grant.go). Present
	// ONLY on the 202 that creates the deployment — it is never stored and never
	// replayed on a later read, so a grant cannot outlive the build it was minted
	// for by being fetched again.
	Upload *uploadGrant `json:"upload,omitempty"`
}

func toDeploymentView(d Deployment) siteDeployment {
	return siteDeployment{
		ID: d.ID, ProjectID: d.ProjectID, Version: d.Version, Status: d.Status, Source: d.Source,
		Commit: d.Commit, LiveURL: d.LiveURL, Bucket: d.Bucket, Prefix: d.Prefix,
		Files: d.Files, Bytes: d.Bytes, Message: d.Message, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// Mount wires the projects surface onto app per HIP-0106. Complex flavour: it keeps
// a package global (mounted) for Shutdown and registers cross-package resolvers, so
// it constructs the Service value directly rather than through cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("projects.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("projects.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("projects.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("projects.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "projects.db"))
	if err != nil {
		return fmt.Errorf("projects.Mount: open store: %w", err)
	}

	b := cloud.NewBase(deps, "projects")
	s := &cloud.Service[state]{Base: b, State: state{
		store:        store,
		blob:         openBlobStore(),
		cf:           sites.NewPurger(b.Log),
		operatorOrgs: operatorOrgsFromEnv(deps.Brand),
		ai:           deps.AI, // may be nil (no gateway) — buildSite degrades to 503.
		bill:         cloud.NewResourceMeter(deps, hostingProvider),
		apex:         env("CLOUD_SITES_APEX", "hanzo.app"), // the pretty <slug>.<apex> the sites edge serves.
		ensureSpace:  base.EnsureSpace,                     // wired-by-default Base data space (fail-soft).
	}}
	mounted = s

	// Inject the store as the site server's slug→project resolver. This is what
	// lights up `<slug>.hanzo.app` (host-routed at the compose root, ahead of the
	// API pipeline) — it resolves the validated subdomain to its authoritative
	// org + S3 prefix through THIS store. Set once at mount; the site middleware
	// reads it per request.
	sites.SetResolver(siteResolver{store: store})

	// Register the store as a project-ownership resolver for the identity trust
	// boundary (cloud.SanitizeIdentity), so a forged cross-org X-Project-Id is
	// refused before any subsystem reads it. Same inversion as sites.SetResolver —
	// cloud does not import projects.
	cloud.RegisterOrgScopeResolver(projectScopeResolver{store: store})

	routes(app, s)

	// The site edge must hand the browser the bytes we published, unedited: a
	// Cloudflare zone with an HTML rewriter on breaks every hydrating app the
	// plane serves (see sites.rewriters). Assert it off-thread so a slow or
	// unreachable Cloudflare cannot delay the mount, and fail-soft — it only ever
	// logs.
	go s.State.cf.AssertHTMLPassthrough(context.Background())

	b.Log.Info("projects mounted", "bucket", s.State.blob.bucket, "s3", s.State.blob.configured(),
		"ai", s.State.ai != nil, "apex", s.State.apex, "billing", s.State.bill.Enabled(), "brand", deps.Brand)
	return nil
}

// ops binds the projects state to the typed handlers. A zip TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so the
// service arrives as a RECEIVER and every op is a method value.
type ops struct{ s *cloud.Service[state] }

// created marks the ops that answer 201 so the document says what the wire sends.
func created() zip.OpOption { return zip.WithStatus(http.StatusCreated) }

// routes registers the projects surface and the mirrored /v1/platform/sites surface.
//
// Most routes are zip TYPED ops, so the REST route, the OpenAPI document, the MCP
// tool and the CLI command all derive from ONE declaration. Five handlers stay
// untyped and are noted where they are registered: deploy takes a RAW zip/tar body
// (typing it would promise a JSON request the route does not accept), and the four
// billed write paths answer a denied hosting gate with a STRUCTURED 402/503 body
// (cloud.DenyResource) that callers branch on — a typed op declares exactly one
// response, so that body has nowhere to live.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its tenant through the request it parks.
	app.Group("/v1/projects").Use(cloud.Bridge())
	app.Group("/v1/sites").Use(cloud.Bridge())
	app.Group("/v1/platform/sites").Use(cloud.Bridge())

	zip.Post(z, "/v1/projects", o.create, created())
	zip.Post(z, "/v1/projects/fork", o.fork, created())
	zip.Get(z, "/v1/projects", o.list)
	zip.Get(z, "/v1/projects/:slug", o.get)
	zip.Patch(z, "/v1/projects/:slug", o.update)
	zip.Delete(z, "/v1/projects/:slug", o.del)

	// deploy: RAW artifact body (zip/tar, multipart or bare) — see the note above.
	app.Post("/v1/projects/:slug/deploy", cloud.Handle(s, deploy))
	zip.Post(z, "/v1/projects/:slug/purge", o.purge)
	zip.Get(z, "/v1/projects/:slug/deployments", o.listDeployments)
	zip.Get(z, "/v1/projects/:slug/deployments/:id", o.getDeployment)
	zip.Post(z, "/v1/projects/:slug/deployments/:id/complete", o.completeDeployment)
	zip.Get(z, "/v1/projects/:slug/domains", o.listDomains)
	zip.Post(z, "/v1/projects/:slug/domains", o.setDomains)
	zip.Post(z, "/v1/projects/:slug/domains/:host/verify", o.verifyDomain)
	zip.Delete(z, "/v1/projects/:slug/domains/:host", o.releaseDomain)

	// /v1/sites — the surface-agnostic deploy_site capability, shared with agents.
	// /v1/sites builds a responsive static site from a brief and deploys it;
	// /v1/sites/deploy is the raw file-manifest deploy; both funnel through the SAME
	// publishSite core as the tar path, so there is one deploy pipeline, one host
	// binding, one metering. Org scope is the IAM-minted X-Org-Id, exactly as
	// /v1/projects.
	// buildSite + deploySiteFiles: billed write paths whose denied-gate 402/503 body
	// is the contract — see the note above.
	app.Post("/v1/sites", cloud.Handle(s, buildSite))
	app.Post("/v1/sites/deploy", cloud.Handle(s, deploySiteFiles))
	zip.Get(z, "/v1/sites", o.listSites)

	// Releases — how content GETS to a site's serving prefix (release.go). The
	// builder's build output already lives in OUR object store, so publishing is a
	// server-side promote into an immutable content-addressed release plus an
	// atomic pointer flip: no bytes traverse the API and no client holds an S3
	// credential. /publish is create+activate (the 99% path); the two halves stay
	// separable for a staged rollout, and activate doubles as the free rollback.
	siteReleases(app, s, "/v1/sites")
	// The two READ/POINTER release ops are spelled with LITERAL paths, not built
	// from the base: cmd/zipdoc needs a constant path argument to give an op an
	// identity to document, so a computed path silently costs both surfaces their
	// prose. Two lines per surface is the price of a documented route.
	zip.Get(z, "/v1/sites/:slug/releases", o.listReleases)
	zip.Post(z, "/v1/sites/:slug/releases/:release/activate", o.activateRelease)

	// /v1/platform/sites — the PaaS static-site surface. Static sites are the
	// S3-backed part of the platform (container apps live at /v1/platform/projects,
	// clients/platform); this is the SAME engine as /v1/projects, exposed under the
	// platform namespace so a user's one flow is: create a site → upload a zip (or
	// tar.gz) → bind a custom domain → live. Org/project scope is the IAM-minted
	// X-Org-Id, exactly as the /v1/projects surface. hanzo.app's upload UI posts a
	// zip to POST /v1/platform/sites/:slug/deploy.
	zip.Post(z, "/v1/platform/sites", o.create, created())
	zip.Get(z, "/v1/platform/sites", o.list)
	zip.Get(z, "/v1/platform/sites/:slug", o.get)
	zip.Patch(z, "/v1/platform/sites/:slug", o.update)
	zip.Delete(z, "/v1/platform/sites/:slug", o.del)
	// deploy: RAW artifact body — see the note above.
	app.Post("/v1/platform/sites/:slug/deploy", cloud.Handle(s, deploy))
	zip.Post(z, "/v1/platform/sites/:slug/purge", o.purge)
	zip.Get(z, "/v1/platform/sites/:slug/deployments", o.listDeployments)
	zip.Get(z, "/v1/platform/sites/:slug/deployments/:id", o.getDeployment)
	zip.Get(z, "/v1/platform/sites/:slug/domains", o.listDomains)
	zip.Post(z, "/v1/platform/sites/:slug/domains", o.setDomains)
	zip.Post(z, "/v1/platform/sites/:slug/domains/:host/verify", o.verifyDomain)
	zip.Delete(z, "/v1/platform/sites/:slug/domains/:host", o.releaseDomain)
	siteReleases(app, s, "/v1/platform/sites")
	zip.Get(z, "/v1/platform/sites/:slug/releases", o.listReleases)
	zip.Post(z, "/v1/platform/sites/:slug/releases/:release/activate", o.activateRelease)
}

// siteReleases registers the release routes under a site-surface base path. Both
// site surfaces (/v1/sites and /v1/platform/sites) are the same engine, so they
// get the same four routes from this one registration — a release published on
// one is visible and activatable on the other because there is only one store.
func siteReleases(app cloud.Router, s *cloud.Service[state], base string) {
	// publish + create: billed write paths whose denied-gate 402/503 body is the
	// contract — see the note on routes. Their paths stay computed because an
	// untyped route needs no constant to be documented; the typed pair beside each
	// call site does, and is spelled out there.
	app.Post(base+"/:slug/publish", cloud.Handle(s, publishSiteRelease))
	app.Post(base+"/:slug/releases", cloud.Handle(s, createRelease))
}

// ---- handlers ----

type siteCreate struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Framework   string `json:"framework"`
	Repo        struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
	} `json:"repo"`
	// Analytics is the opt-OUT for the wired-by-default analytics beacon: absent
	// (nil) ⇒ ON (the default); explicit false ⇒ off. A pointer so "unset" is
	// distinguishable from "false" — the only way to turn the default off.
	Analytics *bool `json:"analytics"`
	// Visibility is "public" (the default when absent) or "private". Publishing
	// publicly is ungated — that is the point of a community. Going PRIVATE is
	// the paid feature, so an unfunded org asking for it is refused rather than
	// silently downgraded (see resolve).
	Visibility string `json:"visibility"`
	// Upstream/License credit the third-party work this project was published
	// from. Taken from any caller: disclaiming authorship can only cost the
	// publisher credit, so it needs no gate (see Project.Upstream).
	Upstream string `json:"upstream"`
	License  string `json:"license"`
	// ForkedFrom is the lineage stamp. json:"-": it is set by the fork path from
	// the parent it actually resolved, never by the caller, so an attribution edge
	// always names a real ancestor.
	ForkedFrom string `json:"-"`
}

// begin resolves the request and the caller's org in ONE place. The org NEVER
// comes from an In field — an In field is caller-supplied, so a tenant key read
// from one is a cross-tenant read the caller asserted for itself. It comes from
// the request cloud.Bridge parked; off the HTTP path there is none, so the op
// refuses. The request is returned alongside for the helpers that still take it
// (the billing gate, the admin predicate, the edge purge).
func (o ops) begin(ctx context.Context) (*zip.Ctx, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	v, ok := org(c)
	if !ok {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	return c, v, nil
}

// project resolves the caller's own project by the slug in the path. A slug in
// another org is reported not-found, so the slug space leaks no existence.
func (o ops) project(ctx context.Context, c *zip.Ctx, org, slug string) (Project, error) {
	p, err := o.s.State.store.GetProject(ctx, org, normalizeSlug(slug))
	if errors.Is(err, errNotFound) {
		return Project{}, zip.ErrNotFound("project not found")
	}
	if err != nil {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return p, nil
}

// create registers a new project (a static site) in the caller's org. The slug is
// derived from the name when omitted, must be free in this org, and becomes the
// project's public subdomain, so a reserved label is refused.
//
// Example: {"name": "Spring Launch", "slug": "spring-launch", "description": "campaign microsite", "framework": "next", "visibility": "private"}
func (o ops) create(ctx context.Context, in *siteCreate) (*siteProject, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	return createProject(o.s, c, org, *in)
}

// createProject is the ONE path that validates a siteCreate and persists a
// Project. Both POST /v1/projects and POST /v1/projects/fork funnel through here,
// so slug/framework validation, ID minting, and conflict mapping live in exactly
// one place. It returns the project view; the callers declare the 201.
func createProject(s *cloud.Service[state], c *zip.Ctx, org string, body siteCreate) (*siteProject, error) {
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	slug := strings.ToLower(strings.TrimSpace(body.Slug))
	if slug == "" {
		slug = slugify(name)
	}
	if !slugRE.MatchString(slug) {
		return nil, zip.ErrBadRequest("slug must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	// A reserved label (api, admin, login, a brand term, …) may never become a
	// project slug — so it can never be published to <slug>.hanzo.app and shadow a
	// real app/api host. ONE reserved-list source (clients/sites/reserved.go),
	// enforced here at create AND at BindHost.
	if sites.IsReserved(slug) {
		return nil, zip.ErrBadRequest("slug is a reserved subdomain and cannot be used")
	}
	framework := strings.ToLower(strings.TrimSpace(body.Framework))
	if framework == "" {
		framework = "static"
	}
	if !frameworks[framework] {
		return nil, zip.ErrBadRequest("unsupported framework")
	}

	// Resolved BEFORE the row is built, so an unfunded org asking for private is
	// refused without a half-created project left behind.
	vis, err := resolve(s, c, body.Visibility)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	id, err := genID("proj")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	p := Project{
		ID: id, Org: org, Slug: slug, Name: name, Description: strings.TrimSpace(body.Description),
		RepoURL: strings.TrimSpace(body.Repo.URL), RepoBranch: strings.TrimSpace(body.Repo.Branch),
		RepoProvider: providerFromURL(body.Repo.URL), Framework: framework,
		Status: "draft", Bucket: s.State.blob.bucket, CreatedAt: now, UpdatedAt: now,
		ForkedFrom: body.ForkedFrom,
		Visibility: vis,
		Upstream:   credit(body.Upstream), License: credit(body.License),
	}
	if p.RepoBranch == "" && p.RepoURL != "" {
		p.RepoBranch = "main"
	}
	// The ONE place every create path (POST /v1/projects, /v1/projects/fork,
	// /v1/sites) applies the wired-by-default subsystems: analytics ON unless the
	// caller opted out, and the project's Base data-space namespace. Pure, so the
	// defaults are set deterministically before persist.
	setProjectDefaults(&p, body.Analytics)
	if err := s.State.store.CreateProject(c.Context(), p); err != nil {
		if errors.Is(err, errConflict) {
			return nil, zip.ErrConflict("project slug already exists in this org")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	// Best-effort provision the Base data space (form/forum/data submissions). Runs
	// only after a successful persist so a conflicting create provisions nothing;
	// a Base hiccup is logged and swallowed — it never fails the create.
	provisionSpace(s, c.Context(), &p)
	// Give it a canonical repo at git.hanzo.ai, world-readable exactly when the
	// project is.
	share(s, c.Context(), p)
	out := toProjectView(p)
	return &out, nil
}

// setProjectDefaults applies the wired-by-default project settings to a NEW
// project: analytics ON unless the caller opted out (analytics:false), and the
// Base data-space namespace ("<org>/<slug>" — the app's namespace/repoId
// convention, same layout as the S3 sitePrefix). Pure (no I/O), so it is the ONE
// deterministic place defaults are decided; every create path funnels through it
// via createProject. Default-ON but overridable: a nil analytics ⇒ ON, an
// explicit false ⇒ off.
func setProjectDefaults(p *Project, analytics *bool) {
	p.Analytics = analytics == nil || *analytics
	p.SpaceId = sitePrefix(p.Org, p.Slug)
}

// provisionSpace best-effort-provisions a new project's Base data space (the
// submissions collection its deployed site POSTs form/forum/data to). It is
// FAIL-SOFT by construction: a disabled embed (ErrNotEmbedded) or any transient
// Base error is logged and swallowed, so it can NEVER fail project creation — the
// same graceful-degradation policy as the edge cache purge (onPublish). The space
// is idempotent and org-level, so a later deploy or first submission re-ensures it.
func provisionSpace(s *cloud.Service[state], ctx context.Context, p *Project) {
	if s.State.ensureSpace == nil {
		return
	}
	if err := s.State.ensureSpace(ctx, p.Org); err != nil {
		if errors.Is(err, base.ErrNotEmbedded) {
			s.Log.Info("base embed disabled; project data space deferred (set CLOUD_BASE_EMBED=1)", "org", p.Org, "space", p.SpaceId)
			return
		}
		s.Log.Warn("provision base space failed (continuing)", "org", p.Org, "space", p.SpaceId, "err", err)
	}
}

// projectList is the project index — a bare array, as the console reads it.
type projectList = []siteProject

// list lists every project the caller's org owns, newest first. Org is the bound
// isolation boundary, so another tenant's projects are never returned.
//
// Response: [{"id": "prj_8a1b", "slug": "spring-launch", "name": "Spring Launch", "status": "live", "url": "https://spring-launch.hanzo.app", "visibility": "private"}]
func (o ops) list(ctx context.Context, _ *struct{}) (*projectList, error) {
	_, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListProjects(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(projectList, 0, len(rows))
	for _, p := range rows {
		out = append(out, toProjectView(p))
	}
	return &out, nil
}

// projectRef addresses one project by its slug.
type projectRef struct {
	// Slug is the project slug from the path.
	Slug string `json:"slug"`
}

// get reads one project of the caller's org. It returns the status, live URL,
// framework and linked repo, and a slug in another org is reported not-found.
//
// Example: {"slug": "spring-launch"}
func (o ops) get(ctx context.Context, in *projectRef) (*siteProject, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	p, err := o.project(ctx, c, org, in.Slug)
	if err != nil {
		return nil, err
	}
	out := toProjectView(p)
	return &out, nil
}

type siteUpdate struct {
	// Slug is the project slug from the path — which project to edit.
	Slug         string  `json:"slug"`
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	Framework    *string `json:"framework"`
	CacheControl *string `json:"cacheControl"`
	Repo         *struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
	} `json:"repo"`
	// Visibility flips an existing project between "public" and "private". Same
	// ONE rule as at create: public is free, private needs a paid plan.
	Visibility *string `json:"visibility"`
	// Hidden is MODERATION, and the only admin-gated field on this body: it pulls
	// a public project out of the catalogue from admin.hanzo.ai without editing
	// the publisher's own visibility choice, so un-hiding restores exactly what
	// they asked for. A tenant sending it is ignored.
	Hidden       *bool   `json:"hidden"`
	HiddenReason *string `json:"hiddenReason"`
	// Upstream/License credit the third-party work this app was published from —
	// settable after the fact, because the demos that need crediting most are the
	// ones already live. Pointers so "" clears a credit and absent leaves it.
	Upstream *string `json:"upstream"`
	License  *string `json:"license"`
}

// credit normalizes one attribution line: trimmed, single-line, bounded. It is
// free text on purpose — "UI8 — Fitness Pro Website UI Kit" is the honest answer
// and no enum could hold it — but free text that reaches a rendered card must not
// smuggle newlines or run unbounded.
func credit(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// update edits one project of the caller's org. Every field is a pointer, so an
// absent one is left alone and an explicit null or empty clears it; hidden and
// hiddenReason are moderation and are ignored unless the caller is an operator.
//
// Example: {"slug": "spring-launch", "description": "campaign microsite", "visibility": "public", "upstream": "UI8 — Fitness Pro Website UI Kit"}
func (o ops) update(ctx context.Context, in *siteUpdate) (*siteProject, error) {
	c, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, err := o.project(ctx, c, org, in.Slug)
	if err != nil {
		return nil, err
	}
	body := *in
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" {
			return nil, zip.ErrBadRequest("name cannot be empty")
		}
		p.Name = n
	}
	if body.Description != nil {
		p.Description = strings.TrimSpace(*body.Description)
	}
	if body.Framework != nil {
		f := strings.ToLower(strings.TrimSpace(*body.Framework))
		if !frameworks[f] {
			return nil, zip.ErrBadRequest("unsupported framework")
		}
		p.Framework = f
	}
	if body.CacheControl != nil {
		cc := strings.TrimSpace(*body.CacheControl)
		if len(cc) > 256 {
			return nil, zip.ErrBadRequest("cacheControl too long")
		}
		if strings.ContainsAny(cc, "\r\n") {
			return nil, zip.ErrBadRequest("cacheControl must not contain newlines")
		}
		p.CacheControl = cc
	}
	if body.Repo != nil {
		p.RepoURL = strings.TrimSpace(body.Repo.URL)
		p.RepoBranch = strings.TrimSpace(body.Repo.Branch)
		p.RepoProvider = providerFromURL(p.RepoURL)
		if p.RepoBranch == "" && p.RepoURL != "" {
			p.RepoBranch = "main"
		}
	}
	if body.Visibility != nil {
		vis, err := resolve(s, c, *body.Visibility)
		if err != nil {
			return nil, err
		}
		p.Visibility = vis
	}
	// Moderation is admin-only and subtractive; a tenant sending it is ignored.
	// Clearing Hidden clears the reason with it, so a lifted moderation leaves no
	// stale explanation behind for the console to render.
	if body.Hidden != nil && c.IsAdmin() {
		p.Hidden = *body.Hidden
		p.HiddenReason = ""
		if p.Hidden && body.HiddenReason != nil {
			p.HiddenReason = credit(*body.HiddenReason)
		}
	}
	if body.Upstream != nil {
		p.Upstream = credit(*body.Upstream)
	}
	if body.License != nil {
		p.License = credit(*body.License)
	}
	p.UpdatedAt = time.Now().Unix()
	if err := s.State.store.UpdateProject(ctx, p); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("project not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	// Reconcile the repo to whatever this update settled on — including a
	// moderation, which must reach the source and not just the listing.
	share(s, ctx, p)
	out := toProjectView(p)
	return &out, nil
}

// del deletes one project of the caller's org and everything it published. The
// subdomain binding, the release history and the object-store content all go, so
// the slug is free to reclaim and a reclaimed slug inherits no rollback menu.
//
// Example: {"slug": "spring-launch"}
func (o ops) del(ctx context.Context, in *projectRef) (*struct{}, error) {
	_, org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	slug := normalizeSlug(in.Slug)
	p, deleted, err := s.State.store.DeleteProject(ctx, org, slug)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("project not found")
	}
	// Release the public subdomain binding so the slug is free to reclaim.
	if uErr := s.State.store.UnbindHost(ctx, p.Slug, org, p.Slug); uErr != nil {
		s.Log.Warn("unbind host failed (continuing)", "org", org, "slug", p.Slug, "err", uErr)
	}
	// Drop the release rows so a reclaimed slug never inherits the previous
	// owner's rollback menu.
	if rErr := s.State.store.DeleteReleases(ctx, org, p.Slug); rErr != nil {
		s.Log.Warn("delete releases failed (continuing)", "org", org, "slug", p.Slug, "err", rErr)
	}
	// Best-effort purge of the live site; metadata is already gone, so a purge
	// failure must not resurrect the project — log and continue. BOTH spaces go:
	// the legacy mutable prefix AND the site's release space, which is a sibling
	// of it (releaseSpace) and so is not covered by the first purge.
	if s.State.blob.configured() {
		if cli, cErr := s.State.blob.client(); cErr == nil {
			for _, prefix := range []string{sitePrefix(org, p.Slug), releaseSpace(org) + "/" + p.Slug} {
				if pErr := purgePrefix(ctx, cli, s.State.blob.bucket, prefix); pErr != nil {
					s.Log.Warn("purge site failed (continuing)", "org", org, "slug", p.Slug, "prefix", prefix, "err", pErr)
				}
			}
		}
	}
	// Purge the edge cache-tag so the deleted project stops serving stale copies
	// from the edge; its metadata and S3 origin are already gone.
	purgeTag(s, ctx, org, p.Slug)
	return nil, nil
}

// ---- helpers ----

func slugParam(c *zip.Ctx) string { return normalizeSlug(c.Param("slug")) }

// normalizeSlug is the ONE slug normalization both the untyped deploy handler and
// every typed op apply, so a slug means the same thing however it arrived.
func normalizeSlug(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// org resolves the org for a request — the tenant-isolation KEY, and also an
// S3-key segment (sitePrefix = org+"/"+slug). Empty org is allowed only for a
// verified GLOBAL admin (bucketed under the literal "admin" org), matching the
// provisioning control plane. The gateway strips client-supplied identity headers
// and mints X-Org-Id / X-User-IsAdmin only on the JWT-validated path (HIP-0026),
// so neither is spoofable from the edge.
//
// The key comes from principal.Org — the ONE canonical resolver (crm, prompts,
// agents, framework key off the same one), which returns the VALIDATED IAM owner
// VERBATIM (trimmed, ≤128, cloned). Verbatim is load-bearing: the old sanitizeOrg
// FOLD (lowercase + non-alnum→'-' + truncate-32) is NON-injective — two DISTINCT
// validated owners ("acme"/"Acme", or names differing only past 32 chars) collapse
// onto ONE key and thus ONE S3 prefix, itself a cross-tenant collision (one org
// can overwrite/read another's deployed site). Keying off the verbatim owner makes
// the key injective with no lock-out. Since that owner is an S3-key segment,
// orgPathSafe refuses ONLY the traversal class ('/', '\\', or a "."/".." segment)
// → 403, so it can never escape its prefix; SanitizeIdentity already strips
// whitespace/control/format runes upstream, so no legitimate owner is refused.
func org(c *zip.Ctx) (string, bool) {
	if org, ok := principal.Org(c); ok {
		if !orgPathSafe(org) {
			return "", false // org can't be a safe S3-key segment — refuse, don't escape the prefix
		}
		return org, true
	}
	// No validated org. Only a verified GLOBAL admin gets the shared "admin" bucket:
	// principal.Org already required a validated principal, and c.IsAdmin() is set
	// only for a verified global admin (never restored from client input) — require
	// both, so an unvalidated request can never reach the admin namespace.
	if principal.Validated(c) && c.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// orgPathSafe reports whether org is safe to embed VERBATIM as a single S3-key
// path segment (sitePrefix = org+"/"+slug). Because the key is verbatim (no fold),
// this is the guard that keeps a path-hostile owner from escaping its own prefix:
// it refuses ONLY the traversal class — a path separator ('/' or '\\') or a "." /
// ".." relative segment — so case/dots/underscores/length are all preserved and
// distinct owners stay distinct (the non-injective fold is never applied). Empty
// is unsafe. A real IAM owner claim is a single safe token, so nothing legitimate
// is refused here.
func orgPathSafe(org string) bool {
	if org == "" || org == "." || org == ".." {
		return false
	}
	return !strings.ContainsAny(org, "/\\")
}

// sanitizeOrg folds a string to a DNS-ish label (lowercase + non-alnum→'-' +
// truncate-32). It is NOT the tenant key — folding is non-injective and would
// collide distinct tenants (see org, which keys off the verbatim principal.Org).
// It survives only for domains.go's brand/label derivation, where a lossy,
// display-oriented fold is exactly what's wanted.
func sanitizeOrg(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out
}

// slugify derives a slug from a display name: lowercase, non-alnum→'-',
// collapse repeats, trim, cap at 40. Used when the caller omits an explicit
// slug. The result is validated by slugRE before use.
func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := false
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

// providerFromURL classifies a git remote into a known provider for display.
func providerFromURL(raw string) string {
	r := strings.ToLower(raw)
	switch {
	case r == "":
		return ""
	case strings.Contains(r, "github.com"):
		return "github"
	case strings.Contains(r, "gitlab"):
		return "gitlab"
	case strings.Contains(r, "bitbucket"):
		return "bitbucket"
	default:
		return "git"
	}
}

// Shutdown closes the projects store. Idempotent. Mirrors the provisioning
// Shutdown contract so the serve layer releases subsystem resources uniformly.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
