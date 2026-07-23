package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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
// org's connected installation. Fails closed (error, never a value) when GitHub is
// not connected for org or the App creds are absent. Called by the git object plane
// (outbound mirror) and the sync handlers below.
func InstallationToken(ctx context.Context, org string) (string, error) {
	if mounted == nil {
		return "", fmt.Errorf("integrations: not mounted")
	}
	conn, ok := ConnectionFor(org, "github")
	if !ok {
		return "", fmt.Errorf("integrations: github not connected for org")
	}
	// Enablement gate — the ONE github choke point (gateEnabled): githubTokenForOrg
	// (Pages/repo APIs), spawnImport, and the git-object-plane outbound mirror ALL
	// mint through here, so `disable` freezes every installation-token exit at once.
	// Fail-closed (disabled or store error → no token). github declares Secrets:nil
	// so the generic KMS-delete-on-disconnect path never touched this — the gate is
	// the only thing that can stop a live App installation from minting.
	if err := gateEnabled(ctx, mounted, orgScope, org, "", "github", ""); err != nil {
		return "", err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(conn.ExternalID), 10, 64)
	if err != nil {
		return "", fmt.Errorf("integrations: invalid github installation id")
	}
	return ghApp.installationToken(ctx, id)
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
	Name          string `json:"name"`
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"defaultBranch"`
	Imported      bool   `json:"imported"`
	SyncStatus    string `json:"syncStatus"` // "synced" | "conflict" | "" (not imported)
	LastSyncedAt  string `json:"lastSyncedAt,omitempty"`
	HTMLURL       string `json:"htmlUrl,omitempty"`
}

// githubRepos lists the org's granted GitHub repositories, each annotated with its
// native import + sync status. Org-authed: the org comes from the validated
// principal, and the granted set is bounded to THAT org's installation token — an
// org can never enumerate another org's repos.
func githubRepos(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	tok, herr := githubTokenForOrg(c.Context(), org)
	if herr != nil {
		return herr
	}
	repos, err := installationRepos(c.Context(), tok)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "list github repositories: %v", err)
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Name)
	}
	// Best-effort status roll-up from the git object plane (nil if git is unmounted).
	status, _ := cloud.GitRepoStatuses(c.Context(), org, "", names)
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
	return c.JSON(http.StatusOK, map[string]any{"repos": out})
}

type githubImportReqBody struct {
	Repos []string `json:"repos"`
	All   bool     `json:"all"`
}

type githubImportItem struct {
	Name     string
	CloneURL string
}

// githubImport imports the selected (or all) granted repos into git.hanzo.ai. The
// selection is intersected with the installation's GRANTED set, so a client can
// never import a repo the App was not granted (org isolation + a grant check). The
// import runs in a bounded background worker (don't block the request); the console
// polls githubRepos for the per-repo status to flip to imported.
func githubImport(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	var body githubImportReqBody
	if err := c.Bind(&body); err != nil {
		return err
	}
	if !body.All && len(body.Repos) == 0 {
		return zip.ErrBadRequest("provide repos[] or all:true")
	}
	tok, herr := githubTokenForOrg(c.Context(), org)
	if herr != nil {
		return herr
	}
	granted, err := installationRepos(c.Context(), tok)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "list github repositories: %v", err)
	}
	want := map[string]bool{}
	for _, n := range body.Repos {
		want[strings.TrimSuffix(strings.TrimSpace(n), ".git")] = true
	}
	var items []githubImportItem
	for _, r := range granted {
		if r.Archived || r.Disabled { // un-fetchable — skip, never fabricate an import
			continue
		}
		if body.All || want[r.Name] {
			items = append(items, githubImportItem{Name: r.Name, CloneURL: r.CloneURL})
		}
	}
	if len(items) == 0 {
		return zip.ErrBadRequest("no matching repositories are granted to the installation")
	}
	spawnImport(org, items)
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return c.JSON(http.StatusAccepted, map[string]any{"queued": len(items), "repos": names})
}

// githubTokenForOrg resolves the org's installation token with honest HTTP errors:
// 503 when the App is not configured, 409 when this org has not connected GitHub,
// 502 when the mint itself fails.
func githubTokenForOrg(ctx context.Context, org string) (string, error) {
	if !githubConfigured() {
		return "", zip.Errorf(http.StatusServiceUnavailable, "github integration is not configured on this deployment")
	}
	if _, ok := ConnectionFor(org, "github"); !ok {
		return "", zip.Errorf(http.StatusConflict, "github is not connected for this organization")
	}
	tok, err := InstallationToken(ctx, org)
	if err != nil {
		// A disabled connector surfaces as the gate's 403 (InstallationToken →
		// gateEnabled), not a 502 "mint failed" — propagate a ready HTTPError as-is
		// so the Pages/repo callers return the honest "connector is disabled".
		if he, ok := httpErr(err); ok {
			return "", he
		}
		return "", zip.Errorf(http.StatusBadGateway, "mint github installation token: %v", err)
	}
	return tok, nil
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
				rctx, rcancel := context.WithTimeout(ctx, importRepoTimeout)
				defer rcancel()
				tok, err := InstallationToken(rctx, org)
				if err != nil {
					log.Warn("github import: token", "org", org, "repo", it.Name, "err", err)
					return
				}
				if err := cloud.ImportGitRepo(rctx, cloud.GitImportReq{
					Org: org, Repo: it.Name, CloneURL: it.CloneURL, Token: tok, MirrorURL: it.CloneURL,
				}); err != nil {
					log.Warn("github import failed", "org", org, "repo", it.Name, "err", err)
					return
				}
				log.Info("github import ok", "org", org, "repo", it.Name)
			}(it)
		}
	}()
}
