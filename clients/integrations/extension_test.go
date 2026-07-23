package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// orgfake is a clean, injectable org-plane OAUTH provider used to prove the
// /v1/integrations → /v1/connectors (kind=oauth, scope=org) migration end-to-end
// without HTTP-mocking a real provider. Its funcs indirect to package vars armed
// per test (mirroring the "fake" user provider in connectors_test.go).
var (
	orgFakeConfigured = func() bool { return true }
	orgFakeAuthorize  = func(_ OAuthConfig, _ /*redirectURI*/, state string) (string, error) {
		orgFakeCapturedState = state
		return "https://provider.test/authorize?state=" + state, nil
	}
	orgFakeExchange = func(context.Context, OAuthConfig, string, string) (*ExchangeResult, error) {
		return &ExchangeResult{
			Tokens:       map[string]string{accessSecret: "ORGFAKE-AT"},
			ExternalID:   "EXT-ACME",
			AccountLabel: "Acme Inc",
			Scopes:       []string{"read", "write"},
		}, nil
	}
	orgFakeCapturedState string
)

func init() {
	register(&Provider{
		ID: "orgfake", Name: "Org Fake", Description: "org oauth proof provider",
		Category:     "Test",
		Capabilities: []string{"test-capability"},
		Secrets:      []string{accessSecret},
		RedirectPath: callbackPath("orgfake"),
		Configured:   func() bool { return orgFakeConfigured() },
		Creds:        func() OAuthConfig { return OAuthConfig{ClientID: "cid", ClientSecret: "sec"} },
		Authorize:    func(c OAuthConfig, r, s string) (string, error) { return orgFakeAuthorize(c, r, s) },
		Exchange: func(ctx context.Context, c OAuthConfig, r, code string) (*ExchangeResult, error) {
			return orgFakeExchange(ctx, c, r, code)
		},
	})
}

// asAdmin is `as` with the OWN-org admin bit — needed for AdminOnly org providers
// (cloudflare) on the mutation verbs.
func asAdmin(t *testing.T, app *zip.App, method, path, org, user string, body any) httpResult {
	t.Helper()
	var rqBody []byte
	if body != nil {
		rqBody, _ = json.Marshal(body)
	}
	rq := httptest.NewRequest(method, path, bytes.NewReader(rqBody))
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", user)
	rq.Header.Set("X-User-IsOrgAdmin", "true")
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: buf.Bytes()}
}

func extByID(exts []Extension, id string) (Extension, bool) {
	for _, e := range exts {
		if e.ID == id {
			return e, true
		}
	}
	return Extension{}, false
}

// TestUnifiedListFoldsBothPlanes proves the ONE list: /v1/connectors returns org
// AND user extensions in one call, each carrying kind/scope as VALUES + the
// capabilities it enables; ?kind= and ?scope= filter on those values.
func TestUnifiedListFoldsBothPlanes(t *testing.T) {
	app := newApp(t, newKMS(t))

	all := decode[listResp](t, asOK(t, app, http.MethodGet, "/v1/connectors", "acme", userEmail, nil))

	// Org OAuth provider.
	of, ok := extByID(all.Extensions, "orgfake")
	if !ok || of.Kind != kindOAuth || of.Scope != orgScope {
		t.Fatalf("orgfake want kind=oauth scope=org, got %+v", of)
	}
	if len(of.Capabilities) != 1 || of.Capabilities[0] != "test-capability" {
		t.Fatalf("orgfake capabilities want [test-capability], got %v", of.Capabilities)
	}
	// Org KEY provider (cloudflare) — same scope, different kind VALUE.
	cf, ok := extByID(all.Extensions, "cloudflare")
	if !ok || cf.Kind != kindKey || cf.Scope != orgScope {
		t.Fatalf("cloudflare want kind=key scope=org, got %+v", cf)
	}
	if len(cf.Capabilities) != 1 || cf.Capabilities[0] != "cf-resource-management" {
		t.Fatalf("cloudflare capabilities want [cf-resource-management], got %v", cf.Capabilities)
	}
	// User provider — same list, scope VALUE differs.
	fk, ok := extByID(all.Extensions, "fake")
	if !ok || fk.Scope != userScope {
		t.Fatalf("fake want scope=user, got %+v", fk)
	}

	// ?kind=oauth returns ONLY oauth-kind extensions.
	oa := decode[listResp](t, asOK(t, app, http.MethodGet, "/v1/connectors?kind=oauth", "acme", userEmail, nil))
	for _, e := range oa.Extensions {
		if e.Kind != kindOAuth {
			t.Fatalf("?kind=oauth returned %s-kind extension %q", e.Kind, e.ID)
		}
	}
	if _, ok := extByID(oa.Extensions, "cloudflare"); ok {
		t.Fatal("?kind=oauth must not return the key-kind cloudflare")
	}
	// ?scope=org excludes user providers; ?kind=key&scope=org narrows to org keys.
	ko := decode[listResp](t, asOK(t, app, http.MethodGet, "/v1/connectors?kind=key&scope=org", "acme", userEmail, nil))
	for _, e := range ko.Extensions {
		if e.Kind != kindKey || e.Scope != orgScope {
			t.Fatalf("?kind=key&scope=org returned %s/%s extension %q", e.Kind, e.Scope, e.ID)
		}
	}
	if _, ok := extByID(ko.Extensions, "fake"); ok {
		t.Fatal("?scope=org must not return a user provider")
	}
}

// TestOrgOAuthMigrationEndToEnd is THE proof: an org OAuth provider connects,
// returns from callback, custodies its token in KMS, shows connected on the
// unified surface, and forgets — all on /v1/connectors, tenant-isolated.
func TestOrgOAuthMigrationEndToEnd(t *testing.T) {
	orgFakeConfigured = func() bool { return true }
	kc := newKMS(t)
	app := newApp(t, kc)

	// 1) connect → authorizeUrl (the OAuth begin leg on the unified plane).
	var beginExchanged bool
	orgFakeExchange = func(context.Context, OAuthConfig, string, string) (*ExchangeResult, error) {
		beginExchanged = true
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "ORGFAKE-AT"}, ExternalID: "EXT-ACME", AccountLabel: "Acme Inc", Scopes: []string{"read"}}, nil
	}
	var begin struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	rb := asOK(t, app, http.MethodPost, "/v1/connectors/orgfake/connect", "acme", userEmail, nil)
	if err := json.Unmarshal(rb.Body, &begin); err != nil || begin.AuthorizeURL == "" {
		t.Fatalf("connect want authorizeUrl, got %s", rb.Body)
	}
	if beginExchanged {
		t.Fatal("connect must NOT exchange before the callback")
	}
	state := orgFakeCapturedState
	if state == "" {
		t.Fatal("connect must mint + pass a state to Authorize")
	}

	// 2) callback → 302 back to console; token sealed BEFORE the row.
	cb := as(t, app, http.MethodGet, "/v1/connectors/orgfake/callback?code=THECODE&state="+state, "", "", nil)
	if cb.Code != http.StatusFound {
		t.Fatalf("callback want 302, got %d (%s)", cb.Code, cb.Body)
	}
	if !beginExchanged {
		t.Fatal("callback must exchange the code")
	}
	// 3) custody: the token is in the ORG KMS namespace (unchanged path), never leaked.
	tok, err := kc.Get(kmsPath("acme", "orgfake"), accessSecret, kmsEnv)
	if err != nil || string(tok) != "ORGFAKE-AT" {
		t.Fatalf("token must be custodied at the org path: %q err=%v", tok, err)
	}

	// 4) the unified surface shows connected + the account.
	got := decode[Extension](t, asOK(t, app, http.MethodGet, "/v1/connectors/orgfake", "acme", userEmail, nil))
	if !got.Connected || len(got.Instances) != 1 || got.Instances[0].Account != "Acme Inc" {
		t.Fatalf("orgfake must show connected+account, got %+v", got)
	}

	// 5) TENANT ISOLATION: org B sees NOT connected and cannot read org A's token.
	gotB := decode[Extension](t, asOK(t, app, http.MethodGet, "/v1/connectors/orgfake", "globex", "u-globex", nil))
	if gotB.Connected || len(gotB.Instances) != 0 {
		t.Fatalf("org B must not see org A's connection, got %+v", gotB)
	}
	// org B forget is idempotent and MUST NOT touch org A's custody.
	asOK(t, app, http.MethodDelete, "/v1/connectors/orgfake", "globex", "u-globex", nil)
	if tok, err := kc.Get(kmsPath("acme", "orgfake"), accessSecret, kmsEnv); err != nil || string(tok) != "ORGFAKE-AT" {
		t.Fatalf("org B forget must not delete org A's token: %q err=%v", tok, err)
	}

	// 6) forget (DELETE) removes the row AND the custodied secret.
	asOK(t, app, http.MethodDelete, "/v1/connectors/orgfake", "acme", userEmail, nil)
	if _, err := kc.Get(kmsPath("acme", "orgfake"), accessSecret, kmsEnv); err == nil {
		t.Fatal("forget must delete the custodied secret")
	}
	after := decode[Extension](t, asOK(t, app, http.MethodGet, "/v1/connectors/orgfake", "acme", userEmail, nil))
	if after.Connected {
		t.Fatalf("orgfake must be disconnected after forget, got %+v", after)
	}
}

// TestEnablementGateAndConfig proves the NEW enablement + config axis: it is
// orthogonal to custody, defaults ON, gates the token exits fail-closed when off,
// and round-trips config — for BOTH planes.
func TestEnablementGateAndConfig(t *testing.T) {
	orgFakeConfigured = func() bool { return true }
	kc := newKMS(t)
	app := newApp(t, kc)

	// Connect orgfake (org oauth) via connect+callback.
	orgFakeExchange = func(context.Context, OAuthConfig, string, string) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT"}, ExternalID: "E", AccountLabel: "Acme"}, nil
	}
	asOK(t, app, http.MethodPost, "/v1/connectors/orgfake/connect", "acme", userEmail, nil)
	as(t, app, http.MethodGet, "/v1/connectors/orgfake/callback?code=c&state="+orgFakeCapturedState, "", "", nil)

	// The in-process seam (what capability surfaces call) yields the token while enabled.
	if _, err := TokenFor(context.Background(), "acme", "orgfake", accessSecret); err != nil {
		t.Fatalf("enabled connector must yield a token via TokenFor: %v", err)
	}
	// Disable → the SAME seam fails CLOSED.
	de := decode[struct {
		Enabled bool `json:"enabled"`
	}](t, asOK(t, app, http.MethodPost, "/v1/connectors/orgfake/disable", "acme", userEmail, nil))
	if de.Enabled {
		t.Fatal("disable must report enabled=false")
	}
	if _, err := TokenFor(context.Background(), "acme", "orgfake", accessSecret); err == nil {
		t.Fatal("disabled connector must fail the token seam CLOSED")
	}
	// The list reflects the disabled state.
	got := decode[Extension](t, asOK(t, app, http.MethodGet, "/v1/connectors/orgfake", "acme", userEmail, nil))
	if got.Enabled {
		t.Fatal("disabled connector must show enabled=false on the unified surface")
	}
	// Re-enable → the seam works again.
	asOK(t, app, http.MethodPost, "/v1/connectors/orgfake/enable", "acme", userEmail, nil)
	if _, err := TokenFor(context.Background(), "acme", "orgfake", accessSecret); err != nil {
		t.Fatalf("re-enabled connector must yield a token again: %v", err)
	}

	// Config round-trips as an opaque object.
	asOK(t, app, http.MethodPatch, "/v1/connectors/orgfake/config", "acme", userEmail, map[string]any{"region": "sfo3"})
	cfg := decode[Extension](t, asOK(t, app, http.MethodGet, "/v1/connectors/orgfake", "acme", userEmail, nil))
	if cfg.Config["region"] != "sfo3" {
		t.Fatalf("config must round-trip, got %+v", cfg.Config)
	}
	// Enablement and config are orthogonal: enabling did not clobber config, config
	// did not clobber enablement.
	if !cfg.Enabled {
		t.Fatal("config write must not clobber the enablement bit")
	}

	// USER plane: the /token custody exit is gated too.
	reset(t)
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "UAT"}}, nil
	}
	asOK(t, app, http.MethodPost, "/v1/connectors/fake/connect", "acme", userEmail, map[string]any{"token": "t"})
	if r := as(t, app, http.MethodGet, "/v1/connectors/fake:default/token", "acme", userEmail, nil); r.Code != http.StatusOK {
		t.Fatalf("enabled user token want 200, got %d (%s)", r.Code, r.Body)
	}
	asOK(t, app, http.MethodPost, "/v1/connectors/fake:default/disable", "acme", userEmail, nil)
	if r := as(t, app, http.MethodGet, "/v1/connectors/fake:default/token", "acme", userEmail, nil); r.Code != http.StatusForbidden {
		t.Fatalf("disabled user token want 403, got %d (%s)", r.Code, r.Body)
	}
}

// TestUnifiedFailClosed proves the security floor: no principal → 403 on every
// verb; and mutating an AdminOnly org connector (cloudflare) needs OWN-org admin.
func TestUnifiedFailClosed(t *testing.T) {
	app := newApp(t, newKMS(t))

	// Every verb 403s without a principal.
	noPrincipal := []struct{ method, path string }{
		{http.MethodGet, "/v1/connectors"},
		{http.MethodGet, "/v1/connectors/orgfake"},
		{http.MethodPost, "/v1/connectors/orgfake/connect"},
		{http.MethodPost, "/v1/connectors/orgfake/enable"},
		{http.MethodPost, "/v1/connectors/orgfake/disable"},
		{http.MethodPatch, "/v1/connectors/orgfake/config"},
		{http.MethodDelete, "/v1/connectors/orgfake"},
	}
	for _, r := range noPrincipal {
		if res := as(t, app, r.method, r.path, "", "", nil); res.Code != http.StatusForbidden {
			t.Fatalf("%s %s without principal want 403, got %d (%s)", r.method, r.path, res.Code, res.Body)
		}
	}

	// AdminOnly org connector (cloudflare): a non-admin org member is refused on
	// the enablement + config verbs; an admin is allowed.
	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/v1/connectors/cloudflare/enable"},
		{http.MethodPost, "/v1/connectors/cloudflare/disable"},
		{http.MethodPatch, "/v1/connectors/cloudflare/config"},
	} {
		if res := as(t, app, r.method, r.path, "acme", userEmail, map[string]any{}); res.Code != http.StatusForbidden {
			t.Fatalf("%s %s by non-admin want 403, got %d (%s)", r.method, r.path, res.Code, res.Body)
		}
		if res := asAdmin(t, app, r.method, r.path, "acme", userEmail, map[string]any{}); res.Code != http.StatusOK {
			t.Fatalf("%s %s by admin want 200, got %d (%s)", r.method, r.path, res.Code, res.Body)
		}
	}

	// A completely unknown connector id is a clean 404 (never a 500).
	if res := asOK2(t, app, http.MethodGet, "/v1/connectors/nosuchthing", "acme", userEmail); res != http.StatusNotFound {
		t.Fatalf("unknown connector want 404, got %d", res)
	}
}

// asOK2 returns just the status code for a GET (compact helper for status-only asserts).
func asOK2(t *testing.T, app *zip.App, method, path, org, user string) int {
	t.Helper()
	return as(t, app, method, path, org, user, nil).Code
}

// orgkey is a self-contained org-plane KEY provider (kind=key, scope=org) with a
// Verify — used to prove the verify exit freezes on disable without an external mock.
var orgKeyVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
	return &ExchangeResult{Tokens: map[string]string{"api_key": "OK"}, ExternalID: "K", AccountLabel: "KeyAcc"}, nil
}

func init() {
	register(&Provider{
		ID: "orgkey", Name: "Org Key", Description: "org key proof provider",
		Category:   "Test",
		Kind:       kindKey,
		Secrets:    []string{"api_key"},
		Verify:     func(ctx context.Context, in VerifyInput) (*ExchangeResult, error) { return orgKeyVerify(ctx, in) },
		Configured: func() bool { return true },
		Creds:      func() OAuthConfig { return OAuthConfig{} },
	})
}

// TestGithubDisableFreezesInstallationToken is the MED-1 proof: github declares
// Secrets:nil and mints installation tokens through the bespoke InstallationToken
// path (Pages, repo-import, push-to-deploy mirror). Disable MUST freeze that mint —
// the single choke point (InstallationToken → gateEnabled) — or `disable` is inert
// on the highest-privilege connector. github is NOT AdminOnly, so a plain org
// member's disable is load-bearing.
func TestGithubDisableFreezesInstallationToken(t *testing.T) {
	withGithubApp(t, mockGitHub(t, nil))
	app := newApp(t, newKMS(t))
	// Seed a connected installation for acme (ExternalID = installation id).
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "acme", Provider: "github", ExternalID: "77", AccountLabel: "acme-gh",
	}); err != nil {
		t.Fatalf("seed github connection: %v", err)
	}

	// ENABLED (no config row): the choke is OPEN — the mint succeeds via the mock,
	// and the Pages read reaches GitHub (mock 404 "pages not enabled"), NOT 403.
	if tok, err := githubTokenForOrg(context.Background(), "acme"); err != nil || tok != "ghs_installation_token" {
		t.Fatalf("enabled github must mint: tok=%q err=%v", tok, err)
	}
	if code := asOK2(t, app, http.MethodGet, "/v1/integrations/github/repos/myrepo/pages", "acme", userEmail); code == http.StatusForbidden {
		t.Fatalf("enabled github Pages must not be 403, got %d", code)
	}

	// DISABLE via the unified surface — by a NON-admin org member (github is not AdminOnly).
	de := decode[struct {
		Enabled bool `json:"enabled"`
	}](t, asOK(t, app, http.MethodPost, "/v1/connectors/github/disable", "acme", userEmail, nil))
	if de.Enabled {
		t.Fatal("disable must report enabled=false")
	}

	// FROZEN: the shared mint fails CLOSED (Pages/import/mirror all mint through here).
	if _, err := InstallationToken(context.Background(), "acme"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled github InstallationToken must fail closed as disabled, got %v", err)
	}
	// githubTokenForOrg surfaces the gate's 403 (not a 502 "mint failed").
	if _, err := githubTokenForOrg(context.Background(), "acme"); err != nil {
		if he, ok := httpErr(err); !ok || he.Status != http.StatusForbidden {
			t.Fatalf("githubTokenForOrg on disabled must be 403, got %v", err)
		}
	} else {
		t.Fatal("githubTokenForOrg on disabled must error")
	}
	// The Pages HTTP surface (the exact attack Red named) is now blocked.
	if code := asOK2(t, app, http.MethodGet, "/v1/integrations/github/repos/myrepo/pages", "acme", userEmail); code != http.StatusForbidden {
		t.Fatalf("disabled github Pages must be 403, got %d", code)
	}

	// RE-ENABLE restores the mint.
	asOK(t, app, http.MethodPost, "/v1/connectors/github/enable", "acme", userEmail, nil)
	if tok, err := githubTokenForOrg(context.Background(), "acme"); err != nil || tok != "ghs_installation_token" {
		t.Fatalf("re-enabled github must mint again: tok=%q err=%v", tok, err)
	}
}

// TestDisableFreezesRefreshAndVerify proves LOW-2: disable also freezes the
// credential-USING exits (refresh rotates it, verify re-checks it) for symmetry
// with the token exits — a disabled connector is fully inert.
func TestDisableFreezesRefreshAndVerify(t *testing.T) {
	app := newApp(t, newKMS(t))

	// USER refresh (fake has Refresh): connect → disable → refresh 403.
	reset(t)
	fakeVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{accessSecret: "AT", refreshSecret: "RT"}}, nil
	}
	asOK(t, app, http.MethodPost, "/v1/connectors/fake/connect", "acme", userEmail, map[string]any{"token": "t"})
	asOK(t, app, http.MethodPost, "/v1/connectors/fake:default/disable", "acme", userEmail, nil)
	if r := as(t, app, http.MethodPost, "/v1/connectors/fake:default/refresh", "acme", userEmail, nil); r.Code != http.StatusForbidden {
		t.Fatalf("disabled user refresh want 403, got %d (%s)", r.Code, r.Body)
	}

	// ORG verify (orgkey has Verify): connect → verify OK → disable → verify 403.
	orgKeyVerify = func(context.Context, VerifyInput) (*ExchangeResult, error) {
		return &ExchangeResult{Tokens: map[string]string{"api_key": "K"}, AccountLabel: "KeyAcc"}, nil
	}
	asOK(t, app, http.MethodPost, "/v1/connectors/orgkey/connect", "acme", userEmail, map[string]any{"token": "k"})
	if r := as(t, app, http.MethodPost, "/v1/connectors/orgkey/verify", "acme", userEmail, nil); r.Code != http.StatusOK {
		t.Fatalf("enabled org verify want 200, got %d (%s)", r.Code, r.Body)
	}
	asOK(t, app, http.MethodPost, "/v1/connectors/orgkey/disable", "acme", userEmail, nil)
	if r := as(t, app, http.MethodPost, "/v1/connectors/orgkey/verify", "acme", userEmail, nil); r.Code != http.StatusForbidden {
		t.Fatalf("disabled org verify want 403, got %d (%s)", r.Code, r.Body)
	}
}
