package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// makeJWT builds an unsigned JWT (alg=none) carrying claims — enough to test
// the local, signature-free claim decode the CLI uses for display.
func makeJWT(claims map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	p, _ := json.Marshal(claims)
	return hdr + "." + base64.RawURLEncoding.EncodeToString(p) + ".sig"
}

func TestDecodeJWTClaims(t *testing.T) {
	tok := makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "hanzo", "sub": "abc", "exp": float64(1783110016)})
	claims, err := decodeJWTClaims(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if claimString(claims, "email") != "z@hanzo.ai" || claimString(claims, "owner") != "hanzo" {
		t.Fatalf("claims wrong: %+v", claims)
	}
	if _, err := decodeJWTClaims("not-a-jwt"); err == nil {
		t.Fatalf("expected error for non-JWT")
	}
}

func TestCredsFromToken(t *testing.T) {
	tok := makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "hanzo", "exp": float64(2000000000)})
	// expires_in present → wins over exp.
	c := credsFromToken(&tokenResp{AccessToken: tok, RefreshToken: "r", ExpiresIn: 3600})
	if c.Subject != "z@hanzo.ai" || c.Owner != "hanzo" || c.RefreshToken != "r" {
		t.Fatalf("identity not extracted: %+v", c)
	}
	if c.Expiry == 2000000000 {
		t.Fatalf("expires_in should win over exp claim")
	}
	// No expires_in → falls back to exp claim.
	c2 := credsFromToken(&tokenResp{AccessToken: tok})
	if c2.Expiry != 2000000000 {
		t.Fatalf("exp claim fallback failed: %d", c2.Expiry)
	}
}

func TestPasswordGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/oauth/access_token" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "password" || r.Form.Get("client_id") != "hanzo-console" ||
			r.Form.Get("username") != "z@hanzo.ai" || r.Form.Get("password") != "pw" {
			t.Errorf("bad form: %v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  makeJWT(map[string]any{"email": "z@hanzo.ai"}),
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "r",
		})
	}))
	defer srv.Close()

	c := newIAMClient(srv.URL, "hanzo-console")
	tr, err := c.passwordGrant(context.Background(), "z@hanzo.ai", "pw", "openid")
	if err != nil {
		t.Fatalf("passwordGrant: %v", err)
	}
	if tr.AccessToken == "" || tr.RefreshToken != "r" {
		t.Fatalf("token resp bad: %+v", tr)
	}
}

func TestPasswordGrantError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "bad password"})
	}))
	defer srv.Close()
	c := newIAMClient(srv.URL, "hanzo-console")
	_, err := c.passwordGrant(context.Background(), "u", "p", "openid")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected invalid_grant error, got %v", err)
	}
}

func TestRefreshGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/v1/iam/oauth/refresh_token" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt" {
			t.Errorf("bad refresh request: %s %v", r.URL.Path, r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": makeJWT(nil), "token_type": "Bearer"})
	}))
	defer srv.Close()
	c := newIAMClient(srv.URL, "hanzo-console")
	if _, err := c.refreshGrant(context.Background(), "rt"); err != nil {
		t.Fatalf("refreshGrant: %v", err)
	}
}

// runRoot executes the cobra root with args, returning stdout and any error.
// stderr is discarded; stdin is provided for password prompts.
func runRoot(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(new(bytes.Buffer))
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestLoginCommandPasswordStdin(t *testing.T) {
	sandbox(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "hanzo"}),
			"token_type":   "Bearer", "expires_in": 3600,
		})
	}))
	defer srv.Close()

	out, err := runRoot(t, "pw\n", "login", "-u", "z@hanzo.ai", "--password-stdin", "--iam-issuer", srv.URL)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(out, "Logged in as z@hanzo.ai @ hanzo") {
		t.Fatalf("login output: %q", out)
	}
	creds, _ := LoadCredentials()
	if creds.AccessToken == "" || creds.Subject != "z@hanzo.ai" {
		t.Fatalf("credentials not persisted: %+v", creds)
	}
}

func TestLoginTokenPasteAndPlatformToken(t *testing.T) {
	sandbox(t)
	tok := makeJWT(map[string]any{"email": "ops@hanzo.ai", "owner": "hanzo"})
	out, err := runRoot(t, "", "login", "--token", tok, "--platform-token", "svc-123")
	if err != nil {
		t.Fatalf("login --token: %v", err)
	}
	if !strings.Contains(out, "ops@hanzo.ai") {
		t.Fatalf("login output: %q", out)
	}
	creds, _ := LoadCredentials()
	if creds.AccessToken != tok || creds.PlatformToken != "svc-123" {
		t.Fatalf("creds not stored: %+v", creds)
	}
}

func TestWhoamiCommand(t *testing.T) {
	sandbox(t)
	creds := credsFromToken(&tokenResp{AccessToken: makeJWT(map[string]any{
		"email": "z@hanzo.ai", "name": "z", "owner": "hanzo", "sub": "u-1", "iss": "https://hanzo.id",
	})})
	if err := creds.Save(); err != nil {
		t.Fatal(err)
	}
	out, err := runRoot(t, "", "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	for _, want := range []string{"z@hanzo.ai", "hanzo", "u-1", "https://hanzo.id"} {
		if !strings.Contains(out, want) {
			t.Fatalf("whoami missing %q in %q", want, out)
		}
	}
}

func TestWhoamiLoggedOut(t *testing.T) {
	sandbox(t)
	if _, err := runRoot(t, "", "whoami"); err == nil {
		t.Fatalf("expected error when logged out")
	}
}

func TestLogoutCommand(t *testing.T) {
	sandbox(t)
	(&Credentials{AccessToken: "x"}).Save()
	if _, err := runRoot(t, "", "logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if c, _ := LoadCredentials(); c.AccessToken != "" {
		t.Fatalf("logout did not clear credentials")
	}
}

func TestAuthTokenCommand(t *testing.T) {
	sandbox(t)
	(&Credentials{AccessToken: "the-token"}).Save()
	out, err := runRoot(t, "", "auth", "token")
	if err != nil {
		t.Fatalf("auth token: %v", err)
	}
	if strings.TrimSpace(out) != "the-token" {
		t.Fatalf("auth token output: %q", out)
	}
}
