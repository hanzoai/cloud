package provider

// github_index.go is WHICH GitHub repositories a person may see and work on,
// and the paged, searched, most-recently-pushed-first list of them.
//
// Whose view it is decides everything, and it is decided once, here:
//
//	a person who connected their own GitHub   what their token lists — every
//	                                          installation of this App they can
//	                                          reach, and the repositories they
//	                                          reach in each
//	an org admin who has not                  every repository in the
//	                                          installations their org holds
//	anyone else                               nothing, and connected:false,
//	                                          which is the console's cue to ask
//	                                          them to connect
//
// A person's view is GitHub's own intersection of what the App was granted and
// what they may read, so it can never show a member a repository the org holds
// but they cannot open — which the org's own list would.
//
// THE INDEX IS CACHED FOR FIVE MINUTES, per org for the admin view and per
// (org, person) for a person's, and the App's webhook clears it the moment
// GitHub says a repository or an installation's grant changed. Reading it cold
// walks every page of every installation; a board polling it would otherwise
// spend a person's hourly GitHub budget in minutes. One process owns the cache
// because one process owns the credentials it is filled with.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

const (
	// indexTTL is how long a filled index is trusted when no webhook said
	// otherwise.
	indexTTL = 5 * time.Minute
	// indexCap bounds how many views are held at once. Each is at most one
	// org's reach, so the bound is on memory a burst of distinct callers can pin.
	indexCap = 1024
	// defaultPage and maxPage bound one answer.
	defaultPage = 50
	maxPage     = 100
	// pageWorkers bounds the pages of one listing read at once.
	pageWorkers = 8
)

// ghReads bounds the GitHub reads this process has in flight at once, across
// every listing: a person reaching two dozen installations, each paged eight at
// a time, would otherwise open a couple of hundred requests together — past what
// GitHub tolerates from one client before it answers with a secondary limit.
var ghReads = make(chan struct{}, 32)

// read is one bounded GitHub page read.
func read[T any](ctx context.Context, get func(ctx context.Context, page int) ([]T, int, error), page int) ([]T, int, error) {
	select {
	case ghReads <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	defer func() { <-ghReads }()
	return get(ctx, page)
}

// indexed is one view of the repositories, sorted most-recently-pushed first.
type indexed struct {
	repos []githubRepo
	// unread names the installations this view could not read, so a short list
	// says it is short.
	unread []string
	// installs is every installation the view was filled from, read or not — what
	// a webhook naming one of them has to clear.
	installs map[int64]bool
	at       time.Time
}

// indexCache holds the filled views. Keys are opaque: "o\x00"+org for an org
// admin's view, "u\x00"+org+"\x00"+user for a person's.
type indexCache struct {
	mu sync.Mutex
	m  map[string]*indexed
	// forgotKey, forgotInst and forgotOrg say when a view, an installation, or
	// every view of an org was last forgotten. A fill that STARTED before then
	// read what the forget was about and is answered but not kept — otherwise a
	// webhook landing mid-fill would be undone by the fill it raced.
	forgotKey  map[string]time.Time
	forgotInst map[int64]time.Time
	forgotOrg  map[string]time.Time
}

var repoIndex = newIndexCache()

func newIndexCache() *indexCache {
	return &indexCache{m: map[string]*indexed{}, forgotKey: map[string]time.Time{}, forgotInst: map[int64]time.Time{}, forgotOrg: map[string]time.Time{}}
}

func orgKey(org string) string        { return "o\x00" + org }
func userKey(org, user string) string { return "u\x00" + org + "\x00" + user }

// keyOrg is the org a view's key is for.
func keyOrg(key string) string {
	_, rest, _ := strings.Cut(key, "\x00")
	org, _, _ := strings.Cut(rest, "\x00")
	return org
}

func (c *indexCache) get(key string) (*indexed, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	if !ok || time.Since(v.at) > indexTTL {
		return nil, false
	}
	return v, true
}

// put keeps v under key unless the view or one of its installations was
// forgotten after the fill began.
func (c *indexCache) put(key string, v *indexed, began time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.forgotKey[key].After(began) || c.forgotOrg[keyOrg(key)].After(began) {
		return
	}
	for id := range v.installs {
		if c.forgotInst[id].After(began) {
			return
		}
	}
	if len(c.m) >= indexCap {
		// Expired first, and then the oldest: the bound holds either way.
		oldest, at := "", time.Now()
		for k, e := range c.m {
			if time.Since(e.at) > indexTTL {
				delete(c.m, k)
				continue
			}
			if e.at.Before(at) {
				oldest, at = k, e.at
			}
		}
		if len(c.m) >= indexCap {
			delete(c.m, oldest)
		}
	}
	c.m[key] = v
	c.prune()
}

// prune drops forget marks no fill can still be racing: a fill runs at most
// fillBudget, so a mark older than that has nothing left to stop.
func (c *indexCache) prune() {
	for k, at := range c.forgotKey {
		if time.Since(at) > fillBudget {
			delete(c.forgotKey, k)
		}
	}
	for id, at := range c.forgotInst {
		if time.Since(at) > fillBudget {
			delete(c.forgotInst, id)
		}
	}
	for org, at := range c.forgotOrg {
		if time.Since(at) > fillBudget {
			delete(c.forgotOrg, org)
		}
	}
}

// forgetUser drops one person's view — they connected, reconnected or left.
func (c *indexCache) forgetUser(org, user string) {
	c.mu.Lock()
	k := userKey(org, user)
	delete(c.m, k)
	c.forgotKey[k] = time.Now()
	c.mu.Unlock()
}

// forgetOrg drops every view of org, its admin's and each person's — a GitHub
// account was just bound to it. That account's installation is new to every
// view filled before it, so forget(id) finds no view naming it: the org's people
// would otherwise list without it until their views expired.
func (c *indexCache) forgetOrg(org string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if keyOrg(k) == org {
			delete(c.m, k)
		}
	}
	c.forgotOrg[org] = time.Now()
}

// forget drops every view filled from installation id, and nothing else. A
// webhook for one installation therefore costs the people who reach it one
// refill each, and costs everyone else nothing: installing the App on an
// account of one's own and churning its repositories reaches no other view.
func (c *indexCache) forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if v.installs[id] {
			delete(c.m, k)
		}
	}
	c.forgotInst[id] = time.Now()
}

// forgetInstallation is what the App's webhook calls when GitHub reports that a
// repository or an installation's grant changed: the views filled from that
// installation, and the grant set the org view is filled from.
func forgetInstallation(id int64) {
	if id <= 0 {
		return
	}
	repoIndex.forget(id)
	grantMu.Lock()
	delete(grantCache, id)
	grantMu.Unlock()
}

// view is the caller's index and whether it is their own GitHub.
func view(ctx context.Context, s *cloud.Service[state], org, user string, admin bool) (*indexed, bool, error) {
	if !githubConfigured() {
		return nil, false, zip.Errorf(http.StatusServiceUnavailable, "github integration is not configured on this deployment")
	}
	tok, _, ok, err := personToken(ctx, s, org, user)
	if err != nil {
		return nil, false, err
	}
	if ok {
		v, err := fill(ctx, s, userKey(org, user), func(ctx context.Context) (*indexed, error) {
			return personRepos(ctx, tok)
		})
		if err == nil {
			return v, true, nil
		}
		if !errors.Is(err, errGitHubRevoked) {
			return nil, false, err
		}
		// Revoked at GitHub: the connection is dead, so it goes, the person is
		// asked to connect again, and an admin still has the org's view.
		forgetPerson(ctx, s, org, user)
	}
	if !admin {
		return &indexed{}, false, nil
	}
	v, err := fill(ctx, s, orgKey(org), func(ctx context.Context) (*indexed, error) {
		return orgRepos(ctx, org)
	})
	return v, false, err
}

// fillBudget bounds one fill, whoever asked for it.
const fillBudget = 2 * time.Minute

// fill is the cached view under key, read once by one caller while the rest
// wait for it. The read runs detached from the caller that happened to ask
// first, under its own budget, so a client that hangs up cannot leave a partial
// view in the cache for everyone behind it.
func fill(ctx context.Context, s *cloud.Service[state], key string, readView func(context.Context) (*indexed, error)) (*indexed, error) {
	if v, ok := repoIndex.get(key); ok {
		return v, nil
	}
	unlock := s.State.flight.lock("index\x00" + key)
	defer unlock()
	if v, ok := repoIndex.get(key); ok {
		return v, nil
	}
	began := time.Now()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fillBudget)
	defer cancel()
	v, err := readView(rctx)
	if err != nil {
		return nil, err
	}
	sortRepos(v.repos)
	v.at = time.Now()
	repoIndex.put(key, v, began)
	return v, nil
}

// fillShare is how many installations one fill reads at once. With each read
// paged pageWorkers at a time, one fill holds at most half of ghReads, so no
// one person's reach can starve everyone else's listing.
const fillShare = 2

// maxViewRepos and maxViewInstalls bound one view: past them the rest is named
// in unread rather than held.
const (
	maxViewRepos    = 20000
	maxViewInstalls = 200
)

// personRepos is every repository a person's token lists, across every
// installation of this App they can reach.
func personRepos(ctx context.Context, tok string) (*indexed, error) {
	installs, err := personInstallations(ctx, tok)
	if err != nil {
		return nil, err
	}
	v := &indexed{installs: map[int64]bool{}}
	if len(installs) > maxViewInstalls {
		for _, in := range installs[maxViewInstalls:] {
			v.unread = append(v.unread, in.Login)
		}
		installs = installs[:maxViewInstalls]
	}
	for _, in := range installs {
		v.installs[in.ID] = true
	}
	out := make([][]githubRepo, len(installs))
	errs := make([]error, len(installs))
	share := make(chan struct{}, fillShare)
	var wg sync.WaitGroup
	for i, in := range installs {
		wg.Add(1)
		share <- struct{}{}
		go func(i int, id int64) {
			defer wg.Done()
			defer func() { <-share }()
			out[i], errs[i] = walk(ctx, func(ctx context.Context, page int) ([]githubRepo, int, error) {
				var p struct {
					Total int          `json:"total_count"`
					Repos []githubRepo `json:"repositories"`
				}
				if err := githubGet(ctx, tok, fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", id, page), &p); err != nil {
					return nil, 0, err
				}
				return p.Repos, p.Total, nil
			})
			for j := range out[i] {
				out[i][j].Installation = id
			}
		}(i, in.ID)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, repos := range out {
		if errors.Is(errs[i], errGitHubRevoked) {
			return nil, errGitHubRevoked
		}
		if errs[i] != nil || len(v.repos)+len(repos) > maxViewRepos {
			v.unread = append(v.unread, installs[i].Login)
			continue
		}
		for _, r := range repos {
			if k := strings.ToLower(r.FullName); !seen[k] {
				seen[k] = true
				v.repos = append(v.repos, r)
			}
		}
	}
	if len(v.repos) == 0 && len(v.unread) > 0 {
		return nil, zip.Errorf(http.StatusBadGateway, "list github repositories: every installation failed (%s)", strings.Join(v.unread, ", "))
	}
	return v, nil
}

// userInstallation is one row of GET /user/installations.
type userInstallation struct {
	ID      int64 `json:"id"`
	AppID   int64 `json:"app_id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

// personInstallations is the installations of THIS App the person reaches. A
// user token is minted for one App and lists only its installations; the id
// check is the second lock, so a token of another App could not widen the view.
func personInstallations(ctx context.Context, tok string) ([]githubInstallation, error) {
	app, _ := strconv.ParseInt(strings.TrimSpace(env.Or(githubAppIDEnv, "")), 10, 64)
	all, err := walk(ctx, func(ctx context.Context, page int) ([]userInstallation, int, error) {
		var p struct {
			Total int                `json:"total_count"`
			Items []userInstallation `json:"installations"`
		}
		if err := githubGet(ctx, tok, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), &p); err != nil {
			return nil, 0, err
		}
		return p.Items, p.Total, nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]githubInstallation, 0, len(all))
	for _, in := range all {
		if app != 0 && in.AppID != 0 && in.AppID != app {
			continue
		}
		out = append(out, githubInstallation{ID: in.ID, Login: in.Account.Login, Type: in.Account.Type})
	}
	return out, nil
}

// orgRepos is every repository in the installations the org holds, read with
// each installation's own token.
func orgRepos(ctx context.Context, org string) (*indexed, error) {
	if len(Connections(org, "github")) == 0 {
		return &indexed{}, nil
	}
	v := &indexed{installs: map[int64]bool{}}
	for _, c := range Connections(org, "github") {
		if id, err := parseInstallation(c.ExternalID); err == nil {
			v.installs[id] = true
		}
	}
	repos, unread, err := reachableRepos(ctx, org)
	if err != nil {
		return nil, err
	}
	if len(repos) > maxViewRepos {
		repos, unread = repos[:maxViewRepos], append(unread, "(truncated)")
	}
	v.repos, v.unread = repos, unread
	return v, nil
}

// walk reads every page of a GitHub listing that reports its total: the first
// page says how many there are, and the rest are read at once, bounded. A
// listing with no total is read page by page until one comes back short.
func walk[T any](ctx context.Context, get func(ctx context.Context, page int) ([]T, int, error)) ([]T, error) {
	const perPage, maxPages = 100, 100 // 10,000 items
	first, total, err := read(ctx, get, 1)
	if err != nil {
		return nil, err
	}
	if len(first) < perPage {
		return first, nil
	}
	if total <= 0 {
		all := first
		for page := 2; page <= maxPages; page++ {
			items, _, err := read(ctx, get, page)
			if err != nil {
				return nil, err
			}
			all = append(all, items...)
			if len(items) < perPage {
				break
			}
		}
		return all, nil
	}
	pages := min((total+perPage-1)/perPage, maxPages)
	rest := make([][]T, pages+1)
	errs := make([]error, pages+1)
	sem := make(chan struct{}, pageWorkers)
	var wg sync.WaitGroup
	for page := 2; page <= pages; page++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(page int) {
			defer wg.Done()
			defer func() { <-sem }()
			rest[page], _, errs[page] = read(ctx, get, page)
		}(page)
	}
	wg.Wait()
	all := first
	for page := 2; page <= pages; page++ {
		if errs[page] != nil {
			return nil, errs[page]
		}
		all = append(all, rest[page]...)
	}
	return all, nil
}

// sortRepos puts the most recently pushed first, a repository never pushed
// last, and breaks ties by name so the same set always pages the same way.
func sortRepos(repos []githubRepo) {
	slices.SortStableFunc(repos, func(a, b githubRepo) int { return compareRepo(a, b) })
}

func compareRepo(a, b githubRepo) int {
	switch {
	case a.PushedAt == b.PushedAt:
	case a.PushedAt == "":
		return 1
	case b.PushedAt == "":
		return -1
	case a.PushedAt > b.PushedAt: // RFC 3339 in UTC, as GitHub writes it, orders as text
		return -1
	default:
		return 1
	}
	return strings.Compare(strings.ToLower(a.FullName), strings.ToLower(b.FullName))
}

// cursor is the position after one repository, in the index's own order. It
// is the repository's sort key rather than an offset, so a page boundary holds
// when a refill between two reads moves the rest of the list.
func cursor(r githubRepo) string {
	return base64.RawURLEncoding.EncodeToString([]byte(r.PushedAt + "\n" + strings.ToLower(r.FullName)))
}

func uncursor(s string) (githubRepo, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return githubRepo{}, err
	}
	pushed, name, ok := strings.Cut(string(raw), "\n")
	if !ok || name == "" {
		return githubRepo{}, fmt.Errorf("malformed")
	}
	return githubRepo{PushedAt: pushed, FullName: name}, nil
}

// ── the op ───────────────────────────────────────────────────────────────────

// githubReposIn narrows and pages the caller's repositories.
type githubReposIn struct {
	// Q keeps repositories whose owner/name contains it, case-insensitively.
	Q string `json:"q"`
	// Owner keeps one GitHub account's repositories — an org or a user login.
	Owner string `json:"owner"`
	// Limit is the page size: 1 to 100, and 50 when absent or unreadable.
	Limit int `json:"limit"`
	// After is the `next` of the previous page. Absent starts at the most
	// recently pushed.
	After string `json:"after"`
}

// githubRepoItem is one repository the caller may see.
type githubRepoItem struct {
	// Owner is the GitHub account that holds it.
	Owner string `json:"owner"`
	// Name is its name within that account.
	Name string `json:"name"`
	// FullName is owner/name, the address a coding run, a branch listing and a
	// deploy take.
	FullName string `json:"full_name"`
	// Private is GitHub's visibility bit.
	Private bool `json:"private"`
	// DefaultBranch is the branch GitHub opens it on.
	DefaultBranch string `json:"default_branch"`
	// PushedAt is when anything was last pushed to it, RFC 3339 UTC. Absent for a
	// repository nothing was ever pushed to.
	PushedAt string `json:"pushed_at,omitempty"`
	// InstallationID is the installation of the Hanzo Platform App that reaches
	// it — the grant a run on it is minted from.
	InstallationID int64 `json:"installation_id"`
	// Codebase is the name it has in the org's code workspace once brought in
	// (POST /v1/provider/github/repos/import): listed by GET /v1/git/repos when
	// it is there.
	Codebase string `json:"codebase"`
}

// githubReposOut is one page of the caller's repositories.
type githubReposOut struct {
	// Repos is this page, most recently pushed first. Never null.
	Repos []githubRepoItem `json:"repos"`
	// Next is the cursor for the page after this one; empty on the last page.
	Next string `json:"next"`
	// Total is how many repositories match q and owner, across every page.
	Total int `json:"total"`
	// Unread names the installations that could not be read, so a short list is
	// distinguishable from a complete one. Absent when the answer is whole.
	Unread []string `json:"unread,omitempty"`
	// Connected is true when the caller has connected their own GitHub account
	// OR when an org admin's organization holds at least one active connected
	// GitHub App installation (even if that installation currently has zero
	// repositories). False is the console's cue to ask the caller to connect.
	Connected bool `json:"connected"`
}

// githubRepos lists the GitHub repositories the caller may work on, most
// recently pushed first, searched and paged on the server.
//
// A person who connected their own GitHub sees what their token reaches in this
// App's installations. An org admin who has not sees every repository in the
// installations their org holds (with connected:true whenever the org holds an
// active installation, even with zero repositories). Anyone else gets an empty
// page with connected:false, which is the console's cue to ask them to connect.
//
// Example: {"q": "cloud", "limit": 50}
// Response: {"repos":[{"owner":"hanzoai","name":"cloud","full_name":"hanzoai/cloud","private":false,"default_branch":"main","pushed_at":"2026-09-24T18:00:00Z","installation_id":62000701}],"next":"","total":1,"connected":true}
func (o ops) githubRepos(ctx context.Context, in *githubReposIn) (*githubReposOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	v, personal, err := view(ctx, o.s, org, factsOf(ctx).user, orgAdmin(ctx))
	if err != nil {
		return nil, err
	}
	connected := personal || len(v.installs) > 0
	limit := in.Limit
	if limit <= 0 {
		limit = defaultPage
	}
	limit = min(limit, maxPage)
	var after *githubRepo
	if a := strings.TrimSpace(in.After); a != "" {
		c, err := uncursor(a)
		if err != nil {
			return nil, zip.ErrBadRequest("after is not a cursor this listing issued")
		}
		after = &c
	}
	q := strings.ToLower(strings.TrimSpace(in.Q))
	owner := strings.TrimSpace(in.Owner)
	out := &githubReposOut{Repos: []githubRepoItem{}, Unread: v.unread, Connected: connected}
	for _, r := range v.repos {
		if owner != "" && !strings.EqualFold(r.Owner.Login, owner) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.FullName), q) {
			continue
		}
		out.Total++
		if after != nil && compareRepo(r, *after) <= 0 {
			continue
		}
		if len(out.Repos) == limit {
			if out.Next == "" {
				last := out.Repos[limit-1]
				out.Next = cursor(githubRepo{PushedAt: last.PushedAt, FullName: last.FullName})
			}
			continue
		}
		out.Repos = append(out.Repos, item(r))
	}
	return out, nil
}

func item(r githubRepo) githubRepoItem {
	owner, name, _ := splitFullName(r.FullName)
	if r.Owner.Login != "" {
		owner = r.Owner.Login
	}
	if r.Name != "" {
		name = r.Name
	}
	return githubRepoItem{
		Owner: owner, Name: name, FullName: r.FullName, Private: r.Private,
		DefaultBranch: r.DefaultBranch, PushedAt: r.PushedAt, InstallationID: r.Installation,
		Codebase: forge.Imported(owner, name),
	}
}

// lookup is the caller's repository at owner/name, from the same view the
// listing answers from — so a repository the caller cannot list is one they
// cannot address either, and both are the same 404. personal says the view is
// the caller's own GitHub, so a read of it goes out with their token.
func lookup(ctx context.Context, s *cloud.Service[state], org, owner, name string) (r githubRepo, found, personal bool, err error) {
	v, personal, err := view(ctx, s, org, factsOf(ctx).user, orgAdmin(ctx))
	if err != nil {
		return githubRepo{}, false, false, err
	}
	want := strings.ToLower(owner + "/" + name)
	for _, r := range v.repos {
		if strings.ToLower(r.FullName) == want {
			return r, true, personal, nil
		}
	}
	return githubRepo{}, false, personal, nil
}
