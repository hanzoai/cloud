package integrations

// marketing_connectors_test.go holds the SHARED test harness for the Guide's
// marketing/ad connectors (meta/google_ads/google_analytics/microsoft/tiktok/reddit/
// linkedin/warpcast/whatsapp) plus the cross-provider coherence proof. Per-provider
// authorize/exchange tests live in each provider's _test.go.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/kms"
	"github.com/zap-proto/zip"
)

// marketingOAuthProviders is every ORG OAuth connector this change adds. AdminOnly,
// Scope="", OAuth (Authorize/Exchange), and their custodied secret set.
var marketingOAuthProviders = []struct {
	id       string
	category string
	secrets  []string
}{
	{"meta_ads", "Advertising", []string{accessSecret}},
	{"google_ads", "Advertising", []string{accessSecret, refreshSecret}},
	{"google_analytics", "Analytics", []string{accessSecret, refreshSecret}},
	{"microsoft_ads", "Advertising", []string{accessSecret, refreshSecret}},
	{"tiktok_ads", "Advertising", []string{accessSecret}},
	{"reddit_ads", "Advertising", []string{accessSecret, refreshSecret}},
	{"linkedin_ads", "Advertising", []string{accessSecret, refreshSecret}},
}

// marketingKeyProviders is every ORG apikey/token connector this change adds.
var marketingKeyProviders = []string{"warpcast", "whatsapp"}

// TestMarketingConnectorsRegisteredCoherent proves every new connector is on the
// registry with the shape Mount asserts: OAuth providers declare Configured/Creds/
// Authorize/Exchange + a matching callback RedirectPath + Scope=""; apikey providers
// declare Kind=apikey + Verify + Configured/Creds and no OAuth callback surface. Each
// is AdminOnly (linking a client's marketing account is an org-admin action) and on
// the ORG plane (invisible to the per-user /v1/connectors surface).
func TestMarketingConnectorsRegisteredCoherent(t *testing.T) {
	for _, want := range marketingOAuthProviders {
		p, ok := registry[want.id]
		if !ok {
			t.Fatalf("%s not registered", want.id)
		}
		if p.Scope != "" {
			t.Fatalf("%s must be org-scoped (Scope==\"\"), got %q", want.id, p.Scope)
		}
		if !p.AdminOnly {
			t.Fatalf("%s must be AdminOnly (marketing account linking is org-admin)", want.id)
		}
		if p.Kind == kindKey {
			t.Fatalf("%s must be an OAuth provider, got apikey", want.id)
		}
		if p.Configured == nil || p.Creds == nil || p.Authorize == nil || p.Exchange == nil {
			t.Fatalf("%s must declare Configured/Creds/Authorize/Exchange", want.id)
		}
		if p.RedirectPath != callbackPath(want.id) {
			t.Fatalf("%s RedirectPath %q must equal %q", want.id, p.RedirectPath, callbackPath(want.id))
		}
		if p.Category != want.category {
			t.Fatalf("%s category %q, want %q", want.id, p.Category, want.category)
		}
		if strings.Join(p.Secrets, ",") != strings.Join(want.secrets, ",") {
			t.Fatalf("%s secrets %v, want %v", want.id, p.Secrets, want.secrets)
		}
		// Org plane only: the per-user connector surface must not resolve it, and the
		// org surface must.
		if p.Scope == userScope {
			t.Fatalf("%s must not be user-scoped", want.id)
		}
	}
	for _, id := range marketingKeyProviders {
		p, ok := registry[id]
		if !ok {
			t.Fatalf("%s not registered", id)
		}
		if p.Scope != "" || !p.AdminOnly {
			t.Fatalf("%s must be org-scoped AdminOnly", id)
		}
		if p.Kind != kindKey || p.Verify == nil {
			t.Fatalf("%s must be an apikey provider with Verify", id)
		}
		if p.Configured == nil || p.Creds == nil {
			t.Fatalf("%s must declare Configured/Creds (Mount asserts it)", id)
		}
		if p.Authorize != nil || p.Exchange != nil || p.RedirectPath != "" {
			t.Fatalf("%s (apikey) must declare no OAuth callback surface", id)
		}
		if len(p.Secrets) == 0 || p.Secrets[0] != apiKeySecret {
			t.Fatalf("%s must custody api_key, got %v", id, p.Secrets)
		}
	}
}

// oauthCallbackViaState drives connect→callback for an AdminOnly OAuth provider and
// returns the callback result. It extracts the REAL signed state from the authorize
// URL, so the callback is exercised with a genuine state (CSRF/nonce path), never a
// forged one. connect is issued as an org admin (AdminOnly); callback is public and
// resolves the org from the signed state alone.
func oauthCallbackViaState(t *testing.T, app *zip.App, provider, org, code string) httpResult {
	t.Helper()
	res := cfReq(t, app, http.MethodPost, "/v1/connectors/"+provider+"/connect", org, true, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("%s connect want 200, got %d (%s)", provider, res.Code, res.Body)
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("%s connect body: %v (%s)", provider, err, res.Body)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatalf("%s authorize url parse: %v", provider, err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("%s authorize url missing state: %s", provider, out.AuthorizeURL)
	}
	return req(t, app, http.MethodGet,
		"/v1/connectors/"+provider+"/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), "", nil)
}

// kmsSecret reads a custodied secret straight from the org's KMS namespace for a
// provider — the seal-verification seam every e2e test uses.
func kmsSecret(t *testing.T, kc *kms.Client, org, provider, name string) ([]byte, bool) {
	t.Helper()
	v, err := kc.Get(kmsPath(org, provider), name, kmsEnv)
	if err != nil {
		return nil, false
	}
	return v, true
}
