package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRedditAuthorizeURL(t *testing.T) {
	raw, err := redditAuthorize(OAuthConfig{ClientID: "rd-cid"}, "https://api.hanzo.ai/v1/integrations/reddit_ads/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("client_id") != "rd-cid" || q.Get("state") != "st8" {
		t.Fatalf("authorize params wrong: %s", raw)
	}
	// duration=permanent is what makes Reddit issue a refresh token.
	if q.Get("duration") != "permanent" {
		t.Fatalf("must request a permanent (refreshable) grant: %s", raw)
	}
	if !strings.Contains(q.Get("scope"), "adsread") || strings.Contains(q.Get("scope"), "adsedit") {
		t.Fatalf("scope must be read-only (adsread, not adsedit): %q", q.Get("scope"))
	}
}

func TestRedditExchangeUsesBasicAuthAndUserAgent(t *testing.T) {
	var tokenAuth, tokenUA, meAuth, meUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_token"):
			tokenAuth, tokenUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "RD-ACCESS-SECRET", "refresh_token": "RD-REFRESH-SECRET",
				"expires_in": 3600, "scope": "adsread identity", "token_type": "bearer",
			})
		case strings.HasSuffix(r.URL.Path, "/me"):
			meAuth, meUA = r.Header.Get("Authorization"), r.Header.Get("User-Agent")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "t2_abc", "name": "acme_ads"})
		}
	}))
	defer srv.Close()
	oldT, oldM := redditTokenURL, redditMeURL
	redditTokenURL, redditMeURL = srv.URL+"/access_token", srv.URL+"/me"
	defer func() { redditTokenURL, redditMeURL = oldT, oldM }()

	res, err := redditExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "sec"}, "redir", "code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("cid:sec"))
	if tokenAuth != wantAuth {
		t.Fatalf("token exchange must authenticate the client with Basic, got %q", tokenAuth)
	}
	if tokenUA != redditUserAgent || meUA != redditUserAgent {
		t.Fatalf("every Reddit call must carry the User-Agent, got token=%q me=%q", tokenUA, meUA)
	}
	if meAuth != "Bearer RD-ACCESS-SECRET" {
		t.Fatalf("account fetch must carry the bearer token, got %q", meAuth)
	}
	if res.Tokens[accessSecret] != "RD-ACCESS-SECRET" || res.Tokens[refreshSecret] != "RD-REFRESH-SECRET" {
		t.Fatalf("both tokens must seal, got %v", res.Tokens)
	}
	if res.ExternalID != "t2_abc" || res.AccountLabel != "acme_ads" {
		t.Fatalf("account not resolved: id=%q label=%q", res.ExternalID, res.AccountLabel)
	}
}

func TestRedditExchangeRequiresSecret(t *testing.T) {
	_, err := redditExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "REDDIT_ADS_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}
