package git

// collect.go — the estate's open source collector (#456).
//
// Once a day the platform reads what GitHub and the package registries say of the
// estate and writes it to the forge's metrics store (metrics_store.go), the store
// the SuperAdmin ops in metrics.go read:
//
//	repositories  every public repository the estate's index lists with a GitHub
//	              address (activity.go) plus public repositories discovered across
//	              the canonical Hanzo estate GitHub orgs: stars, forks, open
//	              issues, watchers, and release asset downloads
//	packages      Hanzo-owned packages named in root manifests (collect_rules.go)
//	              plus packages discovered directly from npm scopes, crates.io
//	              prefixes, and PyPI seeds/candidates (collect_registry.go)
//	downloads     npm and PyPI downloads per day for the last 14 days, crates.io
//	              all-time downloads and daily deltas, Docker Hub namespace
//	              all-time pulls and daily deltas, and GitHub release downloads
//
// It is the platform's and holds no credential. The cron app asks the op by name
// as the platform (cron planeOps); GitHub is read by provider's App, which answers
// facts and never a token (github_figures.go there); the registries and public
// GitHub endpoints are public. It writes through the store, and never as a
// SuperAdmin request: nothing here is reachable over HTTP.
//
// Idempotent. A figure is one per entity, metric and day, and a run replaces what
// it reads, so a run repeated, or a window re-sent, never double-counts. A source
// that fails or runs out of time is named in the answer and costs only itself.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
	"golang.org/x/sync/errgroup"
)

const (
	// collectBudget bounds one collection, inside the 15 minutes a plane call is
	// allowed. Work unfinished at the end is named in Failed and written as read.
	collectBudget = 13 * time.Minute
	// githubBatch is how many repositories one question to GitHub names.
	githubBatch = 100
	// githubParallel and npmParallel bound the questions asked at once.
	githubParallel = 4
	npmParallel    = 8
	// downloadDays is how many days of daily downloads a run sends. A day is
	// settled a few days after it ends, so each run replaces the last two weeks.
	downloadDays = 14
	// maxPyPIProbeExtras bounds extra repo-derived PyPI candidate probes per run.
	maxPyPIProbeExtras = 50
)

// pypiEvery, cratesEvery and maxReleaseRepos bound registry and GitHub public
// reads. Package vars so tests adjust them cleanly.
var (
	pypiEvery       = time.Second
	cratesEvery     = time.Second
	maxReleaseRepos = 30
)

// githubFigures asks provider what GitHub says of repos, as the platform. A
// package var so a test stands in for it.
var githubFigures = func(ctx context.Context, repos []string) (*client.GitHubFigures, error) {
	return providerpeer.IntegrationsGitHubFigures(cloud.For(ctx, authz.AdminOrg), &client.GitHubFiguresIn{Repos: repos})
}

// exposeCollect publishes the collection on the internal plane. Use calls it.
func exposeCollect() {
	cloud.Plane().Post("/git/metrics/collect", planeCollect,
		zip.WithOperationID(client.GitMetricsCollect),
		zip.WithSummary("Read the estate's open source figures and write them to the forge's metrics store"))
}

// planeCollect reads the estate's open source figures from GitHub and the package
// registries and writes them to the forge's metrics store: each public
// repository's stars, forks, open issues and watchers, the packages its root
// manifest names, and those packages' downloads and a Docker Hub namespace's
// pulls.
//
// It is the platform's and nobody else's: the cron app asks it by name, as the
// platform, and any other caller is refused. It is not an HTTP operation and no
// SuperAdmin request reaches it. Running it again replaces the figures it wrote,
// so a repeated run never counts twice. A source that fails or runs out of time
// is named in the answer and the rest is written; a run that wrote nothing is an
// error.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCollect(ctx context.Context, _ *struct{}) (*client.MetricsCollected, error) {
	if cloud.Who(ctx).Org != authz.AdminOrg {
		return nil, zip.ErrForbidden("metrics collection: the platform runs it")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "git not mounted")
	}
	m, err := metricsOf(s)
	if err != nil {
		return nil, err
	}
	doc, err := s.State.activity.get(ctx)
	if err != nil {
		return nil, err
	}
	out, err := collect(ctx, m, estateRepos(doc), time.Now())
	if err != nil {
		s.Log.Warn("open source collection failed", "err", err)
		return nil, zip.ErrInternal("metrics collection failed")
	}
	if out.Figures == 0 && len(out.Failed) > 0 {
		s.Log.Warn("open source collection wrote nothing", "failed", out.Failed)
		return nil, zip.Errorf(http.StatusBadGateway, "metrics collection wrote nothing: %s", strings.Join(out.Failed, "; "))
	}
	if len(out.Failed) > 0 {
		s.Log.Warn("open source collection partly failed", "failed", out.Failed)
	}
	return out, nil
}

// estateRepos are the GitHub repositories the estate's index names, as
// owner/name, each once, in order.
func estateRepos(doc *activityDoc) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range doc.Repos {
		up, ok := normUpstream(r.GitHub)
		if !ok {
			continue
		}
		full := strings.TrimPrefix(up, "github.com/")
		if k := strings.ToLower(full); !seen[k] {
			seen[k] = true
			out = append(out, full)
		}
	}
	sort.Strings(out)
	return out
}

// mergeRepos combines base and extra GitHub owner/name lists, deduplicating
// case-insensitively while preserving base's spelling, sorted.
func mergeRepos(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, r := range base {
		k := strings.ToLower(strings.TrimSpace(r))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(r))
	}
	for _, r := range extra {
		k := strings.ToLower(strings.TrimSpace(r))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(r))
	}
	sort.Strings(out)
	return out
}

// run is one collection in progress.
type run struct {
	m   *metricsStore
	now time.Time

	mu     sync.Mutex
	rows   []metricRow
	failed []string
}

func (r *run) fail(format string, args ...any) {
	r.mu.Lock()
	r.failed = append(r.failed, fmt.Sprintf(format, args...))
	r.mu.Unlock()
}

func (r *run) add(rows ...metricRow) {
	r.mu.Lock()
	r.rows = append(r.rows, rows...)
	r.mu.Unlock()
}

// tally counts a source's reads, so a failure of many is one line.
type tally struct {
	mu            sync.Mutex
	asked, failed int
	first         error
}

func (t *tally) read(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.asked++
	if err != nil && err != errAbsent {
		t.failed++
		if t.first == nil {
			t.first = err
		}
	}
}

func (r *run) report(source string, t *tally) {
	if t.failed > 0 {
		r.fail("%s: %d of %d reads failed, first: %v", source, t.failed, t.asked, t.first)
	}
}

// collect reads every source and writes what it read. A failing source is named
// in Failed; only a store that will not write is an error.
func collect(ctx context.Context, m *metricsStore, repos []string, now time.Time) (*client.MetricsCollected, error) {
	if len(repos) == 0 {
		return nil, fmt.Errorf("the estate's index names no repository on GitHub")
	}
	r := &run{m: m, now: now.UTC()}
	budget, cancel := context.WithTimeout(ctx, collectBudget)
	defer cancel()

	allRepos := mergeRepos(repos, r.discoverOrgRepos(budget))

	out := &client.MetricsCollected{}
	pkgs, answered := r.github(budget, allRepos)
	out.Repos = answered
	if err := r.flush(ctx, pkgs, out); err != nil {
		return nil, err
	}
	// The registries read what is registered: what the manifests just named, what
	// was registered before (subject to ownership verification), and packages
	// discovered directly from the registries for Hanzo estate scopes/prefixes.
	more, err := r.registries(budget, allRepos)
	if err != nil {
		return nil, err
	}
	if err := r.flush(ctx, more, out); err != nil {
		return nil, err
	}
	out.Failed = r.failed
	return out, nil
}

// discoverOrgRepos queries GitHub's public org repos API for every canonical
// Hanzo estate org so repositories outside Forge's local activity index are
// still collected. Rate limits stop discovery cleanly without failing the run.
func (r *run) discoverOrgRepos(ctx context.Context) []string {
	var (
		t     tally
		found []string
		p     = &pace{every: githubPublicEvery}
	)
	for _, org := range estateGitHubOrgs {
		if ctx.Err() != nil {
			break
		}
		repos, err := githubOrgRepos(ctx, p, org)
		if err == errRateLimited {
			r.fail("github orgs: rate limited at %s", org)
			break
		}
		t.read(err)
		found = append(found, repos...)
	}
	r.report("github orgs", &t)
	return found
}

// flush writes the rows read so far and pkgs, and counts them in out. It writes
// on a context of its own, so what was read before the budget ran out is kept.
func (r *run) flush(parent context.Context, pkgs []packageRef, out *client.MetricsCollected) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Minute)
	defer cancel()
	r.mu.Lock()
	rows := r.rows
	r.rows = nil
	r.mu.Unlock()
	rows, dropped := validRows(rows, r.now)
	pkgs, droppedPkgs := validPackages(pkgs)
	if dropped+droppedPkgs > 0 {
		r.fail("%d figures and %d packages were refused as malformed", dropped, droppedPkgs)
	}
	for len(rows) > 0 || len(pkgs) > 0 {
		nr, np := min(len(rows), maxBatch), min(len(pkgs), maxBatch)
		cr, cp, err := checkBatch(rows[:nr], pkgs[:np], r.now)
		if err != nil {
			return err
		}
		if err := r.m.put(ctx, cr, cp); err != nil {
			return fmt.Errorf("write figures: %w", err)
		}
		out.Figures += len(cr)
		out.Packages += len(cp)
		rows, pkgs = rows[nr:], pkgs[np:]
	}
	return nil
}

// validRows keeps the rows a batch would accept, so one malformed figure costs
// itself and not the run, and removes a figure named twice.
func validRows(rows []metricRow, now time.Time) (kept []metricRow, dropped int) {
	y, mo, d := now.UTC().Date()
	latest := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	seen := map[[4]string]bool{}
	for _, row := range rows {
		k := [4]string{row.Date, row.Ecosystem, row.Name, row.Metric}
		if checkRow(row, latest) != nil || seen[k] {
			dropped++
			continue
		}
		seen[k] = true
		kept = append(kept, row)
	}
	return kept, dropped
}

func validPackages(pkgs []packageRef) (kept []packageRef, dropped int) {
	seen := map[[2]string]bool{}
	for _, p := range pkgs {
		k := [2]string{p.Ecosystem, p.Name}
		if _, err := checkPackage(p); err != nil || seen[k] {
			dropped++
			continue
		}
		seen[k] = true
		kept = append(kept, p)
	}
	return kept, dropped
}

type answeredRepo struct {
	repo  string
	stars int64
}

// github asks for every repository's counts and manifests, a hundred at a time,
// and queues the counts as figures. It answers the Hanzo-owned packages the
// manifests name and how many repositories GitHub answered for.
func (r *run) github(ctx context.Context, repos []string) ([]packageRef, int) {
	var batches [][]string
	for i := 0; i < len(repos); i += githubBatch {
		batches = append(batches, repos[i:min(i+githubBatch, len(repos))])
	}
	var (
		mu            sync.Mutex
		pkgs          []packageRef
		answered      int
		answeredRepos []answeredRepo
	)
	day := r.now.Format(dayLayout)
	var g errgroup.Group
	g.SetLimit(githubParallel)
	for i, b := range batches {
		g.Go(func() error {
			got, err := githubFigures(ctx, b)
			if err != nil {
				r.fail("github: repositories %d to %d of %d: %v", i*githubBatch+1, i*githubBatch+len(b), len(repos), err)
				return nil
			}
			var rows []metricRow
			var found []packageRef
			var seen []answeredRepo
			for _, f := range got.Repos {
				owner, _, _ := strings.Cut(f.Repo, "/")
				org := strings.ToLower(owner)
				for _, v := range []struct {
					metric string
					value  int64
				}{{"stars", f.Stars}, {"forks", f.Forks}, {"open_issues", f.Issues}, {"watchers", f.Watchers}} {
					rows = append(rows, metricRow{Date: day, Kind: "repo", Ecosystem: "github", Org: org, Name: f.Repo, Metric: v.metric, Value: v.value})
				}
				for _, p := range manifestPackages(f.Files) {
					p.Upstream = "github.com/" + f.Repo
					found = append(found, p)
				}
				seen = append(seen, answeredRepo{repo: f.Repo, stars: f.Stars})
			}
			r.add(rows...)
			mu.Lock()
			pkgs = append(pkgs, found...)
			answered += len(got.Repos)
			answeredRepos = append(answeredRepos, seen...)
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		r.fail("github: ran out of time: %v", err)
		return pkgs, answered
	}
	if len(answeredRepos) > 0 && maxReleaseRepos > 0 {
		r.releaseDownloads(ctx, answeredRepos)
	}
	return pkgs, answered
}

// releaseDownloads reads release asset download counts for the top answered
// repositories (sorted by stars descending, bounded by maxReleaseRepos and
// budget). It records the cumulative downloads_total gauge and daily
// release_downloads count (delta against the prior day's gauge once one exists,
// or the initial total on first observation when > 0).
func (r *run) releaseDownloads(ctx context.Context, repos []answeredRepo) {
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].stars != repos[j].stars {
			return repos[i].stars > repos[j].stars
		}
		return repos[i].repo < repos[j].repo
	})
	limit := min(len(repos), maxReleaseRepos)
	day := r.now.Format(dayLayout)

	listing, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	prevTotals, err := r.m.latestBefore(listing, "github", "downloads_total", day)
	if err != nil {
		prevTotals = map[string]int64{}
	}

	var t tally
	p := &pace{every: githubPublicEvery}
	for _, item := range repos[:limit] {
		if ctx.Err() != nil {
			break
		}
		total, err := githubReleaseDownloads(ctx, p, item.repo)
		if err == errRateLimited {
			r.fail("github releases: rate limited at %s", item.repo)
			break
		}
		t.read(err)
		if err != nil || total <= 0 {
			continue
		}
		owner, _, _ := strings.Cut(item.repo, "/")
		org := strings.ToLower(owner)
		delta := total
		if prev, ok := prevTotals[item.repo]; ok {
			delta = max(int64(0), total-prev)
		}
		r.add(
			metricRow{Date: day, Kind: "repo", Ecosystem: "github", Org: org, Name: item.repo, Metric: "downloads_total", Value: total},
			metricRow{Date: day, Kind: "repo", Ecosystem: "github", Org: org, Name: item.repo, Metric: "release_downloads", Value: delta},
		)
	}
	r.report("github releases", &t)
}

// registries reads every Hanzo-owned package's downloads from its registry
// (both registered and discovered directly from the registries), and every
// Docker Hub namespace an estate org holds, each registry at once. It answers
// newly discovered packages with valid estate upstreams to register.
func (r *run) registries(ctx context.Context, repos []string) ([]packageRef, error) {
	// The registered list and previous gauges are read even when the budget is
	// spent: the readers below see that and stop, and the store is not theirs to
	// cancel.
	listing, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	pkgs, err := r.m.allPackages(listing)
	if err != nil {
		return nil, fmt.Errorf("list registered packages: %w", err)
	}
	day := r.now.Format(dayLayout)
	prevCrates, err := r.m.latestBefore(listing, "crates", "downloads_total", day)
	if err != nil {
		r.fail("crates previous gauge: %v", err)
		prevCrates = map[string]int64{}
	}
	prevHub, err := r.m.latestBefore(listing, "dockerhub", "pulls_total", day)
	if err != nil {
		r.fail("dockerhub previous gauge: %v", err)
		prevHub = map[string]int64{}
	}

	var npm, pypi, crates, hub []packageRef
	for _, p := range pkgs {
		switch p.Ecosystem {
		case "npm":
			npm = append(npm, p)
		case "pypi":
			pypi = append(pypi, p)
		case "crates":
			crates = append(crates, p)
		case "dockerhub":
			hub = append(hub, p)
		}
	}
	to := r.now.AddDate(0, 0, -1).Format(dayLayout)
	from := r.now.AddDate(0, 0, -downloadDays).Format(dayLayout)
	var (
		wg     sync.WaitGroup
		found  []packageRef
		foundM sync.Mutex
	)
	addFound := func(more []packageRef) {
		if len(more) == 0 {
			return
		}
		foundM.Lock()
		found = append(found, more...)
		foundM.Unlock()
	}
	wg.Go(func() { addFound(r.npm(ctx, npm, from, to)) })
	wg.Go(func() { addFound(r.pypi(ctx, pypi, repos, from, to)) })
	wg.Go(func() { addFound(r.crates(ctx, crates, prevCrates)) })
	wg.Go(func() { addFound(r.dockerhub(ctx, hub, repos, prevHub)) })
	wg.Wait()
	if err := ctx.Err(); err != nil {
		r.fail("registries: ran out of time: %v", err)
	}
	return found, nil
}

// upstreamOwner is the lowercased GitHub owner of an upstream, the figure's org.
func upstreamOwner(upstream string) string {
	o, _, _ := strings.Cut(strings.TrimPrefix(strings.ToLower(upstream), "github.com/"), "/")
	return o
}

func (r *run) npm(ctx context.Context, registered []packageRef, from, to string) []packageRef {
	byKey := make(map[string]packageRef, len(registered))
	regKeys := make(map[string]bool, len(registered))
	for _, p := range registered {
		k := strings.ToLower(p.Name)
		byKey[k] = p
		regKeys[k] = true
	}

	var (
		discTally tally
		toReg     []packageRef
	)
	for _, scope := range estateNpmSearchScopes {
		if ctx.Err() != nil {
			break
		}
		discovered, err := discoverNpmScope(ctx, scope)
		discTally.read(err)
		for _, p := range discovered {
			k := strings.ToLower(p.Name)
			if cur, exists := byKey[k]; !exists {
				byKey[k] = p
				if p.Upstream != "" {
					toReg = append(toReg, p)
				}
			} else if cur.Upstream == "" && p.Upstream != "" {
				cur.Upstream = p.Upstream
				byKey[k] = cur
				toReg = append(toReg, cur)
			}
		}
	}
	r.report("npm discovery", &discTally)

	pkgs := make([]packageRef, 0, len(byKey))
	for _, p := range byKey {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })

	var t tally
	var g errgroup.Group
	g.SetLimit(npmParallel)
	for _, p := range pkgs {
		g.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			// Guard against previously registered third-party packages (e.g.
			// postcss, cookie, express) from forked repositories.
			if !isEstatePackageName("npm", p.Name) {
				owned, repoURL, err := verifyPackageOwnership(ctx, nil, "npm", p.Name)
				if err != nil || !owned {
					return nil
				}
				if p.Upstream == "" {
					p.Upstream = estateUpstreamFromURL(repoURL)
				}
			}
			days, err := npmDownloads(ctx, p.Name, from, to)
			t.read(err)
			r.downloads(p, "npm", "downloads", days)
			return nil
		})
	}
	_ = g.Wait()
	r.report("npm", &t)
	return toReg
}

func normPyPIName(name string) string {
	return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(strings.TrimSpace(name)))
}

func (r *run) pypi(ctx context.Context, registered []packageRef, repos []string, from, to string) []packageRef {
	byKey := make(map[string]packageRef, len(registered))
	for _, p := range registered {
		byKey[normPyPIName(p.Name)] = p
	}

	// Build bounded discovery candidates: estate seeds plus estate-prefixed repo names.
	var candidates []string
	candSeen := map[string]bool{}
	addCand := func(name string) {
		k := normPyPIName(name)
		if k == "" || candSeen[k] || byKey[k].Name != "" {
			return
		}
		candSeen[k] = true
		candidates = append(candidates, name)
	}
	for _, seed := range estatePyPISeeds {
		addCand(seed)
	}
	extras := 0
	for _, full := range repos {
		if extras >= maxPyPIProbeExtras {
			break
		}
		owner, repoName, ok := strings.Cut(full, "/")
		if !ok || !isEstateOrg(owner) || !hasEstatePrefix(repoName) {
			continue
		}
		before := len(candidates)
		addCand(repoName)
		if len(candidates) > before {
			extras++
		}
	}

	metaPace := &pace{every: pypiMetaEvery}
	var toReg []packageRef
	if ctx.Err() == nil && len(candidates) > 0 {
		discovered, err := discoverPyPICandidates(ctx, metaPace, candidates)
		if err != nil && ctx.Err() == nil {
			r.fail("pypi discovery: %v", err)
		}
		for _, p := range discovered {
			k := normPyPIName(p.Name)
			if _, exists := byKey[k]; !exists {
				byKey[k] = p
				if p.Upstream != "" {
					toReg = append(toReg, p)
				}
			}
		}
	}

	pkgs := make([]packageRef, 0, len(byKey))
	for _, p := range byKey {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })

	var t tally
	p := &pace{every: pypiEvery}
	for _, pkg := range pkgs {
		if ctx.Err() != nil {
			break
		}
		if !isEstatePackageName("pypi", pkg.Name) {
			owned, repoURL, err := verifyPackageOwnership(ctx, metaPace, "pypi", pkg.Name)
			if err != nil || !owned {
				continue
			}
			if pkg.Upstream == "" {
				pkg.Upstream = estateUpstreamFromURL(repoURL)
			}
		}
		days, err := pypiDownloads(ctx, p, pkg.Name, from, to)
		t.read(err)
		r.downloads(pkg, "pypi", "downloads", days)
	}
	r.report("pypi", &t)
	return toReg
}

func (r *run) crates(ctx context.Context, registered []packageRef, prevTotals map[string]int64) []packageRef {
	p := &pace{every: cratesEvery}
	day := r.now.Format(dayLayout)

	byKey := make(map[string]packageRef, len(registered))
	for _, pkg := range registered {
		byKey[strings.ToLower(pkg.Name)] = pkg
	}

	var (
		discTally  tally
		discovered = map[string]crateMeta{}
		toReg      []packageRef
	)
	for _, prefix := range estateCratesSearchPrefixes {
		if ctx.Err() != nil {
			break
		}
		found, err := discoverCratesPrefix(ctx, p, prefix)
		discTally.read(err)
		for _, c := range found {
			k := strings.ToLower(c.Name)
			discovered[k] = c
			up := estateUpstreamFromURL(c.Repository)
			if up == "" {
				up = estateUpstreamFromURL(c.Homepage)
			}
			if cur, exists := byKey[k]; !exists {
				ref := packageRef{Ecosystem: "crates", Name: c.Name, Upstream: up}
				byKey[k] = ref
				if up != "" {
					toReg = append(toReg, ref)
				}
			} else if cur.Upstream == "" && up != "" {
				cur.Upstream = up
				byKey[k] = cur
				toReg = append(toReg, cur)
			}
		}
	}
	r.report("crates discovery", &discTally)

	pkgs := make([]packageRef, 0, len(byKey))
	for _, pkg := range byKey {
		pkgs = append(pkgs, pkg)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })

	var t tally
	for _, pkg := range pkgs {
		if ctx.Err() != nil {
			break
		}
		k := strings.ToLower(pkg.Name)
		info, preloaded := discovered[k]
		if !preloaded {
			var err error
			info, err = cratesInfo(ctx, p, pkg.Name)
			t.read(err)
			if err != nil {
				continue
			}
			if !isOwnedPackage("crates", info.Name, info.repoURL()) {
				continue
			}
		}
		org := estateOrgForPackage("crates", pkg.Name, pkg.Upstream)
		r.add(metricRow{Date: day, Kind: "package", Ecosystem: "crates", Org: org, Name: pkg.Name, Metric: "downloads_total", Value: info.Downloads})
		if prev, ok := prevTotals[pkg.Name]; ok {
			delta := max(int64(0), info.Downloads-prev)
			r.add(metricRow{Date: day, Kind: "package", Ecosystem: "crates", Org: org, Name: pkg.Name, Metric: "downloads", Value: delta})
		}
	}
	r.report("crates", &t)
	return toReg
}

func (r *run) downloads(p packageRef, eco, metric string, days []dayCount) {
	org := estateOrgForPackage(eco, p.Name, p.Upstream)
	rows := make([]metricRow, 0, len(days))
	for _, d := range days {
		rows = append(rows, metricRow{Date: d.Day, Kind: "package", Ecosystem: eco, Org: org, Name: p.Name, Metric: metric, Value: d.Count})
	}
	r.add(rows...)
}

// dockerhub reads the pulls of every public repository in each namespace an
// estate org holds, or a registered Docker Hub package names. A repository whose
// namespace and name are a repository of the estate's is registered against it.
// When a previous day's pulls_total gauge exists, daily pulls are also written.
func (r *run) dockerhub(ctx context.Context, registered []packageRef, repos []string, prevTotals map[string]int64) []packageRef {
	byName := make(map[string]string, len(repos))
	spaces := map[string]bool{}
	for _, full := range repos {
		byName[strings.ToLower(full)] = full
		spaces[upstreamOwner(full)] = true
	}
	for _, p := range registered {
		ns, _, _ := strings.Cut(p.Name, "/")
		spaces[strings.ToLower(ns)] = true
	}
	names := make([]string, 0, len(spaces))
	for ns := range spaces {
		names = append(names, ns)
	}
	sort.Strings(names)

	var t tally
	var found []packageRef
	p := &pace{}
	day := r.now.Format(dayLayout)
	for _, ns := range names {
		if ctx.Err() != nil {
			break
		}
		repos, err := hubPulls(ctx, p, ns)
		t.read(err)
		for _, h := range repos {
			name := ns + "/" + h.Name
			r.add(metricRow{Date: day, Kind: "package", Ecosystem: "dockerhub", Org: ns, Name: name, Metric: "pulls_total", Value: h.Pulls})
			if prev, ok := prevTotals[name]; ok {
				delta := max(int64(0), h.Pulls-prev)
				r.add(metricRow{Date: day, Kind: "package", Ecosystem: "dockerhub", Org: ns, Name: name, Metric: "pulls", Value: delta})
			}
			if up, ok := byName[strings.ToLower(name)]; ok {
				found = append(found, packageRef{Ecosystem: "dockerhub", Name: name, Upstream: "github.com/" + up})
			}
		}
	}
	r.report("dockerhub", &t)
	return found
}
