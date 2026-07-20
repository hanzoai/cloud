package team

// Tests for the native email+password login (account RPC "login") and the
// provider_hint federation start. The IAM side is a mock: the password grant is
// proven end-to-end against the SAME two-step wire contract the platform e2e
// auth helper locks (POST /v1/iam/login responseType=code → POST
// /v1/iam/oauth/token), with the RS256 verify seam stubbed.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/team/token"
)

const (
	testPassword = "hunter2-Sup3rSecret!"
	testAccess   = "AT-rs256-access"
	testSub      = "113d4dd4-2486-40de-be2b-88d6e3e0b718"
)

// mockIAM serves the three IAM endpoints the password login walks: login (code
// mint), oauth/token (code exchange) and oauth/userinfo. It asserts the wire
// contract — OAuth params on the query string, credentials in the JSON body.
func mockIAM(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/iam/login", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("clientId") != "hanzo-team" || q.Get("responseType") != "code" || q.Get("type") != "code" {
			t.Errorf("iam login query = %q (want clientId=hanzo-team responseType=code type=code)", r.URL.RawQuery)
		}
		if q.Get("redirectUri") == "" || q.Get("state") == "" {
			t.Errorf("iam login query missing redirectUri/state: %q", r.URL.RawQuery)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["application"] != "hanzo-team" || body["type"] != "code" || body["signinMethod"] != "Password" {
			t.Errorf("iam login body contract broken: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if body["username"] == "ada@acme.io" && body["password"] == testPassword {
			_, _ = w.Write([]byte(`{"status":"ok","data":"code-42"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"error","msg":"password or code is incorrect"}`))
	})
	mux.HandleFunc("/v1/iam/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("code") != "code-42" || r.FormValue("client_secret") != "s3cr3t" {
			t.Errorf("token exchange form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + testAccess + `"}`))
	})
	mux.HandleFunc("/v1/iam/oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testAccess {
			t.Errorf("userinfo auth = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"` + testSub + `","email":"ada@acme.io","name":"Ada"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newLoginApp wires the account api directly (real account store, mock IAM,
// stubbed RS256 verify) onto a zip app, capturing every log line into logs.
func newLoginApp(t *testing.T, iamURL string, logs *bytes.Buffer) *zip.App {
	t.Helper()
	accounts, err := openAccountStore(filepath.Join(t.TempDir(), "account.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accounts.Close() })
	var w io.Writer = io.Discard
	if logs != nil {
		w = logs
	}
	g := &api{
		accounts: accounts,
		cfg: config{
			iamEndpoint: iamURL, iamClientID: "hanzo-team", iamClientSecret: "s3cr3t",
			serverSecret: testSecret, provider: "openid",
		},
		log: luxlog.NewWriter(w),
		verify: func(access string) (cloud.VerifiedIdentity, error) {
			if access != testAccess {
				t.Errorf("verify called with %q, want %q", access, testAccess)
			}
			return cloud.VerifiedIdentity{Owner: "acme", User: testSub}, nil
		},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	g.register(app.Group("/v1/team"), func(h zip.Handler) zip.Handler { return h })
	return app
}

// TestPasswordLoginMintsSession proves the full native email+password path:
// IAM code mint → confidential exchange → verified-owner session establishment
// → an HS256 session token that decodes to (account, org) under the server
// secret, with the IAM access token retained as an HttpOnly cookie.
func TestPasswordLoginMintsSession(t *testing.T) {
	var logs bytes.Buffer
	app := newLoginApp(t, mockIAM(t).URL, &logs)

	req := httptest.NewRequest(http.MethodPost, "http://hanzo.team/v1/team/account",
		strings.NewReader(`{"method":"login","params":{"email":"ada@acme.io","password":"`+testPassword+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Result LoginInfo `json:"result"`
		Error  *Status   `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Error != nil {
		t.Fatalf("login response = %s (err %v)", body, err)
	}
	if out.Result.Account != testSub {
		t.Fatalf("account = %q, want %q", out.Result.Account, testSub)
	}
	dec, err := token.Decode(out.Result.Token, testSecret, true)
	if err != nil || dec.Account != testSub || dec.Extra["org"] != "acme" {
		t.Fatalf("session token round-trip: %+v (err %v)", dec, err)
	}
	// The IAM access token rides the HttpOnly cookie for the agents proxy.
	var sawIAMCookie bool
	for _, ck := range resp.Cookies() {
		if ck.Name == iamTokenCookie && ck.Value == testAccess && ck.HttpOnly {
			sawIAMCookie = true
		}
	}
	if !sawIAMCookie {
		t.Fatalf("iam token cookie not set; cookies = %v", resp.Cookies())
	}
	if strings.Contains(logs.String(), testPassword) {
		t.Fatal("password leaked into logs on the success path")
	}
}

// TestPasswordLoginBadCreds proves wrong credentials answer a clean 401 whose
// error is the platform status the SPA translates — and that the submitted
// password appears NOWHERE in the logs or the response.
func TestPasswordLoginBadCreds(t *testing.T) {
	var logs bytes.Buffer
	app := newLoginApp(t, mockIAM(t).URL, &logs)

	req := httptest.NewRequest(http.MethodPost, "http://hanzo.team/v1/team/account",
		strings.NewReader(`{"method":"login","params":{"email":"ada@acme.io","password":"`+testPassword+`WRONG"}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad-creds status = %d, want 401: %s", resp.StatusCode, body)
	}
	var out struct {
		Error *Status `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Error == nil {
		t.Fatalf("bad-creds body = %s (err %v)", body, err)
	}
	if out.Error.Code != "platform:status:AccountNotFound" || out.Error.Severity != "ERROR" {
		t.Fatalf("bad-creds error = %+v", out.Error)
	}
	if strings.Contains(logs.String(), testPassword) {
		t.Fatalf("password leaked into logs: %s", logs.String())
	}
	if strings.Contains(string(body), testPassword) {
		t.Fatal("password echoed in the response body")
	}
	// Missing credentials are refused before any IAM hop.
	req = httptest.NewRequest(http.MethodPost, "http://hanzo.team/v1/team/account",
		strings.NewReader(`{"method":"login","params":{"email":"","password":""}}`))
	req.Header.Set("Content-Type", "application/json")
	resp2, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("empty-creds request: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty-creds status = %d, want 401", resp2.StatusCode)
	}
}

// TestAuthStartProviderHint locks the federation start: /auth/google and
// /auth/github redirect into the SAME IAM authorize URL as /auth/openid — the
// canonical openid callback — differing ONLY in the provider_hint that makes
// hanzo.id auto-federate; an explicit provider_hint query passes through, and
// plain openid carries none.
func TestAuthStartProviderHint(t *testing.T) {
	app := mountTeam(t)
	cases := []struct{ path, wantHint string }{
		{"/v1/team/account/auth/google", "provider-google"},
		{"/v1/team/account/auth/github", "provider-github"},
		{"/v1/team/account/auth/openid", ""},
		{"/v1/team/account/auth/openid?provider_hint=provider-google", "provider-google"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://hanzo.team"+tc.path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("%s status = %d, want 302", tc.path, resp.StatusCode)
		}
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("%s bad Location: %v", tc.path, err)
		}
		if got := loc.Query().Get("provider_hint"); got != tc.wantHint {
			t.Fatalf("%s provider_hint = %q, want %q", tc.path, got, tc.wantHint)
		}
		const wantCB = "https://hanzo.team/v1/team/account/auth/openid/callback"
		if got := loc.Query().Get("redirect_uri"); got != wantCB {
			t.Fatalf("%s redirect_uri = %q, want the canonical openid callback", tc.path, got)
		}
	}
}

// TestProvidersSurface locks the login-page button set: Google and GitHub (the
// hinted federation starts) plus the plain Hanzo SSO.
func TestProvidersSurface(t *testing.T) {
	app := mountTeam(t)
	code, body := call(t, app, http.MethodGet, "/v1/team/account/providers", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("providers status = %d", code)
	}
	var ps []ProviderInfo
	if err := json.Unmarshal(body, &ps); err != nil {
		t.Fatalf("providers decode: %v (%s)", err, body)
	}
	want := []ProviderInfo{
		{Name: "google", DisplayName: "Google"},
		{Name: "github", DisplayName: "GitHub"},
		{Name: "openid", DisplayName: "Hanzo"},
	}
	if len(ps) != len(want) {
		t.Fatalf("providers = %+v, want %+v", ps, want)
	}
	for i := range want {
		if ps[i] != want[i] {
			t.Fatalf("providers[%d] = %+v, want %+v", i, ps[i], want[i])
		}
	}
}
