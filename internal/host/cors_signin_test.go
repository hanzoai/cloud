package host

// The sign-in routes answer a credentialed cross-origin request only from an
// identity origin (cors_origin.go, sessionRoute; middleware_edge.go, EdgeCORS).
// Every test here drives real requests through the zip/fiber stack.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// liveAllowlist is the shape of the production CLOUD_CORS_ORIGINS: brand-zone
// wildcards, so the app origins below are ADMITTED and the only thing standing
// between them and a credentialed sign-in is the route rule.
var liveAllowlist = []string{
	"*.hanzo.ai", "*.hanzo.app", "*.hanzo.id", "*.lux.id", "*.lux.network",
	"*.lux.cloud", "zoolabs.id", "*.zoo.ngo", "*.pars.id", "*.osage.id",
}

// liveIssuerMap is the production IAM_ISSUER_MAP (hanzoai/universe
// charts/app/values/hanzo/cloud.yaml), so the identity set under test is the one
// Config derives in production.
const liveIssuerMap = `{"auth.hanzo.ai":"https://hanzo.id","hanzo.id":"https://hanzo.id","iam.hanzo.ai":"https://hanzo.id","id.adnexus.dev":"https://id.adnexus.dev","id.bootno.de":"https://id.bootno.de","id.lux.network":"https://lux.id","id.pars.network":"https://pars.id","id.zoo.network":"https://zoolabs.id","lux.id":"https://lux.id","osage.id":"https://osage.id","pars.id":"https://pars.id","www.osage.id":"https://osage.id","www.zoolabs.id":"https://zoolabs.id","zoolabs.id":"https://zoolabs.id"}`

// appOrigins are pages the allowlist admits that are NOT identity origins.
// safe.lux.network is same-site with id.lux.network and console.hanzo.ai with
// iam.hanzo.ai, so the session cookie rides on their credentialed requests.
var appOrigins = []string{
	"https://safe.lux.network",
	"https://console.hanzo.ai",
	"https://www.hanzo.ai",
	"https://customer.hanzo.app",
	"https://console.lux.cloud",
	"https://zoo.ngo",
}

// identityOrigins are the pages a person signs in on. hanzo.ai is one: its /login
// is Hanzo's sign-in page.
var identityOrigins = []string{
	"https://hanzo.ai",
	"https://hanzo.id",
	"https://lux.id",
	"https://zoolabs.id",
	"https://pars.id",
	"https://osage.id",
	"https://iam.hanzo.ai",
	"https://id.hanzo.ai",
	"https://id.lux.network",
}

// signInRoutes is one address in every sessionRoutes entry, each the way the
// shipped SDKs and the identity SPA call it.
var signInRoutes = []struct{ method, path string }{
	{http.MethodPost, "/v1/iam/login"},
	{http.MethodGet, "/v1/iam/web3/nonce"},
	{http.MethodPost, "/v1/iam/web3/verify"},
	{http.MethodPost, "/v1/iam/signin"},
	{http.MethodPost, "/v1/iam/onboard"},
	{http.MethodGet, "/v1/iam/oauth/callback"},
	{http.MethodPost, "/v1/iam/oauth/federation/mfa"},
	{http.MethodGet, "/v1/iam/webauthn/signin/begin"},
	{http.MethodPost, "/v1/iam/webauthn/signin/finish"},
	{http.MethodGet, "/v1/iam/webauthn/signup/begin"},
	{http.MethodPost, "/v1/iam/webauthn/signup/finish"},
	{http.MethodPost, "/v1/iam/signup"},
	{http.MethodGet, "/v1/iam/accounts"},
	{http.MethodGet, "/v1/iam/oauth/authorize"},
	{http.MethodPost, "/v1/iam/oauth/authorize"},
	{http.MethodPost, "/v1/iam/oauth/device"},
	{http.MethodPost, "/v1/iam/oauth/device/info"},
}

// cookieRoutes' addresses, each with the method hanzoai/iam registers it under.
var cookieRouteCalls = []struct{ method, path string }{
	{http.MethodGet, "/v1/iam/account"},
	{http.MethodPut, "/v1/iam/account"},
	{http.MethodGet, "/v1/iam/whoami"},
	{http.MethodGet, "/v1/iam/consent"},
	{http.MethodPut, "/v1/iam/consent"},
	{http.MethodPost, "/v1/iam/preferences"},
	{http.MethodPut, "/v1/iam/password"},
	{http.MethodPost, "/v1/iam/link"},
	{http.MethodPost, "/v1/iam/unlink"},
	{http.MethodGet, "/v1/iam/linked-accounts"},
}

// pkceRoutes are what an app's PKCE client calls from its own origin: discovery,
// keys, the code-for-token exchange, userinfo, revoke and sign-out. None reads
// the session cookie; @hanzo/iam sends revoke with credentials: "include".
// Unchanged.
var pkceRoutes = []struct{ method, path string }{
	{http.MethodGet, "/.well-known/openid-configuration"},
	{http.MethodGet, "/v1/iam/.well-known/openid-configuration"},
	{http.MethodGet, "/.well-known/jwks"},
	{http.MethodGet, "/v1/iam/.well-known/jwks"},
	{http.MethodPost, "/v1/iam/oauth/token"},
	{http.MethodGet, "/v1/iam/oauth/userinfo"},
	{http.MethodPost, "/v1/iam/oauth/revoke"},
	{http.MethodPost, "/v1/iam/oauth/logout"},
	{http.MethodGet, "/v1/iam/organizations"},
}

// signInApp mounts the edge over the live allowlist and identity set, with every
// IAM address answered by one handler.
func signInApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: liveAllowlist}), identityHosts(t, liveIssuerMap)))
	h := func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) }
	app.Raw(zip.MethodAll, "/v1/iam/*", h)
	app.Raw(zip.MethodAll, "/.well-known/*", h)
	return app
}

// ask sends the actual request, or with preflight the browser's preflight for it.
func ask(t *testing.T, app *zip.App, method, path, origin string, preflight bool) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if preflight {
		req = httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Access-Control-Request-Method", method)
		req.Header.Set("Access-Control-Request-Headers", "content-type")
	}
	req.Header.Set("Origin", origin)
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

// credentialed reports whether a browser would hand origin the response to a
// request sent with credentials: "include".
func credentialed(res *http.Response, origin string) bool {
	return res.Header.Get("Access-Control-Allow-Origin") == origin &&
		res.Header.Get("Access-Control-Allow-Credentials") == "true"
}

// An app the allowlist admits gets NO CORS answer on a sign-in route — not the
// credential, and not a bare read either, so it cannot take a password on its
// own page and read the code back. An identity origin keeps both. The preflight
// and the request agree, because they are the same boolean.
func TestSignInRoutesAnswerOnlyIdentityOrigins(t *testing.T) {
	app := signInApp(t)
	for _, r := range signInRoutes {
		for _, preflight := range []bool{true, false} {
			for _, o := range appOrigins {
				res := ask(t, app, r.method, r.path, o, preflight)
				if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
					t.Errorf("%s %s from app %s (preflight=%v): ACAO %q, want none", r.method, r.path, o, preflight, got)
				}
				if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
					t.Errorf("%s %s from app %s (preflight=%v): ACAC %q, want none", r.method, r.path, o, preflight, got)
				}
				if preflight && res.Header.Get("Access-Control-Allow-Methods") != "" {
					t.Errorf("%s %s from app %s: preflight named methods for a route it refuses", r.method, r.path, o)
				}
				if !strings.Contains(res.Header.Get("Vary"), "Origin") {
					t.Errorf("%s %s from app %s: Vary %q lacks Origin — a cache could serve it an identity origin's answer", r.method, r.path, o, res.Header.Get("Vary"))
				}
			}
			for _, o := range identityOrigins {
				if res := ask(t, app, r.method, r.path, o, preflight); !credentialed(res, o) {
					t.Errorf("%s %s from identity origin %s (preflight=%v): ACAO %q ACAC %q, want the credentialed answer",
						r.method, r.path, o, preflight,
						res.Header.Get("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
				}
			}
		}
	}
}

// PKCE is untouched: an app still reaches discovery, keys, the token exchange,
// userinfo and sign-out from its own origin with the credentialed answer it had.
func TestPKCERoutesKeepTheirAnswer(t *testing.T) {
	app := signInApp(t)
	for _, r := range pkceRoutes {
		for _, preflight := range []bool{true, false} {
			for _, o := range appOrigins {
				if res := ask(t, app, r.method, r.path, o, preflight); !credentialed(res, o) {
					t.Errorf("%s %s from app %s (preflight=%v): ACAO %q ACAC %q — a PKCE client lost its answer",
						r.method, r.path, o, preflight,
						res.Header.Get("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
				}
			}
		}
	}
}

// A route that reads the session cookie ahead of a bearer answers an app without
// the credential: its bearer call works, and a request that sends the cookie
// cannot be read. The preflight still names the methods, because a bearer call is
// preflighted too. An identity origin keeps the credentialed answer.
func TestCookieRoutesWithholdTheCredential(t *testing.T) {
	app := signInApp(t)
	for _, r := range cookieRouteCalls {
		for _, preflight := range []bool{true, false} {
			for _, o := range appOrigins {
				res := ask(t, app, r.method, r.path, o, preflight)
				if got := res.Header.Get("Access-Control-Allow-Origin"); got != o {
					t.Errorf("%s %s from app %s (preflight=%v): ACAO %q, want %q — a bearer call lost its answer", r.method, r.path, o, preflight, got, o)
				}
				if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
					t.Errorf("%s %s from app %s (preflight=%v): ACAC %q, want none — the app can spend the session", r.method, r.path, o, preflight, got)
				}
				if preflight && res.Header.Get("Access-Control-Allow-Methods") == "" {
					t.Errorf("%s %s from app %s: preflight named no methods, so the bearer call cannot follow", r.method, r.path, o)
				}
			}
			for _, o := range identityOrigins {
				if res := ask(t, app, r.method, r.path, o, preflight); !credentialed(res, o) {
					t.Errorf("%s %s from identity origin %s (preflight=%v): ACAO %q ACAC %q, want the credentialed answer",
						r.method, r.path, o, preflight,
						res.Header.Get("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
				}
			}
		}
	}
}

// The rule only withholds. An identity host that is not first-party gets no answer
// on a sign-in route, and elsewhere reads as any site does: without credentials.
func TestSignInRuleNeverAdmits(t *testing.T) {
	app := signInApp(t)
	const o = "https://id.adnexus.dev" // pinned in IAM_ISSUER_MAP, absent from the allowlist
	for _, preflight := range []bool{true, false} {
		if got := ask(t, app, http.MethodPost, "/v1/iam/login", o, preflight).Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("POST /v1/iam/login from %s (preflight=%v): ACAO %q — naming an identity host opened a sign-in route to an origin that is not first-party", o, preflight, got)
		}
		for _, path := range []string{"/v1/iam/whoami", "/v1/iam/oauth/token"} {
			res := ask(t, app, http.MethodPost, path, o, preflight)
			if got := res.Header.Get("Access-Control-Allow-Origin"); got != o {
				t.Errorf("POST %s from %s (preflight=%v): ACAO %q, want the site's own read", path, o, preflight, got)
			}
			if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("POST %s from %s (preflight=%v): ACAC %q — naming an identity host granted credentials", path, o, preflight, got)
			}
		}
	}
}

// Every spelling the router resolves to the login handler is judged as login.
// fiber matches without regard to case and ignores a trailing slash, so a rule
// on the literal path would hand /V1/IAM/LOGIN/ the credentialed answer.
func TestSignInRouteEverySpelling(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: liveAllowlist}), identityHosts(t, liveIssuerMap)))
	app.Raw(http.MethodPost, "/v1/iam/login", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"handler": "login"})
	})
	const o = "https://safe.lux.network"
	reached := 0
	for _, p := range []string{
		"/v1/iam/login", "/v1/iam/login/", "/V1/IAM/LOGIN", "/v1/IAM/Login/",
		"/v1/iam/login//", "/v1/iam//login", "/v1/iam/./login", "/v1/iam/%6cogin",
		"/v1/iam/login?x=1", "/v1/iam/login;x",
	} {
		req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai"+p, nil)
		req.Header.Set("Origin", o)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if res.StatusCode != http.StatusOK {
			continue // the router did not resolve it to login; nothing to judge
		}
		reached++
		t.Logf("%s resolves to the login handler", p)
		if res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s reached the login handler with ACAO %q ACAC %q for an app origin",
				p, res.Header.Get("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
		}
	}
	if reached < 4 {
		t.Fatalf("only %d spellings reached the handler — the router's matching changed and this test no longer covers it", reached)
	}
}

// ONE VERDICT, end to end. The embedded IAM runs its own CORS filter and grants
// the credential to every console in IAM_SESSION_ORIGINS — console.hanzo.ai among
// them. It reads the Origin on its own routes (EdgeOrigin keeps it there), and
// whatever it writes, the response carries the edge's answer and no other.
func TestSignInChildCannotAnswerInstead(t *testing.T) {
	sessionOrigins := map[string]bool{"https://console.hanzo.ai": true, "https://hanzo.app": true}
	var sawOrigin []string
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: liveAllowlist}), identityHosts(t, liveIssuerMap)))
	app.Use(EdgeOrigin())
	// A stand-in for hanzoai/iam's filter (internal/cors), on the route it marks
	// [cookie]: echo a listed console with the credential.
	app.Use(zip.H(func(c *zip.Ctx) error {
		if o := c.Header("Origin"); o != "" {
			sawOrigin = append(sawOrigin, o)
			if sessionOrigins[o] {
				c.SetHeader("Access-Control-Allow-Origin", o)
				c.SetHeader("Access-Control-Allow-Credentials", "true")
			}
		}
		return c.Continue()
	}))
	app.Raw(http.MethodPost, "/v1/iam/login", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"handler": "login"})
	})
	app.Raw(http.MethodGet, "/v1/iam/whoami", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"handler": "whoami"})
	})

	for _, o := range []string{"https://console.hanzo.ai", "https://hanzo.app"} {
		if res := ask(t, app, http.MethodGet, "/v1/iam/whoami", o, false); res.Header.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("GET /v1/iam/whoami from %s: ACAC %q — the child credentialed what the edge withheld", o, res.Header.Get("Access-Control-Allow-Credentials"))
		}
	}
	for _, o := range []string{"https://console.hanzo.ai", "https://hanzo.app"} {
		res := ask(t, app, http.MethodPost, "/v1/iam/login", o, false)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/iam/login from %s: status %d, want the request to reach the handler", o, res.StatusCode)
		}
		if res.Header.Get("Access-Control-Allow-Origin") != "" || res.Header.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("POST /v1/iam/login from %s: ACAO %q ACAC %q — the child answered what the edge withheld",
				o, res.Header.Get("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
		}
	}
	if res := ask(t, app, http.MethodPost, "/v1/iam/login", "https://iam.hanzo.ai", false); !credentialed(res, "https://iam.hanzo.ai") {
		t.Errorf("identity origin lost its answer behind EdgeOrigin: ACAO %q ACAC %q",
			res.Header.Get("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
	}
	if want := []string{"https://console.hanzo.ai", "https://hanzo.app", "https://console.hanzo.ai", "https://hanzo.app", "https://iam.hanzo.ai"}; !slices.Equal(sawOrigin, want) {
		t.Errorf("IAM read Origin %v on its own routes, want %v: it is IAM's fact, not a CORS question", sawOrigin, want)
	}
	// And the origin verdict apps/ai reads is still the allowlist's, route-blind.
	if !CORSAllows(t.Context(), "https://console.hanzo.ai") {
		t.Error("CORSAllows refused an allowlisted origin; it must stay the route-blind origin verdict")
	}
}

func TestSessionRoute(t *testing.T) {
	for p, want := range map[string]bool{
		"/v1/iam/login":                  true,
		"/v1/iam/login/":                 true,
		"/V1/IAM/LOGIN":                  true,
		"/v1/iam//login":                 true, // a relay collapses these on the way
		"/v1/iam/./login":                true,
		"/v1/iam/x/../login":             true,
		"/v1/iam/%6cogin":                true,
		"/v1/iam/%2e/login":              true,
		"//v1/iam/web3/nonce":            true,
		"/v1/iam/web3/nonce":             true,
		"/v1/iam/web3/verify":            true,
		"/v1/iam/webauthn/signin/finish": true,
		"/v1/iam/signin":                 true,
		"/v1/iam/onboard":                true,
		"/v1/iam/oauth/callback":         true,
		"/v1/iam/oauth/federation/mfa":   true,
		"/v1/iam/signup":                 true,
		"/v1/iam/accounts":               true,
		"/v1/iam/oauth/authorize":        true,
		"/v1/iam/oauth/device":           true,
		"/v1/iam/oauth/device/info":      true,
		"/v1/iam%252flogin":              true, // encoded twice; a relay can decode it twice
		"/v1/iam/%25252Flogin":           true,
		"/v1/iam/loginx":                 false,
		"/v1/iam/account":                false, // a cookie route, not a sign-in route
		"/v1/iam/accountsx":              false,
		"/v1/iam/oauth/devicex":          false,
		"/v1/iam/webauthn-credentials":   false,
		"/v1/iam/web3x":                  false,
		"/v1/iam/oauth/token":            false,
		"/v1/iam/oauth/userinfo":         false,
		"/v1/iam/oauth/logout":           false,
		"/v1/iam/.well-known/jwks":       false,
		"/login/oauth/authorize":         false,
		"/":                              false,
		"":                               false,
	} {
		if got := sessionRoute(p); got != want {
			t.Errorf("sessionRoute(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestCookieRoute(t *testing.T) {
	for p, want := range map[string]bool{
		"/v1/iam/account":         true,
		"/V1/IAM/Account/":        true,
		"/v1/iam/%61ccount":       true,
		"/v1/iam/whoami":          true,
		"/v1/iam/consent":         true,
		"/v1/iam/preferences":     true,
		"/v1/iam/password":        true,
		"/v1/iam/link":            true,
		"/v1/iam/unlink":          true,
		"/v1/iam/linked-accounts": true,
		"/v1/iam//whoami":         true,
		"/v1/iam/accounts":        false, // a sign-in route: no answer at all
		"/v1/iam/linkx":           false,
		"/v1/iam/oauth/token":     false,
		"/v1/iam/oauth/revoke":    false,
		"/v1/iam/oauth/userinfo":  false,
		"/v1/iam/organizations":   false,
		"/v1/iam/users/admin/z":   false,
		"/v1/iam/tokens/issue":    false,
		"/v1/iam/webauthn/x":      false,
		"/":                       false,
	} {
		if got := cookieRoute(p); got != want {
			t.Errorf("cookieRoute(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestSignInHas(t *testing.T) {
	s := newSignIn(identityHosts(t, liveIssuerMap))
	for o, want := range map[string]bool{
		"https://lux.id":           true,
		"https://osage.id":         true,
		"https://iam.hanzo.ai":     true,
		"https://auth.hanzo.ai":    true,
		"https://hanzo.ai":         true,  // Hanzo's sign-in page is hanzo.ai/login
		"https://www.hanzo.ai":     false, // exact: no subdomain of it
		"https://x.hanzo.ai":       false,
		"https://lux.network":      false, // another brand's apex is not one
		"http://lux.id":            false, // cleartext
		"https://lux.id:8443":      false, // another port is another origin
		"https://LUX.ID":           false, // not canonical
		"https://lux.id.":          false,
		"https://lux.id/":          false,
		"https://evil-lux.id":      false,
		"https://lux.id.evil.com":  false,
		"https://x.lux.id":         false, // a subdomain of an identity host is not one
		"https://safe.lux.network": false,
		"https://hanzo.app":        false,
		"https://iam.hanzo.app":    false,
		"https://user:pw@lux.id":   false,
		"null":                     false,
		"":                         false,
	} {
		if got := s.has(o); got != want {
			t.Errorf("has(%q) = %v, want %v", o, got, want)
		}
	}
}

// THE PAGE'S ORIGIN REACHES IAM, and only IAM. A wallet challenge names the site
// it was asked from (hanzoai/iam internal/wallet `site`): the request's Origin when
// the application registers it, else the host it arrived on. Behind an edge that
// took the Origin off, every challenge asked from https://hanzo.ai at
// api.hanzo.ai named api.hanzo.ai, and the wallet warned the person the sign-in
// was for another site. The stand-in reads Origin and Host as IAM does; the AI
// routes still see no Origin, so their own filter has none to judge.
func TestIAMSeesThePageOrigin(t *testing.T) {
	registered := map[string]bool{"https://hanzo.ai": true}
	var aiSaw []string
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: liveAllowlist}), identityHosts(t, liveIssuerMap)))
	app.Use(EdgeOrigin())
	app.Raw(http.MethodGet, "/v1/iam/web3/nonce", func(c *zip.Ctx) error {
		domain := c.Host()
		if o := c.Header("Origin"); registered[o] {
			domain = strings.TrimPrefix(o, "https://")
		}
		// IAM's own filter answering too: the edge's answer must be the one that leaves.
		c.SetHeader("Access-Control-Allow-Origin", "https://evil.example")
		return c.JSON(http.StatusOK, map[string]string{"domain": domain})
	})
	app.Raw(http.MethodGet, "/v1/chat/completions", func(c *zip.Ctx) error {
		aiSaw = append(aiSaw, c.Header("Origin"))
		return c.JSON(http.StatusOK, map[string]string{})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/iam/web3/nonce?chain=evm&clientId=hanzo-ai", nil)
	req.Host = "api.hanzo.ai"
	req.Header.Set("Origin", "https://hanzo.ai")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["domain"] != "hanzo.ai" {
		t.Errorf("challenge names %q, want hanzo.ai: the page's Origin did not reach IAM", got["domain"])
	}
	if !credentialed(res, "https://hanzo.ai") || len(res.Header.Values("Access-Control-Allow-Origin")) != 1 {
		t.Errorf("ACAO %v ACAC %q: want the edge's one credentialed answer for the identity origin, not the child's",
			res.Header.Values("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
	}

	if res := ask(t, app, http.MethodGet, "/v1/chat/completions", "https://console.hanzo.ai", false); res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/chat/completions: status %d", res.StatusCode)
	}
	if !slices.Equal(aiSaw, []string{""}) {
		t.Errorf("a non-IAM child read Origin %v; only IAM's routes keep it", aiSaw)
	}
}

// IAM's CORS headers never outlive the edge's answer: not when its handler panics
// (the recovery that writes the 500 resets nothing), and not when a handler appends
// a second copy of a header instead of replacing the first.
func TestIAMCannotLeakCORSPastTheEdge(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(zip.Handler(func(c *zip.Ctx) (err error) {
		defer func() {
			if recover() != nil {
				err = c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal"})
			}
		}()
		return c.Continue()
	}))
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: liveAllowlist}), identityHosts(t, liveIssuerMap)))
	app.Use(EdgeOrigin())
	app.Raw(http.MethodPost, "/v1/iam/login", func(c *zip.Ctx) error {
		c.SetHeader("Access-Control-Allow-Origin", c.Header("Origin"))
		c.SetHeader("Access-Control-Allow-Credentials", "true")
		panic("handler failed")
	})
	app.Raw(http.MethodGet, "/v1/iam/keys", func(c *zip.Ctx) error {
		c.Fiber().Response().Header.Add("Access-Control-Allow-Origin", "https://evil.example")
		return c.JSON(http.StatusOK, map[string]string{})
	})

	res := ask(t, app, http.MethodPost, "/v1/iam/login", "https://console.hanzo.ai", false)
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("POST /v1/iam/login: status %d, want the recovered 500", res.StatusCode)
	}
	if credentialed(res, "https://console.hanzo.ai") {
		t.Errorf("a panicking IAM handler left its credentialed CORS answer on the 500: ACAO %q ACAC %q",
			res.Header.Get("Access-Control-Allow-Origin"), res.Header.Get("Access-Control-Allow-Credentials"))
	}

	res = ask(t, app, http.MethodGet, "/v1/iam/keys", "https://console.hanzo.ai", false)
	for _, v := range res.Header.Values("Access-Control-Allow-Origin") {
		if v == "https://evil.example" {
			t.Errorf("an appended Access-Control-Allow-Origin survived the edge: %v", res.Header.Values("Access-Control-Allow-Origin"))
		}
	}
}

// A path that only normalizes to an IAM route is not IAM's: it keeps the strip.
func TestOnlyIAMsOwnPathKeepsTheOrigin(t *testing.T) {
	var saw []string
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: liveAllowlist}), identityHosts(t, liveIssuerMap)))
	app.Use(EdgeOrigin())
	app.Raw(http.MethodGet, "/v1/other/*", func(c *zip.Ctx) error {
		saw = append(saw, c.Header("Origin"))
		return c.JSON(http.StatusOK, map[string]string{})
	})
	ask(t, app, http.MethodGet, "/v1/other/%252e%252e/%252e%252e/v1/iam/x", "https://hanzo.ai", false)
	if !slices.Equal(saw, []string{""}) {
		t.Errorf("a non-IAM route read Origin %q through a path that only normalizes to /v1/iam", saw)
	}
}

// identityHosts is Config.IdentityHosts for an IAM_ISSUER_MAP value: the registry's
// identity hosts plus the pinned ones, as LoadConfig reads them.
func identityHosts(t *testing.T, pinned string) []string {
	t.Helper()
	t.Setenv("IAM_ISSUER_MAP", pinned)
	return cloud.LoadConfig().IdentityHosts
}
