// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/billing/creditledger"
	"github.com/hanzoai/commerce/billing/engine"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/billinginvoice"
	"github.com/hanzoai/commerce/models/subscription"
	commercecurrency "github.com/hanzoai/commerce/models/types/currency"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
)

// A plan paid outside checkout, recorded through the REAL op on the REAL commerce
// embed, with a real finance ledger beside it: who may record one, and what the
// books and the billing engine do with it afterwards.

// bootRecord boots commerce the way Mount does and publishes a ledger to watch.
func bootRecord(t *testing.T) money.FinanceClient {
	t.Helper()
	t.Setenv(network.NetworkEnv, string(network.Mainnet))
	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	fin := finance.New(finance.Local(t.TempDir(), testMaster))
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	if err := Use(zip.New(zip.Config{Logger: luxlog.New("record")}), cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if currentEmbedded() == nil {
		t.Fatal("the commerce embed did not boot; nothing here measures the served path")
	}
	return fin
}

// caller states who a plane call acts for: the tenant, and whether it is a SuperAdmin.
func caller(org string, super bool) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, Admin: super})
}

// paidFor is one payment for the go plan, covering today, at the catalog's price.
func paidFor(t *testing.T, org string) *client.RecordPlanIn {
	t.Helper()
	p, ok, err := tier(context.Background(), "go")
	if err != nil || !ok || p.Price <= 0 {
		t.Fatalf("the plan authority must sell a paid go plan: ok=%v err=%v", ok, err)
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	return &client.RecordPlanIn{
		Subject:     org,
		PlanID:      "go",
		PriceCents:  int64(p.Price),
		PeriodStart: start.Format(time.RFC3339),
		PeriodEnd:   start.AddDate(0, 1, 0).Format(time.RFC3339),
		Processor:   "square",
		Reference:   client.PaymentRef{Invoice: "inv:0-test", Payment: "pay_test"},
		Terms:       "refundable against referred business",
	}
}

func TestRecordingAPlanIsSuperAdminOnly(t *testing.T) {
	bootRecord(t)
	const org = "record-gate"
	in := paidFor(t, org)

	_, err := planeSubscriptionRecord(caller(org, false), in)
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("an org's own caller recorded a plan: err=%v, want 403", err)
	}
	c := InProcessClient().(*inProcessClient)
	if _, paid, _ := c.ActivePaidPlan(context.Background(), org); paid {
		t.Fatal("a refused record opened a paid plan anyway")
	}

	got, err := planeSubscriptionRecord(caller(org, true), in)
	if err != nil || got.Outcome != commercebilling.RecordCreated {
		t.Fatalf("SuperAdmin: outcome=%v err=%v", got, err)
	}
}

func TestAnExternallyCollectedPlanIsNeverChargedOrRenewedByTheProcessor(t *testing.T) {
	fin := bootRecord(t)
	ctx := context.Background()
	const org = "record-external"
	in := paidFor(t, org)

	got, err := planeSubscriptionRecord(caller(org, true), in)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	s := got.Subscription
	if s.Status != "active" || s.ProviderType != "square" || s.DefaultPaymentMethod != "" || s.MRRCents != in.PriceCents {
		t.Fatalf("recorded %+v, want an active square row at %d a month with no payment method", s, in.PriceCents)
	}

	// The plan is live where the gates look: the paywall and the tier.
	c := InProcessClient().(*inProcessClient)
	if slug, paid, err := c.ActivePaidPlan(ctx, org); err != nil || !paid || slug != "go" {
		t.Fatalf("ActivePaidPlan = %q, %v, %v; want go, paid", slug, paid, err)
	}
	if tr, err := planeTier(caller(org, false), &client.TierIn{Subject: org}); err != nil || tr.Tier.Name != "pro" {
		t.Fatalf("tier = %+v, %v; want the paid tier", tr, err)
	}

	// The customer's billing page and the revenue board both read it.
	if subs, err := planeSubscriptions(caller(org, false), &client.SubsIn{}); err != nil || subs.Count != 1 || subs.Rows[0].ID != s.ID {
		t.Fatalf("GET /v1/billing/subscriptions reads %+v, %v; want the recorded plan", subs, err)
	}
	if board, err := planeSubs(caller(org, true), &client.SubsIn{}); err != nil || len(board.Rows) != 1 || board.Rows[0].MRRCents != in.PriceCents {
		t.Fatalf("the revenue board reads %+v, %v; want %d of MRR", board, err, in.PriceCents)
	}

	// No money moved, in either direction.
	bal, err := fin.Balance(ctx, org, org, "usd", false)
	if err != nil || bal.Sign() != 0 {
		t.Fatalf("wallet = %v (err %v); recording a plan grants no credit and charges nothing", bal, err)
	}

	// And nothing the billing engine runs will ever charge or renew it.
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rows, err := commercebilling.ListSubscriptions(ctx, o, org, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v, want the one recorded", len(rows), err)
	}
	sub := rows[0]
	if sub.Type != subscription.External {
		t.Fatalf("type = %q, want external", sub.Type)
	}
	afterEnd := sub.PeriodEnd.Add(24 * time.Hour)
	if engine.IsDue(sub, afterEnd) {
		t.Fatal("the engine answered due for an externally collected plan")
	}
	charged := 0
	pre := &countingPrepaid{}
	inv, _, err := engine.RenewSubscription(ctx, datastore.New(o.Namespaced(ctx)), sub, pre,
		func(context.Context, *datastore.Datastore, *billinginvoice.BillingInvoice, int64) (string, error) {
			charged++
			return "ref", nil
		})
	burned := pre.calls
	if err != nil || inv != nil || burned != 0 || charged != 0 {
		t.Fatalf("renewal: invoice=%v burned=%d charged=%d err=%v; it must do nothing", inv != nil, burned, charged, err)
	}

	// A retry of the same payment changes nothing.
	again, err := planeSubscriptionRecord(caller(org, true), in)
	if err != nil || again.Outcome != commercebilling.RecordUnchanged || again.Subscription.ID != s.ID {
		t.Fatalf("retry: %+v, %v; want the same row unchanged", again, err)
	}
}

// countingPrepaid is prepaid money that holds nothing and counts every time the
// engine asks it for any.
type countingPrepaid struct{ calls int }

func (c *countingPrepaid) Available(context.Context, string, commercecurrency.Type) (int64, error) {
	c.calls++
	return 0, nil
}

func (c *countingPrepaid) Draw(context.Context, string, commercecurrency.Type, int64, string) (engine.Drawn, error) {
	c.calls++
	return engine.Drawn{}, creditledger.ErrShort
}

// One of our orgs is given a plan as a comp, through the real op: SuperAdmin only,
// price 0, the approval and the terms on the row. It reads the plan's tier, and its
// row adds nothing to MRR.
func TestAnOrgGivenACompReadsItsTier(t *testing.T) {
	bootRecord(t)
	const org = "hanzo"
	start := time.Now().UTC().Truncate(24 * time.Hour)
	comp := &client.RecordPlanIn{
		Subject:     org,
		PlanID:      "enterprise",
		PeriodStart: start.Format(time.RFC3339),
		PeriodEnd:   start.AddDate(1, 0, 0).Format(time.RFC3339),
		Processor:   "comp",
		Reference:   client.PaymentRef{Order: "comp-hanzo-2026-10"},
		Terms:       "Hanzo ecosystem org: comp Enterprise, approved by the owner",
	}
	if _, err := planeSubscriptionRecord(caller(org, false), comp); err == nil {
		t.Fatal("a member gave their org a comp")
	}
	rec, err := planeSubscriptionRecord(caller(org, true), comp)
	if err != nil {
		t.Fatalf("record the comp: %v", err)
	}
	if rec.Outcome != commercebilling.RecordCreated || rec.Subscription.MRRCents != 0 || rec.Subscription.ProviderType != "comp" {
		t.Fatalf("recorded %+v, want a created comp at MRR 0", rec)
	}
	view, err := planeTier(caller(org, false), &client.TierIn{Subject: org})
	if err != nil || view.Tier.Name != "enterprise" {
		t.Fatalf("tier %+v (%v), want enterprise", view, err)
	}
	priced := *comp
	priced.PriceCents = 100
	priced.Subject = "acme"
	if _, err := planeSubscriptionRecord(caller("acme", true), &priced); err == nil {
		t.Fatal("a comp with a price was recorded")
	}
}
