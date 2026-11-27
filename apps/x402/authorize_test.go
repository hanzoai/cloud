package x402

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	protocol "github.com/hanzoai/commerce/payment/x402"
)

// A payment agreed before it is made: checked against its resource's terms and
// held for that resource, its amount set aside in the payer's wallet, it cannot
// be spent on anything else first, and it settles its own resource once.
func TestAuthorizeHoldsANonceForOneResource(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		terms := Terms{Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryService}
		pubRegistry(map[string]Terms{client.JobResource("j1"): terms, "/paid/tool": terms})
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)

		// No payment: the terms to sign, and nothing else.
		out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1")})
		if err != nil || out.OK || out.Challenge == "" {
			t.Fatalf("authorize with no payment = %+v %v, want the challenge alone", out, err)
		}
		var required protocol.PaymentRequired
		if err := protocol.DecodeHeader(out.Challenge, &required); err != nil || required.Resource.URL != client.JobResource("j1") {
			t.Fatalf("challenge %+v %v, want one for job:j1", required, err)
		}
		req := required.Accepts[0]
		now := nowUnix()
		payment := h.pay(req, randNonce(), now-60, now+3600)

		// Valid now, but not through the moment it may still be settled.
		out, err = r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: payment, Until: now + 7200, Hold: true})
		if err != nil || out.OK || out.Status != http.StatusPaymentRequired || out.Code != protocol.ReasonValidBefore {
			t.Fatalf("authorize past its window = %+v %v, want refused on validBefore", out, err)
		}
		// Through it: verified and held.
		out, err = r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: payment, Until: now + 1800, Hold: true})
		if err != nil || !out.OK || out.PayTo != req.PayTo || out.ValidBefore != now+3600 || out.Network != req.Network {
			t.Fatalf("authorize = %+v %v, want OK with its terms", out, err)
		}
		h.aside(money.FromCents(100), "after an authorization that sets its amount aside")

		// The held payment spent on another resource first: refused, nothing moved.
		code, b, _ := h.req(http.MethodGet, "/paid/tool", h.payerOrg, payment, "")
		if code != http.StatusPaymentRequired || !bytes.Contains(b, []byte(reasonReplay)) {
			t.Fatalf("a held payment bought another resource: %d %s", code, b)
		}
		h.aside(money.FromCents(100), "after the held payment was refused elsewhere")
		// Another resource cannot hold it either.
		out, _ = r.planeAuthorize(as, &client.AuthorizationIn{Resource: "/paid/tool", Payment: payment, Hold: true})
		if out.OK || out.Code != reasonReplay {
			t.Fatalf("a second hold on another resource = %+v, want refused", out)
		}

		// Its own resource settles it, once.
		settled, err := r.planeSettle(as, &client.SettleIn{Resource: client.JobResource("j1"), Payment: payment})
		if err != nil || !settled.OK {
			t.Fatalf("settle the held payment = %+v %v", settled, err)
		}
		h.tie(money.FromCents(100), "after the held payment settled")
		if again, _ := r.planeSettle(as, &client.SettleIn{Resource: client.JobResource("j1"), Payment: payment}); !again.OK {
			t.Fatalf("settling again = %+v, want the same receipt", again)
		}
		h.tie(money.FromCents(100), "after settling the same payment again")
		out, _ = r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: payment})
		if out.OK || out.Code != reasonReplay || !out.Spent {
			t.Fatalf("authorizing a spent payment = %+v, want refused as spent", out)
		}
		// A resource nothing prices has nothing to authorize.
		if out, _ := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("nope")}); out.Status != http.StatusConflict {
			t.Fatalf("authorize an unpriced resource = %+v, want 409", out)
		}
	})
}

// An org reads its receipts as payer and as payee, by year, with what each paid
// for; another org reads none of them.
func TestSettlementsByRole(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		pubRegistry(map[string]Terms{
			"/paid/tool": {Amount: money.FromCents(100), RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryRoyalty},
		})
		req := h.challenge("/paid/tool", h.payerOrg)
		if code, b, _ := h.payFor("/paid/tool", h.payerOrg, req, randNonce()); code != http.StatusOK {
			t.Fatalf("pay = %d %s", code, b)
		}
		list := func(org, query string) []Receipt {
			t.Helper()
			code, b, _ := h.req(http.MethodGet, "/v1/x402/settlements"+query, org, "", "")
			if code != http.StatusOK {
				t.Fatalf("settlements%s as %s = %d %s", query, org, code, b)
			}
			var out struct {
				Settlements []Receipt `json:"settlements"`
			}
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			return out.Settlements
		}
		paid := list(h.payerOrg, "?role=payer")
		if len(paid) != 1 || paid[0].Resource != "/paid/tool" || paid[0].Category != client.CategoryRoyalty || paid[0].Amount != "1" {
			t.Fatalf("payer's receipts = %+v", paid)
		}
		if got := list(h.payeeOrg, "?role=payee"); len(got) != 1 || got[0].ID != paid[0].ID || got[0].PayeeOrg != h.payeeOrg {
			t.Fatalf("payee's receipts = %+v", got)
		}
		if got := list(h.payeeOrg, "?role=payer"); len(got) != 0 {
			t.Fatalf("the payee read the payer's receipts: %+v", got)
		}
		if got := list("strangerorg", "?role=payee"); len(got) != 0 {
			t.Fatalf("a stranger read receipts: %+v", got)
		}
		if got := list(h.payerOrg, "?role=payer&year=2001"); len(got) != 0 {
			t.Fatalf("a year with no payments answered %+v", got)
		}
		if code, _, _ := h.req(http.MethodGet, "/v1/x402/settlements?role=auditor", h.payerOrg, "", ""); code != http.StatusBadRequest {
			t.Fatalf("an unknown role = %d, want 400", code)
		}
		if code, _, _ := h.req(http.MethodGet, "/v1/x402/settlements", "", "", ""); code != http.StatusForbidden {
			t.Fatalf("an anonymous read = %d, want 403", code)
		}
	})
}

// aside asserts the payer's wallet no longer counts want, and the payee has been
// paid nothing: money set aside for a payment, not yet made.
func (h *harness) aside(want money.Amount, when string) {
	h.t.Helper()
	if got := h.debited(); got.Cmp(want) != 0 {
		h.t.Fatalf("%s: the payer's wallet is down %s, want %s set aside", when, got, want)
	}
	if got := h.credited(); got.Sign() != 0 {
		h.t.Fatalf("%s: the payee was credited %s before any payment", when, got)
	}
}

// A held payment is there when it is settled: the wallet cannot promise the same
// balance twice or spend it on anything else, the settlement is paid from what was
// set aside, and a void of a settled payment says spent and returns nothing. A
// held payment given up returns to the wallet once.
func TestAHeldPaymentIsThereWhenSettledOrVoided(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		all := Terms{Amount: funded, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryService}
		cent := all
		cent.Amount = money.FromCents(1)
		pubRegistry(map[string]Terms{client.JobResource("j1"): all, client.JobResource("j2"): cent, "/paid/tool": cent})
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		sign := func(resource string) string {
			t.Helper()
			out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: resource})
			if err != nil || out.Challenge == "" {
				t.Fatalf("challenge %s = %+v %v", resource, out, err)
			}
			var required protocol.PaymentRequired
			if err := protocol.DecodeHeader(out.Challenge, &required); err != nil {
				t.Fatal(err)
			}
			now := nowUnix()
			return h.pay(required.Accepts[0], randNonce(), now-60, now+3600)
		}
		j1 := sign(client.JobResource("j1"))
		if out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: j1, Hold: true}); err != nil || !out.OK {
			t.Fatalf("hold the whole wallet = %+v %v", out, err)
		}
		h.aside(funded, "after the whole wallet was set aside")
		// The same balance promised to a second job: refused, nothing more set aside.
		j2 := sign(client.JobResource("j2"))
		out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j2"), Payment: j2, Hold: true})
		if err != nil || out.OK || out.Status != http.StatusPaymentRequired || out.Code != protocol.ReasonInsufficientFunds {
			t.Fatalf("a hold of money already held = %+v %v, want 402 %s", out, err, protocol.ReasonInsufficientFunds)
		}
		// Nor spent on a call.
		req := h.challenge("/paid/tool", h.payerOrg)
		if code, b, _ := h.payFor("/paid/tool", h.payerOrg, req, randNonce()); code != http.StatusPaymentRequired || !bytes.Contains(b, []byte(protocol.ReasonInsufficientFunds)) {
			t.Fatalf("a call paid from held money = %d %s, want 402 %s", code, b, protocol.ReasonInsufficientFunds)
		}
		h.aside(funded, "after the held money was refused elsewhere")
		settled, err := r.planeSettle(as, &client.SettleIn{Resource: client.JobResource("j1"), Payment: j1})
		if err != nil || !settled.OK {
			t.Fatalf("settle the held payment = %+v %v", settled, err)
		}
		h.tie(funded, "after the held payment settled")
		if v, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j1"), Payment: j1}); err != nil || !v.Spent || v.Returned.Decimal != "0" {
			t.Fatalf("void of a settled payment = %+v %v, want spent and nothing returned", v, err)
		}
		h.tie(funded, "after a void of the settled payment")

		// A held payment given up returns once.
		if _, err := h.ledger.FinanceClient.Deposit(context.Background(), depositUSD(h.payerOrg, h.payerOrg, money.FromCents(1), "top-up")); err != nil {
			t.Fatal(err)
		}
		if out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j2"), Payment: j2, Hold: true}); err != nil || !out.OK {
			t.Fatalf("hold after the top-up = %+v %v", out, err)
		}
		if got := h.balance(h.payerOrg, h.payerOrg); got.Sign() != 0 {
			t.Fatalf("the payer holds %s, want the cent set aside", got)
		}
		v, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j2"), Payment: j2})
		if err != nil || v.Spent || v.Returned.Decimal != "0.01" {
			t.Fatalf("void = %+v %v, want the cent returned", v, err)
		}
		if again, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j2"), Payment: j2}); err != nil || again.Returned.Decimal != "0" {
			t.Fatalf("a second void = %+v %v, want nothing returned", again, err)
		}
		if got := h.balance(h.payerOrg, h.payerOrg); got.Cmp(money.FromCents(1)) != 0 {
			t.Fatalf("the payer holds %s after the void, want the cent back", got)
		}
		// A payment given up is spent on nothing, the resource it was held for included.
		if out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j2"), Payment: j2, Hold: true}); err != nil || out.OK || out.Code != reasonReplay {
			t.Fatalf("holding a payment given up = %+v %v, want refused as a replay", out, err)
		}
		// A void names the resource the payment is held for, and no other.
		if _, err := r.planeVoid(as, &client.VoidIn{Resource: "/paid/tool", Payment: j2}); err == nil {
			t.Fatal("a void of another resource's hold was answered")
		}
	})
}

// A void decides: an authorization given up before its hold arrived — the hold
// sent first and delivered after — sets nothing aside and settles nothing.
func TestAVoidOvertakenByItsHoldHoldsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		all := Terms{Amount: funded, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryService}
		pubRegistry(map[string]Terms{client.JobResource("j1"): all})
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1")})
		if err != nil || out.Challenge == "" {
			t.Fatalf("challenge = %+v %v", out, err)
		}
		var required protocol.PaymentRequired
		if err := protocol.DecodeHeader(out.Challenge, &required); err != nil {
			t.Fatal(err)
		}
		now := nowUnix()
		pay := h.pay(required.Accepts[0], randNonce(), now-60, now+3600)
		if v, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j1"), Payment: pay}); err != nil || v.Spent || v.Returned.Decimal != "0" {
			t.Fatalf("void of a payment never held = %+v %v, want nothing returned", v, err)
		}
		if out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: pay, Hold: true}); err != nil || out.OK || out.Code != reasonReplay {
			t.Fatalf("the hold arriving after its void = %+v %v, want refused as a replay", out, err)
		}
		if got := h.balance(h.payerOrg, h.payerOrg); got.Cmp(funded) != 0 {
			t.Fatalf("the payer holds %s, want all of %s: a given-up payment set money aside", got, funded)
		}
		if s, err := r.planeSettle(as, &client.SettleIn{Resource: client.JobResource("j1"), Payment: pay}); err == nil && s.OK {
			t.Fatalf("a given-up payment settled: %+v", s)
		}
		if got := h.balance(h.payerOrg, h.payerOrg); got.Cmp(funded) != 0 {
			t.Fatalf("the payer holds %s after settling a given-up payment, want all of %s", got, funded)
		}
	})
}

// A void gives up only an authorization its caller signed: a payment whose
// signature is not its payer's, naming someone's nonce, holds nothing and is
// given up nowhere, and the payer's own authorization with that nonce still holds.
func TestAVoidOfAForgedPaymentGivesNothingUp(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		all := Terms{Amount: funded, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryService}
		pubRegistry(map[string]Terms{client.JobResource("j1"): all})
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1")})
		if err != nil || out.Challenge == "" {
			t.Fatalf("challenge = %+v %v", out, err)
		}
		var required protocol.PaymentRequired
		if err := protocol.DecodeHeader(out.Challenge, &required); err != nil {
			t.Fatal(err)
		}
		now := nowUnix()
		genuine := h.pay(required.Accepts[0], randNonce(), now-60, now+3600)
		p, err := protocol.ParsePayment(genuine)
		if err != nil {
			t.Fatal(err)
		}
		sig := []byte(p.Payload.Signature)
		if sig[10] == '0' {
			sig[10] = '1'
		} else {
			sig[10] = '0'
		}
		p.Payload.Signature = string(sig)
		forged := encode(p)
		if v, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j1"), Payment: forged}); err != nil || v.Spent || v.Returned.Decimal != "0" {
			t.Fatalf("void of a forged payment = %+v %v, want nothing returned", v, err)
		}
		if out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: genuine, Hold: true}); err != nil || !out.OK {
			t.Fatalf("the payer's own authorization after a forged void = %+v %v, want it held", out, err)
		}
		h.aside(funded, "after the genuine authorization was held")
	})
}

// A held payment is given back however late its void comes: its row is kept past
// its authorization's expiry until it is settled or given up, so a void a day
// after the payee's wallet went away still finds it and returns the money. A void
// that cannot check a payment it never held — the payee does not resolve — is an
// outage the caller asks again, not an answer — until the authorization expires,
// when nothing can hold it any more.
func TestALateVoidStillGivesBackWhatIsHeld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		all := Terms{Amount: funded, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryService}
		pubRegistry(map[string]Terms{client.JobResource("j1"): all, client.JobResource("j2"): all})
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		terms := func(resource string) protocol.PaymentRequirements {
			t.Helper()
			out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: resource})
			if err != nil || out.Challenge == "" {
				t.Fatalf("challenge %s = %+v %v", resource, out, err)
			}
			var required protocol.PaymentRequired
			if err := protocol.DecodeHeader(out.Challenge, &required); err != nil {
				t.Fatal(err)
			}
			return required.Accepts[0]
		}
		now := nowUnix()
		pay := h.pay(terms(client.JobResource("j1")), randNonce(), now-60, now+3600)
		if out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: pay, Hold: true}); err != nil || !out.OK {
			t.Fatalf("hold = %+v %v", out, err)
		}
		never := h.pay(terms(client.JobResource("j2")), randNonce(), now-60, now+3*86400)
		lapsed := h.pay(terms(client.JobResource("j2")), randNonce(), now-60, now+3600)
		// A day past the authorization's expiry another hold sweeps the rows, and the
		// seller's payout wallet is gone.
		restore := nowUnix
		later := now + 3600 + 86400 + 120
		nowUnix = func() int64 { return later }
		t.Cleanup(func() { nowUnix = restore })
		other := h.pay(terms(client.JobResource("j2")), randNonce(), later-60, later+3600)
		_, _ = r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j2"), Payment: other, Hold: true})
		gone := all
		gone.RecipientWalletID = "wal_gone"
		pubRegistry(map[string]Terms{client.JobResource("j1"): gone, client.JobResource("j2"): gone})
		if v, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j1"), Payment: pay}); err != nil || v.Returned.Decimal == "0" {
			t.Fatalf("a late void of a held payment = %+v %v, want its amount back", v, err)
		}
		if _, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j2"), Payment: never}); err == nil {
			t.Fatal("a void that could not check a payment it never held answered as if it had")
		}
		// Once the authorization has expired nothing can hold it, so a void of one never
		// held is answered without the check, and a caller retrying it is not stuck
		// behind a payee that will never resolve.
		if v, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j2"), Payment: lapsed}); err != nil || v.Returned.Decimal != "0" {
			t.Fatalf("a void of an expired payment never held = %+v %v, want nothing returned and no outage", v, err)
		}
	})
}

// A hold the rail stopped waiting for can still reach the ledger after the
// payment was given up — a plane call that timed out is not a call that never
// lands. The void records its return at the ledger first, even of nothing, so the
// hold that lands after it moves nothing: the payer keeps all of its money, where
// the hold used to set it aside for a payment nobody would ever return.
func TestAHoldLandingAfterItsVoidSetsNothingAside(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newHarness(t)
		all := Terms{Amount: funded, RecipientOrg: h.payeeOrg, RecipientWalletID: h.recipient, Category: client.CategoryService}
		pubRegistry(map[string]Terms{client.JobResource("j1"): all})
		r := rail{s: live}
		as := cloud.For(context.Background(), h.payerOrg)
		out, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1")})
		if err != nil || out.Challenge == "" {
			t.Fatalf("challenge = %+v %v", out, err)
		}
		var required protocol.PaymentRequired
		if err := protocol.DecodeHeader(out.Challenge, &required); err != nil {
			t.Fatal(err)
		}
		now := nowUnix()
		pay := h.pay(required.Accepts[0], randNonce(), now-60, now+3600)
		ledger := finance.Current().(*halfDown)
		ledger.mu.Lock()
		ledger.slow = true
		ledger.mu.Unlock()
		if _, err := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: pay, Hold: true}); err == nil {
			t.Fatal("a hold whose answer never came answered")
		}
		if v, err := r.planeVoid(as, &client.VoidIn{Resource: client.JobResource("j1"), Payment: pay}); err != nil || v.Spent || v.Returned.Decimal != "0" {
			t.Fatalf("void before the hold landed = %+v %v, want nothing returned yet", v, err)
		}
		for _, err := range ledger.land(context.Background()) {
			if !errors.Is(err, finance.ErrReturned) {
				t.Fatalf("the hold landing after its void: %v, want refused as returned", err)
			}
		}
		h.aside(money.Zero(), "after a hold landed behind its void")
		if out, _ := r.planeAuthorize(as, &client.AuthorizationIn{Resource: client.JobResource("j1"), Payment: pay, Hold: true}); out.OK {
			t.Fatalf("the given-up payment held again = %+v", out)
		}
		h.aside(money.Zero(), "after the given-up payment was asked to hold again")
	})
}
