package marketplace

// payments_test.go proves the pay-per-use client END TO END, over HTTP, with money
// actually moving: publish a monetized listing → a buyer installs it → calling the
// tool is refused with a real x402 challenge → the buyer signs an ERC-3009
// authorization over exactly those terms → the call runs and the ledger ties.
//
// Nothing here is faked below the client. The registry is the marketplace's own, the
// charger is the marketplace's own, the settlement is x402's, the payee wallet is a
// real KMS wallet in the wallets subsystem, and the ledger is a real finance
// ledger. The only stand-in is the TOOL, which does not participate in payment.

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	protocol "github.com/hanzoai/commerce/payment/x402"
)

// ── the proofs ────────────────────────────────────────────────────────────────

// TestPricedToolChallengedThenSettles is the whole defect, closed:
//
//	(a) a priced tool called without payment is refused with the x402 CHALLENGE —
//	    not ErrChargerUnset, which was a permanently unpayable 402 with no terms in
//	    it and therefore no way for any client to proceed;
//	(b) the same call, paid, runs — and the ledger entries tie EXACTLY. The price
//	    is a quarter of a cent, which is zero in every cents-typed field this path
//	    used to run through, so "0.0025 charged" and "0.0025 credited" is the
//	    assertion that a sub-cent price is a real price.
func TestPricedToolChallengedThenSettles(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const (
			seller = platform
			buyer  = "buyerorg"
			tool   = "conn_premium"
		)
		m := newEnv(t, tool)
		payee := m.createWallet(seller)

		price, err := money.ParseUSD("0.0025")
		if err != nil {
			t.Fatalf("parse price: %v", err)
		}
		m.shelve(publishReq{
			Tool: tool, Title: "Premium", Price: "0.0025", Currency: "USD",
			Recipient: payee, Public: true,
		})
		m.install(buyer, tool)
		m.fund(buyer, money.FromCents(100))

		before := m.balance(buyer, buyer)

		// (a) UNPAID → 402 carrying the terms to pay.
		code, body, hdr := m.call(buyer, tool, "")
		if code != http.StatusPaymentRequired {
			t.Fatalf("unpaid priced call = %d (%s), want 402", code, body)
		}
		if bytes.Contains(body, []byte("payment client not configured")) {
			t.Fatalf("still fails closed on an unwired charger, not an x402 challenge: %s", body)
		}
		req := m.challengeOf(hdr)
		if want := client.ToolResource("connector", tool); m.resource != want {
			t.Fatalf("challenge names %q, want the tool resource %q", m.resource, want)
		}
		if req.PayTo == "" {
			t.Fatal("challenge names no payee address — nothing to pay")
		}
		// $0.0025 at USDC's 6 decimals is 2500 smallest units. A cents-typed path would
		// have offered 0 here and the client would have signed away nothing.
		if req.Amount != "2500" {
			t.Fatalf("challenge amount = %q, want 2500 (0.0025 USD at 6dp)", req.Amount)
		}
		if got := m.balance(buyer, buyer); got.Cmp(before) != 0 {
			t.Fatalf("a refused call moved money: %s → %s", before.String(), got.String())
		}

		// (b) PAID → served, and both sides of the ledger tie exactly.
		code, body, hdr = m.call(buyer, tool, m.pay(req, 5*time.Minute))
		if code != http.StatusOK {
			t.Fatalf("paid call = %d (%s), want 200", code, body)
		}
		if !bytes.Contains(body, []byte(`"ran":true`)) {
			t.Fatalf("tool did not run after payment: %s", body)
		}
		if hdr.Get(protocol.HeaderPaymentResponse) == "" {
			t.Fatalf("paid call carried no %s receipt", protocol.HeaderPaymentResponse)
		}

		debited := before.Sub(m.balance(buyer, buyer))
		if debited.Cmp(price) != 0 {
			t.Fatalf("payer debited %s, want exactly %s", debited.String(), price.String())
		}
		credited := m.balance(seller, payee)
		if credited.Cmp(price) != 0 {
			t.Fatalf("seller credited %s, want exactly %s", credited.String(), price.String())
		}
		if credited.IsZero() {
			t.Fatal("a sub-cent price rounded to zero — the money vanished")
		}
		// And the payment is an economic event between the two orgs, at the price, once
		// the rail's drainer has stated it.
		if _, err := x402.Announce(context.Background()); err != nil {
			t.Fatalf("state the settlement: %v", err)
		}
		if got := m.events.All(); len(got) != 1 || got[0].Buyer != buyer || got[0].Seller != seller ||
			got[0].Rail != client.RailX402 || got[0].Gross != price.String() || got[0].FMV != price.String() ||
			got[0].Category != client.CategoryService {
			t.Fatalf("stated %+v, want one x402 payment for a service of %s from %s to %s", got, price, buyer, seller)
		}
	})
}

// TestUnpricedToolUnaffected: closing the client must not put a toll on anything that
// was free. An unlisted tool, and a listed-but-free one, both dispatch with no
// challenge and no ledger movement — even though the charger is wired and consulted.
func TestUnpricedToolUnaffected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const buyer = "buyerorg"
		m := newEnv(t, "conn_free", "conn_unlisted")

		m.shelve(publishReq{Tool: "conn_free", Title: "Free", Public: true})
		m.install(buyer, "conn_free")
		m.install(buyer, "conn_unlisted")
		m.fund(buyer, money.FromCents(100))
		before := m.balance(buyer, buyer)

		for _, tool := range []string{"conn_free", "conn_unlisted"} {
			code, body, hdr := m.call(buyer, tool, "")
			if code != http.StatusOK {
				t.Fatalf("%s = %d (%s), want 200 — an unpriced tool is not for sale", tool, code, body)
			}
			if hdr.Get(protocol.HeaderPaymentRequired) != "" {
				t.Fatalf("%s was challenged for payment it does not cost", tool)
			}
		}
		if got := m.balance(buyer, buyer); got.Cmp(before) != 0 {
			t.Fatalf("free calls moved money: %s → %s", before.String(), got.String())
		}
	})
}

// TestCrossOrgCreditImpossible: a buyer cannot redirect a credit, and a seller
// cannot be paid into a wallet outside its own org.
//
// The payee org on the Terms is the LISTING ROW's publisher, never anything on the
// wire, and wallets resolves an id only within the org it is asked for — so a
// listing naming a wallet that exists in another org is refused when it is
// published or edited, before any buyer can meet it, and the seller's own wallet
// is the only one a call ever pays.
func TestCrossOrgCreditImpossible(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const (
			seller  = platform
			buyer   = "buyerorg"
			outside = "victimorg"
			tool    = "conn_premium"
		)
		m := newEnv(t, tool)
		payee := m.createWallet(seller)
		victimWallet := m.createWallet(outside)

		// The seller lists its tool but names the VICTIM's wallet as the payee.
		bad := publishReq{Tool: tool, Title: "Premium", Price: "1.00", Currency: "USD", Recipient: victimWallet, Public: true}
		if code, b, _ := m.req(http.MethodPost, "/v1/marketplace/listings", arbiter, "", bad); code != http.StatusUnprocessableEntity {
			t.Fatalf("a listing paying another org's wallet = %d (%s), want 422", code, b)
		}
		l := m.shelve(publishReq{Tool: tool, Title: "Premium", Price: "1.00", Currency: "USD", Recipient: payee, Public: true})
		// Nor can an edit move the payout there.
		if code, b, _ := m.req(http.MethodPatch, "/v1/marketplace/listings/"+l.ID, arbiter, "",
			map[string]any{"recipient": victimWallet}); code != http.StatusUnprocessableEntity {
			t.Fatalf("an edit paying another org's wallet = %d (%s), want 422", code, b)
		}
		m.install(buyer, tool)
		m.fund(buyer, money.FromCents(1000))
		code, body, hdr := m.call(buyer, tool, "")
		if code != http.StatusPaymentRequired {
			t.Fatalf("own-wallet listing = %d (%s), want a 402 challenge", code, body)
		}
		if code, body, _ = m.call(buyer, tool, m.pay(m.challengeOf(hdr), 5*time.Minute)); code != http.StatusOK {
			t.Fatalf("paid own-wallet call = %d (%s), want 200", code, body)
		}
		if got := m.balance(seller, payee); got.Cmp(money.FromCents(100)) != 0 {
			t.Fatalf("seller credited %s, want exactly 1", got.String())
		}
		if got := m.balance(outside, victimWallet); !got.IsZero() {
			t.Fatalf("the outside org was credited: %s", got.String())
		}
	})
}

// TestFreeToolSurvivesAnAbsentRail: the charger is consulted on EVERY dispatch, so
// an absent payment rail must not take the free tools down with it. With x402 not
// mounted, an unpriced call still runs — nothing free needs a rail — while a priced
// one is refused rather than served.
//
// This pins a real regression: written the obvious way, Settle refused before it
// asked whether the resource was free, and every free tool in the process 424'd the
// moment marketplace was mounted without x402.
func TestFreeToolSurvivesAnAbsentRail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const buyer = "buyerorg"
		m := newEnv(t, "conn_free", "conn_paid")

		m.shelve(publishReq{
			Tool: "conn_paid", Title: "Paid", Price: "1.00", Currency: "USD",
			Recipient: m.createWallet(platform), Public: true,
		})
		m.install(buyer, "conn_free")
		m.install(buyer, "conn_paid")

		// The rail goes away; the price table stays (marketplace is still mounted).
		if err := x402.Shutdown(); err != nil {
			t.Fatalf("x402.Shutdown: %v", err)
		}

		if code, body, _ := m.call(buyer, "conn_free", ""); code != http.StatusOK {
			t.Fatalf("free tool with no payment rail = %d (%s), want 200", code, body)
		}
		if code, body, _ := m.call(buyer, "conn_paid", ""); code == http.StatusOK {
			t.Fatalf("priced tool was served with no payment rail: %s", body)
		}
	})
}

// zapRows offers a first-party service's tools to every org, as the task board
// does: a platform tool, ranked below an org's own functions.
type zapRows struct{ names []string }

func (zapRows) Source() tools.Source { return tools.SourceZAPService }
func (p zapRows) List(context.Context, tools.Scope) ([]tools.Tool, error) {
	var out []tools.Tool
	for _, n := range p.names {
		out = append(out, tools.Tool{Name: n, Source: tools.SourceZAPService, Dispatchable: true})
	}
	return out, nil
}
func (zapRows) Dispatch(context.Context, tools.Principal, string, map[string]any) (any, error) {
	return map[string]any{"ranFor": platform}, nil
}

// No org puts a toll on a tool it does not own. A connector action resolves in
// every org's scope, so resolving it is not owning it: an org that lists one is
// refused, whichever org asks, and every org's call of it stays free.
func TestNoOrgTollsAPlatformTool(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const (
			victim  = "victim"
			mallory = "mallory"
			tool    = "slack_send_message"
		)
		e := newEnv(t, tool)
		e.install(victim, tool)
		e.fund(victim, money.FromCents(10_000))
		for _, org := range []string{mallory, "globex"} {
			wal := e.createWallet(org)
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings", admin(org), "",
				publishReq{Kind: KindTool, Tool: tool, Title: "Slack", Price: "1.00", Recipient: wal, Public: true}); code != http.StatusForbidden {
				t.Fatalf("%s lists the platform's %s = %d (%s), want 403", org, tool, code, b)
			}
		}
		before := e.balance(victim, victim)
		if code, b, h := e.call(victim, tool, ""); code != http.StatusOK || h.Get(protocol.HeaderPaymentRequired) != "" {
			t.Fatalf("the victim's call of a platform tool = %d (%s), want 200 and no challenge", code, b)
		}
		if got := e.balance(victim, victim); got.Cmp(before) != 0 {
			t.Fatalf("the victim paid %s", before.Sub(got))
		}
	})
}

// A call is priced by the row that runs it, never by its name. The platform's
// listing of a first-party tool prices the orgs whose call reaches that tool; an
// org whose own function shares the name runs its own, for nothing, and pays
// nobody. And an org's own function is not a per-call offer at all: no other org
// could reach it.
func TestAToolIsPricedByTheRowThatRuns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const alpha, bravo = "alpha", "bravo"
		e := newEnv(t)
		tools.Default().Register(zapRows{names: []string{"task_list"}})
		tools.Default().Register(ownRows{byOrg: map[string][]string{alpha: {"function_summarize"}, bravo: {"function_summarize", "task_list"}}})
		till := e.createWallet(platform)
		e.shelve(publishReq{Tool: "task_list", Title: "Boards", Price: "1.00", Recipient: till, Public: true})

		awal := e.createWallet(alpha)
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings", admin(alpha), "",
			publishReq{Kind: KindTool, Tool: "function_summarize", Title: "Summarize", Price: "1.00", Recipient: awal, Public: true}); code != http.StatusUnprocessableEntity {
			t.Fatalf("a per-call offer of alpha's own function = %d (%s), want 422", code, b)
		}
		for _, org := range []string{alpha, bravo} {
			e.install(org, "task_list")
			e.install(org, "function_summarize")
			e.fund(org, money.FromCents(10_000))
		}
		bravoBefore := e.balance(bravo, bravo)
		for _, tool := range []string{"task_list", "function_summarize"} {
			code, b, h := e.call(bravo, tool, "")
			if code != http.StatusOK || !bytes.Contains(b, []byte(`"ranFor":"bravo"`)) || h.Get(protocol.HeaderPaymentRequired) != "" {
				t.Fatalf("bravo's own %s = %d (%s), want bravo's row, run free", tool, code, b)
			}
		}
		if got := e.balance(bravo, bravo); got.Cmp(bravoBefore) != 0 {
			t.Fatalf("bravo paid %s to run its own functions", bravoBefore.Sub(got))
		}
		// alpha has no row of that name, so its call reaches the platform's, and pays.
		code, b, h := e.call(alpha, "task_list", "")
		if code != http.StatusPaymentRequired {
			t.Fatalf("alpha's call of the platform's task_list = %d (%s), want 402", code, b)
		}
		if code, b, _ = e.call(alpha, "task_list", e.pay(e.challengeOf(h), 5*time.Minute)); code != http.StatusOK ||
			!bytes.Contains(b, []byte(`"ranFor":"admin"`)) {
			t.Fatalf("alpha's paid call = %d (%s)", code, b)
		}
		if got := e.balance(platform, till); got.Cmp(money.FromCents(100)) != 0 {
			t.Fatalf("the platform holds %s, want the one call alpha paid", got)
		}
	})
}

// A caller whose wallet cannot cover a priced call is refused, and the seller is
// credited nothing: a payment is drawn from the wallet only when it covers it.
func TestAnUnfundedCallerPaysNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const caller = "sybil"
		e := newEnv(t, "conn_gold")
		till := e.createWallet(platform)
		e.shelve(publishReq{Tool: "conn_gold", Title: "Gold", Price: "1000000", Recipient: till, Public: true})
		e.install(caller, "conn_gold")
		code, _, h := e.call(caller, "conn_gold", "")
		if code != http.StatusPaymentRequired {
			t.Fatalf("unpaid = %d", code)
		}
		code, b, h := e.call(caller, "conn_gold", e.pay(e.challengeOf(h), 5*time.Minute))
		var settled protocol.SettlementResponse
		if code != http.StatusPaymentRequired || protocol.DecodeHeader(h.Get(protocol.HeaderPaymentResponse), &settled) != nil ||
			settled.Success || settled.ErrorReason != "insufficient_funds" {
			t.Fatalf("a call its wallet cannot cover = %d (%s) %+v, want 402 insufficient_funds", code, b, settled)
		}
		if got := e.balance(platform, till); !got.IsZero() {
			t.Fatalf("the seller was credited %s by a caller with nothing", got)
		}
	})
}
