package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
)

// tokenEndpoint is a stand-in token endpoint for a refreshing provider.
func tokenEndpoint(t *testing.T) *stub {
	t.Helper()
	return newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		switch a.form().Get("grant_type") {
		case "authorization_code":
			say(w, `{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600,"scope":"identity submit"}`)
		case "refresh_token":
			if a.form().Get("refresh_token") != "refresh-1" {
				w.WriteHeader(http.StatusBadRequest)
				say(w, `{"error":"invalid_grant"}`)
				return
			}
			say(w, `{"access_token":"access-2","expires_in":3600}`)
		default:
			refuse(w, 400, "no")
		}
	})
}

func refreshing(srv *stub, basic bool) grant {
	return grant{
		id: "demo", name: "Demo", tokenURL: srv.URL, authURL: "https://example.com/auth",
		scopes: []string{"identity"}, clientIDEnv: "DEMO_ID", clientSecretEnv: "DEMO_SECRET",
		refreshes: true, basicAuth: basic, headers: map[string]string{"User-Agent": "hanzo-test"},
	}
}

func TestAProviderThatRefreshesKeepsTheRefreshTokenAndTheExpiry(t *testing.T) {
	srv := tokenEndpoint(t)
	p := oauthProvider(refreshing(srv, true))
	if len(p.Secrets) != 2 || p.Secrets[1] != refreshSecret || p.Refresh == nil {
		t.Fatalf("secrets=%v refresh=%v", p.Secrets, p.Refresh != nil)
	}
	before := time.Now()
	res, err := p.Exchange(context.Background(), OAuthConfig{ClientID: "id", ClientSecret: "sec"}, "https://api.hanzo.ai/cb", "code")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tokens[accessSecret] != "access-1" || res.Tokens[refreshSecret] != "refresh-1" {
		t.Fatalf("tokens = %v", res.Tokens)
	}
	if res.ExpiresAt < before.Add(59*time.Minute).Unix() || res.ExpiresAt > before.Add(61*time.Minute).Unix() {
		t.Fatalf("expires at %d", res.ExpiresAt)
	}
	got := srv.took("POST /")[0]
	if got.Header.Get("User-Agent") != "hanzo-test" || !strings.HasPrefix(got.Header.Get("Authorization"), "Basic ") {
		t.Fatalf("exchange headers = %v", got.Header)
	}
}

func TestARefreshTradesTheRefreshTokenAndKeepsItWhenNoneComesBack(t *testing.T) {
	srv := tokenEndpoint(t)
	t.Setenv("DEMO_ID", "id")
	t.Setenv("DEMO_SECRET", "sec")
	p := oauthProvider(refreshing(srv, false))
	res, err := p.Refresh(context.Background(), "refresh-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tokens[accessSecret] != "access-2" || res.Tokens[refreshSecret] != "refresh-1" || res.ExpiresAt == 0 {
		t.Fatalf("refresh = %+v", res)
	}
	f := srv.took("POST /")[0].form()
	if f.Get("client_id") != "id" || f.Get("client_secret") != "sec" || f.Get("grant_type") != "refresh_token" {
		t.Fatalf("form = %v", f)
	}
	if _, err := p.Refresh(context.Background(), "stale"); err == nil || strings.Contains(err.Error(), "stale") {
		t.Fatalf("a dead refresh token = %v", err)
	}
}

// An org that sealed its own app refreshes with it, not the deployment's.
func TestARefreshUsesTheOrgsOwnApp(t *testing.T) {
	srv := tokenEndpoint(t)
	t.Setenv("DEMO_ID", "deployment-id")
	t.Setenv("DEMO_SECRET", "deployment-secret")
	newApp(t, newKMS(t))
	own := refreshing(srv, false)
	own.id = "x"
	if err := kmsPut(mounted, "/orgs/acme/social/apps/x", "client_id", []byte("org-id")); err != nil {
		t.Fatal(err)
	}
	if err := kmsPut(mounted, "/orgs/acme/social/apps/x", "client_secret", []byte("org-secret")); err != nil {
		t.Fatal(err)
	}
	p := oauthProvider(own)
	if _, err := p.Refresh(cloud.For(context.Background(), "acme"), "refresh-1"); err != nil {
		t.Fatal(err)
	}
	if f := srv.took("POST /")[0].form(); f.Get("client_id") != "org-id" || f.Get("client_secret") != "org-secret" {
		t.Fatalf("form = %v", f)
	}
}

// A provider that does not declare refreshes stays as it was: one secret, no
// refresh, and nothing about expiry kept.
func TestAProviderThatDoesNotRefreshIsUnchanged(t *testing.T) {
	srv := tokenEndpoint(t)
	sp := refreshing(srv, false)
	sp.refreshes = false
	p := oauthProvider(sp)
	if len(p.Secrets) != 1 || p.Refresh != nil {
		t.Fatalf("secrets=%v refresh=%v", p.Secrets, p.Refresh != nil)
	}
	res, err := p.Exchange(context.Background(), OAuthConfig{ClientID: "id", ClientSecret: "sec"}, "https://api.hanzo.ai/cb", "code")
	if err != nil || res.ExpiresAt != 0 || res.Tokens[refreshSecret] != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestEverySocialNetworkThatNeedsARefreshDeclaresOne(t *testing.T) {
	for _, id := range []string{"youtube", "reddit", "pinterest", "tiktok", "threads"} {
		p := registry[id]
		if p == nil || p.Refresh == nil || !p.PerTarget || len(p.Secrets) != 2 {
			t.Errorf("%s: %+v", id, p)
		}
	}
	for _, id := range []string{"facebook", "instagram"} {
		// A Page token does not expire and has nothing to refresh.
		if p := registry[id]; p == nil || p.Refresh != nil {
			t.Errorf("%s keeps a refresh it cannot use", id)
		}
	}
}
