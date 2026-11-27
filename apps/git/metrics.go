package git

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// metrics.go — the estate's open source adoption, as figures the forge holds
// (#456): GitHub stars, forks and traffic, registry downloads and pulls, one
// figure per entity, metric and UTC day.
//
//	POST /v1/admin/git/metrics   the collector's daily batch, upserted whole
//	GET  /v1/admin/git/metrics   series over a window, by ecosystem, org or entity
//
// Both are SuperAdmin's, in the operator's address family: the figures include
// private repositories' traffic. How the collector authenticates (an API key is
// never SuperAdmin) is decided with the collector, #456 phase 2. The rules a figure obeys and the rollups
// are metrics_rules.go; the table is metrics_store.go. Nothing here fetches a
// figure: the collector reads GitHub and the registries and posts what it read.

// openMetrics opens the platform's one forge metrics store through the one
// opener, where it lives now (hanzo-sql when the DSN is set: manifest.SQLStores).
func openMetrics(dir string) (*metricsStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), metricsSubsystem, dir, newMetricsStore)
}

// metricsHolder opens the store on first use, as keyHolder does the SSH key
// registry. A store that does not open leaves git serving everything else: the
// metrics ops answer 503, repo rows go without their packages, and the next use
// tries again.
type metricsHolder struct {
	mu sync.Mutex
	m  *metricsStore
}

func (h *metricsHolder) get(dataDir string) (*metricsStore, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m != nil {
		return h.m, nil
	}
	m, err := openMetrics(dataDir)
	if err != nil {
		return nil, err
	}
	h.m = m
	return m, nil
}

func (h *metricsHolder) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		return nil
	}
	err := h.m.Close()
	h.m = nil
	return err
}

// metricsOf is the store for an op: a store that does not open is a 503, and
// the cause goes to the log rather than to the caller.
func metricsOf(s *cloud.Service[state]) (*metricsStore, error) {
	m, err := s.State.metrics.get(s.State.dataDir)
	if err != nil {
		s.Log.Warn("forge metrics store did not open", "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "forge metrics are not available")
	}
	return m, nil
}

// metricRowIn is one figure as the collector posts it.
type metricRowIn struct {
	// Date is the UTC day the figure is for, YYYY-MM-DD. At most tomorrow.
	Date string `json:"date"`
	// Kind is what the figure is about: "repo", "package" or "org".
	Kind string `json:"kind"`
	// Ecosystem is where the figure was read: "github", "npm", "pypi", "crates"
	// or "dockerhub".
	Ecosystem string `json:"ecosystem"`
	// Org is the GitHub org the entity belongs to, the per-org breakdown's key.
	Org string `json:"org"`
	// Name is the entity: owner/repo on GitHub, the package's name on a registry.
	Name string `json:"name"`
	// Metric is one of stars, forks, open_issues, watchers, downloads_total and
	// pulls_total (running totals), or downloads, pulls, release_downloads,
	// clones, views, unique_clones and unique_views (that day's count).
	Metric string `json:"metric"`
	// Value is the figure. Never negative.
	Value int64 `json:"value"`
}

// packageIn registers a package as published from a GitHub repository.
type packageIn struct {
	// Ecosystem is the package's registry: "npm", "pypi", "crates" or "dockerhub".
	Ecosystem string `json:"ecosystem"`
	// Name is the package's name on its registry.
	Name string `json:"name"`
	// Upstream is the GitHub repository it is published from, in any spelling
	// of its address; it is stored as github.com/<owner>/<repo>.
	Upstream string `json:"upstream"`
}

// putMetricsIn is one batch: figures, package registrations, or both.
type putMetricsIn struct {
	// Rows are the figures, at most 5000. A figure already held for the same
	// date, ecosystem, name and metric is replaced.
	Rows []metricRowIn `json:"rows"`
	// Packages are package registrations, at most 5000. A package already
	// registered moves to the upstream given.
	Packages []packageIn `json:"packages"`
}

// putMetricsOut says what the batch wrote.
type putMetricsOut struct {
	// Written is the number of figures written.
	Written int `json:"written"`
	// Packages is the number of packages registered.
	Packages int `json:"packages"`
}

// getMetricsIn selects figures and how they roll up.
type getMetricsIn struct {
	// Ecosystem keeps one ecosystem's figures; empty keeps every one.
	Ecosystem string `json:"ecosystem"`
	// Org keeps one GitHub org's figures; empty keeps every org's.
	Org string `json:"org"`
	// Name keeps one entity's figures; empty keeps every entity's.
	Name string `json:"name"`
	// Metric keeps one metric; empty keeps every metric.
	Metric string `json:"metric"`
	// From is the window's first day, YYYY-MM-DD. Empty means 90 days before to.
	From string `json:"from"`
	// To is the window's last day, YYYY-MM-DD. Empty means today (UTC).
	To string `json:"to"`
	// Granularity is "day" (the default, up to 400 days) or "month" (up to 3660).
	Granularity string `json:"granularity"`
	// By groups the series: "name" (the default, one per entity), "org" or
	// "ecosystem".
	By string `json:"by"`
}

// getMetricsOut is the window read and its series.
type getMetricsOut struct {
	// From is the window's first day.
	From string `json:"from"`
	// To is the window's last day.
	To string `json:"to"`
	// Granularity is the points' granularity, "day" or "month".
	Granularity string `json:"granularity"`
	// By is what the series are grouped by.
	By string `json:"by"`
	// Series are one per group and metric, ordered by key then metric.
	Series []metricSeries `json:"series"`
}

// putMetrics writes one batch of the estate's open source figures and package
// registrations, whole or not at all. A figure already held for the same date,
// ecosystem, name and metric is replaced, so re-sending a window is safe.
// SuperAdmin only.
//
// Example: {"rows": [{"date": "2026-11-09", "kind": "repo", "ecosystem": "github", "org": "hanzoai", "name": "hanzoai/ui", "metric": "stars", "value": 412}], "packages": [{"ecosystem": "npm", "name": "@hanzo/ui", "upstream": "github.com/hanzoai/ui"}]}
func (o ops) putMetrics(ctx context.Context, in *putMetricsIn) (*putMetricsOut, error) {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Super.Refusal()
	}
	m, err := metricsOf(o.s)
	if err != nil {
		return nil, err
	}
	rows := make([]metricRow, len(in.Rows))
	for i, r := range in.Rows {
		rows[i] = metricRow{Date: r.Date, Kind: r.Kind, Ecosystem: r.Ecosystem, Org: r.Org, Name: r.Name, Metric: r.Metric, Value: r.Value}
	}
	pkgs := make([]packageRef, len(in.Packages))
	for i, p := range in.Packages {
		pkgs[i] = packageRef{Ecosystem: p.Ecosystem, Name: p.Name, Upstream: p.Upstream}
	}
	rows, pkgs, err = checkBatch(rows, pkgs, time.Now())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if err := m.put(ctx, rows, pkgs); err != nil {
		return nil, internalErr(err)
	}
	return &putMetricsOut{Written: len(rows), Packages: len(pkgs)}, nil
}

// getMetrics answers the estate's open source figures over a window as series,
// one per group and metric: by entity, org or ecosystem, per day or per month.
// A running total (stars) takes each entity's last day in a month, a daily count
// (downloads) sums its days, and a group sums its entities. A window or a match
// too wide to answer whole is refused, never cut. SuperAdmin only.
//
// Example: {"metric": "downloads", "granularity": "month", "by": "ecosystem", "from": "2025-05-01"}
func (o ops) getMetrics(ctx context.Context, in *getMetricsIn) (*getMetricsOut, error) {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Super.Refusal()
	}
	if err := dialect.Text(in.Ecosystem, in.Org, in.Name, in.Metric, in.From, in.To, in.Granularity, in.By); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	w, err := checkWindow(strings.TrimSpace(in.From), strings.TrimSpace(in.To),
		strings.TrimSpace(in.Granularity), strings.TrimSpace(in.By), time.Now())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	m, err := metricsOf(o.s)
	if err != nil {
		return nil, err
	}
	rows, more, err := m.query(ctx, metricQuery{
		Ecosystem: strings.TrimSpace(in.Ecosystem), Org: strings.TrimSpace(in.Org),
		Name: strings.TrimSpace(in.Name), Metric: strings.TrimSpace(in.Metric),
		From: w.From, To: w.To,
	}, maxRows)
	if err != nil {
		return nil, internalErr(err)
	}
	if more {
		return nil, zip.Errorf(http.StatusBadRequest,
			"more than %d figures match: narrow the query (metric, ecosystem, org, name or a shorter window)", maxRows)
	}
	series := rollup(rows, w.Granularity, w.By)
	if len(series) > maxSeries {
		return nil, zip.Errorf(http.StatusBadRequest,
			"%d series match, at most %d: narrow the query or group by org or ecosystem", len(series), maxSeries)
	}
	return &getMetricsOut{From: w.From, To: w.To, Granularity: w.Granularity, By: w.By, Series: series}, nil
}

// packageRefView is one package a repository publishes.
type packageRefView struct {
	// Ecosystem is the package's registry: "npm", "pypi", "crates" or "dockerhub".
	Ecosystem string `json:"ecosystem"`
	// Name is the package's name on its registry.
	Name string `json:"name"`
}

// withPackages fills each view's Packages from the registry, by its Upstream, in
// one read. The registry is not what a repo list is for: a lookup that fails is
// logged and the views go without packages.
func withPackages(ctx context.Context, s *cloud.Service[state], views []repoView) []repoView {
	var ups []string
	for _, v := range views {
		if v.Upstream != "" {
			ups = append(ups, v.Upstream)
		}
	}
	if len(ups) == 0 {
		return views
	}
	m, err := s.State.metrics.get(s.State.dataDir)
	if err != nil {
		s.Log.Warn("forge metrics store did not open; repos answered without packages", "err", err)
		return views
	}
	found, err := m.packagesFor(ctx, ups)
	if err != nil {
		s.Log.Warn("forge packages lookup failed; repos answered without packages", "err", err)
		return views
	}
	for i, v := range views {
		for _, p := range found[strings.ToLower(v.Upstream)] {
			views[i].Packages = append(views[i].Packages, packageRefView{Ecosystem: p.Ecosystem, Name: p.Name})
		}
	}
	return views
}
