package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
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
// stderr is discarded; stdin is what the command reads.
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

// TestLoginTakesNoPassword pins the sign-in to the IdP's own page: the device
// grant by default, a pasted token otherwise. A password typed into this binary
// is a credential the CLI has to hold, echo-guard and forward; IAM's own page is
// the one place it belongs, and the device grant reaches that page from any
// terminal, headless included.
func TestLoginTakesNoPassword(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"login"}, {"auth", "login"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		for _, name := range []string{"username", "password-stdin"} {
			if cmd.Flags().Lookup(name) != nil {
				t.Errorf("`hanzo %s` takes --%s: a password must not reach the CLI", strings.Join(path, " "), name)
			}
		}
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

// authTokenFixture stores three identities — hanzo/a and admin/z live, hanzo/z
// (the active one) expired with a refresh token — beside an IAM that renews with
// whatever `renewal` holds, and returns the IAM's URL and how often it was asked.
func authTokenFixture(t *testing.T, renewal *string) (iam string, asked *[]string) {
	t.Helper()
	t.Setenv("HANZO_HOME", t.TempDir())
	t.Setenv("HANZO_TOKEN", "")
	asked = new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		*asked = append(*asked, r.Form.Get("refresh_token"))
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": *renewal, "refresh_token": "rt-next", "token_type": "Bearer", "expires_in": 3600})
	}))
	t.Cleanup(srv.Close)

	live, past := time.Now().Add(time.Hour).Unix(), time.Now().Add(-time.Hour).Unix()
	s := &IdentityStore{}
	s.Put(&Credentials{AccessToken: makeJWT(map[string]any{"jti": "a"}), Subject: "a@hanzo.ai", Owner: "hanzo", Expiry: live, RefreshToken: "rt-a"})
	s.Put(&Credentials{AccessToken: makeJWT(map[string]any{"jti": "admin"}), Subject: "z@hanzo.ai", Owner: "admin", Expiry: live})
	s.Put(&Credentials{AccessToken: makeJWT(map[string]any{"jti": "z-old"}), Subject: "z@hanzo.ai", Owner: "hanzo", Expiry: past, RefreshToken: "rt-z"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	return srv.URL, asked
}

func storedIdentities(t *testing.T) *IdentityStore {
	t.Helper()
	s, err := LoadIdentities()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuthTokenAsPrintsAnotherIdentityAndMovesNothing(t *testing.T) {
	renewal := makeJWT(map[string]any{"jti": "z-new"})
	iam, asked := authTokenFixture(t, &renewal)
	live := storedIdentities(t).Identities

	for _, tc := range []struct{ as, want string }{
		{"hanzo/a", live["hanzo/a"].AccessToken},
		{"admin", live["admin/z"].AccessToken}, // a bare owner held once
		{"admin/z", live["admin/z"].AccessToken},
	} {
		out, err := runRoot(t, "", "auth", "token", "--as", tc.as, "--iam-issuer", iam)
		if err != nil || strings.TrimSpace(out) != tc.want {
			t.Fatalf("--as %s: %q, %v", tc.as, out, err)
		}
	}
	if len(*asked) != 0 {
		t.Fatalf("live tokens were refreshed: %v", *asked)
	}
	if got := storedIdentities(t).Active; got != "hanzo/z" {
		t.Fatalf("--as moved the active identity to %s", got)
	}

	// A bare owner held twice is ambiguous, and one not held is not found: neither is answered for another.
	for as, want := range map[string]string{"hanzo": "ambiguous", "nobody": "no stored identity"} {
		if out, err := runRoot(t, "", "auth", "token", "--as", as, "--iam-issuer", iam); err == nil || out != "" || !strings.Contains(err.Error(), want) {
			t.Fatalf("--as %s: %q, %v", as, out, err)
		}
	}

	// HANZO_TOKEN speaks for the active identity only; naming one asks the store.
	t.Setenv("HANZO_TOKEN", "from-env")
	if out, _ := runRoot(t, "", "auth", "token", "--iam-issuer", iam); strings.TrimSpace(out) != "from-env" {
		t.Fatalf("env token not printed: %q", out)
	}
	if out, _ := runRoot(t, "", "auth", "token", "--as", "hanzo/a", "--iam-issuer", iam); strings.TrimSpace(out) != live["hanzo/a"].AccessToken {
		t.Fatalf("--as took the env token: %q", out)
	}
}

func TestAuthTokenRefreshesTheIdentityItPrintsAndOnlyThat(t *testing.T) {
	renewal := makeJWT(map[string]any{"jti": "z-new", "email": "z@hanzo.ai", "owner": "hanzo"})
	iam, asked := authTokenFixture(t, &renewal)
	before := storedIdentities(t).Identities

	// The active identity's token has lapsed: the CLI renews it, files it, prints it.
	out, err := runRoot(t, "", "auth", "token", "--iam-issuer", iam)
	if err != nil || strings.TrimSpace(out) != renewal {
		t.Fatalf("expired token not renewed: %q, %v", out, err)
	}
	if len(*asked) != 1 || (*asked)[0] != "rt-z" {
		t.Fatalf("refresh asked with %v", *asked)
	}
	after := storedIdentities(t)
	if after.Active != "hanzo/z" || after.Identities["hanzo/z"].AccessToken != renewal || after.Identities["hanzo/z"].RefreshToken != "rt-next" {
		t.Fatalf("renewal not filed under hanzo/z: %+v", after.Identities["hanzo/z"])
	}
	for _, k := range []string{"hanzo/a", "admin/z"} {
		if after.Identities[k].AccessToken != before[k].AccessToken {
			t.Fatalf("%s changed", k)
		}
	}
	if out, _ := runRoot(t, "", "auth", "token", "--as", "hanzo/z", "--iam-issuer", iam); strings.TrimSpace(out) != renewal || len(*asked) != 1 {
		t.Fatalf("a renewed token was asked for again: %q %v", out, *asked)
	}
}

func TestAuthTokenRefusesWhatItCannotRenew(t *testing.T) {
	// IAM answers with a token for someone else: it is not filed under hanzo/z.
	other := makeJWT(map[string]any{"email": "mallory@hanzo.ai", "owner": "hanzo"})
	iam, _ := authTokenFixture(t, &other)
	out, err := runRoot(t, "", "auth", "token", "--as", "hanzo/z", "--iam-issuer", iam)
	if err == nil || out != "" || !strings.Contains(err.Error(), "refusing to file it") {
		t.Fatalf("a foreign renewal was printed: %q, %v", out, err)
	}
	if got := storedIdentities(t).Identities["hanzo/z"].AccessToken; got == other {
		t.Fatal("a foreign renewal was filed")
	}

	// No refresh token and past its expiry: the session has ended, and an expired token is not printed.
	s := storedIdentities(t)
	s.Identities["hanzo/z"].RefreshToken = ""
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	out, err = runRoot(t, "", "auth", "token", "--iam-issuer", iam)
	if err == nil || out != "" || !strings.Contains(err.Error(), "session has ended") {
		t.Fatalf("an expired token was printed: %q, %v", out, err)
	}
}

func TestAuthTokenMachineLinkFallback(t *testing.T) {
	iam, _ := authTokenFixture(t, nil)
	// Expire active session without refresh token so interactive auth has ended.
	s := storedIdentities(t)
	s.Identities["hanzo/z"].RefreshToken = ""
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	// Install a 0700 <hanzoDir>/link/token helper (client_credentials machine identity).
	dir, err := hanzoDir()
	if err != nil {
		t.Fatal(err)
	}
	linkDir := dir + "/link"
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := linkDir + "/token"
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf 'machine-grid-jwt\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Default `hanzo auth token` falls back to the machine link helper when the user session has ended.
	out, err := runRoot(t, "", "auth", "token", "--iam-issuer", iam)
	if err != nil || strings.TrimSpace(out) != "machine-grid-jwt" {
		t.Fatalf("expected machine-grid-jwt fallback, got %q, err=%v", out, err)
	}

	// Explicit --as NEVER falls back to machine link identity.
	if out, err := runRoot(t, "", "auth", "token", "--as", "hanzo/z", "--iam-issuer", iam); err == nil || out != "" {
		t.Fatalf("expected explicit --as to refuse fallback, got %q, err=%v", out, err)
	}
}

