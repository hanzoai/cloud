package deploy

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"
)

// ── open redirect ────────────────────────────────────────────────────────────

// TestSafeReturn is the open-redirect guard: a return path may only be a path on
// THIS host. Every off-host shape collapses to "/".
func TestSafeReturn(t *testing.T) {
	cases := []struct{ in, want string }{
		// legitimate same-host paths survive, query included.
		{"/", "/"},
		{"/applications", "/applications"},
		{"/applications?name=cloud&view=tree", "/applications?name=cloud&view=tree"},
		// empty / relative → default.
		{"", "/"},
		{"applications", "/"},
		// protocol-relative: the browser resolves these as ANOTHER ORIGIN.
		{"//evil.example", "/"},
		{"//evil.example/path", "/"},
		{`/\evil.example`, "/"},
		{`/\/evil.example`, "/"},
		// absolute URLs, any scheme.
		{"https://evil.example/x", "/"},
		{"http://evil.example", "/"},
		{"javascript:alert(1)", "/"},
		{"data:text/html,x", "/"},
		// host-relative with userinfo tricks.
		{"https://cd.hanzo.ai@evil.example", "/"},
	}
	for _, c := range cases {
		if got := safeReturn(c.in); got != c.want {
			t.Errorf("safeReturn(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ── content negotiation: redirect vs 403 ─────────────────────────────────────

// TestWantsDocument pins the ONE rule that decides whether a refusal is a
// sign-in redirect or a 403. It must answer "no" for everything that is not
// positively a browser document GET — the API contract depends on it.
func TestWantsDocument(t *testing.T) {
	cases := []struct {
		name                                  string
		method, dest, mode, accept, requested string
		want                                  bool
	}{
		// Browser navigation — the only "yes" cases.
		{"navigation (Sec-Fetch-Dest)", "GET", "document", "navigate", "text/html,*/*", "", true},
		{"navigation (mode only)", "GET", "", "navigate", "text/html", "", true},
		{"legacy browser, html accept", "GET", "", "", "text/html,application/xhtml+xml", "", true},

		// Browser subresource/XHR — Sec-Fetch-* decides, and it says no.
		{"fetch from page JS", "GET", "empty", "cors", "application/json", "", false},
		{"fetch that lies via Accept", "GET", "empty", "cors", "text/html", "", false},
		{"same-origin xhr", "GET", "empty", "same-origin", "*/*", "", false},

		// Non-browser API clients.
		{"curl (no headers)", "GET", "", "", "", "", false},
		{"json client", "GET", "", "", "application/json", "", false},
		{"wildcard accept", "GET", "", "", "*/*", "", false},
		{"legacy xhr header", "GET", "", "", "text/html", "XMLHttpRequest", false},
		{"html+json accept prefers api", "GET", "", "", "text/html,application/json", "", false},

		// A mutation is NEVER redirected — bouncing a POST silently drops it.
		{"POST navigation", "POST", "document", "navigate", "text/html", "", false},
		{"PUT navigation", "PUT", "document", "navigate", "text/html", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wantsDocument(c.method, c.dest, c.mode, c.accept, c.requested); got != c.want {
				t.Errorf("wantsDocument(%q,%q,%q,%q,%q) = %v, want %v",
					c.method, c.dest, c.mode, c.accept, c.requested, got, c.want)
			}
		})
	}
}

// TestGuardRefusesNonAdmin drives the negotiation through the REAL router: a
// non-SuperAdmin never reaches a handler — an API call gets 403, a browser
// navigation gets bounced to sign-in. Neither is ever served the data.
func TestGuardRefusesNonAdmin(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, fakeSvc())

	// API call (no Accept, the shape every API client and the existing e2e sends).
	resp := do(t, app, httptest.NewRequest("GET", "/v1/deploy/applications", nil))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("API call without admin = %d, want 403", resp.StatusCode)
	}

	// XHR that asks for HTML must STILL be 403 — Sec-Fetch-Dest decides.
	req := httptest.NewRequest("GET", "/v1/deploy/applications", nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	if resp := do(t, app, req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("XHR without admin = %d, want 403", resp.StatusCode)
	}

	// Browser navigation → 302 to the sign-in page, carrying where to come back to.
	req = httptest.NewRequest("GET", "/v1/deploy/applications?env=main", nil)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Accept", "text/html")
	resp = do(t, app, req)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("navigation without admin = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	if loc.Path != loginPath {
		t.Errorf("redirect path = %q, want %q", loc.Path, loginPath)
	}
	if got := loc.Query().Get("returnTo"); got != "/v1/deploy/applications?env=main" {
		t.Errorf("returnTo = %q, want the originating path+query", got)
	}

	// A mutation is refused with 403, never redirected.
	req = httptest.NewRequest("POST", "/v1/deploy/applications/cloud/sync", nil)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Accept", "text/html")
	if resp := do(t, app, req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST without admin = %d, want 403 (never a redirect)", resp.StatusCode)
	}
}

// ── PKCE ─────────────────────────────────────────────────────────────────────

// TestPKCEChallenge pins the S256 transform against the RFC 7636 Appendix B
// vector, so it stays byte-identical to IAM's own pkceChallenge.
func TestPKCEChallenge(t *testing.T) {
	const (
		verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	)
	if got := pkceChallenge(verifier); got != challenge {
		t.Errorf("pkceChallenge = %q, want the RFC 7636 vector %q", got, challenge)
	}
	// The challenge is not the verifier (the whole point of S256).
	if pkceChallenge("x") == "x" {
		t.Error("challenge must never equal the verifier")
	}
}

// TestRandomTokenIsUnique guards against a constant/predictable state nonce.
func TestRandomTokenIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := randomToken()
		if err != nil {
			t.Fatalf("randomToken: %v", err)
		}
		if len(tok) < 43 {
			t.Fatalf("randomToken = %q, want >= 256 bits of entropy", tok)
		}
		if seen[tok] {
			t.Fatal("randomToken repeated a value")
		}
		seen[tok] = true
	}
}

// ── login: the authorize hop ─────────────────────────────────────────────────

// TestLoginRedirectsToIAM asserts the authorize URL is well formed, carries PKCE,
// and that the state it publishes is the SAME nonce it stored in the flow cookie.
func TestLoginRedirectsToIAM(t *testing.T) {
	app, _ := signinApp(t, "https://iam.test")

	req := httptest.NewRequest("GET", "/v1/deploy/login?returnTo=/applications", nil)
	req.Host = "cd.hanzo.ai"
	req.Header.Set("X-Forwarded-Proto", "https")
	resp := do(t, app, req)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login = %d, want 302", resp.StatusCode)
	}

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	if want := "https://iam.test/v1/iam/oauth/authorize"; loc.Scheme+"://"+loc.Host+loc.Path != want {
		t.Errorf("authorize endpoint = %q, want %q", loc.Scheme+"://"+loc.Host+loc.Path, want)
	}
	q := loc.Query()
	if q.Get("client_id") != defaultClientID {
		t.Errorf("client_id = %q, want %q (the app whose organization is the admin org)", q.Get("client_id"), defaultClientID)
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
	if q.Get("redirect_uri") != "https://cd.hanzo.ai/v1/deploy/callback" {
		t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") == "" {
		t.Error("no code_challenge: the flow is not PKCE-protected")
	}

	// The flow cookie must exist, be HttpOnly, and its nonce must be the state.
	f, cookie := flowFrom(t, resp)
	if !cookie.HttpOnly || !cookie.Secure {
		t.Errorf("flow cookie HttpOnly=%v Secure=%v, want both true", cookie.HttpOnly, cookie.Secure)
	}
	if f.Nonce != q.Get("state") {
		t.Errorf("state %q != flow cookie nonce %q", q.Get("state"), f.Nonce)
	}
	if pkceChallenge(f.Verifier) != q.Get("code_challenge") {
		t.Error("published code_challenge does not derive from the stored verifier")
	}
	if f.Return != "/applications" {
		t.Errorf("stored return = %q, want /applications", f.Return)
	}
	// The verifier must never be published to the address bar.
	if strings.Contains(resp.Header.Get("Location"), f.Verifier) {
		t.Error("PKCE verifier leaked into the authorize URL")
	}
}

// TestLoginRejectsOffHostReturnTo: an attacker-supplied returnTo cannot make the
// completed sign-in land off-host.
func TestLoginRejectsOffHostReturnTo(t *testing.T) {
	app, _ := signinApp(t, "https://iam.test")
	req := httptest.NewRequest("GET", "/v1/deploy/login?returnTo=https://evil.example/steal", nil)
	req.Host = "cd.hanzo.ai"
	resp := do(t, app, req)
	f, _ := flowFrom(t, resp)
	if f.Return != "/" {
		t.Errorf("stored return = %q, want / (off-host returnTo must be dropped)", f.Return)
	}
}

// ── callback: the exchange ───────────────────────────────────────────────────

// TestCallbackRequiresMatchingState is the login-CSRF guard: without the flow
// cookie this browser started, no code is redeemed and no session is minted.
func TestCallbackRequiresMatchingState(t *testing.T) {
	app, iam := signinApp(t, "")

	// No flow cookie at all.
	resp := do(t, app, httptest.NewRequest("GET", "/v1/deploy/callback?code=c&state=s", nil))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback with no flow cookie = %d, want 400", resp.StatusCode)
	}
	assertNoSession(t, resp)

	// Flow cookie present, but the returned state is someone else's.
	req := httptest.NewRequest("GET", "/v1/deploy/callback?code=c&state=attacker-state", nil)
	req.AddCookie(&http.Cookie{Name: flowCookie, Value: encodeFlow(flow{Nonce: "real-nonce", Verifier: "v", Return: "/"})})
	resp = do(t, app, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback with mismatched state = %d, want 400", resp.StatusCode)
	}
	assertNoSession(t, resp)

	// A garbage cookie is not a flow.
	req = httptest.NewRequest("GET", "/v1/deploy/callback?code=c&state=x", nil)
	req.AddCookie(&http.Cookie{Name: flowCookie, Value: "!!!not-base64!!!"})
	if resp := do(t, app, req); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback with corrupt flow cookie = %d, want 400", resp.StatusCode)
	}

	if iam.calls != 0 {
		t.Errorf("IAM token endpoint was called %d times for refused callbacks, want 0", iam.calls)
	}
}

// TestCallbackRefusesNonAdminOrg: a VALID sign-in by a user outside the admin org
// mints NO session. The console refuses the role plainly rather than handing out a
// cookie that 403s everything.
func TestCallbackRefusesNonAdminOrg(t *testing.T) {
	app, iam := signinApp(t, "")
	iam.token = fakeJWT("hanzo", "someone", time.Hour) // a real user, wrong org

	resp := completeSignin(t, app)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("callback for a non-admin-org principal = %d, want 403", resp.StatusCode)
	}
	assertNoSession(t, resp)
}

// TestCallbackMintsSessionForSuperAdmin is the happy path: the session cookie is
// the token, under cloud's EXISTING cookie name, hardened, and the browser lands
// on the remembered path.
func TestCallbackMintsSessionForSuperAdmin(t *testing.T) {
	app, iam := signinApp(t, "")
	iam.token = fakeJWT("admin", "cto", 2*time.Hour)

	resp := completeSignin(t, app)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/applications" {
		t.Errorf("landed on %q, want the remembered /applications", got)
	}

	var sess *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			sess = ck
		}
		if ck.Name == flowCookie && ck.MaxAge >= 0 {
			t.Error("flow cookie must be cleared once consumed")
		}
	}
	if sess == nil {
		t.Fatalf("no %s cookie minted", sessionCookie)
	}
	if sess.Value != iam.token {
		t.Error("session cookie must carry the IAM access token verbatim (cloud re-validates it)")
	}
	if !sess.HttpOnly {
		t.Error("session cookie must be HttpOnly (page JS must never read the token)")
	}
	if !sess.Secure {
		t.Error("session cookie must be Secure")
	}
	if sess.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax (Strict breaks the OAuth return hop)", sess.SameSite)
	}
	if sess.Domain != "" {
		t.Errorf("session cookie Domain = %q, want host-only (never shared with sibling hosts)", sess.Domain)
	}
	if sess.MaxAge <= 0 || sess.MaxAge > int((2*time.Hour).Seconds()) {
		t.Errorf("session MaxAge = %d, want bounded by the token's own expiry", sess.MaxAge)
	}

	// The exchange used PKCE and the code, with no secret configured.
	if iam.form.Get("code_verifier") == "" {
		t.Error("token exchange sent no code_verifier")
	}
	if iam.form.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", iam.form.Get("grant_type"))
	}
	if iam.form.Get("redirect_uri") != "https://cd.hanzo.ai/v1/deploy/callback" {
		t.Errorf("exchange redirect_uri = %q, must match the authorize one byte for byte", iam.form.Get("redirect_uri"))
	}
}

// TestCallbackFailsClosedOnExchangeError: IAM refusing the code mints no session.
func TestCallbackFailsClosedOnExchangeError(t *testing.T) {
	app, iam := signinApp(t, "")
	iam.status = http.StatusBadRequest

	resp := completeSignin(t, app)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("callback with a rejected code = %d, want 401", resp.StatusCode)
	}
	assertNoSession(t, resp)
}

// ── flow cookie ──────────────────────────────────────────────────────────────

func TestDecodeFlow(t *testing.T) {
	// Round trip.
	f, err := decodeFlow(encodeFlow(flow{Nonce: "n", Verifier: "v", Return: "/x"}))
	if err != nil || f.Nonce != "n" || f.Verifier != "v" || f.Return != "/x" {
		t.Fatalf("round trip = (%+v, %v)", f, err)
	}
	// A tampered return path is re-checked, not trusted.
	f, err = decodeFlow(encodeFlow(flow{Nonce: "n", Verifier: "v", Return: "https://evil.example"}))
	if err != nil {
		t.Fatalf("decodeFlow: %v", err)
	}
	if f.Return != "/" {
		t.Errorf("tampered return = %q, want / (re-validated on read)", f.Return)
	}
	// Incomplete / malformed cookies are not flows.
	for _, bad := range []string{"", "%%%", encodeFlow(flow{Nonce: "n"}), encodeFlow(flow{Verifier: "v"})} {
		if _, err := decodeFlow(bad); err == nil {
			t.Errorf("decodeFlow(%q) = nil error, want rejection", bad)
		}
	}
}

func TestTokenIdentity(t *testing.T) {
	owner, user := tokenIdentity(fakeJWT("admin", "cto", time.Hour))
	if owner != "admin" || user != "cto" {
		t.Errorf("tokenIdentity = (%q,%q), want (admin,cto)", owner, user)
	}
	// Anything that is not a JWT yields no identity — and callback then refuses,
	// because "" is never the admin org.
	for _, bad := range []string{"", "not.a.jwt", "a.b", "..", "x"} {
		if o, _ := tokenIdentity(bad); o != "" {
			t.Errorf("tokenIdentity(%q) owner = %q, want empty", bad, o)
		}
	}
}

// ── harness ──────────────────────────────────────────────────────────────────

// fakeIAM is a stand-in for IAM's token endpoint, recording what the exchange sent.
type fakeIAM struct {
	token  string
	status int
	calls  int
	form   url.Values
}

// signinApp builds the real router over a state whose IAM is a local fake (or the
// literal issuer when one is given, for the no-network authorize assertions).
func signinApp(t *testing.T, issuer string) (*zip.App, *fakeIAM) {
	t.Helper()
	iam := &fakeIAM{token: fakeJWT("admin", "cto", time.Hour), status: http.StatusOK}
	if issuer == "" {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/iam/oauth/token" {
				http.NotFound(w, r)
				return
			}
			iam.calls++
			_ = r.ParseForm()
			iam.form = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(iam.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": iam.token})
		}))
		t.Cleanup(srv.Close)
		issuer = srv.URL
	}
	svc := fakeSvc()
	svc.State.oauth = oauth{
		issuer: issuer, clientID: defaultClientID, adminOrg: "admin",
		publicURL: "https://cd.hanzo.ai", http: &http.Client{Timeout: 5 * time.Second},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, svc)
	return app, iam
}

// completeSignin drives login → callback with the flow cookie the login handed
// back, i.e. exactly what a browser does.
func completeSignin(t *testing.T, app *zip.App) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/deploy/login?returnTo=/applications", nil)
	req.Host = "cd.hanzo.ai"
	start := do(t, app, req)
	_, cookie := flowFrom(t, start)

	loc, _ := url.Parse(start.Header.Get("Location"))
	cb := httptest.NewRequest("GET", "/v1/deploy/callback?code=the-code&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	cb.Host = "cd.hanzo.ai"
	cb.AddCookie(&http.Cookie{Name: flowCookie, Value: cookie.Value})
	return do(t, app, cb)
}

func do(t *testing.T, app *zip.App, req *http.Request) *http.Response {
	t.Helper()
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func flowFrom(t *testing.T, resp *http.Response) (flow, *http.Cookie) {
	t.Helper()
	for _, ck := range resp.Cookies() {
		if ck.Name == flowCookie && ck.Value != "" {
			f, err := decodeFlow(ck.Value)
			if err != nil {
				t.Fatalf("flow cookie: %v", err)
			}
			return f, ck
		}
	}
	t.Fatal("no flow cookie was set")
	return flow{}, nil
}

func assertNoSession(t *testing.T, resp *http.Response) {
	t.Helper()
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie && ck.Value != "" {
			t.Fatalf("a session cookie was minted on a refused sign-in: %q", ck.Value)
		}
	}
}

func encodeFlow(f flow) string {
	blob, _ := json.Marshal(f)
	return base64.RawURLEncoding.EncodeToString(blob)
}

// fakeJWT builds a structurally valid, UNSIGNED JWT. That is exactly the point of
// the test: nothing in this package trusts the signature — cloud's identity
// boundary re-verifies the token on every later request, and this only exercises
// the claim read that decides whether a cookie is worth minting.
func fakeJWT(owner, name string, ttl time.Duration) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]any{"alg": "RS256", "typ": "JWT"}) + "." +
		enc(map[string]any{"owner": owner, "name": name, "exp": time.Now().Add(ttl).Unix()}) + "." +
		"not-a-real-signature"
}
