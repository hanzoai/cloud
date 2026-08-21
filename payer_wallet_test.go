package cloud

// payer_wallet_test.go — the two doors must name the SAME wallet, not merely the
// same org.
//
// A ledger is an org's books; a wallet is an account inside them. For a tenant org
// the two coincide, which is why substituting one for the other looks harmless
// everywhere it is tested. In the shared signup org they do not, and that is
// exactly where a self-serve stranger lives: the balance they topped up sits under
// <org>/<name>, and a charge addressed to the bare org spends the platform's pool
// instead.
//
// The signed billing_account claim is what distinguishes them, and it rides an
// HTTP header. So any path that rebuilds a payer from something OTHER than the
// request — a detached stream, an agent door — can silently drop it and answer one
// wallet where the direct call answers another. That is not a rounding difference:
// the gate reads one balance and the debit lands on the other.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// wallets answers the payer resolved on the REQUEST and the payer resolved after a
// Detach, for one identical request.
func wallets(t *testing.T, org, user, claim string) (direct, detached Payer) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(Bridge())
	app.Post("/probe", func(c *zip.Ctx) error {
		direct = PayerOf(c.Context())
		detached = PayerOf(Detach(context.Background(), c))
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", user)
	if claim != "" {
		req.Header.Set("X-Billing-Account-Id", claim)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return direct, detached
}

// A signed claim naming a person inside the caller's own org is the wallet, on
// BOTH doors. This is the one the streamed answer used to get wrong.
func TestDetachKeepsTheClaimedWallet(t *testing.T) {
	direct, detached := wallets(t, "acme", "bob", "person:acme/bob")

	if direct.Wallet != "acme/bob" {
		t.Fatalf("the direct door resolved %q, want %q — this test proves nothing if the "+
			"claim is not being honoured on the path that always did", direct.Wallet, "acme/bob")
	}
	if detached.Wallet != direct.Wallet {
		t.Fatalf("detached wallet = %q, direct = %q — one request, two wallets. The gate "+
			"weighs one balance and the debit lands on the other, which is the org-pool "+
			"substitution the wallet rule exists to prevent.", detached.Wallet, direct.Wallet)
	}
	// And the rest of the payer survives too: a debit attributes to the same caller.
	if detached.Actor != direct.Actor || detached.Project != direct.Project {
		t.Errorf("detached actor/project = %q/%q, direct = %q/%q",
			detached.Actor, detached.Project, direct.Actor, direct.Project)
	}
}

// With no claim the legacy rule answers the org, and the two doors still agree —
// so the fix cannot be an accident of the claim being present.
func TestDetachAgreesWithoutAClaim(t *testing.T) {
	direct, detached := wallets(t, "acme", "bob", "")
	if direct.Wallet == "" {
		t.Fatal("no wallet resolved on the direct door")
	}
	if detached.Wallet != direct.Wallet {
		t.Fatalf("detached wallet = %q, direct = %q", detached.Wallet, direct.Wallet)
	}
}

// A claim naming an account in ANOTHER org is ignored on both doors: a caller
// cannot name a wallet outside the org the boundary proved they belong to.
func TestDetachIgnoresACrossOrgClaim(t *testing.T) {
	direct, detached := wallets(t, "acme", "bob", "person:evil/mallory")
	if direct.Wallet == "evil/mallory" {
		t.Fatal("a cross-org billing claim was honoured on the direct door")
	}
	if detached.Wallet != direct.Wallet {
		t.Fatalf("detached wallet = %q, direct = %q", detached.Wallet, direct.Wallet)
	}
}
