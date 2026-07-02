// Package platform is the Hanzo Cloud PaaS control plane: the per-org,
// user-facing Platform-as-a-Service, mounted natively in the unified cloud
// binary at /v1/platform (HIP-0106). It is the Go port of the standalone
// Dokploy (platform.hanzo.ai) tRPC backend — the culmination of "one binary
// ships all of Hanzo Cloud."
//
// Relationship to the sibling subsystems:
//
//   - clients/paassvc  (/v1/paas)     — the ADMIN fleet drift board: observes +
//     deploys SYSTEM Service CRs across the platform namespaces, global-admin
//     only. It answers "what is the fleet running, and roll a tag."
//   - clients/projectsvc (/v1/projects) — per-org STATIC sites (S3 hosting).
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
	"github.com/hanzoai/cloud/clients/provisioning"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// slugRE constrains a project/app slug to a DNS/identifier-safe token. It is the
// org-unique handle AND the CR name AND part of the namespace/URL, so it is the
// injection/traversal guard at the boundary.
var slugRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// buildTypes is the closed set of build strategies (Dokploy: buildType). "image"
// means "no build — run a prebuilt image" (source == image).
var buildTypes = map[string]bool{
	"nixpacks": true, "dockerfile": true, "static": true, "buildpacks": true, "image": true,
}

// EnvVarJSON is the JSON shape of one application env var as stored/served.
type EnvVarJSON struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

type svc struct {
	store     *Store
	k8s       *k8sClient
	cancel    context.CancelFunc // stops the build reconciler on Shutdown
	log       luxlog.Logger
	brand     string
	env       string
	domain    string
	sitesHost string // per-tenant apps host suffix; a custom domain must be under <org>.<sitesHost>
}

// mounted is the active service so Shutdown can release the store.
var mounted *svc

// Mount wires the /v1/platform surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("platform.Mount: nil zip.App")
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

	k := newK8sClient(getenv("CLOUD_PLATFORM_IMAGE_PREFIX", defaultBuildImagePrefix), getenv("CLOUD_PLATFORM_BUILD_NS", "hanzo"))
	if k.initErr != "" {
		log.Warn("kubernetes client unavailable; deploy/build will fail closed", "err", k.initErr)
	}

	s := &svc{store: store, k8s: k, log: log, brand: deps.Brand, env: deps.Env, domain: deps.Domain,
		sitesHost: getenv("CLOUD_PLATFORM_SITES_HOST", "hanzo.app")}
	mounted = s
	s.routes(app)

	// Own the git build→deploy handoff: a background reconciler that applies the
	// Service CR once a build Job succeeds (reconcile.go). Restart-safe — it reads
	// "building" deployments from the store, so it resumes across a cloud restart.
	// Started only when the cluster client resolved (else deploy/build fail closed
	// and there is nothing to reconcile).
	if k.dyn != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.runBuildReconciler(ctx)
	}

	log.Info("platform control plane mounted",
		"prefix", "/v1/platform", "k8s", k.dyn != nil, "brand", deps.Brand, "env", deps.Env)
	return nil
}

// routes registers the /v1/platform surface on app. Extracted from Mount so
// tests can mount the same routes over an svc with an injected (fake/nil) k8s
// client — hermetic, never touching a real cluster.
func (s *svc) routes(app *zip.App) {
	// projects
	app.Get("/v1/platform/projects", s.listProjects)
	app.Post("/v1/platform/projects", s.createProject)
	app.Get("/v1/platform/projects/:project", s.getProject)
	app.Delete("/v1/platform/projects/:project", s.deleteProject)

	// applications
	app.Get("/v1/platform/projects/:project/apps", s.listApps)
	app.Post("/v1/platform/projects/:project/apps", s.createApp)
	app.Get("/v1/platform/projects/:project/apps/:app", s.getApp)
	app.Delete("/v1/platform/projects/:project/apps/:app", s.deleteApp)

	// deploy lifecycle + history (deploy.go)
	app.Post("/v1/platform/projects/:project/apps/:app/deploy", s.deploy)
	app.Post("/v1/platform/projects/:project/apps/:app/stop", s.stop)
	app.Post("/v1/platform/projects/:project/apps/:app/start", s.start)
	app.Get("/v1/platform/projects/:project/apps/:app/deployments", s.listDeployments)
	app.Get("/v1/platform/projects/:project/apps/:app/deployments/:id", s.getDeployment)
	app.Get("/v1/platform/projects/:project/apps/:app/deployments/:id/logs", s.deploymentLogs)

	app.Get("/v1/platform/health", s.health)
}

// Registered as "platformsvc" (not "platform") so serve.go's generic
// GET /v1/<name>/health lands at the unrouted /v1/platformsvc/health and does
// NOT shadow the real fail-closed probe at /v1/platform/health (same convention
// as paassvc/mlsvc/kmssvc). Order 124 binds the /v1/platform family before the
// projectsvc (125) neighbours and well before the AI /v1/* catch-all (150).
func init() {
	cloud.Register("platformsvc", 124, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("platform.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
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
// Empty org is allowed only for a validated global admin (bucketed under
// "admin"): a forged X-User-IsAdmin cannot exist without a validated principal
// (SanitizeIdentity sets it only for a JWT-verified global admin, HIP-0026), and
// even then reaches only the admin bucket, never a real tenant's namespace. This
// is the ONLY source of the tenant; no handler reads an org from body or path.
func (s *svc) tenant(c *zip.Ctx) (string, bool) {
	if c.User() == "" {
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

type projectView struct {
	ID           string `json:"id"`
	Org          string `json:"org"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Applications int    `json:"applications"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

func toProjectView(p Project, apps int) projectView {
	return projectView{
		ID: p.ID, Org: p.Org, Slug: p.Slug, Name: p.Name, Description: p.Description,
		Applications: apps, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
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

func (s *svc) createProject(c *zip.Ctx) error {
	org, ok := s.tenant(c)
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
	now := time.Now().Unix()
	id, err := genID("proj")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	p := Project{ID: id, Org: org, Slug: slug, Name: name, Description: strings.TrimSpace(body.Description), CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateProject(c.Context(), p); err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("project slug already exists in this org")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toProjectView(p, 0))
}

func (s *svc) listProjects(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.ListProjects(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]projectView, 0, len(rows))
	for _, p := range rows {
		apps, _ := s.store.ListApplications(c.Context(), org, p.ID)
		out = append(out, toProjectView(p, len(apps)))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *svc) getProject(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, projectParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	apps, _ := s.store.ListApplications(c.Context(), org, p.ID)
	return c.JSON(http.StatusOK, toProjectView(p, len(apps)))
}

func (s *svc) deleteProject(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, apps, deleted, err := s.store.DeleteProject(c.Context(), org, projectParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("project not found")
	}
	// Best-effort teardown of each app's Service CR in the tenant namespace.
	for _, a := range apps {
		if err := s.k8s.deleteService(c.Context(), org, a.Slug); err != nil {
			s.log.Warn("teardown service CR failed (continuing)", "org", org, "app", a.Slug, "err", err)
		}
	}
	_ = p
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

func (s *svc) createApp(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, projectParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
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
	buildType := strings.ToLower(strings.TrimSpace(body.BuildType))
	if buildType == "" {
		if source == "image" {
			buildType = "image"
		} else {
			buildType = "nixpacks"
		}
	}
	if !buildTypes[buildType] {
		return zip.ErrBadRequest("unsupported buildType")
	}
	// Fail-closed on secret env: plaintext secrets are NEVER stored. KMS-sealed
	// secret env is a phase-2 capability; until then reject secret:true loudly.
	for _, e := range body.Env {
		if !envKeyRE.MatchString(e.Key) {
			return zip.ErrBadRequest("env key must match ^[A-Za-z_][A-Za-z0-9_]*$")
		}
		if e.Secret {
			return zip.Errorf(http.StatusNotImplemented,
				"secret env vars require KMS sealing (phase 2); set secret:false or manage the secret via /v1/kms")
		}
	}
	envJSON, _ := json.Marshal(body.Env)
	domains := sanitizeDomains(body.Domains)
	if err := s.validateOrgDomains(org, domains); err != nil {
		return err
	}
	domainsJSON, _ := json.Marshal(domains)

	now := time.Now().Unix()
	id, err := genID("app")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	a := Application{
		ID: id, Org: org, ProjectID: p.ID, Slug: slug, Name: name, Description: strings.TrimSpace(body.Description),
		Environment: firstNonEmpty(strings.TrimSpace(body.Environment), "production"), Source: source,
		RepoURL: strings.TrimSpace(body.Repo.URL), RepoBranch: firstNonEmpty(strings.TrimSpace(body.Repo.Branch), branchDefault(body.Repo.URL)),
		RepoProvider: providerFromURL(body.Repo.URL), ImageRepo: strings.TrimSpace(body.Image.Repository), ImageTag: strings.TrimSpace(body.Image.Tag),
		BuildType: buildType, Dockerfile: strings.TrimSpace(body.Dockerfile), Port: portOr(body.Port), Replicas: s.k8s.limits.clampReplicas(body.Replicas),
		EnvJSON: string(envJSON), DomainsJSON: string(domainsJSON), Status: "draft", Namespace: tenantNamespace(org),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateApplication(c.Context(), a); err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("application slug already exists in this project")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	return c.JSON(http.StatusCreated, toAppView(a))
}

func (s *svc) listApps(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, projectParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	rows, err := s.store.ListApplications(c.Context(), org, p.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	out := make([]appView, 0, len(rows))
	for _, a := range rows {
		v := toAppView(a)
		// Attach live phase/health from the operator CR when the cluster is
		// reachable (best-effort; never blocks the list).
		if a.Status == "live" || a.Status == "deploying" {
			v.Phase, v.Health = s.k8s.observeService(c.Context(), org, a.Slug)
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, out)
}

// loadApp resolves (project, app) for the caller's org, re-verifying tenancy at
// each hop. Returns the project and application or a mapped HTTP error.
func (s *svc) loadApp(c *zip.Ctx, org string) (Project, Application, error) {
	p, err := s.store.GetProject(c.Context(), org, projectParam(c))
	if errors.Is(err, errNotFound) {
		return Project{}, Application{}, zip.ErrNotFound("project not found")
	}
	if err != nil {
		return Project{}, Application{}, zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	a, err := s.store.GetApplication(c.Context(), org, p.ID, appParam(c))
	if errors.Is(err, errNotFound) {
		return Project{}, Application{}, zip.ErrNotFound("application not found")
	}
	if err != nil {
		return Project{}, Application{}, zip.Errorf(http.StatusInternalServerError, "get app: %v", err)
	}
	return p, a, nil
}

func (s *svc) getApp(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	_, a, err := s.loadApp(c, org)
	if err != nil {
		return err
	}
	v := toAppView(a)
	v.Phase, v.Health = s.k8s.observeService(c.Context(), org, a.Slug)
	return c.JSON(http.StatusOK, v)
}

func (s *svc) deleteApp(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, projectParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	a, deleted, err := s.store.DeleteApplication(c.Context(), org, p.ID, appParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("application not found")
	}
	if err := s.k8s.deleteService(c.Context(), org, a.Slug); err != nil {
		s.log.Warn("teardown service CR failed (continuing)", "org", org, "app", a.Slug, "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ── health ───────────────────────────────────────────────────────────────────

// health is a REAL probe: 200 when the metadata store is open AND the cluster is
// reachable; 503 + the real reason otherwise (never status-theater). Not
// admin-gated — liveness must be probe-able without a JWT.
func (s *svc) health(c *zip.Ctx) error {
	res := map[string]any{"service": "platform", "status": "ok", "k8s": s.k8s.dyn != nil}
	if s.k8s.dyn == nil {
		res["status"] = "degraded"
		res["error"] = s.k8s.initErr
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
	case strings.Contains(r, "gitea"):
		return "gitea"
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

// validateOrgDomains binds every requested ingress host to the CALLER's own org
// (RED — cross-tenant/apex domain hijack). Without this, a tenant could set
// domains:["api.hanzo.ai"] or another org's host and the operator would render
// an Ingress claiming it. In the first slice a custom domain MUST be under the
// tenant's own subtree "<org>.<sitesHost>" (e.g. maxpower may only claim
// "*.maxpower.hanzo.app"), so a domain can never target another org's space or a
// Hanzo apex. Verified custom domains (arbitrary hosts + DNS/ACME proof) are a
// phase-2 capability (domain CRUD, [DESIGN]); until then unverified hosts are
// refused rather than blindly trusted.
func (s *svc) validateOrgDomains(org string, domains []string) error {
	if len(domains) == 0 {
		return nil
	}
	suffix := "." + org + "." + s.sitesHost // e.g. ".maxpower.hanzo.app"
	for _, d := range domains {
		if !strings.HasSuffix(d, suffix) || len(d) <= len(suffix) {
			return zip.Errorf(http.StatusNotImplemented,
				"custom domain %q is not verified for this org; the first slice allows only hosts under %q (verified custom domains are phase 2)", d, org+"."+s.sitesHost)
		}
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

// Shutdown closes the platform store. Idempotent. Mirrors the projectsvc/paassvc
// Shutdown contract so the serve layer releases subsystem resources uniformly.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	if mounted.cancel != nil {
		mounted.cancel() // stop the build reconciler
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
