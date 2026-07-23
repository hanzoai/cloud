package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// metaMock stands in for Meta's oauth/access_token (two legs: code→short,
// fb_exchange_token→long) and /me endpoints. It captures the grant_type it saw and
// the Authorization header on /me so a test can prove the long-lived leg ran and the
// token rode the header (never the URL).
type metaMock struct {
	tokenErr bool // leg-1 (code→short) returns an error
	longErr  bool // leg-2 (fb_exchange_token→long) returns an error
	meAuth   string
	sawLong  bool
}

func newMetaMock(t *testing.T, m *metaMock) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			_ = r.ParseForm()
			if m.tokenErr {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"bad code","type":"OAuthException","code":100}}`))
				return
			}
			if r.Form.Get("grant_type") == "fb_exchange_token" {
				m.sawLong = true
				if m.longErr {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"cannot exchange","type":"OAuthException","code":100}}`))
					return
				}
				_, _ = w.Write([]byte(`{"access_token":"META-LONG-LIVED-SECRET","token_type":"bearer","expires_in":5184000}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"META-SHORT-SECRET","token_type":"bearer","expires_in":3600}`))
		case strings.HasSuffix(r.URL.Path, "/me"):
			m.meAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"id":"act_123","name":"Acme Ads"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldT, oldM := metaTokenURL, metaMeURL
	metaTokenURL, metaMeURL = srv.URL+"/token", srv.URL+"/me"
	t.Cleanup(func() { metaTokenURL, metaMeURL = oldT, oldM })
}

func TestMetaAuthorizeURL(t *testing.T) {
	raw, err := metaAuthorize(OAuthConfig{ClientID: "app-123"}, "https://api.hanzo.ai/v1/integrations/meta_ads/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("client_id") != "app-123" || q.Get("state") != "st8" || q.Get("response_type") != "code" {
		t.Fatalf("authorize params wrong: %s", raw)
	}
	scope := q.Get("scope")
	if !strings.Contains(scope, "ads_read") {
		t.Fatalf("scope must grant ads_read: %q", scope)
	}
	// Read-only means read-only: no WRITE-capable Meta scope may ever appear —
	// ads_management (spend), business_management (BM asset write), or any
	// *_management / rw_* variant. Meta comma-delimits the scope list.
	for _, s := range strings.Split(scope, ",") {
		if strings.Contains(s, "_management") || strings.HasPrefix(s, "rw_") {
			t.Fatalf("scope %q includes a write-capable grant %q; the connector is documented read-only", scope, s)
		}
	}
}

func TestMetaExchangeSealsLongLivedToken(t *testing.T) {
	m := &metaMock{}
	newMetaMock(t, m)
	res, err := metaExchange(context.Background(), OAuthConfig{ClientID: "app", ClientSecret: "secret"},
		"https://api.hanzo.ai/v1/integrations/meta_ads/callback", "authcode")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if !m.sawLong {
		t.Fatal("exchange must trade the short token for a long-lived one")
	}
	if res.Tokens[accessSecret] != "META-LONG-LIVED-SECRET" {
		t.Fatalf("must seal the long-lived token, got %q", res.Tokens[accessSecret])
	}
	if _, hasRefresh := res.Tokens[refreshSecret]; hasRefresh {
		t.Fatal("Meta issues no refresh token; none must be sealed")
	}
	if res.ExpiresAt == 0 {
		t.Fatal("long-lived token expiry must be recorded")
	}
	if res.ExternalID != "act_123" || res.AccountLabel != "Acme Ads" {
		t.Fatalf("account not resolved: id=%q label=%q", res.ExternalID, res.AccountLabel)
	}
	if m.meAuth != "Bearer META-LONG-LIVED-SECRET" {
		t.Fatalf("account fetch must carry the token in the header, got %q", m.meAuth)
	}
}

// TestMetaExchangeFallsBackToShortToken proves the long-lived leg is best-effort:
// when fb_exchange_token fails, the connection still lands on the SHORT token with
// its REAL expiry (now+3600), never a fabricated one and never the long-lived TTL,
// and still no refresh token.
func TestMetaExchangeFallsBackToShortToken(t *testing.T) {
	m := &metaMock{longErr: true}
	newMetaMock(t, m)
	before := time.Now().Unix()
	res, err := metaExchange(context.Background(), OAuthConfig{ClientID: "app", ClientSecret: "secret"},
		"https://api.hanzo.ai/v1/integrations/meta_ads/callback", "authcode")
	if err != nil {
		t.Fatalf("exchange must still succeed on the short token: %v", err)
	}
	if !m.sawLong {
		t.Fatal("the long-lived exchange must have been attempted")
	}
	if res.Tokens[accessSecret] != "META-SHORT-SECRET" {
		t.Fatalf("must fall back to the short token, got %q", res.Tokens[accessSecret])
	}
	if _, hasRefresh := res.Tokens[refreshSecret]; hasRefresh {
		t.Fatal("no refresh token must be sealed")
	}
	// Expiry is the SHORT token's real one (~now+3600), not the long-lived 5184000
	// and not a fabricated value.
	after := time.Now().Unix()
	if res.ExpiresAt < before+3600 || res.ExpiresAt > after+3600 {
		t.Fatalf("expiry must be the short token's real ~now+3600, got %d (window %d..%d)", res.ExpiresAt, before+3600, after+3600)
	}
}

func TestMetaExchangeRequiresSecret(t *testing.T) {
	_, err := metaExchange(context.Background(), OAuthConfig{ClientID: "app"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "META_APP_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

func TestMetaExchangeErrorBodyIsTokenFree(t *testing.T) {
	newMetaMock(t, &metaMock{tokenErr: true})
	_, err := metaExchange(context.Background(), OAuthConfig{ClientID: "app", ClientSecret: "top-secret-value"}, "redir", "code")
	if err == nil {
		t.Fatal("a token endpoint error must fail the exchange, not fake success")
	}
	if strings.Contains(err.Error(), "top-secret-value") {
		t.Fatalf("exchange error leaked the client secret: %v", err)
	}
}

// TestMetaE2EConnectSealsAndIsolates drives the full org plane: connect (org admin)
// → callback (signed state) → the long-lived token is sealed ONLY in the connecting
// org's KMS namespace, the row holds no secret, and a second org sees nothing.
func TestMetaE2EConnectSealsAndIsolates(t *testing.T) {
	newMetaMock(t, &metaMock{})
	t.Setenv(metaAppIDEnv, "app-id")
	t.Setenv(metaAppSecretEnv, "app-secret")
	kc := newKMS(t)
	app := newApp(t, kc)

	cb := oauthCallbackViaState(t, app, "meta_ads", "acme", "authcode")
	if cb.Code != http.StatusFound || !strings.Contains(cb.Location, "connected=meta_ads") {
		t.Fatalf("callback want 302 connected, got %d loc=%q body=%s", cb.Code, cb.Location, cb.Body)
	}
	got, ok := kmsSecret(t, kc, "acme", "meta_ads", accessSecret)
	if !ok || string(got) != "META-LONG-LIVED-SECRET" {
		t.Fatalf("long-lived token must be sealed for acme, got %q ok=%v", got, ok)
	}
	conn, ok := ConnectionFor("acme", "meta_ads")
	if !ok || conn.ExternalID != "act_123" {
		t.Fatalf("connection row wrong: %+v ok=%v", conn, ok)
	}
	if strings.Contains(conn.AccountLabel+conn.ExternalID, "META-LONG-LIVED-SECRET") {
		t.Fatal("token leaked into the connection row")
	}
	// Tenant isolation: org B has no secret and the token seam refuses it.
	if _, ok := kmsSecret(t, kc, "orgb", "meta_ads", accessSecret); ok {
		t.Fatal("orgb must not have a credential")
	}
	if _, err := TokenFor(context.Background(), "orgb", "meta_ads", accessSecret); err == nil {
		t.Fatal("TokenFor must refuse a non-connected org")
	}
	// The connecting org CAN read its token via the in-process seam.
	if v, err := TokenFor(context.Background(), "acme", "meta_ads", accessSecret); err != nil || string(v) != "META-LONG-LIVED-SECRET" {
		t.Fatalf("acme TokenFor want the sealed token, got %q err=%v", v, err)
	}
}

// TestMetaStateCannotBeReplayedAtGoogleAds proves the signed state is bound to the
// provider it was minted for: a state issued by meta_ads connect, replayed at the
// google_ads callback, is rejected as invalid — one connector's consent can never be
// redirected into another connector's custody.
func TestMetaStateCannotBeReplayedAtGoogleAds(t *testing.T) {
	t.Setenv(metaAppIDEnv, "app-id")
	t.Setenv(metaAppSecretEnv, "app-secret")
	app := newApp(t, newKMS(t))

	// Mint a genuine signed state via meta_ads connect (org admin).
	res := cfReq(t, app, http.MethodPost, "/v1/integrations/meta_ads/connect", "acme", true, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("meta connect want 200, got %d (%s)", res.Code, res.Body)
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("connect body: %v (%s)", err, res.Body)
	}
	u, _ := url.Parse(out.AuthorizeURL)
	metaState := u.Query().Get("state")
	if metaState == "" {
		t.Fatal("no state minted")
	}
	// Replay it at a DIFFERENT provider's callback.
	cb := req(t, app, http.MethodGet,
		"/v1/integrations/google_ads/callback?state="+url.QueryEscape(metaState)+"&code=x", "", nil)
	if cb.Code != http.StatusFound {
		t.Fatalf("callback want a 302 redirect, got %d (%s)", cb.Code, cb.Body)
	}
	if !strings.Contains(cb.Location, "error=google_ads") || !strings.Contains(cb.Location, "invalid") {
		t.Fatalf("cross-provider state replay must be rejected as invalid state, got loc=%q", cb.Location)
	}
}

// TestMetaConnectRequiresOrgAdmin proves a plain member cannot link the ad account.
func TestMetaConnectRequiresOrgAdmin(t *testing.T) {
	t.Setenv(metaAppIDEnv, "app-id")
	t.Setenv(metaAppSecretEnv, "app-secret")
	app := newApp(t, newKMS(t))
	res := cfReq(t, app, http.MethodPost, "/v1/integrations/meta_ads/connect", "acme", false, nil)
	if res.Code != http.StatusForbidden {
		t.Fatalf("member connect want 403, got %d (%s)", res.Code, res.Body)
	}
}

// TestMetaConnectHonest503WhenUnconfigured proves an unconfigured deployment answers
// an honest 503, never a dead consent URL.
func TestMetaConnectHonest503WhenUnconfigured(t *testing.T) {
	app := newApp(t, newKMS(t))
	res := cfReq(t, app, http.MethodPost, "/v1/integrations/meta_ads/connect", "acme", true, nil)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured connect want 503, got %d (%s)", res.Code, res.Body)
	}
}
