package commerce

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// planClient points a Client at a stub commerce serving one canned
// /v1/billing/subscriptions body.
func planClient(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-token")
}

// Plan must READ commerce's mrrCents, not re-derive MRR from price and
// interval. It used to re-derive it, with its own copy of commerce's
// normalization and no reading of quantity at all — so a 10-seat $20/seat plan
// showed $20 here and $200 in commerce's own rollup. The interval arithmetic is
// pinned where it lives now, in commerce's api/billing.
func TestPlanReadsCommerceMRR(t *testing.T) {
	c := planClient(t, `{"subscriptions":[
		{"status":"active","mrrCents":20000,"plan":{"name":"team"}},
		{"status":"active","mrrCents":1000,"plan":{"name":"pro"}}
	]}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 21000 {
		t.Errorf("MRR = %d, want 21000 (the sum of what commerce reported)", got.MRR)
	}
	if !got.Active {
		t.Error("Active = false with an active subscription")
	}
	if got.Name != "team" {
		t.Errorf("Name = %q, want team (the first active plan)", got.Name)
	}
}

// The seat-inclusive figure must survive verbatim. This is the case the old
// re-derivation got wrong: it saw price 2000 and reported 2000.
func TestPlanDoesNotRederiveFromPrice(t *testing.T) {
	// price and interval are still on the wire; they must NOT be consulted.
	c := planClient(t, `{"subscriptions":[
		{"status":"active","mrrCents":20000,
		 "plan":{"name":"team","price":2000,"interval":"month"}}
	]}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 20000 {
		t.Errorf("MRR = %d, want 20000 — price/interval must not be re-normalized here", got.MRR)
	}
}

// Only active/trialing count; a canceled subscription contributes nothing and
// does not make the subject look subscribed.
func TestPlanIgnoresInactiveSubscriptions(t *testing.T) {
	c := planClient(t, `{"subscriptions":[
		{"status":"canceled","mrrCents":50000,"plan":{"name":"enterprise"}},
		{"status":"trialing","mrrCents":1500,"plan":{"name":"pro"}}
	]}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 1500 {
		t.Errorf("MRR = %d, want 1500 (canceled excluded)", got.MRR)
	}
	if got.Name != "pro" {
		t.Errorf("Name = %q, want pro", got.Name)
	}
}

// No subscriptions is an honest zero and "pay-as-you-go", never an error and
// never a fabricated tier.
func TestPlanWithNoSubscriptions(t *testing.T) {
	c := planClient(t, `{"subscriptions":[]}`)

	got, err := c.Plan(context.Background(), "org-1")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got.MRR != 0 || got.Active || got.Name != "pay-as-you-go" {
		t.Errorf("Plan = %+v, want {pay-as-you-go 0 false}", got)
	}
}

// Guard the decode shape itself: mrrCents is the field read. If that tag ever
// drifted from what commerce emits, every board would silently report zero
// revenue rather than fail.
func TestSubscriptionsWireReadsMRRCents(t *testing.T) {
	var w subscriptionsWire
	if err := json.Unmarshal([]byte(`{"subscriptions":[{"mrrCents":4242}]}`), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(w.Subscriptions) != 1 || w.Subscriptions[0].MRRCents != 4242 {
		t.Fatalf("decoded %+v, want one subscription with MRRCents 4242", w.Subscriptions)
	}
}
