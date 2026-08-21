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
//	GET    /v1/projects/edge                 the edge: provider, reach, cache policy
//	GET    /v1/projects/:slug/deployments    deploy history
//	GET    /v1/projects/:slug/deployments/:id one deployment
//	POST   /v1/projects/:slug/deployments/:id/complete  CI completion hook
//
// Sites (the surface-agnostic deploy_site capability, shared with agents):
//
//	POST   /v1/projects/sites                generate a responsive site from a brief + deploy
//	POST   /v1/projects/sites/deploy         deploy a raw file manifest (the deploy_site tool)
//	GET    /v1/projects/sites                list the org's live sites
//	GET    /v1/projects/sites/:slug          one live site
//
// Releases (the server-side promote — see release.go):
//
//	POST   /v1/projects/:slug/publish                      promote a build output + go live
//	POST   /v1/projects/:slug/releases                     promote only (no flip)
//	GET    /v1/projects/:slug/releases                     rollback menu, newest first
//	POST   /v1/projects/:slug/releases/:release/activate   flip the pointer (go live / roll back)
//
// The browser half — GET /v1/projects/tags, the public pk-keyed pixel config —
// is served from tagdoor.go, here because this process owns the store it reads.
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
	"github.com/hanzoai/cloud/apps/base"
	"github.com/hanzoai/cloud/apps/event"
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
	edge  sites.Edge
	// resolver reads the custom-domain ownership challenge (domains.go); nil ⇒ the
	// system resolver. Tests inject a fake so verification is deterministic.
	resolver fqdn.Resolver
	// ai generates static sites from a natural-language brief for
	// POST /v1/projects/sites. It is the SAME shared inference client the agents
	// surface uses (deps.AI) and may be nil when no gateway is configured —
	// buildSite then answers 503 honestly.
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
	// stop ends the background visibility audit, so it does not outlive the store
	// it reads (Shutdown).
	stop context.CancelFunc
}

// mounted is the active service so Shutdown can release the store. The unified
// binary mounts one projects surface.
var mounted *cloud.Service[state]

// ---- HTTP response shapes (the published contract) ----

// projectsRepo is the git source a project builds from, when it has one. A project
// deployed by uploading an artifact has none, and all three fields are then absent.
type projectsRepo struct {
	// URL is the clone address of the repository this project builds from.
	URL string `json:"url,omitempty"`
	// Branch is the ref a push has to touch for this project to rebuild. Pushes to
	// any other branch are ignored.
	Branch string `json:"branch,omitempty"`
	// Provider is the forge the URL was recognised as — it decides which webhook
	// and which credential reach the repository, and is DERIVED from the URL rather
	// than chosen by the caller.
	Provider string `json:"provider,omitempty"`
}

type projectsProject struct {
	// ID is the project's internal identifier. It is stable across a rename, but it
	// is not what the API addresses this project by — `slug` is.
	ID string `json:"id"`
	// Org is the organisation that owns the project, and therefore who pays for it
	// and who may change it. It is also the AUTHORSHIP line a gallery credits;
	// there is no separate author field.
	Org string `json:"org"`
	// Slug is the identifier that MATTERS: the handle every later call addresses,
	// the S3 key segment the site's objects live under, and the label of the public
	// host `<slug>.hanzo.app`. Because it is a hostname it is constrained and
	// reserved labels such as `api` are refused.
	Slug string `json:"slug"`
	// Name is the project's display name, free text a person chose.
	Name string `json:"name"`
	// Description is the one-line summary, which is copied onto forks of this
	// project and shown on a gallery card.
	Description string `json:"description,omitempty"`
	// Repo is the git source this project builds from, empty when it is deployed by
	// uploading an artifact instead.
	Repo projectsRepo `json:"repo"`
	// Framework is a BUILD HINT from a closed set, defaulting to static. It tells CI
	// how to build a linked repo and never gates a deploy, so a wrong value costs a
	// build rather than access.
	Framework string `json:"framework"`
	// Status is where the project stands — whether a build has ever succeeded and
	// whether anything is serving right now.
	Status string `json:"status"`
	// LiveURL is where the site answers today. Absent until something has been
	// deployed.
	LiveURL string `json:"liveUrl,omitempty"`
	// Bucket is the object-store bucket the site's files are served out of.
	Bucket string `json:"bucket,omitempty"`
	// CurrentDeploymentID names the deployment currently serving, so a caller can
	// ask what is live without scanning the history.
	CurrentDeploymentID string `json:"currentDeploymentId,omitempty"`
	// CacheControl is the Cache-Control policy the edge serves this site's HTML
	// under — how long a reader may hold a stale page before asking again. Assets
	// are content-addressed and are not governed by it.
	CacheControl string `json:"cacheControl,omitempty"`
	// LastPurgeAt is when the edge cache was last cleared, as Unix seconds, so a
	// console can say how fresh what readers see actually is. Absent means never.
	LastPurgeAt int64 `json:"lastPurgeAt,omitempty"`
	// Analytics is whether the web-analytics beacon is injected into this site's
	// pages. It is ON by default — a project has to opt out — and it is what the
	// static builder reads to decide whether to inject at all.
	Analytics bool `json:"analytics"`
	// Space is the project's Base data space, which is where a deployed site's form,
	// forum and data submissions land. Absent means the site stores nothing.
	Space string `json:"space,omitempty"`
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
	Visibility string `json:"visibility"`
	// Hidden is PLATFORM MODERATION, and it is a different axis from visibility: it
	// pulls a public project out of the catalogue without editing the publisher's
	// own choice, so un-hiding restores exactly what they asked for. A project is
	// listed only when it is public AND not hidden. Always present, never omitted,
	// for the same reason as visibility.
	Hidden bool `json:"hidden"`
	// HiddenReason is why moderation hid it. Absent when it is not hidden.
	HiddenReason string `json:"hiddenReason,omitempty"`
	// Starred is THIS CALLER's star, not a property of the project — two people
	// in the same org see different values for the same row, which is the whole
	// point of it. Always present so a client can tell "not starred" from "this
	// API is too old to say", the same reason visibility and hidden are.
	Starred bool `json:"starred"`
	// Upstream credits the third-party work this project was published from — a
	// free-text line, because the honest answer is a name and a title that no enum
	// could hold. Absent means NOBODY HAS SAID, not that there is nothing to say.
	Upstream string `json:"upstream,omitempty"`
	// License is the terms that upstream work carries. Absent has the same reading:
	// undeclared, not unencumbered.
	License string `json:"license,omitempty"`
	// Tags is the site's browser tag config: platform slug → non-secret pixel id (GA
	// measurement, Meta pixel, …) — what track.js injects and the server CAPI reads,
	// per site. Omitted when none are set. The API SECRET is never here (KMS).
	Tags map[string]string `json:"tags,omitempty"`
	// CreatedAt is when the project was created, as Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the project's own record last changed, as Unix seconds. A
	// deploy is not an edit of the project, so this does not move on every publish.
	UpdatedAt int64 `json:"updatedAt"`
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
	// ID identifies this one deployment attempt, and is what CI quotes back to
	// complete it.
	ID string `json:"id"`
	// ProjectID is the project this deployment belongs to.
	ProjectID string `json:"projectId"`
	// Version counts deployments of this project from 1, so the history reads as an
	// ordered sequence rather than by timestamp. It is per project, not global.
	Version int `json:"version"`
	// Status is where the attempt got to — queued, live, or failed. A deployment
	// that is live is not necessarily the one SERVING: the project's own
	// currentDeploymentId says which is.
	Status string `json:"status"`
	// Source is what caused the deployment — a git push, an uploaded artifact, a
	// generated site.
	Source string `json:"source"`
	// Commit is the revision that was built, for a deployment that came from a
	// repository. Absent for an uploaded artifact, which has no revision.
	Commit string `json:"commit,omitempty"`
	// LiveURL is where this deployment serves, once it is live.
	LiveURL string `json:"liveUrl,omitempty"`
	// Bucket is the object-store bucket its files were written to.
	Bucket string `json:"bucket,omitempty"`
	// Prefix is the key prefix within that bucket holding EXACTLY this deployment's
	// objects — the unit an upload grant is scoped to, so a grant for one deployment
	// cannot write over another.
	Prefix string `json:"prefix,omitempty"`
	// Files is how many objects the deployment published.
	Files int `json:"files"`
	// Bytes is their total size in bytes.
	Bytes int64 `json:"bytes"`
	// Message is what happened, in words — the build's own note, or on a failure
	// why it failed.
	Message string `json:"message,omitempty"`
	// CreatedAt is when the deployment was queued, as Unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when it last changed state, as Unix seconds — so the gap between
	// the two is how long the build took.
	UpdatedAt int64 `json:"updatedAt"`
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
		edge:        newEdge(context.Background(), b.Log),
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
	event.SetKeyResolver(keyResolver{store: store})
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

	// The public per-site tag door (GET /v1/projects/tags), served by THIS process because it
	// reads THIS process's project store in-process — the same reason the key/site/scope
	// resolvers above are registered here rather than reached across the plane.
	mountTagDoor(app, s)

	// The site edge must hand the browser the bytes we published, unedited: a
	// An edge with a document rewriter on breaks every hydrating app the
	// plane serves (see sites.rewriters). Assert it off-thread so a slow or
	// unreachable edge cannot delay the mount, and fail-soft — it only ever
	// logs.
	go s.State.edge.EnsureVerbatim(context.Background())

	// Nothing a publisher took private — and nothing a deleted project left
	// behind — may be readable because this process was away when it happened
	// (visibility.go). Off-thread and read-only in the steady state, so a forge
	// that is down delays no mount and changes nothing; it keeps trying until it
	// lands, because boot is the least reliable moment to ask the forge anything
	// and this is the only thing that recovers a close nobody noticed missing.
	audited, stop := context.WithCancel(context.Background())
	s.State.stop = stop
	go audit(s, audited)

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
// An archive upload cannot join them, and the reason is the wire, not effort:
// its request body is BYTES — a zip or tar(.gz), raw in the body or as a
// multipart part — and zip decodes every non-empty typed body with
// jsonenc.Unmarshal before the handler runs, so an In here would answer a real
// archive with 400. Typing it would not mis-describe the route, it would break
// it.
//
// It used to be worse than untypeable, it was TWO OPERATIONS: the same address
// also took a JSON git descriptor and answered 202 instead of 200, so no typed
// registration could have described it even in principle. That half is now its
// own typed op (POST .../deployments, startDeployment), which is what let these
// two say something concrete at last. They are untyped and no longer SILENT:
// openapi.Register declares the archive request through openapi.Binary — the
// honest declaration no Go struct can make, rendering `application/octet-stream`
// with `{type: string, format: binary}`, which a generator turns into a file
// parameter — and the deployment they answer with. Before it, both published an
// operationId and a tag and nothing else, indistinguishable from a route that
// takes no body and returns none, so every generated SDK offered a site upload
// with nowhere to put the site.
//
// What Register still cannot carry is FIELD prose: it derives a schema by
// reflection and Go drops comments, so projectsDeployment's properties publish
// bare here. That is a generator gap, not a diligence one — do not hand-write a
// schema beside the struct, which is the drift Register exists to prevent.
//
// The ONE rule stated on both is the scope, because it is the rule this plane
// lives or dies by: the org is the gateway-minted, IAM-validated owner and is
// NEVER read from the request, and every project lookup is keyed by (org, slug).
// So a slug is unique within an org and nowhere else, and another tenant's slug
// misses exactly like a nonexistent one — there is no branch anywhere that can
// answer "this exists, but not for you".
//
// Declared through the same registry Register uses, so a description renders only
// while the router actually serves the route.
func init() {
	const archiveProse = "Takes a built site live at `https://<slug>.hanzo.app` in one call. The body is the " +
		"site itself — a `zip` or `tar.gz` holding `index.html` at its root (or a single wrapper " +
		"directory that does), sent raw or as a multipart file part. It is unpacked to the site's " +
		"own storage prefix and served immediately, answering the finished deployment.\n\n" +
		"It is bounded by the edge body limit (16 MiB by default), and that bound is the whole " +
		"reason the other path exists: an oversized POST is refused by the server BEFORE any " +
		"handler runs and surfaces as an opaque `400 Error when parsing request` that reads like a " +
		"malformed payload rather than a size cap. A site too large for one archive opens a " +
		"deployment with `POST /v1/projects/{slug}/deployments` instead and writes its files straight " +
		"to storage against the scoped grant that answers with — no body limit, and no bytes " +
		"through this API at all.\n\n" +
		"Billing is fail-closed and fails FIRST: the hosting gate runs before anything is parsed " +
		"or uploaded, so an unfunded org is 402 and an unreachable commerce is 503 with nothing " +
		"written. The debit lands only on success — a failed upload is never billed and never " +
		"flips the live site — and a redeploy answers the SAME URL, because slug and apex are " +
		"stable.\n\n" +
		"Scope: a validated principal is required (403 without one) and the site is resolved " +
		"within that principal's org, so another tenant's slug is a 404. Object storage must be " +
		"configured (503); an archive that does not walk is a 400 and one over the size cap is a " +
		"413."

	// THE live archive address, and now there is one. routes() used to post this
	// handler at three — /v1/sites/{slug}/deploy and /v1/platform/sites/{slug}/deploy
	// beside it — and each needed its own declaration here, because a surface
	// declared on the wire and not here reaches the document with nothing to say
	// about itself, which is what `describe` refuses. Two of the three are gone, so
	// two of the declarations are.
	//
	// Register states the two facts the router cannot: the body is BYTES
	// (openapi.Binary — the declaration no Go struct can make) and the answer is a
	// deployment.
	openapi.Register("/v1/projects/:slug/deploy", http.MethodPost, openapi.Binary{}, projectsDeployment{})
	openapi.Describe("/v1/projects/:slug/deploy", http.MethodPost,
		"Upload a built site as one archive and serve it", archiveProse)
}

// routes registers the projects surface as TYPED ops, all of it under
// /v1/projects.
//
// IT USED TO BE THREE SURFACES. The same handlers answered at /v1/sites and at
// /v1/platform/sites, which is one capability wearing three addresses: three
// operationIds, three SDK methods and three CLI commands for one row in one
// store, and a client that learned any one of them had learned a name the other
// two contradict. HIP-0139 §3.1 gives a capability one prefix and §7 closes the
// misfiled pair by folding it, so the sites nouns that had no /v1/projects twin
// moved here and the ones that were byte-identical to a twin were deleted rather
// than given a third spelling. /v1/platform belongs to the platform app and is
// gone from this file entirely.
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
// scope bounds it to the prefix this app OWNS (manifest/apps.go) — the same
// confinement the per-prefix installs used to spell out by hand.
//
// The ops themselves are declared on the *zip.App with FULL paths rather than on
// a group: a group leaf of "" would publish "/v1/projects/" — a different path in
// the document than the one this surface has always served.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	app.Use(cloud.DenyEnvelope())
	r := cloud.ZipApp(app)

	zip.Post(r, "/v1/projects", o.create, zip.WithStatus(http.StatusCreated))
	zip.Post(r, "/v1/projects/fork", o.fork, zip.WithStatus(http.StatusCreated))
	zip.Get(r, "/v1/projects", o.list)

	// SITES, the surface-agnostic deploy_site capability shared with agents, and a
	// COLLECTION rather than a prefix: /v1/projects/sites builds a responsive static
	// site from a brief, /v1/projects/sites/deploy is the raw file-manifest deploy,
	// and both funnel through the SAME publishSite core as the archive path below —
	// one deploy pipeline, one host binding, one metering. It reads as a second
	// collection beside the projects list because that is what it is: the same rows,
	// listed as what a customer calls them (listSites carries the live URL; list
	// carries the project record).
	//
	// `sites` is a RESERVED label (apps/sites reserved.go), so no project can hold
	// that slug and this static segment can never shadow one.
	zip.Post(r, "/v1/projects/sites", o.buildSite)
	zip.Post(r, "/v1/projects/sites/deploy", o.deploySite)
	zip.Get(r, "/v1/projects/sites", o.listSites)
	// One site, by slug. Every sub-resource under a site answered — deployments,
	// releases, publish — and the site itself did not, so the one call a client
	// makes to ask "is it live yet?" returned 404 for a LIVE site exactly as for
	// one that never existed. A CI lane watching for its own publish waited
	// forever on a success it had already achieved.
	zip.Get(r, "/v1/projects/sites/:slug", o.getSite)

	// THE EDGE. It reports on the network in front of every published site — which
	// provider, whether it can act, what it caches and for how long. It is here
	// because this is the app that holds the edge; a separate process to answer one
	// question would be a deployment for a noun, and `edge` names a POSITION, not a
	// product, so it gets no prefix of its own (LLM.md). `edge` is reserved for the
	// same reason `sites` is.
	zip.Get(r, "/v1/projects/edge", o.edge, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))

	zip.Get(r, "/v1/projects/:slug", o.get)
	zip.Patch(r, "/v1/projects/:slug", o.update)
	zip.Delete(r, "/v1/projects/:slug", o.del, zip.WithStatus(http.StatusNoContent))

	// UNTYPED BY DESIGN — and by BYTES alone, now that it is one operation. The
	// request body is a zip or tar(.gz) of the built site, raw or as a multipart
	// part, and zip decodes every non-empty typed body as JSON before the handler
	// runs, so an In here would answer a real archive with 400. Its request and
	// response ARE declared (openapi.Register + openapi.Binary, see init above), so
	// it publishes a shape rather than a bare address; what it still cannot have is
	// zip's own registry — prose lifted per field, an MCP tool and a CLI command.
	app.Post("/v1/projects/:slug/deploy", cloud.Handle(s, deploy))

	zip.Post(r, "/v1/projects/:slug/purge", o.purge)

	// The deployment lifecycle, in the order it runs: open one and take the scoped
	// write grant, then complete it. startDeployment is the typed half that used to
	// share the /deploy address with the archive upload above, disambiguated by
	// Content-Type — two operations at one address, which is exactly why neither
	// could be typed. POST on the collection that already lists them: the verb is
	// the method, the noun is the resource, and nothing new had to be named.
	zip.Post(r, "/v1/projects/:slug/deployments", o.startDeployment, zip.WithStatus(http.StatusAccepted))
	zip.Get(r, "/v1/projects/:slug/deployments", o.listDeployments)
	zip.Get(r, "/v1/projects/:slug/deployments/:id", o.getDeployment)
	zip.Post(r, "/v1/projects/:slug/deployments/:id/complete", o.completeDeployment)
	zip.Get(r, "/v1/projects/:slug/domains", o.listDomains)
	zip.Post(r, "/v1/projects/:slug/domains", o.bindDomains)
	zip.Post(r, "/v1/projects/:slug/domains/:host/verify", o.verifyDomain)
	zip.Delete(r, "/v1/projects/:slug/domains/:host", o.releaseDomain, zip.WithStatus(http.StatusNoContent))

	// Releases — how content GETS to a site's serving prefix (release.go). The
	// builder's build output already lives in OUR object store, so publishing is a
	// server-side promote into an immutable content-addressed release plus an
	// atomic pointer flip: no bytes traverse the API and no client holds an S3
	// credential. /publish is create+activate (the 99% path); the two halves stay
	// separable for a staged rollout, and activate doubles as the free rollback.
	//
	// They lived only under /v1/sites while deployments lived only under
	// /v1/projects, which made a CI client straddle two nouns to ship one site.
	// The whole lifecycle now answers under one — open, write, complete, publish.
	zip.Post(r, "/v1/projects/:slug/publish", o.publishSiteRelease)
	zip.Post(r, "/v1/projects/:slug/releases", o.createRelease, zip.WithStatus(http.StatusCreated))
	zip.Get(r, "/v1/projects/:slug/releases", o.listReleases)
	zip.Post(r, "/v1/projects/:slug/releases/:release/activate", o.activateRelease)
}

// ---- handlers ----

type projectsCreate struct {
	// Name is the project's display name and the only REQUIRED field. When slug is
	// omitted it is also what the slug is derived from.
	Name string `json:"name"`
	// Slug is the handle everything else addresses this project by: the public host
	// `<slug>.hanzo.app`, the object-store key segment, and the path parameter of
	// every later call. Derived from the name when omitted. It is a hostname label,
	// so it is constrained and reserved labels such as `api` or `admin` are refused.
	Slug string `json:"slug"`
	// Description is the one-line summary, copied onto anything forked from this
	// project.
	Description string `json:"description"`
	// Framework is a BUILD HINT from a closed set, defaulting to static. It tells CI
	// how to build a linked repo and never gates a deploy.
	Framework string `json:"framework"`
	// Repo links a git source, so pushes to it rebuild this project. Omit it for a
	// project deployed by uploading an artifact.
	Repo struct {
		// URL is the repository's clone address.
		URL string `json:"url"`
		// Branch is the ref a push must touch to trigger a rebuild; pushes to any
		// other branch are ignored.
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
	// Upstream credits the third-party work this project was published from. It is
	// accepted from any caller: giving away credit can only cost the publisher, so
	// it needs no gate.
	Upstream string `json:"upstream"`
	// License is the terms that upstream work carries.
	License string `json:"license"`
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
	// real app/api host. ONE reserved-list source (apps/sites/reserved.go),
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
	// /v1/projects/sites) applies the wired-by-default subsystems: analytics ON
	// unless the caller opted out, and the project's Base data-space namespace.
	// Pure, so the defaults are set deterministically before persist.
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
	// project is — on a name that STARTS OVER, so a reclaimed slug inherits
	// nothing from the project that held it before (visibility.go).
	born(s, p)
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
	c, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListProjects(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	// ONE query for every star this person holds, not one per row: a list of
	// forty projects asking the database forty-one questions is how a page gets
	// slow for a reason nobody can see. A failure here is not fatal — the list is
	// the answer, and rendering it unstarred beats refusing to render it at all.
	starred, err := o.s.State.store.StarredBy(ctx, org, c.User())
	if err != nil {
		starred = map[string]bool{}
	}
	out := make(projectsProjects, 0, len(rows))
	for _, p := range rows {
		v := toProject(p)
		v.Starred = starred[p.ID]
		out = append(out, v)
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
	c, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	out := toProject(p)
	// One project, so one lookup — and the same non-fatal rule the list follows:
	// an unreadable star is reported as "not starred" rather than losing the
	// project the caller actually asked for.
	if starred, err := o.s.State.store.StarredBy(ctx, org, c.User()); err == nil {
		out.Starred = starred[p.ID]
	}
	return &out, nil
}

type projectsUpdate struct {
	// Slug is the project to update, from the path. The URL is the addressing
	// authority — a `slug` in the body cannot move the write to another project.
	Slug string `json:"slug"`
	// Name replaces the display name. Absent leaves it; the slug never moves with it.
	Name *string `json:"name"`
	// Description replaces the one-line summary. Absent leaves it.
	Description *string `json:"description"`
	// Framework replaces the build hint. It affects the NEXT build only — nothing
	// already deployed is rebuilt.
	Framework *string `json:"framework"`
	// CacheControl replaces the Cache-Control policy the edge serves this site's
	// HTML under. Absent leaves it.
	CacheControl *string `json:"cacheControl"`
	// Repo relinks the git source. Absent leaves the existing link.
	Repo *struct {
		// URL is the repository's clone address.
		URL string `json:"url"`
		// Branch is the ref a push must touch to trigger a rebuild.
		Branch string `json:"branch"`
	} `json:"repo"`
	// Visibility flips an existing project between "public" and "private". Same
	// ONE rule as at create: public is free, private needs a paid plan.
	Visibility *string `json:"visibility"`
	// Hidden is MODERATION, and the only admin-gated field on this body: it pulls
	// a public project out of the catalogue from admin.hanzo.ai without editing
	// the publisher's own visibility choice, so un-hiding restores exactly what
	// they asked for. A tenant sending it is ignored.
	Hidden *bool `json:"hidden"`
	// HiddenReason records WHY moderation hid it, so the action can be explained
	// and reviewed later. Admin-gated like hidden itself.
	HiddenReason *string `json:"hiddenReason"`
	// Upstream credits the third-party work this project was published from, and is
	// settable after the fact because the live demos are the ones that most need
	// crediting. An explicit empty string CLEARS the credit; absent leaves it.
	Upstream *string `json:"upstream"`
	// License is the terms that upstream work carries, with the same clear-versus-
	// leave rule.
	License *string `json:"license"`
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
	share(s, p)
	out := toProject(p)
	return &out, nil
}

// DeleteProject deletes a project and takes its site off the internet.
//
// The metadata delete is authoritative and everything after it is best-effort,
// in this order: the public `<slug>` subdomain binding is released so the slug is
// free to reclaim, the release rows are dropped so a reclaimed slug never
// inherits the previous owner's rollback menu, the git source is retired on
// every copy it has so a reclaimed slug never adopts a repository left behind
// (visibility.go), the S3 origin is purged under BOTH `<org>/<slug>/` and the
// site's sibling release space, and the edge cache-tag is flushed. A failure in
// any of those is logged and the delete still answers 204 — resurrecting a
// project because a purge missed would be worse than a leaked prefix.
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
	// Take the source with it, before this answers: the slug is free to reclaim
	// the moment it does, and a repository left behind is one the next project of
	// that name adopts — commits and all — and then publishes (visibility.go).
	forget(s, p)
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
	// The audit reads the store on a timer, so it ends with it rather than
	// outliving it (visibility.go).
	if mounted.State.stop != nil {
		mounted.State.stop()
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
