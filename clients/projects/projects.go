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
// Deploy pipeline: a deploy uploads the built static site to OUR S3
// (CLOUD_PROJECTS_BUCKET on s3.hanzo.ai) under "<org>/<slug>/", marks the
// bucket public-read, and records a live URL. The hanzoai/static container
// (the static-app image) serves the same bucket behind the gateway for a pretty
// host; GitHub export is an optional second step that never blocks going live.
package projects

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
	"github.com/hanzoai/cloud/clients/base"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/sites"
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
	// operatorOrgs may bind CUSTOM domains to their sites in addition to a global
	// admin — the platform operator (the deployment's own brand org) manages
	// customer domains until per-org DNS-ownership verification is wired here.
	// Env CLOUD_PLATFORM_OPERATOR_ORGS (comma-separated) overrides; default is the
	// brand org (hanzo). A bound domain only SERVES once its owner points DNS at
	// this edge, so binding without DNS control is inert — the real gate is DNS.
	operatorOrgs map[string]bool
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

type repoView struct {
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type projectView struct {
	ID                  string   `json:"id"`
	Org                 string   `json:"org"`
	Slug                string   `json:"slug"`
	Name                string   `json:"name"`
	Description         string   `json:"description,omitempty"`
	Repo                repoView `json:"repo"`
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
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func toProjectView(p Project) projectView {
	return projectView{
		ID: p.ID, Org: p.Org, Slug: p.Slug, Name: p.Name, Description: p.Description,
		Repo:      repoView{URL: p.RepoURL, Branch: p.RepoBranch, Provider: p.RepoProvider},
		Framework: p.Framework, Status: p.Status, LiveURL: p.LiveURL, Bucket: p.Bucket,
		CurrentDeploymentID: p.CurrentDeploy, CacheControl: p.CacheControl, LastPurgeAt: p.LastPurgeAt,
		Analytics: p.Analytics, Space: p.SpaceId,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type deploymentView struct {
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
}

func toDeploymentView(d Deployment) deploymentView {
	return deploymentView{
		ID: d.ID, ProjectID: d.ProjectID, Version: d.Version, Status: d.Status, Source: d.Source,
		Commit: d.Commit, LiveURL: d.LiveURL, Bucket: d.Bucket, Prefix: d.Prefix,
		Files: d.Files, Bytes: d.Bytes, Message: d.Message, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// Mount wires the projects surface onto app per HIP-0106. Complex flavour: it keeps
// a package global (mounted) for Shutdown and registers cross-package resolvers, so
// it constructs the Service value directly rather than through cloud.Mount.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("projects.Mount: nil zip.App")
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

	b.Log.Info("projects mounted", "bucket", s.State.blob.bucket, "s3", s.State.blob.configured(),
		"ai", s.State.ai != nil, "apex", s.State.apex, "billing", s.State.bill.Enabled(), "brand", deps.Brand)
	return nil
}

// routes registers the projects surface and the mirrored /v1/platform/sites surface.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Post("/v1/projects", cloud.Handle(s, create))
	app.Post("/v1/projects/fork", cloud.Handle(s, fork))
	app.Get("/v1/projects", cloud.Handle(s, list))
	app.Get("/v1/projects/:slug", cloud.Handle(s, get))
	app.Patch("/v1/projects/:slug", cloud.Handle(s, update))
	app.Delete("/v1/projects/:slug", cloud.Handle(s, del))

	app.Post("/v1/projects/:slug/deploy", cloud.Handle(s, deploy))
	app.Post("/v1/projects/:slug/purge", cloud.Handle(s, purge))
	app.Get("/v1/projects/:slug/deployments", cloud.Handle(s, listDeployments))
	app.Get("/v1/projects/:slug/deployments/:id", cloud.Handle(s, getDeployment))
	app.Post("/v1/projects/:slug/deployments/:id/complete", cloud.Handle(s, completeDeployment))
	app.Get("/v1/projects/:slug/domains", cloud.Handle(s, listDomains))
	app.Post("/v1/projects/:slug/domains", cloud.Handle(s, setDomains))

	// /v1/sites — the surface-agnostic deploy_site capability, shared with agents.
	// /v1/sites builds a responsive static site from a brief and deploys it;
	// /v1/sites/deploy is the raw file-manifest deploy; both funnel through the SAME
	// publishSite core as the tar path, so there is one deploy pipeline, one host
	// binding, one metering. Org scope is the IAM-minted X-Org-Id, exactly as
	// /v1/projects.
	app.Post("/v1/sites", cloud.Handle(s, buildSite))
	app.Post("/v1/sites/deploy", cloud.Handle(s, deploySiteFiles))
	app.Get("/v1/sites", cloud.Handle(s, listSites))

	// /v1/platform/sites — the PaaS static-site surface. Static sites are the
	// S3-backed part of the platform (container apps live at /v1/platform/projects,
	// clients/platform); this is the SAME engine as /v1/projects, exposed under the
	// platform namespace so a user's one flow is: create a site → upload a zip (or
	// tar.gz) → bind a custom domain → live. Org/project scope is the IAM-minted
	// X-Org-Id, exactly as the /v1/projects surface. hanzo.app's upload UI posts a
	// zip to POST /v1/platform/sites/:slug/deploy.
	app.Post("/v1/platform/sites", cloud.Handle(s, create))
	app.Get("/v1/platform/sites", cloud.Handle(s, list))
	app.Get("/v1/platform/sites/:slug", cloud.Handle(s, get))
	app.Patch("/v1/platform/sites/:slug", cloud.Handle(s, update))
	app.Delete("/v1/platform/sites/:slug", cloud.Handle(s, del))
	app.Post("/v1/platform/sites/:slug/deploy", cloud.Handle(s, deploy))
	app.Post("/v1/platform/sites/:slug/purge", cloud.Handle(s, purge))
	app.Get("/v1/platform/sites/:slug/deployments", cloud.Handle(s, listDeployments))
	app.Get("/v1/platform/sites/:slug/deployments/:id", cloud.Handle(s, getDeployment))
	app.Get("/v1/platform/sites/:slug/domains", cloud.Handle(s, listDomains))
	app.Post("/v1/platform/sites/:slug/domains", cloud.Handle(s, setDomains))
}

// ---- handlers ----

type createReq struct {
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
}

func create(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body createReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	return createProject(s, c, org, body)
}

// createProject is the ONE path that validates a createReq and persists a
// Project. Both POST /v1/projects and POST /v1/projects/fork funnel through here,
// so slug/framework validation, ID minting, and conflict mapping live in exactly
// one place. It returns 201 with the project view on success.
func createProject(s *cloud.Service[state], c *zip.Ctx, org string, body createReq) error {
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	slug := strings.ToLower(strings.TrimSpace(body.Slug))
	if slug == "" {
		slug = slugify(name)
	}
	if !slugRE.MatchString(slug) {
		return zip.ErrBadRequest("slug must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	// A reserved label (api, admin, login, a brand term, …) may never become a
	// project slug — so it can never be published to <slug>.hanzo.app and shadow a
	// real app/api host. ONE reserved-list source (clients/sites/reserved.go),
	// enforced here at create AND at BindHost.
	if sites.IsReserved(slug) {
		return zip.ErrBadRequest("slug is a reserved subdomain and cannot be used")
	}
	framework := strings.ToLower(strings.TrimSpace(body.Framework))
	if framework == "" {
		framework = "static"
	}
	if !frameworks[framework] {
		return zip.ErrBadRequest("unsupported framework")
	}

	now := time.Now().Unix()
	id, err := genID("proj")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	p := Project{
		ID: id, Org: org, Slug: slug, Name: name, Description: strings.TrimSpace(body.Description),
		RepoURL: strings.TrimSpace(body.Repo.URL), RepoBranch: strings.TrimSpace(body.Repo.Branch),
		RepoProvider: providerFromURL(body.Repo.URL), Framework: framework,
		Status: "draft", Bucket: s.State.blob.bucket, CreatedAt: now, UpdatedAt: now,
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
			return zip.ErrConflict("project slug already exists in this org")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	// Best-effort provision the Base data space (form/forum/data submissions). Runs
	// only after a successful persist so a conflicting create provisions nothing;
	// a Base hiccup is logged and swallowed — it never fails the create.
	provisionSpace(s, c.Context(), &p)
	return c.JSON(http.StatusCreated, toProjectView(p))
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

func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.State.store.ListProjects(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]projectView, 0, len(rows))
	for _, p := range rows {
		out = append(out, toProjectView(p))
	}
	return c.JSON(http.StatusOK, out)
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
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
	return c.JSON(http.StatusOK, toProjectView(p))
}

type updateReq struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	Framework    *string `json:"framework"`
	CacheControl *string `json:"cacheControl"`
	Repo         *struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
	} `json:"repo"`
}

func update(s *cloud.Service[state], c *zip.Ctx) error {
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
	var body updateReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" {
			return zip.ErrBadRequest("name cannot be empty")
		}
		p.Name = n
	}
	if body.Description != nil {
		p.Description = strings.TrimSpace(*body.Description)
	}
	if body.Framework != nil {
		f := strings.ToLower(strings.TrimSpace(*body.Framework))
		if !frameworks[f] {
			return zip.ErrBadRequest("unsupported framework")
		}
		p.Framework = f
	}
	if body.CacheControl != nil {
		cc := strings.TrimSpace(*body.CacheControl)
		if len(cc) > 256 {
			return zip.ErrBadRequest("cacheControl too long")
		}
		if strings.ContainsAny(cc, "\r\n") {
			return zip.ErrBadRequest("cacheControl must not contain newlines")
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
	p.UpdatedAt = time.Now().Unix()
	if err := s.State.store.UpdateProject(c.Context(), p); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("project not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	return c.JSON(http.StatusOK, toProjectView(p))
}

func del(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	slug := slugParam(c)
	p, deleted, err := s.State.store.DeleteProject(c.Context(), org, slug)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("project not found")
	}
	// Release the public subdomain binding so the slug is free to reclaim.
	if uErr := s.State.store.UnbindHost(c.Context(), p.Slug, org, p.Slug); uErr != nil {
		s.Log.Warn("unbind host failed (continuing)", "org", org, "slug", p.Slug, "err", uErr)
	}
	// Best-effort purge of the live site; metadata is already gone, so a purge
	// failure must not resurrect the project — log and continue.
	if s.State.blob.configured() {
		if cli, cErr := s.State.blob.client(); cErr == nil {
			if pErr := purgePrefix(c.Context(), cli, s.State.blob.bucket, sitePrefix(org, p.Slug)); pErr != nil {
				s.Log.Warn("purge site failed (continuing)", "org", org, "slug", p.Slug, "err", pErr)
			}
		}
	}
	// Purge the edge cache-tag so the deleted project stops serving stale copies
	// from the edge; its metadata and S3 origin are already gone.
	purgeTag(s, c.Context(), org, p.Slug)
	return c.NoContent(http.StatusNoContent)
}

// ---- helpers ----

func slugParam(c *zip.Ctx) string { return strings.ToLower(strings.TrimSpace(c.Param("slug"))) }

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
