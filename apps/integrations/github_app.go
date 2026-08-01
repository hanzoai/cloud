package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// github_app.go is the GitHub-App engine behind the provider (github.go): it mints
// short-lived installation access tokens from the App private key, lists the repos
// an installation grants, and serves the two authed sync routes — GET
// /v1/integrations/github/repos and POST /v1/integrations/github/repos/import. The
// actual git object work (create + mirror-in) crosses into clients/git via the
// cloud.GitImporter seam, so this file never imports the git package.
//
// TOKENS ARE NEVER STORED. Only the installation_id is custodied (the connection
// ExternalID). Every list/import mints a fresh 1-hour installation token via
// ghinstallation (the same library the CI runner uses), cached per-installation
// until ~10m before expiry, and hands it to git through the seam req — never a log,
// never argv.

// githubAPIBase is the GitHub API root (api.github.com, or a GitHub Enterprise host
// via GITHUB_API_URL). A package var so a test can point the App-JWT + installation
// calls at a mock server; never mutated in production.
var githubAPIBase = func() string {
	if v := strings.TrimSpace(os.Getenv("GITHUB_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}()

var githubHTTP = &http.Client{Timeout: 30 * time.Second}

// ghAppState caches the App transport + per-installation tokens. The AppsTransport
// is rebuilt only when the App creds/base change (fingerprint), and installation
// tokens are re-minted lazily near expiry — so a bulk import of N repos mints one
// token, not N.
type ghAppState struct {
	mu        sync.Mutex
	tr        *ghinstallation.AppsTransport
	loadedFor string // fingerprint of (appID, key length, base) — rebuild on change
	tokens    map[int64]cachedInstallToken
}

type cachedInstallToken struct {
	token string
	exp   time.Time
}

var ghApp = &ghAppState{tokens: map[int64]cachedInstallToken{}}

// transport returns the App JWT transport, (re)building it from the current ENV.
// Fails closed when the App is not configured. The build fingerprint includes the
// API base so a test that repoints githubAPIBase rebuilds (and drops stale tokens).
func (g *ghAppState) transport() (*ghinstallation.AppsTransport, error) {
	appID := strings.TrimSpace(os.Getenv(githubAppIDEnv))
	pem := strings.TrimSpace(os.Getenv(githubAppKeyEnv))
	if appID == "" || pem == "" {
		return nil, fmt.Errorf("github app not configured: set %s and %s", githubAppIDEnv, githubAppKeyEnv)
	}
	base := strings.TrimRight(githubAPIBase, "/")
	fp := appID + "|" + strconv.Itoa(len(pem)) + "|" + base
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tr != nil && g.loadedFor == fp {
		return g.tr, nil
	}
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s must be numeric: %w", githubAppIDEnv, err)
	}
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, id, []byte(pem))
	if err != nil {
		return nil, fmt.Errorf("load github app key: %w", err)
	}
	tr.BaseURL = base
	g.tr, g.loadedFor = tr, fp
	g.tokens = map[int64]cachedInstallToken{} // creds/base changed → drop stale tokens
	return tr, nil
}

// installationToken mints (or returns a cached) installation access token for id.
// Cached until 10m before expiry so a burst of operations reuses one token.
func (g *ghAppState) installationToken(ctx context.Context, id int64) (string, error) {
	g.mu.Lock()
	if c, ok := g.tokens[id]; ok && time.Until(c.exp) > 10*time.Minute {
		t := c.token
		g.mu.Unlock()
		return t, nil
	}
	g.mu.Unlock()
	tr, err := g.transport()
	if err != nil {
		return "", err
	}
	itr := ghinstallation.NewFromAppsTransport(tr, id)
	itr.BaseURL = strings.TrimRight(githubAPIBase, "/")
	tok, err := itr.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("mint installation token: %w", err)
	}
	g.mu.Lock()
	g.tokens[id] = cachedInstallToken{token: tok, exp: time.Now().Add(50 * time.Minute)} // TTL=1h
	g.mu.Unlock()
	return tok, nil
}

// InstallationToken mints a fresh short-lived GitHub App installation token for
// one of org's connected GitHub accounts. Fails closed (error, never a value)
// when that account is not connected or the App creds are absent. Called by the
// git object plane (outbound mirror) and the sync handlers below.
//
// The owner is required because a GitHub App is installed PER ACCOUNT: an org
// that owns hanzoai, hanzo-apps and hanzo-docs holds three installations, and a
// token minted for one grants nothing on the others. An empty owner selects the
// org's single connection when it has exactly one, which is what a caller with
// no account in hand can correctly mean; with several it is ambiguous and fails
// rather than guessing.
func InstallationToken(ctx context.Context, org, owner string) (string, error) {
	if mounted == nil {
		return "", fmt.Errorf("integrations: not mounted")
	}
	conn, err := githubConnection(org, owner)
	if err != nil {
		return "", err
	}
	id, perr := strconv.ParseInt(strings.TrimSpace(conn.ExternalID), 10, 64)
	if perr != nil {
		return "", fmt.Errorf("integrations: invalid github installation id")
	}
	return ghApp.installationToken(ctx, id)
}

// githubConnection resolves which of an org's GitHub accounts a call means.
//
// A named owner selects its own connection. When no connection carries that name
// the org's SINGLE connection answers if it has exactly one: a row predating the
// owner key, or one whose account was renamed on GitHub, still holds the right
// installation, and refusing it would break a working mirror over a label. With
// several accounts there is no such fallback — the name is then the only thing
// distinguishing them, and guessing would mint a token for the wrong account.
func githubConnection(org, owner string) (Connection, error) {
	if owner != "" {
		if conn, ok := ConnectionFor(org, "github", owner); ok {
			return conn, nil
		}
		if conns := Connections(org, "github"); len(conns) == 1 {
			return conns[0], nil
		}
		return Connection{}, fmt.Errorf("integrations: github account %q is not connected for this org", owner)
	}
	conns := Connections(org, "github")
	switch len(conns) {
	case 0:
		return Connection{}, fmt.Errorf("integrations: github not connected for org")
	case 1:
		return conns[0], nil
	default:
		names := make([]string, 0, len(conns))
		for _, c := range conns {
			names = append(names, c.Owner)
		}
		return Connection{}, fmt.Errorf("integrations: this org has %d connected github accounts (%s); name the one to use",
			len(conns), strings.Join(names, ", "))
	}
}

// githubInstallationAccount fetches an installation's account login via the App JWT,
// which both VALIDATES the installation (the App can see it) and yields the human
// label. Used by githubExchange at connect time.
func githubInstallationAccount(ctx context.Context, id int64) (string, error) {
	tr, err := ghApp.transport()
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(githubAPIBase, "/") + "/app/installations/" + strconv.FormatInt(id, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// The AppsTransport signs the request with the App JWT.
	resp, err := (&http.Client{Transport: tr, Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("github call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("github http %d: %s", resp.StatusCode, truncateBody(body))
	}
	var out struct {
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("github decode: %w", err)
	}
	return out.Account.Login, nil
}

// ── GitHub API: the installation's repositories ──────────────────────────────

type githubRepo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
}

// installationRepos lists every repository the installation token grants, following
// pagination. Bounded (per_page=100, page cap) so a huge org can't exhaust memory.
func installationRepos(ctx context.Context, token string) ([]githubRepo, error) {
	const perPage = 100
	const maxPages = 100 // 10k repos ceiling
	var all []githubRepo
	for page := 1; page <= maxPages; page++ {
		endpoint := fmt.Sprintf("%s/installation/repositories?per_page=%d&page=%d",
			strings.TrimRight(githubAPIBase, "/"), perPage, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := githubHTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github call: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("github http %d: %s", resp.StatusCode, truncateBody(body))
		}
		var pageResp struct {
			TotalCount   int          `json:"total_count"`
			Repositories []githubRepo `json:"repositories"`
		}
		if err := json.Unmarshal(body, &pageResp); err != nil {
			return nil, fmt.Errorf("github decode: %w", err)
		}
		all = append(all, pageResp.Repositories...)
		if len(pageResp.Repositories) < perPage || (pageResp.TotalCount > 0 && len(all) >= pageResp.TotalCount) {
			break
		}
	}
	return all, nil
}

// ── handlers: list + import ──────────────────────────────────────────────────

type githubRepoView struct {
	// Name is the repository's short name within the installation.
	Name string `json:"name"`
	// FullName is GitHub's owner/name.
	FullName string `json:"fullName"`
	// Private is GitHub's visibility bit for the repo.
	Private bool `json:"private"`
	// DefaultBranch is the repo's default branch at GitHub.
	DefaultBranch string `json:"defaultBranch"`
	// Imported is whether this repo has been mirrored into git.hanzo.ai.
	Imported bool `json:"imported"`
	// SyncStatus is "synced", "conflict", or "" when the repo is not imported.
	SyncStatus string `json:"syncStatus"`
	// LastSyncedAt is the last successful mirror, RFC 3339 UTC. Absent if never.
	LastSyncedAt string `json:"lastSyncedAt,omitempty"`
	// HTMLURL is the repo's page at GitHub.
	HTMLURL string `json:"htmlUrl,omitempty"`
}

// githubReposOut is the org's granted repository set.
type githubReposOut struct {
	// Repos is every repo the installation grants. Never null; [] when none.
	Repos []githubRepoView `json:"repos"`
}

// githubRepos lists the org's granted GitHub repositories, each annotated with its
// native import + sync status from the git object plane. Org-authed: the org comes
// from the validated principal, and the granted set is bounded to THAT org's
// installation token — an org can never enumerate another org's repos. The console
// polls it to watch an import flip a repo to imported.
//
// Response: {"repos":[{"name":"widgets","fullName":"acme/widgets","private":true,"defaultBranch":"main","imported":true,"syncStatus":"synced","lastSyncedAt":"2026-07-01T10:00:00Z","htmlUrl":"https://github.com/acme/widgets"}]}
func (o ops) githubRepos(ctx context.Context, _ *noArgs) (*githubReposOut, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	repos, err := reachableRepos(ctx, org)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Name)
	}
	// Best-effort status roll-up from the git object plane (nil if git is unmounted).
	status, _ := cloud.GitRepoStatuses(ctx, org, "", names)
	out := make([]githubRepoView, 0, len(repos))
	for _, r := range repos {
		v := githubRepoView{
			Name: r.Name, FullName: r.FullName, Private: r.Private,
			DefaultBranch: r.DefaultBranch, HTMLURL: r.HTMLURL,
		}
		if st, ok := status[r.Name]; ok && st.Imported {
			v.Imported = true
			v.SyncStatus = "synced"
			if st.Conflict {
				v.SyncStatus = "conflict"
			}
			if st.LastSyncedAt > 0 {
				v.LastSyncedAt = rfc3339(st.LastSyncedAt)
			}
		}
		out = append(out, v)
	}
	return &githubReposOut{Repos: out}, nil
}

// githubImportIn selects which of the installation's granted repositories to
// import. Give repos[] or all:true — neither is a 400, because "import nothing"
// is not a request worth queueing.
type githubImportIn struct {
	// Repos names the repositories to import, either owner-qualified
	// ("hanzo-apps/ai") or as a bare name ("ai"); a trailing ".git" is stripped.
	// A bare name that matches more than one granted repository is an error
	// rather than a guess, because one Hanzo org may hold several GitHub
	// installations and a name is only unique within an owner.
	// Ignored when all is true.
	Repos []string `json:"repos"`
	// All imports every repository the installation grants, instead of naming
	// them. Archived and disabled repositories are skipped either way — they
	// cannot be fetched.
	All bool `json:"all"`
}

// githubImportOut acknowledges the queued import. It reports what was ACCEPTED,
// not what has landed: the import itself runs in the background, so poll GET
// /v1/integrations/github/repos for each repository's status to flip to imported.
type githubImportOut struct {
	// Queued is how many repositories were handed to the background importer.
	Queued int `json:"queued"`
	// Repos names those repositories, in the installation's listing order.
	Repos []string `json:"repos"`
}

type githubImportItem struct {
	Name string
	// Owner is the GitHub account the repo belongs to. Carried because the import
	// mints a token per account, and a token from the wrong one cannot clone.
	Owner    string
	CloneURL string
}

// selectImports resolves the caller's selectors against the installation's granted
// set. A selector is either owner-qualified ("hanzo-apps/ai") or a bare name
// ("ai"); the qualified form matches FullName and the bare form matches Name.
//
// A bare name is only unique within one owner, and a Hanzo org may hold several
// GitHub installations — hanzoai/ai, hanzo-apps/ai and hanzo-docs/ai are three
// repositories. A bare name reaching more than one of them is an error, so the
// caller names the one it meant instead of receiving whichever the listing
// reached first.
func selectImports(granted []githubRepo, repos []string, all bool) ([]githubImportItem, error) {
	want := map[string]bool{}
	for _, n := range repos {
		want[strings.TrimSuffix(strings.TrimSpace(n), ".git")] = true
	}
	hits := map[string][]string{} // selector -> the full names it reached
	var items []githubImportItem
	for _, r := range granted {
		if r.Archived || r.Disabled { // un-fetchable — skip, never fabricate an import
			continue
		}
		switch {
		case all:
		case want[r.FullName]:
			hits[r.FullName] = append(hits[r.FullName], r.FullName)
		case want[r.Name]:
			hits[r.Name] = append(hits[r.Name], r.FullName)
		default:
			continue
		}
		owner, _, _ := splitFullName(r.FullName)
		items = append(items, githubImportItem{Name: r.Name, Owner: owner, CloneURL: r.CloneURL})
	}
	for sel, full := range hits {
		if len(full) > 1 {
			sort.Strings(full)
			return nil, zip.ErrBadRequest(fmt.Sprintf("%q matches %s; name one of them", sel, strings.Join(full, ", ")))
		}
	}
	if len(items) == 0 {
		return nil, zip.ErrBadRequest("no matching repositories are granted to the installation")
	}
	return items, nil
}

// GithubImport imports the selected (or all) granted repos into git.hanzo.ai. The
// selection is intersected with the installation's GRANTED set, so a client can
// never import a repo the App was not granted (org isolation + a grant check). The
// import runs in a bounded background worker (don't block the request), so the
// answer is 202 Accepted; poll GET /v1/integrations/github/repos for the per-repo
// status to flip to imported.
//
// Example: {"repos":["widgets"]}
// Response: {"queued":1,"repos":["widgets"]}
func (o ops) githubImport(ctx context.Context, in *githubImportIn) (*githubImportOut, error) {
	org, err := authed(ctx, principalRequired)
	if err != nil {
		return nil, err
	}
	if !in.All && len(in.Repos) == 0 {
		return nil, zip.ErrBadRequest("provide repos[] or all:true")
	}
	granted, err := reachableRepos(ctx, org)
	if err != nil {
		return nil, err
	}
	items, err := selectImports(granted, in.Repos, in.All)
	if err != nil {
		return nil, err
	}
	spawnImport(org, items)
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return &githubImportOut{Queued: len(items), Repos: names}, nil
}

// githubTokenForOrg resolves the org's installation token with honest HTTP errors:
// 503 when the App is not configured, 409 when this org has not connected GitHub,
// 502 when the mint itself fails.
func githubTokenFor(ctx context.Context, org, owner string) (string, error) {
	if !githubConfigured() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "github integration is not configured on this deployment")
	}
	if len(Connections(org, "github")) == 0 {
		return "", zip.Errorf(http.StatusConflict, "github is not connected for this organization")
	}
	tok, err := InstallationToken(ctx, org, owner)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "mint github installation token: %v", err)
	}
	return tok, nil
}

// grantedRepos lists every repository an org can reach, across ALL of its
// connected GitHub accounts. Each account is a separate installation with its own
// token, so this mints one per account and unions the results.
//
// A single account failing does not empty the list: its error is carried back so
// a caller can report a partial view honestly, while the accounts that answered
// still return their repositories. Losing every repo because one installation was
// revoked would be worse than reporting the gap.
func reachableRepos(ctx context.Context, org string) ([]githubRepo, error) {
	conns := Connections(org, "github")
	if len(conns) == 0 {
		return nil, zip.Errorf(http.StatusConflict, "github is not connected for this organization")
	}
	if !githubConfigured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "github integration is not configured on this deployment")
	}
	// The accounts are read CONCURRENTLY. Each is an independent installation with
	// its own token and its own pagination, so reading them in series made the wall
	// clock their SUM — 816 repos across three accounts is ten sequential pages,
	// about seven seconds, and under load the largest account exceeded the request
	// deadline and dropped out of the union. Degrading to the accounts that
	// answered is right (below), but an account should not fail merely for being
	// listed last.
	type result struct {
		owner string
		repos []githubRepo
		err   error
	}
	out := make([]result, len(conns))
	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Add(1)
		go func(i int, owner string) {
			defer wg.Done()
			out[i] = result{owner: owner}
			tok, err := InstallationToken(ctx, org, owner)
			if err != nil {
				out[i].err = err
				return
			}
			out[i].repos, out[i].err = installationRepos(ctx, tok)
		}(i, c.Owner)
	}
	wg.Wait()

	// Merged in CONNECTION order, not completion order, so the same set of
	// accounts always yields the same list — a caller paging this cannot have rows
	// reshuffle because one account happened to answer first.
	var (
		all      []githubRepo
		failures []string
	)
	seen := map[string]bool{}
	for _, r := range out {
		if r.err != nil {
			failures = append(failures, r.owner)
			continue
		}
		for _, repo := range r.repos {
			if seen[repo.FullName] {
				continue
			}
			seen[repo.FullName] = true
			all = append(all, repo)
		}
	}
	if len(all) == 0 && len(failures) > 0 {
		return nil, zip.Errorf(http.StatusBadGateway, "list github repositories: every connected account failed (%s)", strings.Join(failures, ", "))
	}
	return all, nil
}

// ── bounded background import ─────────────────────────────────────────────────

// importSem globally caps concurrent repo imports across all orgs so an "import
// all" of a large org can't exhaust goroutines/FDs or hammer GitHub's rate limit.
var importSem = make(chan struct{}, importConcurrency())

func importConcurrency() int {
	n := 4
	if v := strings.TrimSpace(os.Getenv("GITHUB_IMPORT_CONCURRENCY")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 1 {
			n = p
		}
	}
	return n
}

const (
	importRepoTimeout  = 15 * time.Minute
	importBatchTimeout = 3 * time.Hour
)

// spawnImport runs one org's import batch detached (cancel-immune) so the HTTP
// response returns immediately. Each repo runs under a global slot + its own
// timeout + panic-recover; the token is re-resolved per repo (cached) so a long
// batch never outlives one 1-hour token. Idempotent: a re-run re-fetches.
func spawnImport(org string, items []githubImportItem) {
	log := mounted.Log
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), importBatchTimeout)
		defer cancel()
		for _, it := range items {
			select {
			case importSem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			func(it githubImportItem) {
				defer func() { <-importSem }()
				defer func() {
					if r := recover(); r != nil {
						log.Warn("github import panic", "org", org, "repo", it.Name, "recover", r)
					}
				}()
				// The import runs DETACHED, so there is no request to forward and the
				// plane call would arrive anonymous — the callee reads the tenant
				// from the caller identity and refuses one it cannot see. cloud.For
				// states the org this job acts for; an inbound request always wins
				// over a stated one, so a job can supply an identity where there is
				// none and can never launder one.
				rctx, rcancel := context.WithTimeout(cloud.For(ctx, org), importRepoTimeout)
				defer rcancel()
				tok, err := InstallationToken(rctx, org, it.Owner)
				if err != nil {
					log.Warn("github import: token", "org", org, "repo", it.Name, "err", err)
					return
				}
				// Project is the GitHub ACCOUNT the repo came from, which is what
				// keeps hanzoai/ai, hanzo-apps/ai and hanzo-docs/ai three repos
				// rather than one overwriting the next: native stores a repo at
				// <org>/<project>/<name>.git, so without it they share a path.
				if err := cloud.ImportGitRepo(rctx, cloud.GitImportReq{
					Org: org, Project: it.Owner, Repo: it.Name,
					CloneURL: it.CloneURL, Token: tok, MirrorURL: it.CloneURL,
				}); err != nil {
					log.Warn("github import failed", "org", org, "repo", it.Name, "err", err)
					return
				}
				log.Info("github import ok", "org", org, "repo", it.Name)
			}(it)
		}
	}()
}
