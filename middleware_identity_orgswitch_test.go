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

	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
	model "github.com/hanzoai/iam/pkg/model"
	"github.com/zap-proto/zip"
)

// walletProbe signs claims into a real token, drives it through SanitizeIdentity
// (adminOrg="admin") with the given client-selected X-Org-Id, and returns the money
// address the request would spend from plus the DATA scope it would read.
//
// ok is principal.WalletOf's refusal bit: false means the request may not touch
// money at all. billOrg/billUser are the ledger + wallet the gate checks and the
// debit drains — read through identityFromCtx, the SAME function BillingGate calls.
func walletProbe(t *testing.T, claims idClaims, selected string) (billOrg, billUser, dataOrg string, ok bool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	tok := signWith(t, key, claims)

	done := make(chan struct{})
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(c *zip.Ctx) error {
		in := identityFromCtx(c)
		billOrg, billUser = in.Org, in.User
		dataOrg, _ = principal.Org(c)
		_, ok = principal.WalletOf(c)
		close(done)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if selected != "" {
		req.Header.Set("X-Org-Id", selected)
	}
	if _, err := app.Fiber().Test(req); err != nil {
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
	for _, o := range orgs {
		c.Orgs = append(c.Orgs, model.OrgRef{Org: o, Role: "member"})
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
	for _, selected := range []string{"", "hanzo"} {
		billOrg, _, dataOrg, ok := walletProbe(t, memberClaims("hanzo", "hanzo", "acme"), selected)
		if !ok || billOrg != "hanzo" || dataOrg != "hanzo" {
			t.Errorf("selected=%q: bill=%q data=%q ok=%v, want hanzo/hanzo/true", selected, billOrg, dataOrg, ok)
		}
	}
}

// TestNonMemberSelectionIgnored: the signed set is the whole authorization. A
// selection naming an org the token does not carry is DISCARDED — the caller keeps
// acting, and paying, in their own org. It is not an error, because a stale
// localStorage selection after a membership is revoked is ordinary, and it must
// degrade to "your own org", never to "someone else's ledger".
func TestNonMemberSelectionIgnored(t *testing.T) {
	billOrg, billUser, dataOrg, ok := walletProbe(t, memberClaims("hanzo", "hanzo", "acme"), "victim")

	if !ok {
		t.Fatal("a validated caller with a stale selection must still resolve their own wallet")
	}
	if billOrg == "victim" || billUser == "victim" || dataOrg == "victim" {
		t.Fatalf("a non-member selection reached the request (bill=%q/%q data=%q) — cross-tenant", billOrg, billUser, dataOrg)
	}
	if billOrg != "hanzo" || dataOrg != "hanzo" {
		t.Errorf("bill=%q data=%q, want hanzo/hanzo (the caller's own org)", billOrg, dataOrg)
	}
}

// TestLegacyTokenCannotSwitch: a token minted before IAM shipped the `orgs` claim
// carries an EMPTY membership set, so it can select nothing and stays pinned to
// home. Same for an opaque key and a client_credentials machine, for which IAM never
// mints the claim at all. The switch is strictly additive — no token gains reach.
func TestLegacyTokenCannotSwitch(t *testing.T) {
	noClaim := memberClaims("hanzo") // no orgs at all
	billOrg, _, dataOrg, ok := walletProbe(t, noClaim, "acme")
	if !ok || billOrg != "hanzo" || dataOrg != "hanzo" {
		t.Fatalf("legacy token switched: bill=%q data=%q ok=%v, want hanzo/hanzo/true", billOrg, dataOrg, ok)
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

// TestUnresolvableOrgRefuses_NoBrandSubstitute is the second half of fail-closed,
// at the layer that actually spent: the metering client must REFUSE an empty org
// rather than fall back to its configured (brand) org. Without this, an org-less
// AuthInput reads — and a Record debits — whatever the platform's own wallet holds.
func TestUnresolvableOrgRefuses_NoBrandSubstitute(t *testing.T) {
	// A client configured with the brand org, exactly as build.go wires it.
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	c, err := metering.New(metering.Config{BaseURL: fc.server(t).URL, Token: "svc", Org: "hanzo"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The GATE: a funded brand org must not authorize an org-less principal.
	if err := c.Authorize(t.Context(), metering.AuthInput{User: "someone", AmountCents: 100}); err == nil {
		t.Error("Authorize with no org allowed the request — the brand's balance is not a substitute payer")
	}
	// The DEBIT: likewise refuses rather than posting under the brand header.
	if _, err := c.Record(t.Context(), metering.Usage{User: "someone", AmountCents: 100}); err == nil {
		t.Error("Record with no org posted a debit — it would land on the brand's ledger")
	}
	if n := fc.usages(); n != 0 {
		t.Errorf("commerce saw %d usage posts for an org-less debit, want 0", n)
	}
}
