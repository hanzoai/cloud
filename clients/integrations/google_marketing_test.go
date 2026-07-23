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

// TestGoogleMarketingAuthorizeScopes proves each Google marketing provider requests
// its OWN product scope over the SHARED google.go plumbing — Ads gets adwords,
// Analytics gets analytics.readonly, and both keep offline-consent (refresh token).
func TestGoogleMarketingAuthorizeScopes(t *testing.T) {
	cases := []struct {
		provider string
		authFn   func(OAuthConfig, string, string) (string, error)
		want     string
		notWant  string
	}{
		{"google_ads", googleAuthorizeWith(googleAdsScopes), "auth/adwords", "analytics"},
		{"google_analytics", googleAuthorizeWith(googleAnalyticsScopes), "auth/analytics.readonly", "adwords"},
	}
	for _, tc := range cases {
		raw, err := tc.authFn(OAuthConfig{ClientID: "cid"}, "https://api.hanzo.ai/v1/integrations/"+tc.provider+"/callback", "st8")
		if err != nil {
			t.Fatalf("%s authorize: %v", tc.provider, err)
		}
		u, _ := url.Parse(raw)
		q := u.Query()
		scope := q.Get("scope")
		if !strings.Contains(scope, tc.want) {
			t.Fatalf("%s scope %q missing %q", tc.provider, scope, tc.want)
		}
		if strings.Contains(scope, tc.notWant) {
			t.Fatalf("%s scope %q must not carry %q (product isolation)", tc.provider, scope, tc.notWant)
		}
		if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
			t.Fatalf("%s must request offline consent for a refresh token: %s", tc.provider, raw)
		}
	}
}

// newGoogleMock points google.go's token + userinfo endpoints at a mock so the
// SHARED googleExchange can be exercised for a marketing provider.
func newGoogleMock(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "GADS-ACCESS-SECRET",
				"refresh_token": "GADS-REFRESH-SECRET",
				"expires_in":    3600,
				"scope":         "openid email https://www.googleapis.com/auth/adwords",
				"token_type":    "Bearer",
			})
		case strings.HasSuffix(r.URL.Path, "/userinfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-9", "email": "ops@acme.com"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldT, oldU := googleTokenURL, googleUserinfoURL
	googleTokenURL, googleUserinfoURL = srv.URL+"/token", srv.URL+"/userinfo"
	t.Cleanup(func() { googleTokenURL, googleUserinfoURL = oldT, oldU })
}

// TestGoogleAdsE2ESealsInOwnNamespace proves google_ads seals BOTH tokens into its
// OWN KMS namespace (distinct from the base google provider), via the reused
// googleExchange, and that google_analytics stays empty for the same org — the
// per-product custody isolation the separate provider ids buy.
func TestGoogleAdsE2ESealsInOwnNamespace(t *testing.T) {
	newGoogleMock(t)
	t.Setenv(googleClientIDEnv, "cid")
	t.Setenv(googleClientSecretEnv, "csecret")
	kc := newKMS(t)
	app := newApp(t, kc)

	cb := oauthCallbackViaState(t, app, "google_ads", "acme", "authcode")
	if cb.Code != http.StatusFound || !strings.Contains(cb.Location, "connected=google_ads") {
		t.Fatalf("callback want 302 connected, got %d loc=%q body=%s", cb.Code, cb.Location, cb.Body)
	}
	access, ok := kmsSecret(t, kc, "acme", "google_ads", accessSecret)
	if !ok || string(access) != "GADS-ACCESS-SECRET" {
		t.Fatalf("google_ads access token must be sealed, got %q ok=%v", access, ok)
	}
	if refresh, ok := kmsSecret(t, kc, "acme", "google_ads", refreshSecret); !ok || string(refresh) != "GADS-REFRESH-SECRET" {
		t.Fatalf("google_ads refresh token must be sealed, got %q ok=%v", refresh, ok)
	}
	// The sibling Google product got nothing — separate consent, separate custody.
	if _, ok := kmsSecret(t, kc, "acme", "google_analytics", accessSecret); ok {
		t.Fatal("google_analytics must be empty — per-product custody isolation broken")
	}
	if _, ok := kmsSecret(t, kc, "acme", "google", accessSecret); ok {
		t.Fatal("base google provider must be empty — per-product custody isolation broken")
	}
}

// TestGoogleAdsExchangeRequiresSecret proves the reused exchange still fails honestly
// when only the client id is configured.
func TestGoogleAdsExchangeRequiresSecret(t *testing.T) {
	_, err := googleExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}
