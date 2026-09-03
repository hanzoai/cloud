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

	"github.com/hanzoai/iam/pkg/pkce"
)

// TestXPKCEAuthorizeURL proves the authorize URL carries an S256 code_challenge
// that is the HASH of the verifier — the verifier itself never appears — and the scope
// is least-privilege read (no write).
func TestXPKCEAuthorizeURL(t *testing.T) {
	creds := OAuthConfig{ClientID: "cid", ClientSecret: "csecret"}
	raw, err := xAuthorize(creds, "https://api.hanzo.ai/v1/integration/x/callback", "st8")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("must be S256, got %q", q.Get("code_challenge_method"))
	}
	verifier := xVerifier(creds)
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

// TestXVerifierDeterministic proves the verifier is a stable, valid RFC-7636
// verifier bound to the app credential, and that it differs when the secret differs.
func TestXVerifierDeterministic(t *testing.T) {
	c1 := OAuthConfig{ClientID: "cid", ClientSecret: "s1"}
	v1a, v1b := xVerifier(c1), xVerifier(c1)
	if v1a != v1b {
		t.Fatal("verifier must be deterministic for the same credential")
	}
	if len(v1a) != 43 { // base64url(sha256) unpadded = 43 chars, in-range for PKCE
		t.Fatalf("verifier length = %d, want 43", len(v1a))
	}
	if v1a == xVerifier(OAuthConfig{ClientID: "cid", ClientSecret: "s2"}) {
		t.Fatal("verifier must change when the client secret rotates")
	}
	if pkce.Challenge(v1a) == v1a {
		t.Fatal("challenge must be the HASH of the verifier, not the verifier")
	}
}

// newXMock stands in for X's token + /2/users/me endpoints. It records the
// Authorization header and posted form so a test can prove Basic client auth + the
// matching code_verifier.
type xMock struct {
	auth     string
	verifier string
}

func newXMock(t *testing.T, m *xMock) {
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
	oldT, oldMe := xTokenURL, xMeURL
	xTokenURL, xMeURL = srv.URL+"/2/oauth2/token", srv.URL+"/2/users/me"
	t.Cleanup(func() { xTokenURL, xMeURL = oldT, oldMe })
}

func TestXExchangeBasicAuthAndVerifier(t *testing.T) {
	m := &xMock{}
	newXMock(t, m)
	creds := OAuthConfig{ClientID: "cid", ClientSecret: "csecret"}
	res, err := xExchange(context.Background(), creds, "https://api.hanzo.ai/v1/integration/x/callback", "authcode")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	// Confidential client authenticated with HTTP Basic client_id:client_secret.
	wantAuth := basicAuth("cid", "csecret")
	if m.auth != wantAuth {
		t.Fatalf("exchange must authenticate the client with Basic, got %q", m.auth)
	}
	// The code_verifier sent MUST be the one the challenge in Authorize was derived from.
	if m.verifier != xVerifier(creds) {
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

func TestXExchangeRequiresSecret(t *testing.T) {
	_, err := xExchange(context.Background(), OAuthConfig{ClientID: "cid"}, "redir", "code")
	if err == nil || !strings.Contains(err.Error(), "X_CLIENT_SECRET") {
		t.Fatalf("want a specific not-configured error, got %v", err)
	}
}

func TestXExchangeTokenFree(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
	}))
	t.Cleanup(srv.Close)
	old := xTokenURL
	xTokenURL = srv.URL + "/2/oauth2/token"
	t.Cleanup(func() { xTokenURL = old })
	_, err := xExchange(context.Background(), OAuthConfig{ClientID: "cid", ClientSecret: "top-secret-value"}, "redir", "code")
	if err == nil || strings.Contains(err.Error(), "top-secret-value") {
		t.Fatalf("exchange error must be present and secret-free: %v", err)
	}
}

func sha256sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
