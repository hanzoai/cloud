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
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
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
// happened whatever this process managed to do with the deposit afterwards. A
// conversion that went missing whenever the credit refused would under-report
// exactly the payments an operator is already chasing.
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
		return settlement{}, context.DeadlineExceeded
	}
	if err := s.record(context.Background(), paid(), settledRef, settledReceipt); err == nil {
		t.Fatal("a credit that could not be sized answered success")
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
