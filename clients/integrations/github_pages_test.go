package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// github_pages_test.go proves GitHub Pages management drives the real
// mint→grant→Pages-API path against a recording mock GitHub: owner is derived
// SERVER-SIDE from the installation's granted full_name (never the client), an
// ungranted repo never reaches the Pages API, the custom-domain set/clear maps to
// the exact GitHub body, and a GitHub error never leaks the installation token.

type recordedReq struct {
	Method string
	Path   string
	Body   string
}

// pagesMock stands in for the GitHub API for the Pages surface: it mints tokens,
// reports the installation account, lists granted repos, and serves the Pages
// resource — recording every request so a test can assert the exact owner/name/body
// the handler sent. noPages makes the site resource 404 (Pages not enabled).
type pagesMock struct {
	mu            sync.Mutex
	reqs          []recordedReq
	repos         []map[string]any
	getSite       map[string]any
	noPages       bool
	repoListCalls int // count of GET /installation/repositories (grant-set enumerations)
	srv           *httptest.Server
}

func newPagesMock(t *testing.T, repos []map[string]any) *pagesMock {
	t.Helper()
	m := &pagesMock{repos: repos}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case strings.HasPrefix(p, "/app/installations/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"login": "acme-gh"}})
		case p == "/installation/repositories":
			m.mu.Lock()
			m.repoListCalls++
			m.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(m.repos), "repositories": m.repos})
		case strings.HasSuffix(p, "/pages/builds"):
			m.record(r)
			if m.noPages {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "queued", "url": "https://api.github.com/x/pages/builds/1"})
		case strings.HasSuffix(p, "/pages"):
			m.record(r)
			m.servePages(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *pagesMock) record(r *http.Request) {
	var body string
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	m.mu.Lock()
	m.reqs = append(m.reqs, recordedReq{Method: r.Method, Path: r.URL.Path, Body: body})
	m.mu.Unlock()
}

func (m *pagesMock) servePages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if m.noPages {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		site := m.getSite
		if site == nil {
			site = map[string]any{"status": "built", "html_url": "https://acme-gh.github.io/widgets", "source": map[string]any{"branch": "main", "path": "/"}}
		}
		_ = json.NewEncoder(w).Encode(site)
	case http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "building", "html_url": "https://acme-gh.github.io/widgets", "source": map[string]any{"branch": "main", "path": "/"}})
	case http.MethodPut:
		if m.noPages {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if m.noPages {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *pagesMock) find(method, path string) (recordedReq, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reqs {
		if r.Method == method && r.Path == path {
			return r, true
		}
	}
	return recordedReq{}, false
}

func (m *pagesMock) sawPath(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reqs {
		if r.Path == path {
			return true
		}
	}
	return false
}

// connectOrg wires org→installation on the mounted store so the token mints.
func connectOrg(t *testing.T, org, instID, account string) {
	t.Helper()
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: org, Provider: "github", ExternalID: instID, AccountLabel: account,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

var widgets = []map[string]any{
	{"name": "widgets", "full_name": "acme-gh/widgets", "private": true, "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
}

// TestPagesGet_ReturnsStatusAndURL: a granted repo with Pages returns the live URL.
func TestPagesGet_ReturnsStatusAndURL(t *testing.T) {
	m := newPagesMock(t, widgets)
	m.getSite = map[string]any{
		"status": "built", "cname": "www.example.com", "html_url": "https://www.example.com",
		"build_type": "legacy", "https_enforced": true,
		"source": map[string]any{"branch": "main", "path": "/"},
	}
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", r.Code, r.Body)
	}
	var v githubPagesView
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("json: %v", err)
	}
	if v.Repo != "widgets" || v.URL != "https://www.example.com" || v.Status != "built" || v.CNAME != "www.example.com" || !v.HTTPSEnforced {
		t.Fatalf("unexpected view: %+v", v)
	}
	if v.Source == nil || v.Source.Branch != "main" || v.Source.Path != "/" {
		t.Fatalf("unexpected source: %+v", v.Source)
	}
	// Owner in the GitHub path is server-derived from full_name.
	if !m.sawPath("/repos/acme-gh/widgets/pages") {
		t.Fatalf("expected GET /repos/acme-gh/widgets/pages, saw %+v", m.reqs)
	}
}

// TestPagesGet_OwnerFromGrantNotClient: the owner is taken from GitHub's full_name,
// never guessed from the connection account or the client. Grant a repo whose owner
// differs from the connection's AccountLabel and prove the Pages call uses the grant.
func TestPagesGet_OwnerFromGrantNotClient(t *testing.T) {
	repos := []map[string]any{
		{"name": "site", "full_name": "octocorp/site", "private": false, "default_branch": "main", "clone_url": "https://github.com/octocorp/site.git"},
	}
	m := newPagesMock(t, repos)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh") // account label acme-gh, but grant owner is octocorp

	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/site/pages", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", r.Code, r.Body)
	}
	if !m.sawPath("/repos/octocorp/site/pages") {
		t.Fatalf("owner must come from full_name (octocorp), saw %+v", m.reqs)
	}
	if m.sawPath("/repos/acme-gh/site/pages") {
		t.Fatalf("owner must NOT come from the connection account label")
	}
}

// TestPagesGet_UngrantedRepoNeverReachesGitHub: a repo not in the granted set is a
// 404 and the Pages API is never called for it — the cross-tenant guard.
func TestPagesGet_UngrantedRepoNeverReachesGitHub(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/secret-repo/pages", "acme", nil)
	if r.Code != http.StatusNotFound {
		t.Fatalf("ungranted repo want 404, got %d (%s)", r.Code, r.Body)
	}
	if m.sawPath("/repos/acme-gh/secret-repo/pages") {
		t.Fatalf("ungranted repo must never reach the Pages API, saw %+v", m.reqs)
	}
}

// TestPagesGet_NotEnabled404: GitHub 404 on the site resource maps to an honest 404.
func TestPagesGet_NotEnabled404(t *testing.T) {
	m := newPagesMock(t, widgets)
	m.noPages = true
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil); r.Code != http.StatusNotFound {
		t.Fatalf("no-pages want 404, got %d (%s)", r.Code, r.Body)
	}
}

// TestPagesGet_UnconnectedOrg409 / NoPrincipal403: the same fail-closed gates as the
// repo list — an org that never connected GitHub can never touch Pages, and an
// anonymous caller is refused.
func TestPagesGet_GateFailsClosed(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "beta", nil); r.Code != http.StatusConflict {
		t.Fatalf("unconnected org want 409, got %d (%s)", r.Code, r.Body)
	}
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("no principal want 403, got %d", r.Code)
	}
}

// TestPagesUnconfigured503: the whole surface is inert (503) with no App creds.
func TestPagesUnconfigured503(t *testing.T) {
	t.Setenv(githubAppIDEnv, "")
	t.Setenv(githubAppKeyEnv, "")
	t.Setenv(githubAppSlugEnv, "")
	resetGithubApp()
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "1", "acme-gh")
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured want 503, got %d (%s)", r.Code, r.Body)
	}
}

// TestPagesEnable_BranchSource: POST with a branch sends the exact source body and
// returns 201.
func TestPagesEnable_BranchSource(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/widgets/pages", "acme",
		map[string]any{"branch": "gh-pages", "path": "/docs"})
	if r.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", r.Code, r.Body)
	}
	rec, ok := m.find(http.MethodPost, "/repos/acme-gh/widgets/pages")
	if !ok {
		t.Fatalf("expected POST to pages, saw %+v", m.reqs)
	}
	var sent struct {
		Source struct{ Branch, Path string } `json:"source"`
	}
	if err := json.Unmarshal([]byte(rec.Body), &sent); err != nil {
		t.Fatalf("sent body: %v", err)
	}
	if sent.Source.Branch != "gh-pages" || sent.Source.Path != "/docs" {
		t.Fatalf("unexpected source body: %s", rec.Body)
	}
}

// TestPagesEnable_DefaultsToDefaultBranch: POST with no branch defaults to the repo's
// default branch (from the grant), not a client value.
func TestPagesEnable_DefaultsToDefaultBranch(t *testing.T) {
	m := newPagesMock(t, widgets) // default_branch: main
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	if r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/widgets/pages", "acme", map[string]any{}); r.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", r.Code, r.Body)
	}
	rec, _ := m.find(http.MethodPost, "/repos/acme-gh/widgets/pages")
	if !strings.Contains(rec.Body, `"branch":"main"`) {
		t.Fatalf("expected default branch main, got %s", rec.Body)
	}
}

// TestPagesEnable_Workflow: buildType=workflow sends {build_type:workflow}, no source.
func TestPagesEnable_Workflow(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	if r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/widgets/pages", "acme",
		map[string]any{"buildType": "workflow"}); r.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d (%s)", r.Code, r.Body)
	}
	rec, _ := m.find(http.MethodPost, "/repos/acme-gh/widgets/pages")
	if !strings.Contains(rec.Body, `"build_type":"workflow"`) || strings.Contains(rec.Body, `"source"`) {
		t.Fatalf("expected workflow build, no source, got %s", rec.Body)
	}
}

// TestPagesUpdate_SetCNAME: PUT a domain sends cname as that string.
func TestPagesUpdate_SetCNAME(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodPut, "/v1/integrations/github/repos/widgets/pages", "acme",
		map[string]any{"cname": "www.example.com", "httpsEnforced": true})
	if r.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", r.Code, r.Body)
	}
	rec, ok := m.find(http.MethodPut, "/repos/acme-gh/widgets/pages")
	if !ok {
		t.Fatalf("expected PUT, saw %+v", m.reqs)
	}
	if !strings.Contains(rec.Body, `"cname":"www.example.com"`) || !strings.Contains(rec.Body, `"https_enforced":true`) {
		t.Fatalf("unexpected PUT body: %s", rec.Body)
	}
}

// TestPagesUpdate_ClearCNAME: PUT cname="" clears the custom domain (GitHub cname:null).
func TestPagesUpdate_ClearCNAME(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	if r := req(t, app, http.MethodPut, "/v1/integrations/github/repos/widgets/pages", "acme",
		map[string]any{"cname": ""}); r.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", r.Code, r.Body)
	}
	rec, _ := m.find(http.MethodPut, "/repos/acme-gh/widgets/pages")
	if !strings.Contains(rec.Body, `"cname":null`) {
		t.Fatalf("clear must send cname:null, got %s", rec.Body)
	}
}

// TestPagesUpdate_InvalidCNAMENeverSent: a malformed domain is a 400 and no PUT is sent.
func TestPagesUpdate_InvalidCNAMENeverSent(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	if r := req(t, app, http.MethodPut, "/v1/integrations/github/repos/widgets/pages", "acme",
		map[string]any{"cname": "not a domain"}); r.Code != http.StatusBadRequest {
		t.Fatalf("invalid cname want 400, got %d (%s)", r.Code, r.Body)
	}
	if _, ok := m.find(http.MethodPut, "/repos/acme-gh/widgets/pages"); ok {
		t.Fatalf("a rejected cname must never be PUT to GitHub")
	}
}

// TestPagesUpdate_NoFields400: an empty update is a 400 (nothing to change).
func TestPagesUpdate_NoFields400(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	if r := req(t, app, http.MethodPut, "/v1/integrations/github/repos/widgets/pages", "acme",
		map[string]any{}); r.Code != http.StatusBadRequest {
		t.Fatalf("empty update want 400, got %d (%s)", r.Code, r.Body)
	}
}

// TestPagesBuild_Queued: POST builds returns 202 with the queued status.
func TestPagesBuild_Queued(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/widgets/pages/builds", "acme", nil)
	if r.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d (%s)", r.Code, r.Body)
	}
	var out struct{ Repo, Status string }
	_ = json.Unmarshal(r.Body, &out)
	if out.Repo != "widgets" || out.Status != "queued" {
		t.Fatalf("unexpected build view: %s", r.Body)
	}
	if !m.sawPath("/repos/acme-gh/widgets/pages/builds") {
		t.Fatalf("expected POST builds, saw %+v", m.reqs)
	}
}

// TestPagesDisable: DELETE removes the site and reports disabled.
func TestPagesDisable(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodDelete, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", r.Code, r.Body)
	}
	if !strings.Contains(string(r.Body), `"disabled":true`) {
		t.Fatalf("unexpected disable body: %s", r.Body)
	}
	if _, ok := m.find(http.MethodDelete, "/repos/acme-gh/widgets/pages"); !ok {
		t.Fatalf("expected DELETE, saw %+v", m.reqs)
	}
}

// TestPagesErrorNeverLeaksToken: a GitHub 500 surfaces an honest error but never the
// installation token.
func TestPagesErrorNeverLeaksToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case r.URL.Path == "/installation/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(widgets), "repositories": widgets})
		case strings.HasSuffix(r.URL.Path, "/pages"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
	if r.Code != http.StatusBadGateway {
		t.Fatalf("github 500 want 502, got %d (%s)", r.Code, r.Body)
	}
	if strings.Contains(string(r.Body), "ghs_installation_token") {
		t.Fatalf("error body leaked the installation token: %s", r.Body)
	}
}

// TestPagesInputGrammar unit-proves the validation guards at the boundary.
func TestPagesInputGrammar(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{{"widgets", true}, {"acme-gh.github.io", true}, {"a_b.c-d", true},
		{"", false}, {".hidden", false}, {"-lead", false}, {"a/b", false}, {"..", false}} {
		if validRepoName(tc.name) != tc.ok {
			t.Errorf("validRepoName(%q)=%v, want %v", tc.name, !tc.ok, tc.ok)
		}
	}
	for _, tc := range []struct {
		ref string
		ok  bool
	}{{"main", true}, {"gh-pages", true}, {"release/1.2", true},
		{"", false}, {"/lead", false}, {"trail/", false}, {"a..b", false}} {
		if validGitRef(tc.ref) != tc.ok {
			t.Errorf("validGitRef(%q)=%v, want %v", tc.ref, !tc.ok, tc.ok)
		}
	}
	for _, tc := range []struct {
		dom string
		ok  bool
	}{{"example.com", true}, {"www.example.co.uk", true},
		{"nodot", false}, {"has space.com", false}, {"a.com\r\nx", false}, {"", false}} {
		if validCustomDomain(tc.dom) != tc.ok {
			t.Errorf("validCustomDomain(%q)=%v, want %v", tc.dom, !tc.ok, tc.ok)
		}
	}
}
