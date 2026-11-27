package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// collect_registry.go — what the package registries and public GitHub APIs say
// of a package or org (#456). Each reads a public API with no credential, as a
// generic client.

// The registries' API roots. Package vars so a test points them at a server.
var (
	npmAPI          = "https://api.npmjs.org"
	npmRegistryAPI  = "https://registry.npmjs.org"
	pypiAPI         = "https://pypistats.org/api"
	pypiRegistryAPI = "https://pypi.org"
	cratesAPI       = "https://crates.io/api/v1"
	hubAPI          = "https://hub.docker.com/v2"
	githubPublicAPI = "https://api.github.com"
)

// collectAgent names this client to the registries, as they ask.
const collectAgent = "hanzo-oss-metrics/1 (+https://hanzo.ai)"

// collectHTTP is the client every registry read goes through.
var collectHTTP = &http.Client{Timeout: 30 * time.Second}

// collectBackoff is the wait before the first retry; each retry doubles it. A
// package var so a test does not sit through it.
var collectBackoff = time.Second

// pypiMetaEvery and githubPublicEvery space metadata and public GitHub reads.
var (
	pypiMetaEvery     = 100 * time.Millisecond
	githubPublicEvery = 100 * time.Millisecond
)

const (
	// maxRetries is how many times one read is tried again after a 429, a 5xx or
	// a connection that failed.
	maxRetries = 3
	// maxRetryWait caps one wait, a registry's Retry-After included.
	maxRetryWait = time.Minute
	// maxRegistryBody bounds one registry answer.
	maxRegistryBody = 16 << 20
)

// errAbsent is a package, org or namespace the registry does not hold.
var errAbsent = errors.New("not on the registry")

// errRateLimited signals an upstream API quota exhaustion (e.g. GitHub 403
// with X-RateLimit-Remaining: 0) so callers stop probing instead of hammering.
var errRateLimited = errors.New("rate limited")

// pace spaces the reads of one registry that asks to be read slowly.
type pace struct {
	every time.Duration
	mu    sync.Mutex
	next  time.Time
}

func (p *pace) wait(ctx context.Context) error {
	if p == nil || p.every <= 0 {
		return ctx.Err()
	}
	p.mu.Lock()
	now := time.Now()
	at := p.next
	if at.Before(now) {
		at = now
	}
	p.next = at.Add(p.every)
	p.mu.Unlock()
	select {
	case <-time.After(at.Sub(now)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getJSON reads u into out: spaced by p, tried again with a doubling wait on a
// 429, a 5xx or a connection that failed, errAbsent on a 404, and
// errRateLimited on a GitHub-style 403 quota exhaustion. Any other status is
// the answer's own error.
func getJSON(ctx context.Context, p *pace, u string, out any) error {
	wait := collectBackoff
	var last error
	for try := 0; try <= maxRetries; try++ {
		if try > 0 {
			select {
			case <-time.After(min(wait, maxRetryWait)):
			case <-ctx.Done():
				return ctx.Err()
			}
			wait *= 2
		}
		if err := p.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", collectAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := collectHTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxRegistryBody))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return errAbsent
		case resp.StatusCode == http.StatusForbidden &&
			(resp.Header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(string(body)), "rate limit")):
			return errRateLimited
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			last = fmt.Errorf("%s answered %d", u, resp.StatusCode)
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			continue
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("%s answered %d", u, resp.StatusCode)
		case rerr != nil:
			last = rerr
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("%s: decode: %w", u, err)
		}
		return nil
	}
	return last
}

// dayCount is one day's figure.
type dayCount struct {
	Day   string
	Count int64
}

func escapeNpmPath(name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// npmDownloads is a package's downloads per day from..to, inclusive.
func npmDownloads(ctx context.Context, name, from, to string) ([]dayCount, error) {
	var out struct {
		Downloads []struct {
			Day       string `json:"day"`
			Downloads int64  `json:"downloads"`
		} `json:"downloads"`
	}
	if err := getJSON(ctx, nil, npmAPI+"/downloads/range/"+from+":"+to+"/"+escapeNpmPath(name), &out); err != nil {
		return nil, err
	}
	days := make([]dayCount, 0, len(out.Downloads))
	for _, d := range out.Downloads {
		days = append(days, dayCount{d.Day, d.Downloads})
	}
	return days, nil
}

// npmPackageRepo reads an npm package's repository or homepage URL from the
// npm registry metadata endpoint.
func npmPackageRepo(ctx context.Context, name string) (string, error) {
	var out struct {
		Repository npmRepoField `json:"repository"`
		Homepage   string       `json:"homepage"`
	}
	if err := getJSON(ctx, nil, npmRegistryAPI+"/"+escapeNpmPath(name), &out); err != nil {
		return "", err
	}
	if out.Repository.URL != "" {
		return out.Repository.URL, nil
	}
	return out.Homepage, nil
}

// discoverNpmScope searches the npm registry for packages published under an
// estate scope (e.g. "hanzo" for @hanzo/*) so monorepo workspace packages are
// discovered even when a repository's root package.json is private.
func discoverNpmScope(ctx context.Context, scope string) ([]packageRef, error) {
	const pageSize = 250
	var found []packageRef
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		if ctx.Err() != nil {
			break
		}
		offset := page * pageSize
		u := fmt.Sprintf("%s/-/v1/search?text=scope:%s&size=%d&from=%d",
			npmRegistryAPI, url.QueryEscape(scope), pageSize, offset)
		var out struct {
			Objects []struct {
				Package struct {
					Name  string `json:"name"`
					Links struct {
						Repository string `json:"repository"`
						Homepage   string `json:"homepage"`
					} `json:"links"`
				} `json:"package"`
			} `json:"objects"`
			Total int `json:"total"`
		}
		if err := getJSON(ctx, nil, u, &out); err != nil {
			if err == errAbsent {
				return found, nil
			}
			return found, err
		}
		for _, obj := range out.Objects {
			name := strings.TrimSpace(obj.Package.Name)
			if len(name) > maxNameLen || !npmName.MatchString(name) {
				continue
			}
			repoURL := obj.Package.Links.Repository
			if repoURL == "" {
				repoURL = obj.Package.Links.Homepage
			}
			if !isOwnedPackage("npm", name, repoURL) {
				continue
			}
			key := strings.ToLower(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			up := estateUpstreamFromURL(obj.Package.Links.Repository)
			if up == "" {
				up = estateUpstreamFromURL(obj.Package.Links.Homepage)
			}
			found = append(found, packageRef{Ecosystem: "npm", Name: name, Upstream: up})
		}
		if len(out.Objects) < pageSize || offset+len(out.Objects) >= out.Total {
			break
		}
	}
	return found, nil
}

// pypiDownloads is a project's downloads per day from..to, inclusive, counting
// no mirrors.
func pypiDownloads(ctx context.Context, p *pace, name, from, to string) ([]dayCount, error) {
	var out struct {
		Data []struct {
			Category  string `json:"category"`
			Date      string `json:"date"`
			Downloads int64  `json:"downloads"`
		} `json:"data"`
	}
	norm := strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(name))
	if err := getJSON(ctx, p, pypiAPI+"/packages/"+url.PathEscape(norm)+"/overall?mirrors=false", &out); err != nil {
		return nil, err
	}
	var days []dayCount
	for _, d := range out.Data {
		if d.Category == "without_mirrors" && d.Date >= from && d.Date <= to {
			days = append(days, dayCount{d.Date, d.Downloads})
		}
	}
	return days, nil
}

// pypiPackageMeta fetches a project's canonical name and repository URL from
// the PyPI JSON API.
func pypiPackageMeta(ctx context.Context, p *pace, name string) (pkgName, repoURL string, err error) {
	var out struct {
		Info struct {
			Name        string            `json:"name"`
			HomePage    string            `json:"home_page"`
			ProjectURLs map[string]string `json:"project_urls"`
		} `json:"info"`
	}
	if err := getJSON(ctx, p, pypiRegistryAPI+"/pypi/"+url.PathEscape(name)+"/json", &out); err != nil {
		return "", "", err
	}
	pkgName = strings.TrimSpace(out.Info.Name)
	if pkgName == "" {
		pkgName = name
	}
	return pkgName, bestPyPIRepoURL(out.Info.ProjectURLs, out.Info.HomePage), nil
}

func bestPyPIRepoURL(urls map[string]string, homePage string) string {
	keys := make([]string, 0, len(urls))
	for k := range urls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// First pass: prefer any explicit estate GitHub URL.
	for _, k := range keys {
		if isEstateRepoURL(urls[k]) {
			return urls[k]
		}
	}
	if isEstateRepoURL(homePage) {
		return homePage
	}
	// Second pass: prefer explicit repository/source links (so third-party
	// repos are accurately detected and rejected).
	for _, k := range keys {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "repo") || strings.Contains(lk, "source") || strings.Contains(lk, "github") || strings.Contains(lk, "code") {
			if v := strings.TrimSpace(urls[k]); v != "" {
				return v
			}
		}
	}
	for _, k := range keys {
		if _, _, ok := extractGitHubOwner(urls[k]); ok {
			return urls[k]
		}
	}
	return strings.TrimSpace(homePage)
}

// discoverPyPICandidates probes PyPI JSON metadata for candidate estate package
// names, keeping only packages confirmed to belong to the Hanzo estate.
func discoverPyPICandidates(ctx context.Context, p *pace, candidates []string) ([]packageRef, error) {
	var found []packageRef
	seen := map[string]bool{}
	var firstErr error
	for _, cand := range candidates {
		if ctx.Err() != nil {
			break
		}
		norm := strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(strings.TrimSpace(cand)))
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		pkgName, repoURL, err := pypiPackageMeta(ctx, p, cand)
		if err == errAbsent {
			continue
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !pypiName.MatchString(pkgName) || !isOwnedPackage("pypi", pkgName, repoURL) {
			continue
		}
		found = append(found, packageRef{
			Ecosystem: "pypi",
			Name:      pkgName,
			Upstream:  estateUpstreamFromURL(repoURL),
		})
	}
	return found, firstErr
}

// crateMeta is a crate's download count and repository metadata from crates.io.
type crateMeta struct {
	Name       string `json:"name"`
	Downloads  int64  `json:"downloads"`
	Repository string `json:"repository"`
	Homepage   string `json:"homepage"`
}

func (c crateMeta) repoURL() string {
	if c.Repository != "" {
		return c.Repository
	}
	return c.Homepage
}

// cratesInfo reads a crate's all-time downloads and repository metadata in one
// crates.io request.
func cratesInfo(ctx context.Context, p *pace, name string) (crateMeta, error) {
	var out struct {
		Crate crateMeta `json:"crate"`
	}
	if err := getJSON(ctx, p, cratesAPI+"/crates/"+url.PathEscape(name), &out); err != nil {
		return crateMeta{}, err
	}
	if out.Crate.Name == "" {
		out.Crate.Name = name
	}
	return out.Crate, nil
}

// cratesDownloads is a crate's all-time downloads.
func cratesDownloads(ctx context.Context, p *pace, name string) (int64, error) {
	info, err := cratesInfo(ctx, p, name)
	if err != nil {
		return 0, err
	}
	return info.Downloads, nil
}

// discoverCratesPrefix searches crates.io for a Hanzo estate prefix and returns
// only crates owned by the Hanzo estate.
func discoverCratesPrefix(ctx context.Context, p *pace, prefix string) ([]crateMeta, error) {
	const perPage = 100
	var found []crateMeta
	seen := map[string]bool{}
	for page := 1; page <= 5; page++ {
		if ctx.Err() != nil {
			break
		}
		u := fmt.Sprintf("%s/crates?q=%s&per_page=%d&page=%d",
			cratesAPI, url.QueryEscape(prefix), perPage, page)
		var out struct {
			Crates []crateMeta `json:"crates"`
			Meta   struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := getJSON(ctx, p, u, &out); err != nil {
			if err == errAbsent {
				return found, nil
			}
			return found, err
		}
		for _, c := range out.Crates {
			if !cratesName.MatchString(c.Name) || !isOwnedPackage("crates", c.Name, c.repoURL()) {
				continue
			}
			key := strings.ToLower(c.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, c)
		}
		if len(out.Crates) < perPage || page*perPage >= out.Meta.Total {
			break
		}
	}
	return found, nil
}

// verifyPackageOwnership confirms via live registry metadata whether a package
// whose name does not carry a Hanzo scope/prefix actually points back to a
// Hanzo estate GitHub organization.
func verifyPackageOwnership(ctx context.Context, p *pace, eco, name string) (owned bool, repoURL string, err error) {
	if isEstateScopedPackage(eco, name) {
		return true, "", nil
	}
	switch eco {
	case "npm":
		repoURL, err = npmPackageRepo(ctx, name)
		if err != nil {
			return false, "", err
		}
		return isOwnedPackage("npm", name, repoURL), repoURL, nil
	case "pypi":
		var canonical string
		canonical, repoURL, err = pypiPackageMeta(ctx, p, name)
		if err != nil {
			return false, "", err
		}
		return isOwnedPackage("pypi", canonical, repoURL), repoURL, nil
	case "crates":
		var info crateMeta
		info, err = cratesInfo(ctx, p, name)
		if err != nil {
			return false, "", err
		}
		repoURL = info.repoURL()
		return isOwnedPackage("crates", info.Name, repoURL), repoURL, nil
	case "dockerhub":
		return isEstatePackageName("dockerhub", name), "", nil
	default:
		return false, "", nil
	}
}

// hubRepo is one Docker Hub repository's all-time pulls.
type hubRepo struct {
	Name  string `json:"name"`
	Pulls int64  `json:"pull_count"`
}

// hubPulls is every public repository of a Docker Hub namespace, with its pulls.
func hubPulls(ctx context.Context, p *pace, namespace string) ([]hubRepo, error) {
	var all []hubRepo
	next := hubAPI + "/repositories/" + url.PathEscape(namespace) + "/?page_size=100"
	for pages := 0; next != "" && pages < 50; pages++ {
		var out struct {
			Next    string    `json:"next"`
			Results []hubRepo `json:"results"`
		}
		if err := getJSON(ctx, p, next, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Results...)
		// Only the registry's own pages are followed.
		if next = out.Next; !strings.HasPrefix(next, hubAPI+"/") {
			break
		}
	}
	return all, nil
}

// githubOrgRepos lists the public repositories of a GitHub organization via
// the public REST API (up to 5 pages of 100 repositories).
func githubOrgRepos(ctx context.Context, p *pace, org string) ([]string, error) {
	const perPage = 100
	var out []string
	for page := 1; page <= 5; page++ {
		if ctx.Err() != nil {
			break
		}
		u := fmt.Sprintf("%s/orgs/%s/repos?type=public&per_page=%d&page=%d",
			githubPublicAPI, url.PathEscape(org), perPage, page)
		var batch []struct {
			FullName string `json:"full_name"`
			Private  bool   `json:"private"`
		}
		if err := getJSON(ctx, p, u, &batch); err != nil {
			if err == errAbsent {
				return out, nil
			}
			return out, err
		}
		for _, r := range batch {
			if !r.Private && strings.Contains(r.FullName, "/") {
				out = append(out, r.FullName)
			}
		}
		if len(batch) < perPage {
			break
		}
	}
	return out, nil
}

// githubReleaseDownloads sums download_count across every release asset of
// owner/repo via GitHub's public releases API (up to 3 pages of 100 releases).
func githubReleaseDownloads(ctx context.Context, p *pace, repo string) (int64, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return 0, nil
	}
	const perPage = 100
	var total int64
	for page := 1; page <= 3; page++ {
		if ctx.Err() != nil {
			break
		}
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d&page=%d",
			githubPublicAPI, url.PathEscape(owner), url.PathEscape(name), perPage, page)
		var batch []struct {
			Draft  bool `json:"draft"`
			Assets []struct {
				DownloadCount int64 `json:"download_count"`
			} `json:"assets"`
		}
		if err := getJSON(ctx, p, u, &batch); err != nil {
			if err == errAbsent {
				return total, nil
			}
			return total, err
		}
		for _, rel := range batch {
			if rel.Draft {
				continue
			}
			for _, a := range rel.Assets {
				if a.DownloadCount > 0 {
					total += a.DownloadCount
				}
			}
		}
		if len(batch) < perPage {
			break
		}
	}
	return total, nil
}
