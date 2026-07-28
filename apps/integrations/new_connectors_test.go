package integrations

// new_connectors_test.go proves the connectors this change adds — the CRM/support,
// analytics, commerce, and content key-connectors, and the salesforce/adroll/twitter/
// google-data OAuth connectors — are registered coherently for Mount and wired to the
// right verify transport with the right placement, path, and seal. Per-provider OAuth
// exchange + custom-verify behavior lives in the sibling *_test.go files; this file is
// the shared coherence + key-wiring proof.

import (
	"context"
	"strings"
	"testing"
)

// newKeyConnectors is every per-user key connector this change adds.
var newKeyConnectors = []string{
	"zendesk", "pipedrive", "intercom", "reamaze",
	"optimizely", "amplitude", "mixpanel", "posthog",
	"coinbase_commerce", "shipstation", "shipwire", "authorizenet",
	"contentful", "netlify", "recaptcha",
}

// newOAuthConnectors is every org OAuth connector this change adds.
var newOAuthConnectors = []struct {
	id        string
	category  string
	adminOnly bool
	secrets   []string
}{
	{"salesforce", "CRM", true, []string{accessSecret, refreshSecret, salesforceInstanceURL}},
	{"adroll", "Advertising", true, []string{accessSecret, refreshSecret}},
	{"twitter", "Social", true, []string{accessSecret, refreshSecret}},
	{"google_bigquery", "Data", true, []string{accessSecret, refreshSecret}},
	{"google_cloud", "Data", true, []string{accessSecret, refreshSecret}},
}

// TestNewKeyConnectorsWellFormed asserts every new key connector satisfies Mount's
// user-scope contract: Verify present, api_key custody + category set, and NO org-plane
// fields — so boot cannot panic and /v1/connectors derives method "token".
func TestNewKeyConnectorsWellFormed(t *testing.T) {
	for _, id := range newKeyConnectors {
		p, ok := registry[id]
		if !ok {
			t.Errorf("%s: not registered", id)
			continue
		}
		if p.Scope != userScope {
			t.Errorf("%s: Scope = %q, want userScope", id, p.Scope)
		}
		if p.Verify == nil {
			t.Errorf("%s: nil Verify", id)
		}
		if len(p.Secrets) == 0 || p.Secrets[0] != apiKeySecret {
			t.Errorf("%s: must custody api_key, got %v", id, p.Secrets)
		}
		if p.Category == "" {
			t.Errorf("%s: no Category", id)
		}
		if p.AdminOnly {
			t.Errorf("%s: a per-user key connector must not be AdminOnly", id)
		}
		if p.Authorize != nil || p.Exchange != nil || p.Revoke != nil || p.Configured != nil || p.Creds != nil || p.RedirectPath != "" {
			t.Errorf("%s: org-plane field set on a user-scoped provider (Mount would reject)", id)
		}
	}
}

// TestNewOAuthConnectorsCoherent asserts every new OAuth connector is org-scoped,
// AdminOnly (linking a marketing/CRM account is an org-admin action), declares the
// full OAuth surface Mount asserts, and has a callback RedirectPath that matches.
func TestNewOAuthConnectorsCoherent(t *testing.T) {
	for _, want := range newOAuthConnectors {
		p, ok := registry[want.id]
		if !ok {
			t.Fatalf("%s not registered", want.id)
		}
		if p.Scope != "" {
			t.Errorf("%s must be org-scoped (Scope==\"\"), got %q", want.id, p.Scope)
		}
		if p.AdminOnly != want.adminOnly {
			t.Errorf("%s AdminOnly = %v, want %v", want.id, p.AdminOnly, want.adminOnly)
		}
		if p.Kind == apiKeyKind {
			t.Errorf("%s must be an OAuth provider, got apikey", want.id)
		}
		if p.Configured == nil || p.Creds == nil || p.Authorize == nil || p.Exchange == nil {
			t.Errorf("%s must declare Configured/Creds/Authorize/Exchange", want.id)
		}
		if p.RedirectPath != callbackPath(want.id) {
			t.Errorf("%s RedirectPath %q must equal %q", want.id, p.RedirectPath, callbackPath(want.id))
		}
		if p.Category != want.category {
			t.Errorf("%s category %q, want %q", want.id, p.Category, want.category)
		}
		if strings.Join(p.Secrets, ",") != strings.Join(want.secrets, ",") {
			t.Errorf("%s secrets %v, want %v", want.id, p.Secrets, want.secrets)
		}
	}
}

// keyWireCases pins each keyVerify connector's env seam, verify path, credential
// placement, and (for account-scoped ones) the account hint. The token is a sentinel
// that must arrive exactly where the provider expects and seal under api_key.
var keyWireCases = []struct {
	id, env, rawPath, tok, acct string
	place                       placement
	header                      string // header name for headerAt
}{
	{"zendesk", "ZENDESK_API_BASE", "/api/v2/users/me.json", "me@co.com/token:" + sentinel, "acme", basicRaw, ""},
	{"pipedrive", "PIPEDRIVE_API_BASE", "/v1/users/me", sentinel + "PIPE", "", headerAt, "x-api-token"},
	{"intercom", "INTERCOM_API_BASE", "/me", sentinel + "INTER", "", bearer, ""},
	{"reamaze", "REAMAZE_API_BASE", "/api/v1/channels", "me@co.com:" + sentinel, "acme", basicRaw, ""},
	{"optimizely", "OPTIMIZELY_API_BASE", "/v2/projects", sentinel + "OPTI", "", bearer, ""},
	{"amplitude", "AMPLITUDE_API_BASE", "/api/2/annotations", "apikey:" + sentinel, "", basicRaw, ""},
	{"mixpanel", "MIXPANEL_API_BASE", "/api/app/projects/PROJ42/schemas", "sauser:" + sentinel, "PROJ42", basicRaw, ""},
	{"posthog", "POSTHOG_API_BASE", "/api/users/@me/", sentinel + "PH", "", bearer, ""},
	{"coinbase_commerce", "COINBASE_COMMERCE_API_BASE", "/charges?limit=1", sentinel + "CC", "", headerAt, "X-CC-Api-Key"},
	{"shipstation", "SHIPSTATION_API_BASE", "/carriers", "apikey:" + sentinel, "", basicRaw, ""},
	{"shipwire", "SHIPWIRE_API_BASE", "/api/v3/stock?limit=1", "user:" + sentinel, "", basicRaw, ""},
	{"contentful", "CONTENTFUL_API_BASE", "/spaces", sentinel + "CMA", "", bearer, ""},
	{"netlify", "NETLIFY_API_BASE", "/api/v1/user", sentinel + "NET", "", bearer, ""},
}

// TestNewKeyConnectorsWiring drives each key connector through its env seam to a
// capture server and asserts the credential arrives at the right path with the right
// placement, and that a 200 seals it under api_key — proving the declarative
// registration is wired, not just present.
func TestNewKeyConnectorsWiring(t *testing.T) {
	ctx := context.Background()
	for _, tc := range keyWireCases {
		t.Run(tc.id, func(t *testing.T) {
			srv, cap := captureServer(t, 0)
			t.Setenv(tc.env, srv.URL)
			res, err := registry[tc.id].Verify(ctx, VerifyInput{Token: tc.tok, AccountID: tc.acct})
			if err != nil {
				t.Fatalf("%s verify: %v", tc.id, err)
			}
			s := cap.snap()
			wantPath, wantQuery, _ := strings.Cut(tc.rawPath, "?")
			if s.path != wantPath {
				t.Errorf("%s path = %q, want %q", tc.id, s.path, wantPath)
			}
			if wantQuery != "" && !strings.Contains(s.rawQuery, wantQuery) {
				t.Errorf("%s query = %q, want contains %q", tc.id, s.rawQuery, wantQuery)
			}
			switch tc.place {
			case bearer:
				if s.auth != "Bearer "+tc.tok {
					t.Errorf("%s auth = %q, want bearer", tc.id, s.auth)
				}
			case headerAt:
				if got := s.headers.Get(tc.header); got != tc.tok {
					t.Errorf("%s header %s = %q, want the token", tc.id, tc.header, got)
				}
				if s.auth != "" {
					t.Errorf("%s header placement set Authorization: %q", tc.id, s.auth)
				}
			case basicRaw:
				if got := decodeBasic(t, s.auth); got != tc.tok {
					t.Errorf("%s basic = %q, want %q", tc.id, got, tc.tok)
				}
			}
			if res.Tokens[apiKeySecret] != tc.tok {
				t.Errorf("%s sealed = %q, want the token under api_key", tc.id, res.Tokens[apiKeySecret])
			}
		})
	}
}

// TestNewKeyConnectorsFailClosedTokenFree asserts each key connector fails closed on a
// 401 and never echoes the credential — the fail-closed + token-free contract, per
// provider (keyverify_test proves the mechanism; this proves each registration).
func TestNewKeyConnectorsFailClosedTokenFree(t *testing.T) {
	ctx := context.Background()
	for _, tc := range keyWireCases {
		t.Run(tc.id, func(t *testing.T) {
			srv, _ := captureServer(t, 401)
			t.Setenv(tc.env, srv.URL)
			res, err := registry[tc.id].Verify(ctx, VerifyInput{Token: tc.tok, AccountID: tc.acct})
			if err == nil || res != nil {
				t.Fatalf("%s: want fail-closed on 401, got res=%v err=%v", tc.id, res, err)
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("%s: error leaked the credential: %v", tc.id, err)
			}
		})
	}
}
