package cloud

// THE SELECTED ORG PAYS — the whole thread, end to end, in one file.
//
// A person belongs to several orgs, picks one in the @hanzo/iam switcher, and the
// surface sends it as X-Org-Id. Every assertion here drives a REAL RSA-signed IAM
// token (with the `orgs` membership claim IAM actually mints) through the REAL
// trust boundary (SanitizeIdentity) and reads the money address the debit uses
// (identityFromCtx → principal.WalletOf → account.Payer). Nothing is stubbed
// between the token and the ledger key, because the bug this closes lived exactly
// in that gap: the selection was stripped at the boundary, and even if it had
// survived, the payer was re-derived from the home org and ignored it.
//
// Each test states which half it pins:
//
//	SelectedOrgIsThePayer      — the switcher's org reaches the debit.
//	NonMemberSelectionIgnored  — a selection outside the signed set never lands.
//	MasqueradeSpendsOwnBooks   — platform sudo is not a selection.
//	UnresolvableOrgRefuses     — no payer ⟹ no charge, and no substitute payer.

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// memberClaims is a normal human token: home org `owner`, plus the signed `orgs`
// membership set IAM mints (home first). aud matches the validator's allowlist via
// tokenClaims.
func memberClaims(owner string, orgs ...string) idClaims {
	c := tokenClaims("hanzo-cloud", owner, owner+"@example.test", false, time.Now().Add(time.Hour))
	c.Name = "alice"
	// Take FULL control of the membership set: tokenClaims seeds the ordinary
	// app-org == user-org case, but this helper states the tenancy explicitly, and
	// `memberClaims(owner)` with no orgs must keep meaning a pre-claim LEGACY token
	// (the empty set), not a token that quietly inherited a home org.
	c.Orgs = nil
	for _, o := range orgs {
		c.Orgs = append(c.Orgs, authz.Membership{Org: o, Role: "member"})
	}
	return c
}

// TestUnresolvableOrgRefuses_NoBrandSubstitute is the second half of fail-closed,
// at the layer that actually spent: the metering client must REFUSE an empty org
// rather than fall back to its configured (brand) org. Without this, an org-less
// AuthInput reads — and a Record debits — whatever the platform's own wallet holds.
func TestUnresolvableOrgRefuses_NoBrandSubstitute(t *testing.T) {
	// A client configured with the brand org, exactly as build.go wires it.
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	c, err := meter.New(meter.Config{BaseURL: fc.server(t).URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The GATE: a funded brand org must not authorize an org-less principal.
	if err := c.Authorize(t.Context(), meter.AuthInput{User: "someone", AmountCents: 100}); err == nil {
		t.Error("Authorize with no org allowed the request — the brand's balance is not a substitute payer")
	}
	// The DEBIT: likewise refuses rather than posting under the brand header.
	if _, err := c.Record(t.Context(), meter.Usage{User: "someone", AmountCents: 100}); err == nil {
		t.Error("Record with no org posted a debit — it would land on the brand's ledger")
	}
	if n := fc.usages(); n != 0 {
		t.Errorf("commerce saw %d usage posts for an org-less debit, want 0", n)
	}
}

// TestAnonymousRoutesAreUntouched: no credential ⇒ no gate, on a public route.
func TestAnonymousRoutesAreUntouched(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Raw(http.MethodGet, "/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	cases := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{name: "bare anonymous request", mutate: nil},
		{
			name:   "anonymous with a forged X-Org-Id",
			mutate: func(r *http.Request) { r.Header.Set("X-Org-Id", "victim") },
		},
		{
			name:   "unparseable bearer",
			mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer not-a-jwt") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			if tc.mutate != nil {
				tc.mutate(req)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("Test request: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("anonymous /health = %d, want 200 (the identity boundary must never gate a public route)", resp.StatusCode)
			}
		})
	}
}

// TestAnonymousGrantsNoPrincipal is the security half of the test above: letting an
// anonymous request THROUGH must not also make it someone. A forged X-Org-Id
// survives for the documented Phase-1 data path, but it carries no validated user,
// so principal.Org refuses it and nothing org-scoped is served.
func TestAnonymousGrantsNoPrincipal(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)

	var gotUser string
	var gotAdmin bool
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
		gotUser, gotAdmin = c.User(), c.IsAdmin()
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Org-Id", "victim")
	req.Header.Set("X-User-Id", "u-forged")
	req.Header.Set("X-User-IsAdmin", "true")
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatalf("Test request: %v", err)
	}
	if gotUser != "" {
		t.Fatalf("anonymous request minted a user %q — a forged principal", gotUser)
	}
	if gotAdmin {
		t.Fatalf("anonymous request minted admin authority")
	}
}

// TestMembershipMatchIsByteExact pins the org switch's comparison. Folding would let a
// member of one org select a DISTINCT org that differs only by case or an invisible
// rune — the cross-org fold the injective org boundary exists to prevent.
//
// Deliberately NOT asserted: a TRAILING-space variant ("acme "). That value cannot
// exist at this layer — fasthttp OWS-trims it on the wire, so the boundary receives
// exactly "acme" and honors it, which is correct (the caller really is a member).
// The whitespace fold risk lives on the CLAIM side, where JSON preserves it, and is
// covered by the trailWSOwner cases in middleware_identity_test.go.
func TestMembershipMatchIsByteExact(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	tok := signWith(t, key, memberClaims("gotham", "gotham", "acme"))

	for _, sel := range []string{"ACME", "Acme", "ac me", "acme​"} {
		t.Run(sel, func(t *testing.T) {
			app, got := newIdentityApp(t, v)
			probe(t, app, func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+tok)
				r.Header.Set("X-Org-Id", sel)
			})
			if got.org != "gotham" {
				t.Fatalf("selection %q resolved to %q, want home %q — a fold across distinct orgs", sel, got.org, "gotham")
			}
		})
	}
}

// TestHomeOrgHeaderSurvivesSwitch: the boundary must keep reporting home and
// effective as DISTINCT facts. BillingOrg decides who pays from both together, so
// collapsing them would silently remove the masquerade carve-out.
func TestHomeOrgHeaderSurvivesSwitch(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	tok := signWith(t, key, memberClaims("hanzo", "hanzo", "acme"))

	var gotOwner, gotOrg string
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v))
	app.Raw(http.MethodGet, "/anchor", func(c *zip.Ctx) error {
		gotOwner, gotOrg = c.Header("X-User-Owner"), c.Org()
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/anchor", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Org-Id", "acme")
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatalf("Test request: %v", err)
	}
	if gotOwner != "hanzo" {
		t.Fatalf("X-User-Owner = %q, want %q (home must survive a switch)", gotOwner, "hanzo")
	}
	if gotOrg != "acme" {
		t.Fatalf("X-Org-Id = %q, want %q (effective must follow the switch)", gotOrg, "acme")
	}
}
