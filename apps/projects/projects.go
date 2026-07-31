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
	// operatorOrgs may bind a CUSTOM domain to their sites WITHOUT proving they
	// own it, in addition to a global admin — the platform operator (the
	// deployment's own brand org) manages customer DNS, so its bind is the vouch.
	// Env CLOUD_PLATFORM_OPERATOR_ORGS (comma-separated) overrides; default is the
	// brand org (hanzo). Keyed by the VERBATIM validated IAM owner — the same value
	// org() resolves, never a fold (operatorOrgsFromEnv says why). Every OTHER org
	// self-serves: it claims the host pending and proves control with the DNS
	// challenge (domains.go).
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

func toProjectView(p Project) projectView {
	return projectView{
		ID: p.ID, Org: p.Org, Slug: p.Slug, Name: p.Name, Description: p.Description,
		Repo:      repoView{URL: p.RepoURL, Branch: p.RepoBranch, Provider: p.RepoProvider},
		Framework: p.Framework, Status: p.Status, LiveURL: p.LiveURL, Bucket: p.Bucket,
		CurrentDeploymentID: p.CurrentDeploy, CacheControl: p.CacheControl, LastPurgeAt: p.LastPurgeAt,
		Analytics: p.Analytics, Space: p.SpaceId,
		ForkedFrom: p.ForkedFrom,
		Visibility: p.Visibility, Hidden: p.Hidden, HiddenReason: p.HiddenReason,
		Upstream: p.Upstream, License: p.License,
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
	// Upload is the prefix-scoped, short-lived S3 write grant handed to CI with a
	// queued git deployment, so it needs no bucket credential (grant.go). Present
	// ONLY on the 202 that creates the deployment — it is never stored and never
	// replayed on a later read, so a grant cannot outlive the build it was minted
	// for by being fetched again.
	Upload *uploadGrant `json:"upload,omitempty"`
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

// The prose for this whole surface. NOTHING here is a typed op: every route is a
// raw handler, because this plane's wire is polymorphic where an op has to be
// singular — a deploy takes a tar, a zip, a multipart part or a git JSON body and
// answers 200 or 202 depending which, and three path families share one set of
// handlers. zipdoc lifts prose from a typed op's doc comment, so without these
// declarations all thirty-nine operations would publish an operationId and nothing
// else: every generated SDK method and every CLI command on the tenant's project
// plane, unable to explain itself.
//
// The ONE rule stated on every operation below is the scope, because it is the rule
// this plane lives or dies by: the org is the gateway-minted, IAM-validated owner
// and is NEVER read from the request, and every project lookup is keyed by (org,
// slug). So a slug is unique within an org and nowhere else, and another tenant's
// slug misses exactly like a nonexistent one — there is no branch anywhere that can
// answer "this exists, but not for you".
//
// Declared through the same registry Register uses, so a description renders only
// while the router actually serves the route — which is also why the three path
// families are declared separately rather than aliased: each is its own live route.
func init() {
	// ---- /v1/projects — the project plane ----

	openapi.Describe("/v1/projects", http.MethodPost,
		"Create a project — the handle a site is deployed and served under",
		"Creates a project and answers 201 with it in `draft`. `name` is required; `slug` is "+
			"derived from the name when omitted and is the identifier that matters — it becomes "+
			"the S3 key segment, the public host `<slug>.hanzo.app`, and the handle every later "+
			"call addresses, so it must match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$` and may not "+
			"be a reserved label such as `api` or `admin`. `framework` is a build hint from a "+
			"closed set, defaulting to `static`; it never gates a deploy, it only tells CI how to "+
			"build a linked repo.\n\n"+
			"Two defaults are worth knowing: the analytics beacon is ON unless `analytics` is "+
			"explicitly false, and `visibility` is `public` unless asked otherwise. Publishing "+
			"publicly is free; PRIVATE is the paid feature, and an unfunded org asking for it is "+
			"refused with 402 rather than quietly published as public. Creation also provisions "+
			"the project's data space and a canonical git repo, both best-effort — neither can "+
			"fail the create.\n\n"+
			"Scope: a validated principal is required (403 without one) and the project is "+
			"created in THAT principal's org. The slug is unique per org, so a slug already used "+
			"in the caller's own org is a 409 while the same slug in another org is irrelevant.")
	openapi.Describe("/v1/projects", http.MethodGet,
		"Every project your org owns",
		"Lists the caller org's projects with their slug, name, framework, visibility, status "+
			"and live URL — the same rows console and the builder render, because there is only "+
			"one store behind both. Requires a validated principal (403 without one) and is keyed "+
			"by that principal's org, so it never contains another tenant's project.")
	openapi.Describe("/v1/projects/fork", http.MethodPost,
		"Fork a starter template or a published project into your own org",
		"Creates a NEW project in the caller's org seeded from a parent named by `slug`, and "+
			"answers 201 with the child. The parent is resolved templates FIRST — the caller "+
			"org's own private templates ahead of the public gallery — and only then as the live "+
			"project uniquely serving that slug, which is the same resolution the edge uses to "+
			"serve `<slug>.hanzo.app`: what you can browse is what you can fork. A template's "+
			"`variant` picks its format, page or theme.\n\n"+
			"A fork copies the recipe, not the bytes. A live parent contributes its linked repo "+
			"so the child builds from the same source, but the parent's deployed release is never "+
			"copied — releases are per-tenant, and the fork publishes its own. `name` and `target` "+
			"override the child's name and slug, defaulting to the parent's. Lineage is stamped "+
			"server-side from the parent actually resolved and cannot be supplied by the caller, "+
			"so an attribution edge always names a real ancestor.\n\n"+
			"Scope: a validated principal is required (403 without one) and the child lands in "+
			"that principal's org. It goes through the same create path as a plain create, so the "+
			"same slug rules, the same 409 on a slug already taken in the caller's org, and the "+
			"same 402 when an unfunded org asks for private all apply. An unknown parent slug, or "+
			"a template variant that does not exist, is a 404.")
	openapi.Describe("/v1/projects/:slug", http.MethodGet,
		"One project by its slug",
		"Answers the project — its name, framework, visibility, status, linked repo, live URL "+
			"and timestamps. Requires a validated principal (403 without one) and resolves the "+
			"slug within THAT principal's org, so a slug that belongs to another tenant is a 404, "+
			"indistinguishable from one that was never created.")
	openapi.Describe("/v1/projects/:slug", http.MethodPatch,
		"Change a project's settings, leaving the rest alone",
		"Updates only the fields present in the body and answers the whole project after the "+
			"change. `name`, `description`, `framework`, `cacheControl`, the linked `repo`, "+
			"`visibility` and the `upstream`/`license` credits are all settable; an omitted field "+
			"is untouched, and a credit sent as an empty string is cleared.\n\n"+
			"The SLUG IS NOT SETTABLE. It is the public host and the storage prefix, so renaming "+
			"it would move a live site out from under its own URL — create a new project instead. "+
			"Flipping `visibility` to private runs the same paid gate as at create and answers "+
			"402 for an unfunded org rather than leaving it public. `hidden` and `hiddenReason` "+
			"are MODERATION and are honoured only for a global admin; a tenant sending them is "+
			"silently ignored, which is deliberate — moderation must not edit the publisher's own "+
			"visibility choice, so lifting it restores exactly what they asked for.\n\n"+
			"Scope: a validated principal is required (403 without one) and the project is "+
			"resolved within that principal's org, so another tenant's slug is a 404.")
	openapi.Describe("/v1/projects/:slug", http.MethodDelete,
		"Delete a project and take its site down",
		"Removes the project and answers 204. This is not just a row: the public subdomain "+
			"binding is released so the slug can be reclaimed, every release row is dropped so a "+
			"future owner of that slug never inherits the previous one's rollback menu, the "+
			"stored site objects are purged from both the live prefix and the release space, and "+
			"the edge cache tag is flushed so nothing keeps serving from cache.\n\n"+
			"The metadata delete is the point of no return — the cleanup that follows is "+
			"best-effort and a failure in it is logged rather than resurrecting the project, so a "+
			"successful 204 means the project is gone even if some bytes are reclaimed late. "+
			"Scope: a validated principal is required (403 without one) and the project is "+
			"resolved within that principal's org, so another tenant's slug is a 404 and cannot "+
			"be deleted.")
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
	openapi.Describe("/v1/projects/:slug/purge", http.MethodPost,
		"Flush the edge cache for a site without redeploying it",
		"Purges the project's edge cache tag so stale copies stop being served, stamps the "+
			"purge time, and answers the updated project. It NEVER writes or deletes stored "+
			"objects — the live build keeps serving from the origin, only cached copies drop — so "+
			"it is safe to call at any time and is the right tool when the origin is already "+
			"correct and the edge is not.\n\n"+
			"A purge that cannot reach the edge is non-fatal: the timestamp is still stamped and "+
			"the answer is still 200, so treat the response as 'the purge was requested', not "+
			"'every edge node has dropped it'. Scope: a validated principal is required (403 "+
			"without one) and the project is resolved within that principal's org, so another "+
			"tenant's slug is a 404.")
	openapi.Describe("/v1/projects/:slug/deployments", http.MethodGet,
		"The project's deploy history",
		"Lists the project's deployments — version, status, source, commit, file and byte "+
			"counts, live URL and timestamps — which is where a deployment id for the detail and "+
			"completion routes comes from. Requires a validated principal (403 without one); the "+
			"project is resolved within that principal's org, so another tenant's slug is a 404 "+
			"and its deployments are unreachable.")
	openapi.Describe("/v1/projects/:slug/deployments/:id", http.MethodGet,
		"One deployment",
		"Answers a single deployment's status, version, source, commit, counts and live URL — "+
			"how a queued git build is polled to completion. Requires a validated principal (403 "+
			"without one); both the project and the deployment are read within that principal's "+
			"org, so a deployment id belonging to another tenant, or to another project, is a "+
			"404.")
	openapi.Describe("/v1/projects/:slug/deployments/:id/complete", http.MethodPost,
		"Finish a queued build — the CI completion hook",
		"Closes out a deployment that the git path queued, flipping it to `live` or `error` and "+
			"answering the deployment. `status` is required and must be exactly one of those two "+
			"(400 otherwise); `commit`, `message`, `files` and `bytes` are recorded as reported.\n\n"+
			"On a live completion the platform claims the public host itself and reports the URL "+
			"it OWNS — a `liveUrl` in the body is a hint that can refine that, never a way to "+
			"assert a subdomain another tenant holds. `keys`, the manifest CI just uploaded, is "+
			"what replaces a delete-enabled sync: an upload grant authorizes writes only, so CI "+
			"cannot remove anything, and the prefix is reconciled against this list instead. Omit "+
			"`keys` and NOTHING is deleted — the prefix only grows, which is the safe default. "+
			"Reconciliation runs only on a live completion, so a failed build can never prune the "+
			"site the previous good one published.\n\n"+
			"Scope: this is an ordinary org-scoped route, not an unauthenticated webhook — CI "+
			"calls it with an org-scoped token, and the project and deployment are both resolved "+
			"within that org, so another tenant's deployment is a 404.")
	openapi.Describe("/v1/projects/:slug/domains", http.MethodGet,
		"Every custom hostname this site holds, live or pending",
		"Answers the site's verified hosts plus every pending claim, and for each pending one "+
			"the EXACT DNS records still owed and the hostname to point them at. It is the "+
			"domains panel in one call: a claim holds the name so nobody else can take it, but "+
			"only a verified host actually routes. Requires a validated principal (403 without "+
			"one); the project is resolved within that principal's org, so another tenant's slug "+
			"is a 404.")
	openapi.Describe("/v1/projects/:slug/domains", http.MethodPost,
		"Point your own domain at this site",
		"Attaches one or more custom hostnames and answers a row per host. WHICH OUTCOME YOU "+
			"GET depends on whether ownership is already established: a global admin, or the "+
			"platform-operator org that manages customer DNS, binds VERIFIED immediately, because "+
			"that bind is itself the vouch; every other org gets a PENDING claim plus the DNS "+
			"challenge to publish, and the host does not route until the verify call proves "+
			"control.\n\n"+
			"Claims are first-come and idempotent for the same site: repeating the call returns "+
			"the SAME token rather than invalidating a record the customer has already published. "+
			"A host already bound to another site is a 409, a reserved label is a 400, and a "+
			"hostname we operate is refused with 403 for a non-vouched caller — those names are "+
			"assigned by the platform, and no DNS proof is even possible in a zone we run.\n\n"+
			"Scope: a validated principal is required (403 without one), the project is resolved "+
			"within that principal's org, and the claim is recorded against that org — so proving "+
			"a domain for one tenant never lets another bind it.")
	openapi.Describe("/v1/projects/:slug/domains/:host/verify", http.MethodPost,
		"Check the DNS proof for a claimed domain and go live if it passes",
		"Resolves the ownership challenge for a pending claim. On success the host is promoted "+
			"and starts routing at the edge immediately, and the edge cache is flushed so it "+
			"serves the right site from the first request. An already-verified host answers its "+
			"current state unchanged, so the call is safe to repeat.\n\n"+
			"A proof that is NOT yet visible is not an error: the answer is 200 with the claim "+
			"still pending and a detail saying what the lookup found, because the check genuinely "+
			"ran and DNS simply has not propagated — retry rather than re-claiming. A host this "+
			"site has not claimed is a 404.\n\n"+
			"Scope: a validated principal is required (403 without one), and both the project and "+
			"the claim are looked up within that principal's org, so a claim held by another "+
			"tenant cannot be verified from here.")
	openapi.Describe("/v1/projects/:slug/domains/:host", http.MethodDelete,
		"Stop serving this site on a custom domain",
		"Unbinds the hostname from the site and answers 204. The host stops routing here and "+
			"the edge cache is flushed so nothing keeps answering from cache; the name is "+
			"released, so it can then be claimed again — by this site or by any other. The site "+
			"itself is untouched and keeps serving at its own `<slug>.hanzo.app` host.\n\n"+
			"Scope: a validated principal is required (403 without one) and the project is "+
			"resolved within that principal's org, so another tenant's slug is a 404 and its "+
			"domains cannot be released from here.")

	// ---- /v1/sites — the surface-agnostic deploy_site capability ----

	openapi.Describe("/v1/sites", http.MethodPost,
		"Describe a site in words and get it generated and deployed live",
		"Generates a responsive static site from a natural-language `brief`, deploys it, and "+
			"answers the live URL with the resolved slug, the deployment id and the file list. "+
			"The project is created if the slug does not exist yet and reused if it does, so this "+
			"one call covers both the first publish and a regeneration. `slug` and `name` are "+
			"optional — a slug is derived from the generated title, and a usable one is minted "+
			"when nothing good can be derived, so a deploy never fails purely for lack of a "+
			"name.\n\n"+
			"Order matters and is fail-closed: the hosting gate runs BEFORE any inference, so a "+
			"denied caller is 402 or 503 with nothing generated, nothing uploaded and no model "+
			"tokens spent. Generation and hosting are billed to the same payer. A brief that is "+
			"empty or over the cap is a 400, as is a generation that does not parse; a failed "+
			"upload is never billed.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is created "+
			"and stored under THAT principal's org — the same org rule as the project plane, "+
			"which is why a site made here is an ordinary project visible at `/v1/projects`. "+
			"Object storage must be configured (503), and so must inference (503).")
	openapi.Describe("/v1/sites/deploy", http.MethodPost,
		"Deploy a site from a file manifest you supply",
		"Takes a map of path to file content, deploys it, and answers the live URL with the "+
			"resolved slug, the deployment id and the file list — the raw half of the site "+
			"capability, for a site that is already written rather than generated. The project is "+
			"created if the slug is new and reused if not.\n\n"+
			"Hand-built files run through the SAME validation, viewport injection and guards a "+
			"generated site does, so they are exactly as safe and as responsive; and it funnels "+
			"into the same publish core as every other deploy path, so versioning, host binding "+
			"and metering happen once, in one place. The hosting gate is fail-closed and runs "+
			"before the upload: 402 unfunded, 503 unreachable, nothing written. `files` is "+
			"required (400), a manifest over the file-count cap is a 400, and a file set that "+
			"does not validate is a 400 naming why.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is stored "+
			"under THAT principal's org. Object storage must be configured, else 503.")
	openapi.Describe("/v1/sites", http.MethodGet,
		"Your org's live sites and their URLs",
		"Lists only the projects that are actually LIVE, each with its pretty URL, name and "+
			"last update — narrower than the project list, which includes drafts and failures, "+
			"and the right read for 'what is my org currently serving'. Requires a validated "+
			"principal (403 without one) and is keyed by that principal's org.")

	// ---- releases: the server-side promote, on both site surfaces ----

	openapi.Describe("/v1/sites/:slug/publish", http.MethodPost,
		"Publish a build output and take it live in one call",
		"Promotes a build output into a new immutable release and points the site at it, "+
			"answering the release marked active with the live URL. This is create-plus-activate "+
			"in sequence with no extra semantics — the two halves stay separately callable for a "+
			"staged rollout, and they cannot drift because this path is literally both.\n\n"+
			"No bytes traverse the API. `source` is a path RELATIVE to the caller org's own "+
			"storage space, and the org segment is prepended server-side from the validated "+
			"principal while the bucket never appears in the request at all — so the worst a "+
			"hostile source can name is something the caller's own org already owns, and no "+
			"client ever holds a storage credential. A release id is a digest of the manifest it "+
			"was built from, which makes re-publishing identical bytes idempotent by "+
			"construction: same content, same id, no copy.\n\n"+
			"Promoting is the billable work and the hosting gate runs before any copy, so this "+
			"can never become a way to deploy for free: 402 unfunded, 503 unreachable. Scope: a "+
			"validated principal is required (403 without one) and the site is resolved within "+
			"that principal's org, so another tenant's slug is a 404. Object storage must be "+
			"configured (503); a source that breaks the object or byte guards is a 400 or 413 and "+
			"a source that moved under the scan is a 409.")
	openapi.Describe("/v1/sites/:slug/releases", http.MethodPost,
		"Promote a build output into a release WITHOUT serving it",
		"Creates a new immutable release from `source` and answers 201 with it — the staged "+
			"half of publishing, for running whatever check you want against a release before "+
			"anyone sees it. Nothing is served until the activate call; the live site is "+
			"untouched.\n\n"+
			"The source is a path relative to the caller org's OWN storage space, with the org "+
			"segment prepended server-side from the validated principal and the bucket never in "+
			"the request, so a release can only ever be built from bytes the caller's org already "+
			"owns. The id is a digest of the manifest, so promoting unchanged content returns the "+
			"existing release rather than copying again.\n\n"+
			"This is where the copy happens, so this is where the hosting gate runs: 402 for an "+
			"unfunded org, 503 for unreachable commerce, before any bytes move. Scope: a "+
			"validated principal is required (403 without one) and the site is resolved within "+
			"that principal's org, so another tenant's slug is a 404. Object storage must be "+
			"configured (503).")
	openapi.Describe("/v1/sites/:slug/releases", http.MethodGet,
		"The site's releases, newest first — the rollback menu",
		"Lists the site's retained releases with their id, object and byte counts, source and "+
			"creation time, marking which one is currently active. This is what a rollback picks "+
			"from, so the retention bound matters: each publish reclaims releases past the "+
			"retention depth, and the live release is never a reclaim candidate. Requires a "+
			"validated principal (403 without one); the site is resolved within that principal's "+
			"org, so another tenant's slug is a 404.")
	openapi.Describe("/v1/sites/:slug/releases/:release/activate", http.MethodPost,
		"Point the site at a release — going live, and equally rolling back",
		"Flips the site's pointer to an existing release and answers it marked active. Serving "+
			"reads through that pointer, so the change is one atomic update with nothing rebuilt "+
			"and nothing re-copied — which is exactly why rolling back is the SAME operation "+
			"aimed at an older release, and is free.\n\n"+
			"It verifies the bytes before it flips, so it can fail two distinguishable ways and "+
			"the difference is the fix: a release nobody can name, or whose row is gone, is a "+
			"404; a release still listed but whose bytes were reclaimed by retention is a 410, "+
			"meaning that rollback target is not coming back and the content must be published "+
			"again. Not billed — no new content is produced, only a pointer moved.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404.")

	// ---- /v1/platform/sites — the PaaS static-site surface ----
	//
	// The same engine and the same handlers as /v1/projects, under the platform
	// namespace, so a user's one flow is create a site, upload a zip, bind a
	// domain, live. Each is its own live route and so needs its own declaration;
	// where the behaviour is identical the prose says so rather than inventing a
	// difference that is not in the code.

	openapi.Describe("/v1/platform/sites", http.MethodPost,
		"Create a static site on the platform",
		"Creates a site and answers 201 with it in `draft`. It is the SAME operation as "+
			"creating a project — one engine, one store — surfaced under the platform namespace "+
			"where container apps live next door, so a site created here is the same row a "+
			"project call sees. `name` is required and `slug` defaults from it; the slug becomes "+
			"the storage prefix and the public host `<slug>.hanzo.app`, so it must match "+
			"`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$` and may not be a reserved label. Analytics is "+
			"on unless explicitly disabled, and `private` visibility is the paid option — an "+
			"unfunded org asking for it is 402, never silently public.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is created "+
			"in that principal's org, where its slug must be unique — a collision within the "+
			"caller's own org is a 409.")
	openapi.Describe("/v1/platform/sites", http.MethodGet,
		"Every static site your org owns",
		"Lists the caller org's sites with their slug, name, framework, visibility, status and "+
			"live URL. The same rows the project list answers — one store, two namespaces — so "+
			"nothing created through either surface is missing here. Requires a validated "+
			"principal (403 without one) and is keyed by that principal's org.")
	openapi.Describe("/v1/platform/sites/:slug", http.MethodGet,
		"One static site by its slug",
		"Answers the site's name, framework, visibility, status, linked repo, live URL and "+
			"timestamps. Requires a validated principal (403 without one) and resolves the slug "+
			"within THAT principal's org, so another tenant's slug is a 404 and cannot be "+
			"distinguished from one that never existed.")
	openapi.Describe("/v1/platform/sites/:slug", http.MethodPatch,
		"Change a site's settings, leaving the rest alone",
		"Updates only the fields present in the body — `name`, `description`, `framework`, "+
			"`cacheControl`, the linked `repo`, `visibility` and the `upstream`/`license` "+
			"credits — and answers the whole site afterwards. The SLUG IS NOT SETTABLE: it is the "+
			"public host and the storage prefix, so a rename would move a live site out from "+
			"under its own URL. Going private runs the paid gate and answers 402 for an unfunded "+
			"org; `hidden` is moderation and is honoured only for a global admin, ignored from a "+
			"tenant.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404.")
	openapi.Describe("/v1/platform/sites/:slug", http.MethodDelete,
		"Delete a static site and take it down",
		"Removes the site and answers 204, releasing its subdomain binding so the slug can be "+
			"reclaimed, dropping its release rows so a future owner inherits no rollback menu, "+
			"purging the stored objects from both the live prefix and the release space, and "+
			"flushing the edge cache. The metadata delete is the point of no return; the cleanup "+
			"after it is best-effort, so a 204 means the site is gone even where some bytes are "+
			"reclaimed late.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404.")
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
	openapi.Describe("/v1/platform/sites/:slug/purge", http.MethodPost,
		"Flush the edge cache for a site without redeploying it",
		"Purges the site's edge cache tag, stamps the purge time, and answers the updated site. "+
			"Stored objects are never written or deleted — the live build keeps serving from the "+
			"origin and only cached copies drop — so this is the right tool when the origin is "+
			"already correct and the edge is stale. An edge that cannot be reached is non-fatal: "+
			"the timestamp is stamped and the answer is still 200, so read it as 'requested', not "+
			"'every node has dropped it'.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404.")
	openapi.Describe("/v1/platform/sites/:slug/deployments", http.MethodGet,
		"The site's deploy history",
		"Lists the site's deployments — version, status, source, commit, file and byte counts, "+
			"live URL and timestamps — and is where a deployment id for the detail route comes "+
			"from. Requires a validated principal (403 without one); the site is resolved within "+
			"that principal's org, so another tenant's slug is a 404.")
	openapi.Describe("/v1/platform/sites/:slug/deployments/:id", http.MethodGet,
		"One deployment",
		"Answers a single deployment's status, version, source, commit, counts and live URL — "+
			"how a queued build is polled to completion. Requires a validated principal (403 "+
			"without one); both the site and the deployment are read within that principal's org, "+
			"so a deployment belonging to another tenant, or to another site, is a 404. The "+
			"completion hook that finishes a queued build lives on the project surface.")
	openapi.Describe("/v1/platform/sites/:slug/domains", http.MethodGet,
		"Every custom hostname this site holds, live or pending",
		"Answers the site's verified hosts plus every pending claim, and for each pending one "+
			"the exact DNS records still owed and the hostname to point them at. A claim holds "+
			"the name against anyone else taking it, but only a verified host actually routes. "+
			"Requires a validated principal (403 without one); the site is resolved within that "+
			"principal's org, so another tenant's slug is a 404.")
	openapi.Describe("/v1/platform/sites/:slug/domains", http.MethodPost,
		"Point your own domain at this site",
		"Attaches one or more custom hostnames and answers a row per host. A global admin, or "+
			"the platform-operator org that manages customer DNS, binds VERIFIED immediately — "+
			"that bind is itself the vouch; every other org gets a PENDING claim plus the DNS "+
			"challenge to publish, and the host does not route until it is verified. Claims are "+
			"first-come and idempotent for the same site, so repeating the call returns the SAME "+
			"token rather than invalidating a record already published.\n\n"+
			"A host already bound to another site is a 409, a reserved label is a 400, and a "+
			"hostname we operate is 403 for a non-vouched caller — those are assigned by the "+
			"platform, and no DNS proof is possible in a zone we run. Scope: a validated "+
			"principal is required (403 without one), the site is resolved within that "+
			"principal's org, and the claim is recorded against that org.")
	openapi.Describe("/v1/platform/sites/:slug/domains/:host/verify", http.MethodPost,
		"Check the DNS proof for a claimed domain and go live if it passes",
		"Resolves the ownership challenge for a pending claim. On success the host is promoted, "+
			"starts routing at the edge immediately, and the edge cache is flushed so it serves "+
			"the right site from the first request; an already-verified host answers unchanged, "+
			"so the call is safe to repeat. A proof not yet visible is NOT an error — the answer "+
			"is 200 with the claim still pending and a detail saying what the lookup found, "+
			"because the check ran and DNS has simply not propagated. A host this site has not "+
			"claimed is a 404.\n\n"+
			"Scope: a validated principal is required (403 without one), and both the site and "+
			"the claim are looked up within that principal's org, so another tenant's claim "+
			"cannot be verified from here.")
	openapi.Describe("/v1/platform/sites/:slug/domains/:host", http.MethodDelete,
		"Stop serving this site on a custom domain",
		"Unbinds the hostname and answers 204. The host stops routing here and the edge cache "+
			"is flushed so nothing keeps answering from cache; the name is released and can be "+
			"claimed again by this or any other site. The site itself is untouched and keeps "+
			"serving at its own `<slug>.hanzo.app` host.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404.")
	openapi.Describe("/v1/platform/sites/:slug/publish", http.MethodPost,
		"Publish a build output and take it live in one call",
		"Promotes a build output into a new immutable release and points the site at it, "+
			"answering the release marked active with the live URL — create-plus-activate in one "+
			"call, the same operation the sites surface offers, over the same single store.\n\n"+
			"No bytes traverse the API: `source` is a path RELATIVE to the caller org's own "+
			"storage space, the org segment is prepended server-side from the validated "+
			"principal, and the bucket never appears in the request — so a source can only ever "+
			"name bytes the caller's org already owns, and no client holds a storage credential. "+
			"A release id is a digest of its manifest, making re-publishing identical bytes "+
			"idempotent by construction.\n\n"+
			"Promoting is the billable work and the gate runs before any copy: 402 unfunded, 503 "+
			"unreachable. Scope: a validated principal is required (403 without one) and the site "+
			"is resolved within that principal's org, so another tenant's slug is a 404. Object "+
			"storage must be configured (503); a source breaking the object or byte guards is a "+
			"400 or 413, and one that moved under the scan is a 409.")
	openapi.Describe("/v1/platform/sites/:slug/releases", http.MethodPost,
		"Promote a build output into a release WITHOUT serving it",
		"Creates a new immutable release from `source` and answers 201 with it — the staged "+
			"half of publishing, for checking a release before anyone sees it. The live site is "+
			"untouched until the activate call.\n\n"+
			"The source is relative to the caller org's OWN storage space, with the org segment "+
			"prepended server-side and the bucket never in the request, so a release can only be "+
			"built from bytes that org already owns; the id is a digest of the manifest, so "+
			"promoting unchanged content returns the existing release instead of copying again. "+
			"The copy happens here, so the hosting gate runs here: 402 unfunded, 503 unreachable, "+
			"before any bytes move.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404. Object storage must "+
			"be configured (503).")
	openapi.Describe("/v1/platform/sites/:slug/releases", http.MethodGet,
		"The site's releases, newest first — the rollback menu",
		"Lists the site's retained releases with their id, object and byte counts, source and "+
			"creation time, marking which is active. Retention is bounded: each publish reclaims "+
			"releases past the retention depth, and the live release is never a reclaim "+
			"candidate — so this list is what a rollback can actually reach. Requires a validated "+
			"principal (403 without one); the site is resolved within that principal's org, so "+
			"another tenant's slug is a 404.")
	openapi.Describe("/v1/platform/sites/:slug/releases/:release/activate", http.MethodPost,
		"Point the site at a release — going live, and equally rolling back",
		"Flips the site's pointer to an existing release and answers it marked active. Serving "+
			"reads through that pointer, so this is one atomic update with nothing rebuilt and "+
			"nothing re-copied, and rolling back is the SAME operation aimed at an older release, "+
			"for free.\n\n"+
			"The bytes are checked before the flip, so the two failures are distinguishable and "+
			"the difference is the fix: an unknown release, or one whose row is gone, is a 404; a "+
			"release still listed but whose bytes were reclaimed by retention is a 410 — that "+
			"target is not coming back and the content must be published again. Not billed: no "+
			"new content is produced, only a pointer moved.\n\n"+
			"Scope: a validated principal is required (403 without one) and the site is resolved "+
			"within that principal's org, so another tenant's slug is a 404.")
}

// routes registers the projects surface and the mirrored /v1/platform/sites surface.
func routes(app cloud.Router, s *cloud.Service[state]) {
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
	app.Post("/v1/projects/:slug/domains/:host/verify", cloud.Handle(s, verifyDomain))
	app.Delete("/v1/projects/:slug/domains/:host", cloud.Handle(s, releaseDomain))

	// /v1/sites — the surface-agnostic deploy_site capability, shared with agents.
	// /v1/sites builds a responsive static site from a brief and deploys it;
	// /v1/sites/deploy is the raw file-manifest deploy; both funnel through the SAME
	// publishSite core as the tar path, so there is one deploy pipeline, one host
	// binding, one metering. Org scope is the IAM-minted X-Org-Id, exactly as
	// /v1/projects.
	app.Post("/v1/sites", cloud.Handle(s, buildSite))
	app.Post("/v1/sites/deploy", cloud.Handle(s, deploySiteFiles))
	app.Get("/v1/sites", cloud.Handle(s, listSites))

	// Releases — how content GETS to a site's serving prefix (release.go). The
	// builder's build output already lives in OUR object store, so publishing is a
	// server-side promote into an immutable content-addressed release plus an
	// atomic pointer flip: no bytes traverse the API and no client holds an S3
	// credential. /publish is create+activate (the 99% path); the two halves stay
	// separable for a staged rollout, and activate doubles as the free rollback.
	siteReleases(app, s, "/v1/sites")

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
	app.Post("/v1/platform/sites/:slug/domains/:host/verify", cloud.Handle(s, verifyDomain))
	app.Delete("/v1/platform/sites/:slug/domains/:host", cloud.Handle(s, releaseDomain))
	siteReleases(app, s, "/v1/platform/sites")
}

// siteReleases registers the release routes under a site-surface base path. Both
// site surfaces (/v1/sites and /v1/platform/sites) are the same engine, so they
// get the same four routes from this one registration — a release published on
// one is visible and activatable on the other because there is only one store.
func siteReleases(app cloud.Router, s *cloud.Service[state], base string) {
	app.Post(base+"/:slug/publish", cloud.Handle(s, publishSiteRelease))
	app.Post(base+"/:slug/releases", cloud.Handle(s, createRelease))
	app.Get(base+"/:slug/releases", cloud.Handle(s, listReleases))
	app.Post(base+"/:slug/releases/:release/activate", cloud.Handle(s, activateRelease))
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

	// Resolved BEFORE the row is built, so an unfunded org asking for private is
	// refused without a half-created project left behind.
	vis, err := resolve(s, c, body.Visibility)
	if err != nil {
		return err
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
			return zip.ErrConflict("project slug already exists in this org")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	// Best-effort provision the Base data space (form/forum/data submissions). Runs
	// only after a successful persist so a conflicting create provisions nothing;
	// a Base hiccup is logged and swallowed — it never fails the create.
	provisionSpace(s, c.Context(), &p)
	// Give it a canonical repo at git.hanzo.ai, world-readable exactly when the
	// project is.
	share(s, c.Context(), p)
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
	if body.Visibility != nil {
		vis, err := resolve(s, c, *body.Visibility)
		if err != nil {
			return err
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
	if err := s.State.store.UpdateProject(c.Context(), p); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("project not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	// Reconcile the repo to whatever this update settled on — including a
	// moderation, which must reach the source and not just the listing.
	share(s, c.Context(), p)
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
	// Drop the release rows so a reclaimed slug never inherits the previous
	// owner's rollback menu.
	if rErr := s.State.store.DeleteReleases(c.Context(), org, p.Slug); rErr != nil {
		s.Log.Warn("delete releases failed (continuing)", "org", org, "slug", p.Slug, "err", rErr)
	}
	// Best-effort purge of the live site; metadata is already gone, so a purge
	// failure must not resurrect the project — log and continue. BOTH spaces go:
	// the legacy mutable prefix AND the site's release space, which is a sibling
	// of it (releaseSpace) and so is not covered by the first purge.
	if s.State.blob.configured() {
		if cli, cErr := s.State.blob.client(); cErr == nil {
			for _, prefix := range []string{sitePrefix(org, p.Slug), releaseSpace(org) + "/" + p.Slug} {
				if pErr := purgePrefix(c.Context(), cli, s.State.blob.bucket, prefix); pErr != nil {
					s.Log.Warn("purge site failed (continuing)", "org", org, "slug", p.Slug, "prefix", prefix, "err", pErr)
				}
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
