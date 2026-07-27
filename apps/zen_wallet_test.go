// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package apps

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/zen"
	"github.com/zap-proto/zip"
)

// The money's address is (ledger, account), and the zen path is the one that used
// to carry only the ledger. These tests pin the whole address across the two places
// zen touches money — the pre-serve gate and the post-serve debit — against the
// resolver every OTHER path already uses (principal.WalletOf, i.e. account.Payer).
//
// The failure they exist to prevent is not "zen bills the wrong number"; it is zen
// billing the wrong WALLET, which reads as two unrelated symptoms at once: a $0
// signup served for free off the signup org's funded pool, and a member who had
// bought credit refused because the purchase landed in that same pool.

// tenant drives cloudTenantResolver over the SAME zip.Ctx the identity boundary
// feeds in production, so a test sets the headers SanitizeIdentity mints.
func tenant(t *testing.T, headers map[string]string) zen.Tenant {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/t", func(c *zip.Ctx) error {
		return c.JSON(200, cloudTenantResolver(c))
	})
	req := httptest.NewRequest("GET", "/t", nil)
	for h, v := range headers {
		req.Header.Set(h, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("tenant probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out zen.Tenant
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("tenant decode: %v (%s)", err, b)
	}
	return out
}

// walletOf is the address every non-zen money path resolves: the edge BillingGate,
// the ai prepaid gate, the ai usage debit and GET /v1/billing/balance.
func walletOf(t *testing.T, headers map[string]string) principal.Wallet {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/w", func(c *zip.Ctx) error {
		w, ok := principal.WalletOf(c)
		return c.JSON(200, map[string]any{"ledger": w.Ledger, "account": w.Account, "ok": ok})
	})
	req := httptest.NewRequest("GET", "/w", nil)
	for h, v := range headers {
		req.Header.Set(h, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("wallet probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Ledger  string `json:"ledger"`
		Account string `json:"account"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("wallet decode: %v (%s)", err, b)
	}
	return principal.Wallet{Ledger: out.Ledger, Account: out.Account}
}

// signupMember is the live shape of a self-serve account: home org == the shared
// signup org, X-User-Id == the JWT `sub` (a UUID), X-User-Name == the IAM username.
// 100% of self-serve accounts look like this, and it is the ONLY shape where the
// pool and the person differ — which is why the bug was invisible to tenant orgs.
var signupMember = map[string]string{
	"X-User-Id":     "3f2a9c14-7b6e-4d05-9a11-8c73e2f0b4d6",
	"X-User-Name":   "z@hanzo.ai",
	"X-Org-Id":      account.SignupOrg,
	"X-User-Owner":  account.SignupOrg,
	"Authorization": "Bearer test",
}

// orgMember is a member of a REAL tenant org, whose members share one balance.
var orgMember = map[string]string{
	"X-User-Id":     "0d1e2f30-4a5b-6c7d-8e9f-a0b1c2d3e4f5",
	"X-User-Name":   "bob",
	"X-Org-Id":      "acme",
	"X-User-Owner":  "acme",
	"Authorization": "Bearer test",
}

// TestZenAddressIsTheOneAddress is the deliverable: for the same request, zen's
// gate, zen's debit and every other money path name ONE (ledger, account) pair.
//
// It fails on the old code for signupMember only — zen answered ("hanzo","hanzo")
// while everything else answered ("hanzo","hanzo/z@hanzo.ai") — and that is exactly
// the blast radius: every self-serve signup, no tenant org.
func TestZenAddressIsTheOneAddress(t *testing.T) {
	for name, h := range map[string]map[string]string{
		"signup org member (pool != person)": signupMember,
		"tenant org member (pool == person)": orgMember,
	} {
		want := walletOf(t, h)
		got := tenant(t, h)
		if got.BillingOrg != want.Ledger {
			t.Errorf("%s: zen ledger = %q, want %q", name, got.BillingOrg, want.Ledger)
		}
		// The gate reads Payer; so does the debit, through meterUsage.
		if got.Payer() != want.Account {
			t.Errorf("%s: zen gate address = %q, want %q — a funded member 402s, or an empty one serves free", name, got.Payer(), want.Account)
		}
		if debit := meterUsage(zen.Usage{Tenant: got}); debit.User != want.Account || debit.Org != want.Ledger {
			t.Errorf("%s: zen debit address = (%q,%q), want (%q,%q) — the gate and the debit have separated", name, debit.Org, debit.User, want.Ledger, want.Account)
		}
	}
}

// TestZenDebitAddressesWhatTheGateAuthorized: the gate and the debit read the SAME
// expression, so no header shape can make them disagree. This is the inversion the
// codebase has shipped twice (clients/principal/wallet.go): gate on the person,
// spend from the pool.
func TestZenDebitAddressesWhatTheGateAuthorized(t *testing.T) {
	for _, tn := range []zen.Tenant{
		{Org: "hanzo", BillingOrg: "hanzo", Wallet: "hanzo/alice", User: "hanzo/alice"},
		{Org: "acme", BillingOrg: "acme", User: "acme/robot"},
		{Org: "victim", BillingOrg: "hanzo", Wallet: "hanzo/root", User: "hanzo/root"},
	} {
		u := meterUsage(zen.Usage{Tenant: tn})
		if u.User != tn.Payer() {
			t.Errorf("tenant %+v: debit account = %q, gate account = %q", tn, u.User, tn.Payer())
		}
		if u.Org != tn.BillingOrg {
			t.Errorf("tenant %+v: debit ledger = %q, want %q", tn, u.Org, tn.BillingOrg)
		}
		// The actor is a different axis and must survive: for a machine key the
		// payer is the org while the actor is the key.
		if u.Actor != tn.User {
			t.Errorf("tenant %+v: actor = %q, want %q — attribution lost to the address", tn, u.Actor, tn.User)
		}
	}
}

// TestZenRefusesWithoutAWallet: an unresolvable principal yields the ZERO tenant,
// which zen's Valid() refuses. An anonymous caller with a forged X-Org-Id must never
// name a ledger — it could otherwise probe, and then drain, a victim org's balance.
func TestZenRefusesWithoutAWallet(t *testing.T) {
	tn := tenant(t, map[string]string{"X-Org-Id": "victim"})
	if tn.Valid() {
		t.Fatalf("forged X-Org-Id with no principal resolved a billable tenant: %+v", tn)
	}
	if tn.Payer() != "" || tn.BillingOrg != "" {
		t.Fatalf("unattributable request resolved an address: %+v", tn)
	}
}
