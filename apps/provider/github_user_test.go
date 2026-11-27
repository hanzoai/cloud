package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

// github_user_test.go drives a person's own GitHub end to end against a GitHub
// that holds real state: OAuth codes and rotating refresh tokens, the
// installations each person reaches, the repositories each installation holds,
// each person's permission on each, and branches. Nothing under test is faked;
// only GitHub is.

// hubRepo is one repository in the fake GitHub.
type hubRepo struct {
	full     string
	private  bool
	branch   string
	pushed   string
	install  int64
	branches []string
	// push is who may push to it, by login; everyone the installation reaches may
	// read it.
	push map[string]bool
	// admin is who administers it, by login.
	admin map[string]bool
}

// fakeHub is GitHub, as much of it as this surface reads.
type fakeHub struct {
	mu sync.Mutex
	// reach is which installations each login's token reaches.
	reach map[string][]int64
	// accounts names each installation's account, and types says what kind of
	// account it is ("Organization" when unnamed).
	accounts map[int64]string
	types    map[int64]string
	repos    []*hubRepo
	// issued is the live access token per login, and refresh its refresh token.
	gen        map[string]int
	access     map[string]string // token -> login
	refresh    map[string]string // refresh token -> login
	revoked    []string
	expires    int64 // expires_in for new tokens; 0 for a token that does not expire
	minted     []string
	perms      []map[string]string // the permissions each scoped mint asked for
	userHits   int
	tokenCalls int
	failRevoke bool
	tokenDown  bool
	userDown   bool
}

func newFakeHub(t *testing.T) (*fakeHub, *httptest.Server) {
	t.Helper()
	h := &fakeHub{
		reach: map[string][]int64{}, accounts: map[int64]string{}, types: map[int64]string{},
		gen: map[string]int{}, access: map[string]string{}, refresh: map[string]string{},
		expires: 28800,
	}
	srv := httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(srv.Close)
	return h, srv
}

// issue mints a login's next token pair.
func (h *fakeHub) issue(login string) (string, string) {
	h.gen[login]++
	n := h.gen[login]
	tok, ref := fmt.Sprintf("tok-%s-%d", login, n), fmt.Sprintf("ref-%s-%d", login, n)
	h.access[tok] = login
	h.refresh[ref] = login
	return tok, ref
}

func (h *fakeHub) repo(full string) *hubRepo {
	for _, r := range h.repos {
		if strings.EqualFold(r.full, full) {
			return r
		}
	}
	return nil
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p := r.URL.Path
	switch {
	case p == "/login/oauth/access_token" && h.tokenDown:
		h.tokenCalls++
		w.WriteHeader(http.StatusBadGateway)
	case p == "/login/oauth/access_token":
		h.tokenCalls++
		_ = r.ParseForm()
		var login string
		if r.Form.Get("grant_type") == "refresh_token" {
			login = h.refresh[r.Form.Get("refresh_token")]
			delete(h.refresh, r.Form.Get("refresh_token")) // GitHub retires it
		} else if c := r.Form.Get("code"); strings.HasPrefix(c, "code-") {
			login = strings.TrimPrefix(c, "code-")
		}
		if r.Form.Get("client_secret") != "shh" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "incorrect_client_credentials"})
			return
		}
		if login == "" {
			code := "bad_verification_code"
			if r.Form.Get("grant_type") == "refresh_token" {
				code = "bad_refresh_token"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"error": code})
			return
		}
		tok, ref := h.issue(login)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "refresh_token": ref, "expires_in": h.expires})
	case strings.HasPrefix(p, "/applications/") && r.Method == http.MethodDelete && h.failRevoke:
		w.WriteHeader(http.StatusInternalServerError)
	case strings.HasPrefix(p, "/applications/") && r.Method == http.MethodDelete:
		var body struct {
			Token string `json:"access_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.revoked = append(h.revoked, body.Token)
		delete(h.access, body.Token)
		w.WriteHeader(http.StatusNoContent)
	case p == "/user" && h.userDown:
		w.WriteHeader(http.StatusBadGateway)
	case p == "/user":
		login, ok := h.access[auth]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": login})
	case p == "/user/installations":
		login, ok := h.access[auth]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.userHits++
		var items []map[string]any
		for _, id := range h.reach[login] {
			kind := h.types[id]
			if kind == "" {
				kind = "Organization"
			}
			items = append(items, map[string]any{"id": id, "app_id": 424242, "account": map[string]any{"login": h.accounts[id], "type": kind}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(items), "installations": items})
	case strings.HasPrefix(p, "/user/installations/") && strings.HasSuffix(p, "/repositories"):
		login, ok := h.access[auth]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(p, "/user/installations/"), "/repositories"), 10, 64)
		var in []*hubRepo
		for _, x := range h.reach[login] {
			if x == id {
				for _, rp := range h.repos {
					if rp.install == id {
						in = append(in, rp)
					}
				}
			}
		}
		h.pageAs(w, r, in, login)
	case p == "/installation/repositories":
		id, _ := strconv.ParseInt(strings.TrimPrefix(auth, "ghs-inst-"), 10, 64)
		var in []*hubRepo
		for _, rp := range h.repos {
			if rp.install == id {
				in = append(in, rp)
			}
		}
		h.page(w, r, in)
	case strings.HasPrefix(p, "/app/installations/") && strings.HasSuffix(p, "/access_tokens"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(p, "/app/installations/"), "/access_tokens"), 10, 64)
		var body struct {
			Repos []string          `json:"repositories"`
			Perms map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		tok := "ghs-inst-" + strconv.FormatInt(id, 10)
		out := map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		if len(body.Repos) > 0 {
			var named []map[string]any
			for _, name := range body.Repos {
				for _, rp := range h.repos {
					if rp.install == id && strings.HasSuffix(strings.ToLower(rp.full), "/"+strings.ToLower(name)) {
						named = append(named, map[string]any{"full_name": rp.full})
					}
				}
			}
			tok = fmt.Sprintf("ghs-run-%d-%s", id, strings.Join(body.Repos, ","))
			h.minted = append(h.minted, tok)
			h.perms = append(h.perms, body.Perms)
			out = map[string]any{"token": tok, "permissions": body.Perms, "repositories": named}
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/repos/"):
		parts := strings.Split(strings.TrimPrefix(p, "/repos/"), "/")
		if len(parts) < 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rp := h.repo(parts[0] + "/" + parts[1])
		if rp == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		login, person := h.access[auth]
		inst := auth == "ghs-inst-"+strconv.FormatInt(rp.install, 10)
		switch rest := strings.Join(parts[2:], "/"); {
		case rest == "installation":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": rp.install})
		case rest == "" && (person || inst):
			if person && !h.reaches(login, rp.install) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name": rp.full, "default_branch": rp.branch,
				"permissions": map[string]bool{"pull": true, "push": rp.push[login]},
			})
		case strings.HasPrefix(rest, "git/matching-refs/heads") && (person && h.reaches(login, rp.install) || inst):
			prefix := strings.TrimPrefix(strings.TrimPrefix(rest, "git/matching-refs/heads"), "/")
			var refs []map[string]any
			for _, b := range rp.branches {
				if strings.HasPrefix(b, prefix) {
					refs = append(refs, map[string]any{"ref": "refs/heads/" + b, "object": map[string]any{"sha": "sha-" + b}})
				}
			}
			_ = json.NewEncoder(w).Encode(refs)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// hits is how many times GitHub was asked for a person's installations. The
// caller holds h.mu.
func (h *fakeHub) hits() int { return h.userHits }

func (h *fakeHub) reaches(login string, id int64) bool {
	for _, x := range h.reach[login] {
		if x == id {
			return true
		}
	}
	return false
}

// page answers one GitHub listing page of repositories, 100 to a page.
func (h *fakeHub) page(w http.ResponseWriter, r *http.Request, in []*hubRepo) {
	h.pageAs(w, r, in, "")
}

// pageAs is page as a person reads it: each repository carries that person's
// permissions on it, as GitHub's listings for a user token do.
func (h *fakeHub) pageAs(w http.ResponseWriter, r *http.Request, in []*hubRepo, login string) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	lo, hi := (page-1)*100, page*100
	var out []map[string]any
	for i, rp := range in {
		if i < lo || i >= hi {
			continue
		}
		owner, name, _ := strings.Cut(rp.full, "/")
		item := map[string]any{
			"name": name, "full_name": rp.full, "owner": map[string]any{"login": owner},
			"private": rp.private, "default_branch": rp.branch, "pushed_at": rp.pushed,
		}
		if login != "" {
			item["permissions"] = map[string]bool{"admin": rp.admin[login], "push": rp.push[login] || rp.admin[login], "pull": true}
		}
		out = append(out, item)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(in), "repositories": out})
}

// withHub points every GitHub base at the fake and configures the App and its
// OAuth client.
func withHub(t *testing.T) (*fakeHub, *zip.App) {
	t.Helper()
	h, srv := newFakeHub(t)
	withGithubApp(t, srv)
	oldWeb := githubWebBase
	githubWebBase = srv.URL
	t.Cleanup(func() { githubWebBase = oldWeb })
	t.Setenv(githubClientIDEnv, "Iv23test")
	t.Setenv(githubClientSecretEnv, "shh")
	repoIndex = newIndexCache()
	t.Cleanup(func() { repoIndex = newIndexCache() })
	return h, newApp(t, newKMS(t))
}

// asPerson issues one request as a person in an org, optionally an org admin.
func asPerson(t *testing.T, app *zip.App, method, path, org, user string, admin bool, body any) httpResult {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, rd)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", user)
		if admin {
			rq.Header.Set("X-User-IsOrgAdmin", "true")
		}
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: b}
}

// connectAs walks the whole flow — connect, GitHub's return, the console's
// completion — as user in org, authorizing on GitHub as login.
func connectAs(t *testing.T, app *zip.App, org, user, login string) httpResult {
	t.Helper()
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", org, user, false, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", r.Code, r.Body)
	}
	grant := callbackGrant(t, app, stateOf(t, r.Body), "code-"+login)
	return asPerson(t, app, http.MethodPost, "/v1/provider/github/user/complete", org, user, false, map[string]string{"grant": grant})
}

func stateOf(t *testing.T, body []byte) string {
	t.Helper()
	var out githubUserConnectOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("connect body: %v (%s)", err, body)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatalf("authorizeUrl: %v", err)
	}
	if u.Query().Get("client_id") != "Iv23test" || u.Query().Get("redirect_uri") != "https://api.hanzo.ai/v1/provider/github/user/callback" {
		t.Fatalf("authorize URL names the wrong client or return: %s", out.AuthorizeURL)
	}
	// GitHub skips its page for anyone who authorized the App before, so without
	// the picker a connect after a Disconnect could only return the same account.
	if u.Query().Get("prompt") != "select_account" {
		t.Fatalf("authorize URL must ask GitHub for its account picker: %s", out.AuthorizeURL)
	}
	return u.Query().Get("state")
}

// callbackGrant runs GitHub's return and reads the grant the console is handed.
func callbackGrant(t *testing.T, app *zip.App, state, code string) string {
	t.Helper()
	r := asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), "", "", false, nil)
	if r.Code != http.StatusFound {
		t.Fatalf("callback want 302, got %d (%s)", r.Code, r.Body)
	}
	u, err := url.Parse(r.Location)
	if err != nil || u.Query().Get("complete") != "github" || u.Query().Get("grant") == "" {
		t.Fatalf("callback should send the console a grant, got %q", r.Location)
	}
	return u.Query().Get("grant")
}

func userOut(t *testing.T, r httpResult) githubUserOut {
	t.Helper()
	var out githubUserOut
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("status body: %v (%s)", err, r.Body)
	}
	return out
}

// TestPersonConnectsTheirOwnGitHub is the whole flow: the callback completes
// nothing, the console completes it as the person who started it, the token is
// sealed in their own custody, and status reports it.
func TestPersonConnectsTheirOwnGitHub(t *testing.T) {
	_, app := withHub(t)
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); !got.Configured || got.Connected {
		t.Fatalf("before connecting: want configured and not connected, got %+v", got)
	}
	r := connectAs(t, app, "acme", "alice", "octo-alice")
	if r.Code != http.StatusOK {
		t.Fatalf("complete want 200, got %d (%s)", r.Code, r.Body)
	}
	got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil))
	if !got.Connected || got.Login != "octo-alice" || got.ExpiresAt == "" {
		t.Fatalf("after connecting: %+v", got)
	}
	// Sealed in the person's own custody, and nowhere in the row.
	path, _ := userPath("acme", "alice", githubUserID, defaultLabel)
	if tok, err := kmsGet(mounted, path, accessSecret); err != nil || !strings.HasPrefix(string(tok), "tok-octo-alice-") {
		t.Fatalf("token not in custody: %q %v", tok, err)
	}
	// Another person in the same org is not connected by it.
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "bob", false, nil)); got.Connected {
		t.Fatalf("bob must not inherit alice's connection: %+v", got)
	}
	// The org plane is untouched: a person's token is not an installation.
	if conns := Connections("acme", "github"); len(conns) != 0 {
		t.Fatalf("a personal connection must not appear as an org installation: %+v", conns)
	}
}

// TestAnotherPersonCannotCompleteAFlow is the transplant: a link carrying
// alice's state, opened by bob, parks BOB's code for alice's flow. Bob's own
// signed-in console cannot complete it, and alice never learns the grant id.
func TestAnotherPersonCannotCompleteAFlow(t *testing.T) {
	_, app := withHub(t)
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false, nil)
	grant := callbackGrant(t, app, stateOf(t, r.Body), "code-octo-bob")

	for _, who := range []struct{ org, user string }{{"acme", "bob"}, {"other", "alice"}} {
		if rr := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/complete", who.org, who.user, false,
			map[string]string{"grant": grant}); rr.Code != http.StatusNotFound {
			t.Fatalf("%s/%s completing alice's flow want 404, got %d (%s)", who.org, who.user, rr.Code, rr.Body)
		}
	}
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "bob", false, nil)); got.Connected {
		t.Fatalf("bob must not be connected by alice's flow: %+v", got)
	}
	// A grant is single-use: once completed it is gone.
	if rr := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/complete", "acme", "alice", false,
		map[string]string{"grant": grant}); rr.Code != http.StatusOK {
		t.Fatalf("alice completing her own flow want 200, got %d (%s)", rr.Code, rr.Body)
	}
	if rr := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/complete", "acme", "alice", false,
		map[string]string{"grant": grant}); rr.Code != http.StatusNotFound {
		t.Fatalf("a completed grant must not complete twice, got %d", rr.Code)
	}
	// A state is single-use too.
	st := stateOf(t, asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false, nil).Body)
	callbackGrant(t, app, st, "code-octo-alice")
	rr := asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(st)+"&code=code-x", "", "", false, nil)
	if !strings.Contains(rr.Location, "error=github") {
		t.Fatalf("a replayed state must fail, got %q", rr.Location)
	}
	// An org's install state is not a person's, and the reverse.
	orgState, _ := sign(mounted, "acme", "", "github", "n1")
	rr = asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(orgState)+"&code=code-x", "", "", false, nil)
	if !strings.Contains(rr.Location, "error=github") {
		t.Fatalf("an org state must not complete a personal flow, got %q", rr.Location)
	}
}

// TestUnconfiguredConnectIs503 is the deployment without the App's client
// secret: status says so, and connect refuses honestly.
func TestUnconfiguredConnectIs503(t *testing.T) {
	_, app := withHub(t)
	t.Setenv(githubClientSecretEnv, "")
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); got.Configured {
		t.Fatalf("no client secret must read configured=false: %+v", got)
	}
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false, nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("connect without a client secret want 503, got %d (%s)", r.Code, r.Body)
	}
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "", "", false, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous connect want 401, got %d", r.Code)
	}
}

// TestPersonTokenRotates proves an access token near its end is refreshed with
// the refresh token GitHub handed beside it, and the new pair is sealed.
func TestPersonTokenRotates(t *testing.T) {
	h, app := withHub(t)
	h.expires = 30 // inside the one-minute window, so the next read rotates
	connectAs(t, app, "acme", "alice", "octo-alice")
	tok, login, ok, err := personToken(context.Background(), mounted, "acme", "alice")
	if err != nil || !ok || login != "octo-alice" {
		t.Fatalf("token: ok=%v login=%q err=%v", ok, login, err)
	}
	if tok != "tok-octo-alice-2" {
		t.Fatalf("a token inside the refresh window must rotate, got %q", tok)
	}
	// A rotation that GitHub answered keeps its tokens even when /user would
	// not: the old refresh token is already gone.
	h.mu.Lock()
	h.userDown = true
	h.mu.Unlock()
	if tok, _, ok, err := personToken(context.Background(), mounted, "acme", "alice"); !ok || err != nil || tok != "tok-octo-alice-3" {
		t.Fatalf("a rotation must stand without /user, got %q ok=%v err=%v", tok, ok, err)
	}
	h.mu.Lock()
	h.userDown = false
	h.mu.Unlock()
	// GitHub not answering is not GitHub refusing: the read fails and the
	// connection stands.
	h.mu.Lock()
	h.tokenDown = true
	h.mu.Unlock()
	if _, _, ok, err := personToken(context.Background(), mounted, "acme", "alice"); ok || err == nil {
		t.Fatalf("a token endpoint that is down must fail the read, got ok=%v err=%v", ok, err)
	}
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); !got.Connected {
		t.Fatalf("a transient GitHub failure must not drop the connection: %+v", got)
	}
	// Our own client secret going wrong is ours to fix, and costs no person
	// their connection.
	h.mu.Lock()
	h.tokenDown = false
	h.mu.Unlock()
	t.Setenv(githubClientSecretEnv, "rotated")
	if _, _, ok, err := personToken(context.Background(), mounted, "acme", "alice"); ok || err == nil {
		t.Fatalf("a refused client secret must fail the read, got ok=%v err=%v", ok, err)
	}
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); !got.Connected {
		t.Fatalf("a wrong client secret must not drop the connection: %+v", got)
	}
	t.Setenv(githubClientSecretEnv, "shh")
	// A refresh token GitHub retired leaves the person asked to connect again.
	h.mu.Lock()
	h.refresh = map[string]string{}
	h.mu.Unlock()
	if _, _, ok, err := personToken(context.Background(), mounted, "acme", "alice"); ok || err != nil {
		t.Fatalf("a dead refresh token must read as not connected, got ok=%v err=%v", ok, err)
	}
	// And the dead connection is dropped, so status says so and nothing retries
	// the refresh with the App's client secret on every read.
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); got.Connected {
		t.Fatalf("a dead connection must read not connected: %+v", got)
	}
	h.mu.Lock()
	calls := h.tokenCalls
	h.mu.Unlock()
	for range 5 {
		_, _, _, _ = personToken(context.Background(), mounted, "acme", "alice")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tokenCalls != calls {
		t.Fatalf("a dropped connection must not be refreshed again, GitHub was asked %d more times", h.tokenCalls-calls)
	}
}

// TestDisconnectForgetsAndRevokes proves disconnect revokes at GitHub, drops
// custody and the row, and succeeds when there is nothing to disconnect.
func TestDisconnectForgetsAndRevokes(t *testing.T) {
	h, app := withHub(t)
	connectAs(t, app, "acme", "alice", "octo-alice")
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/disconnect", "acme", "alice", false, nil)
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"revoked":true`) {
		t.Fatalf("disconnect want 200 and revoked, got %d (%s)", r.Code, r.Body)
	}
	if len(h.revoked) != 1 || !strings.HasPrefix(h.revoked[0], "tok-octo-alice-") {
		t.Fatalf("disconnect must revoke the token at GitHub, got %v", h.revoked)
	}
	path, _ := userPath("acme", "alice", githubUserID, defaultLabel)
	if tok, err := kmsGet(mounted, path, accessSecret); err == nil && len(tok) > 0 {
		t.Fatal("disconnect must drop the sealed token")
	}
	if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); got.Connected {
		t.Fatalf("disconnected must read not connected: %+v", got)
	}
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/disconnect", "acme", "alice", false, nil); r.Code != http.StatusOK ||
		!strings.Contains(string(r.Body), `"revoked":false`) {
		t.Fatalf("disconnecting nothing want 200 and nothing revoked, got %d (%s)", r.Code, r.Body)
	}
	// GitHub refusing the revocation is said, not swallowed.
	connectAs(t, app, "acme", "alice", "octo-alice")
	h.mu.Lock()
	h.failRevoke = true
	h.mu.Unlock()
	if r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/disconnect", "acme", "alice", false, nil); r.Code != http.StatusOK ||
		!strings.Contains(string(r.Body), `"revoked":false`) {
		t.Fatalf("a revocation GitHub refused must say revoked:false, got %d (%s)", r.Code, r.Body)
	}
}

// ── the repository index ────────────────────────────────────────────────────

// seed gives the hub n repositories in installation id owned by account, pushed
// one minute apart, the first most recently.
func (h *fakeHub) seed(id int64, account string, n int, prefix string) {
	h.accounts[id] = account
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		h.repos = append(h.repos, &hubRepo{
			full: fmt.Sprintf("%s/%s%03d", account, prefix, i), branch: "main", install: id,
			pushed: base.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339),
			push:   map[string]bool{}, branches: []string{"main"},
		})
	}
}

type reposPage struct {
	Repos     []githubRepoItem `json:"repos"`
	Next      string           `json:"next"`
	Total     int              `json:"total"`
	Unread    []string         `json:"unread"`
	Connected bool             `json:"connected"`
}

func listRepos(t *testing.T, app *zip.App, org, user string, admin bool, query string) reposPage {
	t.Helper()
	r := asPerson(t, app, http.MethodGet, "/v1/provider/github/repos"+query, org, user, admin, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("repos%s want 200, got %d (%s)", query, r.Code, r.Body)
	}
	var out reposPage
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("repos body: %v (%s)", err, r.Body)
	}
	return out
}

// TestReposPageSearchAndOrder proves the listing is most-recently-pushed first,
// searched and narrowed on the server, and that walking `next` visits every
// repository exactly once — across two installations and GitHub's own paging.
func TestReposPageSearchAndOrder(t *testing.T) {
	h, app := withHub(t)
	h.seed(11, "hanzoai", 150, "svc") // two GitHub pages
	h.seed(12, "luxfi", 30, "node")
	h.reach["octo-alice"] = []int64{11, 12}
	// A repository never pushed lists last.
	h.repos = append(h.repos, &hubRepo{full: "luxfi/empty", install: 12, branch: "main", push: map[string]bool{}})
	connectAs(t, app, "acme", "alice", "octo-alice")

	first := listRepos(t, app, "acme", "alice", false, "?limit=50")
	if !first.Connected || first.Total != 181 || len(first.Repos) != 50 || first.Next == "" {
		t.Fatalf("first page: connected=%v total=%d len=%d next=%q", first.Connected, first.Total, len(first.Repos), first.Next)
	}
	// hanzoai/svc000 and luxfi/node000 were pushed at the same instant; the name
	// breaks the tie.
	if first.Repos[0].FullName != "hanzoai/svc000" || first.Repos[1].FullName != "luxfi/node000" {
		t.Fatalf("order: want svc000, node000 first; got %s, %s", first.Repos[0].FullName, first.Repos[1].FullName)
	}
	if it := first.Repos[0]; it.Owner != "hanzoai" || it.Name != "svc000" || it.InstallationID != 11 || it.DefaultBranch != "main" || it.PushedAt == "" {
		t.Fatalf("item shape: %+v", it)
	}
	seen := map[string]bool{}
	var prev string
	for page, q := first, ""; ; {
		for _, it := range page.Repos {
			if seen[it.FullName] {
				t.Fatalf("%s listed twice", it.FullName)
			}
			seen[it.FullName] = true
			if prev != "" && it.PushedAt != "" && prev < it.PushedAt {
				t.Fatalf("not most-recent-first: %s after %s", it.PushedAt, prev)
			}
			if it.PushedAt != "" {
				prev = it.PushedAt
			}
		}
		if page.Next == "" {
			break
		}
		q = "?limit=50&after=" + url.QueryEscape(page.Next)
		page = listRepos(t, app, "acme", "alice", false, q)
	}
	if len(seen) != 181 {
		t.Fatalf("walking next visited %d repositories, want 181", len(seen))
	}
	all := listRepos(t, app, "acme", "alice", false, "?limit=100&q=empty")
	if all.Total != 1 || all.Repos[0].FullName != "luxfi/empty" {
		t.Fatalf("search: %+v", all)
	}
	lux := listRepos(t, app, "acme", "alice", false, "?owner=LUXFI&q=NODE00")
	if lux.Total != 10 {
		t.Fatalf("owner + case-insensitive search: want 10 (node000-node009), got %d", lux.Total)
	}
	for _, it := range lux.Repos {
		if it.Owner != "luxfi" || !strings.Contains(it.Name, "node00") {
			t.Fatalf("filtered page carries %s", it.FullName)
		}
	}
	if r := asPerson(t, app, http.MethodGet, "/v1/provider/github/repos?after=@@@", "acme", "alice", false, nil); r.Code != http.StatusBadRequest {
		t.Fatalf("a forged cursor want 400, got %d", r.Code)
	}
}

// TestReposAreThePersonsOwn proves each person sees exactly what their own
// token reaches, an org admin who has not connected sees the org's
// installations, a member who has not connected sees nothing and is asked to
// connect — and no org can read another's.
func TestReposAreThePersonsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, app := withHub(t)
		h.seed(21, "hanzoai", 3, "a")
		h.seed(22, "zooai", 2, "z")
		h.reach["octo-alice"] = []int64{21}
		h.reach["octo-bob"] = []int64{22}
		connectAs(t, app, "acme", "alice", "octo-alice")
		connectAs(t, app, "acme", "bob", "octo-bob")
		// acme holds the zooai installation.
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "acme", Provider: "github", Label: "zooai", ExternalID: "22", AccountLabel: "zooai",
		}); err != nil {
			t.Fatal(err)
		}
		names := func(p reposPage) []string {
			var out []string
			for _, it := range p.Repos {
				out = append(out, it.FullName)
			}
			sort.Strings(out)
			return out
		}
		if got := names(listRepos(t, app, "acme", "alice", false, "")); strings.Join(got, ",") != "hanzoai/a000,hanzoai/a001,hanzoai/a002" {
			t.Fatalf("alice sees %v", got)
		}
		if got := names(listRepos(t, app, "acme", "bob", false, "")); strings.Join(got, ",") != "zooai/z000,zooai/z001" {
			t.Fatalf("bob sees %v", got)
		}
		// Carol connected nothing: a member is asked to connect, an admin sees the
		// org's installations.
		if p := listRepos(t, app, "acme", "carol", false, ""); p.Connected || len(p.Repos) != 0 || p.Total != 0 {
			t.Fatalf("an unconnected member must see nothing, got %+v", p)
		}
		if p := listRepos(t, app, "acme", "carol", true, ""); p.Connected || strings.Join(names(p), ",") != "zooai/z000,zooai/z001" {
			t.Fatalf("an unconnected admin must see the org's installations, got %+v", p)
		}
		// Alice in ANOTHER org is a different custody: her acme connection and its
		// cached index do not follow her.
		if p := listRepos(t, app, "beta", "alice", false, ""); p.Connected || len(p.Repos) != 0 {
			t.Fatalf("alice's acme index leaked into beta: %+v", p)
		}
		if p := listRepos(t, app, "beta", "carol", true, ""); len(p.Repos) != 0 {
			t.Fatalf("beta's admin must not see acme's installations: %+v", p)
		}
	})
}

// TestWebhookClearsTheIndex proves the index is served from cache until GitHub
// says a repository changed, and fresh the moment it does.
func TestWebhookClearsTheIndex(t *testing.T) {
	h, app := withHub(t)
	t.Setenv(githubWebhookSecretEnv, "hook")
	h.seed(31, "hanzoai", 2, "r")
	h.reach["octo-alice"] = []int64{31}
	connectAs(t, app, "acme", "alice", "octo-alice")
	if p := listRepos(t, app, "acme", "alice", false, ""); p.Total != 2 {
		t.Fatalf("want 2, got %d", p.Total)
	}
	h.mu.Lock()
	h.repos = append(h.repos, &hubRepo{full: "hanzoai/new", install: 31, branch: "main", push: map[string]bool{},
		pushed: time.Now().UTC().Format(time.RFC3339)})
	hits := h.hits()
	h.mu.Unlock()
	if p := listRepos(t, app, "acme", "alice", false, ""); p.Total != 2 || h.hits() != hits {
		t.Fatalf("within five minutes the index is the cache's: total=%d, GitHub read again=%v", p.Total, h.hits() != hits)
	}
	for _, event := range []string{"repository", "installation_repositories"} {
		payload := []byte(`{"action":"created","installation":{"id":31},"repository":{"name":"new","full_name":"hanzoai/new"}}`)
		if r := webhookPost(t, app, event, ghSign("hook", payload), "", payload); r.Code != http.StatusOK {
			t.Fatalf("%s webhook want 200, got %d (%s)", event, r.Code, r.Body)
		}
		p := listRepos(t, app, "acme", "alice", false, "")
		if p.Total != 3 || p.Repos[0].FullName != "hanzoai/new" {
			t.Fatalf("after a %s event the index must be fresh: %+v", event, p)
		}
	}
	// An unsigned delivery clears nothing.
	h.mu.Lock()
	hits = h.hits()
	h.mu.Unlock()
	payload := []byte(`{"installation":{"id":31}}`)
	webhookPost(t, app, "repository", "sha256=00", "", payload)
	listRepos(t, app, "acme", "alice", false, "")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits() != hits {
		t.Fatal("an unsigned delivery must not clear the index")
	}
}

// TestBranchesDefaultFirstWithPrefix proves the default leads, the rest are by
// name, the prefix is GitHub's, paging walks every branch once, and a
// repository outside the caller's view is 404.
func TestBranchesDefaultFirstWithPrefix(t *testing.T) {
	h, app := withHub(t)
	h.seed(41, "hanzoai", 1, "cloud")
	h.seed(42, "private", 1, "vault")
	rp := h.repo("hanzoai/cloud000")
	rp.branch = "trunk"
	rp.branches = []string{"a-first", "feat/login", "feat/api", "trunk", "zeta", "feature"}
	h.reach["octo-alice"] = []int64{41}
	connectAs(t, app, "acme", "alice", "octo-alice")

	get := func(query string) githubBranchesOut {
		t.Helper()
		r := asPerson(t, app, http.MethodGet, "/v1/provider/github/repos/hanzoai/cloud000/branches"+query, "acme", "alice", false, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("branches%s want 200, got %d (%s)", query, r.Code, r.Body)
		}
		var out githubBranchesOut
		_ = json.Unmarshal(r.Body, &out)
		return out
	}
	all := get("")
	var got []string
	for _, b := range all.Branches {
		got = append(got, b.Name)
	}
	if strings.Join(got, ",") != "trunk,a-first,feat/api,feat/login,feature,zeta" || !all.Branches[0].Default || all.Total != 6 {
		t.Fatalf("default first then by name: %v (total %d)", got, all.Total)
	}
	feat := get("?q=feat/")
	if feat.Total != 2 || feat.Branches[0].Name != "feat/api" || feat.Branches[1].Name != "feat/login" {
		t.Fatalf("prefix search: %+v", feat)
	}
	// Two at a time, starting with the default: every branch once.
	var walked []string
	for page := get("?limit=1"); ; page = get("?limit=2&after=" + url.QueryEscape(page.Next)) {
		for _, b := range page.Branches {
			walked = append(walked, b.Name)
		}
		if page.Next == "" {
			break
		}
	}
	if strings.Join(walked, ",") != strings.Join(got, ",") {
		t.Fatalf("paging walked %v, want %v", walked, got)
	}
	// A repository alice's token does not reach answers exactly like one that does
	// not exist.
	for _, path := range []string{"private/vault000", "hanzoai/nope"} {
		if r := asPerson(t, app, http.MethodGet, "/v1/provider/github/repos/"+path+"/branches", "acme", "alice", false, nil); r.Code != http.StatusNotFound {
			t.Fatalf("%s want 404, got %d (%s)", path, r.Code, r.Body)
		}
	}
	if r := asPerson(t, app, http.MethodGet, "/v1/provider/github/repos/hanzoai/cloud000/branches?q=../x", "acme", "alice", false, nil); r.Code != http.StatusBadRequest {
		t.Fatalf("a traversal prefix want 400, got %d", r.Code)
	}
}

// ── admission to a run ──────────────────────────────────────────────────────

// runAs asks the plane op as a caller, the way the coding engine does. admin is
// the bit the CALLER carries; whether the person administers the org is IAM's
// answer (iamAdmins), and the mint reads that one.
func runAs(org, user string, admin bool, full string) (*client.GitHubRun, error) {
	owner, repo, _ := strings.Cut(full, "/")
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user, OrgAdmin: admin})
	return planeGitHubRun(ctx, &client.GitHubRunIn{Owner: owner, Repo: repo})
}

// iamAdmins stands IAM's role read up on the plane: the people named, keyed
// "org/user", administer that org NOW, and nobody else does. Change the map to
// promote or demote someone between two calls.
type iamAdmins struct {
	mu    sync.Mutex
	of    map[string]bool
	named map[string]bool // on a tenant role called "System Manager", not admins
}

func serveIAM(t *testing.T, admins ...string) *iamAdmins {
	t.Helper()
	a := &iamAdmins{of: map[string]bool{}, named: map[string]bool{}}
	for _, k := range admins {
		a.of[k] = true
	}
	planetest.Ruled(t, func(org, user string) client.Roles {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.of[org+"/"+user] {
			return client.Roles{Roles: []string{"System Manager"}, Admin: true}
		}
		// A role a tenant named "System Manager" is the NAME and not the grant.
		if a.named[org+"/"+user] {
			return client.Roles{Roles: []string{"System Manager"}}
		}
		return client.Roles{Roles: []string{}}
	})
	return a
}

func (a *iamAdmins) set(key string, admin bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.of[key] = admin
}

// TestRunIsAdmittedByGitHubAccess proves who may start a run on a repository
// the forge does not hold: a person whose own GitHub access pushes to it, or an
// org admin on the org's own installation — and nobody else, with nothing
// minted for them.
func TestRunIsAdmittedByGitHubAccess(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, app := withHub(t)
		iam := serveIAM(t, "acme/carol", "beta/carol")
		h.seed(51, "hanzoai", 2, "r")
		h.seed(52, "zooai", 1, "z")
		h.repo("hanzoai/r000").push["octo-alice"] = true // alice pushes r000; reads r001
		h.reach["octo-alice"] = []int64{51}
		connectAs(t, app, "acme", "alice", "octo-alice")
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "acme", Provider: "github", Label: "zooai", ExternalID: "52", AccountLabel: "zooai",
		}); err != nil {
			t.Fatal(err)
		}

		run, err := runAs("acme", "alice", false, "hanzoai/r000")
		if err != nil || run.Token == "" || run.Owner != "hanzoai" || run.Repo != "r000" || run.Base != "main" || run.Login != "octo-alice" {
			t.Fatalf("alice on a repository she pushes to must be admitted: %+v %v", run, err)
		}
		refusals := []struct {
			name, org, user string
			admin           bool
			repo            string
		}{
			{"read-only access", "acme", "alice", false, "hanzoai/r001"},
			{"a repository her token does not reach", "acme", "alice", false, "zooai/z000"},
			{"a member who has not connected", "acme", "bob", false, "hanzoai/r000"},
			{"a member on the org's own installation", "acme", "bob", false, "zooai/z000"},
			{"an admin on an installation another org holds", "beta", "carol", true, "zooai/z000"},
			{"an admin on an account no org holds", "acme", "carol", true, "hanzoai/r000"},
		}
		before := len(h.minted)
		for _, c := range refusals {
			if _, err := runAs(c.org, c.user, c.admin, c.repo); err == nil {
				t.Fatalf("%s must be refused", c.name)
			}
		}
		if len(h.minted) != before {
			t.Fatalf("a refused run must mint nothing, minted %v", h.minted[before:])
		}
		// An org admin runs on the org's own installation without a connection.
		if run, err := runAs("acme", "carol", true, "zooai/z000"); err != nil || run.Login != "" || run.Repo != "z000" {
			t.Fatalf("an admin on the org's installation must be admitted: %+v %v", run, err)
		}
		// An org admin whose own access is read-only still runs on the org's own.
		h.reach["octo-alice"] = append(h.reach["octo-alice"], 52)
		iam.set("acme/alice", true)
		if _, err := runAs("acme", "alice", true, "zooai/z000"); err != nil {
			t.Fatalf("an admin falls back to the org's installation: %v", err)
		}
		// No org on the call is refused before anything else.
		if _, err := planeGitHubRun(context.Background(), &client.GitHubRunIn{Owner: "hanzoai", Repo: "r000"}); err == nil {
			t.Fatal("a call with no org must be refused")
		}
		_ = app
	})
}

// TestRepoLookupIsTheListingsView proves the plane lookup answers from exactly
// the view the listing does: a person's own, an admin's org installations, and
// 404 for everything else.
func TestRepoLookupIsTheListingsView(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, _ := withHub(t)
		h.seed(61, "hanzoai", 1, "site")
		h.seed(62, "zooai", 1, "z")
		h.repo("hanzoai/site000").private = true
		h.reach["octo-alice"] = []int64{61}
		if err := githubUserSave(context.Background(), mounted, "acme", "alice", &ExchangeResult{
			Tokens: map[string]string{accessSecret: "tok-octo-alice-9"}, AccountLabel: "octo-alice",
		}); err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		h.access["tok-octo-alice-9"] = "octo-alice"
		h.mu.Unlock()
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "acme", Provider: "github", Label: "zooai", ExternalID: "62", AccountLabel: "zooai",
		}); err != nil {
			t.Fatal(err)
		}
		ask := func(org, user string, admin bool, owner, repo string) (*client.GitHubRepo, error) {
			ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user, OrgAdmin: admin})
			return planeGitHubRepo(ctx, &client.GitHubRepoIn{Owner: owner, Repo: repo})
		}
		if r, err := ask("acme", "alice", false, "HANZOAI", "site000"); err != nil || r.Owner != "hanzoai" ||
			!r.Private || r.Installation != 61 || !r.Personal || r.Base != "main" {
			t.Fatalf("alice's own repository: %+v %v", r, err)
		}
		if r, err := ask("acme", "carol", true, "zooai", "z000"); err != nil || r.Personal || r.Installation != 62 {
			t.Fatalf("an admin's org installation: %+v %v", r, err)
		}
		for _, c := range []struct {
			org, user  string
			admin      bool
			owner, rep string
		}{
			{"acme", "alice", false, "zooai", "z000"},
			{"acme", "bob", false, "hanzoai", "site000"},
			{"beta", "carol", true, "zooai", "z000"},
		} {
			if _, err := ask(c.org, c.user, c.admin, c.owner, c.rep); err == nil {
				t.Fatalf("%s/%s must not see %s/%s", c.org, c.user, c.owner, c.rep)
			}
		}
	})
}

// TestAGrantCompletesOnce races completions of one parked grant: exactly one
// exchanges the code.
func TestAGrantCompletesOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, app := withHub(t)
		r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false, nil)
		grant := callbackGrant(t, app, stateOf(t, r.Body), "code-octo-alice")
		h.mu.Lock()
		before := h.tokenCalls
		h.mu.Unlock()
		var wg sync.WaitGroup
		codes := make([]int, 8)
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = asPerson(t, app, http.MethodPost, "/v1/provider/github/user/complete", "acme", "alice", false,
					map[string]string{"grant": grant}).Code
			}(i)
		}
		wg.Wait()
		ok := 0
		for _, c := range codes {
			if c == http.StatusOK {
				ok++
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if ok != 1 || h.tokenCalls-before != 1 {
			t.Fatalf("one grant must complete once: %d successes %v, %d exchanges", ok, codes, h.tokenCalls-before)
		}
	})
}

// TestAWebhookClearsOnlyTheViewsItNames proves a signed delivery for one
// installation costs nothing to a view filled from others — installing the App
// on an account of one's own and churning it reaches nobody else.
func TestAWebhookClearsOnlyTheViewsItNames(t *testing.T) {
	h, app := withHub(t)
	t.Setenv(githubWebhookSecretEnv, "hook")
	h.seed(71, "hanzoai", 2, "r")
	h.seed(99, "mallory", 1, "m")
	h.reach["octo-alice"] = []int64{71}
	h.reach["octo-mal"] = []int64{99}
	connectAs(t, app, "acme", "alice", "octo-alice")
	connectAs(t, app, "evil", "mal", "octo-mal")
	listRepos(t, app, "acme", "alice", false, "")
	listRepos(t, app, "evil", "mal", false, "")
	h.mu.Lock()
	hits := h.hits()
	h.mu.Unlock()
	for range 3 {
		payload := []byte(`{"action":"renamed","installation":{"id":99}}`)
		if r := webhookPost(t, app, "repository", ghSign("hook", payload), "", payload); r.Code != http.StatusOK {
			t.Fatalf("webhook want 200, got %d", r.Code)
		}
	}
	listRepos(t, app, "acme", "alice", false, "")
	h.mu.Lock()
	if h.hits() != hits {
		h.mu.Unlock()
		t.Fatal("a webhook for another installation must not refill alice's view")
	}
	h.mu.Unlock()
	listRepos(t, app, "evil", "mal", false, "")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits() != hits+1 {
		t.Fatalf("the view filled from installation 99 must refill once, GitHub was asked %d times", h.hits()-hits)
	}
}

// TestAForgetDuringAFillIsNotUndone proves a webhook landing while a view is
// being read is not overwritten by that read.
func TestAForgetDuringAFillIsNotUndone(t *testing.T) {
	c := newIndexCache()
	began := time.Now()
	c.forget(5)
	c.put("k", &indexed{installs: map[int64]bool{5: true}, at: time.Now()}, began)
	if _, ok := c.get("k"); ok {
		t.Fatal("a view read before its installation was forgotten must not be kept")
	}
	c.put("k", &indexed{installs: map[int64]bool{5: true}, at: time.Now()}, time.Now())
	if _, ok := c.get("k"); !ok {
		t.Fatal("a view read after the forget must be kept")
	}
	c.forgetUser("o", "u")
	c.put(userKey("o", "u"), &indexed{at: time.Now()}, began)
	if _, ok := c.get(userKey("o", "u")); ok {
		t.Fatal("a person's view read before they were forgotten must not be kept")
	}
}

// TestAnAccountBoundIsInTheNextListing proves a GitHub account bound to an org is
// listed the next time its people ask, not when their views expire. Its
// installation is new to every view filled before it, so the webhook GitHub
// sends for it clears none of them; another org's view stays as it was.
func TestAnAccountBoundIsInTheNextListing(t *testing.T) {
	h, app := withHub(t)
	h.seed(71, "hanzoai", 2, "r")
	h.seed(99, "mallory", 1, "m")
	h.reach["octo-alice"] = []int64{71}
	h.reach["octo-mal"] = []int64{99}
	connectAs(t, app, "acme", "alice", "octo-alice")
	connectAs(t, app, "evil", "mal", "octo-mal")
	if got := listRepos(t, app, "acme", "alice", false, ""); got.Total != 2 {
		t.Fatalf("before her own account is installed alice lists %d repositories, want 2", got.Total)
	}
	listRepos(t, app, "evil", "mal", false, "")

	// Alice installs the App on her own account, and acme binds it.
	h.mu.Lock()
	h.seed(72, "alice", 1, "own")
	h.reach["octo-alice"] = []int64{71, 72}
	h.mu.Unlock()
	if err := bind(context.Background(), mounted, registry["github"], Connection{
		Org: "acme", Provider: "github", Label: "alice", ExternalID: "72", AccountLabel: "alice",
	}, nil); err != nil {
		t.Fatal(err)
	}
	got := listRepos(t, app, "acme", "alice", false, "")
	if got.Total != 3 || !slices.ContainsFunc(got.Repos, func(r githubRepoItem) bool { return r.FullName == "alice/own000" }) {
		t.Fatalf("the account acme just bound must be in alice's next listing, got %d: %+v", got.Total, got.Repos)
	}

	h.mu.Lock()
	hits := h.hits()
	h.mu.Unlock()
	listRepos(t, app, "evil", "mal", false, "")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hits() != hits {
		t.Fatal("an account bound to acme must not refill another org's view")
	}
}

// TestARePointedAdminIsNotTheOrgsAdmin proves an admin bit carried into another
// org by an acting-as call admits nothing there.
func TestARePointedAdminIsNotTheOrgsAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, _ := withHub(t)
		serveIAM(t, "victim/mallory") // even an admin of that org by IAM's word
		h.seed(81, "victimco", 1, "app")
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "victim", Provider: "github", Label: "victimco", ExternalID: "81", AccountLabel: "victimco",
		}); err != nil {
			t.Fatal(err)
		}
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "victim", User: "mallory", OrgAdmin: true, ActedBy: "evil/mallory"})
		if _, err := planeGitHubRun(ctx, &client.GitHubRunIn{Owner: "victimco", Repo: "app000"}); err == nil {
			t.Fatal("an admin re-pointed into another org must not be admitted there")
		}
		if _, err := planeGitHubRepo(ctx, &client.GitHubRepoIn{Owner: "victimco", Repo: "app000"}); err == nil {
			t.Fatal("an admin re-pointed into another org must not see its installations")
		}
	})
}

// TestEveryMintIsRecorded proves a minted run token leaves one audit record
// naming the person, the repository and how they were admitted.
func TestEveryMintIsRecorded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, _ := withHub(t)
		serveIAM(t, "acme/carol")
		rec, err := audit.Open(t.TempDir(), "provider", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		mounted.State.audit = rec
		h.seed(91, "hanzoai", 1, "r")
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "acme", Provider: "github", Label: "hanzoai", ExternalID: "91", AccountLabel: "hanzoai",
		}); err != nil {
			t.Fatal(err)
		}
		before, _ := rec.Head()
		if _, err := runAs("acme", "carol", true, "hanzoai/r000"); err != nil {
			t.Fatal(err)
		}
		after, _ := rec.Head()
		if after != before+1 {
			t.Fatalf("a mint must append one audit record, head went %d -> %d", before, after)
		}
		if src := mintSource(t, rec); src != "iam" {
			t.Fatalf("an admin's mint must record that IAM admitted it, got source %q", src)
		}
	})
}

// mintSource reads the live source the newest github.run.mint record names.
func mintSource(t *testing.T, rec *audit.Recorder) string {
	t.Helper()
	return mintRecord(t, rec)["source"]
}

// mintRecord is the newest github.run.mint row's after-image.
func mintRecord(t *testing.T, rec *audit.Recorder) map[string]string {
	t.Helper()
	rows, _, err := rec.Query(context.Background(), audit.Filter{Action: "github.run.mint", Limit: 1})
	if err != nil || len(rows) == 0 {
		t.Fatalf("read the mint record: %d rows, %v", len(rows), err)
	}
	var after map[string]string
	if err := json.Unmarshal(rows[0].After, &after); err != nil {
		t.Fatalf("mint record after: %v", err)
	}
	return after
}

// TestAPlanRunIsMintedAReadToken proves a plan's token can read and nothing
// else, on both ways a run is admitted, and that the trail says which it was.
func TestAPlanRunIsMintedAReadToken(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, app := withHub(t)
		serveIAM(t, "acme/carol")
		rec, err := audit.Open(t.TempDir(), "provider", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		mounted.State.audit = rec
		h.seed(97, "hanzoai", 1, "r")
		h.repo("hanzoai/r000").push["octo-alice"] = true
		h.reach["octo-alice"] = []int64{97}
		connectAs(t, app, "acme", "alice", "octo-alice")
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "acme", Provider: "github", Label: "hanzoai", ExternalID: "97", AccountLabel: "hanzoai",
		}); err != nil {
			t.Fatal(err)
		}
		read := func(org, user string) {
			t.Helper()
			ctx := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user})
			if _, err := planeGitHubRun(ctx, &client.GitHubRunIn{Owner: "hanzoai", Repo: "r000", Read: true}); err != nil {
				t.Fatalf("%s's plan must be admitted: %v", user, err)
			}
			h.mu.Lock()
			asked := h.perms[len(h.perms)-1]
			h.mu.Unlock()
			if len(asked) != 1 || asked["contents"] != "read" {
				t.Fatalf("%s's plan token was asked for %v, want contents:read alone", user, asked)
			}
			if got := mintRecord(t, rec)["access"]; got != "read" {
				t.Fatalf("%s's plan mint recorded access %q, want read", user, got)
			}
		}
		read("acme", "alice") // on her own access
		read("acme", "carol") // on the org's installation, as IAM's admin
		if _, err := runAs("acme", "alice", false, "hanzoai/r000"); err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		asked := h.perms[len(h.perms)-1]
		h.mu.Unlock()
		if asked["contents"] != "write" || asked["pull_requests"] != "write" {
			t.Fatalf("a build's token was asked for %v, want write", asked)
		}
		if got := mintRecord(t, rec)["access"]; got != "write" {
			t.Fatalf("a build's mint recorded access %q, want write", got)
		}
	})
}

// TestADemotedAdminIsMintedNothing proves the admin bit a caller carries admits
// nothing: a run's completion states the person it was started by, and an org
// admin demoted since then — IAM no longer calls them one — gets no token on the
// org's installation, whatever bit rode in with the call. Promoted again, the
// same call mints, and the record names IAM as what admitted it.
func TestADemotedAdminIsMintedNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, app := withHub(t)
		iam := serveIAM(t)
		rec, err := audit.Open(t.TempDir(), "provider", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		mounted.State.audit = rec
		h.seed(95, "hanzoai", 1, "r")
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "acme", Provider: "github", Label: "hanzoai", ExternalID: "95", AccountLabel: "hanzoai",
		}); err != nil {
			t.Fatal(err)
		}
		before, _ := rec.Head()
		minted := len(h.minted)
		for _, carried := range []bool{true, false} {
			if _, err := runAs("acme", "dave", carried, "hanzoai/r000"); err == nil {
				t.Fatalf("a demoted admin carrying admin=%v must be refused", carried)
			}
		}
		if len(h.minted) != minted {
			t.Fatalf("a demoted admin was minted %v", h.minted[minted:])
		}
		if head, _ := rec.Head(); head != before {
			t.Fatalf("a refused mint must record no mint, head went %d -> %d", before, head)
		}
		// A role a tenant named "System Manager" is not the grant either.
		iam.mu.Lock()
		iam.named["acme/dave"] = true
		iam.mu.Unlock()
		if _, err := runAs("acme", "dave", true, "hanzoai/r000"); err == nil || len(h.minted) != minted {
			t.Fatalf("a member on a role named System Manager must be minted nothing: %v", err)
		}
		iam.set("acme/dave", true)
		if _, err := runAs("acme", "dave", false, "hanzoai/r000"); err != nil {
			t.Fatalf("an admin by IAM's word is admitted whatever the caller carries: %v", err)
		}
		if src := mintSource(t, rec); src != "iam" {
			t.Fatalf("the mint must record IAM as its source, got %q", src)
		}
		// A person whose own GitHub pushes there is admitted on that, and the record
		// says so — IAM is not what admitted them.
		h.repo("hanzoai/r000").push["octo-erin"] = true
		h.reach["octo-erin"] = []int64{95}
		connectAs(t, app, "acme", "erin", "octo-erin")
		if _, err := runAs("acme", "erin", false, "hanzoai/r000"); err != nil {
			t.Fatalf("a person who pushes there is admitted: %v", err)
		}
		if src := mintSource(t, rec); src != "github" {
			t.Fatalf("a person's mint must record GitHub as its source, got %q", src)
		}
	})
}

// TestIAMSilenceIsNoAdmin proves an IAM that cannot be asked admits no one on the
// org's installation: the mint fails closed rather than reading the caller's bit.
func TestIAMSilenceIsNoAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, _ := withHub(t)
		t.Setenv("ZIP_RUNTIME_DIR", t.TempDir()) // nothing listening for iam
		h.seed(96, "hanzoai", 1, "r")
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "acme", Provider: "github", Label: "hanzoai", ExternalID: "96", AccountLabel: "hanzoai",
		}); err != nil {
			t.Fatal(err)
		}
		minted := len(h.minted)
		if _, err := runAs("acme", "carol", true, "hanzoai/r000"); err == nil {
			t.Fatal("with IAM silent an admin bit must admit nothing")
		}
		if len(h.minted) != minted {
			t.Fatalf("minted %v with IAM silent", h.minted[minted:])
		}
	})
}

// flakyKMS is custody that cannot read refresh tokens right now.
type flakyKMS struct{ secret.Store }

func (f flakyKMS) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	if strings.Contains(ref, "/"+refreshSecret+"@") {
		return nil, fmt.Errorf("kms: connection refused")
	}
	return f.Store.GetSecret(ctx, ref)
}

// TestAStoreOutageIsNotADeadCredential proves custody that could not be read is
// not custody holding nothing: the read fails and the connection stands.
func TestAStoreOutageIsNotADeadCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, app := withHub(t)
		h.expires = 30
		connectAs(t, app, "acme", "alice", "octo-alice")
		real := mounted.State.kms
		mounted.State.kms = flakyKMS{real}
		_, _, ok, err := personToken(context.Background(), mounted, "acme", "alice")
		mounted.State.kms = real
		if ok || err == nil {
			t.Fatalf("an unreadable refresh token must fail the read, got ok=%v err=%v", ok, err)
		}
		if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); !got.Connected {
			t.Fatalf("a store outage must not drop the connection: %+v", got)
		}
	})
}

// planeKMS answers a delete of a secret the store does not hold the way the plane
// peer does: cloud.ErrNoSecret in the op's words, with the store's own sentinel
// gone.
type planeKMS struct{ secret.Store }

func (p planeKMS) DeleteSecret(ctx context.Context, ref string) error {
	if err := p.Store.DeleteSecret(ctx, ref); errors.Is(err, secret.ErrNone) {
		return fmt.Errorf("kms kms_delete: %w", secret.ErrNone)
	} else {
		return err
	}
}

// TestDisconnectWithNoRefreshTokenOverThePlane: a connection with no refresh
// token on file disconnects cleanly when custody is the plane peer, whose answer
// to deleting the absent refresh token is cloud.ErrNoSecret.
func TestDisconnectWithNoRefreshTokenOverThePlane(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, app := withHub(t)
		connectAs(t, app, "acme", "alice", "octo-alice")
		path, _ := userPath("acme", "alice", githubUserID, defaultLabel)
		if err := kmsDelete(mounted, path, refreshSecret); err != nil {
			t.Fatal(err)
		}
		if _, err := kmsGet(mounted, path, refreshSecret); !errors.Is(err, secret.ErrNone) {
			t.Fatalf("precondition: no refresh token on file, got %v", err)
		}
		real := mounted.State.kms
		mounted.State.kms = planeKMS{real}
		t.Cleanup(func() { mounted.State.kms = real })
		r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/disconnect", "acme", "alice", false, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("disconnect with no refresh token want 200, got %d (%s)", r.Code, r.Body)
		}
		if got := userOut(t, asPerson(t, app, http.MethodGet, "/v1/provider/github/user", "acme", "alice", false, nil)); got.Connected {
			t.Fatalf("disconnected must read not connected: %+v", got)
		}
	})
}
