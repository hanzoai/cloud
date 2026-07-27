// Package platform is the Hanzo Cloud PaaS control plane: the per-org,
// user-facing Platform-as-a-Service, mounted natively in the unified cloud
// binary at /v1/platform (HIP-0106). It is the Go port of the standalone
// Dokploy (platform.hanzo.ai) tRPC backend — the culmination of "one binary
// ships all of Hanzo Cloud."
//
// Relationship to the sibling subsystems:
//
//   - clients/paas  (/v1/paas)     — the ADMIN fleet drift board: observes +
//     deploys SYSTEM Service CRs across the platform namespaces, SuperAdmin
//     only. It answers "what is the fleet running, and roll a tag."
//   - clients/projects (/v1/projects) — per-org STATIC sites (S3 hosting).
//   - clients/platform (/v1/platform)  — THIS: per-org CONTAINER apps. Users
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
// The API is designed-first in Goa (clients/platform/design; `goa gen` emits
// the OpenAPI 3 contract at clients/platform/design/gen/http/openapi3.*). The
// runtime handlers below implement that contract natively on zip so the binary
// keeps ONE router and stays behind the SanitizeIdentity trust boundary.
package platform

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/provisioning"
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
	resolver    dnsResolver        // custom-domain ownership verification (domains.go); nil ⇒ system resolver
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
		State: state{store: store, projects: iamProjects{}, k8s: k, kmsIdentity: newKMSOrgIdentity(deps.KMS),
			sitesHost: getenv("CLOUD_PLATFORM_SITES_HOST", "hanzo.app")}}
	mounted = s
	// UNIFIED PAYWALL (server-side enforcement). To gate the /v1/platform surface
	// behind the caller's plan, wrap it with entitlements.RequireProduct(deps.Commerce,
	// "platform") — note routes() registers FLAT app.Get paths (not a group), so
	// enabling means converting them to app.Group("/v1/platform", mw) or wrapping each.
	// DEFERRED — DO NOT ENABLE YET: the "platform" product is ABSENT from @hanzo/plans
	// licensing.product_ids (v1.4.4), so enforcing now would 402 every org. Flip on
	// once the catalog licenses "platform" to a tier. See clients/entitlements.
	routes(app, s)

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
	app.Get("/v1/platform/projects", cloud.Handle(s, listProjects))
	app.Post("/v1/platform/projects", cloud.Handle(s, createProject))
	app.Get("/v1/platform/projects/:project", cloud.Handle(s, getProject))
	app.Delete("/v1/platform/projects/:project", cloud.Handle(s, deleteProject))

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

type repoView struct {
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
	Repo                repoView     `json:"repo"`
	Image               imageView    `json:"image"`
	BuildType           string       `json:"buildType,omitempty"`
	Dockerfile          string       `json:"dockerfile,omitempty"`
	Env                 []EnvVarJSON `json:"env"`
	Port                int          `json:"port"`
	Replicas            int          `json:"replicas"`
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
		Repo:      repoView{URL: a.RepoURL, Branch: a.RepoBranch, Provider: a.RepoProvider},
		Image:     imageView{Repository: a.ImageRepo, Tag: a.ImageTag},
		BuildType: a.BuildType, Dockerfile: a.Dockerfile, Env: env, Port: a.Port,
		Replicas: a.Replicas, Domains: domains, Status: a.Status, Namespace: a.Namespace,
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

type createProjectReq struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

func createProject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body createProjectReq
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
	p, err := s.State.projects.Create(c.Context(), org, slug, name, strings.TrimSpace(body.Description))
	if errors.Is(err, errConflict) {
		return zip.ErrConflict("project slug already exists in this org")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toProjectView(p, 0))
}

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

func deleteProject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	project := projectParam(c)
	// Delete the project in IAM (the source of truth) first; a missing project is
	// a clean 404. Then cascade-delete platform's own app tree under it.
	deleted, err := s.State.projects.Delete(c.Context(), org, project)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("project not found")
	}
	apps, err := s.State.store.DeleteProjectApps(c.Context(), org, project)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete apps: %v", err)
	}
	// Best-effort teardown of each app's Service CR + KMSSecret in the tenant ns.
	for _, a := range apps {
		if err := s.State.k8s.deleteService(c.Context(), org, a.Slug); err != nil {
			s.Log.Warn("teardown service CR failed (continuing)", "org", org, "app", a.Slug, "err", err)
		}
		if err := s.State.k8s.deleteKMSSecret(c.Context(), org, a.Slug); err != nil {
			s.Log.Warn("teardown KMSSecret failed (continuing)", "org", org, "app", a.Slug, "err", err)
		}
	}
	return c.NoContent(http.StatusNoContent)
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
	Env        []EnvVarJSON `json:"env"`
	Domains    []string     `json:"domains"`
}

// requireProject confirms the request's :project exists in IAM for org, returning
// its name (the app-scope key) or a mapped 404/500. It is the ONE place the app
// routes verify project existence before touching platform's app tree.
func requireProject(s *cloud.Service[state], c *zip.Ctx, org string) (string, error) {
	project := projectParam(c)
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
		EnvJSON: string(envJSON), DomainsJSON: string(domainsJSON), Status: "draft", Namespace: tenantNamespace(org),
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
func health(s *cloud.Service[state], c *zip.Ctx) error {
	res := map[string]any{"service": "platform", "status": "ok", "k8s": s.State.k8s.dyn != nil}
	if s.State.k8s.dyn == nil {
		res["status"] = "degraded"
		res["error"] = s.State.k8s.initErr
		return c.JSON(http.StatusServiceUnavailable, res)
	}
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

// Shutdown closes the platform store. Idempotent. Mirrors the projects/paas
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
