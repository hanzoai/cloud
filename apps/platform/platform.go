// Package platform is Hanzo PaaS: deploy containers to your own tenant
// namespace — builds, releases, environments, logs, custom domains.
//
// It is the per-org container platform at /v1/platform — projects,
// applications, builds, deploys, environments, releases, logs and verified
// custom domains, each app reconciled into the caller's own tenant-<org>
// Kubernetes namespace.
//
// Relationship to the sibling subsystems:
//
//   - fleet.go       (/v1/platform/fleet) — the ADMIN fleet drift board: observes +
//     deploys SYSTEM Service CRs across the platform namespaces, SuperAdmin
//     only. It answers "what is the fleet running, and roll a tag."
//   - apps/projects (/v1/sites)     — per-org STATIC sites (S3 hosting).
//   - apps/platform (/v1/platform)  — THIS: per-org CONTAINER apps. Users
//     create projects + applications, build them (arcd BuildKit) and deploy them
//     (operator hanzo.ai/v1 Service CR into their OWN tenant-<org> namespace).
//
// All three share the ONE deploy mechanic — write an operator CR, let the
// operator reconcile — but /v1/platform is per-tenant: every route is scoped to
// the gateway-minted, IAM-VALIDATED X-Org-Id (c.Org()); the deploy namespace is
// DERIVED from that org (tenant-<org>), never taken from the request. A tenant
// can never read, build, or deploy into another org's namespace. That is the
// red-team bar and it is structural: cross-tenant identifiers are simply not
// inputs to any handler.
//
// The handlers below run natively on zip, so the binary keeps ONE router and
// every route stays behind the SanitizeIdentity trust boundary. That router is
// the ONE source of the published contract (openapi/ projects it). The Goa
// design module beside this package (apps/platform/design — its own go.mod,
// not built or tested here) is a SECOND source and has already drifted: it
// emits 15 operations against the 30 this package registers. Do not read it as
// the contract, and do not regenerate against it.
package platform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud/apps/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/provisioning"
	"github.com/hanzoai/cloud/internal/fqdn"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// slugRE constrains a project/app slug to a DNS/identifier-safe token. It is the
// org-unique handle AND the CR name AND part of the namespace/URL, so it is the
// injection/traversal guard at the boundary.
var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// buildTypes is the closed set of buildable strategies for a git source. "pack"
// (hanzoai/pack, a BuildKit gateway.v0 frontend) is the zero-config default that
// detects any project — Go, Node, Python, Rust, static; "dockerfile" is the
// explicit escape hatch. An image source never builds (buildType "image"), so it
// is not a member here — it is forced by source in createApp.
var buildTypes = map[string]bool{
	"pack": true, "dockerfile": true,
}

// EnvVarJSON is the JSON shape of one application env var as stored/served.
type EnvVarJSON struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

// state is platform's own data; the shared deps (log, kms, bill, brand, env,
// domain) live in the embedded cloud.Base, reached as s.Log / s.KMS / s.Bill /
// s.Brand / s.Env / s.Domain — never re-plumbed here.
type state struct {
	store       *Store
	projects    ProjectStore // IAM-backed project lifecycle (projects.go); apps live under its names
	k8s         *k8sClient
	kmsIdentity tenantKMSIdentity  // per-tenant KMS machine-identity provisioner (secrets.go); nil ⇒ sync stays fail-closed pending
	cancel      context.CancelFunc // stops the build reconciler on Shutdown
	sitesHost   string             // per-tenant apps host suffix; a custom domain must be under <org>.<sitesHost>
	appLock     appMutex           // per-app serialization of apply-CR→finalize-live (applylive.go, RED LOW-1)
	deployGate  inflightGate       // per-org in-flight synchronous-deploy cap (deploy.go, RED LOW L1)
	resolver    fqdn.Resolver      // custom-domain ownership verification (domains.go); nil ⇒ system resolver
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the /v1/platform surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("platform.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("platform.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "platform")
	if deps.DataDir == "" {
		return fmt.Errorf("platform.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("platform.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "platform.db"))
	if err != nil {
		return fmt.Errorf("platform.Mount: open store: %w", err)
	}

	// Build Jobs run in a DEDICATED, isolated namespace — NOT the main platform
	// namespace (which holds cloud's own ~hundreds of secrets). The default is
	// fail-secure: an unset CLOUD_PLATFORM_BUILD_NS lands privileged-ish builds in
	// the isolated build ns (holding only the per-org push creds + git token), never
	// alongside the platform secrets (H2). The operator provisions this namespace and
	// its scoped credentials (like every other build credential today).
	k := newK8sClient(getenv("CLOUD_PLATFORM_IMAGE_PREFIX", defaultBuildImagePrefix), getenv("CLOUD_PLATFORM_BUILD_NS", defaultBuildNamespace))
	if k.initErr != "" {
		log.Warn("kubernetes client unavailable; deploy/build will fail closed", "err", k.initErr)
	}

	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "platform"),
		State: state{store: store, k8s: k, kmsIdentity: newKMSOrgIdentity(deps.KMS, deps.IAMIssuer, deps.Brand),
			sitesHost: getenv("CLOUD_PLATFORM_SITES_HOST", "hanzo.app")}}
	// The project source is the CANONICAL IAM: external over HTTP when the
	// deployment names one (IAM_URL), the embedded store when this binary IS
	// the IAM. The HTTP client authenticates per-org with the SAME
	// <org>-platform-kms identity the KMS sync uses — one identity per tenant.
	s.State.projects = newProjectStore(deps.IAMIssuer, s.State.kmsIdentity)
	mounted = s
	// UNIFIED PAYWALL (server-side enforcement). To gate the /v1/platform surface
	// behind the caller's plan, wrap it with entitlements.RequireProduct(deps.Commerce,
	// "platform") — note routes() registers FLAT app.Get paths (not a group), so
	// enabling means converting them to app.Group("/v1/platform", mw) or wrapping each.
	// DEFERRED — DO NOT ENABLE YET: the "platform" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so enforcing now would 402 every org. Flip on
	// once the catalog licenses "platform" to a tier. See clients/entitlements.
	routes(app, s)

	// The fleet board (/v1/platform/fleet) — the platform's view of its OWN service
	// tier, folded in from what used to be the separate /v1/paas product. It carries
	// its own dynamic client because it observes the whole platform tier, not one
	// tenant namespace; the routes are siblings under the one /v1/platform prefix.
	fb := cloud.NewBase(deps, "platform")
	fleetRoutes(app, &cloud.Service[fleetState]{Base: fb, State: buildFleet(fb)})

	// The cloud's own embedded-git apex is a trusted build source (clients/git
	// serves repos at this host), so a self-hosted-git app builds with no env.
	selfGitHost = strings.ToLower(strings.TrimSpace(deps.Domain))

	// git-push-to-deploy: a push landed on the embedded git server (clients/git)
	// triggers a build for every app tracking that repo+branch. Inverted so git
	// never imports platform — build.go RegisterPushBuilder ⇄ OnGitPush (push.go).
	cloud.RegisterPushBuilder(func(ctx context.Context, ev cloud.GitPushEvent) error { return buildFromPush(mounted, ctx, ev) })

	// Own the git build→deploy handoff: a background reconciler that applies the
	// Service CR once a build Job succeeds (reconcile.go). Restart-safe — it reads
	// "building" deployments from the store, so it resumes across a cloud restart.
	// Started only when the cluster client resolved (else deploy/build fail closed
	// and there is nothing to reconcile).
	if k.dyn != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.State.cancel = cancel
		go runBuildReconciler(s, ctx)
		go runOrphanReaper(s, ctx)
		// Meter running deployments' compute onto their org's ledger every interval —
		// the last wire in the OSS compute-royalty loop (computemeter.go). Same cancel
		// context as the reconciler, so Shutdown stops both; single-writer by the same
		// topology the build meter relies on.
		go runComputeMeter(s, ctx)
	}

	log.Info("platform control plane mounted",
		"prefix", "/v1/platform", "k8s", k.dyn != nil, "brand", deps.Brand, "env", deps.Env)
	return nil
}

// routes registers the /v1/platform surface on app. Extracted from Mount so
// tests can mount the same routes over a Service with an injected (fake/nil) k8s
// client — hermetic, never touching a real cluster.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// projects
	// A project is IAM's resource, created and deleted at /v1/iam/projects. The
	// platform makes APPS under one, never the project itself, so it exposes no
	// project lifecycle — only this read, which is a PROJECTION IAM cannot serve:
	// the project plus how many platform apps live under it.
	app.Get("/v1/platform/projects", cloud.Handle(s, listProjects))
	app.Get("/v1/platform/projects/:project", cloud.Handle(s, getProject))

	// applications
	app.Get("/v1/platform/projects/:project/apps", cloud.Handle(s, listApps))
	app.Post("/v1/platform/projects/:project/apps", cloud.Handle(s, createApp))
	app.Get("/v1/platform/projects/:project/apps/:app", cloud.Handle(s, getApp))
	app.Delete("/v1/platform/projects/:project/apps/:app", cloud.Handle(s, deleteApp))

	// env management: replace an app's env set (plain + secret). Secret values are
	// sealed into KMS; plaintext is never persisted (secrets.go). One write path.
	app.Put("/v1/platform/projects/:project/apps/:app/env", cloud.Handle(s, setEnv))

	// deploy lifecycle + history (deploy.go)
	app.Post("/v1/platform/projects/:project/apps/:app/deploy", cloud.Handle(s, deploy))
	app.Post("/v1/platform/projects/:project/apps/:app/stop", cloud.Handle(s, stop))
	app.Post("/v1/platform/projects/:project/apps/:app/start", cloud.Handle(s, start))
	app.Get("/v1/platform/projects/:project/apps/:app/deployments", cloud.Handle(s, listDeployments))
	app.Get("/v1/platform/projects/:project/apps/:app/deployments/:id", cloud.Handle(s, getDeployment))
	app.Get("/v1/platform/projects/:project/apps/:app/deployments/:id/logs", cloud.Handle(s, deploymentLogs))

	// Vercel-style release flows (preview.go), all reusing the ONE deploy mechanic
	// (deployTagCore → applyLive; write the Service CR, the operator reconciles):
	// a per-branch preview target with its OWN slug + host, promote an already-built
	// tag/deployment to prod, and rollback to a prior image. Org-scoped like the rest.
	app.Post("/v1/platform/projects/:project/apps/:app/preview", cloud.Handle(s, preview))
	app.Post("/v1/platform/projects/:project/apps/:app/promote", cloud.Handle(s, promote))
	app.Post("/v1/platform/projects/:project/apps/:app/rollback", cloud.Handle(s, rollback))

	// custom domains + org-subtree hosts (domains.go): list, add (subtree active /
	// custom pending-with-challenge), verify a custom claim's DNS, remove.
	app.Get("/v1/platform/projects/:project/apps/:app/domains", cloud.Handle(s, listDomains))
	app.Post("/v1/platform/projects/:project/apps/:app/domains", cloud.Handle(s, addDomain))
	app.Post("/v1/platform/projects/:project/apps/:app/domains/:host/verify", cloud.Handle(s, verifyDomain))
	app.Delete("/v1/platform/projects/:project/apps/:app/domains/:host", cloud.Handle(s, removeDomain))

	app.Get("/v1/platform/health", cloud.Handle(s, health))

	// Container-serverless one-shot: POST /v1/run — create-or-update an image app
	// (in the org's default project) and deploy it via the SAME Service-CR writer,
	// returning its live URL. A top-level convenience over the project→app→deploy
	// flow above; org-scoped by s.tenant, never by the body (run.go). Bound at order
	// 124, before the AI /v1/* catch-all (150).
	app.Post("/v1/run", cloud.Handle(s, run))

	// console aggregates (Environments / Pipelines / Builds / Releases) — flat,
	// top-level REST DERIVED from the SAME project/app/deploy/build data above
	// (console.go). GET-only projections: the ONE write path stays POST .../apps
	// and .../deploy. Registered here (order 124) so they bind before the /v1/*
	// AI catch-all; every handler is org-scoped through s.tenant like the rest.
	app.Get("/v1/environments", cloud.Handle(s, listEnvironments))
	app.Get("/v1/pipelines", cloud.Handle(s, listPipelines))
	app.Get("/v1/builds", cloud.Handle(s, listBuilds))
	app.Get("/v1/releases", cloud.Handle(s, listReleases))

	// Native build API (the no-GitHub-builders trigger, ex-/v1/arcd). Privileged:
	// token-gated + image-ref allowlisted (runner.go). `hanzo build`, the
	// git-push-to-deploy hook, and cloud's own self-release all POST here.
	app.Post("/v1/runner", cloud.Handle(s, runnerBuild))
	// A release answers 202 with an id, so the id has to be answerable. Without
	// these a release that dies in the detached pipeline is indistinguishable from
	// one still running — which is exactly how a release that launched nothing
	// looked like one in flight.
	app.Get("/v1/runner/releases", cloud.Handle(s, listSelfReleases))
	app.Get("/v1/runner/releases/:id", cloud.Handle(s, getSelfRelease))
}

// The platform surface's declared bodies AND its prose, in the route table's own
// order so path, payload and meaning are read (and changed) together. Register
// names the exact struct the matching handler binds or serves — openapi reflects
// the schema from it, so the published contract follows the code. Describe states
// what a CALLER gets, which no reflection can derive: none of these routes is a
// typed op, so there is no doc comment for zipdoc to lift, and a bare operation
// publishes an operationId and NOTHING else — an SDK method that cannot explain
// itself and a CLI command with no help text. Both halves render only while the
// route is live, so this list can never add a path.
func init() {
	openapi.Register("/v1/platform/projects", "GET", nil, []projectView{})
	openapi.Describe("/v1/platform/projects", "GET",
		"Your org's projects, each with how many apps live under it",
		"Lists the caller org's projects with the number of platform applications in each. A "+
			"project is IAM's resource — it is created and deleted at /v1/iam/projects, never here "+
			"— so this is the ONE projection IAM cannot serve: the project plus what the platform "+
			"has put under it.\n\n"+
			"Requires a validated principal; 403 without one, and the org comes from that validated "+
			"identity rather than a request header. This is the console's first authenticated read, "+
			"so a project store that is not yet initialised degrades to an EMPTY list rather than a "+
			"500 — a new org genuinely has zero projects — and the real cause is surfaced to "+
			"operators instead of to the caller.")

	openapi.Register("/v1/platform/projects/:project", "GET", nil, projectView{})
	openapi.Describe("/v1/platform/projects/:project", "GET",
		"One project and its app count",
		"Returns a single project of the caller's org with the number of platform applications "+
			"under it. A project this org does not have is 404, which is also what another tenant's "+
			"project looks like from here. Requires a validated principal; 403 without one.")

	openapi.Register("/v1/platform/projects/:project/apps", "GET", nil, []appView{})
	openapi.Describe("/v1/platform/projects/:project/apps", "GET",
		"The applications in one project, with what the cluster says about them",
		"Lists the caller org's applications under one project. Each row carries the stored record "+
			"and, for an app that is live or deploying, the LIVE phase and health read from its "+
			"operator Service CR; an app with sealed env also carries its secret-sync state. Those "+
			"cluster reads are best-effort — an unreachable cluster leaves those fields empty and "+
			"never blocks the listing.\n\n"+
			"The project must exist in IAM for this org, or the answer is 404; the `default` "+
			"project is implicit and always accepted, because it is part of what an org IS. "+
			"Requires a validated principal; 403 without one.")

	openapi.Register("/v1/platform/projects/:project/apps", "POST", createAppReq{}, appView{})
	openapi.Describe("/v1/platform/projects/:project/apps", "POST",
		"Create an application from a git repo or a container image",
		"Registers a new application under one of the caller org's projects and answers 201 with "+
			"it. Creating does NOT deploy: the app lands in `draft` and nothing reaches the cluster "+
			"until /deploy.\n\n"+
			"`source` is `git` — which requires `repo.url` — or `image`, which requires "+
			"`image.repository`; anything else is 400. A git app builds with zero-config `pack` by "+
			"default and may opt into `dockerfile`; an image app never builds. The repo URL and "+
			"Dockerfile path are validated here against the SAME allowlist the privileged build "+
			"enforces, so an unsafe source is refused before it is ever persisted.\n\n"+
			"The `slug` is the app's identity in the cluster: given or derived from `name`, it must "+
			"match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`, and a slug already used in this project "+
			"is 409. `replicas` and `storageGb` are clamped to the deployment's limits rather than "+
			"refused.\n\n"+
			"Env keys must match `^[A-Za-z_][A-Za-z0-9_]*$`. A variable marked `secret: true` is "+
			"SEALED into KMS and its plaintext is never written to the database — and if KMS is "+
			"unavailable the create fails 503 rather than falling back to storing a secret in the "+
			"clear.\n\n"+
			"The app is seeded with its canonical default host, so it has a working HTTPS URL the "+
			"moment it deploys. A bare custom domain cannot be attached here — it has to go through "+
			"add-domain and DNS verification first. Requires a validated principal; 403 without "+
			"one, and every cluster object it will later create lands in that org's own "+
			"`tenant-<org>` namespace.")

	openapi.Register("/v1/platform/projects/:project/apps/:app", "GET", nil, appView{})
	openapi.Describe("/v1/platform/projects/:project/apps/:app", "GET",
		"One application, with its live phase, health and secret sync",
		"Returns a single application of the caller's org together with what the cluster currently "+
			"reports for it: the operator Service CR's phase and health, and whether its sealed env "+
			"has synced. An app this org and project do not have is 404. Requires a validated "+
			"principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app", "DELETE",
		"Delete an application and tear down what it runs",
		"Removes the application record and tears down what it owns in the org's tenant namespace "+
			"— its operator Service CR and its KMSSecret — then answers 204. An app this org and "+
			"project do not have is 404, never a silent success.\n\n"+
			"Teardown is best-effort by design: a cluster that refuses or is unreachable does not "+
			"block the delete, so the record cannot be left orphaned behind a broken cluster; the "+
			"failure is logged for operators and the orphan reaper reconciles it. Requires a "+
			"validated principal; 403 without one.")

	openapi.Register("/v1/platform/projects/:project/apps/:app/env", "PUT", setEnvReq{}, appView{})
	openapi.Describe("/v1/platform/projects/:project/apps/:app/env", "PUT",
		"Replace an app's environment variables",
		"Writes the app's whole environment set and answers the updated application. This is the "+
			"one post-create write path for env, and it REPLACES rather than merges: a variable "+
			"absent from the body is gone, and a secret dropped from the set leaves the app's "+
			"Secret on its next deploy.\n\n"+
			"Keys must match `^[A-Za-z_][A-Za-z0-9_]*$`. A value marked `secret: true` is sealed "+
			"into KMS and blanked in the database, so plaintext is never persisted — and the write "+
			"fails 503 if KMS is unavailable rather than storing one in the clear.\n\n"+
			"The rule worth knowing: this does not restart anything. Once the app has been deployed "+
			"the secret sync is re-declared immediately so the operator re-materialises the Secret, "+
			"but RUNNING pods keep the environment they started with until their next deploy or "+
			"restart. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/deploy", "POST",
		"Deploy the app — build it first if it comes from git",
		"Starts a new, monotonically versioned deployment of the app and answers 202 with the "+
			"deployment record. A 202 is an ACCEPTED deployment, not a live one.\n\n"+
			"An IMAGE app deploys the tag you name (falling back to the app's tag, then `latest`) "+
			"by writing its operator Service CR; the operator reconciles it to running. A GIT app "+
			"launches an in-cluster BuildKit Job at `commit` — or the app's branch — and comes back "+
			"in `building`; the Service CR is applied later, by the reconciler, once the Job "+
			"succeeds. The reconciler is restart-safe, so a build in flight survives a cloud "+
			"restart.\n\n"+
			"Deploys are bounded per org: over the concurrent-deploy cap is 429 and NOTHING is "+
			"recorded, so a rejected deploy leaves no phantom in the history. An unreachable "+
			"cluster is 503 but still records an honest `error` deployment, because a deploy that "+
			"was attempted and failed must not be indistinguishable from one never made. Every "+
			"other failure is likewise recorded in its real terminal state.\n\n"+
			"This is metered work: a git build is billed to the org's ledger in wall-clock build "+
			"minutes once the Job finishes, and the running deployment is billed for its compute "+
			"per tick for as long as it stays live. Requires a validated principal; 403 without "+
			"one, and everything is written into that org's own `tenant-<org>` namespace.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/stop", "POST",
		"Stop an app without deleting it",
		"Scales the app's Service to zero replicas and marks it stopped, answering the updated "+
			"application. Nothing else is removed — the record, its env, its domains and its "+
			"deployment history all survive, and /start brings it back at the same replica count."+
			"\n\n"+
			"An app that is not deployed has no Service CR to scale and is 404. An unreachable "+
			"cluster is 503 and a cluster that refuses the scale is 502. Because the pods stop, so "+
			"does the compute metering. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/start", "POST",
		"Start a stopped app back up",
		"Scales the app's Service back to its configured replica count and marks it live, "+
			"answering the updated application. It does not redeploy: the image already on the "+
			"Service CR is what comes back.\n\n"+
			"The billing watermark is reset to now as part of starting, so the org is charged for "+
			"THIS live span and never for the gap the app spent stopped. An app with no Service CR "+
			"is 404, an unreachable cluster is 503, and a cluster that refuses the scale is 502. "+
			"Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/deployments", "GET",
		"An app's deployment history",
		"Lists every deployment recorded for one of the caller org's applications, newest version "+
			"first, each with its version, status, source, commit and image. Failed and superseded "+
			"attempts are included — that is the point of a history. Requires a validated "+
			"principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/deployments/:id", "GET",
		"One deployment of one app",
		"Returns a single deployment by id, scoped to the named application of the caller's org — "+
			"so an id belonging to another app or another tenant is 404, not a read. Requires a "+
			"validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/deployments/:id/logs", "GET",
		"Real logs for a deployment — the build's, then the app's",
		"Returns the deployment's recorded status timeline together with LIVE pod logs pulled from "+
			"the cluster: the build pod's output while a git build is running, and the running "+
			"app's output once it is deployed. The `source` field says which of the two the body "+
			"is — `build`, `app` or `none` — so a console can label the pane honestly.\n\n"+
			"It never fabricates log content. When no pod exists yet, or the cluster is "+
			"unreachable, it degrades to the recorded timeline and says so. Every cluster read is "+
			"confined to the caller org's own namespaces and time-boxed. Requires a validated "+
			"principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/preview", "POST",
		"Put a branch on its own URL",
		"Deploys an already-built `image` to a per-branch preview and answers its URL, the branch, "+
			"the preview's slug and the deployment. The preview is a FIRST-CLASS application named "+
			"`<app>-<branch>` in the same project and tenant namespace, with its own default host — "+
			"so it is completely isolated from production while reusing the same deploy mechanic. "+
			"Re-previewing a branch converges that same target in place rather than stacking "+
			"another one.\n\n"+
			"It carries NO environment variables, deliberately: a preview never inherits "+
			"production's secrets. It also does not build — `image` is required and must already "+
			"exist, and `branch` defaults to the parent app's. A branch that does not resolve to a "+
			"valid slug distinct from the parent's is 400. Requires a validated principal; 403 "+
			"without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/promote", "POST",
		"Promote an already-built release to the app",
		"Redeploys an image that already exists — named either by `deploymentId`, which promotes "+
			"that deployment's exact built image, or by `tag`, resolved the same way a deploy "+
			"resolves one. One of the two is required; neither is 400.\n\n"+
			"Promotion never builds. A deployment that carries no built image cannot be promoted "+
			"and is 400, and a deployment id outside this app is 404. It runs through the same "+
			"deploy core as everything else, so it takes a NEW version number and is subject to the "+
			"same per-org concurrency cap. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/rollback", "POST",
		"Go back to the previous release",
		"Redeploys a prior image: the one named by `deploymentId`, or — with no body — the newest "+
			"earlier deployment that carries a real built image and did not error, skipping the "+
			"release currently live. An app with nothing earlier to return to is 400.\n\n"+
			"A rollback is a deploy of an old image, not a rewind: it takes a NEW version number "+
			"and appends to the history rather than erasing what came after. Both lookups are "+
			"scoped to this app and org, so another tenant's image can never be rolled in. "+
			"Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/domains", "GET",
		"Every hostname this app answers on",
		"Lists the app's hosts: the permanent default host it was born with, any org-subtree hosts "+
			"attached to it, and every custom host claimed for it with its verification state and, "+
			"while pending, the DNS challenge records to publish. Live endpoint status for each "+
			"host is observed from the cluster. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/domains", "POST",
		"Attach a hostname — instantly if you already own it, otherwise with a DNS challenge",
		"Attaches `host` to the app, and which of two things happens depends on who owns the name. "+
			"A host inside the caller org's own subtree is structurally owned, so it goes ACTIVE "+
			"immediately and answers 201. A bring-your-own host is claimed as PENDING and answers "+
			"the DNS challenge records to publish; it is NOT rendered into the app's ingress until "+
			"/verify passes.\n\n"+
			"Claims are globally unique. A host already claimed by another organization is 409, and "+
			"so is one claimed by a different app in your own; re-adding this app's OWN claim is "+
			"idempotent and answers its current state at 200. The default host is always attached "+
			"and re-adding it is 409. A host under the platform's shared apex that is not the "+
			"caller's own subtree is 403 — it belongs to whoever owns that subtree and can never be "+
			"grabbed through the custom path.\n\n"+
			"`host` must be a valid DNS hostname; anything else is 400. Requires a validated "+
			"principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/domains/:host/verify", "POST",
		"Check a custom domain's DNS and turn it on if it passes",
		"Runs the DNS challenge check for a pending custom host and, when it passes, marks the "+
			"host verified and renders it into the app's ingress so it starts serving.\n\n"+
			"A check that RAN and did not pass is not an error: it answers 200 with the host still "+
			"pending and the reason in `detail`, so a console can show the operator what DNS is "+
			"actually returning. An already-verified host answers as-is without re-checking. A host "+
			"not claimed by this app is 404. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/platform/projects/:project/apps/:app/domains/:host", "DELETE",
		"Detach a hostname and release the claim",
		"Drops the host from the app's ingress and releases any custom claim on it, so the name "+
			"becomes claimable again — by this org or any other. Answers 204.\n\n"+
			"The default host is permanent and cannot be removed: that is 400, not 404. A host that "+
			"is neither attached nor claimed here is 404. Requires a validated principal; 403 "+
			"without one.")

	openapi.Describe("/v1/platform/health", "GET",
		"Whether this control plane can actually deploy anything",
		"A real probe, not a status page. It answers 200 only when the metadata store is open AND "+
			"the cluster is genuinely reachable — proved by LISTING the operator App CRD, which "+
			"settles reachability and CRD presence in one bounded call, and which is the exact "+
			"question every deploy depends on. Anything else is 503 carrying the real reason and "+
			"whether the CRD was found.\n\n"+
			"A constructed cluster client proves nothing — it is built from a kubeconfig, not from "+
			"a reachable apiserver — so this deliberately spends a round trip rather than reporting "+
			"`ok` while every deploy fails. Not admin-gated: liveness has to be probe-able without "+
			"a credential.")

	openapi.Register("/v1/run", "POST", runReq{}, runView{})
	openapi.Describe("/v1/run", "POST",
		"Run a container image and get back a URL",
		"The one-call shortcut over project → app → deploy: give it a `name` and an `image` and it "+
			"creates or updates an image-source application in your org's DEFAULT project, deploys "+
			"it through the same operator Service-CR writer everything else uses, and answers its "+
			"id, name, live URL, status and shape. Re-running the same name UPDATES it in place, so "+
			"the call is idempotent by name.\n\n"+
			"What it produces is a first-class application, not a special object: it is listable, "+
			"stoppable and redeployable through the /v1/platform routes like any other app.\n\n"+
			"`minScale` is the replica floor. `maxScale` above it declares an autoscaling ceiling; "+
			"`maxScale: 0` means no autoscaler at all — a fixed run at the floor. Both are clamped "+
			"to the deployment's limits. `runtime` and `shape` are accepted for the client contract "+
			"and echoed back: the image is the runtime unit and sizing is the operator's default.\n\n"+
			"It is BILLING-GATED before it touches the cluster: a flat per-run fee is authorized "+
			"against the org's own prepaid balance first, so an org that cannot pay is refused "+
			"without anything being created. An unreachable cluster is 503 — a run never reports a "+
			"URL it did not create. Secret env is sealed into KMS and fails closed without it.\n\n"+
			"Requires a validated principal; 403 without one. The org is resolved from that "+
			"validated identity and is what both pays and owns the namespace — it is never read "+
			"from the body.")

	openapi.Describe("/v1/environments", "GET",
		"Your deploy targets, and what is running on each",
		"Returns the org's environments — the distinct deploy targets its applications name, "+
			"`production` for anything that names none — each aggregating the apps that target it, "+
			"a rolled-up status and when it last changed.\n\n"+
			"An environment is DERIVED, not stored: there is nothing to create or delete here, and "+
			"an environment exists exactly as long as an app points at it. Requires a validated "+
			"principal; 403 without one.")

	openapi.Describe("/v1/pipelines", "GET",
		"One build-and-deploy pipeline per app, with its latest run",
		"Returns one pipeline per application in the caller's org — its repo or image source, its "+
			"current status, and when its most recent deployment ran and how long it took. A "+
			"pipeline is a PROJECTION of an app plus its newest deployment, not a separate record: "+
			"it comes into existence with the app and is triggered only through /deploy, never "+
			"here. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/builds", "GET",
		"Real build records for your org",
		"Lists the org's BuildKit build records — the git build step behind a deploy — each with "+
			"the repo it built, the short commit, its status, when it started and how long it took. "+
			"These are real records or an honest empty list; a build appears here because one ran, "+
			"never because a page needed a row. Builds are created only by /deploy and the "+
			"push-to-deploy hook. Requires a validated principal; 403 without one.")

	openapi.Describe("/v1/releases", "GET",
		"The versions that actually reached the cluster",
		"Lists the org's releases: the deployments that were genuinely applied to the cluster, with "+
			"the app they belong to, their version, environment, status and when they were "+
			"released. A deployment that failed or is still building is NOT a release and is "+
			"excluded — reaching the cluster is what makes one. Requires a validated principal; 403 "+
			"without one.")

	openapi.Describe("/v1/runner", "POST",
		"Trigger a native build — an image, or the binaries a repo declares",
		"The fabric's own build trigger, and what `hanzo build`, git-push-to-deploy and cloud's own "+
			"self-release all call. It answers 202 with the build job id: a queued build, not a "+
			"pushed artifact.\n\n"+
			"Two lanes, and a build is exactly one of them. The IMAGE lane takes `repo` and the "+
			"output `image` and launches a BuildKit Job that pushes it. The ARTIFACT lane takes "+
			"`binaries` — the same recipe the repo's hanzo.yml declares — and publishes to object "+
			"storage instead; it must carry no `image`, because a build produces binaries or an "+
			"image, never both. `release: true` is the third mode: cloud self-publishing its own "+
			"image, version computed, built, smoke-tested, tagged and announced.\n\n"+
			"PRIVILEGED, with exactly two credentials and never a third: the shared build-callback "+
			"token compared in constant time — the machine path, which a user never holds — or a "+
			"validated IAM principal who is an ADMIN of their org, which is the `hanzo build` user "+
			"path and means one IAM login authorizes a build with no separate build token. A plain "+
			"member is refused.\n\n"+
			"Both paths are bounded the same way: the output must push to a registry the fabric "+
			"owns, and on the IAM path the image's registry namespace must MATCH the caller's own "+
			"validated org — so an org admin can only publish into their own brand and can never "+
			"overwrite another's through the shared push credential. The same confinement applies "+
			"to the artifact lane's repo owner. Cutting a release is IAM's decision alone: the "+
			"build token may enqueue a build but may not cut one.\n\n"+
			"The output image is parsed and validated as a single well-formed OCI ref before any "+
			"authorization decision reads it, so a crafted ref cannot smuggle a build-exporter "+
			"attribute past the check.")

	openapi.Describe("/v1/runner/releases", "GET",
		"Self-publish releases this process has run",
		"Lists the platform's own release runs with their current state, so a release that answered "+
			"202 with an id can be followed to its end. SuperAdmin only — this is the platform's "+
			"own publishing record, not a tenant surface.\n\n"+
			"The record lives in THIS process's memory, so it covers the releases this instance "+
			"started and does not survive a restart.")

	openapi.Describe("/v1/runner/releases/:id", "GET",
		"One self-publish release by the id its 202 returned",
		"Returns the state of one release run — which is the whole reason the trigger answers with "+
			"an id, because without this a release that died in the detached pipeline would look "+
			"exactly like one still in flight. SuperAdmin only.\n\n"+
			"A 404 means the id is unknown OR has aged out of this process's in-memory record. That "+
			"is the honest answer either way: the process genuinely cannot tell the two apart.")
}

// ── tenancy ──────────────────────────────────────────────────────────────────

// tenant resolves the org for a request from the VALIDATED identity.
//
// REQUIRES A VALIDATED PRINCIPAL (RED HIGH), mirroring clients/s3.tenant.
// SanitizeIdentity sets X-User-Id ONLY when it validated a bearer/cookie; on the
// no-principal "Phase-1 data" residual path it RESTORES the client's raw
// X-Org-Id but leaves X-User-Id empty (middleware_identity.go). /v1/platform is
// a control plane that MUTATES cluster state (creates operator Service CRs +
// BuildKit Jobs in tenant-<org>) — strictly more consequential than a data read
// — so trusting X-Org-Id alone would let a direct-to-pod caller forge
// `X-Org-Id: victim` with NO bearer and deploy/read into another tenant. We gate
// on c.User() being present: every legitimate caller reaches this through the
// gateway or the console BFF, which mint a user-bound bearer (→ X-User-Id set),
// so this refuses ONLY the anonymous-forge path and breaks no real client.
//
// Empty org is allowed only for a validated SuperAdmin (bucketed under
// "admin"): a forged X-User-IsAdmin cannot exist without a validated principal
// (SanitizeIdentity sets it only for a JWT-verified SuperAdmin, HIP-0026), and
// even then reaches only the admin bucket, never a real tenant's namespace. This
// is the ONLY source of the tenant; no handler reads an org from body or path.
func tenant(s *cloud.Service[state], c *zip.Ctx) (string, bool) {
	if !principal.Validated(c) {
		return "", false // no validated principal — refuse the forgeable Phase-1 data path
	}
	// ONE org normalizer, cloud-wide: the injective provisioning.SanitizeOrg, so a
	// tenant resolved here keys the SAME namespace/image boundary as everywhere
	// else and two distinct owners never collapse onto one tenant (CRIT-2).
	if org := provisioning.SanitizeOrg(c.Org()); org != "" {
		return org, true
	}
	if c.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// ── HTTP views (the published contract; mirrors the Goa design result types) ──

type gitSource struct {
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type imageView struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
}

type appView struct {
	ID                  string       `json:"id"`
	Org                 string       `json:"org"`
	ProjectID           string       `json:"projectId"`
	Slug                string       `json:"slug"`
	Name                string       `json:"name"`
	Description         string       `json:"description,omitempty"`
	Environment         string       `json:"environment"`
	Source              string       `json:"source"`
	Repo                gitSource    `json:"repo"`
	Image               imageView    `json:"image"`
	BuildType           string       `json:"buildType,omitempty"`
	Dockerfile          string       `json:"dockerfile,omitempty"`
	Env                 []EnvVarJSON `json:"env"`
	Port                int          `json:"port"`
	Replicas            int          `json:"replicas"`
	StorageGB           int          `json:"storageGb,omitempty"` // GiB; absent means stateless
	Domains             []string     `json:"domains"`
	Status              string       `json:"status"`
	Namespace           string       `json:"namespace,omitempty"`
	CurrentDeploymentID string       `json:"currentDeploymentId,omitempty"`
	Phase               string       `json:"phase,omitempty"`
	Health              string       `json:"health,omitempty"`
	SecretSync          string       `json:"secretSync,omitempty"`       // ""|pending|syncing|ready|failed (secrets.go)
	SecretSyncDetail    string       `json:"secretSyncDetail,omitempty"` // honest reason when not ready
	CreatedAt           int64        `json:"createdAt"`
	UpdatedAt           int64        `json:"updatedAt"`
}

func toAppView(a Application) appView {
	env := []EnvVarJSON{}
	if a.EnvJSON != "" {
		_ = json.Unmarshal([]byte(a.EnvJSON), &env)
	}
	// Never echo secret values back over the API — mask them.
	for i := range env {
		if env[i].Secret {
			env[i].Value = ""
		}
	}
	domains := []string{}
	if a.DomainsJSON != "" {
		_ = json.Unmarshal([]byte(a.DomainsJSON), &domains)
	}
	return appView{
		ID: a.ID, Org: a.Org, ProjectID: a.ProjectID, Slug: a.Slug, Name: a.Name,
		Description: a.Description, Environment: a.Environment, Source: a.Source,
		Repo:      gitSource{URL: a.RepoURL, Branch: a.RepoBranch, Provider: a.RepoProvider},
		Image:     imageView{Repository: a.ImageRepo, Tag: a.ImageTag},
		BuildType: a.BuildType, Dockerfile: a.Dockerfile, Env: env, Port: a.Port,
		Replicas: a.Replicas, StorageGB: a.StorageGB, Domains: domains, Status: a.Status, Namespace: a.Namespace,
		CurrentDeploymentID: a.CurrentDeploy, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

type deploymentView struct {
	ID            string `json:"id"`
	Org           string `json:"org"`
	ApplicationID string `json:"applicationId"`
	Version       int    `json:"version"`
	Status        string `json:"status"`
	Source        string `json:"source"`
	Commit        string `json:"commit,omitempty"`
	Image         string `json:"image,omitempty"`
	BuildID       string `json:"buildId,omitempty"`
	Message       string `json:"message,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

func toDeploymentView(d Deployment) deploymentView {
	return deploymentView{
		ID: d.ID, Org: d.Org, ApplicationID: d.ApplicationID, Version: d.Version,
		Status: d.Status, Source: d.Source, Commit: d.Commit, Image: d.Image,
		BuildID: d.BuildID, Message: d.Message, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// ── project handlers ─────────────────────────────────────────────────────────

func listProjects(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.State.projects.List(c.Context(), org)
	if err != nil {
		// This list is the console dashboard's first authenticated read. A
		// co-resident IAM store that is not yet initialized (the iamStore guard's
		// typed 503) — or any transient store failure — must degrade to an empty
		// project set, never a 500 that breaks dashboard init: a new org genuinely
		// has zero projects. The real cause is surfaced to operators, not swallowed;
		// written in-band (nil returned) so no outer error filter can reflatten it.
		s.Log.Warn("platform: project store unavailable; serving empty project list", "org", org, "err", err)
		return c.JSON(http.StatusOK, []projectView{})
	}
	out := make([]projectView, 0, len(rows))
	for _, p := range rows {
		if p == nil {
			continue // never nil-deref a stray nil row into a 500
		}
		apps, _ := s.State.store.ListApplications(c.Context(), org, p.Name)
		out = append(out, toProjectView(p, len(apps)))
	}
	return c.JSON(http.StatusOK, out)
}

func getProject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.State.projects.Get(c.Context(), org, projectParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if p == nil {
		return zip.ErrNotFound("project not found")
	}
	apps, _ := s.State.store.ListApplications(c.Context(), org, p.Name)
	return c.JSON(http.StatusOK, toProjectView(p, len(apps)))
}

// ── application handlers ─────────────────────────────────────────────────────

type createAppReq struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Environment string `json:"environment"`
	Source      string `json:"source"` // git | image
	Repo        struct {
		URL    string `json:"url"`
		Branch string `json:"branch"`
	} `json:"repo"`
	Image struct {
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
	} `json:"image"`
	BuildType  string       `json:"buildType"`
	Dockerfile string       `json:"dockerfile"`
	Port       int          `json:"port"`
	Replicas   int          `json:"replicas"`
	StorageGB  int          `json:"storageGb"`
	Env        []EnvVarJSON `json:"env"`
	Domains    []string     `json:"domains"`
}

// requireProject confirms the request's :project exists in IAM for org, returning
// its name (the app-scope key) or a mapped 404/500. It is the ONE place the app
// routes verify project existence before touching platform's app tree.
func requireProject(s *cloud.Service[state], c *zip.Ctx, org string) (string, error) {
	project := projectParam(c)
	// The DEFAULT project is implicit — part of what an org IS. Its row is owed
	// by IAM provisioning, and no surface (run, apps under it) fails an org for
	// a row IAM owes it. Every other project must exist in IAM.
	if project == principal.DefaultProject {
		return project, nil
	}
	ok, err := s.State.projects.Exists(c.Context(), org, project)
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	if !ok {
		return "", zip.ErrNotFound("project not found")
	}
	return project, nil
}

func createApp(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	project, herr := requireProject(s, c, org)
	if herr != nil {
		return herr
	}
	var body createAppReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	slug := normalizeSlug(body.Slug, name)
	if !slugRE.MatchString(slug) {
		return zip.ErrBadRequest("slug must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	source := strings.ToLower(strings.TrimSpace(body.Source))
	switch source {
	case "git":
		if strings.TrimSpace(body.Repo.URL) == "" {
			return zip.ErrBadRequest("source 'git' requires repo.url")
		}
		// (CRIT-1) Reject an unsafe repo.url / dockerfile at the boundary (400)
		// before it is ever persisted or reaches the privileged build. This is the
		// SAME validator the build path enforces (validate.go) — refusing early is
		// defense in depth + a clear client error, not a new rule.
		if _, err := validateRepoURL(body.Repo.URL); err != nil {
			return zip.ErrBadRequest(err.Error())
		}
		if strings.TrimSpace(body.Dockerfile) != "" {
			if _, err := validateDockerfile(body.Dockerfile); err != nil {
				return zip.ErrBadRequest(err.Error())
			}
		}
	case "image":
		if strings.TrimSpace(body.Image.Repository) == "" {
			return zip.ErrBadRequest("source 'image' requires image.repository")
		}
	default:
		return zip.ErrBadRequest("source must be 'git' or 'image'")
	}
	// buildType is a function of source: an image app never builds ("image"); a
	// git app defaults to zero-config pack and may opt into the dockerfile escape
	// hatch. The build path (buildFrontendCmd) keys off dockerfile presence, so
	// buildType is honest metadata, not a second switch.
	buildType := "image"
	if source == "git" {
		buildType = strings.ToLower(strings.TrimSpace(body.BuildType))
		if buildType == "" {
			buildType = "pack"
		}
		if !buildTypes[buildType] {
			return zip.ErrBadRequest("buildType must be 'pack' or 'dockerfile'")
		}
	}
	// Validate env keys at the boundary, then SEAL secret:true values into KMS so
	// plaintext is NEVER persisted (sealSecretEnv blanks the stored value; the real
	// value lives only in the embedded KMS). Fails closed if KMS is unavailable —
	// a plaintext secret never lands in the DB as a fallback.
	for _, e := range body.Env {
		if !envKeyRE.MatchString(e.Key) {
			return zip.ErrBadRequest("env key must match ^[A-Za-z_][A-Za-z0-9_]*$")
		}
	}
	sealedEnv, err := sealSecretEnv(s, c.Context(), org, slug, body.Env)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	envJSON, _ := json.Marshal(sealedEnv)
	// Seed the canonical default host so every app has a working HTTPS URL the
	// moment it deploys, then validate the full ingress set (subtree hosts + the
	// default always pass; a bare custom host at create still 501s — it must go
	// through add-domain → verify first).
	domains := seedDefaultDomain(s, org, slug, sanitizeDomains(body.Domains))
	if err := validateOrgDomains(s, c.Context(), org, domains); err != nil {
		return err
	}
	domainsJSON, _ := json.Marshal(domains)

	now := time.Now().Unix()
	id, err := genID("app")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	a := Application{
		ID: id, Org: org, ProjectID: project, Slug: slug, Name: name, Description: strings.TrimSpace(body.Description),
		Environment: firstNonEmpty(strings.TrimSpace(body.Environment), "production"), Source: source,
		RepoURL: strings.TrimSpace(body.Repo.URL), RepoBranch: firstNonEmpty(strings.TrimSpace(body.Repo.Branch), branchDefault(body.Repo.URL)),
		RepoProvider: providerFromURL(body.Repo.URL), ImageRepo: strings.TrimSpace(body.Image.Repository), ImageTag: strings.TrimSpace(body.Image.Tag),
		BuildType: buildType, Dockerfile: strings.TrimSpace(body.Dockerfile), Port: portOr(body.Port), Replicas: s.State.k8s.limits.clampReplicas(body.Replicas),
		StorageGB: s.State.k8s.limits.clampStorage(body.StorageGB),
		EnvJSON:   string(envJSON), DomainsJSON: string(domainsJSON), Status: "draft", Namespace: tenantNamespace(org),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateApplication(c.Context(), a); err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("application slug already exists in this project")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toAppView(a))
}

func listApps(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	project, herr := requireProject(s, c, org)
	if herr != nil {
		return herr
	}
	rows, err := s.State.store.ListApplications(c.Context(), org, project)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	out := make([]appView, 0, len(rows))
	for _, a := range rows {
		v := toAppView(a)
		// Attach live phase/health from the operator CR when the cluster is
		// reachable (best-effort; never blocks the list).
		if a.Status == "live" || a.Status == "deploying" {
			v.Phase, v.Health = s.State.k8s.observeService(c.Context(), org, a.Slug)
		}
		if len(secretEnvKeys(a.EnvJSON)) > 0 {
			v.SecretSync, v.SecretSyncDetail = s.State.k8s.observeSecretSync(c.Context(), org, a.Slug, true)
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, out)
}

// loadApp resolves (projectName, app) for the caller's org, re-verifying tenancy
// at each hop: the project must exist in IAM, then the app under it. Returns the
// project name (the app-scope key / operator part-of label) and application, or a
// mapped HTTP error.
func loadApp(s *cloud.Service[state], c *zip.Ctx, org string) (string, Application, error) {
	project, herr := requireProject(s, c, org)
	if herr != nil {
		return "", Application{}, herr
	}
	a, err := s.State.store.GetApplication(c.Context(), org, project, appParam(c))
	if errors.Is(err, errNotFound) {
		return "", Application{}, zip.ErrNotFound("application not found")
	}
	if err != nil {
		return "", Application{}, zip.Errorf(http.StatusInternalServerError, "get app: %v", err)
	}
	return project, a, nil
}

func getApp(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, err := loadApp(s, c, org)
	if err != nil {
		return err
	}
	v := toAppView(a)
	v.Phase, v.Health = s.State.k8s.observeService(c.Context(), org, a.Slug)
	v.SecretSync, v.SecretSyncDetail = s.State.k8s.observeSecretSync(c.Context(), org, a.Slug, len(secretEnvKeys(a.EnvJSON)) > 0)
	return c.JSON(http.StatusOK, v)
}

func deleteApp(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	project, herr := requireProject(s, c, org)
	if herr != nil {
		return herr
	}
	a, deleted, err := s.State.store.DeleteApplication(c.Context(), org, project, appParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("application not found")
	}
	if err := s.State.k8s.deleteService(c.Context(), org, a.Slug); err != nil {
		s.Log.Warn("teardown service CR failed (continuing)", "org", org, "app", a.Slug, "err", err)
	}
	if err := s.State.k8s.deleteKMSSecret(c.Context(), org, a.Slug); err != nil {
		s.Log.Warn("teardown KMSSecret failed (continuing)", "org", org, "app", a.Slug, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ── env management ─────────────────────────────────────────────────────────────

type setEnvReq struct {
	Env []EnvVarJSON `json:"env"`
}

// setEnv REPLACES an app's env set (plain + secret) — the ONE post-create write
// path for env vars. Secret:true values are sealed into KMS (sealSecretEnv blanks
// the persisted value); a secret dropped from the set is removed from the CR on
// the next deploy. Fails closed if KMS is unavailable. If the app is already
// deployed it re-declares the secret sync immediately (the operator re-materializes
// the Secret); pods pick up changed env on their next deploy/restart.
func setEnv(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, herr := loadApp(s, c, org)
	if herr != nil {
		return herr
	}
	var body setEnvReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	for _, e := range body.Env {
		if !envKeyRE.MatchString(e.Key) {
			return zip.ErrBadRequest("env key must match ^[A-Za-z_][A-Za-z0-9_]*$")
		}
	}
	sealed, err := sealSecretEnv(s, c.Context(), org, a.Slug, body.Env)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	envJSON, _ := json.Marshal(sealed)
	a.EnvJSON = string(envJSON)
	a.UpdatedAt = time.Now().Unix()
	if err := s.State.store.UpdateApplication(c.Context(), a); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist env: %v", err)
	}
	// Re-declare the secret sync so an added/removed secret updates the KMSSecret CR
	// now (only meaningful once the tenant namespace exists — i.e. after a deploy).
	if a.Namespace != "" {
		ensureSecretSync(s, c.Context(), org, a)
	}
	v := toAppView(a)
	v.Phase, v.Health = s.State.k8s.observeService(c.Context(), org, a.Slug)
	v.SecretSync, v.SecretSyncDetail = s.State.k8s.observeSecretSync(c.Context(), org, a.Slug, len(secretEnvKeys(a.EnvJSON)) > 0)
	return c.JSON(http.StatusOK, v)
}

// ── health ───────────────────────────────────────────────────────────────────

// health is a REAL probe: 200 when the metadata store is open AND the cluster is
// reachable; 503 + the real reason otherwise (never status-theater). Not
// admin-gated — liveness must be probe-able without a JWT.
// health probes the cluster for real. A constructed client proves nothing — it is
// built from a kubeconfig, not from a reachable apiserver — so this asks the one
// question the deploy path depends on: can we LIST the operator App CRD? That
// answers reachability AND CRD presence in a single bounded call (Limit 1). The
// probe came from the folded /v1/paas/health, which is why it survived the fold and
// the nil-check it replaced did not: two health routes cannot both be the truth,
// and a nil-check reports "ok" while every deploy 502s.
func health(s *cloud.Service[state], c *zip.Ctx) error {
	res := map[string]any{"service": "platform", "status": "ok", "k8s": s.State.k8s.dyn != nil}
	if s.State.k8s.dyn == nil {
		res["status"] = "degraded"
		res["error"] = s.State.k8s.initErr
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	if _, err := s.State.k8s.dyn.Resource(k8s.Apps).Namespace(scanOrder()[0]).
		List(c.Context(), metav1.ListOptions{Limit: 1}); err != nil {
		res["status"], res["crd"], res["error"] = "degraded", false, err.Error()
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["crd"] = true
	return c.JSON(http.StatusOK, res)
}

// ── helpers ──────────────────────────────────────────────────────────────────

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func projectParam(c *zip.Ctx) string { return strings.ToLower(strings.TrimSpace(c.Param("project"))) }
func appParam(c *zip.Ctx) string     { return strings.ToLower(strings.TrimSpace(c.Param("app"))) }

// normalizeSlug returns an explicit slug (lowercased/trimmed) or derives one
// from the name.
func normalizeSlug(explicit, name string) string {
	slug := strings.ToLower(strings.TrimSpace(explicit))
	if slug == "" {
		slug = slugify(name)
	}
	return slug
}

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

func branchDefault(repoURL string) string {
	if strings.TrimSpace(repoURL) == "" {
		return ""
	}
	return "main"
}

// validateOrgDomains binds every ingress host that will render into the operator
// CR to the CALLER's own org (RED — cross-tenant/apex domain hijack). Without it,
// a tenant could set domains:["api.hanzo.ai"] or another org's host and the
// operator would render an Ingress claiming it. A host is accepted iff it is
// EITHER under the tenant's own subtree "<org>.<sitesHost>" (structurally owned,
// e.g. "*.maxpower.hanzo.app") OR a BYO custom domain this org has already PROVEN
// ownership of (a verified platform_domains row). Every other host — an unverified
// claim, another org's domain, a Hanzo apex — is refused (501), so an arbitrary
// host can only reach the ingress through add-domain → DNS verify, never blindly.
func validateOrgDomains(s *cloud.Service[state], ctx context.Context, org string, domains []string) error {
	for _, d := range domains {
		if isOrgSubtreeHost(s, org, d) {
			continue
		}
		// A non-subtree host is allowed only when this org owns a VERIFIED claim on
		// it. LookupDomain is the global (cross-org) uniqueness read; we accept it
		// only when the row is this org's AND verified — a foreign or pending row is
		// refused, so the ownership boundary holds.
		if s.State.store != nil {
			if row, found, err := s.State.store.LookupDomain(ctx, d); err == nil && found && row.Org == org && row.Status == "verified" {
				continue
			}
		}
		return zip.Errorf(http.StatusNotImplemented,
			"custom domain %q is not verified for this org; add it to the app and complete DNS verification first (only hosts under %q, or an ownership-verified custom domain, are accepted)", d, org+"."+s.State.sitesHost)
	}
	return nil
}

// sanitizeDomains lowercases, trims, and drops empties/dupes from ingress hosts.
func sanitizeDomains(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// genID returns "<prefix>_<22-char-url-safe-token>" (96 bits of entropy).
func genID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func getenv(key, dflt string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return dflt
}

// Shutdown closes the platform store. Idempotent. Mirrors the projects
// Shutdown contract so the serve layer releases subsystem resources uniformly.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	if mounted.State.cancel != nil {
		mounted.State.cancel() // stop the build reconciler
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
