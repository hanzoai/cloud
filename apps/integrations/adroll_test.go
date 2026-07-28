package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func newAdrollMock(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/token"):
			_, _ = w.Write([]byte(`{"access_token":"ADROLL-ACCESS-SECRET","refresh_token":"ADROLL-REFRESH-SECRET","expires_in":3600,"token_type":"bearer"}`))
		case strings.HasSuffix(r.URL.Path, "/organization/get"):
			_, _ = w.Write([]byte(`{"results":{"eid":"ORG-EID-42","name":"Acme Retargeting"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldT, oldMe := adrollTokenURL, adrollMeURL
	adrollTokenURL, adrollMeURL = srv.URL+"/auth/token", srv.URL+"/api/v1/organization/get"
	t.Cleanup(func() { adrollTokenURL, adrollMeURL = oldT, oldMe })
}

func TestAdrollAuthorizeURL(t *testing.T) {
	raw, err := adrollAuthorize(OAuthConfig{ClientID: "cid"}, "https://api.hanzo.ai/v1/integrations/adroll/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "cid" || q.Get("state") != "st8" {
		t.Fatalf("authorize params wrong: %s", raw)
	}
}

func TestAdrollExchangeSeals(t *testing.T) {
	newAdrollMock(t)
	res, err := adrollExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "csecret"},
		"https://api.hanzo.ai/v1/integrations/adroll/callback", "code")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if res.Tokens[accessSecret] != "ADROLL-ACCESS-SECRET" || res.Tokens[refreshSecret] != "ADROLL-REFRESH-SECRET" {
		t.Fatalf("tokens not sealed: %+v", res.Tokens)
	}
	if res.ExternalID != "ORG-EID-42" || res.AccountLabel != "Acme Retargeting" {
		t.Fatalf("account wrong: id=%q label=%q", res.ExternalID, res.AccountLabel)
	}
	if res.ExpiresAt == 0 {
		t.Fatal("expiry must be recorded")
	}
}

func TestAdrollExchangeRequiresSecret(t *testing.T) {
	_, err := adrollExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "ADROLL_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

func TestAdrollExchangeTokenFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(srv.Close)
	old := adrollTokenURL
	adrollTokenURL = srv.URL + "/auth/token"
	t.Cleanup(func() { adrollTokenURL = old })
	_, err := adrollExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "top-secret-value"}, "redir", "code")
	if err == nil || strings.Contains(err.Error(), "top-secret-value") {
		t.Fatalf("exchange error must be present and secret-free: %v", err)
	}
}
