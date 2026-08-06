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

// ── rows and boards (the exact JSON the console FE modules consume) ───────────
//
// The four row types are named `<thing>Row` rather than `<thing>View`, which is
// what they were called while they were invisible to the document. Publishing them
// puts them in the FLEET's schema namespace, which is flat and single-valued: one
// name, one shape, wherever two apps meet (openapi.Weave). `pipelineView` is
// already published by apps/world (a news pipeline) and `buildView` by apps/agents
// (a provenance build), and neither is this. The name that was not yet published is
// the one that yields — the same rule apps/templates followed when its Template
// became a StarterKit — and it yields to the word this file already uses for these
// values: every one of them is a ROW on a console page.
//
// The JSON is untouched: the FE normalizers read r.environments / r.pipelines /
// r.builds / r.releases and the same field names underneath, which is the contract.

// environmentRow matches console EnvironmentsModule `Environment`.
type environmentRow struct {
	// ID is the environment's name, which is also its identity — an environment
	// is derived from the apps that target it, so it has no id of its own.
	ID string `json:"id"`
	// Name is the environment's name as an app declared it.
	Name string `json:"name"`
	// Type buckets the name for display: production, staging, development or
	// custom.
	Type string `json:"type"`
	// Status rolls up the real states of this environment's apps: degraded,
	// active, idle or empty.
	Status string `json:"status"`
	// Services are the apps that target this environment, by name.
	Services []string `json:"services"`
	// UpdatedAt is when any of them last changed, RFC3339 UTC; empty when unset.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// pipelineRow matches console PipelinesModule `Pipeline`.
type pipelineRow struct {
	// ID is the application id — one pipeline is one application.
	ID string `json:"id"`
	// Name is the application's name.
	Name string `json:"name"`
	// Repo is the git repo or image the pipeline builds from.
	Repo string `json:"repo,omitempty"`
	// Status is the latest deployment's status, or the app's when it has none.
	Status string `json:"status"`
	// LastRun is when the most recent deployment started, RFC3339 UTC.
	LastRun string `json:"lastRun,omitempty"`
	// Duration is how long that run took; empty while it is still queued or
	// building.
	Duration string `json:"duration,omitempty"`
}

// buildRow matches console BuildsModule `Build`.
type buildRow struct {
	// ID is the build record's id.
	ID string `json:"id"`
	// Repo is the repo the build built, or the image it produced.
	Repo string `json:"repo,omitempty"`
	// Commit is the short git ref the build pinned.
	Commit string `json:"commit,omitempty"`
	// Status is the build's real state: queued, building, succeeded or failed.
	Status string `json:"status"`
	// StartedAt is when the build was recorded, RFC3339 UTC.
	StartedAt string `json:"startedAt,omitempty"`
	// Duration is the wall time of a TERMINAL build; empty while it still runs.
	Duration string `json:"duration,omitempty"`
}

// releaseRow matches console ReleasesModule `Release`.
type releaseRow struct {
	// ID is the deployment's id — a release IS a deployment that reached the
	// cluster.
	ID string `json:"id"`
	// Name is the application the release belongs to.
	Name string `json:"name"`
	// Version is the released image tag, or v<n> when the image carries none.
	Version string `json:"version,omitempty"`
	// Environment is the deploy target the application names.
	Environment string `json:"environment,omitempty"`
	// Status is deploying or live — the two states that mean released.
	Status string `json:"status"`
	// ReleasedAt is when the deployment last changed, RFC3339 UTC.
	ReleasedAt string `json:"releasedAt,omitempty"`
}

// environmentBoard is the Environments page.
type environmentBoard struct {
	// Environments are the org's deploy targets, in first-seen order.
	Environments []environmentRow `json:"environments"`
}

// pipelineBoard is the Pipelines page.
type pipelineBoard struct {
	// Pipelines are one per application in the caller's org.
	Pipelines []pipelineRow `json:"pipelines"`
}

// buildBoard is the Builds page.
type buildBoard struct {
	// Builds are the org's real BuildKit build records, newest first.
	Builds []buildRow `json:"builds"`
}

// releaseBoard is the Releases page.
type releaseBoard struct {
	// Releases are the deployments that genuinely reached the cluster.
	Releases []releaseRow `json:"releases"`
}

// ── environments ─────────────────────────────────────────────────────────────

// listEnvironments returns your deploy targets, and what is running on each.
//
// It returns the org's environments — the distinct deploy targets its applications
// name, `production` for anything that names none — each aggregating the apps that
// target it, a rolled-up status and when it last changed.
//
// An environment is DERIVED, not stored: there is nothing to create or delete here,
// and an environment exists exactly as long as an app points at it. Requires a
// validated principal; 403 without one.
func (o ops) listEnvironments(ctx context.Context, _ *noInput) (*environmentBoard, error) {
	s := o.s
	_, org, err := o.caller(ctx)
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

	out := make([]environmentRow, 0, len(order))
	for _, env := range order {
		g := byEnv[env]
		out = append(out, environmentRow{
			ID:        env,
			Name:      env,
			Type:      classifyEnvType(env),
			Status:    envStatus(g.anyLive, g.anyError, len(g.services)),
			Services:  g.services,
			UpdatedAt: rfc3339(g.updated),
		})
	}
	return &environmentBoard{Environments: out}, nil
}

// ── pipelines ────────────────────────────────────────────────────────────────

// listPipelines returns one build-and-deploy pipeline per app, with its latest run.
//
// It returns one pipeline per application in the caller's org — its repo or image
// source, its current status, and when its most recent deployment ran and how long
// it took. A pipeline is a PROJECTION of an app plus its newest deployment, not a
// separate record: it comes into existence with the app and is triggered only
// through /deploy, never here. Requires a validated principal; 403 without one.
func (o ops) listPipelines(ctx context.Context, _ *noInput) (*pipelineBoard, error) {
	s := o.s
	_, org, err := o.caller(ctx)
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

	out := make([]pipelineRow, 0, len(apps))
	for _, a := range apps {
		v := pipelineRow{
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
	return &pipelineBoard{Pipelines: out}, nil
}

// ── builds ───────────────────────────────────────────────────────────────────

// listBuilds returns real build records for your org.
//
// It lists the org's BuildKit build records — the git build step behind a deploy —
// each with the repo it built, the short commit, its status, when it started and
// how long it took. These are real records or an honest empty list; a build appears
// here because one ran, never because a page needed a row. Builds are created only
// by /deploy and the push-to-deploy hook. Requires a validated principal; 403
// without one.
func (o ops) listBuilds(ctx context.Context, _ *noInput) (*buildBoard, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	builds, err := s.State.store.ListBuildsByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list builds: %v", err)
	}
	appByID, err := appIndex(s, ctx, org)
	if err != nil {
		return nil, err
	}
	depByID, err := deploymentIndex(s, ctx, org)
	if err != nil {
		return nil, err
	}

	out := make([]buildRow, 0, len(builds))
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
		out = append(out, buildRow{
			ID:        b.ID,
			Repo:      repo,
			Commit:    commit,
			Status:    b.Status,
			StartedAt: rfc3339(b.CreatedAt),
			Duration:  buildDuration(b.Status, b.CreatedAt, b.UpdatedAt),
		})
	}
	return &buildBoard{Builds: out}, nil
}

// ── releases ─────────────────────────────────────────────────────────────────

// listReleases returns the versions that actually reached the cluster.
//
// It lists the org's releases: the deployments that were genuinely applied to the
// cluster, with the app they belong to, their version, environment, status and when
// they were released. A deployment that failed or is still building is NOT a
// release and is excluded — reaching the cluster is what makes one. Requires a
// validated principal; 403 without one.
func (o ops) listReleases(ctx context.Context, _ *noInput) (*releaseBoard, error) {
	s := o.s
	_, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	deps, err := s.State.store.ListDeploymentsByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list deployments: %v", err)
	}
	appByID, err := appIndex(s, ctx, org)
	if err != nil {
		return nil, err
	}

	out := make([]releaseRow, 0, len(deps))
	for _, d := range deps {
		if !isReleased(d.Status) {
			continue // only versions that reached the cluster count as releases
		}
		name, env := d.ApplicationID, ""
		if a, has := appByID[d.ApplicationID]; has {
			name = firstNonEmpty(a.Name, a.Slug)
			env = a.Environment
		}
		out = append(out, releaseRow{
			ID:          d.ID,
			Name:        name,
			Version:     releaseVersion(d),
			Environment: env,
			Status:      d.Status,
			ReleasedAt:  rfc3339(d.UpdatedAt),
		})
	}
	return &releaseBoard{Releases: out}, nil
}

// ── join indexes (all org-scoped) ────────────────────────────────────────────

func appIndex(s *cloud.Service[state], ctx context.Context, org string) (map[string]Application, error) {
	apps, err := s.State.store.ListAllApplications(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list apps: %v", err)
	}
	m := make(map[string]Application, len(apps))
	for _, a := range apps {
		m[a.ID] = a
	}
	return m, nil
}

func deploymentIndex(s *cloud.Service[state], ctx context.Context, org string) (map[string]Deployment, error) {
	deps, err := s.State.store.ListDeploymentsByOrg(ctx, org)
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
