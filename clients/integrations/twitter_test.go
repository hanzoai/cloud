package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestTwitterPKCEAuthorizeURL proves the authorize URL carries an S256 code_challenge
// that is the HASH of the verifier — the verifier itself never appears — and the scope
// is least-privilege read (no write).
func TestTwitterPKCEAuthorizeURL(t *testing.T) {
	creds := OAuthConfig{ClientID: "cid", ClientSecret: "csecret"}
	raw, err := twitterAuthorize(creds, "https://api.hanzo.ai/v1/integrations/twitter/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("must be S256, got %q", q.Get("code_challenge_method"))
	}
	verifier := twitterVerifier(creds)
	challenge := q.Get("code_challenge")
	if challenge == "" {
		t.Fatal("no code_challenge")
	}
	// The challenge is the S256 of the verifier — and the verifier is NOT in the URL.
	want := base64.RawURLEncoding.EncodeToString(sha256sum(verifier))
	if challenge != want {
		t.Fatalf("code_challenge is not S256(verifier): got %q want %q", challenge, want)
	}
	if strings.Contains(raw, verifier) {
		t.Fatal("the code_verifier must NEVER appear in the authorize URL")
	}
	scope := q.Get("scope")
	for _, want := range []string{"tweet.read", "users.read", "offline.access"} {
		if !strings.Contains(scope, want) {
			t.Errorf("scope %q missing %q", scope, want)
		}
	}
	if strings.Contains(scope, "tweet.write") || strings.Contains(scope, "tweet.moderate") {
		t.Errorf("scope must be read-only (no write): %q", scope)
	}
}

// TestTwitterVerifierDeterministic proves the verifier is a stable, valid RFC-7636
// verifier bound to the app credential, and that it differs when the secret differs.
func TestTwitterVerifierDeterministic(t *testing.T) {
	c1 := OAuthConfig{ClientID: "cid", ClientSecret: "s1"}
	v1a, v1b := twitterVerifier(c1), twitterVerifier(c1)
	if v1a != v1b {
		t.Fatal("verifier must be deterministic for the same credential")
	}
	if len(v1a) != 43 { // base64url(sha256) unpadded = 43 chars, in-range for PKCE
		t.Fatalf("verifier length = %d, want 43", len(v1a))
	}
	if v1a == twitterVerifier(OAuthConfig{ClientID: "cid", ClientSecret: "s2"}) {
		t.Fatal("verifier must change when the client secret rotates")
	}
	if twitterChallenge(v1a) == v1a {
		t.Fatal("challenge must be the HASH of the verifier, not the verifier")
	}
}

// newTwitterMock stands in for X's token + /2/users/me endpoints. It records the
// Authorization header and posted form so a test can prove Basic client auth + the
// matching code_verifier.
type twitterMock struct {
	auth     string
	verifier string
}

func newTwitterMock(t *testing.T, m *twitterMock) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/token"):
			_ = r.ParseForm()
			m.auth = r.Header.Get("Authorization")
			m.verifier = r.Form.Get("code_verifier")
			_, _ = w.Write([]byte(`{"access_token":"X-ACCESS-SECRET","refresh_token":"X-REFRESH-SECRET","expires_in":7200,"scope":"tweet.read users.read offline.access","token_type":"bearer"}`))
		case strings.HasSuffix(r.URL.Path, "/users/me"):
			_, _ = w.Write([]byte(`{"data":{"id":"1500","username":"acmehq","name":"Acme"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldT, oldMe := twitterTokenURL, twitterMeURL
	twitterTokenURL, twitterMeURL = srv.URL+"/2/oauth2/token", srv.URL+"/2/users/me"
	t.Cleanup(func() { twitterTokenURL, twitterMeURL = oldT, oldMe })
}

func TestTwitterExchangeBasicAuthAndVerifier(t *testing.T) {
	m := &twitterMock{}
	newTwitterMock(t, m)
	creds := OAuthConfig{ClientID: "cid", ClientSecret: "csecret"}
	res, err := twitterExchange(context.Background(), creds, "https://api.hanzo.ai/v1/integrations/twitter/callback", "authcode")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	// Confidential client authenticated with HTTP Basic client_id:client_secret.
	wantAuth := basicAuth("cid", "csecret")
	if m.auth != wantAuth {
		t.Fatalf("exchange must authenticate the client with Basic, got %q", m.auth)
	}
	// The code_verifier sent MUST be the one the challenge in Authorize was derived from.
	if m.verifier != twitterVerifier(creds) {
		t.Fatalf("exchange code_verifier %q != derived verifier", m.verifier)
	}
	if res.Tokens[accessSecret] != "X-ACCESS-SECRET" || res.Tokens[refreshSecret] != "X-REFRESH-SECRET" {
		t.Fatalf("tokens not sealed: %+v", res.Tokens)
	}
	if res.ExternalID != "1500" || res.AccountLabel != "acmehq" {
		t.Fatalf("account wrong: id=%q label=%q", res.ExternalID, res.AccountLabel)
	}
	if res.ExpiresAt == 0 {
		t.Fatal("expiry must be recorded")
	}
}

func TestTwitterExchangeRequiresSecret(t *testing.T) {
	_, err := twitterExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "TWITTER_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

func TestTwitterExchangeTokenFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
	}))
	t.Cleanup(srv.Close)
	old := twitterTokenURL
	twitterTokenURL = srv.URL + "/2/oauth2/token"
	t.Cleanup(func() { twitterTokenURL = old })
	_, err := twitterExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "top-secret-value"}, "redir", "code")
	if err == nil || strings.Contains(err.Error(), "top-secret-value") {
		t.Fatalf("exchange error must be present and secret-free: %v", err)
	}
}

func sha256sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
