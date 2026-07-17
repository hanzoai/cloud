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

// TestMultiIdentityLoginSwitch is the full multi-identity story: two logins for
// the same email under different owners (admin vs hanzo — the privilege-
// separation case) coexist, `auth list` shows both, `switch` flips the active
// pointer and rewrites credentials.json, and legacy single-file readers always
// see the active identity.
func TestMultiIdentityLoginSwitch(t *testing.T) {
	sandbox(t)
	adminTok := makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "admin", "sub": "u-admin", "exp": float64(2000000000)})
	hanzoTok := makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "hanzo", "sub": "u-hanzo", "exp": float64(2000000001)})

	// First login → admin/z is stored and active.
	out, err := runRoot(t, "", "login", "--token", adminTok)
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	if !strings.Contains(out, "admin/z") {
		t.Fatalf("login should report the key: %q", out)
	}
	if c, _ := LoadCredentials(); c.Owner != "admin" || c.Subject != "z@hanzo.ai" {
		t.Fatalf("active not admin after first login: %+v", c)
	}

	// Second login (different owner) → added beside admin/z, becomes active,
	// does NOT clobber the first.
	if _, err := runRoot(t, "", "login", "--token", hanzoTok); err != nil {
		t.Fatalf("login hanzo: %v", err)
	}
	store, err := LoadIdentities()
	if err != nil {
		t.Fatalf("load identities: %v", err)
	}
	if len(store.Identities) != 2 {
		t.Fatalf("want 2 identities, got %d: %v", len(store.Identities), store.keys())
	}
	if store.Identities["admin/z"] == nil || store.Identities["hanzo/z"] == nil {
		t.Fatalf("both identities must persist, got %v", store.keys())
	}
	if store.Active != "hanzo/z" {
		t.Fatalf("active = %q, want hanzo/z (last login)", store.Active)
	}
	// Legacy reader sees the active (hanzo) identity.
	if c, _ := LoadCredentials(); c.Owner != "hanzo" {
		t.Fatalf("credentials.json not mirroring active: %+v", c)
	}

	// auth list shows both, with the active row marked.
	out, err = runRoot(t, "", "auth", "list")
	if err != nil {
		t.Fatalf("auth list: %v", err)
	}
	for _, want := range []string{"admin/z", "hanzo/z", "z@hanzo.ai", "*"} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth list missing %q in:\n%s", want, out)
		}
	}

	// switch admin → active flips + credentials.json is rewritten to admin.
	if _, err := runRoot(t, "", "auth", "switch", "admin"); err != nil {
		t.Fatalf("auth switch admin: %v", err)
	}
	if st, _ := LoadIdentities(); st.Active != "admin/z" {
		t.Fatalf("active after switch = %q, want admin/z", st.Active)
	}
	if c, _ := LoadCredentials(); c.Owner != "admin" || c.Subject != "z@hanzo.ai" {
		t.Fatalf("switch did not rewrite credentials.json: %+v", c)
	}

	// whoami (top-level, reads the active token) reflects admin.
	out, err = runRoot(t, "", "whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if !strings.Contains(out, "admin") || !strings.Contains(out, "z@hanzo.ai") {
		t.Fatalf("whoami not reflecting the switched-to identity: %q", out)
	}

	// switch by the full owner/name key works too.
	if _, err := runRoot(t, "", "auth", "switch", "hanzo/z"); err != nil {
		t.Fatalf("auth switch hanzo/z: %v", err)
	}
	if c, _ := LoadCredentials(); c.Owner != "hanzo" {
		t.Fatalf("switch by full key failed: %+v", c)
	}
}

// TestAuthListJSON checks the machine-readable projection.
func TestAuthListJSON(t *testing.T) {
	sandbox(t)
	tok := makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "admin", "sub": "a"})
	if _, err := runRoot(t, "", "login", "--token", tok); err != nil {
		t.Fatalf("login: %v", err)
	}
	out, err := runRoot(t, "", "auth", "list", "-o", "json")
	if err != nil {
		t.Fatalf("auth list json: %v", err)
	}
	var rows []identityRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json unmarshal: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0].Key != "admin/z" || rows[0].Owner != "admin" || !rows[0].Active {
		t.Fatalf("json rows wrong: %+v", rows)
	}
}

// TestLogoutOneOfMany removes a single identity and, only when the last one is
// gone, clears the store entirely.
func TestLogoutOneOfMany(t *testing.T) {
	sandbox(t)
	admin := makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "admin", "sub": "a"})
	hanzo := makeJWT(map[string]any{"email": "z@hanzo.ai", "owner": "hanzo", "sub": "h"})
	if _, err := runRoot(t, "", "login", "--token", admin); err != nil {
		t.Fatal(err)
	}
	if _, err := runRoot(t, "", "login", "--token", hanzo); err != nil { // active = hanzo/z
		t.Fatal(err)
	}

	// logout of the named owner (admin) leaves hanzo/z active.
	if _, err := runRoot(t, "", "logout", "admin"); err != nil {
		t.Fatalf("logout admin: %v", err)
	}
	store, _ := LoadIdentities()
	if store.Identities["admin/z"] != nil {
		t.Fatalf("admin/z not removed: %v", store.keys())
	}
	if store.Active != "hanzo/z" {
		t.Fatalf("active = %q, want hanzo/z", store.Active)
	}
	if c, _ := LoadCredentials(); c.Owner != "hanzo" {
		t.Fatalf("credentials.json not mirroring survivor: %+v", c)
	}

	// logout of the active (no arg) removes the last identity → both files gone.
	if _, err := runRoot(t, "", "logout"); err != nil {
		t.Fatalf("logout active: %v", err)
	}
	if c, _ := LoadCredentials(); c.AccessToken != "" {
		t.Fatalf("credentials.json not cleared: %+v", c)
	}
	if st, _ := LoadIdentities(); len(st.Identities) != 0 {
		t.Fatalf("identity store not cleared: %v", st.keys())
	}
}

// TestMigrateLegacyCredentials proves a pre-multi-identity credentials.json is
// adopted into the store and preserved when a new identity is added.
func TestMigrateLegacyCredentials(t *testing.T) {
	sandbox(t)
	// Simulate an old single-file login: only credentials.json exists.
	legacy := credsFromToken(&tokenResp{AccessToken: makeJWT(map[string]any{
		"email": "z@hanzo.ai", "owner": "hanzo", "sub": "h",
	})})
	if err := legacy.Save(); err != nil {
		t.Fatal(err)
	}
	store, err := LoadIdentities()
	if err != nil {
		t.Fatalf("load identities: %v", err)
	}
	if store.Identities["hanzo/z"] == nil || store.Active != "hanzo/z" {
		t.Fatalf("legacy credentials not migrated: active=%q keys=%v", store.Active, store.keys())
	}
	// A fresh login as a different owner preserves the migrated identity.
	if _, err := runRoot(t, "", "login", "--token", makeJWT(map[string]any{
		"email": "z@hanzo.ai", "owner": "admin", "sub": "a",
	})); err != nil {
		t.Fatal(err)
	}
	st2, _ := LoadIdentities()
	if len(st2.Identities) != 2 || st2.Identities["hanzo/z"] == nil {
		t.Fatalf("migrated identity lost after new login: %v", st2.keys())
	}
}
