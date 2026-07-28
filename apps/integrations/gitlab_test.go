package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGitLabRegisteredProvider proves the gitlab provider is on the registry with
// the contract the git-sync bridge depends on: id "gitlab", the two token secret
// names, and a callback path that matches the generic dispatcher (the one the
// GitLab app's Callback URL points at).
func TestGitLabRegisteredProvider(t *testing.T) {
	p, ok := registry["gitlab"]
	if !ok {
		t.Fatal("gitlab provider not registered")
	}
	if p.RedirectPath != callbackPath("gitlab") {
		t.Fatalf("RedirectPath %q must equal %q", p.RedirectPath, callbackPath("gitlab"))
	}
	if len(p.Secrets) != 2 || p.Secrets[0] != "access_token" || p.Secrets[1] != "refresh_token" {
		t.Fatalf("gitlab must custody access_token+refresh_token, got %v", p.Secrets)
	}
}

// TestGitLabAuthorizeURL proves the consent URL carries the Authorization Code
// params + state, requests the LEAST-PRIVILEGE scopes, and NEVER requests the
// dangerous ones (api/sudo/admin_mode/*_runner/k8s_proxy/*_registry) even if the
// app was provisioned with them.
func TestGitLabAuthorizeURL(t *testing.T) {
	creds := OAuthConfig{ClientID: "5a68c0e6"}
	raw, err := gitlabAuthorize(creds, "https://api.hanzo.ai/v1/integrations/gitlab/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "5a68c0e6" || q.Get("state") != "st8" {
		t.Fatalf("client_id/state wrong: %s", raw)
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type must be code: %s", raw)
	}
	scope := q.Get("scope")
	for _, want := range []string{"openid", "profile", "email", "read_api", "read_repository", "write_repository"} {
		if !containsField(scope, want) {
			t.Fatalf("scope %q missing least-privilege %q", scope, want)
		}
	}
	for _, banned := range []string{"api", "sudo", "admin_mode", "k8s_proxy", "create_runner", "manage_runner", "write_registry", "read_registry", "ai_features"} {
		if containsField(scope, banned) {
			t.Fatalf("scope %q must NOT request dangerous %q", scope, banned)
		}
	}
}

// containsField reports whether space-separated field list s contains exactly x
// (so "read_api" does not match a request for "api").
func containsField(s, x string) bool {
	for _, f := range strings.Fields(s) {
		if f == x {
			return true
		}
	}
	return false
}

// TestGitLabExchange drives the real exchange against a mock token + /api/v4/user
// endpoint and asserts both tokens are sealed for KMS custody, the account is
// resolved, and the form is the authorization_code grant with the secret.
func TestGitLabExchange(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = r.ParseForm()
			gotForm = r.Form
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "glpat.access",
				"refresh_token": "glpat.refresh",
				"expires_in":    7200,
				"scope":         "openid profile email read_api read_repository write_repository",
				"token_type":    "Bearer",
			})
		case "/api/v4/user":
			if r.Header.Get("Authorization") != "Bearer glpat.access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "username": "hanzo"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	old := gitlabBase
	gitlabBase = srv.URL
	defer func() { gitlabBase = old }()

	creds := OAuthConfig{ClientID: "cid", ClientSecret: "secret"}
	res, err := gitlabExchange(context.Background(), creds, "https://api.hanzo.ai/v1/integrations/gitlab/callback", "authcode")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Tokens["access_token"] != "glpat.access" || res.Tokens["refresh_token"] != "glpat.refresh" {
		t.Fatalf("both tokens must be sealed, got %v", res.Tokens)
	}
	if res.AccountLabel != "hanzo" || res.ExternalID != "42" {
		t.Fatalf("account not resolved: label=%q id=%q", res.AccountLabel, res.ExternalID)
	}
	if len(res.Scopes) != 6 {
		t.Fatalf("want 6 scopes, got %v", res.Scopes)
	}
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("code") != "authcode" || gotForm.Get("client_secret") != "secret" {
		t.Fatalf("exchange form wrong: %v", gotForm)
	}
}

// TestGitLabExchangeRequiresSecret proves the exchange fails honestly (no fake OK)
// when only the client id is configured — the KMS secret hasn't landed yet.
func TestGitLabExchangeRequiresSecret(t *testing.T) {
	_, err := gitlabExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "GITLAB_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

// TestGitLabExchangeErrorBody proves an OAuth error body becomes an honest error.
func TestGitLabExchangeErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "bad code"})
	}))
	defer srv.Close()
	old := gitlabBase
	gitlabBase = srv.URL
	defer func() { gitlabBase = old }()

	_, err := gitlabExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "s"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("want invalid_grant error, got %v", err)
	}
}
