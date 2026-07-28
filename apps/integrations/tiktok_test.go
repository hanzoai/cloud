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

func TestTiktokAuthorizeURL(t *testing.T) {
	raw, err := tiktokAuthorize(OAuthConfig{ClientID: "tt-app"}, "https://api.hanzo.ai/v1/integrations/tiktok_ads/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	// TikTok uses app_id (not client_id) and carries no scope in the URL.
	if q.Get("app_id") != "tt-app" || q.Get("state") != "st8" {
		t.Fatalf("authorize params wrong: %s", raw)
	}
}

func TestTiktokExchangeSealsDurableToken(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "message": "OK",
			"data": map[string]any{"access_token": "TT-ACCESS-SECRET", "advertiser_ids": []string{"adv-1", "adv-2"}},
		})
	}))
	defer srv.Close()
	old := tiktokTokenURL
	tiktokTokenURL = srv.URL
	defer func() { tiktokTokenURL = old }()

	res, err := tiktokExchange(context.Background(), OAuthConfig{ClientID: "app", ClientSecret: "sec"}, "redir", "authcode-xyz")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	// The credential + auth_code rode the JSON body, never the URL.
	if gotBody["secret"] != "sec" || gotBody["auth_code"] != "authcode-xyz" || gotBody["grant_type"] != "authorization_code" {
		t.Fatalf("exchange body wrong: %v", gotBody)
	}
	if res.Tokens[accessSecret] != "TT-ACCESS-SECRET" {
		t.Fatalf("access token must seal, got %v", res.Tokens)
	}
	if _, hasRefresh := res.Tokens[refreshSecret]; hasRefresh {
		t.Fatal("TikTok issues no refresh token; none must be sealed")
	}
	if res.ExternalID != "adv-1" {
		t.Fatalf("first advertiser id must be the externalId, got %q", res.ExternalID)
	}
}

func TestTiktokExchangeNonZeroCodeFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 40002, "message": "auth_code expired"})
	}))
	defer srv.Close()
	old := tiktokTokenURL
	tiktokTokenURL = srv.URL
	defer func() { tiktokTokenURL = old }()
	_, err := tiktokExchange(context.Background(), OAuthConfig{ClientID: "app", ClientSecret: "sec"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "40002") {
		t.Fatalf("a non-zero envelope code must fail the exchange, got %v", err)
	}
}

func TestTiktokExchangeRequiresSecret(t *testing.T) {
	_, err := tiktokExchange(context.Background(), OAuthConfig{ClientID: "app"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "TIKTOK_ADS_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}
