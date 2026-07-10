package giftcard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	giftcardModel "github.com/hanzoai/cloud/clients/commerce/models/giftcard"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// ctxFor builds a gin test context wired so middleware.GetOrganization(c) +
// org.Namespaced(c) resolve to the ae SQLite datastore in org `ns`'s namespace
// — the exact production plumbing (org name IS the namespace). The caller is an
// ADMIN, since redeem/void are admin-gated money moves.
func ctxFor(w http.ResponseWriter, ns string) *gin.Context {
	return ctxForAs(w, ns, true)
}

// ctxForAs is ctxFor with an explicit admin flag. It injects the verified IAM
// claim the gateway/EdgeAuth would mint so middleware.RequireAdmin authorizes
// (admin=true) or rejects (admin=false) the money action.
func ctxForAs(w http.ResponseWriter, ns string, admin bool) *gin.Context {
	c, _ := gin.CreateTestContext(w)
	org := &organization.Organization{}
	org.Name = ns
	c.Set("organization", org)
	c.Set("context", nscontext.WithNamespace(context.Background(), ns))
	c.Set("iam_authenticated", true)
	c.Set("iam_claims", &auth.IAMClaims{Owner: ns, IsAdmin: admin})
	return c
}

// issueCard seeds a gift card in org `ns`.
func issueCard(t *testing.T, base context.Context, ns, code string, cents int64) *giftcardModel.GiftCard {
	t.Helper()
	db := datastore.New(nscontext.WithNamespace(base, ns))
	g := giftcardModel.New(db)
	g.Code = code
	g.InitialBalanceCents = currency.Cents(cents)
	g.Currency = "usd"
	if err := g.Create(); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	return g
}

// TestRedeem_HTTP_IdempotentReplay proves the money path over the wire: two
// POSTs with the same idempotency key return 200 and debit exactly once.
func TestRedeem_HTTP_IdempotentReplay(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	g := issueCard(t, context.Background(), "acme", "GIFT-HTTP", 5000)

	body, _ := json.Marshal(redeemRequest{AmountCents: 1500, Currency: "usd", IdempotencyKey: "http_idem_1"})

	do := func() (int, redeemResponse) {
		w := httptest.NewRecorder()
		c := ctxFor(w, "acme")
		c.Params = gin.Params{{Key: "giftcardid", Value: g.Id()}}
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/giftcard/"+g.Id()+"/redeem", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		Redeem(c)
		var resp redeemResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp
	}

	code1, resp1 := do()
	if code1 != 200 {
		t.Fatalf("redeem 1 status = %d", code1)
	}
	if resp1.BalanceCents != 3500 {
		t.Fatalf("balance after redeem 1 = %d, want 3500", resp1.BalanceCents)
	}

	code2, resp2 := do()
	if code2 != 200 {
		t.Fatalf("redeem replay status = %d", code2)
	}
	if resp2.BalanceCents != 3500 {
		t.Fatalf("balance after replay = %d, want 3500 (idempotent — no second debit)", resp2.BalanceCents)
	}
	if resp1.Redemption.Id() != resp2.Redemption.Id() {
		t.Fatalf("replay produced a different redemption: %s vs %s", resp1.Redemption.Id(), resp2.Redemption.Id())
	}
}

// TestRedeem_HTTP_InsufficientFunds proves over-redeem maps to 402, not 500.
func TestRedeem_HTTP_InsufficientFunds(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	g := issueCard(t, context.Background(), "acme", "GIFT-HTTP-OVER", 1000)

	body, _ := json.Marshal(redeemRequest{AmountCents: 5000, Currency: "usd", IdempotencyKey: "http_over"})
	w := httptest.NewRecorder()
	c := ctxFor(w, "acme")
	c.Params = gin.Params{{Key: "giftcardid", Value: g.Id()}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/giftcard/"+g.Id()+"/redeem", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	Redeem(c)

	if w.Code != 402 {
		t.Fatalf("over-redeem status = %d, want 402; body=%s", w.Code, w.Body.String())
	}
}

// TestRedeem_Void_NonAdmin_403 proves the HIGH-4 fix: the gift-card money
// subroutes (redeem, void) — which util/rest.Route did NOT wrap with the auth
// middleware and TokenRequired(Admin) no-ops on the IAM path — now reject a
// non-admin caller with 403, BEFORE any card mutation. A merchant staffer with a
// valid non-admin session must not be able to drain gift cards.
func TestRedeem_Void_NonAdmin_403(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	g := issueCard(t, context.Background(), "acme", "GIFT-NOADMIN", 5000)

	cases := []struct {
		name    string
		handler func(*gin.Context)
		body    []byte
	}{
		{"redeem", Redeem, mustJSON(redeemRequest{AmountCents: 100, Currency: "usd", IdempotencyKey: "na"})},
		{"void", Void, mustJSON(voidRequest{RedemptionId: "any"})},
	}
	for _, tcase := range cases {
		t.Run(tcase.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c := ctxForAs(w, "acme", false) // authenticated, NOT admin
			c.Params = gin.Params{{Key: "giftcardid", Value: g.Id()}}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/commerce/giftcard/"+g.Id()+"/"+tcase.name, bytes.NewReader(tcase.body))
			c.Request.Header.Set("Content-Type", "application/json")
			tcase.handler(c)
			if w.Code != 403 {
				t.Fatalf("%s as non-admin = %d, want 403 (money subroute must gate admin); body=%s", tcase.name, w.Code, w.Body.String())
			}
		})
	}

	// Balance must be untouched — the gate fired before any redemption.
	adb := datastore.New(nscontext.WithNamespace(context.Background(), "acme"))
	after := giftcardModel.New(adb)
	if err := after.GetById(g.Id()); err != nil {
		t.Fatalf("reload card: %v", err)
	}
	bal, err := giftcardModel.BalanceCents(adb, after)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal != currency.Cents(5000) {
		t.Fatalf("balance = %d after refused non-admin redeem, want 5000 (no debit)", bal)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// TestRedeem_HTTP_CrossTenant404 proves a card in org acme is a 404 for org beta.
func TestRedeem_HTTP_CrossTenant404(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()
	gin.SetMode(gin.TestMode)

	g := issueCard(t, context.Background(), "acme", "GIFT-HTTP-ISO", 5000)

	body, _ := json.Marshal(redeemRequest{AmountCents: 100, Currency: "usd", IdempotencyKey: "http_iso"})
	w := httptest.NewRecorder()
	c := ctxFor(w, "beta") // caller is org beta, card belongs to acme
	c.Params = gin.Params{{Key: "giftcardid", Value: g.Id()}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/giftcard/"+g.Id()+"/redeem", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	Redeem(c)

	if w.Code != 404 {
		t.Fatalf("cross-tenant redeem status = %d, want 404 (tenant isolation); body=%s", w.Code, w.Body.String())
	}
}
