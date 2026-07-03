package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// stubSlackAPI stands in for Slack's Web API. /oauth.v2.access returns a good
// bot-token body for code "goodcode" and an error body otherwise; /auth.revoke
// returns ok. It repoints slackWebAPIBase for the test and restores it after.
func stubSlackAPI(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.FormValue("code") == "goodcode" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":           true,
				"access_token": "xoxb-real-token",
				"scope":        "chat:write,users:read",
				"bot_user_id":  "U0BOT",
				"team":         map[string]string{"id": "T0TEAM", "name": "Acme Inc"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_code"})
	})
	mux.HandleFunc("/auth.revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		// "badtoken" simulates a Slack-side rejection.
		if r.FormValue("token") == "badtoken" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "revoked": true})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })
}

func TestSlackAuthorizeURL(t *testing.T) {
	creds := OAuthConfig{ClientID: "cid-123", ClientSecret: "secret"}
	redirectURI := "https://api.hanzo.ai/v1/integrations/slack/callback"
	raw, err := slackAuthorize(creds, redirectURI, "STATE-XYZ")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if u.Host != "slack.com" || u.Path != "/oauth/v2/authorize" {
		t.Fatalf("unexpected authorize endpoint: %s", raw)
	}
	q := u.Query()
	if q.Get("client_id") != "cid-123" {
		t.Fatalf("client_id: %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != redirectURI {
		t.Fatalf("redirect_uri: %q", q.Get("redirect_uri"))
	}
	if q.Get("state") != "STATE-XYZ" {
		t.Fatalf("state: %q", q.Get("state"))
	}
	if q.Get("scope") == "" {
		t.Fatal("scope must be present")
	}
}

func TestSlackExchangeParsesOK(t *testing.T) {
	stubSlackAPI(t)
	res, err := slackExchange(context.Background(),
		OAuthConfig{ClientID: "cid", ClientSecret: "sec"},
		"https://api.hanzo.ai/v1/integrations/slack/callback", "goodcode")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if got := res.Tokens[slackBotTokenSecret]; got != "xoxb-real-token" {
		t.Fatalf("bot_token: %q", got)
	}
	if res.ExternalID != "T0TEAM" || res.AccountLabel != "Acme Inc" || res.BotUserID != "U0BOT" {
		t.Fatalf("metadata parse mismatch: %+v", res)
	}
	if len(res.Scopes) != 2 || res.Scopes[0] != "chat:write" {
		t.Fatalf("scopes parse mismatch: %+v", res.Scopes)
	}
}

func TestSlackExchangeErrorBody(t *testing.T) {
	stubSlackAPI(t)
	if _, err := slackExchange(context.Background(),
		OAuthConfig{ClientID: "cid", ClientSecret: "sec"},
		"https://api.hanzo.ai/v1/integrations/slack/callback", "wrongcode"); err == nil {
		t.Fatal("exchange with ok:false must return an error")
	}
}

func TestSlackRevoke(t *testing.T) {
	stubSlackAPI(t)
	if err := slackRevoke(context.Background(), OAuthConfig{}, "xoxb-real-token"); err != nil {
		t.Fatalf("revoke ok body must succeed: %v", err)
	}
	if err := slackRevoke(context.Background(), OAuthConfig{}, "badtoken"); err == nil {
		t.Fatal("revoke ok:false body must return an error (caller logs it)")
	}
	// Empty token is a no-op success (nothing to revoke).
	if err := slackRevoke(context.Background(), OAuthConfig{}, ""); err != nil {
		t.Fatalf("empty-token revoke must be a no-op: %v", err)
	}
}

// TestSlackProviderRegistered proves the reference provider self-registered with
// the shape the framework expects.
func TestSlackProviderRegistered(t *testing.T) {
	p, ok := registry["slack"]
	if !ok {
		t.Fatal("slack provider not registered")
	}
	if p.RedirectPath != "/v1/integrations/slack/callback" {
		t.Fatalf("slack RedirectPath: %q", p.RedirectPath)
	}
	if p.Category != "Communication" {
		t.Fatalf("slack Category: %q", p.Category)
	}
	if len(p.Secrets) != 1 || p.Secrets[0] != "bot_token" {
		t.Fatalf("slack Secrets: %+v", p.Secrets)
	}
}
