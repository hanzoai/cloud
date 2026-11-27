package git

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/namespace"
)

func metricsFor(t *testing.T, open sqltest.Open) *metricsStore {
	t.Helper()
	m, err := newMetricsStore(open(namespace.System(), metricsSubsystem))
	if err != nil {
		t.Fatalf("newMetricsStore: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// TestMetricsPutIsIdempotent: the collector re-sends GitHub's 14 days of traffic
// every run, so a re-post of the same (date, ecosystem, name, metric) replaces
// the value; it never adds a second row.
func TestMetricsPutIsIdempotent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		m := metricsFor(t, open)
		ctx := context.Background()
		r := metricRow{Date: "2026-11-01", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/ui", Metric: "stars", Value: 10}
		for range 2 {
			if err := m.put(ctx, []metricRow{r}, nil); err != nil {
				t.Fatal(err)
			}
		}
		r.Value, r.Org = 12, "hanzo-ai"
		if err := m.put(ctx, []metricRow{r}, nil); err != nil {
			t.Fatal(err)
		}
		got, more, err := m.query(ctx, metricQuery{}, 10)
		if err != nil || more {
			t.Fatalf("query: %v more=%v", err, more)
		}
		if len(got) != 1 || got[0].Value != 12 || got[0].Org != "hanzo-ai" {
			t.Fatalf("rows = %+v, want one row of 12 under hanzo-ai", got)
		}
	})
}

func TestMetricsQueryFilters(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		m := metricsFor(t, open)
		ctx := context.Background()
		rows := []metricRow{
			{Date: "2026-10-31", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/ui", Metric: "stars", Value: 9},
			{Date: "2026-11-01", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/ui", Metric: "stars", Value: 10},
			{Date: "2026-11-01", Kind: "repo", Ecosystem: "github", Org: "luxfi", Name: "luxfi/node", Metric: "stars", Value: 50},
			{Date: "2026-11-01", Kind: "package", Ecosystem: "npm", Org: "hanzoai", Name: "@hanzo/ui", Metric: "downloads", Value: 700},
		}
		if err := m.put(ctx, rows, nil); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			q    metricQuery
			want int
		}{
			{metricQuery{}, 4},
			{metricQuery{Org: "hanzoai"}, 3},
			{metricQuery{Ecosystem: "npm"}, 1},
			{metricQuery{Metric: "stars", From: "2026-11-01"}, 2},
			{metricQuery{Name: "hanzoai/ui", To: "2026-10-31"}, 1},
		} {
			got, _, err := m.query(ctx, c.q, 100)
			if err != nil || len(got) != c.want {
				t.Errorf("query %+v = %d rows (%v), want %d", c.q, len(got), err, c.want)
			}
		}
		// Ordered (ecosystem, name, metric, date), and the limit says there is more.
		got, more, err := m.query(ctx, metricQuery{}, 3)
		if err != nil || !more || len(got) != 3 || got[0].Name != "hanzoai/ui" || got[0].Date != "2026-10-31" {
			t.Fatalf("limited = %+v more=%v %v", got, more, err)
		}
	})
}

func TestPackagesForMatchesUpstreamCaseInsensitively(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		m := metricsFor(t, open)
		ctx := context.Background()
		pkgs := []packageRef{
			{Ecosystem: "npm", Name: "@hanzo/ui", Upstream: "github.com/hanzoai/ui"},
			{Ecosystem: "pypi", Name: "hanzo-ui", Upstream: "github.com/hanzoai/ui"},
			{Ecosystem: "crates", Name: "lux-node", Upstream: "github.com/luxfi/node"},
		}
		if err := m.put(ctx, nil, pkgs); err != nil {
			t.Fatal(err)
		}
		got, err := m.packagesFor(ctx, []string{"github.com/HanzoAI/ui", "github.com/none/here"})
		if err != nil {
			t.Fatal(err)
		}
		ui := got["github.com/hanzoai/ui"]
		if len(got) != 1 || len(ui) != 2 || ui[0].Name != "@hanzo/ui" || ui[1].Ecosystem != "pypi" {
			t.Fatalf("packagesFor = %+v", got)
		}
		// Re-registering a package moves it; it is not a second entry.
		if err := m.put(ctx, nil, []packageRef{{Ecosystem: "npm", Name: "@hanzo/ui", Upstream: "github.com/hanzoai/gui"}}); err != nil {
			t.Fatal(err)
		}
		got, _ = m.packagesFor(ctx, []string{"github.com/hanzoai/ui", "github.com/hanzoai/gui"})
		if len(got["github.com/hanzoai/ui"]) != 1 || len(got["github.com/hanzoai/gui"]) != 1 {
			t.Fatalf("after move = %+v", got)
		}
		if none, err := m.packagesFor(ctx, nil); err != nil || len(none) != 0 {
			t.Fatalf("empty input = %+v %v", none, err)
		}
	})
}

func TestLatestBeforeReturnsMostRecentPriorGauge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		m := metricsFor(t, open)
		ctx := context.Background()
		rows := []metricRow{
			{Date: "2026-11-17", Kind: "package", Ecosystem: "dockerhub", Org: "hanzoai", Name: "hanzoai/ui", Metric: "pulls_total", Value: 500},
			{Date: "2026-11-19", Kind: "package", Ecosystem: "dockerhub", Org: "hanzoai", Name: "hanzoai/ui", Metric: "pulls_total", Value: 700},
			{Date: "2026-11-20", Kind: "package", Ecosystem: "dockerhub", Org: "hanzoai", Name: "hanzoai/ui", Metric: "pulls_total", Value: 750},
			{Date: "2026-11-18", Kind: "package", Ecosystem: "dockerhub", Org: "hanzoai", Name: "hanzoai/cli", Metric: "pulls_total", Value: 40},
		}
		if err := m.put(ctx, rows, nil); err != nil {
			t.Fatal(err)
		}
		got, err := m.latestBefore(ctx, "dockerhub", "pulls_total", "2026-11-20")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got["hanzoai/ui"] != 700 || got["hanzoai/cli"] != 40 {
			t.Fatalf("latestBefore = %v, want ui=700 cli=40", got)
		}
		// Before the earliest date returns empty.
		empty, err := m.latestBefore(ctx, "dockerhub", "pulls_total", "2026-11-17")
		if err != nil || len(empty) != 0 {
			t.Fatalf("latestBefore earliest = %v, %v", empty, err)
		}
	})
}

func TestPurgeUnownedPackagesRemovesThirdPartyAndKeepsEstate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		m := metricsFor(t, open)
		ctx := context.Background()
		pkgs := []packageRef{
			{Ecosystem: "npm", Name: "@hanzo/ui", Upstream: "github.com/hanzoai/ui"},
			{Ecosystem: "npm", Name: "@hanzogui/core", Upstream: "github.com/hanzogui/gui"},
			{Ecosystem: "npm", Name: "hanzo-mcp", Upstream: "github.com/hanzoai/mcp"},
			{Ecosystem: "pypi", Name: "hanzoai", Upstream: "github.com/hanzoai/python-sdk"},
			{Ecosystem: "crates", Name: "luxfi", Upstream: "github.com/luxfi/node"},
			{Ecosystem: "npm", Name: "zen-sdk", Upstream: "github.com/zenlm/sdk"},
			// Bogus third-party packages from forked repos.
			{Ecosystem: "npm", Name: "postcss", Upstream: "github.com/hanzoai/postcss"},
			{Ecosystem: "npm", Name: "cookie", Upstream: "github.com/hanzoai/cookie"},
			{Ecosystem: "npm", Name: "express", Upstream: "github.com/hanzoai/express"},
			{Ecosystem: "npm", Name: "eslint", Upstream: "github.com/hanzoai/eslint"},
			{Ecosystem: "npm", Name: "prettier", Upstream: "github.com/hanzoai/prettier"},
			{Ecosystem: "npm", Name: "sharp", Upstream: "github.com/hanzoai/sharp"},
		}
		rows := []metricRow{
			{Date: "2026-11-19", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/ui", Metric: "stars", Value: 412},
			{Date: "2026-11-19", Kind: "package", Ecosystem: "npm", Org: "hanzoai", Name: "@hanzo/ui", Metric: "downloads", Value: 500},
			{Date: "2026-11-19", Kind: "package", Ecosystem: "npm", Org: "hanzoai", Name: "postcss", Metric: "downloads", Value: 75000000},
			{Date: "2026-11-19", Kind: "package", Ecosystem: "npm", Org: "hanzoai", Name: "cookie", Metric: "downloads", Value: 60000000},
			{Date: "2026-11-19", Kind: "package", Ecosystem: "npm", Org: "hanzoai", Name: "express", Metric: "downloads", Value: 40000000},
		}
		if err := m.put(ctx, rows, pkgs); err != nil {
			t.Fatal(err)
		}

		purgedPkgs, purgedMetrics, err := m.PurgeUnownedPackages(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if purgedPkgs != 6 || purgedMetrics != 3 {
			t.Fatalf("purged = (%d pkgs, %d metrics), want (6, 3)", purgedPkgs, purgedMetrics)
		}

		remPkgs, err := m.allPackages(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(remPkgs) != 6 {
			t.Fatalf("remaining packages = %+v, want 6 Hanzo estate packages", remPkgs)
		}
		for _, p := range remPkgs {
			if !isEstatePackageName(p.Ecosystem, p.Name) {
				t.Fatalf("unowned package survived purge: %+v", p)
			}
		}

		remRows, _, err := m.query(ctx, metricQuery{}, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(remRows) != 2 {
			t.Fatalf("remaining rows = %+v, want github stars + @hanzo/ui downloads", remRows)
		}
	})
}
