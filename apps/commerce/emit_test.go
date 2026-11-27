package commerce

// emit_test.go — WHAT A SETTLED PAYMENT SAYS TO THE EVENT PLANE.
//
// The sale this states is the one a server-side conversion is built from
// (apps/destinations translates `order_completed` into every connected platform's
// Purchase), so three things about it are properties and not hopes:
//
//	THE VALUE IS THE MONEY THAT MOVED, in the currency the card was charged —
//	never the scorer's converted nano-USD, and never nothing.
//	THE DEDUP KEY IS THE SETTLEMENT'S OWN REFERENCE, so a retried or replayed
//	settlement converges on ONE conversion instead of reporting the sale twice.
//	IT CANNOT REACH THE PAYMENT. It is detached, bounded, dropped at the ceiling
//	and panic-guarded, exactly as the risk teaching beside it is.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
)

// sale is one statement that LEFT this process, as the client saw it.
type sale struct {
	org string
	in  *client.EventIn
}

// mute substitutes the event plane's client so an endpoint fixture states its sale to a
// function instead of to an analytics child that is not running. It is [quiet]'s
// half for the other plane: [screen.emit] spawns a goroutine the request outlives,
// and a fixture that unbinds the plane underneath it is racing a peer call it
// never meant to make.
func mute(t *testing.T) {
	t.Helper()
	prior := send
	send = func(context.Context, *client.EventIn) (*client.EventCaptured, error) {
		return &client.EventCaptured{Accepted: 1}, nil
	}
	t.Cleanup(func() { send = prior })
}

// watchSales substitutes the client and hands back what leaves, so what a settlement
// states can be asserted without an analytics child to receive it.
func watchSales(t *testing.T) <-chan sale {
	t.Helper()
	seen := make(chan sale, 16)
	prior := send
	send = func(ctx context.Context, in *client.EventIn) (*client.EventCaptured, error) {
		seen <- sale{org: cloud.Who(ctx).Org, in: in}
		return &client.EventCaptured{Accepted: 1}, nil
	}
	t.Cleanup(func() { send = prior })
	return seen
}

// paid is the payment a cleared card produces at either mint.
func paid() payment {
	return payment{
		org:      "acme",
		ledger:   "acme",
		subject:  "person_42",
		path:     "/v1/commerce/payments",
		via:      "/v1/commerce/payments",
		cents:    4950,
		currency: "eur",
		facts:    map[string]string{client.SignalNano: "0", "currency": "eur"},
		moded:    true,
	}
}

// said reads one attribute off a stated occurrence.
func said(in *client.EventIn, name string) string {
	for _, s := range in.Attributes {
		if s.Name == name {
			return s.Value
		}
	}
	return ""
}

// TestPurchase_IsTheMoneyThatMoved. A conversion is only worth what it says it is
// worth: the platform optimising on it bids against this number.
//
// Mutation proof: state the scorer's nano-USD instead and a €49.50 sale reports 0
// (the risk signal is absent for any currency this process cannot state in USD);
// drop the divide and it reports 4,950.
func TestPurchase_IsTheMoneyThatMoved(t *testing.T) {
	in := purchase(paid(), settledRef)
	if in == nil {
		t.Fatal("a settled payment stated nothing")
	}
	switch {
	case in.Name != orderCompleted:
		t.Errorf("name %q, want %q — the name the conversion translator maps onto Purchase",
			in.Name, orderCompleted)
	case in.Subject != "person_42":
		t.Errorf("subject %q, want person_42 — the match key every adapter hashes before send", in.Subject)
	case in.Product != "commerce":
		t.Errorf("product %q, want commerce — the surface that saw the sale", in.Product)
	case said(in, "revenue") != "49.5":
		t.Errorf("revenue %q, want 49.5 — major units of the currency charged", said(in, "revenue"))
	case said(in, "currency") != "eur":
		t.Errorf("currency %q, want eur", said(in, "currency"))
	}
}

// TestPurchase_DedupsOnTheSettlement. `event_id` is what makes the browser pixel and
// this server-side conversion count ONCE at the platform, and settlement is
// at-least-once — the endpoint retries, a webhook replays. Keyed on the settlement's own
// reference they converge; keyed on anything minted per call they do not.
//
// Mutation proof: mint an id here and the two statements below stop matching.
func TestPurchase_DedupsOnTheSettlement(t *testing.T) {
	first, again := purchase(paid(), settledRef), purchase(paid(), settledRef)
	if got := said(first, "event_id"); got != settledRef {
		t.Fatalf("event_id %q, want the settlement reference %q", got, settledRef)
	}
	if said(first, "event_id") != said(again, "event_id") {
		t.Error("one settlement stated two ids — a replayed webhook would report the sale twice")
	}
}

// TestPurchase_StatesNothingItCannotIdentify. A sale with no payer cannot be matched
// and a sale with no settlement cannot be deduplicated; either one is a row that
// costs a platform's model more than it tells it.
func TestPurchase_StatesNothingItCannotIdentify(t *testing.T) {
	if in := purchase(payment{cents: 100}, settledRef); in != nil {
		t.Error("a payment with no payer was stated anyway")
	}
	if in := purchase(paid(), ""); in != nil {
		t.Error("a payment with no settlement was stated anyway")
	}
}

// TestPurchase_ASaleOfNothingStatesNoValue. Zero and absent are different facts: a
// stated 0 is a purchase worth nothing, which is a number a platform will average
// into its bidding. An amount this endpoint never observed is simply not said.
func TestPurchase_ASaleOfNothingStatesNoValue(t *testing.T) {
	p := paid()
	p.cents = 0
	if got := said(purchase(p, settledRef), "revenue"); got != "" {
		t.Errorf("revenue %q, want it unstated", got)
	}
}

// TestEmit_StatesTheSaleUnderThePayersOwnOrg. The tenant is the LEDGER's — the org
// whose balance the payment funded and whose destinations are connected. Filed under
// any other org the conversion reaches a platform nobody linked.
func TestEmit_StatesTheSaleUnderThePayersOwnOrg(t *testing.T) {
	seen := watchSales(t)
	p := paid()
	p.org, p.ledger = "hanzo", "acme" // a SuperAdmin acting inside a customer's org
	riskGate(luxlog.New("test")).emit(p, settledRef)

	select {
	case got := <-seen:
		if got.org != "acme" {
			t.Errorf("the sale was filed under %q, want acme — the org whose balance it funded", got.org)
		}
		if said(got.in, "revenue") != "49.5" {
			t.Errorf("revenue %q, want 49.5", said(got.in, "revenue"))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a settled payment was never stated on the event plane")
	}
}

// TestEmit_CannotReachThePayment. The card cleared and the customer has been answered
// by the time this runs, so a plane that panics, refuses or hangs is telemetry lost
// and nothing else.
func TestEmit_CannotReachThePayment(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(context.Context, *client.EventIn) (*client.EventCaptured, error)
	}{
		{"the plane panics", func(context.Context, *client.EventIn) (*client.EventCaptured, error) {
			panic("the analytics child died mid-call")
		}},
		{"the plane refuses", func(context.Context, *client.EventIn) (*client.EventCaptured, error) {
			return nil, context.DeadlineExceeded
		}},
		{"the occurrence landed nowhere", func(context.Context, *client.EventIn) (*client.EventCaptured, error) {
			return &client.EventCaptured{Accepted: 0, Dropped: 1}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			prior := send
			send = func(ctx context.Context, in *client.EventIn) (*client.EventCaptured, error) {
				defer close(done)
				return tc.call(ctx, in)
			}
			t.Cleanup(func() { send = prior })

			riskGate(luxlog.New("test")).emit(paid(), settledRef)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("the statement never ran")
			}
			if !released(3 * time.Second) {
				t.Errorf("%d detached hand-off(s) never came back", inflight())
			}
		})
	}
}

// TestEmit_DropsAtTheCeiling. Past the ceiling a statement is DROPPED rather than
// queued: a queue defers the loss instead of bounding it, and the memory this process
// holds must not be a function of how fast money is arriving.
func TestEmit_DropsAtTheCeiling(t *testing.T) {
	for range maxEmits {
		emitting <- struct{}{}
	}
	t.Cleanup(func() {
		for len(emitting) > 0 {
			<-emitting
		}
	})
	seen := watchSales(t)
	riskGate(luxlog.New("test")).emit(paid(), settledRef)
	select {
	case got := <-seen:
		t.Errorf("a statement went out past the ceiling: %+v", got.in)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRecordStatesTheSaleEvenWhenTheCreditRefuses. The card cleared, so the sale
// happened whatever commerce managed to do with the credit afterwards (it posts the
// credit itself and answers the caller if it cannot). A conversion that went missing
// whenever a credit failed would under-report exactly the payments an operator is
// already chasing. The receipt is read — it is what says the money was real — and a
// currency the ledger does not hold is commerce's to refuse, never this record's.
//
// Mutation proof: return early from [screen.record] on the credit error and this
// hears nothing.
func TestRecordStatesTheSaleEvenWhenTheCreditRefuses(t *testing.T) {
	prior := teach
	teach = func(context.Context, *client.RiskObserveIn) (*client.RiskObserved, error) {
		return &client.RiskObserved{Learned: 1}, nil
	}
	t.Cleanup(func() { teach = prior })
	seen := watchSales(t)

	s := riskGate(luxlog.New("test"))
	s.receipt = func(context.Context, string, string) (settlement, error) {
		return settlement{cents: 4950, currency: "eur"}, nil
	}
	if err := s.record(context.Background(), paid(), settledRef, settledReceipt); err != nil {
		t.Fatalf("recording a settled sale refused it: %v", err)
	}
	select {
	case got := <-seen:
		if said(got.in, "event_id") != settledRef {
			t.Errorf("event_id %q, want %q", said(got.in, "event_id"), settledRef)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a settled payment whose credit refused was never stated as a sale")
	}
}

// TestOrderCompletedIsTheTranslatorsOwnName pins the one string that has to agree
// across two apps: apps/destination/translate.go maps this name onto the normalized
// Purchase, so a rename here silently downgrades every server-side conversion to a
// custom event no platform optimises on.
func TestOrderCompletedIsTheTranslatorsOwnName(t *testing.T) {
	if orderCompleted != "order_completed" {
		t.Errorf("the sale is named %q — apps/destinations maps %q onto Purchase, and /v1/event/insights "+
			"counts it as an order", orderCompleted, "order_completed")
	}
	if strings.TrimSpace(orderCompleted) != orderCompleted {
		t.Error("the name carries padding, which is a second name in the column every lens groups by")
	}
}

// TestPurchase_StatesNoCurrencyItDidNotObserve. An unstated currency is the
// translator's USD default (apps/destinations); restating that default here would be
// a second place deciding what empty means.
func TestPurchase_StatesNoCurrencyItDidNotObserve(t *testing.T) {
	p := paid()
	p.currency = ""
	if got := said(purchase(p, settledRef), "currency"); got != "" {
		t.Errorf("currency %q, want it unstated", got)
	}
}

// TestPurchase_NamesTheBuyer. The row is joined to the buyer's own visits and the
// ad click they came through by the IAM subject their browser is identified by;
// the wallet is the books, and rides beside it as the payer, which is all a
// renewal later knows.
func TestPurchase_NamesTheBuyer(t *testing.T) {
	p := paid()
	p.subject, p.person = "acme/alice", "5f0c-sub"
	in := purchase(p, settledRef)
	if in.Subject != "5f0c-sub" || said(in, "payer") != "acme/alice" {
		t.Errorf("subject %q payer %q, want the buyer's sub and the wallet beside it", in.Subject, said(in, "payer"))
	}
	if said(in, "recurring") != "" {
		t.Error("a sale somebody made is not recurring")
	}
	p.person = ""
	if in := purchase(p, settledRef); in.Subject != "acme/alice" || said(in, "payer") != "acme/alice" {
		t.Errorf("with no person the wallet is the subject and the payer: %+v", in)
	}
}

// TestScreened_APlanSaleStatesWhatTheCardWasCharged. A sale's input names no
// amount — the catalog prices it — so the only place its worth exists is the
// receipt. Stated from the input it was a purchase worth nothing, which is what
// the one live server-side order on the plane carried.
func TestScreened_APlanSaleStatesWhatTheCardWasCharged(t *testing.T) {
	quiet(t)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	seen := watchSales(t)
	sold := &client.Sold{Sale: &client.Sale{SubscriptionID: "sub_pro", EventID: "sub_pro", AmountCents: 1900, Currency: "usd", Coupon: "50OFFFOUNDER"}}
	core := func(context.Context, *client.SaleIn) (*client.Sold, error) { return sold, nil }
	receipt := func(in *client.SaleIn, out *client.Sold) cleared {
		return cleared{ref: out.Sale.EventID, id: out.Sale.SubscriptionID, cents: out.Sale.AmountCents,
			currency: out.Sale.Currency, test: out.Sale.Test, moded: true, plan: "pro", coupon: out.Sale.Coupon, consent: in.Consent}
	}
	buyer := zip.WithCaller(context.Background(), zip.Caller{Org: gateOrg, User: gateUser})
	checkout := "analytics,marketing"
	in := &client.SaleIn{PlanID: "pro", Subject: wallet(gateOrg), Consent: &checkout}
	if _, err := screened(riskGate(luxlog.New("test")), "/billing/subscribe", false,
		func(_ context.Context, in *client.SaleIn) (int64, string) { return 0, in.Currency },
		func(in *client.SaleIn) string { return in.Subject }, receipt, nil, core)(buyer, in); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if said(got.in, "revenue") != "19" || said(got.in, "currency") != "usd" || said(got.in, "event_id") != "sub_pro" {
			t.Errorf("stated %+v, want 19 usd keyed on the sale's event id", got.in.Attributes)
		}
		if said(got.in, "coupon") != "50OFFFOUNDER" {
			t.Errorf("coupon = %q, want the code the sale was bought with", said(got.in, "coupon"))
		}
		if said(got.in, "consent") != checkout {
			t.Errorf("consent = %q, want the choice the checkout stated", said(got.in, "consent"))
		}
		if got.in.Subject != gateUser {
			t.Errorf("subject %q, want the buyer %q", got.in.Subject, gateUser)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the sale was never stated")
	}
}

// TestSweeps_StateLiveCharges. A renewal and an auto-reload are sales the
// platform made with nobody present: each paid live one is stated under the org
// that paid, keyed on its invoice or transaction, marked recurring; a sandbox
// charge and one that failed are not sales.
func TestSweeps_StateLiveCharges(t *testing.T) {
	seen := watchSales(t)
	sweeps = riskGate(luxlog.New("test"))
	renewed([]commercebilling.Renewal{
		{OrgName: "acme", UserId: "acme/alice", InvoiceId: "inv_1", AmountCents: 2000, Currency: "usd", Success: true},
		{OrgName: "acme", UserId: "acme/bob", InvoiceId: "inv_2", AmountCents: 2000, Currency: "usd", Success: true, Test: true},
		{OrgName: "acme", UserId: "acme/eve", InvoiceId: "inv_3", Success: false},
	})
	reloaded([]commercebilling.RechargeResult{
		{OrgName: "zeta", UserId: "zeta", Charged: true, AmountCents: 2500, Currency: "usd", TransactionId: "tx_9"},
		{OrgName: "zeta", UserId: "zeta", Charged: true, AmountCents: 2500, Currency: "usd", TransactionId: "tx_t", Test: true},
	})
	want := map[string]string{"inv_1": "acme", "tx_9": "zeta"}
	for len(want) > 0 {
		select {
		case got := <-seen:
			id := said(got.in, "event_id")
			if want[id] != got.org || said(got.in, "recurring") != "true" {
				t.Errorf("stated %s under %q recurring=%q", id, got.org, said(got.in, "recurring"))
			}
			delete(want, id)
		case <-time.After(3 * time.Second):
			t.Fatalf("never stated: %v", want)
		}
	}
	select {
	case got := <-seen:
		t.Errorf("a sandbox or failed charge was stated: %+v", got.in)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestEmit_OnlyLiveMoneyIsASale. A sandbox charge costs a test card nothing;
// stated as a sale it would reach the platform's ad accounts as revenue anyone
// could mint. A charge whose books nobody said states nothing either, and a
// receipt that cannot be read says none.
func TestEmit_OnlyLiveMoneyIsASale(t *testing.T) {
	sandbox, unsaid := paid(), paid()
	sandbox.test = true
	unsaid.moded = false
	for name, p := range map[string]payment{"sandbox books": sandbox, "books nobody said": unsaid} {
		t.Run(name, func(t *testing.T) {
			seen := watchSales(t)
			riskGate(luxlog.New("test")).emit(p, settledRef)
			select {
			case got := <-seen:
				t.Fatalf("stated %+v", got.in)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
	t.Run("a receipt that cannot be read", func(t *testing.T) {
		seen := watchSales(t)
		s := riskGate(luxlog.New("test"))
		s.receipt = func(context.Context, string, string) (settlement, error) {
			return settlement{}, context.DeadlineExceeded
		}
		p := paid()
		p.moded = false
		_ = s.record(context.Background(), p, settledRef, settledReceipt)
		select {
		case got := <-seen:
			t.Fatalf("stated %+v", got.in)
		case <-time.After(200 * time.Millisecond):
		}
	})
}

// TestPurchase_StatesTheCurrencysOwnScale. A yen has no minor unit: ¥500 is 500,
// not 5.
func TestPurchase_StatesTheCurrencysOwnScale(t *testing.T) {
	p := paid()
	p.cents, p.currency = 500, "jpy"
	if got := said(purchase(p, settledRef), "revenue"); got != "500" {
		t.Errorf("revenue %q, want 500", got)
	}
}

// TestSweeps_StateNothingOffMainnet. A test network's charges are sandbox money
// whatever mode a row records.
func TestSweeps_StateNothingOffMainnet(t *testing.T) {
	t.Setenv(network.NetworkEnv, "testnet")
	seen := watchSales(t)
	renewed([]commercebilling.Renewal{{OrgName: "acme", UserId: "acme/a", InvoiceId: "inv", AmountCents: 100, Currency: "usd", Success: true}})
	reloaded([]commercebilling.RechargeResult{{OrgName: "acme", UserId: "acme", Charged: true, AmountCents: 100, TransactionId: "tx"}})
	select {
	case got := <-seen:
		t.Fatalf("stated %+v off mainnet", got.in)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestASandboxTopUpIsNoSale drives the browser top-up endpoint over a settlement
// its receipt says went to the sandbox: the credit lands in the sandbox books and
// nothing is stated as a sale.
func TestASandboxTopUpIsNoSale(t *testing.T) {
	funded(t)
	app := creditEndpoint(t, stating(riskGate(luxlog.New("sandbox")),
		settlement{cents: gateCents, currency: "usd", test: true}))
	seen := watchSales(t)
	if code, body := topup(t, app); code != http.StatusOK {
		t.Fatalf("sandbox top-up %d %s", code, body)
	}
	select {
	case got := <-seen:
		t.Fatalf("a sandbox charge was stated as a sale: %+v", got.in)
	case <-time.After(300 * time.Millisecond):
	}
}

// A test-mode plan sale is stated as test traffic: the plan as a line item, `test`,
// and no amount anywhere a platform could sum into revenue.
func TestSandboxPlanSaleIsStatedAsTestTraffic(t *testing.T) {
	p := paid()
	p.test, p.plan, p.interval = true, "dev", "year"
	in := purchase(p, settledRef)
	switch {
	case in == nil:
		t.Fatal("a sandbox plan sale stated nothing")
	case said(in, "test") != "true":
		t.Errorf("test = %q, want true", said(in, "test"))
	case said(in, "revenue") != "" || said(in, "item_price") != "":
		t.Errorf("a sandbox sale stated an amount: revenue %q price %q", said(in, "revenue"), said(in, "item_price"))
	case said(in, "item_id") != "dev" || said(in, "item_variant") != "annual":
		t.Errorf("line item: %q %q", said(in, "item_id"), said(in, "item_variant"))
	}
}

func TestLivePlanSaleStatesItsLineItem(t *testing.T) {
	p := paid()
	p.plan, p.interval = "max", "month"
	in := purchase(p, settledRef)
	if said(in, "item_id") != "max" || said(in, "item_variant") != "monthly" || said(in, "item_price") != "49.5" || said(in, "test") != "" || said(in, "coupon") != "" {
		t.Errorf("live plan sale: %+v", in.Attributes)
	}
	p.coupon = "SPRING"
	if in := purchase(p, settledRef); said(in, "coupon") != "SPRING" {
		t.Errorf("a sale bought with a coupon states it: %+v", in.Attributes)
	}
}

// A canceled subscription is stated on the caller's own plane, with the plan it
// ended and no amount.
func TestCanceledIsStatedWithItsPlan(t *testing.T) {
	seen := watchSales(t)
	sweeps = riskGate(luxlog.New("test"))
	canceled("acme", &commercebilling.Subscription{UserID: "acme/alice", PlanID: "dev", Quantity: 2})
	select {
	case got := <-seen:
		in := got.in
		switch {
		case got.org != "acme" || in.Name != "subscription_canceled" || in.Product != "commerce":
			t.Errorf("stated %s/%s under %s", in.Product, in.Name, got.org)
		case said(in, "plan") != "dev" || said(in, "item_id") != "dev" || said(in, "quantity") != "2":
			t.Errorf("line: %+v", in.Attributes)
		case said(in, "revenue") != "":
			t.Errorf("a cancellation stated an amount: %q", said(in, "revenue"))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the cancellation was never stated")
	}
	canceled("acme", nil)
	canceled("acme", &commercebilling.Subscription{PlanID: "dev"})
	select {
	case got := <-seen:
		t.Fatalf("stated %+v for nothing to state", got.in)
	case <-time.After(150 * time.Millisecond):
	}
}

// filing is one customer the CRM was asked to file.
type filing struct {
	org   string
	buyer crm.Buyer
}

// watchCustomers substitutes the CRM's customer write and hands back what is filed.
func watchCustomers(t *testing.T) <-chan filing {
	t.Helper()
	seen := make(chan filing, 16)
	prior := file
	file = func(_ context.Context, org string, b crm.Buyer, _ time.Time) error {
		seen <- filing{org: org, buyer: b}
		return nil
	}
	t.Cleanup(func() { file = prior })
	return seen
}

// TestEmit_FilesThePayerAsACustomerOfTheOrgItPaid. A live payment files its payer by
// the address the request carried, in the org whose balance it funded and no other,
// with the wallet it was paid from as the commerce user; one payment files once.
func TestEmit_FilesThePayerAsACustomerOfTheOrgItPaid(t *testing.T) {
	mute(t)
	seen := watchCustomers(t)
	p := paid()
	p.org, p.ledger, p.email = "hanzo", "acme", "ada@example.com"
	riskGate(luxlog.New("test")).emit(p, settledRef)

	select {
	case got := <-seen:
		if got.org != "acme" || got.buyer != (crm.Buyer{Email: "ada@example.com", User: "person_42"}) {
			t.Errorf("filed %+v, want ada@example.com as person_42 in acme", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a settled payment's payer was never filed")
	}
	select {
	case got := <-seen:
		t.Errorf("one payment filed twice: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestEmit_FilesNoCustomerWhoCannotBeNamedOrPaidNothingReal. A payer with no address,
// a sandbox charge and a charge whose books nobody said file nobody: there is no
// person to name, and no customer where no money moved.
func TestEmit_FilesNoCustomerWhoCannotBeNamedOrPaidNothingReal(t *testing.T) {
	mute(t)
	anonymous, sandbox, unsaid := paid(), paid(), paid()
	anonymous.email = ""
	sandbox.email, sandbox.test = "ada@example.com", true
	unsaid.email, unsaid.moded = "ada@example.com", false
	for name, p := range map[string]payment{"no address": anonymous, "sandbox books": sandbox, "books nobody said": unsaid} {
		t.Run(name, func(t *testing.T) {
			seen := watchCustomers(t)
			riskGate(luxlog.New("test")).emit(p, settledRef)
			select {
			case got := <-seen:
				t.Fatalf("filed %+v", got)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

// TestEmit_ACRMThatRefusesCannotReachThePayment. An org with no CRM answers an error;
// the statement beside it is still made and the hand-off comes back.
func TestEmit_ACRMThatRefusesCannotReachThePayment(t *testing.T) {
	seen := watchSales(t)
	prior := file
	file = func(context.Context, string, crm.Buyer, time.Time) error { return context.DeadlineExceeded }
	t.Cleanup(func() { file = prior })
	p := paid()
	p.email = "ada@example.com"
	riskGate(luxlog.New("test")).emit(p, settledRef)
	select {
	case <-seen:
	case <-time.After(3 * time.Second):
		t.Fatal("the sale was not stated")
	}
	if !released(3 * time.Second) {
		t.Errorf("%d detached hand-off(s) never came back", inflight())
	}
}
