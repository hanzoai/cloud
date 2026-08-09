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
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/brand"
	"github.com/hanzoai/cloud/internal/fqdn"
	"github.com/hanzoai/namespace"
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
	store, err := openStore(deps.DataDir)
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
	// The project source is the CANONICAL IAM: the iam peer over the plane when
	// the deployment names a separate one (IAM_URL), the embedded store when this
	// binary IS the IAM. Neither takes an address or a credential — the peer is
	// addressed by name and the tenant rides the call.
	s.State.projects = newProjectStore()
	mounted = s
	// platform registers TYPED ops, which live on the *zip.App's registry — the one
	// value OpenAPI, MCP, the CLI and the generated SDKs are projected from. A Router
	// that is not backed by one must fail the mount rather than serve routes no
	// projection knows.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("platform.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	// UNIFIED PAYWALL (server-side enforcement). To gate the /v1/platform surface
	// behind the caller's plan, wrap it with entitlements.RequireProduct(deps.Commerce,
	// "platform") — note routes() registers FLAT absolute paths (not a group), so
	// enabling means converting them to app.Group("/v1/platform", mw) or wrapping each.
	// DEFERRED — DO NOT ENABLE YET: the "platform" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so enforcing now would 402 every org. Flip on
	// once the catalog licenses "platform" to a tier. See clients/entitlements.
	routes(zapp, s)

	// The fleet board (/v1/platform/fleet) — the platform's view of its OWN service
	// tier, folded in from what used to be the separate /v1/paas product. It carries
	// its own dynamic client because it observes the whole platform tier, not one
	// tenant namespace; the routes are siblings under the one /v1/platform prefix.
	fb := cloud.NewBase(deps, "platform")
	fs := &cloud.Service[fleetState]{Base: fb, State: buildFleet(fb)}
	fleetRoutes(zapp, fs)

	// The delivery surface (/v1/platform/apps, /v1/platform/cd) — declarations in
	// universe git reconciled by cd.hanzo.ai, which is the ONE deploy plane. It
	// takes both services because it joins two planes: the declaration comes from
	// git (s holds the KMS client the universe token is read with) and the
	// reconciliation from the cluster (fs holds the dynamic client). Handed both
	// here rather than reaching a package global at request time — see apps.go.
	appsRoutes(zapp, s, fs)

	// The cloud's own embedded-git apex is a trusted build source (clients/git
	// serves repos at this host), so a self-hosted-git app builds with no env.
	//
	// The APEX, not deps.Domain verbatim. deps.Domain is this deployment's own
	// host — "api.hanzo.ai" — and the forge is "git.hanzo.ai": a SIBLING, not a
	// child. hostAllowed matches selfGitHost or a subdomain OF it, so handing it
	// the API host made the self-hosted-git allowance unreachable: every native
	// build was refused with `host "git.hanzo.ai" is not an allowed git
	// provider`, and the estate fell back to GitHub. Taking the registrable apex
	// ("hanzo.ai") admits every sibling the deployment owns — git., ci., cd. —
	// for hanzo.ai, lux.network, zoo.network and any white-label domain alike,
	// with no list to maintain per brand.
	selfGitHost = brand.Apex(deps.Domain)

	// git-push-to-deploy: a push landed on the embedded git server (clients/git)
	// triggers a build for every app tracking that repo+branch. Inverted so git
	// never imports platform — build.go RegisterPushBuilder ⇄ OnGitPush (push.go).
	cloud.RegisterPushBuilder(func(ctx context.Context, ev cloud.GitPushEvent) error { return buildFromPush(mounted, ctx, ev) })

	// The same trigger on the plane. git and platform are separate processes, so
	// the registration above is nil in the process where pushes actually land —
	// which made OnGitPush's nil-when-unregistered a silent no-op for every push
	// the fleet has ever served.
	exposePush()

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

// routes registers the /v1/platform surface as TYPED ops. Extracted from Mount so
// tests can mount the same routes over a Service with an injected (fake/nil) k8s
// client — hermetic, never touching a real cluster.
//
// Every op takes the ABSOLUTE path on the app rather than a leaf on a group. The
// collection root IS /v1/platform/projects: declaring it as the empty leaf of a
// Group("/v1/platform/projects") names /v1/platform/projects/ — a path this API has
// never served — and op.Path is the identity every projection keys on. It is also
// what keeps the surface free of a childless middleware node, which zip refuses to
// compose.
//
// Registration order is match order, and it is the order these routes have always
// had: the static collections before the parameterised forms, /v1/run and the flat
// console reads at order 124 so they bind ahead of the AI /v1/* catch-all (150).
//
// This surface installs no middleware of its own — see ops.go on why the identity
// bridge belongs to whoever composes the app.
func routes(app *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}

	// projects
	// A project is IAM's resource, created and deleted at /v1/iam/projects. The
	// platform makes APPS under one, never the project itself, so it exposes no
	// project lifecycle — only this read, which is a PROJECTION IAM cannot serve:
	// the project plus how many platform apps live under it.
	zip.Get(app, "/v1/platform/projects", o.listProjects)
	zip.Get(app, "/v1/platform/projects/:project", o.getProject)

	// applications
	zip.Get(app, "/v1/platform/projects/:project/apps", o.listApps)
	zip.Post(app, "/v1/platform/projects/:project/apps", o.createApp, zip.WithStatus(http.StatusCreated))
	zip.Get(app, "/v1/platform/projects/:project/apps/:app", o.getApp)
	zip.Delete(app, "/v1/platform/projects/:project/apps/:app", o.deleteApp)

	// env management: replace an app's env set (plain + secret). Secret values are
	// sealed into KMS; plaintext is never persisted (secrets.go). One write path.
	zip.Put(app, "/v1/platform/projects/:project/apps/:app/env", o.setEnv)

	// deploy lifecycle + history (deploy.go)
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/deploy", o.deploy, zip.WithStatus(http.StatusAccepted))
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/stop", o.stop)
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/start", o.start)
	zip.Get(app, "/v1/platform/projects/:project/apps/:app/deployments", o.listDeployments)
	zip.Get(app, "/v1/platform/projects/:project/apps/:app/deployments/:id", o.getDeployment)
	zip.Get(app, "/v1/platform/projects/:project/apps/:app/deployments/:id/logs", o.deploymentLogs)

	// Vercel-style release flows (preview.go), all reusing the ONE deploy mechanic
	// (deployTagCore → applyLive; write the Service CR, the operator reconciles):
	// a per-branch preview target with its OWN slug + host, promote an already-built
	// tag/deployment to prod, and rollback to a prior image. Org-scoped like the rest.
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/preview", o.preview, zip.WithStatus(http.StatusAccepted))
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/promote", o.promote, zip.WithStatus(http.StatusAccepted))
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/rollback", o.rollback, zip.WithStatus(http.StatusAccepted))

	// custom domains + org-subtree hosts (domains.go): list, add (subtree active /
	// custom pending-with-challenge), verify a custom claim's DNS, remove.
	zip.Get(app, "/v1/platform/projects/:project/apps/:app/domains", o.listDomains)
	// Two success statuses, because attaching a host has two honest outcomes: a new
	// claim is 201, and re-adding this app's own is idempotent at 200. The answer
	// states which (domainView.StatusCode), so the document publishes both.
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/domains", o.addDomain,
		zip.WithStatus(http.StatusOK, http.StatusCreated))
	// Declared even though 200 is the default, because domainView STATES its status
	// and zip refuses a code an op did not declare — an answer may only send what
	// the document publishes. Verifying never creates a claim, so 200 is the whole
	// set here.
	zip.Post(app, "/v1/platform/projects/:project/apps/:app/domains/:host/verify", o.verifyDomain,
		zip.WithStatus(http.StatusOK))
	zip.Delete(app, "/v1/platform/projects/:project/apps/:app/domains/:host", o.removeDomain)

	// The probe declares its 503 because it answers a failure with its OWN body —
	// the real reason and whether the CRD was found — rather than the error envelope.
	zip.Get(app, "/v1/platform/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))

	// Container-serverless one-shot: POST /v1/run — create-or-update an image app
	// (in the org's default project) and deploy it via the SAME Service-CR writer,
	// returning its live URL. A top-level convenience over the project→app→deploy
	// flow above; org-scoped by the validated identity, never by the body (run.go).
	zip.Post(app, "/v1/run", o.run, zip.WithStatus(http.StatusAccepted))

	// console aggregates (Environments / Pipelines / Builds / Releases) — flat,
	// top-level REST DERIVED from the SAME project/app/deploy/build data above
	// (console.go). GET-only projections: the ONE write path stays POST .../apps
	// and .../deploy. Every op is org-scoped through the validated identity like the
	// rest.
	zip.Get(app, "/v1/environments", o.listEnvironments)
	zip.Get(app, "/v1/pipelines", o.listPipelines)
	zip.Get(app, "/v1/builds", o.listBuilds)
	zip.Get(app, "/v1/releases", o.listReleases)

	// Native build API (the no-GitHub-builders trigger, ex-/v1/arcd). Privileged:
	// token-gated + image-ref allowlisted (runner.go). `hanzo build`, the
	// git-push-to-deploy hook, and cloud's own self-release all POST here.
	zip.Post(app, "/v1/runner", o.runnerBuild, zip.WithStatus(http.StatusAccepted))
	// A release answers 202 with an id, so the id has to be answerable. Without
	// these a release that dies in the detached pipeline is indistinguishable from
	// one still running — which is exactly how a release that launched nothing
	// looked like one in flight.
	zip.Get(app, "/v1/runner/releases", o.listSelfReleases)
	zip.Get(app, "/v1/runner/releases/:id", o.getSelfRelease)
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
func tenant(c *zip.Ctx) (string, bool) {
	if !principal.Validated(c) {
		return "", false // no validated principal — refuse the forgeable Phase-1 data path
	}
	// ONE org normalizer, cloud-wide: the injective namespace.Sanitize, so a
	// tenant resolved here keys the SAME namespace/image boundary as everywhere
	// else and two distinct owners never collapse onto one tenant (CRIT-2).
	if org := namespace.Sanitize(c.Org()); org != "" {
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

// projectList is the caller org's projects as one list answers them. It is a
// NAMED slice because the route has always served a bare JSON array, and a bare
// array is what its clients parse; naming it is what lets the document describe
// the array instead of publishing an anonymous one.
type projectList []projectView

// listProjects returns your org's projects, each with how many apps live under it.
//
// It lists the caller org's projects with the number of platform applications in
// each. A project is IAM's resource — it is created and deleted at
// /v1/iam/projects, never here — so this is the ONE projection IAM cannot serve:
// the project plus what the platform has put under it.
//
// Requires a validated principal; 403 without one, and the org comes from that
// validated identity rather than a request header. This is the console's first
// authenticated read, so a project store that is not yet initialised degrades to
// an EMPTY list rather than a 500 — a new org genuinely has zero projects — and
// the real cause is surfaced to operators instead of to the caller.
func (o ops) listProjects(ctx context.Context, _ *noInput) (*projectList, error) {
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.projects.List(ctx, org)
	if err != nil {
		// This list is the console dashboard's first authenticated read. A
		// co-resident IAM store that is not yet initialized (the iamStore guard's
		// typed 503) — or any transient store failure — must degrade to an empty
		// project set, never a 500 that breaks dashboard init: a new org genuinely
		// has zero projects. The real cause is surfaced to operators, not swallowed;
		// answered as an empty list so no outer error filter can reflatten it.
		o.s.Log.Warn("platform: project store unavailable; serving empty project list", "org", org, "err", err)
		return &projectList{}, nil
	}
	out := make(projectList, 0, len(rows))
	for _, p := range rows {
		if p == nil {
			continue // never nil-deref a stray nil row into a 500
		}
		apps, _ := o.s.State.store.ListApplications(ctx, org, p.Name)
		out = append(out, toProjectView(p, len(apps)))
	}
	return &out, nil
}

// getProject returns one project and its app count.
//
// It returns a single project of the caller's org with the number of platform
// applications under it. A project this org does not have is 404, which is also
// what another tenant's project looks like from here. Requires a validated
// principal; 403 without one.
func (o ops) getProject(ctx context.Context, in *projectRef) (*projectView, error) {
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	p, err := o.s.State.projects.Get(ctx, org, slugOf(in.Project))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if p == nil {
		return nil, zip.ErrNotFound("project not found")
	}
	apps, _ := o.s.State.store.ListApplications(ctx, org, p.Name)
	v := toProjectView(p, len(apps))
	return &v, nil
}

// ── application handlers ─────────────────────────────────────────────────────

// createAppReq is a new application, from a git repo or a container image.
//
// Every field but Project carries `url:"-"`, which is what keeps this a BODY:
// zip's binder fills an In field from the query string as well as the body, and
// this route has never taken an application's fields there — without the opt-out
// `?slug=other` would silently redirect the write the body asked for.
type createAppReq struct {
	// Project is the project to create the application under, from the path.
	Project string `json:"project"`
	// Name is the application's display name. Required; the slug is derived from
	// it when none is given.
	Name string `json:"name" url:"-"`
	// Slug is the app's identity in the cluster — its CR name and part of its
	// host. Given or derived from Name, it must match
	// `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`, and one already used in this
	// project is 409.
	Slug string `json:"slug" url:"-"`
	// Description is free text about what the application is.
	Description string `json:"description" url:"-"`
	// Environment is the deploy target this app names ("production" by default).
	Environment string `json:"environment" url:"-"`
	// Source is `git`, which requires repo.url, or `image`, which requires
	// image.repository. Anything else is 400.
	Source string `json:"source" url:"-"` // git | image
	// Repo is the git source to build from, for source `git`.
	Repo gitOrigin `json:"repo" url:"-"`
	// Image is the container image to run, for source `image`.
	Image imageOrigin `json:"image" url:"-"`
	// BuildType is `pack` — the zero-config default that detects any project —
	// or `dockerfile`, the explicit escape hatch. An image app never builds.
	BuildType string `json:"buildType" url:"-"`
	// Dockerfile is the path to build from, for buildType `dockerfile`.
	Dockerfile string `json:"dockerfile" url:"-"`
	// Port is the container port the app listens on.
	Port int `json:"port" url:"-"`
	// Replicas is how many copies to run; clamped to the deployment's limit
	// rather than refused.
	Replicas int `json:"replicas" url:"-"`
	// StorageGB is the persistent volume size in GiB; absent means stateless.
	// Clamped to the deployment's limit rather than refused.
	StorageGB int `json:"storageGb" url:"-"`
	// Env is the application's environment. Keys must match
	// `^[A-Za-z_][A-Za-z0-9_]*$`; a variable marked `secret: true` is sealed into
	// KMS and its plaintext is never written to the database.
	Env []EnvVarJSON `json:"env" url:"-"`
	// Domains are extra ingress hosts. The canonical default host is always
	// attached; a bare custom host is refused here and must go through
	// add-domain → verify first.
	Domains []string `json:"domains" url:"-"`
}

// gitOrigin is the git source an application builds from. It is a named type
// rather than the anonymous struct it was, because the document names what it
// publishes and an anonymous struct has no name to publish.
type gitOrigin struct {
	// URL is the repository clone URL. Required for source `git`, and validated
	// against the same allowlist the privileged build enforces.
	URL string `json:"url"`
	// Branch is the branch to build; defaults to `main` for a git source.
	Branch string `json:"branch"`
}

// imageOrigin is the container image an application runs.
type imageOrigin struct {
	// Repository is the image repository. Required for source `image`.
	Repository string `json:"repository"`
	// Tag is the image tag to deploy; `latest` when omitted.
	Tag string `json:"tag"`
}

// requireProject confirms the request's project exists in IAM for org, returning
// its name (the app-scope key) or a mapped 404/500. It is the ONE place the app
// routes verify project existence before touching platform's app tree.
func requireProject(s *cloud.Service[state], ctx context.Context, org, project string) (string, error) {
	project = slugOf(project)
	// The DEFAULT project is implicit — part of what an org IS. Its row is owed
	// by IAM provisioning, and no surface (run, apps under it) fails an org for
	// a row IAM owes it. Every other project must exist in IAM.
	if project == principal.DefaultProject {
		return project, nil
	}
	ok, err := s.State.projects.Exists(ctx, org, project)
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	if !ok {
		return "", zip.ErrNotFound("project not found")
	}
	return project, nil
}

// createApp creates an application from a git repo or a container image.
//
// It registers a new application under one of the caller org's projects and
// answers 201 with it. Creating does NOT deploy: the app lands in `draft` and
// nothing reaches the cluster until /deploy.
//
// `source` is `git` — which requires `repo.url` — or `image`, which requires
// `image.repository`; anything else is 400. A git app builds with zero-config
// `pack` by default and may opt into `dockerfile`; an image app never builds. The
// repo URL and Dockerfile path are validated here against the SAME allowlist the
// privileged build enforces, so an unsafe source is refused before it is ever
// persisted.
//
// The `slug` is the app's identity in the cluster: given or derived from `name`,
// it must match `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`, and a slug already used in
// this project is 409. `replicas` and `storageGb` are clamped to the deployment's
// limits rather than refused.
//
// Env keys must match `^[A-Za-z_][A-Za-z0-9_]*$`. A variable marked `secret: true`
// is SEALED into KMS and its plaintext is never written to the database — and if
// KMS is unavailable the create fails 503 rather than falling back to storing a
// secret in the clear.
//
// The app is seeded with its canonical default host, so it has a working HTTPS URL
// the moment it deploys. A bare custom domain cannot be attached here — it has to
// go through add-domain and DNS verification first. Requires a validated
// principal; 403 without one, and every cluster object it will later create lands
// in that org's own `tenant-<org>` namespace.
func (o ops) createApp(ctx context.Context, body *createAppReq) (*appView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	project, err := requireProject(s, ctx, org, body.Project)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	slug := normalizeSlug(body.Slug, name)
	if !slugRE.MatchString(slug) {
		return nil, zip.ErrBadRequest("slug must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	source := strings.ToLower(strings.TrimSpace(body.Source))
	switch source {
	case "git":
		if strings.TrimSpace(body.Repo.URL) == "" {
			return nil, zip.ErrBadRequest("source 'git' requires repo.url")
		}
		// (CRIT-1) Reject an unsafe repo.url / dockerfile at the boundary (400)
		// before it is ever persisted or reaches the privileged build. This is the
		// SAME validator the build path enforces (validate.go) — refusing early is
		// defense in depth + a clear client error, not a new rule.
		if _, err := validateRepoURL(body.Repo.URL); err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
		if strings.TrimSpace(body.Dockerfile) != "" {
			if _, err := validateDockerfile(body.Dockerfile); err != nil {
				return nil, zip.ErrBadRequest(err.Error())
			}
		}
	case "image":
		if strings.TrimSpace(body.Image.Repository) == "" {
			return nil, zip.ErrBadRequest("source 'image' requires image.repository")
		}
	default:
		return nil, zip.ErrBadRequest("source must be 'git' or 'image'")
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
			return nil, zip.ErrBadRequest("buildType must be 'pack' or 'dockerfile'")
		}
	}
	// Validate env keys at the boundary, then SEAL secret:true values into KMS so
	// plaintext is NEVER persisted (sealSecretEnv blanks the stored value; the real
	// value lives only in the embedded KMS). Fails closed if KMS is unavailable —
	// a plaintext secret never lands in the DB as a fallback.
	for _, e := range body.Env {
		if !envKeyRE.MatchString(e.Key) {
			return nil, zip.ErrBadRequest("env key must match ^[A-Za-z_][A-Za-z0-9_]*$")
		}
	}
	sealedEnv, err := sealSecretEnv(s, ctx, org, slug, body.Env)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	envJSON, _ := json.Marshal(sealedEnv)
	// Seed the canonical default host so every app has a working HTTPS URL the
	// moment it deploys, then validate the full ingress set (subtree hosts + the
	// default always pass; a bare custom host at create still 501s — it must go
	// through add-domain → verify first).
	domains := seedDefaultDomain(s, org, slug, sanitizeDomains(body.Domains))
	if err := validateOrgDomains(s, ctx, org, domains); err != nil {
		return nil, err
	}
	domainsJSON, _ := json.Marshal(domains)

	now := time.Now().Unix()
	id := genID("app")
	a := Application{
		ID: id, Org: org, ProjectID: project, Slug: slug, Name: name, Description: strings.TrimSpace(body.Description),
		Environment: cmp.Or(strings.TrimSpace(body.Environment), "production"), Source: source,
		RepoURL: strings.TrimSpace(body.Repo.URL), RepoBranch: cmp.Or(strings.TrimSpace(body.Repo.Branch), branchDefault(body.Repo.URL)),
		RepoProvider: providerFromURL(body.Repo.URL), ImageRepo: strings.TrimSpace(body.Image.Repository), ImageTag: strings.TrimSpace(body.Image.Tag),
		BuildType: buildType, Dockerfile: strings.TrimSpace(body.Dockerfile), Port: portOr(body.Port), Replicas: s.State.k8s.limits.clampReplicas(body.Replicas),
		StorageGB: s.State.k8s.limits.clampStorage(body.StorageGB),
		EnvJSON:   string(envJSON), DomainsJSON: string(domainsJSON), Status: "draft", Namespace: tenantNamespace(org),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateApplication(ctx, a); err != nil {
		if errors.Is(err, errConflict) {
			return nil, zip.ErrConflict("application slug already exists in this project")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	v := toAppView(a)
	return &v, nil
}

// appList is one project's applications as a list answers them — a bare JSON
// array, named so the document can describe it.
type appList []appView

// listApps returns the applications in one project, with what the cluster says
// about them.
//
// It lists the caller org's applications under one project. Each row carries the
// stored record and, for an app that is live or deploying, the LIVE phase and
// health read from its operator Service CR; an app with sealed env also carries
// its secret-sync state. Those cluster reads are best-effort — an unreachable
// cluster leaves those fields empty and never blocks the listing.
//
// The project must exist in IAM for this org, or the answer is 404; the `default`
// project is implicit and always accepted, because it is part of what an org IS.
// Requires a validated principal; 403 without one.
func (o ops) listApps(ctx context.Context, in *projectRef) (*appList, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	project, err := requireProject(s, ctx, org, in.Project)
	if err != nil {
		return nil, err
	}
	rows, err := s.State.store.ListApplications(ctx, org, project)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	out := make(appList, 0, len(rows))
	for _, a := range rows {
		v := toAppView(a)
		// Attach live phase/health from the operator CR when the cluster is
		// reachable (best-effort; never blocks the list).
		if a.Status == "live" || a.Status == "deploying" {
			v.Phase, v.Health = s.State.k8s.observeService(ctx, org, a.Slug)
		}
		if len(secretEnvKeys(a.EnvJSON)) > 0 {
			v.SecretSync, v.SecretSyncDetail = s.State.k8s.observeSecretSync(ctx, org, a.Slug, true)
		}
		out = append(out, v)
	}
	return &out, nil
}

// loadApp resolves (projectName, app) for the caller's org, re-verifying tenancy
// at each hop: the project must exist in IAM, then the app under it. Returns the
// project name (the app-scope key / operator part-of label) and application, or a
// mapped HTTP error.
func loadApp(s *cloud.Service[state], ctx context.Context, org, project, app string) (string, Application, error) {
	project, herr := requireProject(s, ctx, org, project)
	if herr != nil {
		return "", Application{}, herr
	}
	a, err := s.State.store.GetApplication(ctx, org, project, slugOf(app))
	if errors.Is(err, errNotFound) {
		return "", Application{}, zip.ErrNotFound("application not found")
	}
	if err != nil {
		return "", Application{}, zip.Errorf(http.StatusInternalServerError, "get app: %v", err)
	}
	return project, a, nil
}

// getApp returns one application, with its live phase, health and secret sync.
//
// It returns a single application of the caller's org together with what the
// cluster currently reports for it: the operator Service CR's phase and health,
// and whether its sealed env has synced. An app this org and project do not have
// is 404. Requires a validated principal; 403 without one.
func (o ops) getApp(ctx context.Context, in *appRef) (*appView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, in.Project, in.App)
	if err != nil {
		return nil, err
	}
	v := toAppView(a)
	v.Phase, v.Health = s.State.k8s.observeService(ctx, org, a.Slug)
	v.SecretSync, v.SecretSyncDetail = s.State.k8s.observeSecretSync(ctx, org, a.Slug, len(secretEnvKeys(a.EnvJSON)) > 0)
	return &v, nil
}

// deleteApp deletes an application and tears down what it runs.
//
// It removes the application record and tears down what it owns in the org's
// tenant namespace — its operator Service CR and its KMSSecret — then answers 204.
// An app this org and project do not have is 404, never a silent success.
//
// Teardown is best-effort by design: a cluster that refuses or is unreachable does
// not block the delete, so the record cannot be left orphaned behind a broken
// cluster; the failure is logged for operators and the orphan reaper reconciles
// it. Requires a validated principal; 403 without one.
func (o ops) deleteApp(ctx context.Context, in *appRef) (*noContent, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	project, err := requireProject(s, ctx, org, in.Project)
	if err != nil {
		return nil, err
	}
	a, deleted, err := s.State.store.DeleteApplication(ctx, org, project, slugOf(in.App))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("application not found")
	}
	if err := s.State.k8s.deleteService(ctx, org, a.Slug); err != nil {
		s.Log.Warn("teardown service CR failed (continuing)", "org", org, "app", a.Slug, "err", err)
	}
	if err := s.State.k8s.deleteKMSSecret(ctx, org, a.Slug); err != nil {
		s.Log.Warn("teardown KMSSecret failed (continuing)", "org", org, "app", a.Slug, "err", err)
	}
	return nil, nil
}

// ── env management ─────────────────────────────────────────────────────────────

// setEnvReq is an application's whole environment set. Env carries `url:"-"` so
// the variables can only arrive in the body: this route has never taken them off
// the query string, and a binder that filled them from there would let a URL
// rewrite an app's environment.
type setEnvReq struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
	// Env is the app's whole environment set, REPLACING what it had. Keys must
	// match `^[A-Za-z_][A-Za-z0-9_]*$`; a variable marked `secret: true` is
	// sealed into KMS and blanked in the database.
	Env []EnvVarJSON `json:"env" url:"-"`
}

// setEnv replaces an app's environment variables.
//
// It writes the app's whole environment set and answers the updated application.
// This is the one post-create write path for env, and it REPLACES rather than
// merges: a variable absent from the body is gone, and a secret dropped from the
// set leaves the app's Secret on its next deploy.
//
// Keys must match `^[A-Za-z_][A-Za-z0-9_]*$`. A value marked `secret: true` is
// sealed into KMS and blanked in the database, so plaintext is never persisted —
// and the write fails 503 if KMS is unavailable rather than storing one in the
// clear.
//
// The rule worth knowing: this does not restart anything. Once the app has been
// deployed the secret sync is re-declared immediately so the operator
// re-materialises the Secret, but RUNNING pods keep the environment they started
// with until their next deploy or restart. Requires a validated principal; 403
// without one.
func (o ops) setEnv(ctx context.Context, body *setEnvReq) (*appView, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	_, a, err := loadApp(s, ctx, org, body.Project, body.App)
	if err != nil {
		return nil, err
	}
	for _, e := range body.Env {
		if !envKeyRE.MatchString(e.Key) {
			return nil, zip.ErrBadRequest("env key must match ^[A-Za-z_][A-Za-z0-9_]*$")
		}
	}
	sealed, err := sealSecretEnv(s, ctx, org, a.Slug, body.Env)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	envJSON, _ := json.Marshal(sealed)
	a.EnvJSON = string(envJSON)
	a.UpdatedAt = time.Now().Unix()
	if err := s.State.store.UpdateApplication(ctx, a); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "persist env: %v", err)
	}
	// Re-declare the secret sync so an added/removed secret updates the KMSSecret CR
	// now (only meaningful once the tenant namespace exists — i.e. after a deploy).
	if a.Namespace != "" {
		ensureSecretSync(s, ctx, org, a)
	}
	v := toAppView(a)
	v.Phase, v.Health = s.State.k8s.observeService(ctx, org, a.Slug)
	v.SecretSync, v.SecretSyncDetail = s.State.k8s.observeSecretSync(ctx, org, a.Slug, len(secretEnvKeys(a.EnvJSON)) > 0)
	return &v, nil
}

// ── health ───────────────────────────────────────────────────────────────────

// readiness is what the probe answers: whether this control plane can actually
// deploy anything, and when it cannot, why.
//
// It states its own HTTP status (see StatusCode) because the two outcomes are the
// SAME body at two codes — a degraded probe answers its real reason rather than
// the error envelope, so the reason is a field a client reads and not a string it
// parses out of a message.
type readiness struct {
	// Service is always "platform" — which control plane answered.
	Service string `json:"service"`
	// Status is "ok" when this plane can deploy, "degraded" when it cannot.
	Status string `json:"status"`
	// K8s is whether a cluster client resolved at all. False means no kubeconfig.
	K8s bool `json:"k8s"`
	// CRD is whether the operator App CRD was found, and is absent when no
	// cluster client resolved and the question could not be asked.
	CRD *bool `json:"crd,omitempty"`
	// Error is the real reason the plane is degraded; absent when it is not.
	Error string `json:"error,omitempty"`
}

// StatusCode is the code this answer carries: 200 for a plane that can deploy, 503
// for one that cannot. Declared on the op with zip.WithStatus, so the document,
// the SDKs and the CLI publish both.
func (r *readiness) StatusCode() int {
	if r.Status == "ok" {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

// health reports whether this control plane can actually deploy anything.
//
// A real probe, not a status page. It answers 200 only when the metadata store is
// open AND the cluster is genuinely reachable — proved by LISTING the operator App
// CRD, which settles reachability and CRD presence in one bounded call, and which
// is the exact question every deploy depends on. Anything else is 503 carrying the
// real reason and whether the CRD was found.
//
// A constructed cluster client proves nothing — it is built from a kubeconfig, not
// from a reachable apiserver — so this deliberately spends a round trip rather than
// reporting `ok` while every deploy fails. Not admin-gated: liveness has to be
// probe-able without a credential.
func (o ops) health(ctx context.Context, _ *noInput) (*readiness, error) {
	s := o.s
	res := readiness{Service: "platform", Status: "ok", K8s: s.State.k8s.dyn != nil}
	if s.State.k8s.dyn == nil {
		res.Status, res.Error = "degraded", s.State.k8s.initErr
		return &res, nil
	}
	if _, err := s.State.k8s.dyn.Resource(k8s.Apps).Namespace(scanOrder()[0]).
		List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		found := false
		res.Status, res.CRD, res.Error = "degraded", &found, err.Error()
		return &res, nil
	}
	found := true
	res.CRD = &found
	return &res, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
