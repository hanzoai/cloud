package analytics

// event_rpc_test.go — the peer door, held to the two properties that make it safe
// to let another process write into a shared, per-tenant store.
//
//	THE TENANT IS THE CALLER'S. Not a field (there is none), not a default, and
//	not absent — a call with no principal writes nothing at all.
//	IT REACHES THE ONE WRITE CORE. What a peer states is normalized, scrubbed and
//	published exactly like what a browser posts, so there is one answer to "what
//	is an event" rather than two that drift.

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	planeops "github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// asPeer is a plane call: a context with no request behind it, carrying the org
// the caller states. It is the one place zip reads a stated caller.
func asPeer(org string) context.Context { return cloud.For(context.Background(), org) }

// TestPlaneCapture_StampsTheCallersOwnTenant is the whole isolation property,
// read off the COMMITTED fact rather than off the argument: the row's org is the
// caller's, and it got there without the wire carrying one.
//
// Mutation proof: take the org from anywhere but cloud.Who and the committed
// fact carries the wrong tenant (or none, which normalize would land under "").
func TestPlaneCapture_StampsTheCallersOwnTenant(t *testing.T) {
	w := fakeWarehouse(t)

	out, err := planeCapture(asPeer("acme"), &planeops.EventIn{
		Name:    "risk_decided",
		Product: "risk",
		Subject: "8f14e45fceea167a",
		Attributes: []planeops.Signal{
			{Name: "action", Value: "review"},
			{Name: "posture", Value: "shadow"},
		},
	})
	if err != nil {
		t.Fatalf("planeCapture: %v", err)
	}
	if out.Accepted != 1 || out.Dropped != 0 {
		t.Fatalf("receipt {accepted:%d dropped:%d}, want exactly one landed", out.Accepted, out.Dropped)
	}
	if len(w.facts) != 1 {
		t.Fatalf("%d facts committed, want 1", len(w.facts))
	}
	f := w.facts[0]
	switch {
	case f.org != "acme":
		t.Errorf("the fact landed under org %q, want %q — the tenant is the caller's", f.org, "acme")
	case f.signal != signalAct:
		t.Errorf("signal %q, want %q — a peer's occurrence is an act, in the same partition "+
			"as the product's own", f.signal, signalAct)
	case f.kind != kindTrack:
		t.Errorf("kind %q, want %q", f.kind, kindTrack)
	case f.name != "risk_decided":
		t.Errorf("name %q, want the stated one", f.name)
	case f.product != "risk":
		t.Errorf("product %q, want the emitting surface", f.product)
	case f.distinct != "8f14e45fceea167a":
		t.Errorf("distinct %q, want the stated subject", f.distinct)
	}
	for k, want := range map[string]string{
		"action": "review", "posture": "shadow", "$source": sourcePlane,
	} {
		if got := f.attributes[k]; got != want {
			t.Errorf("attributes[%q] = %q, want %q", k, got, want)
		}
	}
}

// TestPlaneCapture_FailsClosedWithoutAPrincipal. An unidentified peer has no
// partition, and defaulting one would make a shared store writable by anybody who
// can reach the socket.
func TestPlaneCapture_FailsClosedWithoutAPrincipal(t *testing.T) {
	w := fakeWarehouse(t)

	_, err := planeCapture(context.Background(), &planeops.EventIn{Name: "risk_decided"})
	he, ok := err.(*zip.HTTPError)
	if !ok || he.Status != 403 {
		t.Fatalf("err %v, want a 403 — a call that acts for no tenant must write nothing", err)
	}
	if len(w.facts) != 0 {
		t.Errorf("%d facts committed by a call with no principal", len(w.facts))
	}
}

// TestPlaneCapture_RefusesAnUnnamedAct. Every other route into the write core has
// a server-chosen default name for the signal it carries; a tracked act has none,
// so an unnamed one is unroutable — refused at the boundary where the caller can
// still be told, rather than counted as a silent drop.
func TestPlaneCapture_RefusesAnUnnamedAct(t *testing.T) {
	w := fakeWarehouse(t)

	for _, name := range []string{"", "   "} {
		_, err := planeCapture(asPeer("acme"), &planeops.EventIn{Name: name})
		he, ok := err.(*zip.HTTPError)
		if !ok || he.Status != 400 {
			t.Errorf("name %q: err %v, want a 400", name, err)
		}
	}
	if len(w.facts) != 0 {
		t.Errorf("%d facts committed by an unnamed act", len(w.facts))
	}
}

// TestEventIn_CannotNameAnOrg is the STRUCTURAL half of the isolation: a peer
// cannot spoof a tenant it cannot spell.
//
// Mutation proof: add an Org field to plane.EventIn and this names it.
func TestEventIn_CannotNameAnOrg(t *testing.T) {
	rt := reflect.TypeFor[planeops.EventIn]()
	for field := range rt.Fields() {
		name := strings.ToLower(field.Name)
		for _, banned := range []string{"org", "tenant", "brand", "owner"} {
			if strings.Contains(name, banned) {
				t.Errorf("plane.EventIn.%s names the tenant — the organisation a row lands under "+
					"rides the caller, never the argument", field.Name)
			}
		}
	}
}

// TestStated_DropsAnUnnamedPair. An empty key and an absent one read identically
// out of the store, so keeping it buys nothing and costs a dictionary entry in
// every row.
func TestStated_DropsAnUnnamedPair(t *testing.T) {
	got := stated([]planeops.Signal{
		{Name: "action", Value: "allow"},
		{Name: "  ", Value: "nowhere"},
		{Name: "", Value: "nowhere either"},
		{Name: " posture ", Value: "live"},
	})
	if len(got) != 2 {
		t.Fatalf("stated kept %d pairs, want 2: %v", len(got), got)
	}
	if got["action"] != "allow" || got["posture"] != "live" {
		t.Errorf("stated lost a named pair or kept its padding: %v", got)
	}
	if stated(nil) != nil {
		t.Error("an empty list produced a non-nil bag — an allocation per empty call")
	}
}

// TestPlaneCapture_CarriesTheMoneyToTheFanOut is the whole of the commerce bridge,
// read at the seam the conversion forwarders actually consume.
//
// A peer states a sale in the only shape this plane has — TEXT, because Signal is
// its one name/value pair — and the sink must receive it with the money in the
// FIELDS: apps/destinations builds every platform's Purchase off SinkEvent.Revenue
// and SinkEvent.Currency, so a batch that arrives with those zero is a purchase
// forwarded to Meta, GA4 and the rest as worth nothing at all.
//
// Mutation proof: stop lifting the columns in planeCapture and the sink sees
// Revenue 0 and Currency "" while the raw strings sit uselessly in the properties.
func TestPlaneCapture_CarriesTheMoneyToTheFanOut(t *testing.T) {
	fakeWarehouse(t)
	got := make(chan []SinkEvent, 1)
	remove := AddSink(func(org string, evs []SinkEvent) {
		if org == "acme" {
			got <- evs
		}
	})
	defer remove()

	if _, err := planeCapture(asPeer("acme"), &planeops.EventIn{
		Name:    "order_completed",
		Product: "commerce",
		Subject: "person_42",
		Attributes: []planeops.Signal{
			{Name: "event_id", Value: "sq_pay_9x"},
			{Name: "revenue", Value: "49.5"},
			{Name: "currency", Value: "eur"},
			{Name: "productId", Value: "plan_pro"},
			{Name: "quantity", Value: "2"},
		},
	}); err != nil {
		t.Fatalf("planeCapture: %v", err)
	}

	select {
	case evs := <-got:
		if len(evs) != 1 {
			t.Fatalf("%d events reached the sink, want 1", len(evs))
		}
		ev := evs[0]
		switch {
		case ev.Name != "order_completed":
			t.Errorf("name %q, want order_completed — the name apps/destinations maps onto Purchase", ev.Name)
		case ev.Revenue != 49.5:
			t.Errorf("revenue %v, want 49.5 — a sale forwarded with no value is a conversion worth nothing", ev.Revenue)
		case ev.Currency != "eur":
			t.Errorf("currency %q, want eur — the money the customer actually paid", ev.Currency)
		case ev.ProductID != "plan_pro":
			t.Errorf("productId %q, want plan_pro", ev.ProductID)
		case ev.Quantity != 2:
			t.Errorf("quantity %d, want 2", ev.Quantity)
		case ev.DistinctID != "person_42":
			t.Errorf("distinctId %q, want person_42 — the match key every adapter hashes", ev.DistinctID)
		case ev.Properties["event_id"] != "sq_pay_9x":
			t.Errorf("event_id %v, want sq_pay_9x — the key that dedups this against the browser pixel",
				ev.Properties["event_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sink was never called — a peer's sale reached no consumer")
	}
}

// TestPlaneCapture_AnUnparseableAmountIsNoAmount. A peer that states nonsense where a
// number belongs has stated no number: it lands as the zero it already was, and the
// occurrence is still filed. Refusing the whole sale over a malformed attribute would
// lose a payment that really happened.
func TestPlaneCapture_AnUnparseableAmountIsNoAmount(t *testing.T) {
	fakeWarehouse(t)
	got := make(chan []SinkEvent, 1)
	remove := AddSink(func(org string, evs []SinkEvent) {
		if org == "acme" {
			got <- evs
		}
	})
	defer remove()

	out, err := planeCapture(asPeer("acme"), &planeops.EventIn{
		Name:       "order_completed",
		Attributes: []planeops.Signal{{Name: "revenue", Value: "several"}},
	})
	if err != nil || out.Accepted != 1 {
		t.Fatalf("planeCapture: %v, receipt %+v — the sale is still a sale", err, out)
	}
	select {
	case evs := <-got:
		if evs[0].Revenue != 0 {
			t.Errorf("revenue %v, want 0", evs[0].Revenue)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sink was never called")
	}
}
