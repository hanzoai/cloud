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

func TestLinkedinAuthorizeURL(t *testing.T) {
	raw, err := linkedinAuthorize(OAuthConfig{ClientID: "li-cid"}, "https://api.hanzo.ai/v1/integrations/linkedin_ads/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("client_id") != "li-cid" || q.Get("state") != "st8" || q.Get("response_type") != "code" {
		t.Fatalf("authorize params wrong: %s", raw)
	}
	if !strings.Contains(q.Get("scope"), "r_ads") || strings.Contains(q.Get("scope"), "rw_ads") {
		t.Fatalf("scope must be read-only (r_ads, not rw_ads): %q", q.Get("scope"))
	}
}

func TestLinkedinExchangeSealsAndResolvesAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/accessToken"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "LI-ACCESS-SECRET", "refresh_token": "LI-REFRESH-SECRET",
				"expires_in": 5184000, "scope": "r_ads r_ads_reporting",
			})
		case strings.HasSuffix(r.URL.Path, "/userinfo"):
			if r.Header.Get("Authorization") != "Bearer LI-ACCESS-SECRET" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "li-sub-1", "email": "cmo@acme.com", "name": "Acme CMO"})
		}
	}))
	defer srv.Close()
	oldT, oldM := linkedinTokenURL, linkedinMeURL
	linkedinTokenURL, linkedinMeURL = srv.URL+"/accessToken", srv.URL+"/userinfo"
	defer func() { linkedinTokenURL, linkedinMeURL = oldT, oldM }()

	res, err := linkedinExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "sec"}, "redir", "code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Tokens[accessSecret] != "LI-ACCESS-SECRET" || res.Tokens[refreshSecret] != "LI-REFRESH-SECRET" {
		t.Fatalf("both tokens must seal, got %v", res.Tokens)
	}
	if res.ExternalID != "li-sub-1" || res.AccountLabel != "cmo@acme.com" {
		t.Fatalf("account not resolved: id=%q label=%q", res.ExternalID, res.AccountLabel)
	}
}

// TestLinkedinExchangeWithoutRefresh proves an unapproved app (no refresh token)
// still connects with the access token alone — no fabricated refresh secret.
func TestLinkedinExchangeWithoutRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/accessToken") {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "LI-ACCESS-ONLY", "expires_in": 5184000, "scope": "r_ads"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "s", "email": "e@x.com"})
	}))
	defer srv.Close()
	oldT, oldM := linkedinTokenURL, linkedinMeURL
	linkedinTokenURL, linkedinMeURL = srv.URL+"/accessToken", srv.URL+"/userinfo"
	defer func() { linkedinTokenURL, linkedinMeURL = oldT, oldM }()

	res, err := linkedinExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "sec"}, "redir", "code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Tokens[accessSecret] != "LI-ACCESS-ONLY" {
		t.Fatalf("access token must seal, got %v", res.Tokens)
	}
	if _, has := res.Tokens[refreshSecret]; has {
		t.Fatal("no refresh token was returned; none must be sealed")
	}
}

func TestLinkedinExchangeRequiresSecret(t *testing.T) {
	_, err := linkedinExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "LINKEDIN_ADS_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}
