package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountConsole builds a hermetic Service+app so a test can BOTH seed real store
// records and fire HTTP reads against the console aggregate routes. No real
// cluster is touched (fakeK8s / in-memory dynamic client).
func mountConsole(t *testing.T) (*cloud.Service[state], *zip.App) {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test"), Brand: "hanzo"}, State: state{store: store, k8s: fakeK8s(), sitesHost: "hanzo.app"}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	return s, app
}

// seedConsoleFixture writes a realistic org fleet directly to the store: a
// project with an image app (env=production, live, one applied release) and a
// git app (env=staging, building, one in-flight build). These are the SAME
// records the /v1/platform write path produces; the console aggregates only read
// them.
func seedConsoleFixture(t *testing.T, s *cloud.Service[state], org string) (imageAppID, gitAppID string) {
	t.Helper()
	ctx := context.Background()
	proj := Project{ID: "proj_" + org + "_web", Org: org, Slug: "web", Name: "Web", CreatedAt: 100, UpdatedAt: 100}
	if err := s.State.store.CreateProject(ctx, proj); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	api := Application{
		ID: "app_" + org + "_api", Org: org, ProjectID: proj.ID, Slug: "api", Name: "API",
		Environment: "production", Source: "image", ImageRepo: "ghcr.io/hanzoai/nginx", ImageTag: "1.27",
		BuildType: "image", Port: 8080, Replicas: 1, EnvJSON: "[]", DomainsJSON: "[]",
		Status: "live", Namespace: tenantNamespace(org), CreatedAt: 100, UpdatedAt: 140,
	}
	site := Application{
		ID: "app_" + org + "_site", Org: org, ProjectID: proj.ID, Slug: "site", Name: "Site",
		Environment: "staging", Source: "git", RepoURL: "https://github.com/maxpower/site", RepoBranch: "main", RepoProvider: "github",
		BuildType: "nixpacks", Port: 3000, Replicas: 1, EnvJSON: "[]", DomainsJSON: "[]",
		Status: "building", Namespace: tenantNamespace(org), CreatedAt: 100, UpdatedAt: 100,
	}
	for _, a := range []Application{api, site} {
		if err := s.State.store.CreateApplication(ctx, a); err != nil {
			t.Fatalf("seed app %s: %v", a.Slug, err)
		}
	}
	// A released version of the image app (applied to the cluster → "deploying").
	if err := s.State.store.InsertDeployment(ctx, Deployment{
		ID: "dep_api_1", Org: org, ApplicationID: api.ID, Version: 1, Status: "deploying",
		Source: "image", Image: "ghcr.io/hanzoai/nginx:1.27", CreatedAt: 100, UpdatedAt: 130,
	}); err != nil {
		t.Fatalf("seed release deployment: %v", err)
	}
	// An in-flight git build + its building deployment (not yet a release).
	if err := s.State.store.InsertDeployment(ctx, Deployment{
		ID: "dep_site_1", Org: org, ApplicationID: site.ID, Version: 1, Status: "building",
		Source: "git", Commit: "feature/login-page", Image: "ghcr.io/hanzoai/tenant-" + org + "-site:main",
		BuildID: "bld_site_1", CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatalf("seed build deployment: %v", err)
	}
	if err := s.State.store.InsertBuild(ctx, Build{
		ID: "bld_site_1", Org: org, ApplicationID: site.ID, DeploymentID: "dep_site_1", Status: "building",
		Image: "ghcr.io/hanzoai/tenant-" + org + "-site:main", JobName: "pf-build-" + org + "-site-abc", CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	return api.ID, site.ID
}

// TestConsoleAggregatesShape proves each of the four console routes returns 200,
// the exact `{ "<plural>": [...] }` wrapper the FE reads, and rows DERIVED from
// the real project/app/deploy/build records (no fabrication).
func TestConsoleAggregatesShape(t *testing.T) {
	s, app := mountConsole(t)
	seedConsoleFixture(t, s, "maxpower")

	// ── environments ── two deploy targets aggregated from the two apps.
	code, body := do(t, app, http.MethodGet, "/v1/environments", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("environments want 200, got %d (%s)", code, body)
	}
	var envs struct {
		Environments []environmentView `json:"environments"`
	}
	if err := json.Unmarshal(body, &envs); err != nil {
		t.Fatalf("environments decode: %v (%s)", err, body)
	}
	if len(envs.Environments) != 2 {
		t.Fatalf("want 2 environments, got %d: %s", len(envs.Environments), body)
	}
	byEnv := map[string]environmentView{}
	for _, e := range envs.Environments {
		byEnv[e.ID] = e
	}
	prod, ok := byEnv["production"]
	if !ok {
		t.Fatalf("expected a 'production' environment, got %s", body)
	}
	if prod.Type != "production" || prod.Status != "active" || len(prod.Services) != 1 || prod.Services[0] != "API" || prod.UpdatedAt == "" {
		t.Fatalf("production env derived wrong: %+v", prod)
	}
	if stg, ok := byEnv["staging"]; !ok || stg.Type != "staging" || stg.Status != "idle" {
		t.Fatalf("staging env derived wrong: %+v (present=%v)", stg, ok)
	}

	// ── pipelines ── one per app; status/timing from the latest deployment.
	code, body = do(t, app, http.MethodGet, "/v1/pipelines", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("pipelines want 200, got %d (%s)", code, body)
	}
	var pipes struct {
		Pipelines []pipelineView `json:"pipelines"`
	}
	_ = json.Unmarshal(body, &pipes)
	if len(pipes.Pipelines) != 2 {
		t.Fatalf("want 2 pipelines, got %d: %s", len(pipes.Pipelines), body)
	}
	pByID := map[string]pipelineView{}
	for _, p := range pipes.Pipelines {
		pByID[p.ID] = p
	}
	api := pByID["app_maxpower_api"]
	if api.Repo != "ghcr.io/hanzoai/nginx" || api.Status != "deploying" || api.LastRun == "" || api.Duration != "30s" {
		t.Fatalf("api pipeline derived wrong: %+v", api)
	}
	site := pByID["app_maxpower_site"]
	if site.Repo != "https://github.com/maxpower/site" || site.Status != "building" || site.Duration != "" {
		t.Fatalf("site pipeline derived wrong: %+v", site)
	}

	// ── builds ── the REAL build record, joined to app repo + deployment commit.
	code, body = do(t, app, http.MethodGet, "/v1/builds", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("builds want 200, got %d (%s)", code, body)
	}
	var builds struct {
		Builds []buildView `json:"builds"`
	}
	_ = json.Unmarshal(body, &builds)
	if len(builds.Builds) != 1 {
		t.Fatalf("want 1 build, got %d: %s", len(builds.Builds), body)
	}
	b := builds.Builds[0]
	if b.ID != "bld_site_1" || b.Status != "building" || b.Repo != "https://github.com/maxpower/site" || b.Commit != "feature/logi" || b.StartedAt == "" || b.Duration != "" {
		t.Fatalf("build derived wrong: %+v", b)
	}

	// ── releases ── only the applied ("deploying") version, not the "building" one.
	code, body = do(t, app, http.MethodGet, "/v1/releases", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("releases want 200, got %d (%s)", code, body)
	}
	var rels struct {
		Releases []releaseView `json:"releases"`
	}
	_ = json.Unmarshal(body, &rels)
	if len(rels.Releases) != 1 {
		t.Fatalf("want 1 release (deploying only), got %d: %s", len(rels.Releases), body)
	}
	r := rels.Releases[0]
	if r.ID != "dep_api_1" || r.Name != "API" || r.Version != "1.27" || r.Environment != "production" || r.Status != "deploying" || r.ReleasedAt == "" {
		t.Fatalf("release derived wrong: %+v", r)
	}
}

// TestConsoleAggregatesOrgIsolation proves a second org sees ONLY its own fleet:
// maxpower's seeded records never leak into acme's aggregates (empty, not a
// cross-tenant read), and acme's own records are isolated the same way.
func TestConsoleAggregatesOrgIsolation(t *testing.T) {
	s, app := mountConsole(t)
	seedConsoleFixture(t, s, "maxpower")

	for _, path := range []string{"/v1/environments", "/v1/pipelines", "/v1/builds", "/v1/releases"} {
		code, body := do(t, app, http.MethodGet, path, "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("acme %s want 200, got %d (%s)", path, code, body)
		}
		// The wrapper key must be present with an EMPTY array — acme has no fleet,
		// and no maxpower row may appear.
		var wrap map[string][]json.RawMessage
		if err := json.Unmarshal(body, &wrap); err != nil {
			t.Fatalf("acme %s decode: %v (%s)", path, err, body)
		}
		var total int
		for _, v := range wrap {
			total += len(v)
		}
		if total != 0 {
			t.Fatalf("acme %s must be empty (org isolation), got %s", path, body)
		}
	}
}

// TestConsoleAggregatesForgeableOrgRefused proves the validated-principal gate
// applies to the console routes too: X-Org-Id with NO X-User-Id (the forgeable
// Phase-1 path) is refused 403 on every aggregate.
func TestConsoleAggregatesForgeableOrgRefused(t *testing.T) {
	_, app := mountConsole(t)
	for _, path := range []string{"/v1/environments", "/v1/pipelines", "/v1/builds", "/v1/releases"} {
		if code, _ := doAs(t, app, http.MethodGet, path, "victim", "", nil); code != http.StatusForbidden {
			t.Fatalf("forged org (no principal) %s want 403, got %d", path, code)
		}
	}
}
