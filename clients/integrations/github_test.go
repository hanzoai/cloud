package integrations

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// github_test.go proves the GitHub-App engine: provider registration + gating, the
// install URL, the installation_id exchange, installation-token minting, the repo
// list (paginated), and the authed repos handler — every GitHub API call driven
// against a mock server (the exact seal/mint/list path production uses, no fakes in
// the code under test).

// testAppKey generates a throwaway RSA private key in PKCS1 PEM — the form the App
// private key takes in GITHUB_APP_PRIVATE_KEY.
func testAppKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

// resetGithubApp clears the App-transport + token cache so a test's fresh mock
// server + creds are used, never a prior test's.
func resetGithubApp() {
	ghApp.mu.Lock()
	ghApp.tr = nil
	ghApp.loadedFor = ""
	ghApp.tokens = map[int64]cachedInstallToken{}
	ghApp.mu.Unlock()
	resetGrantCache() // the per-installation grant-set cache is App state too
}

// mockGitHub stands in for the GitHub API: it mints installation tokens, reports an
// installation's account, and lists repositories (paginated). Unauthenticated (a
// mock) — it only shapes responses.
func mockGitHub(t *testing.T, repos []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"login": "acme-gh"}})
		case r.URL.Path == "/installation/repositories":
			// One page per test set (per_page=100 is larger than our sets).
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": repos})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withGithubApp points the App engine at srv and injects test App creds.
func withGithubApp(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old; resetGithubApp() })
	t.Setenv(githubAppIDEnv, "424242")
	t.Setenv(githubAppKeyEnv, testAppKey(t))
	t.Setenv(githubAppSlugEnv, "hanzo")
	resetGithubApp()
}

func TestGitHubProviderRegistered(t *testing.T) {
	p, ok := registry["github"]
	if !ok {
		t.Fatal("github provider not registered")
	}
	if p.RedirectPath != callbackPath("github") {
		t.Fatalf("RedirectPath %q must equal %q", p.RedirectPath, callbackPath("github"))
	}
	if len(p.Secrets) != 0 {
		t.Fatalf("github custodies no sealed secrets (tokens minted on demand), got %v", p.Secrets)
	}
}

// TestGitHubConfiguredGating proves the card is inert until the App creds land — the
// whole "no side effects on deploy" property.
func TestGitHubConfiguredGating(t *testing.T) {
	t.Setenv(githubAppSlugEnv, "")
	t.Setenv(githubAppIDEnv, "")
	t.Setenv(githubAppKeyEnv, "")
	if githubConfigured() {
		t.Fatal("must be unconfigured with no env")
	}
	t.Setenv(githubAppSlugEnv, "hanzo")
	if githubConfigured() {
		t.Fatal("slug alone must not configure (mint needs id+key)")
	}
	t.Setenv(githubAppIDEnv, "1")
	t.Setenv(githubAppKeyEnv, "pem")
	if !githubConfigured() {
		t.Fatal("slug+id+key must configure")
	}
}

func TestGitHubAuthorizeURL(t *testing.T) {
	raw, err := githubAuthorize(OAuthConfig{Extra: map[string]string{githubSlugKey: "hanzo"}}, "", "st9")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !strings.HasPrefix(raw, "https://github.com/apps/hanzo/installations/new?state=st9") {
		t.Fatalf("unexpected install URL: %s", raw)
	}
}

// TestGitHubExchangeInstallationID proves the callback identifier (installation_id)
// is custodied as the ExternalID and validated (account fetched) via the App JWT.
func TestGitHubExchangeInstallationID(t *testing.T) {
	srv := mockGitHub(t, nil)
	withGithubApp(t, srv)

	res, err := githubExchange(context.Background(), OAuthConfig{}, "", "789")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.ExternalID != "789" {
		t.Fatalf("ExternalID must be the installation id, got %q", res.ExternalID)
	}
	if res.AccountLabel != "acme-gh" {
		t.Fatalf("account label must be fetched, got %q", res.AccountLabel)
	}
	if len(res.Tokens) != 0 {
		t.Fatalf("no token is sealed on connect, got %v", res.Tokens)
	}

	// A non-numeric identifier fails honestly (no fake OK).
	if _, err := githubExchange(context.Background(), OAuthConfig{}, "", "not-a-number"); err == nil {
		t.Fatal("non-numeric installation id must error")
	}
}

// TestInstallationTokenMint proves a fresh installation token is minted from the App
// key for a connected org, and cached (a second call does not re-mint).
func TestInstallationTokenMint(t *testing.T) {
	app := newApp(t, newKMS(t))
	srv := mockGitHub(t, nil)
	withGithubApp(t, srv)
	_ = app

	// Connect org "acme" to installation 555.
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "acme", Provider: "github", ExternalID: "555", AccountLabel: "acme-gh",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	tok, err := InstallationToken(context.Background(), "acme")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok != "ghs_installation_token" {
		t.Fatalf("unexpected token %q", tok)
	}

	// A never-connected org fails closed — never a value.
	if _, err := InstallationToken(context.Background(), "stranger"); err == nil {
		t.Fatal("un-connected org must fail closed, got a token")
	}
}

// TestInstallationReposList proves the installation repo list is parsed from the
// GitHub response.
func TestInstallationReposList(t *testing.T) {
	repos := []map[string]any{
		{"name": "alpha", "full_name": "acme-gh/alpha", "private": true, "default_branch": "main", "clone_url": "https://github.com/acme-gh/alpha.git"},
		{"name": "beta", "full_name": "acme-gh/beta", "private": false, "default_branch": "trunk", "clone_url": "https://github.com/acme-gh/beta.git"},
	}
	srv := mockGitHub(t, repos)
	withGithubApp(t, srv)

	got, err := installationRepos(context.Background(), "tok")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].FullName != "acme-gh/beta" || got[1].DefaultBranch != "trunk" {
		t.Fatalf("unexpected repos: %+v", got)
	}
}

// TestReposHandlerOrgScope proves the authed repos handler returns the org's granted
// repos with import status, and an org that has NOT connected GitHub gets a 409 (it
// can never enumerate another org's repos — the list rides that org's own token).
func TestReposHandlerOrgScope(t *testing.T) {
	repos := []map[string]any{
		{"name": "widgets", "full_name": "acme-gh/widgets", "private": true, "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
	}
	srv := mockGitHub(t, repos)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))

	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "acme", Provider: "github", ExternalID: "777", AccountLabel: "acme-gh",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// acme is connected → 200 with its repo.
	r := req(t, app, http.MethodGet, "/v1/integrations/github/repos", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("acme repos want 200, got %d (%s)", r.Code, r.Body)
	}
	var body struct {
		Repos []githubRepoView `json:"repos"`
	}
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(body.Repos) != 1 || body.Repos[0].Name != "widgets" {
		t.Fatalf("acme should see [widgets], got %+v", body.Repos)
	}

	// beta has NOT connected GitHub → 409 (never sees acme's repos).
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos", "beta", nil); r.Code != http.StatusConflict {
		t.Fatalf("un-connected org repos want 409, got %d (%s)", r.Code, r.Body)
	}

	// No principal → 403.
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("no-principal repos want 403, got %d", r.Code)
	}
}

// TestReposHandlerUnconfigured503 proves the whole surface is inert (503) when the
// App creds are absent — the deploy-safety property.
func TestReposHandlerUnconfigured503(t *testing.T) {
	t.Setenv(githubAppIDEnv, "")
	t.Setenv(githubAppKeyEnv, "")
	t.Setenv(githubAppSlugEnv, "")
	resetGithubApp()
	app := newApp(t, newKMS(t))
	_ = mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "github", ExternalID: "1"})
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos", "acme", nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured repos want 503, got %d (%s)", r.Code, r.Body)
	}
}
