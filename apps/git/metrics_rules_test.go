package git

import (
	"strings"
	"testing"
	"time"
)

// TestRollupMonthGaugeTakesLastDayCountSums pins the two rollups: a gauge's
// month is the entity's LAST day in it (stars do not add up across days), a
// count's month is the sum of its days, and a group sums its entities after.
func TestRollupMonthGaugeTakesLastDayCountSums(t *testing.T) {
	rows := []metricRow{
		{Date: "2026-11-01", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/ui", Metric: "stars", Value: 10},
		{Date: "2026-11-20", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/ui", Metric: "stars", Value: 14},
		{Date: "2026-11-20", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/mcp", Metric: "stars", Value: 3},
		{Date: "2026-11-02", Kind: "package", Ecosystem: "npm", Org: "hanzoai", Name: "@hanzo/ui", Metric: "downloads", Value: 5},
		{Date: "2026-11-03", Kind: "package", Ecosystem: "npm", Org: "hanzoai", Name: "@hanzo/ui", Metric: "downloads", Value: 7},
	}

	byName := rollup(rows, "month", "name")
	want := map[string]int64{"github/hanzoai/ui stars": 14, "github/hanzoai/mcp stars": 3, "npm/@hanzo/ui downloads": 12}
	if len(byName) != len(want) {
		t.Fatalf("by name: %d series, want %d: %+v", len(byName), len(want), byName)
	}
	for _, s := range byName {
		k := s.Key + " " + s.Metric
		if len(s.Points) != 1 || s.Points[0].Date != "2026-11" || s.Points[0].Value != want[k] {
			t.Fatalf("%s = %+v, want 2026-11 = %d", k, s.Points, want[k])
		}
	}

	byOrg := rollup(rows, "month", "org")
	for _, s := range byOrg {
		if s.Key != "hanzoai" {
			t.Fatalf("by org key = %q", s.Key)
		}
		switch s.Metric {
		case "stars":
			if s.Kind != "gauge" || s.Points[0].Value != 17 {
				t.Fatalf("org stars = %+v, want gauge 14+3", s)
			}
		case "downloads":
			if s.Kind != "count" || s.Points[0].Value != 12 {
				t.Fatalf("org downloads = %+v, want count 12", s)
			}
		}
	}
}

// TestRollupDaySumsAcrossGroupInDateOrder: a day point is the group's sum that
// day, and points come back oldest first whatever order the rows arrived in.
func TestRollupDaySumsAcrossGroupInDateOrder(t *testing.T) {
	rows := []metricRow{
		{Date: "2026-11-02", Ecosystem: "npm", Org: "a", Name: "x", Metric: "downloads", Value: 4},
		{Date: "2026-11-01", Ecosystem: "npm", Org: "a", Name: "x", Metric: "downloads", Value: 1},
		{Date: "2026-11-01", Ecosystem: "pypi", Org: "b", Name: "y", Metric: "downloads", Value: 2},
	}
	got := rollup(rows, "day", "ecosystem")
	if len(got) != 2 || got[0].Key != "npm" || got[1].Key != "pypi" {
		t.Fatalf("series = %+v", got)
	}
	if p := got[0].Points; len(p) != 2 || p[0].Date != "2026-11-01" || p[0].Value != 1 || p[1].Value != 4 {
		t.Fatalf("npm points = %+v", p)
	}
}

func TestNormUpstream(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"https://github.com/hanzoai/ui.git", "github.com/hanzoai/ui", true},
		{"git@github.com:hanzoai/ui.git", "github.com/hanzoai/ui", true},
		{"github.com/HanzoAI/UI/", "github.com/HanzoAI/UI", true},
		{"http://www.github.com/luxfi/node", "github.com/luxfi/node", true},
		{"https://gitlab.com/hanzoai/ui", "", false},
		{"https://tok@github.com/hanzoai/ui", "", false},
		{"https://x:y@github.com/hanzoai/ui", "", false},
		{"github.com/hanzoai", "", false},
		{"github.com/hanzoai/ui/tree/main", "", false},
		{"", "", false},
	} {
		got, ok := normUpstream(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("normUpstream(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestCheckBatchNamesTheBadRow: one bad row refuses the whole batch, and the
// refusal says which row and why, so a collector can fix it.
func TestCheckBatchNamesTheBadRow(t *testing.T) {
	today := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)
	good := metricRow{Date: "2026-11-10", Kind: "repo", Ecosystem: "github", Org: "hanzoai", Name: "hanzoai/ui", Metric: "stars", Value: 1}
	for _, c := range []struct {
		name string
		bad  func(r *metricRow)
		want string
	}{
		{"metric", func(r *metricRow) { r.Metric = "star" }, `rows[1]: unknown metric "star"`},
		{"ecosystem", func(r *metricRow) { r.Ecosystem = "maven" }, `rows[1]: unknown ecosystem "maven"`},
		{"kind", func(r *metricRow) { r.Kind = "team" }, `rows[1]: unknown kind "team"`},
		{"date", func(r *metricRow) { r.Date = "2026-13-01" }, `rows[1]: date "2026-13-01" is not YYYY-MM-DD`},
		{"future", func(r *metricRow) { r.Date = "2026-11-12" }, `rows[1]: date "2026-11-12" is in the future`},
		{"negative", func(r *metricRow) { r.Value = -1 }, `rows[1]: value -1 is negative`},
		{"name", func(r *metricRow) { r.Name = "  " }, `rows[1]: name is required`},
		{"long", func(r *metricRow) { r.Name = strings.Repeat("a", 215) }, `rows[1]: name is longer than 214`},
		// PostgreSQL refuses these at the statement, as one 500 for the batch; both
		// engines refuse them here instead, by row.
		{"nul", func(r *metricRow) { r.Name = "a\x00b" }, `rows[1]: dialect: text holds a NUL byte or is not valid UTF-8`},
		{"utf8", func(r *metricRow) { r.Org = "\xff" }, `rows[1]: dialect: text holds a NUL byte or is not valid UTF-8`},
	} {
		bad := good
		c.bad(&bad)
		_, _, err := checkBatch([]metricRow{good, bad}, nil, today)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}

	// Tomorrow is allowed: a collector east of UTC is already there.
	tomorrow := good
	tomorrow.Date = "2026-11-11"
	if _, _, err := checkBatch([]metricRow{tomorrow}, nil, today); err != nil {
		t.Fatalf("tomorrow refused: %v", err)
	}

	if _, _, err := checkBatch(nil, nil, today); err == nil {
		t.Fatal("empty batch accepted")
	}
	if _, _, err := checkBatch(make([]metricRow, maxBatch+1), nil, today); err == nil || !strings.Contains(err.Error(), "at most 5000") {
		t.Fatalf("oversized batch: %v", err)
	}

	_, pkgs, err := checkBatch(nil, []packageRef{{Ecosystem: "npm", Name: " @hanzo/ui ", Upstream: "https://github.com/HanzoAI/ui.git"}}, today)
	if err != nil || len(pkgs) != 1 || pkgs[0].Name != "@hanzo/ui" || pkgs[0].Upstream != "github.com/hanzoai/ui" {
		t.Fatalf("package normalised = %+v, %v", pkgs, err)
	}
	for _, c := range []struct {
		p    packageRef
		want string
	}{
		{packageRef{Ecosystem: "github", Name: "x", Upstream: "github.com/a/b"}, `packages[0]: ecosystem "github" is not a package registry`},
		{packageRef{Ecosystem: "npm", Name: "x", Upstream: "gitlab.com/a/b"}, `packages[0]: upstream "gitlab.com/a/b" is not github.com/<owner>/<repo>`},
		{packageRef{Ecosystem: "npm", Name: "", Upstream: "github.com/a/b"}, `packages[0]: name is required`},
		{packageRef{Ecosystem: "npm", Name: "x\x00", Upstream: "github.com/a/b"}, `packages[0]: dialect: text holds a NUL byte or is not valid UTF-8`},
	} {
		if _, _, err := checkBatch(nil, []packageRef{c.p}, today); err == nil || err.Error() != c.want {
			t.Errorf("package %+v: err = %v, want %q", c.p, err, c.want)
		}
	}
}

func TestCheckWindow(t *testing.T) {
	today := time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)
	w, err := checkWindow("", "", "", "", today)
	if err != nil || w.From != "2026-08-13" || w.To != "2026-11-10" || w.Granularity != "day" || w.By != "name" {
		t.Fatalf("defaults = %+v, %v", w, err)
	}
	w, err = checkWindow("2025-05-01", "2026-11-10", "month", "org", today)
	if err != nil || w.Granularity != "month" || w.By != "org" {
		t.Fatalf("month window = %+v, %v", w, err)
	}
	for _, c := range []struct{ from, to, gran, by string }{
		{"2025-10-05", "2026-11-10", "day", ""}, // 401 days
		{"2026-11-10", "2026-11-01", "", ""},    // from after to
		{"", "", "week", ""},
		{"", "", "", "repo"},
		{"2026/11/01", "", "", ""},
		{"2016-01-01", "2026-11-10", "month", ""}, // past 3,660 days
	} {
		if _, err := checkWindow(c.from, c.to, c.gran, c.by, today); err == nil {
			t.Errorf("checkWindow(%q,%q,%q,%q) accepted", c.from, c.to, c.gran, c.by)
		}
	}
}
