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
	"sync"
	"testing"
)

const testPassword = "hunter2-Sup3rSecret!"

// TestCredentialVerbsAreRefused proves hanzo.team establishes no session from a
// credential it handled itself. hanzo.id is the only way in.
//
// The load-bearing assertion is the TRIPWIRE, not the reply shape. Two earlier
// versions of this test were both forgeable:
//
//  1. Asserting "no token came back" passed with passwordLogin restored
//     verbatim, because mountTeam resolves iamEndpoint to PRODUCTION hanzo.id,
//     which refused the invented credentials with a real 401 carrying no token.
//     It could not tell "handler deleted" from "handler refused THESE
//     credentials", and it POSTed credentials to production on every run.
//  2. Asserting the refusal CODE fixed that for a revert, but a handler can
//     still forge the shape: return statusUnauthorized(signInAtIssuer) from
//     passwordLogin's error path and the test passes while the door is open and
//     talking to the issuer.
//
// So the test pins absence BEHAVIORALLY. Any handler that walks a credential
// must call IAM; the door never leaves the process. Pointing IAM_ENDPOINT at a
// recording server and failing on ANY request is the one thing a resurrected
// handler cannot forge — and it takes the credential POST off production.
func TestCredentialVerbsAreRefused(t *testing.T) {
	var mu sync.Mutex
	var tripped []string
	tripwire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tripped = append(tripped, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	}))
	defer tripwire.Close()
	t.Setenv("IAM_ENDPOINT", tripwire.URL)

	app := mountTeam(t)

	// Every verb the account client can send that would establish a session from
	// a credential. loginAsGuest and exchangeGuestToken are the same bypass class
	// one line from the door just closed; confirm is the sharp one — upstream it
	// is email confirmation returning a LoginInfo WITH a token.
	verbs := []string{
		"login", "loginAsGuest", "loginOtp", "signUp", "signUpOtp", "signUpJoin",
		"validateOtp", "join", "joinByToken", "exchangeGuestToken",
		"changePassword", "restorePassword", "requestPasswordReset",
		"confirm", "createAccessLink", "checkJoin", "checkAutoJoin",
		"refreshHanzoAssistantToken",
	}

	for _, verb := range verbs {
		mu.Lock()
		tripped = nil
		mu.Unlock()

		body := `{"method":"` + verb + `","params":{"email":"ada@acme.io","password":"` + testPassword + `"}}`
		req := httptest.NewRequest(http.MethodPost, "http://hanzo.team/v1/team/account", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		// THE assertion: the door answers without ever consulting the issuer.
		mu.Lock()
		hops := append([]string(nil), tripped...)
		mu.Unlock()
		if len(hops) > 0 {
			t.Fatalf("%s reached the issuer (%v) — a handler walked a credential; the door never leaves the process", verb, hops)
		}

		var out struct {
			Result *struct {
				Token   string `json:"token"`
				Account string `json:"account"`
			} `json:"result"`
			Error *Status `json:"error"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s decode %s: %v", verb, raw, err)
		}
		if out.Result != nil && out.Result.Token != "" {
			t.Fatalf("%s minted a session token — the door is open: %s", verb, raw)
		}
		for _, ck := range resp.Header.Values("Set-Cookie") {
			if strings.HasPrefix(ck, authCookie+"=") || strings.HasPrefix(ck, iamTokenCookie+"=") {
				t.Fatalf("%s set a session cookie: %s", verb, ck)
			}
		}
		if out.Error == nil {
			t.Fatalf("%s was not refused: %s", verb, raw)
		}
	}
}

// TestUnknownMethodDoesNotEchoUnbounded pins the one caller-controlled string
// that rides back in an error. The RPC body is capped only by the 16MB gateway
// limit, so an unbounded echo lets the caller size our response.
func TestUnknownMethodDoesNotEchoUnbounded(t *testing.T) {
	app := mountTeam(t)
	huge := strings.Repeat("A", 5000)
	req := httptest.NewRequest(http.MethodPost, "http://hanzo.team/v1/team/account",
		strings.NewReader(`{"method":"`+huge+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("unknown method: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(raw) > 512 {
		t.Fatalf("unknown-method reply is %d bytes — the method name is echoed unbounded", len(raw))
	}
}

// TestAuthStartProviderHint locks what cloud EMITS, which is all this package
// controls: /auth/google and /auth/github redirect into the SAME IAM authorize
// URL as /auth/openid — the canonical openid callback — differing only in the
// provider_hint param; an explicit query value passes through, plain openid
// carries none.
//
// It does NOT assert federation, because there is none: hanzo.id's
// /v1/iam/oauth/authorize strips provider_hint before its login app sees it, so
// /auth/google lands on the same SSO page as /auth/openid (see providerHint —
// the Location is byte-identical with and without the param). This test is the
// contract for the redirect_uri and the param we emit; the day IAM passes the
// param through, federation starts working with no change here.
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
		resp, err := app.Test(req)
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
