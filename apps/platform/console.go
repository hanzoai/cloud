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
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// ── views (the exact JSON the console FE modules consume) ─────────────────────

// environmentView matches console EnvironmentsModule `Environment`.
type environmentView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
	Services  []string `json:"services"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
}

// pipelineView matches console PipelinesModule `Pipeline`.
type pipelineView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Repo     string `json:"repo,omitempty"`
	Status   string `json:"status"`
	LastRun  string `json:"lastRun,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// buildView matches console BuildsModule `Build`.
type buildView struct {
	ID        string `json:"id"`
	Repo      string `json:"repo,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Status    string `json:"status"`
	StartedAt string `json:"startedAt,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

// releaseView matches console ReleasesModule `Release`.
type releaseView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Environment string `json:"environment,omitempty"`
	Status      string `json:"status"`
	ReleasedAt  string `json:"releasedAt,omitempty"`
}

// ── typed ops ────────────────────────────────────────────────────────────────

// consoleOps binds the service to the console read ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listEnvironments),
// which is also the only bound form cmd/zipdoc can lift prose from.
type consoleOps struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// request recovers the live request and the VALIDATED org behind it. These reads
// resolve their tenant exactly as the rest of platform does — tenant(), which
// requires a validated principal and never reads an org from an input — so the
// request is what crosses the typed seam, carried there by cloud.Bridge. Off the
// HTTP path there is no request and therefore no attested caller, which refuses.
func (o consoleOps) request(ctx context.Context) (*zip.Ctx, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	org, ok := tenant(o.s, c)
	if !ok {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	return c, org, nil
}

// environmentList is the org's distinct deploy targets.
type environmentList struct {
	// Environments is one row per environment any of the org's apps targets.
	Environments []environmentView `json:"environments"`
}

// pipelineList is the org's apps projected as CI/CD pipelines.
type pipelineList struct {
	// Pipelines is one row per app, carrying its latest deployment run.
	Pipelines []pipelineView `json:"pipelines"`
}

// buildList is the org's recorded builds.
type buildList struct {
	// Builds is the org's real build records, joined to their app and commit.
	Builds []buildView `json:"builds"`
}

// releaseList is the org's released versions.
type releaseList struct {
	// Releases is one row per deployment that actually reached the cluster.
	Releases []releaseView `json:"releases"`
}

// ── environments ─────────────────────────────────────────────────────────────

// listEnvironments aggregates the org's apps into their deploy targets.
// An environment is not a standalone record — it is the Application.Environment
// scope — so this is list-only, and a new environment appears the moment an app
// targets it.
func (o consoleOps) listEnvironments(ctx context.Context, _ *noInput) (*environmentList, error) {
	s := o.s
	_, org, err := o.request(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := s.State.store.ListAllApplications(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
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
	return &environmentList{Environments: out}, nil
}

// ── pipelines ────────────────────────────────────────────────────────────────

// listPipelines projects each of the org's apps as a CI/CD pipeline.
// A row carries the app's build source (repo or image) and the status and timing
// of its latest deployment run.
// There is one pipeline per app — a pipeline is created by creating an app — so
// this is list-only.
func (o consoleOps) listPipelines(ctx context.Context, _ *noInput) (*pipelineList, error) {
	s := o.s
	_, org, err := o.request(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := s.State.store.ListAllApplications(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	deps, err := s.State.store.ListDeploymentsByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
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
	return &pipelineList{Pipelines: out}, nil
}

// ── builds ───────────────────────────────────────────────────────────────────

// listBuilds returns the org's recorded BuildKit builds, newest first.
// Each row is joined to its app for the source repo and to its deployment for
// the commit.
// A build is triggered by deploying a git-source app — the ONE build trigger —
// so this is list-only, and an org that has built nothing gets an empty list
// rather than a fabricated one.
func (o consoleOps) listBuilds(ctx context.Context, _ *noInput) (*buildList, error) {
	s := o.s
	c, org, err := o.request(ctx)
	if err != nil {
		return nil, err
	}
	builds, err := s.State.store.ListBuildsByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list builds: %v", err)
	}
	appByID, err := appIndex(s, c, org)
	if err != nil {
		return nil, err
	}
	depByID, err := deploymentIndex(s, c, org)
	if err != nil {
		return nil, err
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
	return &buildList{Builds: out}, nil
}

// ── releases ─────────────────────────────────────────────────────────────────

// listReleases returns the org's released versions.
// A release is a deployment that actually reached the cluster (deploying or
// live), so a build that never rolled out is not one.
// Releases are created by deploying — the ONE deploy path — so this is
// list-only, and it reports real deployment history and nothing else.
func (o consoleOps) listReleases(ctx context.Context, _ *noInput) (*releaseList, error) {
	s := o.s
	c, org, err := o.request(ctx)
	if err != nil {
		return nil, err
	}
	deps, err := s.State.store.ListDeploymentsByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	appByID, err := appIndex(s, c, org)
	if err != nil {
		return nil, err
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
	return &releaseList{Releases: out}, nil
}

// ── join indexes (all org-scoped) ────────────────────────────────────────────

func appIndex(s *cloud.Service[state], c *zip.Ctx, org string) (map[string]Application, error) {
	apps, err := s.State.store.ListAllApplications(c.Context(), org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	m := make(map[string]Application, len(apps))
	for _, a := range apps {
		m[a.ID] = a
	}
	return m, nil
}

func deploymentIndex(s *cloud.Service[state], c *zip.Ctx, org string) (map[string]Deployment, error) {
	deps, err := s.State.store.ListDeploymentsByOrg(c.Context(), org)
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
