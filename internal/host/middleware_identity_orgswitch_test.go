package host

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
	"github.com/hanzoai/cloud/sdk/oidctest"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// walletProbe signs claims into a real token, drives it through SanitizeIdentity
// (adminOrg="admin") with the given client-selected X-Org-Id, and returns the money
// address the request would spend from plus the DATA scope it would read.
//
// ok is principal.WalletOf's refusal bit: false means the request may not touch
// money at all. billOrg/billUser are the ledger + wallet the gate checks and the
// debit drains — read through identity, the SAME function BillingGate calls.
func walletProbe(t *testing.T, claims idClaims, selected string) (billOrg, billUser, dataOrg string, ok bool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := cloud.BoundaryOf(&cloud.Config{IAMIssuer: testIssuer, Discovery: oidctest.Discovery(testIssuer), DiscoveryClient: oidctest.Client(testIssuer, jwks.URL)})
	tok := signWith(t, key, claims)

	done := make(chan struct{})
	app := zip.New(zip.Config{})
	app.Use(cloud.SanitizeIdentity(v))
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
		in := identity(c, c.Path())
		billOrg, billUser = in.Org, in.User
		dataOrg, _ = principal.Org(c)
		ok = !meter.Payer(c).Zero()
		close(done)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if selected != "" {
		req.Header.Set("X-Org-Id", selected)
	}
	if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	<-done
	return billOrg, billUser, dataOrg, ok
}

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

// TestSelectedOrgIsThePayer is THE proof. alice's home org is `hanzo`; she is also a
// member of `acme` and selects it. Every cent must come out of ACME's books.
//
// Before this change the boundary discarded the selection (X-Org-Id was re-minted
// from `owner` unconditionally) and principal.BillingOrg keyed the debit on the home
// org, so this request billed `hanzo` — the switcher moved the data and nothing else.
func TestSelectedOrgIsThePayer(t *testing.T) {
	billOrg, billUser, dataOrg, ok := walletProbe(t, memberClaims("hanzo", "hanzo", "acme"), "acme")

	if !ok {
		t.Fatal("a validated member selecting their own org must resolve a wallet")
	}
	if billOrg != "acme" {
		t.Errorf("ledger charged = %q, want %q — the SELECTED org is the payer of record", billOrg, "acme")
	}
	if billUser != "acme" {
		t.Errorf("wallet drained = %q, want %q — a real org pays from its own pool", billUser, "acme")
	}
	if dataOrg != "acme" {
		t.Errorf("data scope = %q, want %q — one selection, one org, data and money together", dataOrg, "acme")
	}
}

// TestSelectedOrgIsThePayer_HomeIsJustAnotherChoice: selecting the home org (or
// selecting nothing) is not a special case — it resolves through the same rule and
// lands on the same ledger. The switcher's default is a selection like any other.
func TestSelectedOrgIsThePayer_HomeIsJustAnotherChoice(t *testing.T) {
	for _, selected := range []string{"", "gotham"} {
		billOrg, _, dataOrg, ok := walletProbe(t, memberClaims("gotham", "gotham", "acme"), selected)
		if !ok || billOrg != "gotham" || dataOrg != "gotham" {
			t.Errorf("selected=%q: bill=%q data=%q ok=%v, want gotham/gotham/true", selected, billOrg, dataOrg, ok)
		}
	}
}

// TestNonMemberSelectionIgnored: the signed set is the whole authorization. A
// selection naming an org the token does not carry is DISCARDED — the caller keeps
// acting, and paying, in their own org. It is not an error, because a stale
// localStorage selection after a membership is revoked is ordinary, and it must
// degrade to "your own org", never to "someone else's ledger".
func TestNonMemberSelectionIgnored(t *testing.T) {
	billOrg, billUser, dataOrg, ok := walletProbe(t, memberClaims("gotham", "gotham", "acme"), "victim")

	if !ok {
		t.Fatal("a validated caller with a stale selection must still resolve their own wallet")
	}
	if billOrg == "victim" || billUser == "victim" || dataOrg == "victim" {
		t.Fatalf("a non-member selection reached the request (bill=%q/%q data=%q) — cross-tenant", billOrg, billUser, dataOrg)
	}
	if billOrg != "gotham" || dataOrg != "gotham" {
		t.Errorf("bill=%q data=%q, want gotham/gotham (the caller's own org)", billOrg, dataOrg)
	}
}

// TestLegacyTokenCannotSwitch: a token minted before IAM shipped the `orgs` claim
// carries an EMPTY membership set, so it can select nothing — AND, since the home
// org is now read from that same set, it resolves no home either. It bills nothing.
//
// CONTRACT CHANGE, deliberate. This test previously asserted such a token "stays
// pinned to home" and expected `hanzo` — but that value came from the `owner`
// claim, which carries the minting APPLICATION's org, not the user's. Pinning to it
// is the vulnerability, not the safe default: it is what let a caller choose their
// tenant (and, when the app belonged to the admin org, their privilege) by choosing
// a login route. So the fallback is gone and this token now resolves NOTHING.
//
// The test's original intent — "no token gains reach" — is strengthened rather than
// weakened: it previously reached one org, and now reaches none. Denial is
// recoverable by re-authenticating within a token TTL; silently billing whichever
// tenant owned the app is not. See idClaims.homeOrg.
func TestLegacyTokenCannotSwitch(t *testing.T) {
	noClaim := memberClaims("hanzo") // no orgs at all
	billOrg, _, dataOrg, ok := walletProbe(t, noClaim, "acme")
	if ok || billOrg != "" || dataOrg != "" {
		t.Fatalf("legacy token resolved a tenant: bill=%q data=%q ok=%v, want empty/empty/false", billOrg, dataOrg, ok)
	}
}

// TestMasqueradeSpendsOwnBooks: platform sudo is NOT a selection. A SuperAdmin may
// act in any org, membership or not — that is what sudo means — so its effective org
// says nothing about who should pay. It spends from the admin ledger; the org being
// inspected is never charged for being looked at.
func TestMasqueradeSpendsOwnBooks(t *testing.T) {
	admin := tokenClaims("hanzo-cloud", "admin", "z@hanzo.ai", false, time.Now().Add(time.Hour))
	admin.Name = "z"
	billOrg, billUser, dataOrg, ok := walletProbe(t, admin, "victim")

	if !ok {
		t.Fatal("a SuperAdmin must resolve a wallet")
	}
	if billOrg == "victim" || billUser == "victim" {
		t.Fatalf("masquerade billed the inspected org (bill=%q/%q) — cross-tenant debit", billOrg, billUser)
	}
	if billOrg != "admin" {
		t.Errorf("ledger charged = %q, want admin (sudo spends its own books)", billOrg)
	}
	if dataOrg != "victim" {
		t.Errorf("data scope = %q, want victim (sudo still SEES the org it switched into)", dataOrg)
	}
}

// TestUnresolvableOrgRefuses is the fail-closed half. A validated principal whose
// `owner` claim carries a zero-width rune is org-less by design (OrgHasUnsafeRune
// refuses to fold it), so no ledger can be named — and the request must be REFUSED,
// not silently attached to a default.
//
// This is the shape of the F1 fail-open: WalletOf discarded BillingOrg's ok-bit and
// returned ok=true with an empty ledger, and metering.orgFor turned that empty
// ledger into the deployment's BRAND org. An unattributable request was therefore
// gated against Hanzo's balance. Both halves are pinned below.
func TestUnresolvableOrgRefuses(t *testing.T) {
	bad := memberClaims("han​zo", "han​zo") // zero-width space inside the owner
	billOrg, billUser, dataOrg, ok := walletProbe(t, bad, "")

	if ok {
		t.Fatalf("an org-less principal resolved a wallet (%q/%q) — money with no payer", billOrg, billUser)
	}
	if billOrg != "" || billUser != "" {
		t.Errorf("refused request still named a payer: org=%q user=%q", billOrg, billUser)
	}
	if dataOrg != "" {
		t.Errorf("org-less principal got data scope %q, want none", dataOrg)
	}
}
