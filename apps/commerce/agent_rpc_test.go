// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// agent_rpc_test.go — the headless purchase ops against the commerce core they address:
// the checkout's lifecycle and the status each step earns, the catalog an agent reads
// before it has a credential, the discovery document, a wire credited once, and a token
// for an invoice or subscription that is not the buyer's.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	commercemod "github.com/hanzoai/commerce"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/models/pendingintent"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
	"github.com/hanzoai/cloud/sdk/plane"
)

// agented is commerce co-resident with the finance ledger behind it, the agent ops
// exposed as Mount exposes them. It returns the ledger the credits land in.
func agented(t *testing.T) money.FinanceClient {
	t.Helper()
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	emb, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{DataDir: t.TempDir(), Dev: true, Ledger: ledger{}, Born: born(), Ceiling: ceiling()})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	PublishEmbedded(emb)
	t.Cleanup(func() {
		PublishEmbedded(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = emb.Stop(ctx)
	})
	fin := funded(t)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeAgent()
	return fin
}

// tenantOf is a call acting for org, as the billing endpoint's cloud.For makes it.
func tenantOf(org string) context.Context { return cloud.For(context.Background(), org) }

func code(c *client.CheckoutCompletion) string {
	if c.Checkout.Error == nil {
		return ""
	}
	return c.Checkout.Error.Code
}

// A CHECKOUT, STEP BY STEP: opened for one item and priced, changed while open, read back,
// and refused — never charged — when the payment cannot be taken. Each refusal keeps the
// status commerce chose and a code to branch on, in the answer and not as an error, and a
// checkout is the opener's alone.
func TestACheckoutIsOpenedChangedReadAndRefusedUnderCommercesStatus(t *testing.T) {
	agented(t)
	org := fresh("agent")
	ctx := tenantOf(org)
	const buyer, other = "ada", "grace"

	opened, err := planeCheckoutOpen(ctx, &client.CheckoutOpenIn{Subject: buyer, Items: []client.AgentItem{{Kind: "credit", UsdCents: 1000}}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.Status != "open" || opened.TotalCents != 1000 || opened.Currency != "usd" || len(opened.Items) != 1 {
		t.Fatalf("opened %+v", opened)
	}
	if _, err := planeCheckoutOpen(ctx, &client.CheckoutOpenIn{Subject: buyer, Items: []client.AgentItem{{Kind: "credit", UsdCents: 1000}, {Kind: "credit", UsdCents: 500}}}); statusOf(err) != 400 {
		t.Fatalf("two items: %v, want 400", err)
	}

	changed, err := planeCheckoutUpdate(ctx, &client.CheckoutUpdateIn{Subject: buyer, ID: opened.ID, Items: []client.AgentItem{{Kind: "credit", UsdCents: 2500}}})
	if err != nil || changed.TotalCents != 2500 || changed.Items[0].UsdCents != 2500 {
		t.Fatalf("update: %+v %v", changed, err)
	}
	if read, err := planeCheckout(ctx, &client.CheckoutRef{Subject: buyer, ID: opened.ID}); err != nil || read.ID != opened.ID || read.Status != "open" {
		t.Fatalf("read: %+v %v", read, err)
	}
	// Another buyer's id is a checkout that does not exist.
	if _, err := planeCheckout(ctx, &client.CheckoutRef{Subject: other, ID: opened.ID}); statusOf(err) != 404 {
		t.Fatalf("another buyer read it: %v, want 404", err)
	}
	if _, err := planeCheckoutUpdate(ctx, &client.CheckoutUpdateIn{Subject: other, ID: opened.ID, Items: []client.AgentItem{{Kind: "credit", UsdCents: 500}}}); statusOf(err) != 404 {
		t.Fatalf("another buyer changed it: %v, want 404", err)
	}
	// Nothing is paid, so there is no order.
	if _, err := planeOrder(ctx, &client.CheckoutRef{Subject: buyer, ID: opened.ID}); statusOf(err) != 404 {
		t.Fatalf("an order before payment: %v, want 404", err)
	}

	for name, tc := range map[string]struct {
		in     client.CheckoutCompleteIn
		status int
		code   string
	}{
		"no key":          {client.CheckoutCompleteIn{Subject: buyer, ID: opened.ID, Payment: client.AgentPay{Type: "stablecoin"}}, 400, "idempotency_key_required"},
		"not the buyer's": {client.CheckoutCompleteIn{Subject: other, ID: opened.ID, Key: "k1", Payment: client.AgentPay{Type: "stablecoin"}}, 404, "not_found"},
		"unknown type":    {client.CheckoutCompleteIn{Subject: buyer, ID: opened.ID, Key: "k2", Payment: client.AgentPay{Type: "cheque"}}, 400, "payment_type_unknown"},
		"no stablecoin":   {client.CheckoutCompleteIn{Subject: buyer, ID: opened.ID, Key: "k3", Payment: client.AgentPay{Type: "stablecoin"}}, 503, "stablecoin_unavailable"},
		"not a token":     {client.CheckoutCompleteIn{Subject: buyer, ID: opened.ID, Key: "k4", Payment: client.AgentPay{Type: "shared_payment_token", Token: "tok_x"}}, 402, "token_invalid"},
	} {
		done, err := planeCheckoutComplete(ctx, &tc.in)
		if err != nil {
			t.Fatalf("%s: a refusal is an answer, got the error %v", name, err)
		}
		if done.Status != tc.status || code(done) != tc.code {
			t.Errorf("%s: status %d code %q, want %d %q", name, done.Status, code(done), tc.status, tc.code)
		}
	}

	// The refusals changed nothing the buyer cannot retry: the checkout is failed, not paid,
	// and it can be changed and paid again.
	if read, err := planeCheckout(ctx, &client.CheckoutRef{Subject: buyer, ID: opened.ID}); err != nil || read.Status != "failed" || read.Order != nil {
		t.Fatalf("after the refusals: %+v %v", read, err)
	}
	if _, err := planeCheckoutUpdate(ctx, &client.CheckoutUpdateIn{Subject: buyer, ID: opened.ID, Items: []client.AgentItem{{Kind: "credit", UsdCents: 500}}}); err != nil {
		t.Fatalf("a failed checkout is changed: %v", err)
	}
}

// THE CATALOG IS PUBLIC: a call that acts for no org is answered, for the brand the host
// names, with the credit packs and the version; and so is a call that does.
func TestTheCatalogIsAnsweredToACallerWithNoOrg(t *testing.T) {
	agented(t)
	for name, ctx := range map[string]context.Context{"anonymous": context.Background(), "tenant": tenantOf(fresh("cat"))} {
		got, err := planeCatalog(ctx, &client.CatalogIn{Host: "hanzo.ai"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var cat struct {
			Version     string            `json:"version"`
			Currency    string            `json:"currency"`
			Plans       []json.RawMessage `json:"plans"`
			CreditPacks []struct {
				ID       string `json:"id"`
				UsdCents int64  `json:"usdCents"`
			} `json:"creditPacks"`
			Payment []json.RawMessage `json:"payment"`
			Links   map[string]string `json:"links"`
		}
		if err := json.Unmarshal(got.Body, &cat); err != nil {
			t.Fatalf("%s: %v: %s", name, err, got.Body)
		}
		if cat.Version != "1" || cat.Currency != "usd" || len(cat.CreditPacks) == 0 || cat.Plans == nil || cat.Payment == nil || cat.Links["checkouts"] != "/v1/billing/checkouts" {
			t.Errorf("%s: catalog %s", name, got.Body)
		}
	}
}

// With no commerce in the process the catalog is 503 for a caller with no org, not an
// empty catalog that reads as nothing for sale.
func TestTheCatalogNeedsCommerce(t *testing.T) {
	if _, err := planeCatalog(context.Background(), &client.CatalogIn{Host: "hanzo.ai"}); statusOf(err) != 503 {
		t.Fatalf("no commerce: %v, want 503", err)
	}
}

// THE DISCOVERY IS COMMERCE'S: the plane serves the document commerce's own handler writes.
func TestTheDiscoveryIsCommerces(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Raw(http.MethodGet, "/d", commercebilling.GetBillingDiscovery)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/d", nil), zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	want, _ := io.ReadAll(resp.Body)
	got, err := planeDiscovery(context.Background(), &cloud.Unit{})
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal(want, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got.Body, &b); err != nil {
		t.Fatalf("the plane's discovery is not JSON: %v", err)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatalf("the plane's discovery is not commerce's:\n plane    %s\n commerce %s", y, x)
	}
}

// A WIRE IS CREDITED ONCE, BY THE SUPERADMIN ALONE: a reference nobody was given credits
// nothing; the reference a payer was given credits that payer's wallet; a repeat posts
// nothing and a repeat for another amount is refused.
func TestAWireIsCreditedOnceBySuperAdmin(t *testing.T) {
	fin := agented(t)
	funder := fin.(finance.Funder)
	org, subject, ref := fresh("wire"), "ada", "WIRE-"+strings.ToUpper(fresh("REF"))
	if _, err := commerceorg.Resolve(context.Background(), org); err != nil {
		t.Fatalf("the payer's org: %v", err)
	}
	if err := pendingintent.PutRef(ref, org, subject, pendingintent.Wire, "wire", 0, "usd", network.SandboxOnly()); err != nil {
		t.Fatalf("a wire reference: %v", err)
	}
	staff := superAdmin(context.Background())
	in := &client.WireReconcileIn{Reference: ref, AmountCents: 2500, BankReference: "bank-1", By: "z@hanzo.ai"}

	if _, err := planeWireReconcile(tenantOf(org), in); !forbidden(err) {
		t.Fatalf("a member reconciled a wire: %v, want 403", err)
	}
	if _, err := planeWireReconcile(staff, &client.WireReconcileIn{Reference: "WIRE-NOBODY", AmountCents: 2500, By: "z@hanzo.ai"}); statusOf(err) != 404 {
		t.Fatalf("a reference nobody was given: %v, want 404", err)
	}
	first, err := planeWireReconcile(staff, in)
	if err != nil || !first.Credited || first.Subject != subject || first.AmountCents != 2500 || first.Reference != ref {
		t.Fatalf("first reconcile: %+v %v", first, err)
	}
	cash := func() int64 {
		f, err := funder.Funds(context.Background(), org, subject, network.SandboxOnly())
		if err != nil {
			t.Fatalf("funds: %v", err)
		}
		return f.Cash.Cents()
	}
	if got := cash(); got != 2500 {
		t.Fatalf("cash after the wire = %d, want 2500", got)
	}
	again, err := planeWireReconcile(staff, in)
	if err != nil || again.Credited {
		t.Fatalf("repeat: %+v %v, want no second credit", again, err)
	}
	if got := cash(); got != 2500 {
		t.Fatalf("cash after a repeat = %d, want 2500: one wire credited twice", got)
	}
	if _, err := planeWireReconcile(staff, &client.WireReconcileIn{Reference: ref, AmountCents: 9900, By: "z@hanzo.ai"}); statusOf(err) != 409 {
		t.Fatalf("a repeat for another amount: %v, want 409", err)
	}
	if _, err := planeWireReconcile(staff, &client.WireReconcileIn{Reference: ref, AmountCents: 2500}); statusOf(err) != 401 {
		t.Fatalf("a reconcile naming nobody: %v, want 401", err)
	}
}

// A TOKEN PAYS ONLY THE BUYER'S OWN RENEWAL: an invoice or a subscription that is not the
// buyer's, or does not exist, is 404 before the token is read. (Renewals themselves are
// driven by commerce's own live tests.)
func TestATokenForAnotherBuyersRenewalIsRefused(t *testing.T) {
	agented(t)
	ctx := tenantOf(fresh("tok"))
	if _, err := planeInvoiceToken(ctx, &client.AgentTokenIn{Subject: "ada", ID: "inv_nobody", Token: "spt_x"}); statusOf(err) != 404 {
		t.Fatalf("an invoice nobody holds: %v, want 404", err)
	}
	if _, err := planeSubscriptionToken(ctx, &client.AgentTokenIn{Subject: "ada", ID: "sub_nobody", Token: "spt_x"}); statusOf(err) != 404 {
		t.Fatalf("a subscription nobody holds: %v, want 404", err)
	}
}

// A STABLECOIN TOP-UP is refused, not faked, while the account cannot take stablecoins, and
// a bad amount is 400.
func TestACryptoTopupIsRefusedWhileStablecoinsAreOff(t *testing.T) {
	agented(t)
	ctx := tenantOf(fresh("crypto"))
	if _, err := planeTopupCrypto(ctx, &client.CryptoTopupIn{Subject: "ada", AmountCents: 1}); statusOf(err) != 400 {
		t.Fatalf("one cent: %v, want 400", err)
	}
	if _, err := planeTopupCrypto(ctx, &client.CryptoTopupIn{Subject: "ada", AmountCents: 1000, Network: "base"}); statusOf(err) != 503 {
		t.Fatalf("stablecoins off: %v, want 503", err)
	}
}

// WIRE STAYS HIDDEN UNTIL THE OWNER GIVES RECEIVING DETAILS: with none configured, the wire
// read is a plain 503 "not configured" (never an internal error, never half a form), and a
// checkout's route does not offer wire among its alternatives.
func TestWireIsHiddenWhileNoReceivingDetailsAreConfigured(t *testing.T) {
	agented(t)
	ctx := tenantOf(fresh("nowire"))
	_, err := planeWire(ctx, &client.WireIn{Host: "hanzo.ai", Payer: "ada"})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable || !strings.Contains(he.Error(), "wire transfer not configured") {
		t.Fatalf("the wire read with no details: %v, want a plain 503 \"wire transfer not configured\"", err)
	}
	for _, country := range []string{"US", "DE", "BR"} {
		r, err := planeRoute(ctx, &client.RouteIn{Subject: "ada", Country: country, AmountCents: 1000})
		if err != nil {
			// No processor is registered here, so there is no route at all, and so no
			// alternative to list: a refusal offers nothing.
			continue
		}
		for _, a := range r.Alternatives {
			if a == "wire" {
				t.Errorf("route %s offers wire with no receiving details: %v", country, r.Alternatives)
			}
		}
	}
}
