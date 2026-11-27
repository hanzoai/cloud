// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/billinginvoice"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/hanzoai/commerce/models/subscription"
	commerceorg "github.com/hanzoai/commerce/pkg/org"

	"github.com/hanzoai/cloud/client"
)

// How a period was paid crosses the plane with the row: it is what decides whether a
// plan covers its holder's AI, so a row that drops it covers nobody.
func TestASubscriptionRowSaysHowItsPeriodWasPaid(t *testing.T) {
	for _, settled := range []string{"card", "external:square", "external:comp", ""} {
		row := subscriptionRow(&commercebilling.Subscription{ID: "sub", Status: "active", Settled: settled})
		if row.Settled != settled {
			t.Errorf("Settled %q crossed as %q", settled, row.Settled)
		}
	}
}

// The seats a period was charged for and what it was charged cross with the row, and
// so does the serving row a tier answer names: a plan reads its figures off them.
func TestARowCarriesWhatItsPeriodCharged(t *testing.T) {
	row := subscriptionRow(&commercebilling.Subscription{ID: "sub", Status: "active", Settled: "card", Quantity: 30, Seats: 2, ChargedCents: 5000})
	if row.Seats != 2 || row.ChargedCents != 5000 || row.Quantity != 30 {
		t.Errorf("row crossed as seats %d charged %d quantity %d, want 2, 5000, 30", row.Seats, row.ChargedCents, row.Quantity)
	}
	if got := tierAnswer(&commercebilling.TierView{User: "acme", Plan: "max-20x", Subscription: "sub-1"}); got.Subscription != "sub-1" || got.Plan != "max-20x" {
		t.Errorf("tier answer %+v, want it to name sub-1", got)
	}
}

// TestSubscriptionRowCarriesItsCoupon: the coupon a sale stamps on the
// subscription (Metadata "coupon", api/billing subscribe_card.go) reaches the
// subscriptions read, so a reader can tell which purchases a coupon made; a plan
// bought without one carries none.
func TestSubscriptionRowCarriesItsCoupon(t *testing.T) {
	bootRecord(t)
	ctx := context.Background()
	const buyer, plain = "coupon-buyer", "coupon-plain"
	for _, org := range []string{buyer, plain} {
		if _, err := planeSubscriptionRecord(caller(org, true), paidFor(t, org)); err != nil {
			t.Fatalf("record %s: %v", org, err)
		}
	}
	o, err := commerceorg.Resolve(ctx, buyer)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := commercebilling.ListSubscriptions(ctx, o, buyer, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	sub := subscription.New(datastore.New(o.Namespaced(ctx)))
	if err := sub.GetById(rows[0].Id()); err != nil {
		t.Fatalf("load: %v", err)
	}
	if sub.Metadata == nil {
		sub.Metadata = map[string]any{}
	}
	sub.Metadata["coupon"] = "50OFFFOUNDER"
	if err := sub.Update(); err != nil {
		t.Fatalf("stamp the coupon: %v", err)
	}

	for org, want := range map[string]string{buyer: "50OFFFOUNDER", plain: ""} {
		subs, err := planeSubscriptions(caller(org, false), &client.SubsIn{})
		if err != nil || subs.Count != 1 {
			t.Fatalf("%s: %+v, %v", org, subs, err)
		}
		if got := subs.Rows[0].Coupon; got != want {
			t.Fatalf("%s: coupon %q, want %q", org, got, want)
		}
	}
}

// When a plan was last paid crosses with the row: the admin board's Last paid column
// reads it. A plan recorded as paid outside a checkout keeps no time of its own record,
// so its last payment is the start of the period that payment covered.
func TestARecordedPlanWasLastPaidWhenItsPeriodBegan(t *testing.T) {
	bootRecord(t)
	const org = "record-last-paid"
	in := paidFor(t, org)
	if _, err := planeSubscriptionRecord(caller(org, true), in); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got, want := lastPaid(t, org), when(t, in.PeriodStart); !got.Equal(want) {
		t.Fatalf("last paid %v, want the recorded period's start %v", got, want)
	}
}

// A plan paid through an invoice was last paid when its latest invoice was paid,
// when that is later than the start of the period recorded with it.
func TestAPlanWasLastPaidWhenItsLatestInvoiceWas(t *testing.T) {
	bootRecord(t)
	const org = "invoice-last-paid"
	in := paidFor(t, org)
	got, err := planeSubscriptionRecord(caller(org, true), in)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	ctx := context.Background()
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	inv := billinginvoice.New(datastore.New(o.Namespaced(ctx)))
	inv.UserId, inv.SubscriptionId, inv.Currency = org, got.Subscription.ID, "usd"
	inv.PeriodStart, inv.PeriodEnd = when(t, in.PeriodStart), when(t, in.PeriodEnd)
	inv.LineItems = []billinginvoice.LineItem{{Id: "li_plan", Type: billinginvoice.LineSubscription, Amount: in.PriceCents, Currency: "usd"}}
	inv.RecalculateSubtotal()
	inv.SetNumber(1)
	if err := inv.Finalize(); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := inv.MarkPaid("card", "sq_later"); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if err := inv.Create(); err != nil {
		t.Fatalf("create: %v", err)
	}
	paid := paidInvoices(t, org, got.Subscription.ID)
	if len(paid) != 1 || paid[0].PaidAt.IsZero() {
		t.Fatalf("paid invoices = %d, want the one with its paid time", len(paid))
	}
	if got := lastPaid(t, org); !got.Equal(paid[0].PaidAt) {
		t.Fatalf("last paid %v, want the invoice's payment %v", got, paid[0].PaidAt)
	}
}

// lastPaid is when org's one subscription says it was last paid, read the way the org
// itself reads it.
func lastPaid(t *testing.T, org string) time.Time {
	t.Helper()
	subs, err := planeSubscriptions(caller(org, false), &client.SubsIn{})
	if err != nil || subs.Count != 1 {
		t.Fatalf("%s: %+v, %v", org, subs, err)
	}
	if subs.Rows[0].LastPaid == "" {
		t.Fatalf("%s: the row says it was never paid", org)
	}
	return when(t, subs.Rows[0].LastPaid)
}

func when(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("%q is no time: %v", s, err)
	}
	return v
}

// invoiceOf is invoice id in org's books, loaded so it can be changed: a row a query
// returns is not bound to the store and cannot be saved back.
func invoiceOf(t *testing.T, org, id string) *billinginvoice.BillingInvoice {
	t.Helper()
	ctx := context.Background()
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve %s: %v", org, err)
	}
	inv := billinginvoice.New(datastore.New(o.Namespaced(ctx)))
	if err := inv.GetById(id); err != nil {
		t.Fatalf("read invoice %s in %s: %v", id, org, err)
	}
	return inv
}

// paidInvoices is every paid invoice of subscription id in org's books.
func paidInvoices(t *testing.T, org, id string) []*billinginvoice.BillingInvoice {
	t.Helper()
	ctx := context.Background()
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve %s: %v", org, err)
	}
	var out []*billinginvoice.BillingInvoice
	if _, err := billinginvoice.Query(datastore.New(o.Namespaced(ctx))).
		Filter("SubscriptionId=", id).Filter("Status=", billinginvoice.Paid).GetAll(&out); err != nil {
		t.Fatalf("invoices of %s: %v", id, err)
	}
	return out
}

// When a subscription's invoices do not read, its plan still does: the AI gate and the
// billing page read plans through this, and a date that only helps a reader must not
// take the plan from them. The row loses what the invoices would have said — here the
// recorded period still dates it.
func TestAPlanWhoseInvoicesDoNotReadStillReads(t *testing.T) {
	bootRecord(t)
	const org = "invoices-down"
	in := paidFor(t, org)
	if _, err := planeSubscriptionRecord(caller(org, true), in); err != nil {
		t.Fatalf("record: %v", err)
	}
	was := paidInvoicesOf
	paidInvoicesOf = func(context.Context, *organization.Organization, string, string, string, int, int) ([]commercebilling.Invoice, error) {
		return nil, errors.New("invoices: the store did not answer")
	}
	t.Cleanup(func() { paidInvoicesOf = was })

	subs, err := planeSubscriptions(caller(org, false), &client.SubsIn{})
	if err != nil || subs.Count != 1 {
		t.Fatalf("a failed invoice read took the plan with it: %+v, %v", subs, err)
	}
	if got, want := when(t, subs.Rows[0].LastPaid), when(t, in.PeriodStart); !got.Equal(want) {
		t.Fatalf("last paid %v, want the recorded period's start %v", got, want)
	}
}

func TestGuestSubscriptionIsClaimedOnReceiptProof(t *testing.T) {
	bootRecord(t)
	ctx := context.Background()
	in := paidFor(t, "hanzo")
	in.Subject = "hanzo/guest-a1b2c3d4e5f6"
	recorded, err := planeSubscriptionRecord(caller("hanzo", true), in)
	if err != nil {
		t.Fatalf("record guest sub: %v", err)
	}
	subID := recorded.Subscription.ID
	o, err := commerceorg.Resolve(ctx, "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	inv := billinginvoice.New(datastore.New(o.Namespaced(ctx)))
	inv.UserId = in.Subject
	inv.SubscriptionId = subID
	inv.Status = billinginvoice.Paid
	if err := inv.Create(); err != nil {
		t.Fatalf("create guest inv: %v", err)
	}
	invID := inv.Id()

	// Wrong invoice ID does not claim the guest subscription.
	before, err := planeSubscriptions(caller("hanzo", false), &client.SubsIn{
		UserID: "hanzo/alice",
		Claim:  "hanzo/alice|" + subID + ":wrong-inv",
	})
	if err != nil || before.Count != 0 {
		t.Fatalf("wrong invoice proof claimed row: %+v, %v", before, err)
	}

	// Matching (subscriptionId, invoiceId) claims the subscription onto hanzo/alice.
	after, err := planeSubscriptions(caller("hanzo", false), &client.SubsIn{
		UserID: "hanzo/alice",
		Claim:  "hanzo/alice|" + subID + ":" + invID,
	})
	if err != nil || after.Count != 1 || after.Rows[0].ID != subID {
		t.Fatalf("expected claimed subscription for hanzo/alice, got %+v, %v", after, err)
	}
}
