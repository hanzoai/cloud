// Package projects is where your sites live: create one, deploy a build, roll
// back to any release.
//
// It is the ONE org-scoped store of buildable/deployable sites, shared by every
// surface that shows a user's projects.
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

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/hanzoai/cloud/apps/base"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/cloud/internal/fqdn"
	"github.com/hanzoai/cloud/internal/shorten"
	"github.com/hanzoai/cloud/openapi"
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
	// forge resolves the deployment's forge client, through which a published
	// project's source is world-readable exactly when the project is
	// (visibility.go). It reads the machine credential from KMS on first use and
	// re-reads it as it rotates, so nothing here holds a secret.
	forge *forge.Source
	// queue runs one visibility reconcile at a time per project, so two writes
	// cannot land on the forge out of order — see visibility.go.
	queue *queue
}

// mounted is the active service so Shutdown can release the store. The unified
// binary mounts one projects surface.
var mounted *cloud.Service[state]

// ---- HTTP response shapes (the published contract) ----

type projectsRepo struct {
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type projectsProject struct {
	ID                  string       `json:"id"`
	Org                 string       `json:"org"`
	Slug                string       `json:"slug"`
	Name                string       `json:"name"`
	Description         string       `json:"description,omitempty"`
	Repo                projectsRepo `json:"repo"`
	Framework           string       `json:"framework"`
	Status              string       `json:"status"`
	LiveURL             string       `json:"liveUrl,omitempty"`
	Bucket              string       `json:"bucket,omitempty"`
	CurrentDeploymentID string       `json:"currentDeploymentId,omitempty"`
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
	// Key is the project's publishable ingest key, minted at create. It is the
	// value the injected beacon carries and the ONE thing that attributes this
	// site's events; the static-builder reads it beside analytics.
	//
	// Publishable means it belongs in a page's source: it names a write scope and
	// mints no principal, so it is returned in full rather than masked. Masking it
	// would only mean every caller needed a second endpoint to get the thing the
	// page already ships.
	Key string `json:"key,omitempty"`
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
	Upstream string `json:"upstream,omitempty"`
	License  string `json:"license,omitempty"`
	// Tags is the site's browser tag config: platform slug → non-secret pixel id (GA
	// measurement, Meta pixel, …) — what track.js injects and the server CAPI reads,
	// per site. Omitted when none are set. The API SECRET is never here (KMS).
	Tags      map[string]string `json:"tags,omitempty"`
	CreatedAt int64             `json:"createdAt"`
	UpdatedAt int64             `json:"updatedAt"`
}

func toProject(p Project) projectsProject {
	return projectsProject{
		ID: p.ID, Org: p.Org, Slug: p.Slug, Name: p.Name, Description: p.Description,
		Repo:      projectsRepo{URL: p.RepoURL, Branch: p.RepoBranch, Provider: p.RepoProvider},
		Framework: p.Framework, Status: p.Status, LiveURL: p.LiveURL, Bucket: p.Bucket,
		CurrentDeploymentID: p.CurrentDeploy, CacheControl: p.CacheControl, LastPurgeAt: p.LastPurgeAt,
		Analytics: p.Analytics, Space: p.SpaceId, Key: p.Key,
		ForkedFrom: p.ForkedFrom,
		Visibility: p.Visibility, Hidden: p.Hidden, HiddenReason: p.HiddenReason,
		Upstream: p.Upstream, License: p.License, Tags: p.Tags,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// projectsProjects is the org's project list, newest activity first — the
// rollup a console renders. A defined slice type, so the response has a NAME in
// the document and a generated SDK returns a list of the same Project it returns
// singly rather than an anonymous array.
type projectsProjects []projectsProject

type projectsDeployment struct {
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
	Upload *projectsUploadGrant `json:"upload,omitempty"`
}

// projectsDeployments is a project's deploy history, newest version first. A
// defined slice type, so the list has a NAME in the document and a generated SDK
// returns a list of the same Deployment it returns singly.
type projectsDeployments []projectsDeployment

func toDeployment(d Deployment) projectsDeployment {
	return projectsDeployment{
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
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("projects.Mount: open store: %w", err)
	}

	b := cloud.NewBase(deps, "projects")
	s := &cloud.Service[state]{Base: b, State: state{
		store:       store,
		blob:        openBlobStore(),
		cf:          sites.NewPurger(b.Log),
		ai:          deps.AI, // may be nil (no gateway) — buildSite degrades to 503.
		bill:        cloud.NewResourceMeter(deps, hostingProvider),
		apex:        environ.Or("CLOUD_SITES_APEX", "hanzo.app"), // the pretty <slug>.<apex> the sites edge serves.
		ensureSpace: base.EnsureSpace,                            // wired-by-default Base data space (fail-soft).
		forge:       &forge.Source{},                             // the credential is read from KMS on first publish, not here.
		queue:       &queue{},
	}}
	mounted = s

	// Inject the store as the site server's slug→project resolver. This is what
	// lights up `<slug>.hanzo.app` (host-routed at the compose root, ahead of the
	// API pipeline) — it resolves the validated subdomain to its authoritative
	// org + S3 prefix through THIS store. Set once at mount; the site middleware
	// reads it per request.
	sites.SetResolver(siteResolver{store: store})

	// ...and publish the SAME resolver on the internal plane, because in
	// production the edge is never in this process (the pod boots ~25 single-app
	// processes, so the registry above is nil wherever it is read). Keep both:
	// co-resident takes the in-process answer with no hop, split takes the plane.
	setResolverForPlane(siteResolver{store: store})
	exposeSites()

	// The ingest door's key→project resolver, on both paths for the same reason.
	// This is the whole of "a site with no project stops recording": the door asks
	// this store which project a beacon's key names, and a key nothing holds is a
	// refusal.
	analytics.SetKeyResolver(keyResolver{store: store})
	setKeyResolverForPlane(keyResolver{store: store})
	exposeKeys()

	// Register the store as a project-ownership resolver for the identity trust
	// boundary (cloud.SanitizeIdentity), so a forged cross-org X-Project-Id is
	// refused before any subsystem reads it. Same inversion as sites.SetResolver —
	// cloud does not import projects.
	//
	// On BOTH paths, for the same reason as the key and site resolvers above: the
	// boundary runs as edge middleware in every process and this store lives in
	// one, so the in-process registration alone left the guard consulting an empty
	// list — and reading "nobody owns it" as "not foreign" — everywhere it ran.
	cloud.RegisterOrgScopeResolver(projectScopeResolver{store: store})
	setScopeResolverForPlane(projectScopeResolver{store: store})
	exposeOwnership()
	// What the org has built and what of it is serving — the tenant-scoped
	// rollup, which sites_live deliberately is not (figures_rpc.go).
	exposeFigures()

	routes(app, s)

	// The public per-site tag door (GET /v1/tags), served by THIS process because it
	// reads THIS process's project store in-process — the same reason the key/site/scope
	// resolvers above are registered here rather than reached across the plane.
	mountTagDoor(app, s)

	// The site edge must hand the browser the bytes we published, unedited: a
	// Cloudflare zone with an HTML rewriter on breaks every hydrating app the
	// plane serves (see sites.rewriters). Assert it off-thread so a slow or
	// unreachable Cloudflare cannot delay the mount, and fail-soft — it only ever
	// logs.
	go s.State.cf.AssertHTMLPassthrough(context.Background())

	// Nothing a publisher took private may be readable because this process was
	// away when they did it (visibility.go). Off-thread and read-only in the
	// steady state, so a forge that is down delays no mount and changes nothing.
	go sweep(s, context.Background())

	b.Log.Info("projects mounted", "bucket", s.State.blob.bucket, "s3", s.State.blob.configured(),
		"ai", s.State.ai != nil, "apex", s.State.apex, "billing", s.State.bill.Enabled(), "brand", deps.Brand)
	return nil
}

// The prose for the TWO routes on this surface that cannot be a typed op, and
// only those two. Everything else here is a typed zip op whose request schema,
// response schema and description are all projections of its Go signature and
// doc comment (see routes below) — which is what puts them in the OpenAPI
// document, the SDKs, the CLI and the MCP tool list with a SHAPE.
//
// A deploy cannot join them, and the reason is the wire, not effort. Its request
// body is a zip or a tar(.gz) of the built site — raw in the body or as a
// multipart part — OR a JSON git descriptor, chosen by Content-Type; and it
// answers 200 for an artifact it published or 202 for a build it queued. A typed
// In is one JSON shape and zip.WithStatus declares one success status, so a typed
// deploy would refuse the archive that is its main path and mislabel half its
// answers. openapi.Describe is the only way a route with no schema can say
// anything at all, so that is what these two keep.
//
// The ONE rule stated on both is the scope, because it is the rule this plane
// lives or dies by: the org is the gateway-minted, IAM-validated owner and is
// NEVER read from the request, and every project lookup is keyed by (org, slug).
// So a slug is unique within an org and nowhere else, and another tenant's slug
// misses exactly like a nonexistent one — there is no branch anywhere that can
// answer "this exists, but not for you".
//
// Declared through the same registry Register uses, so a description renders only
// while the router actually serves the route — which is also why the two path
// families are declared separately rather than aliased: each is its own live route.
func init() {
	openapi.Describe("/v1/projects/:slug/deploy", http.MethodPost,
		"Deploy a build — upload an archive, or trigger a build from the linked repo",
		"Takes a built site live at `https://<slug>.hanzo.app`. It accepts BOTH shapes on one "+
			"address and the content type decides which: a `zip` or `tar.gz` archive — raw in the "+
			"body or as a multipart file part — is uploaded and served immediately, answering 200 "+
			"with the finished deployment; a JSON body instead queues a build from the project's "+
			"linked repo and answers 202 with a queued deployment and, where one could be minted, "+
			"a scoped upload grant for CI to write with. The git path needs a linked repo (400 "+
			"without one) and is finished later by the completion hook.\n\n"+
			"Billing is fail-closed and fails FIRST: the hosting gate runs before anything is "+
			"parsed or uploaded, so an unfunded org is 402 and an unreachable commerce is 503 "+
			"with nothing written. The debit lands only on success — a failed upload is never "+
			"billed and never flips the live site, and a queued build is billed at completion "+
			"rather than at queue time. A redeploy returns the SAME URL, because slug and apex "+
			"are stable.\n\n"+
			"Scope: a validated principal is required (403 without one) and the project is "+
			"resolved within that principal's org, so another tenant's slug is a 404. Object "+
			"storage must be configured, else 503; an archive that does not walk is a 400 and one "+
			"over the size cap is a 413.")
	openapi.Describe("/v1/platform/sites/:slug/deploy", http.MethodPost,
		"Upload a built site — this is where a zip goes live",
		"Takes a built site live at `https://<slug>.hanzo.app`. The content type decides the "+
			"shape: a `zip` or `tar.gz` — raw in the body or as a multipart file part, which is "+
			"what the platform's upload UI posts — is stored and served immediately, answering "+
			"200 with the finished deployment; a JSON body instead queues a build from the site's "+
			"linked repo and answers 202 with a queued deployment plus, where one could be "+
			"minted, a scoped upload grant for CI. The git path requires a linked repo (400 "+
			"without one).\n\n"+
			"The hosting gate is fail-closed and runs first, before anything is parsed or "+
			"uploaded: 402 for an unfunded org, 503 for unreachable commerce, nothing written. "+
			"The debit lands only on success — a failed upload is never billed and never flips "+
			"the live site — and a redeploy answers the SAME URL, because slug and apex are "+
			"stable.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404. Object storage must "+
			"be configured (503); an archive that does not walk is a 400 and one over the size "+
			"cap is a 413.")
}

// routes registers the projects surface and its two mirrors — /v1/sites and
// /v1/platform/sites — as TYPED ops.
//
// THE MONEY ENVELOPE GOES ON FIRST. cloud.Bridge — what carries the validated
// principal to a typed op, which receives a context and its decoded In and
// nothing else — is the composer's install, once at the root of every program,
// so this package does not install its own.
//
// Six of these ops gate on BALANCE, and a
// refused balance is the fleet's nested {"error":{"code","message"}} 402/503 —
// a contract every metered Hanzo client reads by error.code, and one zip's own
// error envelope does not speak. So a gated op returns cloud.Denied and
// cloud.DenyEnvelope writes those bytes back verbatim. It is installed once,
// before any leaf because fiber runs middleware in registration order, and the
// scope bounds it to the three prefixes this app OWNS (manifest/apps.go) — the
// same confinement the per-prefix installs used to spell out by hand.
//
// The ops themselves are declared on the *zip.App with FULL paths rather than on
// those groups: a group leaf of "" would publish "/v1/projects/" — a different
// path in the document than the one this surface has always served.
//
// THE THREE PATH FAMILIES ARE DECLARED SEPARATELY, not aliased: each is its own
// live route, so each is its own op with its own operationId, its own SDK method
// and its own CLI command — which is the whole point of a typed registration.
// They share the handler, so there is still exactly one implementation.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	app.Use(cloud.DenyEnvelope())
	r := cloud.ZipApp(app)

	zip.Post(r, "/v1/projects", o.create, zip.WithStatus(http.StatusCreated))
	zip.Post(r, "/v1/projects/fork", o.fork, zip.WithStatus(http.StatusCreated))
	zip.Get(r, "/v1/projects", o.list)
	zip.Get(r, "/v1/projects/:slug", o.get)
	zip.Patch(r, "/v1/projects/:slug", o.update)
	zip.Delete(r, "/v1/projects/:slug", o.del, zip.WithStatus(http.StatusNoContent))

	// UNTYPED BY DESIGN — the artifact/git deploy. Its request body is a zip or a
	// tar(.gz) of the built site, sent raw or as a multipart part, OR a JSON git
	// descriptor; and it answers 200 for the artifact it just published or 202 for
	// the build it just queued. A typed In is ONE JSON shape and a typed op
	// declares ONE success status, so typing this route would refuse the archive
	// upload that is its main path and mislabel the other half of its answers. Its
	// prose therefore stays an openapi.Describe (see init above), which is the only
	// way a route with no schema can say anything at all.
	app.Post("/v1/projects/:slug/deploy", cloud.Handle(s, deploy))

	zip.Post(r, "/v1/projects/:slug/purge", o.purge)
	zip.Get(r, "/v1/projects/:slug/deployments", o.listDeployments)
	zip.Get(r, "/v1/projects/:slug/deployments/:id", o.getDeployment)
	zip.Post(r, "/v1/projects/:slug/deployments/:id/complete", o.completeDeployment)
	zip.Get(r, "/v1/projects/:slug/domains", o.listDomains)
	zip.Post(r, "/v1/projects/:slug/domains", o.bindDomains)
	zip.Post(r, "/v1/projects/:slug/domains/:host/verify", o.verifyDomain)
	zip.Delete(r, "/v1/projects/:slug/domains/:host", o.releaseDomain, zip.WithStatus(http.StatusNoContent))

	// /v1/sites — the surface-agnostic deploy_site capability, shared with agents.
	// /v1/sites builds a responsive static site from a brief and deploys it;
	// /v1/sites/deploy is the raw file-manifest deploy; both funnel through the SAME
	// publishSite core as the tar path, so there is one deploy pipeline, one host
	// binding, one metering. Org scope is the IAM-minted X-Org-Id, exactly as
	// /v1/projects.
	zip.Post(r, "/v1/sites", o.buildSite)
	zip.Post(r, "/v1/sites/deploy", o.deploySite)
	zip.Get(r, "/v1/sites", o.listSites)

	// Releases — how content GETS to a site's serving prefix (release.go). The
	// builder's build output already lives in OUR object store, so publishing is a
	// server-side promote into an immutable content-addressed release plus an
	// atomic pointer flip: no bytes traverse the API and no client holds an S3
	// credential. /publish is create+activate (the 99% path); the two halves stay
	// separable for a staged rollout, and activate doubles as the free rollback.
	// Spelled out per surface rather than looped: zipdoc keys an op's prose on a
	// LITERAL path, so a computed one would be filed under nothing.
	zip.Post(r, "/v1/sites/:slug/publish", o.publishSiteRelease)
	zip.Post(r, "/v1/sites/:slug/releases", o.createRelease, zip.WithStatus(http.StatusCreated))
	zip.Get(r, "/v1/sites/:slug/releases", o.listReleases)
	zip.Post(r, "/v1/sites/:slug/releases/:release/activate", o.activateRelease)

	// /v1/platform/sites — the PaaS static-site surface. Static sites are the
	// S3-backed part of the platform (container apps live at /v1/platform/projects,
	// clients/platform); this is the SAME engine as /v1/projects, exposed under the
	// platform namespace so a user's one flow is: create a site → upload a zip (or
	// tar.gz) → bind a custom domain → live. Org/project scope is the IAM-minted
	// X-Org-Id, exactly as the /v1/projects surface. hanzo.app's upload UI posts a
	// zip to POST /v1/platform/sites/:slug/deploy.
	zip.Post(r, "/v1/platform/sites", o.create, zip.WithStatus(http.StatusCreated))
	zip.Get(r, "/v1/platform/sites", o.list)
	zip.Get(r, "/v1/platform/sites/:slug", o.get)
	zip.Patch(r, "/v1/platform/sites/:slug", o.update)
	zip.Delete(r, "/v1/platform/sites/:slug", o.del, zip.WithStatus(http.StatusNoContent))
	// The same polymorphic wire, refused for the same reason as its /v1/projects
	// twin — and this is the surface hanzo.app's upload UI actually posts zips to.
	app.Post("/v1/platform/sites/:slug/deploy", cloud.Handle(s, deploy))
	zip.Post(r, "/v1/platform/sites/:slug/purge", o.purge)
	zip.Get(r, "/v1/platform/sites/:slug/deployments", o.listDeployments)
	zip.Get(r, "/v1/platform/sites/:slug/deployments/:id", o.getDeployment)
	zip.Get(r, "/v1/platform/sites/:slug/domains", o.listDomains)
	zip.Post(r, "/v1/platform/sites/:slug/domains", o.bindDomains)
	zip.Post(r, "/v1/platform/sites/:slug/domains/:host/verify", o.verifyDomain)
	zip.Delete(r, "/v1/platform/sites/:slug/domains/:host", o.releaseDomain, zip.WithStatus(http.StatusNoContent))
	zip.Post(r, "/v1/platform/sites/:slug/publish", o.publishSiteRelease)
	zip.Post(r, "/v1/platform/sites/:slug/releases", o.createRelease, zip.WithStatus(http.StatusCreated))
	zip.Get(r, "/v1/platform/sites/:slug/releases", o.listReleases)
	zip.Post(r, "/v1/platform/sites/:slug/releases/:release/activate", o.activateRelease)
}

// ---- handlers ----

type projectsCreate struct {
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

// CreateProject creates a project — the handle a site is deployed and served
// under — and answers 201 with it in `draft`.
//
// `name` is required; `slug` is derived from the name when omitted and is the
// identifier that matters — it becomes the S3 key segment, the public host
// `<slug>.hanzo.app`, and the handle every later call addresses, so it must
// match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$` and may not be a reserved label
// such as `api` or `admin`. `framework` is a build hint from a closed set,
// defaulting to `static`; it never gates a deploy, it only tells CI how to build
// a linked repo.
//
// Two defaults are worth knowing: the analytics beacon is ON unless `analytics`
// is explicitly false, and `visibility` is `public` unless asked otherwise.
// Publishing publicly is free; PRIVATE is the paid feature, and an unfunded org
// asking for it is refused rather than quietly published as public. Creation
// also provisions the project's data space and a canonical git repo, both
// best-effort — neither can fail the create.
//
// Scope: a validated principal is required (403 without one) and the project is
// created in THAT principal's org. The slug is unique per org, so a slug already
// used in the caller's own org is a 409 while the same slug in another org is
// irrelevant.
func (o ops) create(ctx context.Context, in *projectsCreate) (*projectsProject, error) {
	c, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireBody(c); err != nil {
		return nil, err
	}
	return createProject(o.s, c, org, *in)
}

// createProject is the ONE path that validates a projectsCreate and persists a
// Project. Both POST /v1/projects and POST /v1/projects/fork funnel through here,
// so slug/framework validation, ID minting, and conflict mapping live in exactly
// one place. Its caller answers 201 with the project it returns.
func createProject(s *cloud.Service[state], c *zip.Ctx, org string, body projectsCreate) (*projectsProject, error) {
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
	id := genID("proj")
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
	if err := setProjectDefaults(&p, body.Analytics); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
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
	out := toProject(p)
	return &out, nil
}

// setProjectDefaults applies the wired-by-default project settings to a NEW
// project: analytics ON unless the caller opted out (analytics:false), the Base
// data-space namespace ("<org>/<slug>" — the app's namespace/repoId convention,
// same layout as the S3 sitePrefix), and the publishable ingest key. It is the ONE
// place defaults are decided; every create path funnels through it via
// createProject. Default-ON but overridable: a nil analytics ⇒ ON, an explicit
// false ⇒ off.
//
// THE KEY IS MINTED HERE, WITH THE PROJECT, AND NOWHERE ELSE. That is what makes
// analytics zero-config: a site is created and its beacon already has the one
// credential that attributes it, so nothing downstream has to remember to ask for
// one. Minting is the single non-deterministic step (crypto/rand) and the reason
// this returns an error at all — a project that could not get a key must not be
// created, because it would be a site that silently records nothing.
func setProjectDefaults(p *Project, analytics *bool) error {
	p.Analytics = analytics == nil || *analytics
	p.SpaceId = sitePrefix(p.Org, p.Slug)
	key, err := mintKey()
	if err != nil {
		return err
	}
	p.Key = key
	return nil
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

// loadProject reads one project by (org, slug) and maps the store's answer to the
// wire's: a miss — a slug this tenant does not own, which is indistinguishable
// from a nonexistent one — is the 404 every route on this surface answers, and a
// store failure is the 500 it always was. ONE mapping, so no route can grow a
// branch that tells a caller "this exists, but not for you".
func loadProject(s *cloud.Service[state], ctx context.Context, org, slug string) (Project, error) {
	p, err := s.State.store.GetProject(ctx, org, slug)
	if errors.Is(err, errNotFound) {
		return Project{}, zip.ErrNotFound("project not found")
	}
	if err != nil {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return p, nil
}

// ListProjects returns every project your org owns.
//
// Each row carries the slug, name, framework, visibility, status and live URL —
// the same rows console and the builder render, because there is only one store
// behind both. It requires a validated principal (403 without one) and is keyed
// by that principal's org, so it never contains another tenant's project.
func (o ops) list(ctx context.Context, _ *void) (*projectsProjects, error) {
	_, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListProjects(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(projectsProjects, 0, len(rows))
	for _, p := range rows {
		out = append(out, toProject(p))
	}
	return &out, nil
}

// GetProject returns one project of yours by slug — its settings, its live URL
// and the deployment currently serving it.
//
// Scope: a validated principal is required (403 without one) and the lookup is
// keyed by (org, slug), so another tenant's slug is a 404 exactly like a
// nonexistent one.
func (o ops) get(ctx context.Context, in *projectsRef) (*projectsProject, error) {
	_, _, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	out := toProject(p)
	return &out, nil
}

type projectsUpdate struct {
	// Slug is the project to update, from the path. The URL is the addressing
	// authority — a `slug` in the body cannot move the write to another project.
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
	// Tags sets the site's browser tag config: platform slug → non-secret pixel id
	// (e.g. {"ga4":"G-…","meta":"…"}). track.js injects these first-party and the
	// server CAPI reads them, per site. Absent LEAVES them; a present object REPLACES
	// the set (send {} to clear). The ids are public — they ship in the page — so this
	// is not the SECRET path (a CAPI token is sealed via POST /v1/destinations).
	Tags map[string]string `json:"tags"`
}

// credit normalizes one attribution line: trimmed, single-line, bounded. It is
// free text on purpose — "UI8 — Fitness Pro Website UI Kit" is the honest answer
// and no enum could hold it — but free text that reaches a rendered card must not
// smuggle newlines or run unbounded.
func credit(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
	return shorten.To(s, 200)
}

// sanitizeTags cleans a site's browser tag config: lower-cased platform keys, trimmed
// ids, empties dropped, and both the platform count and each id's length bounded. The
// ids are non-secret (they ship in the page) but reach a stored config and the public
// tag door, so they are bounded like any input.
func sanitizeTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(strings.ToLower(k))
		v = strings.TrimSpace(v)
		if k == "" || v == "" || len(k) > 32 || len(v) > 256 {
			continue
		}
		out[k] = v
		if len(out) >= 32 {
			break
		}
	}
	return out
}

// UpdateProject changes a project's settings, and only the settings you send.
//
// Every field is optional and absent means "leave it": `name` may not be blanked,
// `framework` must stay a known build hint, and `cacheControl` is capped at 256
// characters with no newlines (it becomes a response header). `visibility` flips
// public/private under the same rule as create — public is free, private needs a
// funded org. `upstream` and `license` are free-text credit for third-party work,
// and sending "" clears one. Changing anything reconciles the project's canonical
// git repo, so a visibility change reaches the source and not just the listing.
//
// `hidden`/`hiddenReason` are platform MODERATION and are ignored unless the
// caller is a platform admin; they remove a project from the public catalogue
// without touching the publisher's own visibility choice, so un-hiding restores
// exactly what they asked for.
//
// Scope: a validated principal is required (403 without one) and the project is
// resolved within that principal's org, so another tenant's slug is a 404.
func (o ops) update(ctx context.Context, in *projectsUpdate) (*projectsProject, error) {
	c, _, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	if err := requireBody(c); err != nil {
		return nil, err
	}
	s, body := o.s, in
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
	if body.Tags != nil {
		p.Tags = sanitizeTags(body.Tags)
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
	out := toProject(p)
	return &out, nil
}

// DeleteProject deletes a project and takes its site off the internet.
//
// The metadata delete is authoritative and everything after it is best-effort,
// in this order: the public `<slug>` subdomain binding is released so the slug is
// free to reclaim, the release rows are dropped so a reclaimed slug never
// inherits the previous owner's rollback menu, the S3 origin is purged under
// BOTH `<org>/<slug>/` and the site's sibling release space, and the edge
// cache-tag is flushed. A failure in any of those is logged and the delete still
// answers 204 — resurrecting a project because a purge missed would be worse
// than a leaked prefix.
//
// Scope: a validated principal is required (403 without one) and the project is
// resolved within that principal's org, so another tenant's slug is a 404 and
// nothing of theirs is touched.
func (o ops) del(ctx context.Context, in *projectsRef) (*void, error) {
	_, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	p, deleted, err := s.State.store.DeleteProject(ctx, org, slugOf(in.Slug))
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

// slugParam reads the :slug path segment for the ONE untyped route left on this
// surface (deploy), normalized by the same slugOf a typed op's In goes through.
func slugParam(c *zip.Ctx) string { return slugOf(c.Param("slug")) }

// org resolves the org for a request — the tenant-isolation KEY, and also an
// S3-key segment (sitePrefix = org+"/"+slug). Empty org is allowed only for a
// verified GLOBAL admin (bucketed under the literal "admin" org), matching the
// provisioning control plane. The gateway strips client-supplied identity headers
// and mints X-Org-Id / X-User-IsAdmin only on the JWT-validated path (HIP-0026),
// so neither is spoofable from the edge.
//
// The key comes from principal.Org — the ONE canonical resolver (crm, prompts,
// agents, framework key off the same one), which returns the VALIDATED IAM owner
// VERBATIM (trimmed, ≤128, cloned). Verbatim is load-bearing, and it is now the
// ONLY spelling of an org anywhere in this package: a DNS-ish fold (lowercase +
// non-alnum→'-' + truncate-32) is NON-injective — two DISTINCT validated owners
// ("acme"/"Acme", "team.a"/"team-a", or names differing only past 32 chars)
// collapse onto ONE key. Wherever that key decides something, the collision IS
// the break: as an S3 prefix one org overwrites/reads another's deployed site;
// as the platform-operator set (domains.go) it hands a lookalike tenant the
// DNS-proof bypass. Keying off the verbatim owner makes the key injective with
// no lock-out. Since that owner is an S3-key segment, orgPathSafe refuses ONLY
// the traversal class ('/', '\\', or a "."/".." segment) → 403, so it can never
// escape its prefix; SanitizeIdentity already strips whitespace/control/format
// runes upstream, so no legitimate owner is refused.
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
