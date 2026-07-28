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

// TestGoogleRegisteredProvider proves the google provider is on the registry with
// the contract the connector + import bridge depend on: id "google", the two token
// secret names, and a callback path that matches the generic dispatcher.
func TestGoogleRegisteredProvider(t *testing.T) {
	p, ok := registry["google"]
	if !ok {
		t.Fatal("google provider not registered")
	}
	if p.RedirectPath != callbackPath("google") {
		t.Fatalf("RedirectPath %q must equal %q", p.RedirectPath, callbackPath("google"))
	}
	if len(p.Secrets) != 2 || p.Secrets[0] != "access_token" || p.Secrets[1] != "refresh_token" {
		t.Fatalf("google must custody access_token+refresh_token, got %v", p.Secrets)
	}
}

// TestGoogleAuthorizeURL proves the consent URL carries offline access (for a
// refresh token), the requested scopes, and the state.
func TestGoogleAuthorizeURL(t *testing.T) {
	creds := OAuthConfig{ClientID: "cid.apps.googleusercontent.com"}
	raw, err := googleAuthorize(creds, "https://api.hanzo.ai/v1/integrations/google/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "cid.apps.googleusercontent.com" || q.Get("state") != "st8" {
		t.Fatalf("client_id/state wrong: %s", raw)
	}
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Fatalf("offline consent params missing: %s", raw)
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type must be code: %s", raw)
	}
	scope := q.Get("scope")
	for _, want := range []string{"drive.readonly", "spreadsheets", "email"} {
		if !strings.Contains(scope, want) {
			t.Fatalf("scope %q missing %q", scope, want)
		}
	}
}

// TestGoogleExchange drives the real exchange against a mock token + userinfo
// endpoint and asserts both tokens are sealed for KMS custody, scopes are parsed,
// and the account label is resolved.
func TestGoogleExchange(t *testing.T) {
	var gotForm url.Values
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.Form
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "ya29.access",
			"refresh_token": "1//refresh",
			"expires_in":    3600,
			"scope":         "openid email https://www.googleapis.com/auth/drive.readonly https://www.googleapis.com/auth/spreadsheets",
			"token_type":    "Bearer",
		})
	}))
	defer tokenSrv.Close()
	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ya29.access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "1122", "email": "founder@acme.com"})
	}))
	defer userSrv.Close()

	oldT, oldU := googleTokenURL, googleUserinfoURL
	googleTokenURL, googleUserinfoURL = tokenSrv.URL, userSrv.URL
	defer func() { googleTokenURL, googleUserinfoURL = oldT, oldU }()

	creds := OAuthConfig{ClientID: "cid", ClientSecret: "secret"}
	res, err := googleExchange(context.Background(), creds, "https://api.hanzo.ai/v1/integrations/google/callback", "authcode")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Tokens["access_token"] != "ya29.access" || res.Tokens["refresh_token"] != "1//refresh" {
		t.Fatalf("both tokens must be sealed, got %v", res.Tokens)
	}
	if res.AccountLabel != "founder@acme.com" || res.ExternalID != "1122" {
		t.Fatalf("account not resolved: label=%q id=%q", res.AccountLabel, res.ExternalID)
	}
	if len(res.Scopes) != 4 {
		t.Fatalf("want 4 scopes, got %v", res.Scopes)
	}
	// The exchange must send grant_type=authorization_code with the code + secret.
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("code") != "authcode" || gotForm.Get("client_secret") != "secret" {
		t.Fatalf("exchange form wrong: %v", gotForm)
	}
}

// TestGoogleExchangeRequiresSecret proves the exchange fails honestly (no fake OK)
// when only the client id is configured.
func TestGoogleExchangeRequiresSecret(t *testing.T) {
	_, err := googleExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

// TestGoogleExchangeErrorBody proves an OAuth error body becomes an honest error,
// not a silent success.
func TestGoogleExchangeErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "bad code"})
	}))
	defer srv.Close()
	old := googleTokenURL
	googleTokenURL = srv.URL
	defer func() { googleTokenURL = old }()

	_, err := googleExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "s"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("want invalid_grant error, got %v", err)
	}
}
