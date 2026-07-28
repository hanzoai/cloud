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

// fakeIDToken builds an UNSIGNED JWT-shaped id_token carrying claims — enough for
// jwtClaims (which decodes the payload for a label, never verifies the signature).
func fakeIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(b)
	return "eyJhbGciOiJub25lIn0." + payload + ".sig"
}

func TestMicrosoftAuthorizeURL(t *testing.T) {
	raw, err := microsoftAuthorize(OAuthConfig{ClientID: "ms-cid"}, "https://api.hanzo.ai/v1/integrations/microsoft_ads/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("client_id") != "ms-cid" || q.Get("state") != "st8" || q.Get("response_type") != "code" {
		t.Fatalf("authorize params wrong: %s", raw)
	}
	if !strings.Contains(q.Get("scope"), "msads.manage") || !strings.Contains(q.Get("scope"), "offline_access") {
		t.Fatalf("scope must carry msads.manage + offline_access: %q", q.Get("scope"))
	}
}

func TestMicrosoftExchangeSealsBothTokens(t *testing.T) {
	idTok := fakeIDToken(t, map[string]any{"email": "ceo@acme.com", "oid": "ms-oid-1"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_secret") != "sec" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "MS-ACCESS-SECRET", "refresh_token": "MS-REFRESH-SECRET",
			"expires_in": 3600, "id_token": idTok, "scope": "https://ads.microsoft.com/msads.manage",
		})
	}))
	defer srv.Close()
	old := microsoftTokenURL
	microsoftTokenURL = srv.URL
	defer func() { microsoftTokenURL = old }()

	res, err := microsoftExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "sec"}, "redir", "code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Tokens[accessSecret] != "MS-ACCESS-SECRET" || res.Tokens[refreshSecret] != "MS-REFRESH-SECRET" {
		t.Fatalf("both tokens must seal, got %v", res.Tokens)
	}
	if res.ExpiresAt == 0 {
		t.Fatal("expiry must be recorded")
	}
	if res.AccountLabel != "ceo@acme.com" || res.ExternalID != "ms-oid-1" {
		t.Fatalf("account from id_token wrong: label=%q id=%q", res.AccountLabel, res.ExternalID)
	}
}

func TestMicrosoftExchangeRequiresSecret(t *testing.T) {
	_, err := microsoftExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "MICROSOFT_ADS_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

func TestMicrosoftExchangeErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "bad"})
	}))
	defer srv.Close()
	old := microsoftTokenURL
	microsoftTokenURL = srv.URL
	defer func() { microsoftTokenURL = old }()
	_, err := microsoftExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "s"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("want an honest error surfacing invalid_grant, got %v", err)
	}
}
