package team

// Tests for the way in to hanzo.team: hanzo.id and nothing else. They lock the
// two halves of that — the login page advertises exactly one door, and the
// password RPC that used to bypass it is refused.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testPassword = "hunter2-Sup3rSecret!"

// TestPasswordRPCIsRefused proves hanzo.team has NO password door.
//
// The account RPC used to accept {"method":"login", email, password} and walk
// the IAM password grant server-side. It authenticated correctly — and that was
// the problem: a session minted here never passes through hanzo.id, so it skips
// the identity check and the training-data consent that gate a first session.
// The credential-entry surface belongs on the issuer, not on every product host.
//
// A deployed SPA build can still render the form (the front image's
// HIDE_LOCAL_LOGIN gate is broken), so this must fail at the BACKEND: even a
// correct email and password get no session.
func TestPasswordRPCIsRefused(t *testing.T) {
	app := mountTeam(t)
	for _, body := range []string{
		`{"method":"login","params":{"email":"ada@acme.io","password":"` + testPassword + `"}}`,
		`{"method":"login","params":{"email":"","password":""}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "http://hanzo.team/v1/team/account", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("login rpc: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		// The refusal must carry NO session material, whatever its shape.
		var out struct {
			Result *struct {
				Token   string `json:"token"`
				Account string `json:"account"`
			} `json:"result"`
			Error *Status `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if out.Result != nil && out.Result.Token != "" {
			t.Fatalf("password login minted a session token — the door is open: %s", raw)
		}
		if out.Error == nil {
			t.Fatalf("password login was not refused: %s", raw)
		}
		// And no Set-Cookie may establish a session out of band.
		for _, ck := range resp.Header.Values("Set-Cookie") {
			if strings.HasPrefix(ck, authCookie+"=") || strings.HasPrefix(ck, iamTokenCookie+"=") {
				t.Fatalf("refusal set a session cookie: %s", ck)
			}
		}
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

// TestProvidersSurface locks the login-page button set to ONE door: Hanzo SSO.
//
// The page renders exactly what this returns (ProvidersOnlyForm -> Providers.svelte
// iterates it), so every extra entry here is a second, competing way in that also
// duplicates knowledge IAM already owns. Which identities hanzo.id accepts is
// answered on hanzo.id, where the identity check and the training-data consent
// gate the first session. A named provider re-appearing in this list is the
// regression this test exists to catch.
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
	want := []ProviderInfo{{Name: "openid", DisplayName: "Hanzo"}}
	if len(ps) != len(want) {
		t.Fatalf("providers = %+v, want exactly one door %+v", ps, want)
	}
	if ps[0] != want[0] {
		t.Fatalf("providers[0] = %+v, want %+v", ps[0], want[0])
	}
	// Named-IdP buttons are the specific thing that must not come back: they put
	// the provider choice on hanzo.team instead of on hanzo.id.
	for _, p := range ps {
		if p.Name == "google" || p.Name == "github" {
			t.Fatalf("provider %q is advertised again — the choice belongs on hanzo.id", p.Name)
		}
	}
}
