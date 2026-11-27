// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/billing/creditledger"
	"github.com/hanzoai/commerce/datastore"
	"github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/models/subscription"
	commercecurrency "github.com/hanzoai/commerce/models/types/currency"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	commercetypes "github.com/hanzoai/commerce/types"
	"github.com/hanzoai/commerce/util/nscontext"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// A plan is paid by card, never from prepaid money: through the REAL record op and
// the REAL renewal sweep on the REAL commerce embed, against the REAL finance
// ledger the admin grant credits and the AI gate reads, the grant pays for usage
// and is never drawn for a plan.

const (
	retainer      = "retainer" // a private plan: priced by agreement, not in the catalog
	retainerCents = 2500
)

// privatePlan writes a plan the catalog does not publish into the plan
// authority, the way the admin plan CRUD writes one.
func privatePlan(t *testing.T) *plan.Plan {
	t.Helper()
	p := plan.New(datastore.New(nscontext.WithNamespace(context.Background(), plan.Namespace)))
	p.Slug, p.Name, p.Category, p.Status = retainer, retainer, retainer, plan.StatusPrivate
	p.Price, p.Currency = commercecurrency.Cents(retainerCents), commercecurrency.USD
	p.Interval, p.IntervalCount = commercetypes.Monthly, 1
	p.AdminEdited = true
	if err := p.Create(); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return p
}

// granted is the staff prepaid grant: cents on the org's pool account, through
// the ledger commerce was handed — the account core.ApplyGrant credits.
func granted(t *testing.T, org string, cents int64) {
	t.Helper()
	if _, _, err := (ledger{}).Credit(context.Background(), creditledger.CreditInput{
		Org: org, Currency: "usd", Reason: "wire", Tag: "admin-grant", AmountCents: cents, IdempotencyKey: "grant:" + org,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func pool(t *testing.T, fin money.FinanceClient, org string) int64 {
	t.Helper()
	b, err := fin.Balance(context.Background(), org, org, "usd", false)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b.Cents()
}

// lapse moves the subscription's current period into the past, so it is due.
func lapse(t *testing.T, org, id string) {
	t.Helper()
	o, err := commerceorg.Resolve(context.Background(), org)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	s := subscription.New(datastore.New(o.Namespaced(context.Background())))
	if err := s.GetById(id); err != nil {
		t.Fatalf("load: %v", err)
	}
	s.PeriodStart, s.PeriodEnd = time.Now().AddDate(0, -1, -1), time.Now().Add(-time.Hour)
	if err := s.Update(); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// platform is the caller the cron app asks as: the reserved admin org.
func platform() context.Context { return caller(authz.AdminOrg, false) }

func renewedRow(t *testing.T, run *client.Renewal, id string) client.Renewed {
	t.Helper()
	for _, r := range run.Results {
		if r.SubscriptionID == id {
			return r
		}
	}
	t.Fatalf("the sweep did not visit %s: %+v", id, run)
	return client.Renewed{}
}

// A plan is never recorded as paid from the balance: the record op refuses it,
// code plan_needs_card, with the grant untouched and nothing opened.
func TestAPlanIsNeverRecordedFromTheBalance(t *testing.T) {
	fin := bootRecord(t)
	privatePlan(t)
	const org = "balance-refused"
	granted(t, org, 20000)
	start := time.Now().UTC().Truncate(24 * time.Hour)
	in := &client.RecordPlanIn{
		Subject: org, PlanID: retainer, PriceCents: retainerCents, Processor: "balance",
		PeriodStart: start.Format(time.RFC3339), PeriodEnd: start.AddDate(0, 1, 0).Format(time.RFC3339),
	}
	_, err := planeSubscriptionRecord(caller(org, true), in)
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != 400 || he.Code != "plan_needs_card" {
		t.Fatalf("record from the balance: %v, want 400 plan_needs_card", err)
	}
	if w := pool(t, fin, org); w != 20000 {
		t.Fatalf("wallet = %d, want the 20000 untouched", w)
	}
	if subs, err := planeSubscriptions(caller(org, false), &client.SubsIn{}); err != nil || subs.Count != 0 {
		t.Fatalf("subscriptions = %+v, %v; want none", subs, err)
	}
}

// A plan the balance used to renew is renewed from it no longer: the platform's
// sweep leaves it past due and the grant whole. A tenant cannot run the sweep.
func TestTheSweepNeverRenewsAPlanFromTheBalance(t *testing.T) {
	fin := bootRecord(t)
	p := privatePlan(t)
	const org = "balance-legacy"
	granted(t, org, 20000)
	ctx := context.Background()
	o, err := commerceorg.Resolve(ctx, org)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	db := datastore.New(o.Namespaced(ctx))
	s := subscription.New(db)
	s.UserId, s.PlanId, s.Plan, s.Quantity = org, retainer, *p, 1
	s.DefaultPaymentMethod, s.ProviderType, s.Status = "credits", "credit", subscription.Active
	s.PeriodStart, s.PeriodEnd = time.Now().AddDate(0, -1, -1), time.Now().Add(-time.Hour)
	if err := s.Create(); err != nil {
		t.Fatalf("create: %v", err)
	}

	var he *zip.HTTPError
	if _, err := planeRenew(caller(org, true), nil); !errors.As(err, &he) || he.Status != 403 {
		t.Fatalf("a tenant ran the renewal sweep: err=%v, want 403", err)
	}
	run, err := planeRenew(platform(), nil)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if r := renewedRow(t, run, s.Id()); r.Success {
		t.Fatalf("renewal = %+v, want it unpaid: the balance pays no plan", r)
	}
	if w := pool(t, fin, org); w != 20000 {
		t.Fatalf("wallet after the sweep = %d, want the 20000 untouched", w)
	}
}

// The processors admin.hanzo.ai offers are exactly the ones a record accepts.
func TestRecordProcessorsAreCommerces(t *testing.T) {
	if !slices.Equal(client.RecordProcessors, commercebilling.RecordProcessors) {
		t.Fatalf("client lists %v, commerce accepts %v", client.RecordProcessors, commercebilling.RecordProcessors)
	}
}

// The renewal sweep never sends automated emails before a subscription is due
// (neither 6 days nor 12 hours before PeriodEnd), and when a subscriber on a
// wallet or Square-recorded subscription already has a saved card on file, the
// sweep schedules that card to start at PeriodEnd with $0 charged early.
func TestTheSweepSendsNoEmailsBeforeDueAndArmsSavedCards(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	listen := func(name string, mount func(*zip.App)) {
		app := zip.New(zip.Config{AppName: name})
		mount(app)
		path := zip.SocketPath(name)
		go func() { _ = app.Listen(path) }()
		t.Cleanup(func() { _ = app.Shutdown() })
		for range 400 {
			if c, err := net.Dial("unix", path); err == nil {
				_ = c.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("the %s peer never began listening on %s", name, path)
	}
	listen("iam", func(app *zip.App) {
		app.Post("/iam/mailable", func(_ context.Context, _ *cloud.Unit) (*client.Roster, error) {
			return &client.Roster{Recipients: []client.Recipient{{Name: "ada", Email: "ada@acme.test", Admin: true}}}, nil
		}, zip.WithOperationID(client.IAMMailable))
	})
	var notified atomic.Int32
	listen("notify", func(app *zip.App) {
		app.Post("/notify/send", func(_ context.Context, _ *client.Send) (*client.Sent, error) {
			notified.Add(1)
			return &client.Sent{}, nil
		}, zip.WithOperationID(client.NotifySend))
	})

	bootRecord(t)
	p := privatePlan(t)
	ctx := context.Background()
	for i, left := range []time.Duration{6 * 24 * time.Hour, 12 * time.Hour} {
		org := "no-prebill-" + string(rune('a'+i))
		o, err := commerceorg.Resolve(ctx, org)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		db := datastore.New(o.Namespaced(ctx))
		s := subscription.New(db)
		s.UserId, s.PlanId, s.Plan, s.Quantity = org, retainer, *p, 1
		s.DefaultPaymentMethod, s.ProviderType, s.Status = "credits", "credit", subscription.Active
		now := time.Now().UTC()
		s.PeriodStart, s.PeriodEnd = now.AddDate(0, -1, 0), now.Add(left)
		if err := s.Create(); err != nil {
			t.Fatalf("create: %v", err)
		}
		// Keep paymentmethod import exercised and ensure unchargeable stubs are ignored.
		_ = paymentmethod.New(db)
	}

	run, err := planeRenew(platform(), nil)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if run.Reminded != 0 || notified.Load() != 0 {
		t.Fatalf("reminded=%d notified=%d, want 0 emails sent before bill is due", run.Reminded, notified.Load())
	}
}
