// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// plane_topup_test.go — the card endpoints as production reaches them: the billing
// endpoint in its own process, commerce's ops behind commerce's plane socket.
//
// Over the socket zip's call handler serves the op and no Bridge binds a request,
// so cloud.Request is absent inside the op. These tests drive that exact shape —
// a billing-shaped handler resolving the payer from a real request, then a real
// socket round trip — and assert on the two things that decide whether a customer
// was treated honestly: what Square charged, read back from Square's sandbox, and
// which wallet the balance check reads afterwards.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	commercemod "github.com/hanzoai/commerce"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
)

// judged is what the risk screen was asked about: the ledger whose model judged the
// payment and the payer inside it.
type judged struct {
	mu      sync.Mutex
	ledger  string
	subject string
}

func (j *judged) read() (string, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ledger, j.subject
}

// plane is commerce as the billing endpoint reaches it in production: the embedded
// money core charging Square's sandbox, the card ops registered exactly as Mount
// registers them, and the plane served on commerce's own socket. It answers the
// ledger the balance check reads, Square, when the test began, and what the screen
// was asked.
type rig struct {
	fin    money.FinanceClient
	sq     sandboxSquareAPI
	began  time.Time
	screen *judged
}

func servePlane(t *testing.T) rig {
	t.Helper()
	// The sandbox merchant's variables, set before commerce is constructed.
	sq := sandbox(t)
	shortRuntimeDir(t)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	emb, err := commercemod.Embed(context.Background(), commercemod.EmbedConfig{DataDir: t.TempDir(), Dev: true, Ledger: ledger{}})
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

	screen := &judged{}
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(func(_ context.Context, org string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		screen.mu.Lock()
		screen.ledger, screen.subject = org, q.Subject.ID
		screen.mu.Unlock()
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})

	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	exposeSale(riskGate(luxlog.New("planetest")))
	stop, err := cloud.ServePlane(commercepeer.App, luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve the commerce plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	// AFTER every fixture above: the drain has to run before the plane is unbound.
	quiet(t)
	return rig{fin: fin, sq: sq, began: time.Now(), screen: screen}
}

// dial calls one commerce op over the plane SOCKET. It dials the socket directly
// rather than through plane.Call, which would find commerce served in this process
// and run the op in memory: the socket is the arrangement production has.
func dial[In, Out any](ctx context.Context, t *testing.T, op string, in *In) (*Out, error) {
	t.Helper()
	conn, err := zip.DialApp(commercepeer.App)
	if err != nil {
		t.Fatalf("dial commerce: %v", err)
	}
	defer func() { _ = conn.Close() }()
	return zip.Call[In, Out](ctx, conn, op, in)
}

// hop is how a billing-shaped handler reaches commerce.
type hop func(ctx context.Context, in *client.CardIn) (*client.Charged, error)

// socket reaches commerce over its plane socket, the production arrangement.
func socket(t *testing.T) hop {
	return func(ctx context.Context, in *client.CardIn) (*client.Charged, error) {
		return dial[client.CardIn, client.Charged](ctx, t, client.BillingTopupCard, in)
	}
}

// memory reaches commerce in this process through zip.Here with the caller's own
// request still bound — the arrangement where billing and commerce share a process.
func memory() hop {
	return func(ctx context.Context, in *client.CardIn) (*client.Charged, error) {
		return zip.Here[client.CardIn, client.Charged](ctx, cloud.Plane(), client.BillingTopupCard, in)
	}
}

// billingTopup is the browser endpoint as apps/billing serves it (typed.go
// topupToken): the payer resolved from the request by principal.Org and
// principal.PayerIn, the org stated with cloud.For, the retry key off the header,
// and the charge asked of commerce by name.
func billingTopup(t *testing.T, reach hop) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("billingshape"), DisableStartupMessage: true})
	app.Use(zip.H(cloud.Bridge()))
	app.Raw(http.MethodPost, "/v1/billing/topup/token", func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrUnauthorized("sign in to view billing")
		}
		out, err := reach(cloud.For(c.Forward(), org), &client.CardIn{
			SourceID:       "cnon:card-nonce-ok",
			AmountCents:    gateCents,
			Currency:       "usd",
			Subject:        meter.PayerIn(c, org).Subject(),
			IdempotencyKey: strings.TrimSpace(c.Header("X-Idempotency-Key")),
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, out)
	})
	return app
}

// browse posts a top-up as the caller the headers describe and answers the status and body.
func browse(t *testing.T, app *zip.App, hdr map[string]string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/billing/topup/token", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("post the top-up: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// balance reads what the balance check reads for one wallet in the TEST books, where
// a test-mode org's sandbox charge is credited. It fails t unless the live books hold
// nothing for the wallet: a sandbox charge never funds a spendable balance.
func balance(t *testing.T, fin money.FinanceClient, ledger, subject string) int64 {
	t.Helper()
	read := func(test bool) int64 {
		bal, err := fin.Balance(context.Background(), ledger, subject, "usd", test)
		if err != nil {
			t.Fatalf("read %s/%s (test=%t): %v", ledger, subject, test, err)
		}
		return bal.Cents()
	}
	if live := read(false); live != 0 {
		t.Errorf("%s/%s holds %d cents in the live books, want 0 — a sandbox charge funded a spendable balance", ledger, subject, live)
	}
	return read(true)
}

// chargedRef is the processor reference a top-up answered with, Square's payment id.
// It fails t unless the answer says the charge was a test one, as a test-mode org's is.
func chargedRef(t *testing.T, body string) string {
	t.Helper()
	var out client.Charged
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("read the top-up's answer %q: %v", clip(body), err)
	}
	if out.ProcessorRef == "" {
		t.Fatalf("the top-up answered no processor reference: %s", clip(body))
	}
	if !out.Test {
		t.Fatalf("the top-up answered test=false for a test-mode org's sandbox charge: %s", clip(body))
	}
	return out.ProcessorRef
}

// TestPlane_ATopUpOverTheSocketCreditsTheCallersWallet is the production defect.
//
// The billing endpoint resolves the payer from its request and asks commerce over the
// plane socket. Commerce's op sees no bound request there, and the screen used to
// resolve the payment ONLY from one: it built a payment naming nobody, let it through,
// the core charged the card, and the deposit refused "the charge settled and it cannot
// be credited to this balance". Square showed the money taken; the balance never moved.
//
// The caller here carries a signed billing_account claim naming a PERSON in a
// dedicated org, which is the case where re-deriving the payer on the plane would go
// wrong: the claim rides a header zip does not forward, and the claim-less rule
// answers the org pool. The credit must land on the wallet the claim names, which is
// the wallet the balance check reads for this caller.
//
// Mutation proof: restore the request-only resolution in [screened] and this answers
// 500 with Square's payment taken and nothing credited. Re-derive the subject on the
// plane with principal.PayerFrom and the $42 lands in the org pool while the person's
// wallet reads 0.
func TestPlane_ATopUpOverTheSocketCreditsTheCallersWallet(t *testing.T) {
	pl := servePlane(t)
	app := billingTopup(t, socket(t))
	org := fresh("acme")
	bob := org + "/bob"

	code, body := browse(t, app, map[string]string{
		"X-Org-Id":             org,
		"X-User-Id":            "u_412",
		"X-User-Name":          "bob",
		"X-Billing-Account-Id": "person:" + bob,
		"X-Idempotency-Key":    fresh("plane-topup"),
	})
	if code != http.StatusOK {
		t.Fatalf("the top-up over the plane answered %d %s, want 200", code, body)
	}
	ref := chargedRef(t, body)
	pl.sq.settled(t, ref, gateCents, pl.began)
	// Every payment naming this test's org, the person's wallet and the org pool alike.
	got := pl.sq.exactly(t, pl.began, 1, "naming "+org, naming(org))
	if got[0].ID != ref || !strings.HasSuffix(got[0].Note, " "+bob) {
		t.Fatalf("Square lists payment %s (%q) for %s, want %s, the top-up of %s", got[0].ID, got[0].Note, org, ref, bob)
	}
	if got := balance(t, pl.fin, org, bob); got != gateCents {
		t.Fatalf("the wallet the claim names (%s) holds %d cents after a settled $42 top-up, want %d — "+
			"the card was charged and the balance this caller is billed against never moved", bob, got, gateCents)
	}
	if got := balance(t, pl.fin, org, org); got != 0 {
		t.Errorf("the %s org pool holds %d cents, want 0 — the credit landed on the claim-less payer "+
			"rather than on the wallet the caller's own credential names", org, got)
	}
	// The screen judged the payer that was credited, in the ledger that holds it.
	if ledger, subject := pl.screen.read(); ledger != org || subject != bob {
		t.Errorf("the screen judged %q/%q, want %s/%s — the model judged a payer other than the one credited",
			ledger, subject, org, bob)
	}
}

// TestPlane_AnUnresolvedPayerIsRefusedBeforeTheCardIsCharged — fail closed before money
// moves.
//
// Every row is a plane call whose payment names no wallet this process can credit,
// carrying a nonce Square would accept. Square listing no payment for any row's payer
// is the assertion that matters: a refusal AFTER the charge is the stranded payment
// again, and at the status code it looks the same as one before it.
//
// The row that proves the ordering is the org with no validated user: commerce's own
// tenancy rule (callerOrg) admits a stated org with no user, so the core WOULD charge
// it, while the payer rule refuses it. Before this change that caller's card was taken
// and the deposit refused as terminal.
//
// Mutation proof: delete the [payment.named] refusal from [screen.decide] and that row
// charges the card on Square and answers 500.
func TestPlane_AnUnresolvedPayerIsRefusedBeforeTheCardIsCharged(t *testing.T) {
	sq, began := sandbox(t), time.Now()
	org := fresh("acme")
	other := fresh("globex")
	rows := []struct {
		name    string
		caller  *zip.Caller
		subject string
	}{
		{name: "no caller at all", subject: org},
		{name: "an org with no validated user", caller: &zip.Caller{Org: org}, subject: org},
		{name: "a wallet in another org's books", caller: &zip.Caller{Org: org, User: "u_412"}, subject: other + "/bob"},
		{name: "a wallet spelled outside the payer rule", caller: &zip.Caller{Org: org, User: "u_412"}, subject: strings.ToUpper(org) + "/Bob"},
		{name: "no wallet", caller: &zip.Caller{Org: org, User: "u_412"}, subject: ""},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			pl := servePlane(t)
			ctx := context.Background()
			if tc.caller != nil {
				ctx = zip.WithCaller(ctx, *tc.caller)
			}
			out, err := dial[client.CardIn, client.Charged](ctx, t, client.BillingTopupCard, &client.CardIn{
				SourceID:       "cnon:card-nonce-ok",
				AmountCents:    gateCents,
				Currency:       "usd",
				Subject:        tc.subject,
				IdempotencyKey: fresh("plane-refused-" + strings.ReplaceAll(tc.name, " ", "-")),
			})
			if err == nil {
				t.Fatalf("a payment with no wallet to credit answered %+v, want a refusal", out)
			}
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusConflict {
				t.Errorf("the refusal is %v, want 409 from the screen", err)
			}
			for _, w := range []struct{ ledger, subject string }{{org, org}, {org, tc.subject}} {
				if w.subject == "" {
					continue
				}
				if got := balance(t, pl.fin, w.ledger, w.subject); got != 0 {
					t.Errorf("%s/%s holds %d cents after a refused payment, want 0", w.ledger, w.subject, got)
				}
			}
		})
	}
	// Every row's payer is named by org or other, in whatever case the row spelled it.
	sq.exactly(t, began, 0, "naming "+org+" or "+other, func(p squarePayment) bool {
		return naming(org)(p) || naming(other)(p)
	})
}

// TestPlane_TheBrowserAndThePlaneCreditOneWallet — the same caller, reached both ways,
// is credited at one address.
//
// With billing and commerce in one process the op runs through zip.Here with the
// browser's request still bound, and the payment is read off that request. In
// production they are two processes and the op sees only what crossed the socket. A
// top-up that credited a different wallet depending on the deployment shape is money
// as a property of the transport, so both shapes are driven for one caller and must
// land on the wallet the balance check reads for that caller.
//
// Mutation proof: drop the subject from the plane shape and the socket half answers
// 409 with nothing credited; restore the request-only resolution and it answers 500
// with the card charged.
func TestPlane_TheBrowserAndThePlaneCreditOneWallet(t *testing.T) {
	pl := servePlane(t)
	org := fresh(gateOrg)
	caller := map[string]string{"X-Org-Id": org, "X-User-Id": gateUser}
	want := wallet(org)

	refs := map[string]bool{}
	for i, reach := range []struct {
		name string
		hop  hop
	}{{"in memory with the request bound", memory()}, {"over the plane socket", socket(t)}} {
		hdr := map[string]string{"X-Idempotency-Key": fresh(fmt.Sprintf("one-wallet-%d", i))}
		for k, v := range caller {
			hdr[k] = v
		}
		code, body := browse(t, billingTopup(t, reach.hop), hdr)
		if code != http.StatusOK {
			t.Fatalf("%s: the top-up answered %d %s, want 200", reach.name, code, body)
		}
		ref := chargedRef(t, body)
		pl.sq.settled(t, ref, gateCents, pl.began)
		refs[ref] = true
		if got := balance(t, pl.fin, org, want); got != gateCents*int64(i+1) {
			t.Fatalf("%s: the wallet the balance check reads (%s/%s) holds %d cents, want %d",
				reach.name, org, want, got, gateCents*int64(i+1))
		}
		if ledger, subject := pl.screen.read(); ledger != org || subject != want {
			t.Errorf("%s: the screen judged %q/%q, want %q/%q", reach.name, ledger, subject, org, want)
		}
	}
	if len(refs) != 2 {
		t.Fatalf("the two top-ups answered %d distinct payment(s), want 2", len(refs))
	}
	for _, p := range pl.sq.exactly(t, pl.began, 2, "naming "+org, naming(org)) {
		if !refs[p.ID] || !strings.HasSuffix(p.Note, " "+want) {
			t.Errorf("Square lists payment %s (%q), which neither answer named as a top-up of %s", p.ID, p.Note, want)
		}
	}
}

// TestPlane_AMasqueradingSuperAdminIsRefusedOnTheSocketAsInTheBrowser — the ledger
// rule crosses the plane with the org.
//
// A SuperAdmin acting inside a customer's org pays from its own books, so its card
// payment names two organisations and is refused before the card is charged. The
// forwarded caller carries the owner and the admin bit, and the plane shape reads the
// ledger through principal.PayerFrom, so the socket refuses exactly as the browser
// does.
//
// Mutation proof: set the plane ledger to the org instead of principal.PayerFrom and
// this charges the customer's card into the admin's own balance.
func TestPlane_AMasqueradingSuperAdminIsRefusedOnTheSocketAsInTheBrowser(t *testing.T) {
	pl := servePlane(t)
	org := fresh(gateOrg)
	out, err := dial[client.CardIn, client.Charged](zip.WithCaller(context.Background(), zip.Caller{
		Org: org, User: "operator", Owner: adminOrg, Admin: true,
	}), t, client.BillingTopupCard, &client.CardIn{
		SourceID: "cnon:card-nonce-ok", AmountCents: gateCents, Currency: "usd",
		Subject: wallet(org), IdempotencyKey: fresh("masquerade"),
	})
	var he *zip.HTTPError
	if err == nil || !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("a masqueraded top-up over the plane answered %+v, %v — want 409 before the charge", out, err)
	}
	if !strings.Contains(he.Error(), "/v1/admin/grants") {
		t.Errorf("the refusal %q does not name the endpoint that can fund another organisation", he.Error())
	}
	pl.sq.exactly(t, pl.began, 0, "naming "+org, naming(org))
}

// TestPlane_ASubscriptionOverTheSocketIsFiledUnderTheCallersOrg — the sale half.
//
// A plan sale credits no balance, but its screen judged a payment naming nobody over
// the plane: the model of no organisation, a sale observed under no tenant. It now
// judges the caller's ledger and payer, and the subscription it buys is filed in the
// caller's org, readable there and nowhere else.
func TestPlane_ASubscriptionOverTheSocketIsFiledUnderTheCallersOrg(t *testing.T) {
	pl := servePlane(t)
	org := fresh("planeco")
	buyer := zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u_9", Name: "u_9"})
	payer := wallet(org)

	sold, err := dial[client.SaleIn, client.Sold](buyer, t, client.BillingSubscribe, &client.SaleIn{
		SourceID: "cnon:card-nonce-ok", PlanID: "pro-3x", Quantity: 1, Subject: payer,
		Currency: "usd", Email: "u_9@planeco.test", IdempotencyKey: fresh("plane-sale"),
	})
	if err != nil {
		t.Fatalf("a plan sale over the plane answered %v", err)
	}
	if sold == nil || sold.Sale == nil || sold.Sale.SubscriptionID == "" || sold.Sale.AmountCents <= 0 {
		t.Fatalf("the sale answered %+v, want a paid subscription", sold)
	}
	if sold.Sale.AmountCents != 1500 {
		t.Errorf("the sale charged %d cents, want Pro's 1500", sold.Sale.AmountCents)
	}
	pl.sq.soldOnce(t, org, sold.Sale.InvoiceID, sold.Sale.AmountCents, pl.began)
	if ledger, subject := pl.screen.read(); ledger != org || subject != payer {
		t.Errorf("the screen judged %q/%q, want %q/%q", ledger, subject, org, payer)
	}

	mine, err := planeSubscriptions(buyer, &client.SubsIn{})
	if err != nil {
		t.Fatalf("list the buyer's subscriptions: %v", err)
	}
	if !holds(mine, sold.Sale.SubscriptionID) {
		t.Errorf("the buyer's org does not hold subscription %s: %+v", sold.Sale.SubscriptionID, mine.Rows)
	}
	theirs, err := planeSubscriptions(zip.WithCaller(context.Background(), zip.Caller{Org: fresh("otherco"), User: "u_1"}),
		&client.SubsIn{})
	if err != nil {
		t.Fatalf("list another org's subscriptions: %v", err)
	}
	if holds(theirs, sold.Sale.SubscriptionID) {
		t.Errorf("subscription %s is filed where another org reads it", sold.Sale.SubscriptionID)
	}
}

func holds(s *client.Subscriptions, id string) bool {
	for _, r := range s.Rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestWithin_AcceptsOnlyAnAccountInTheLedger pins the subject bound the plane shape
// applies to a stated wallet.
func TestWithin_AcceptsOnlyAnAccountInTheLedger(t *testing.T) {
	for _, tc := range []struct {
		ledger, subject string
		want            bool
	}{
		{"acme", "acme", true},
		{"acme", "acme/bob", true},
		{"Acme", "acme/bob", true},
		{"hanzo", "hanzo/dana", true},
		{"acme", "", false},
		{"", "acme", false},
		{"acme", "globex", false},
		{"acme", "globex/bob", false},
		{"acme", "acme/", false},
		{"acme", "/bob", false},
		{"acme", "acme/bob/eve", false},
		{"acme", "ACME/bob", false},
		{"acme", " acme/bob", false},
		{"acme", "acmex/bob", false},
	} {
		if got := within(tc.ledger, tc.subject); got != tc.want {
			t.Errorf("within(%q, %q) = %t, want %t", tc.ledger, tc.subject, got, tc.want)
		}
	}
}
