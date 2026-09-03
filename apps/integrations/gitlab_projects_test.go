package integrations

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// gitlabMock stands in for gitlab.com: the OAuth token endpoint, /api/v4/user for
// the account label, and /api/v4/projects paged. projects is served per page so a
// test can drive the paging walk; a page beyond the slice is empty, which is how
// GitLab ends a list.
type gitlabMock struct {
	pages    [][]map[string]any
	requests []url.Values
	status   int
}

func (m *gitlabMock) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "glpat.access", "refresh_token": "glpat.refresh",
				"scope": "openid read_api read_repository", "token_type": "Bearer",
			})
		case "/api/v4/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "username": "acme-dev"})
		case "/api/v4/projects":
			if r.Header.Get("Authorization") != "Bearer glpat.access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if m.status != 0 {
				w.WriteHeader(m.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "401 Unauthorized"})
				return
			}
			m.requests = append(m.requests, r.URL.Query())
			page := 1
			fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
			rows := []map[string]any{}
			if page >= 1 && page <= len(m.pages) {
				rows = m.pages[page-1]
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := gitlabBase
	gitlabBase = srv.URL
	t.Cleanup(func() { gitlabBase = old })
	return srv
}

// connectGitLab runs the real connect → callback handshake, so the connection row
// and the KMS-sealed token under test are the ones production writes.
func connectGitLab(t *testing.T, app *zip.App, org string) {
	t.Helper()
	res := req(t, app, http.MethodPost, "/v1/integration/gitlab/connect", org, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("connect body: %v (%s)", err, res.Body)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatalf("authorizeUrl parse: %v", err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("authorizeUrl carried no state: %s", out.AuthorizeURL)
	}
	cb := req(t, app, http.MethodGet,
		"/v1/integration/gitlab/callback?code=authcode&state="+url.QueryEscape(state), "", nil)
	if cb.Code/100 != 3 {
		t.Fatalf("callback want a 302 back to the console, got %d (%s)", cb.Code, cb.Body)
	}
}

func gitlabConfiguredEnv(t *testing.T) {
	t.Helper()
	t.Setenv(gitlabClientIDEnv, "test-client-id")
	t.Setenv(gitlabClientSecretEnv, "test-client-secret")
}

func project(name, ns, visibility string) map[string]any {
	return map[string]any{
		"name": name, "path": name, "path_with_namespace": ns, "visibility": visibility,
		"description": "a project", "last_activity_at": "2026-07-01T10:00:00Z",
		"default_branch":   "main",
		"http_url_to_repo": "https://gitlab.com/" + ns + ".git",
		"web_url":          "https://gitlab.com/" + ns,
	}
}

// TestGitLabProjectsNeedsConnection proves an org with no GitLab connection is
// told so — never an empty list, which reads as "you have no projects".
func TestGitLabProjectsNeedsConnection(t *testing.T) {
	gitlabConfiguredEnv(t)
	app := newApp(t, newKMS(t))
	if r := req(t, app, http.MethodGet, "/v1/integration/gitlab/projects", "acme", nil); r.Code != http.StatusNotFound {
		t.Fatalf("unconnected want 404, got %d (%s)", r.Code, r.Body)
	}
	// And no principal is 403 — the projects belong to an org, so an org is required.
	if r := req(t, app, http.MethodGet, "/v1/integration/gitlab/projects", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("no-principal want 403, got %d", r.Code)
	}
}

// TestGitLabProjectsListsTheConnection proves the sealed token is used to read
// GitLab, the rows are normalized for a client, and the token never appears in
// the answer.
func TestGitLabProjectsListsTheConnection(t *testing.T) {
	gitlabConfiguredEnv(t)
	m := &gitlabMock{pages: [][]map[string]any{{
		project("widgets", "acme/widgets", "private"),
		project("site", "acme/team/site", "public"),
	}}}
	m.serve(t)
	app := newApp(t, newKMS(t))
	connectGitLab(t, app, "acme")

	r := req(t, app, http.MethodGet, "/v1/integration/gitlab/projects", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("projects want 200, got %d (%s)", r.Code, r.Body)
	}
	if strings.Contains(string(r.Body), "glpat.") {
		t.Fatalf("the credential must never reach the answer: %s", r.Body)
	}
	var out gitlabProjectsOut
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("projects body: %v (%s)", err, r.Body)
	}
	if out.Account != "acme-dev" {
		t.Fatalf("account want acme-dev, got %q", out.Account)
	}
	if len(out.Projects) != 2 {
		t.Fatalf("want 2 projects, got %d (%s)", len(out.Projects), r.Body)
	}
	if p := out.Projects[0]; p.Name != "widgets" || p.FullName != "acme/widgets" || !p.Private ||
		p.CloneURL != "https://gitlab.com/acme/widgets.git" || p.DefaultBranch != "main" {
		t.Fatalf("first project not normalized: %+v", p)
	}
	// A subgroup keeps its full namespace — the path is what identifies a project.
	if p := out.Projects[1]; p.FullName != "acme/team/site" || p.Private {
		t.Fatalf("public subgroup project wrong: %+v", p)
	}
	// Least surprise for the caller: archived projects are excluded at the source,
	// so a list never offers a repository nobody can push to.
	if got := m.requests[0].Get("archived"); got != "false" {
		t.Fatalf("archived must be excluded at GitLab, got %q", got)
	}
	if got := m.requests[0].Get("membership"); got != "true" {
		t.Fatalf("membership must bound the list, got %q", got)
	}
}

// TestGitLabProjectsStopsOnAShortPage proves the walk costs ONE call for an
// account that fits on one page — the common case — and pages only when full.
func TestGitLabProjectsStopsOnAShortPage(t *testing.T) {
	gitlabConfiguredEnv(t)
	m := &gitlabMock{pages: [][]map[string]any{{project("one", "acme/one", "private")}}}
	m.serve(t)
	app := newApp(t, newKMS(t))
	connectGitLab(t, app, "acme")

	if r := req(t, app, http.MethodGet, "/v1/integration/gitlab/projects", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("projects want 200, got %d (%s)", r.Code, r.Body)
	}
	if len(m.requests) != 1 {
		t.Fatalf("a short page ends the walk: want 1 request, got %d", len(m.requests))
	}
}

// TestGitLabProjectsRevokedToken proves a token GitLab no longer honors is
// reported as such — a revoked connection and an outage are different answers.
func TestGitLabProjectsRevokedToken(t *testing.T) {
	gitlabConfiguredEnv(t)
	m := &gitlabMock{}
	m.serve(t)
	app := newApp(t, newKMS(t))
	connectGitLab(t, app, "acme")
	m.status = http.StatusUnauthorized

	r := req(t, app, http.MethodGet, "/v1/integration/gitlab/projects", "acme", nil)
	if r.Code != http.StatusUnauthorized {
		t.Fatalf("revoked want 401, got %d (%s)", r.Code, r.Body)
	}
	if !strings.Contains(string(r.Body), "connect it again") {
		t.Fatalf("the answer must say what to do: %s", r.Body)
	}
}

// TestGitLabProjectsIsOrgScoped proves one org can never read another's
// connection — the org comes from the principal, never from the request.
func TestGitLabProjectsIsOrgScoped(t *testing.T) {
	gitlabConfiguredEnv(t)
	m := &gitlabMock{pages: [][]map[string]any{{project("widgets", "acme/widgets", "private")}}}
	m.serve(t)
	app := newApp(t, newKMS(t))
	connectGitLab(t, app, "acme")

	if r := req(t, app, http.MethodGet, "/v1/integration/gitlab/projects", "beta", nil); r.Code != http.StatusNotFound {
		t.Fatalf("another org want 404, got %d (%s)", r.Code, r.Body)
	}
}
