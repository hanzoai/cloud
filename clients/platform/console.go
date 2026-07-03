// console.go — the flat, top-level console aggregates that the Hanzo Cloud
// Console's Environments / Pipelines / Builds / Releases pages render. They are
// NOT a new data model: every row is DERIVED from the SAME per-org project /
// application / deployment / build records the /v1/platform surface already owns
// (platform.go, deploy.go, store.go). One store, one deploy mechanic, four
// read-only projections:
//
//   - environment → a deploy target: the distinct Application.Environment values
//     across the org's apps, each aggregating the apps that target it.
//   - pipeline    → an app's build/deploy configuration + its latest run
//     (one pipeline per application).
//   - build       → a REAL arcd BuildKit build record (platform_builds); the git
//     build step of a deploy. Never fabricated — real records or an honest empty.
//   - release     → a deployment that was actually applied to the cluster
//     (status deploying|live): a released image tag on an app/environment.
//
// All four are GET-only. A pipeline/build/release is CREATED only through the ONE
// existing write path — POST /v1/platform/projects/{p}/apps (create app) and
// .../deploy (trigger a build+deploy) — so there is exactly one way to make them,
// never a duplicate trigger here. Environments are a scope derived from apps, not
// a standalone record, so there is nothing to create/delete independently either.
//
// Every handler is org-scoped through the SAME validated-principal gate as the
// rest of the platform surface (s.tenant → requires c.User(); org is the only
// tenancy key). The response is a `{ "<plural>": [...] }` object — the exact
// shape the console FE normalizers read (r.environments / r.pipelines / r.builds
// / r.releases).
package platform

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// ── views (the exact JSON the console FE modules consume) ─────────────────────

// environmentView matches console2 EnvironmentsModule `Environment`.
type environmentView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
	Services  []string `json:"services"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
}

// pipelineView matches console2 PipelinesModule `Pipeline`.
type pipelineView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Repo     string `json:"repo,omitempty"`
	Status   string `json:"status"`
	LastRun  string `json:"lastRun,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// buildView matches console2 BuildsModule `Build`.
type buildView struct {
	ID        string `json:"id"`
	Repo      string `json:"repo,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

// releaseView matches console2 ReleasesModule `Release`.
type releaseView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Environment string `json:"environment,omitempty"`
	Status      string `json:"status"`
	ReleasedAt  string `json:"releasedAt,omitempty"`
}

// ── environments ─────────────────────────────────────────────────────────────

// listEnvironments aggregates the org's apps into their distinct deploy targets.
// An environment is not a standalone record — it is the Application.Environment
// scope — so this is list-only; a new environment appears the moment an app
// targets it (create an app with that environment via POST .../apps).
func (s *svc) listEnvironments(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	apps, err := s.store.ListAllApplications(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}

	type agg struct {
		services []string
		anyLive  bool
		anyError bool
		updated  int64
	}
	order := make([]string, 0, len(apps))
	byEnv := map[string]*agg{}
	for _, a := range apps {
		env := firstNonEmpty(a.Environment, "production")
		g := byEnv[env]
		if g == nil {
			g = &agg{}
			byEnv[env] = g
			order = append(order, env)
		}
		g.services = append(g.services, firstNonEmpty(a.Name, a.Slug))
		switch a.Status {
		case "live", "deploying":
			g.anyLive = true
		case "error":
			g.anyError = true
		}
		if a.UpdatedAt > g.updated {
			g.updated = a.UpdatedAt
		}
	}

	out := make([]environmentView, 0, len(order))
	for _, env := range order {
		g := byEnv[env]
		out = append(out, environmentView{
			ID:        env,
			Name:      env,
			Type:      classifyEnvType(env),
			Status:    envStatus(g.anyLive, g.anyError, len(g.services)),
			Services:  g.services,
			UpdatedAt: rfc3339(g.updated),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"environments": out})
}

// ── pipelines ────────────────────────────────────────────────────────────────

// listPipelines projects each app as a CI/CD pipeline: its build/deploy config
// (source repo/image) plus the status/timing of its latest deployment run. One
// pipeline per app — a pipeline is created by creating an app, so this is
// list-only.
func (s *svc) listPipelines(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	apps, err := s.store.ListAllApplications(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	deps, err := s.store.ListDeploymentsByOrg(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	// Latest deployment per app (deps are created_at DESC, so first seen is newest).
	latest := make(map[string]Deployment, len(apps))
	for _, d := range deps {
		if _, seen := latest[d.ApplicationID]; !seen {
			latest[d.ApplicationID] = d
		}
	}

	out := make([]pipelineView, 0, len(apps))
	for _, a := range apps {
		v := pipelineView{
			ID:     a.ID,
			Name:   firstNonEmpty(a.Name, a.Slug),
			Repo:   firstNonEmpty(a.RepoURL, a.ImageRepo),
			Status: a.Status,
		}
		if d, has := latest[a.ID]; has {
			v.Status = d.Status
			v.LastRun = rfc3339(d.CreatedAt)
			v.Duration = runDuration(d.Status, d.CreatedAt, d.UpdatedAt)
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, map[string]any{"pipelines": out})
}

// ── builds ───────────────────────────────────────────────────────────────────

// listBuilds returns the org's REAL arcd BuildKit build records (platform_builds),
// joined to their app (source repo) and deployment (commit). Builds are triggered
// by deploying a git-source app (POST .../deploy) — the ONE build trigger — so
// this is list-only. Real records or an honest empty list; never fabricated.
func (s *svc) listBuilds(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	builds, err := s.store.ListBuildsByOrg(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list builds: %v", err)
	}
	appByID, err := s.appIndex(c, org)
	if err != nil {
		return err
	}
	depByID, err := s.deploymentIndex(c, org)
	if err != nil {
		return err
	}

	out := make([]buildView, 0, len(builds))
	for _, b := range builds {
		repo := ""
		if a, has := appByID[b.ApplicationID]; has {
			repo = firstNonEmpty(a.RepoURL, a.ImageRepo)
		}
		repo = firstNonEmpty(repo, b.Image)
		commit := ""
		if d, has := depByID[b.DeploymentID]; has {
			commit = shortCommit(d.Commit)
		}
		out = append(out, buildView{
			ID:        b.ID,
			Repo:      repo,
			Commit:    commit,
			Status:    b.Status,
			StartedAt: rfc3339(b.CreatedAt),
			Duration:  buildDuration(b.Status, b.CreatedAt, b.UpdatedAt),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"builds": out})
}

// ── releases ─────────────────────────────────────────────────────────────────

// listReleases returns the org's released versions: deployments that were
// actually applied to the cluster (status deploying|live), i.e. a released image
// tag on an app/environment. A release is created by a successful deploy — the
// ONE deploy path — so this is list-only. Real deployment history only.
func (s *svc) listReleases(c *zip.Ctx) error {
	org, ok := s.tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deps, err := s.store.ListDeploymentsByOrg(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	appByID, err := s.appIndex(c, org)
	if err != nil {
		return err
	}

	out := make([]releaseView, 0, len(deps))
	for _, d := range deps {
		if !isReleased(d.Status) {
			continue // only versions that reached the cluster count as releases
		}
		name, env := d.ApplicationID, ""
		if a, has := appByID[d.ApplicationID]; has {
			name = firstNonEmpty(a.Name, a.Slug)
			env = a.Environment
		}
		out = append(out, releaseView{
			ID:          d.ID,
			Name:        name,
			Version:     releaseVersion(d),
			Environment: env,
			Status:      d.Status,
			ReleasedAt:  rfc3339(d.UpdatedAt),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"releases": out})
}

// ── join indexes (all org-scoped) ────────────────────────────────────────────

func (s *svc) appIndex(c *zip.Ctx, org string) (map[string]Application, error) {
	apps, err := s.store.ListAllApplications(c.Context(), org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	m := make(map[string]Application, len(apps))
	for _, a := range apps {
		m[a.ID] = a
	}
	return m, nil
}

func (s *svc) deploymentIndex(c *zip.Ctx, org string) (map[string]Deployment, error) {
	deps, err := s.store.ListDeploymentsByOrg(c.Context(), org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	m := make(map[string]Deployment, len(deps))
	for _, d := range deps {
		m[d.ID] = d
	}
	return m, nil
}

// ── derivation helpers (pure functions of the real records) ──────────────────

// isReleased reports whether a deployment status means the version was applied to
// the cluster (a real release). building/error/superseded never released.
func isReleased(status string) bool { return status == "deploying" || status == "live" }

// classifyEnvType buckets a real environment name into a display type. It is a
// pure function of the app's own Environment value — a label, never a fabricated
// environment.
func classifyEnvType(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "production", "prod", "mainnet", "main", "live":
		return "production"
	case "staging", "stage", "preprod", "pre-prod", "testnet", "qa", "uat":
		return "staging"
	case "development", "dev", "devnet", "local", "preview", "sandbox":
		return "development"
	default:
		return "custom"
	}
}

// envStatus aggregates the real statuses of an environment's apps.
func envStatus(anyLive, anyError bool, services int) string {
	switch {
	case anyError:
		return "degraded"
	case anyLive:
		return "active"
	case services > 0:
		return "idle"
	default:
		return "empty"
	}
}

// releaseVersion is the released artifact version: the image tag when present,
// else the monotonic deploy version.
func releaseVersion(d Deployment) string {
	if strings.TrimSpace(d.Image) != "" {
		if _, tag := splitImageRef(d.Image); tag != "" {
			return tag
		}
	}
	return "v" + strconv.Itoa(d.Version)
}

// buildDuration is the wall time of a TERMINAL build; empty while it still runs.
func buildDuration(status string, start, end int64) string {
	switch status {
	case "succeeded", "failed":
		return humanDuration(end - start)
	default:
		return ""
	}
}

// runDuration is a pipeline run's wall time once past the build step; empty while
// queued/building.
func runDuration(status string, start, end int64) string {
	switch status {
	case "queued", "building", "":
		return ""
	default:
		return humanDuration(end - start)
	}
}

// shortCommit trims a git ref/sha to a compact display token.
func shortCommit(ref string) string {
	ref = strings.TrimSpace(ref)
	if len(ref) > 12 {
		return ref[:12]
	}
	return ref
}

// rfc3339 renders a unix timestamp as an RFC3339 string (what the FE's
// `new Date(...)` parses); empty when unset so the FE shows "—".
func rfc3339(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

// humanDuration formats a positive second count compactly ("12s", "3m4s",
// "2h5m"); empty for a non-positive span.
func humanDuration(secs int64) string {
	if secs <= 0 {
		return ""
	}
	switch {
	case secs < 60:
		return strconv.FormatInt(secs, 10) + "s"
	case secs < 3600:
		m, s := secs/60, secs%60
		if s == 0 {
			return strconv.FormatInt(m, 10) + "m"
		}
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		h, m := secs/3600, (secs%3600)/60
		if m == 0 {
			return strconv.FormatInt(h, 10) + "h"
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
}
