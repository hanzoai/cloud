package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// collectNow is the day every collection test runs on: figures are for the 20th
// and daily downloads for the 14 days ending the 19th.
var collectNow = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

type fakeRegistries struct {
	mu                 sync.Mutex
	npmSearch          map[string][]map[string]any // scope -> objects
	npmMeta            map[string]map[string]any   // pkg -> metadata JSON
	pypiMeta           map[string]map[string]any   // pkg -> {"info": ...}
	cratesSearch       map[string][]crateMeta      // prefix -> crates
	cratesCounts       map[string]int64            // override crate downloads
	cratesRepos        map[string]string           // override crate repository URL
	hubRepos           map[string][]hubRepo        // override Docker Hub namespace repos
	orgRepos           map[string][]string         // org -> []full_name
	releases           map[string][]int64          // repo -> []asset download_count
	releaseRateLimitAt int                         // 0 = no rate limit
	releaseCalls       atomic.Int32
}

// registries stands in for all registries and public GitHub APIs and points
// the collector at them. A package named "hanzo-broken" fails on every
// registry; one named "hanzo-gone" is absent.
func registries(t *testing.T) *httptest.Server {
	t.Helper()
	return registriesWith(t, &fakeRegistries{})
}

func registriesWith(t *testing.T, cfg *fakeRegistries) *httptest.Server {
	t.Helper()
	day := func(i int) string {
		return time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format(dayLayout)
	}
	var hubPage sync.Map
	mux := http.NewServeMux()
	mux.HandleFunc("/npm/downloads/range/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/npm/downloads/range/")
		window, name, _ := strings.Cut(rest, "/")
		from, to, _ := strings.Cut(window, ":")
		switch name {
		case "broken", "hanzo-broken":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "gone", "hanzo-gone":
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var days []map[string]any
		for i := 14; i >= 1; i-- {
			if d := day(i); d >= from && d <= to {
				days = append(days, map[string]any{"day": d, "downloads": 100 + i})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"package": name, "downloads": days})
	})
	mux.HandleFunc("/npm-reg/-/v1/search", func(w http.ResponseWriter, r *http.Request) {
		text := r.URL.Query().Get("text")
		scope := strings.TrimPrefix(text, "scope:")
		cfg.mu.Lock()
		objs := cfg.npmSearch[scope]
		cfg.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"objects": objs, "total": len(objs)})
	})
	mux.HandleFunc("/npm-reg/", func(w http.ResponseWriter, r *http.Request) {
		name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/npm-reg/"))
		cfg.mu.Lock()
		meta, ok := cfg.npmMeta[name]
		cfg.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("/pypi/packages/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.Split(strings.TrimPrefix(r.URL.Path, "/pypi/packages/"), "/")[0]
		if name == "broken" || name == "hanzo-broken" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var data []map[string]any
		for i := 30; i >= 0; i-- { // a longer history than the window, and a day after it
			data = append(data, map[string]any{"category": "without_mirrors", "date": day(i), "downloads": 10 + i})
			data = append(data, map[string]any{"category": "with_mirrors", "date": day(i), "downloads": 9999})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"package": name, "data": data})
	})
	mux.HandleFunc("/pypi-reg/pypi/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.Split(strings.TrimPrefix(r.URL.Path, "/pypi-reg/pypi/"), "/")[0]
		cfg.mu.Lock()
		meta, ok := cfg.pypiMeta[name]
		cfg.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("/crates/crates", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		cfg.mu.Lock()
		items := cfg.cratesSearch[q]
		cfg.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"crates": items, "meta": map[string]any{"total": len(items)}})
	})
	mux.HandleFunc("/crates/crates/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/crates/crates/")
		if name == "broken" || name == "hanzo-broken" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		cfg.mu.Lock()
		dl, hasCount := cfg.cratesCounts[name]
		repo := cfg.cratesRepos[name]
		cfg.mu.Unlock()
		if !hasCount {
			dl = 5000
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"crate": map[string]any{"name": name, "downloads": dl, "repository": repo}})
	})
	var srv *httptest.Server
	mux.HandleFunc("/hub/repositories/", func(w http.ResponseWriter, r *http.Request) {
		ns := strings.Split(strings.TrimPrefix(r.URL.Path, "/hub/repositories/"), "/")[0]
		cfg.mu.Lock()
		custom, hasCustom := cfg.hubRepos[ns]
		cfg.mu.Unlock()
		if hasCustom {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": custom})
			return
		}
		switch ns {
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
		case "hanzoai":
			// Two pages: the first names the second, and the second names an address that is not the registry's, which is not followed.
			if _, second := hubPage.Load("hanzoai"); !second && r.URL.Query().Get("page") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"next":    srv.URL + "/hub/repositories/hanzoai/?page=2",
					"results": []map[string]any{{"name": "ui", "pull_count": 700}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"next": "https://elsewhere.invalid/x", "results": []map[string]any{{"name": "cli", "pull_count": 3}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("/github/orgs/", func(w http.ResponseWriter, r *http.Request) {
		org := strings.Split(strings.TrimPrefix(r.URL.Path, "/github/orgs/"), "/")[0]
		cfg.mu.Lock()
		repos := cfg.orgRepos[org]
		cfg.mu.Unlock()
		out := make([]map[string]any, 0, len(repos))
		for _, full := range repos {
			out = append(out, map[string]any{"full_name": full, "private": false})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/github/repos/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/github/repos/")
		parts := strings.Split(rest, "/")
		if len(parts) < 3 || parts[2] != "releases" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := int(cfg.releaseCalls.Add(1))
		if cfg.releaseRateLimitAt > 0 && n > cfg.releaseRateLimitAt {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		repo := parts[0] + "/" + parts[1]
		cfg.mu.Lock()
		counts := cfg.releases[repo]
		cfg.mu.Unlock()
		if len(counts) == 0 {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		var assets []map[string]any
		for _, c := range counts {
			assets = append(assets, map[string]any{"download_count": c})
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"draft": false, "assets": assets}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	was := [7]string{npmAPI, npmRegistryAPI, pypiAPI, pypiRegistryAPI, cratesAPI, hubAPI, githubPublicAPI}
	wasBackoff, wasPypi, wasCrates, wasPypiMeta, wasGHPub := collectBackoff, pypiEvery, cratesEvery, pypiMetaEvery, githubPublicEvery
	npmAPI = srv.URL + "/npm"
	npmRegistryAPI = srv.URL + "/npm-reg"
	pypiAPI = srv.URL + "/pypi"
	pypiRegistryAPI = srv.URL + "/pypi-reg"
	cratesAPI = srv.URL + "/crates"
	hubAPI = srv.URL + "/hub"
	githubPublicAPI = srv.URL + "/github"
	collectBackoff, pypiEvery, cratesEvery, pypiMetaEvery, githubPublicEvery = time.Millisecond, 0, 0, 0, 0
	t.Cleanup(func() {
		npmAPI, npmRegistryAPI, pypiAPI, pypiRegistryAPI, cratesAPI, hubAPI, githubPublicAPI = was[0], was[1], was[2], was[3], was[4], was[5], was[6]
		collectBackoff, pypiEvery, cratesEvery, pypiMetaEvery, githubPublicEvery = wasBackoff, wasPypi, wasCrates, wasPypiMeta, wasGHPub
	})
	return srv
}

// figuresProvider stands in for provider: each repository answers its figures and the
// manifests in files, and one named in gone is missing. asked records each question.
type figuresProvider struct {
	mu    sync.Mutex
	asked [][]string
	stars int64
	files map[string][]client.GitHubFile
	gone  map[string]bool
	fail  func(repos []string) error
}

func (g *figuresProvider) install(t *testing.T) {
	t.Helper()
	was := githubFigures
	t.Cleanup(func() { githubFigures = was })
	githubFigures = func(ctx context.Context, repos []string) (*client.GitHubFigures, error) {
		g.mu.Lock()
		g.asked = append(g.asked, slices.Clone(repos))
		g.mu.Unlock()
		if g.fail != nil {
			if err := g.fail(repos); err != nil {
				return nil, err
			}
		}
		out := &client.GitHubFigures{Repos: []client.GitHubFigure{}}
		for _, r := range repos {
			if g.gone[r] {
				out.Missing = append(out.Missing, r)
				continue
			}
			out.Repos = append(out.Repos, client.GitHubFigure{Repo: r, Stars: g.stars, Forks: 2, Issues: 3, Watchers: 4, Files: g.files[r]})
		}
		return out, nil
	}
}

func file(path, text string) client.GitHubFile { return client.GitHubFile{Path: path, Text: text} }

// rows reads every figure of one ecosystem and metric.
func rows(t *testing.T, m *metricsStore, eco, metric string) map[string]int64 {
	t.Helper()
	got, more, err := m.query(context.Background(), metricQuery{Ecosystem: eco, Metric: metric}, 100000)
	if err != nil || more {
		t.Fatalf("query %s/%s: %v more=%v", eco, metric, err, more)
	}
	out := map[string]int64{}
	for _, r := range got {
		out[r.Name+"@"+r.Date] = r.Value
	}
	return out
}

func TestCollectWritesWhatTheSourcesSay(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		registries(t)
		g := &figuresProvider{stars: 41, files: map[string][]client.GitHubFile{
			"hanzoai/ui":   {file("package.json", `{"name":"@hanzo/ui"}`), file("Cargo.toml", "[package]\nname = \"hanzo-ui\"\n")},
			"hanzoai/py":   {file("pyproject.toml", "[project]\nname = \"hanzo\"\n")},
			"hanzoai/site": {file("package.json", `{"name":"site","private":true}`)},
		}}
		g.install(t)
		m := metricsFor(t, open)
		ctx := context.Background()

		out, err := collect(ctx, m, []string{"hanzoai/ui", "hanzoai/py", "hanzoai/site", "luxfi/node"}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Failed) != 0 {
			t.Fatalf("failed = %v", out.Failed)
		}
		// 4 repos x 4 counts; npm 14 days; pypi 14 days; one crate; two hub repos.
		if out.Repos != 4 || out.Packages != 4 || out.Figures != 16+14+14+1+2 {
			t.Fatalf("collected %+v, want 4 repos, 4 packages (npm, crates, pypi, dockerhub), 47 figures", out)
		}
		stars := rows(t, m, "github", "stars")
		if len(stars) != 4 || stars["hanzoai/ui@2026-11-20"] != 41 {
			t.Fatalf("stars = %v", stars)
		}
		if got := rows(t, m, "github", "open_issues"); got["luxfi/node@2026-11-20"] != 3 {
			t.Fatalf("open_issues = %v", got)
		}
		npm := rows(t, m, "npm", "downloads")
		if len(npm) != 14 || npm["@hanzo/ui@2026-11-19"] != 101 || npm["@hanzo/ui@2026-11-06"] != 114 {
			t.Fatalf("npm downloads = %v, want the 14 days ending the 19th", npm)
		}
		if _, today := npm["@hanzo/ui@2026-11-20"]; today {
			t.Fatal("a day still in progress was written")
		}
		py := rows(t, m, "pypi", "downloads")
		if len(py) != 14 || py["hanzo@2026-11-19"] != 11 {
			t.Fatalf("pypi downloads = %v, want mirrors left out and only the window", py)
		}
		if got := rows(t, m, "crates", "downloads_total"); got["hanzo-ui@2026-11-20"] != 5000 {
			t.Fatalf("crates = %v", got)
		}
		hub := rows(t, m, "dockerhub", "pulls_total")
		if len(hub) != 2 || hub["hanzoai/ui@2026-11-20"] != 700 || hub["hanzoai/cli@2026-11-20"] != 3 {
			t.Fatalf("hub = %v, want both pages of the namespace", hub)
		}

		// Registered against the repository its manifest or its name came from, and no private package.
		found, err := m.packagesFor(ctx, []string{"github.com/hanzoai/ui", "github.com/hanzoai/site"})
		if err != nil {
			t.Fatal(err)
		}
		var ui []string
		for _, p := range found["github.com/hanzoai/ui"] {
			ui = append(ui, p.Ecosystem+":"+p.Name)
		}
		// hanzoai/cli is on Docker Hub and not the estate's: figures, but registered against nothing.
		if !slices.Equal(ui, []string{"crates:hanzo-ui", "dockerhub:hanzoai/ui", "npm:@hanzo/ui"}) || len(found["github.com/hanzoai/site"]) != 0 {
			t.Fatalf("registered = %v / %v", ui, found["github.com/hanzoai/site"])
		}
		if org := figureOrg(t, m, "npm", "@hanzo/ui"); org != "hanzoai" {
			t.Fatalf("a package's org = %q, want its repository's owner", org)
		}
	})
}

func figureOrg(t *testing.T, m *metricsStore, eco, name string) string {
	t.Helper()
	got, _, err := m.query(context.Background(), metricQuery{Ecosystem: eco, Name: name}, 1)
	if err != nil || len(got) == 0 {
		t.Fatalf("no figure for %s/%s: %v", eco, name, err)
	}
	return got[0].Org
}

// A day's run, repeated, is the same figures: the second reads a new star count
// and a changed download figure and replaces what the first wrote.
func TestCollectIsIdempotent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		registries(t)
		g := &figuresProvider{stars: 1, files: map[string][]client.GitHubFile{"hanzoai/ui": {file("package.json", `{"name":"@hanzo/ui"}`)}}}
		g.install(t)
		m := metricsFor(t, open)
		ctx := context.Background()

		first, err := collect(ctx, m, []string{"hanzoai/ui"}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		before, _, _ := m.query(ctx, metricQuery{}, 1000)
		g.stars = 9
		second, err := collect(ctx, m, []string{"hanzoai/ui"}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		after, _, _ := m.query(ctx, metricQuery{}, 1000)
		if len(after) != len(before) || second.Figures != first.Figures {
			t.Fatalf("rows %d then %d (figures %d then %d): a run must replace, never add", len(before), len(after), first.Figures, second.Figures)
		}
		if got := rows(t, m, "github", "stars"); got["hanzoai/ui@2026-11-20"] != 9 {
			t.Fatalf("stars = %v, want the second run's", got)
		}
		pkgs, _ := m.allPackages(ctx)
		if len(pkgs) != 2 {
			t.Fatalf("packages = %+v, want the npm package and its Docker Hub image, once each", pkgs)
		}
	})
}

// GitHub is asked a hundred at a time.
func TestCollectAsksGitHubInBatchesOfAHundred(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		registries(t)
		g := &figuresProvider{stars: 1}
		g.install(t)
		m := metricsFor(t, open)
		var repos []string
		for i := range 250 {
			repos = append(repos, fmt.Sprintf("acme/r%03d", i))
		}
		out, err := collect(context.Background(), m, repos, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		var sizes []int
		for _, b := range g.asked {
			sizes = append(sizes, len(b))
		}
		slices.Sort(sizes)
		if !slices.Equal(sizes, []int{50, 100, 100}) || out.Repos != 250 || out.Figures != 1000 {
			t.Fatalf("batches %v, repos %d, figures %d", sizes, out.Repos, out.Figures)
		}
	})
}

// A source that fails costs itself and is named; the rest is written. A
// repository GitHub does not publish is not an error.
func TestCollectNamesAFailingSourceAndWritesTheRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		registries(t)
		g := &figuresProvider{
			stars: 5,
			gone:  map[string]bool{"acme/gone": true},
			files: map[string][]client.GitHubFile{
				"hanzoai/ui":     {file("package.json", `{"name":"@hanzo/ui"}`)},
				"hanzoai/broken": {file("package.json", `{"name":"hanzo-broken"}`), file("pyproject.toml", "[project]\nname = \"hanzo-broken\"\n"), file("Cargo.toml", "[package]\nname = \"hanzo-broken\"\n")},
				"hanzoai/gone":   {file("package.json", `{"name":"hanzo-gone"}`)},
			},
			// The batch holding the last repository fails.
			fail: func(repos []string) error {
				if slices.Contains(repos, "zzz/after") {
					return errors.New("github http 502")
				}
				return nil
			},
		}
		g.install(t)
		m := metricsFor(t, open)

		// 101 repositories: the first batch of 100 answers, the second (one repository) fails.
		repos := []string{"acme/gone", "hanzoai/broken", "hanzoai/gone", "hanzoai/ui"}
		for i := len(repos); i < 100; i++ {
			repos = append(repos, fmt.Sprintf("acme/r%03d", i))
		}
		repos = append(repos, "zzz/after")
		out, err := collect(context.Background(), m, repos, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(out.Failed, "\n")
		for _, want := range []string{"github: repositories 101 to 101 of 101", "npm: 1 of 3 reads failed", "pypi: 1 of 1 reads failed", "crates: 1 of 1 reads failed", "dockerhub:"} {
			if want == "dockerhub:" {
				// hanzoai answers; nothing in the estate's owners is "broken".
				if strings.Contains(joined, want) {
					t.Fatalf("dockerhub failed: %s", joined)
				}
				continue
			}
			if !strings.Contains(joined, want) {
				t.Fatalf("failed = %q, want it to name %q", joined, want)
			}
		}
		// What the healthy sources read is written: 99 repositories (GitHub does not
		// publish acme/gone), the one package that is not "broken", and the hub.
		if out.Repos != 99 {
			t.Fatalf("repos answered = %d, want 99", out.Repos)
		}
		if got := rows(t, m, "npm", "downloads"); len(got) != 14 {
			t.Fatalf("npm downloads = %v, want the healthy package's 14 days", got)
		}
		if got := rows(t, m, "dockerhub", "pulls_total"); len(got) != 2 {
			t.Fatalf("hub = %v", got)
		}
		if got := rows(t, m, "github", "stars"); got["acme/gone@2026-11-20"] != 0 || len(got) != 99 {
			t.Fatalf("a repository GitHub does not publish was written, or the figures are short: %d", len(got))
		}
	})
}

// A source gone from its registry is absent, not failed.
func TestCollectDoesNotCallAnAbsentPackageAFailure(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		registries(t)
		g := &figuresProvider{stars: 1, files: map[string][]client.GitHubFile{"hanzoai/gone": {file("package.json", `{"name":"hanzo-gone"}`)}}}
		g.install(t)
		out, err := collect(context.Background(), metricsFor(t, open), []string{"hanzoai/gone"}, collectNow)
		if err != nil || len(out.Failed) != 0 {
			t.Fatalf("collect: %+v %v", out, err)
		}
	})
}

// With no estate to ask about there is nothing to collect.
func TestCollectRefusesAnEmptyEstate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		if _, err := collect(context.Background(), metricsFor(t, open), nil, collectNow); err == nil {
			t.Fatal("collected an estate of nothing")
		}
	})
}

// A work that runs out of its budget still writes what it read.
func TestCollectWritesWhatItReadWhenTheBudgetEnds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		registries(t)
		g := &figuresProvider{stars: 1}
		g.install(t)
		ctx, cancel := context.WithCancel(context.Background())
		was := githubFigures
		githubFigures = func(c context.Context, repos []string) (*client.GitHubFigures, error) {
			out, err := was(c, repos)
			cancel() // the budget ends after GitHub answered
			return out, err
		}
		t.Cleanup(func() { githubFigures = was })
		m := metricsFor(t, open)
		out, err := collect(ctx, m, []string{"acme/a"}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		if got := rows(t, m, "github", "stars"); len(got) != 1 {
			t.Fatalf("stars = %v, want what GitHub answered before the budget ended", got)
		}
		if !strings.Contains(strings.Join(out.Failed, ";"), "ran out of time") {
			t.Fatalf("failed = %v, want it to say time ran out", out.Failed)
		}
	})
}

// Third-party packages in forked repo manifests or stale forge_packages rows
// are rejected; packages verified via registry metadata to point to a Hanzo
// estate GitHub org are accepted.
func TestCollectRejectsThirdPartyPackagesAndVerifiesOwnership(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		cfg := &fakeRegistries{
			npmMeta: map[string]map[string]any{
				"postcss":      {"name": "postcss", "repository": map[string]any{"type": "git", "url": "git+https://github.com/postcss/postcss.git"}},
				"cookie":       {"name": "cookie", "repository": "jshttp/cookie"},
				"custom-hanzo": {"name": "custom-hanzo", "repository": "https://github.com/hanzoai/custom-hanzo"},
			},
			cratesRepos: map[string]string{
				"tokio":    "https://github.com/tokio-rs/tokio",
				"hanzo-ui": "https://github.com/hanzoai/ui",
			},
		}
		registriesWith(t, cfg)
		g := &figuresProvider{
			stars: 10,
			files: map[string][]client.GitHubFile{
				"hanzoai/postcss":  {file("package.json", `{"name":"postcss","repository":"postcss/postcss"}`)},
				"hanzoai/cookie":   {file("package.json", `{"name":"cookie"}`)},
				"hanzoai/express":  {file("package.json", `{"name":"express"}`)},
				"hanzoai/eslint":   {file("package.json", `{"name":"eslint"}`)},
				"hanzoai/prettier": {file("package.json", `{"name":"prettier"}`)},
				"hanzoai/sharp":    {file("package.json", `{"name":"sharp"}`)},
				"hanzoai/ui":       {file("package.json", `{"name":"@hanzo/ui"}`)},
			},
		}
		g.install(t)
		m := metricsFor(t, open)
		ctx := context.Background()

		// Seed stale polluted registrations as exist in live prod prior to cleanup.
		if err := m.put(ctx, nil, []packageRef{
			{Ecosystem: "npm", Name: "postcss", Upstream: "github.com/hanzoai/postcss"},
			{Ecosystem: "npm", Name: "cookie", Upstream: "github.com/hanzoai/cookie"},
			{Ecosystem: "npm", Name: "custom-hanzo", Upstream: "github.com/hanzoai/custom-hanzo"},
			{Ecosystem: "crates", Name: "tokio", Upstream: "github.com/hanzoai/tokio"},
		}); err != nil {
			t.Fatal(err)
		}

		out, err := collect(ctx, m, []string{
			"hanzoai/postcss", "hanzoai/cookie", "hanzoai/express",
			"hanzoai/eslint", "hanzoai/prettier", "hanzoai/sharp", "hanzoai/ui",
		}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Failed) != 0 {
			t.Fatalf("failed = %v", out.Failed)
		}

		npm := rows(t, m, "npm", "downloads")
		for _, bad := range []string{"postcss", "cookie", "express", "eslint", "prettier", "sharp"} {
			if _, ok := npm[bad+"@2026-11-19"]; ok {
				t.Fatalf("third-party package %q was collected into npm downloads", bad)
			}
		}
		if npm["@hanzo/ui@2026-11-19"] != 101 {
			t.Fatalf("missing @hanzo/ui downloads: %v", npm)
		}
		if npm["custom-hanzo@2026-11-19"] != 101 {
			t.Fatalf("missing registry-verified custom-hanzo downloads: %v", npm)
		}
		crates := rows(t, m, "crates", "downloads_total")
		if _, ok := crates["tokio@2026-11-20"]; ok {
			t.Fatalf("third-party crate tokio was collected: %v", crates)
		}
	})
}

// Direct registry discovery finds monorepo npm packages (even when root
// package.json is private), crates.io packages by prefix (filtering out
// third-party noise), and PyPI estate packages.
func TestCollectDiscoversMonorepoPackagesAcrossRegistries(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		cfg := &fakeRegistries{
			npmSearch: map[string][]map[string]any{
				"hanzo": {
					{"package": map[string]any{"name": "@hanzo/mcp", "links": map[string]any{"repository": "https://github.com/hanzoai/mcp"}}},
					{"package": map[string]any{"name": "@hanzo/sdk", "links": map[string]any{"repository": "https://github.com/hanzoai/js-sdk"}}},
				},
				"hanzogui": {
					{"package": map[string]any{"name": "@hanzogui/core", "links": map[string]any{"repository": "https://github.com/hanzogui/gui"}}},
				},
			},
			cratesSearch: map[string][]crateMeta{
				"hanzo": {
					{Name: "hanzo-mcp", Downloads: 900, Repository: "https://github.com/hanzoai/mcp"},
					{Name: "unrelated-crate", Downloads: 99999, Repository: "https://github.com/someone/unrelated"},
				},
				"lux-": {
					{Name: "lux-node", Downloads: 1200, Repository: "https://github.com/luxfi/node"},
				},
			},
			pypiMeta: map[string]map[string]any{
				"hanzoai": {
					"info": map[string]any{
						"name":         "hanzoai",
						"project_urls": map[string]any{"Repository": "https://github.com/hanzoai/python-sdk"},
					},
				},
				"zen-sdk": {
					"info": map[string]any{
						"name":         "zen-sdk",
						"project_urls": map[string]any{"Source": "https://github.com/zenlm/sdk"},
					},
				},
			},
		}
		registriesWith(t, cfg)
		g := &figuresProvider{
			stars: 50,
			files: map[string][]client.GitHubFile{
				// Monorepo with private root package.json!
				"hanzoai/mono": {file("package.json", `{"name":"mono","private":true,"workspaces":["packages/*"]}`)},
			},
		}
		g.install(t)
		m := metricsFor(t, open)
		ctx := context.Background()

		out, err := collect(ctx, m, []string{"hanzoai/mono"}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Failed) != 0 {
			t.Fatalf("failed = %v", out.Failed)
		}

		npm := rows(t, m, "npm", "downloads")
		for _, want := range []string{"@hanzo/mcp", "@hanzo/sdk", "@hanzogui/core"} {
			if npm[want+"@2026-11-19"] != 101 {
				t.Fatalf("monorepo npm package %q not discovered: %v", want, npm)
			}
		}
		crates := rows(t, m, "crates", "downloads_total")
		if crates["hanzo-mcp@2026-11-20"] != 900 || crates["lux-node@2026-11-20"] != 1200 {
			t.Fatalf("discovered crates = %v", crates)
		}
		if _, leaked := crates["unrelated-crate@2026-11-20"]; leaked {
			t.Fatalf("third-party crate leaked from search: %v", crates)
		}
		py := rows(t, m, "pypi", "downloads")
		if py["hanzoai@2026-11-19"] != 11 || py["zen-sdk@2026-11-19"] != 11 {
			t.Fatalf("discovered pypi = %v", py)
		}
	})
}

// Daily deltas are written for Docker Hub pulls and crates.io downloads once a
// prior day's gauge exists, remain idempotent on same-day reruns, and clamp to
// zero if a counter drops.
func TestCollectComputesDailyDeltasForPullsCratesAndReleases(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		cfg := &fakeRegistries{
			cratesCounts: map[string]int64{"hanzo-ui": 5000},
			hubRepos: map[string][]hubRepo{
				"hanzoai": {{Name: "ui", Pulls: 700}},
			},
			releases: map[string][]int64{
				"hanzoai/ui": {100, 50},
			},
		}
		registriesWith(t, cfg)
		g := &figuresProvider{
			stars: 41,
			files: map[string][]client.GitHubFile{
				"hanzoai/ui": {file("Cargo.toml", "[package]\nname = \"hanzo-ui\"\n")},
			},
		}
		g.install(t)
		m := metricsFor(t, open)
		ctx := context.Background()

		day1 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
		day2 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		day3 := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

		// Day 1: gauges recorded, no prior day for crates/dockerhub deltas, initial release_downloads populated.
		if _, err := collect(ctx, m, []string{"hanzoai/ui"}, day1); err != nil {
			t.Fatal(err)
		}
		if got := rows(t, m, "crates", "downloads"); len(got) != 0 {
			t.Fatalf("day1 crates daily downloads = %v, want empty without prior gauge", got)
		}
		if got := rows(t, m, "dockerhub", "pulls"); len(got) != 0 {
			t.Fatalf("day1 dockerhub daily pulls = %v, want empty without prior gauge", got)
		}
		if got := rows(t, m, "github", "release_downloads"); got["hanzoai/ui@2026-11-19"] != 150 {
			t.Fatalf("day1 release_downloads = %v, want 150", got)
		}

		// Day 2: counters advance -> daily deltas written!
		cfg.mu.Lock()
		cfg.cratesCounts["hanzo-ui"] = 5120
		cfg.hubRepos["hanzoai"] = []hubRepo{{Name: "ui", Pulls: 765}}
		cfg.releases["hanzoai/ui"] = []int64{130, 50}
		cfg.mu.Unlock()

		if _, err := collect(ctx, m, []string{"hanzoai/ui"}, day2); err != nil {
			t.Fatal(err)
		}
		if got := rows(t, m, "crates", "downloads"); got["hanzo-ui@2026-11-20"] != 120 {
			t.Fatalf("day2 crates daily downloads = %v, want 120", got)
		}
		if got := rows(t, m, "dockerhub", "pulls"); got["hanzoai/ui@2026-11-20"] != 65 {
			t.Fatalf("day2 dockerhub daily pulls = %v, want 65", got)
		}
		if got := rows(t, m, "github", "release_downloads"); got["hanzoai/ui@2026-11-20"] != 30 {
			t.Fatalf("day2 release_downloads = %v, want 30 (180-150)", got)
		}

		// Day 2 rerun (idempotent): still deltas against Day 1, not Day 2!
		cfg.mu.Lock()
		cfg.cratesCounts["hanzo-ui"] = 5130
		cfg.hubRepos["hanzoai"] = []hubRepo{{Name: "ui", Pulls: 770}}
		cfg.releases["hanzoai/ui"] = []int64{140, 50}
		cfg.mu.Unlock()
		if _, err := collect(ctx, m, []string{"hanzoai/ui"}, day2); err != nil {
			t.Fatal(err)
		}
		if got := rows(t, m, "crates", "downloads"); got["hanzo-ui@2026-11-20"] != 130 {
			t.Fatalf("day2 rerun crates daily downloads = %v, want 130", got)
		}
		if got := rows(t, m, "dockerhub", "pulls"); got["hanzoai/ui@2026-11-20"] != 70 {
			t.Fatalf("day2 rerun dockerhub daily pulls = %v, want 70", got)
		}
		if got := rows(t, m, "github", "release_downloads"); got["hanzoai/ui@2026-11-20"] != 40 {
			t.Fatalf("day2 rerun release_downloads = %v, want 40", got)
		}

		// Day 3: counter drop clamps cleanly to 0 (never negative).
		cfg.mu.Lock()
		cfg.cratesCounts["hanzo-ui"] = 5000
		cfg.hubRepos["hanzoai"] = []hubRepo{{Name: "ui", Pulls: 700}}
		cfg.releases["hanzoai/ui"] = []int64{100, 50}
		cfg.mu.Unlock()
		if _, err := collect(ctx, m, []string{"hanzoai/ui"}, day3); err != nil {
			t.Fatal(err)
		}
		if got := rows(t, m, "crates", "downloads"); got["hanzo-ui@2026-11-21"] != 0 {
			t.Fatalf("day3 clamped crates downloads = %v, want 0", got)
		}
		if got := rows(t, m, "dockerhub", "pulls"); got["hanzoai/ui@2026-11-21"] != 0 {
			t.Fatalf("day3 clamped dockerhub pulls = %v, want 0", got)
		}
		if got := rows(t, m, "github", "release_downloads"); got["hanzoai/ui@2026-11-21"] != 0 {
			t.Fatalf("day3 clamped release_downloads = %v, want 0", got)
		}
	})
}

// Estate GitHub orgs (luxfi, zenlm, zooai, hanzo-js, etc.) are discovered via
// GitHub's public org repos API and merged with activity.json repos without
// breaking the githubFigures seam.
func TestCollectDiscoversEstateGitHubOrgs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		cfg := &fakeRegistries{
			orgRepos: map[string][]string{
				"luxfi":    {"luxfi/node", "luxfi/evm"},
				"zenlm":    {"zenlm/zen"},
				"zooai":    {"zooai/gym"},
				"hanzo-js": {"hanzo-js/sdk"},
				"hanzoai":  {"hanzoai/ui"}, // duplicate of activity index repo
			},
		}
		registriesWith(t, cfg)
		g := &figuresProvider{stars: 25}
		g.install(t)
		m := metricsFor(t, open)

		out, err := collect(context.Background(), m, []string{"hanzoai/ui"}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Failed) != 0 {
			t.Fatalf("failed = %v", out.Failed)
		}
		if out.Repos != 6 {
			t.Fatalf("repos = %d, want 6 (1 index + 5 unique discovered estate org repos)", out.Repos)
		}
		stars := rows(t, m, "github", "stars")
		for _, want := range []string{"hanzoai/ui", "luxfi/node", "luxfi/evm", "zenlm/zen", "zooai/gym", "hanzo-js/sdk"} {
			if stars[want+"@2026-11-20"] != 25 {
				t.Fatalf("missing discovered estate repo %q in stars: %v", want, stars)
			}
		}
	})
}

// Release download collection stops cleanly on GitHub 403 rate limit without
// failing the rest of the run.
func TestCollectReleaseDownloadsHandlesRateLimitGracefully(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		cfg := &fakeRegistries{
			releases: map[string][]int64{
				"hanzoai/a": {42},
				"hanzoai/b": {99},
			},
			releaseRateLimitAt: 1,
		}
		registriesWith(t, cfg)
		g := &figuresProvider{stars: 10}
		g.install(t)
		m := metricsFor(t, open)

		out, err := collect(context.Background(), m, []string{"hanzoai/a", "hanzoai/b"}, collectNow)
		if err != nil {
			t.Fatal(err)
		}
		if got := rows(t, m, "github", "release_downloads"); got["hanzoai/a@2026-11-20"] != 42 || len(got) != 1 {
			t.Fatalf("release_downloads = %v, want only hanzoai/a before rate limit", got)
		}
		if !strings.Contains(strings.Join(out.Failed, ";"), "rate limited") {
			t.Fatalf("failed = %v, want rate limit notice", out.Failed)
		}
	})
}

// estateJSON is the index's document with repos in it, each with its GitHub page.
func estateJSON(repos ...string) []byte {
	var rs []map[string]any
	for _, r := range repos {
		rs = append(rs, map[string]any{"repo": r, "org": strings.Split(r, "/")[0], "group": "hanzo", "github": "https://github.com/" + r})
	}
	b, _ := json.Marshal(map[string]any{"read": "2026-11-20T00:00:00Z", "repos": rs})
	return b
}

func platformCtx(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org})
}

// The op is the platform's: any other caller, and no caller, is refused before
// anything is read or written.
func TestPlaneCollectIsThePlatformsAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		registries(t)
		g := &figuresProvider{stars: 7}
		g.install(t)
		mounted.Load().State.activity = newActivityCache(func(context.Context) ([]byte, error) {
			return estateJSON("hanzoai/ui"), nil
		})

		for _, c := range []struct{ name, org string }{{"a tenant", "acme"}, {"nobody", ""}, {"the brand", "hanzo"}} {
			_, err := planeCollect(platformCtx(c.org), nil)
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusForbidden {
				t.Fatalf("%s: err = %v, want 403", c.name, err)
			}
		}
		if len(g.asked) != 0 {
			t.Fatalf("GitHub was asked %d times by callers that were refused", len(g.asked))
		}

		out, err := planeCollect(platformCtx(authz.AdminOrg), nil)
		if err != nil {
			t.Fatalf("the platform: %v", err)
		}
		if out.Repos != 1 || out.Figures < 4 {
			t.Fatalf("collected %+v", out)
		}
		m, err := metricsOf(mounted.Load())
		if err != nil {
			t.Fatal(err)
		}
		if got := rows(t, m, "github", "stars"); got["hanzoai/ui@"+time.Now().UTC().Format(dayLayout)] != 7 {
			t.Fatalf("stars = %v", got)
		}
	})
}

// It is not an HTTP operation: no address serves it, whoever asks.
func TestCollectIsNotServedOverHTTP(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		for _, path := range []string{"/v1/git/metrics/collect", "/v1/admin/git/metrics/collect", "/v1/git/collect"} {
			for _, method := range []string{http.MethodPost, http.MethodGet} {
				if code, _ := doForge(t, app, method, path, "admin", true, map[string]any{}); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
					t.Fatalf("%s %s answered %d for a SuperAdmin, want it not served", method, path, code)
				}
			}
		}
	})
}

// A run that wrote nothing is a failed run, so the schedule reports it.
func TestPlaneCollectThatWroteNothingFails(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		registries(t)
		g := &figuresProvider{fail: func([]string) error { return errors.New("github http 502") }}
		g.install(t)
		mounted.Load().State.activity = newActivityCache(func(context.Context) ([]byte, error) {
			return estateJSON("acme/ui"), nil // no Docker Hub namespace to read
		})
		_, err := planeCollect(platformCtx(authz.AdminOrg), nil)
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusBadGateway {
			t.Fatalf("err = %v, want 502", err)
		}
	})
}

// An estate not yet indexed has nothing to ask about.
func TestPlaneCollectNeedsTheIndex(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		mounted.Load().State.activity = newActivityCache(func(context.Context) ([]byte, error) { return nil, errNotIndexed })
		if _, err := planeCollect(platformCtx(authz.AdminOrg), nil); err == nil {
			t.Fatal("collected with no index")
		}
	})
}

var _ = cloud.Who // the gate under test reads the plane's stated caller
