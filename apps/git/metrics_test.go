package git

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// doForge runs a /v1/admin/git request as a member of org, a SuperAdmin when super.
// An empty org is an anonymous caller.
func doForge(t *testing.T, app *zip.App, method, path, org string, super bool, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if super {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestForgeMetricsIsSuperAdminOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		body := map[string]any{"rows": []map[string]any{{"date": "2026-11-01", "kind": "repo", "ecosystem": "github", "org": "hanzoai", "name": "hanzoai/ui", "metric": "stars", "value": 1}}}
		for _, c := range []struct {
			name, org string
		}{{"member", "acme"}, {"anonymous", ""}} {
			if code, b := doForge(t, app, http.MethodPost, "/v1/admin/git/metrics", c.org, false, body); code != http.StatusForbidden && code != http.StatusUnauthorized {
				t.Fatalf("%s POST: want 403, got %d %s", c.name, code, b)
			}
			if code, b := doForge(t, app, http.MethodGet, "/v1/admin/git/metrics", c.org, false, nil); code != http.StatusForbidden && code != http.StatusUnauthorized {
				t.Fatalf("%s GET: want 403, got %d %s", c.name, code, b)
			}
		}
	})
}

func TestForgeMetricsRoundTrip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		day := time.Now().UTC()
		d1, d2 := day.AddDate(0, 0, -2).Format(dayLayout), day.AddDate(0, 0, -1).Format(dayLayout)
		in := map[string]any{
			"rows": []map[string]any{
				{"date": d1, "kind": "repo", "ecosystem": "github", "org": "hanzoai", "name": "hanzoai/ui", "metric": "stars", "value": 10},
				{"date": d2, "kind": "repo", "ecosystem": "github", "org": "hanzoai", "name": "hanzoai/ui", "metric": "stars", "value": 14},
			},
			"packages": []map[string]any{{"ecosystem": "npm", "name": "@hanzo/ui", "upstream": "https://github.com/hanzoai/ui"}},
		}
		code, b := doForge(t, app, http.MethodPost, "/v1/admin/git/metrics", "admin", true, in)
		if code != http.StatusOK {
			t.Fatalf("POST: %d %s", code, b)
		}
		var put putMetricsOut
		if err := json.Unmarshal(b, &put); err != nil || put.Written != 2 || put.Packages != 1 {
			t.Fatalf("POST out = %s (%v)", b, err)
		}

		code, b = doForge(t, app, http.MethodGet, "/v1/admin/git/metrics?metric=stars&by=org", "admin", true, nil)
		if code != http.StatusOK {
			t.Fatalf("GET: %d %s", code, b)
		}
		var out getMetricsOut
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if out.Granularity != "day" || out.By != "org" || out.To != day.Format(dayLayout) || len(out.Series) != 1 {
			t.Fatalf("GET out = %s", b)
		}
		s := out.Series[0]
		if s.Key != "hanzoai" || s.Kind != "gauge" || len(s.Points) != 2 || s.Points[1].Value != 14 {
			t.Fatalf("series = %+v", s)
		}
	})
}

func TestForgeMetricsRejectsBadBatchWhole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		d := time.Now().UTC().Format(dayLayout)
		in := map[string]any{"rows": []map[string]any{
			{"date": d, "kind": "repo", "ecosystem": "github", "org": "hanzoai", "name": "hanzoai/ui", "metric": "stars", "value": 1},
			{"date": d, "kind": "repo", "ecosystem": "github", "org": "hanzoai", "name": "hanzoai/ui", "metric": "star", "value": 1},
		}}
		code, b := doForge(t, app, http.MethodPost, "/v1/admin/git/metrics", "admin", true, in)
		if code != http.StatusBadRequest || !strings.Contains(string(b), "rows[1]") {
			t.Fatalf("bad batch: %d %s", code, b)
		}
		_, b = doForge(t, app, http.MethodGet, "/v1/admin/git/metrics", "admin", true, nil)
		var out getMetricsOut
		if err := json.Unmarshal(b, &out); err != nil || len(out.Series) != 0 {
			t.Fatalf("after refused batch: %s (%v)", b, err)
		}
	})
}

func TestForgeMetricsRefusesWideQueries(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		for _, q := range []string{
			"from=2025-01-01&to=2026-11-01",
			"from=2026-11-02&to=2026-11-01",
			"granularity=week",
			"by=repo",
		} {
			if code, b := doForge(t, app, http.MethodGet, "/v1/admin/git/metrics?"+q, "admin", true, nil); code != http.StatusBadRequest {
				t.Errorf("%s: want 400, got %d %s", q, code, b)
			}
		}
	})
}

// TestRepoViewsCarryTheirGitHubUpstream: a repo linked to GitHub, and a forge
// mirror of a GitHub repository, name it; anything else names none.
func TestRepoViewsCarryTheirGitHubUpstream(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_ = mountApp(t)
		s := mounted.Load()
		for _, c := range []struct{ origin, want string }{
			{"https://github.com/hanzoai/ui.git", "github.com/hanzoai/ui"},
			{"https://gitlab.com/hanzoai/ui.git", ""},
			{"", ""},
		} {
			if v := toView(s, Repo{Org: "acme", Name: "ui", Origin: c.origin}, nil, ""); v.Upstream != c.want {
				t.Errorf("toView origin %q: upstream %q, want %q", c.origin, v.Upstream, c.want)
			}
		}
		if v := forgeView("acme", forge.Repo{Name: "node", Mirror: true, Origin: "https://github.com/luxfi/node"}, nil); v.Upstream != "github.com/luxfi/node" {
			t.Errorf("mirror upstream = %q", v.Upstream)
		}
		if v := forgeView("acme", forge.Repo{Name: "node", Origin: "https://github.com/luxfi/node"}, nil); v.Upstream != "" {
			t.Errorf("a repository that is not a mirror named an upstream: %q", v.Upstream)
		}
	})
}

// TestWithPackagesJoinsByUpstream: a repo row lists the packages registered
// against its upstream, whatever case either was spelt in.
func TestWithPackagesJoinsByUpstream(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_ = mountApp(t)
		s := mounted.Load()
		ctx := t.Context()
		m, err := s.State.metrics.get(s.State.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.put(ctx, nil, []packageRef{
			{Ecosystem: "npm", Name: "@hanzo/ui", Upstream: "github.com/hanzoai/ui"},
			{Ecosystem: "pypi", Name: "hanzo-ui", Upstream: "github.com/hanzoai/ui"},
		}); err != nil {
			t.Fatal(err)
		}
		views := withPackages(ctx, s, []repoView{{Name: "ui", Upstream: "github.com/HanzoAI/ui"}, {Name: "none"}})
		if p := views[0].Packages; len(p) != 2 || p[0] != (packageRefView{Ecosystem: "npm", Name: "@hanzo/ui"}) {
			t.Fatalf("packages = %+v", p)
		}
		if views[1].Packages != nil {
			t.Fatalf("a repo with no upstream got packages: %+v", views[1].Packages)
		}
	})
}

// TestReposSurviveAMetricsStoreFailure: the registry failing costs a repo list
// its packages, never the list.
func TestReposSurviveAMetricsStoreFailure(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "widgets"}); code != http.StatusCreated {
			t.Fatalf("create: %d %s", code, b)
		}
		s := mounted.Load()
		m, err := s.State.metrics.get(s.State.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		_ = m.Close()
		views := withPackages(t.Context(), s, []repoView{{Name: "ui", Upstream: "github.com/hanzoai/ui"}})
		if len(views) != 1 || views[0].Packages != nil {
			t.Fatalf("after a failed lookup = %+v", views)
		}
		if code, b := do(t, app, http.MethodGet, "/v1/git/repos", "acme", nil); code != http.StatusOK || !strings.Contains(string(b), `"widgets"`) {
			t.Fatalf("list with the registry down: %d %s", code, b)
		}
		s.State.metrics = &metricsHolder{} // Shutdown must not close it twice.
	})
}
