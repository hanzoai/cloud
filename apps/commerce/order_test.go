// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	commerceplan "github.com/hanzoai/commerce/models/plan"
	"github.com/hanzoai/commerce/util/test/ae"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// An organization's seat plan is restated by a SuperAdmin: the row becomes per seat
// and leaves contact sales, its order figures land in whole dollars, and a later
// restatement that names neither (the ladder's team step) keeps all of it. Order
// figures a checkout cannot offer are 400.
func TestAnOrganizationsSeatPlanIsRestated(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	super := superAdmin(ctx)
	yes, no := true, false
	if _, err := planeCreate(super, &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise",
		Interval: "month", Status: "active", ContactSales: &yes}); err != nil {
		t.Fatalf("add: %v", err)
	}
	order := &client.PlanCapacity{MinSeats: ptr(20), MaxMembers: ptr(500), SpendLimits: []int{20, 50, 100, 200, 500, 1000},
		SpendLimit: ptr(100), BalanceMonths: ptr(1), BalanceCeiling: ptr(50000)}
	row, err := planeSet(super, &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise",
		Price: 24000, AnnualTotal: 24000, Interval: "year", Status: "active", PerSeat: &yes, ContactSales: &no, Limits: order})
	if err != nil {
		t.Fatalf("restate: %v", err)
	}
	l := row.Limits
	if !row.PerSeat || row.ContactSales || row.Price != 24000 || row.AnnualTotal != 24000 || row.Interval != "year" || l == nil ||
		*l.MinSeats != 20 || *l.MaxMembers != 500 || !slices.Equal(l.SpendLimits, order.SpendLimits) || *l.SpendLimit != 100 ||
		*l.BalanceMonths != 1 || *l.BalanceCeiling != 50000 {
		t.Fatalf("restated %+v limits %+v", row, l)
	}

	// The ladder's team step restates the card's lines and names neither seat pricing
	// nor contact sales: both stay as the enterprise step left them. A save from a page
	// that draws only the capacities keeps the order figures too.
	again, err := planeSet(super, &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise",
		Price: 24000, AnnualTotal: 24000, Interval: "year", Status: "active", Features: []string{"Seat price + usage at API rates"},
		Limits: &client.PlanCapacity{MinSeats: ptr(20), MaxMembers: ptr(500)}})
	if l := again.Limits; err != nil || !again.PerSeat || again.ContactSales || l == nil ||
		!slices.Equal(l.SpendLimits, order.SpendLimits) || *l.SpendLimit != 100 || *l.BalanceMonths != 1 || *l.BalanceCeiling != 50000 {
		t.Fatalf("a restatement naming none of them: %+v, %v; want per seat, not contact sales, its order kept", again, err)
	}

	for name, c := range map[string]*client.PlanCapacity{
		"a starting limit not offered": {SpendLimits: []int{20, 50}, SpendLimit: ptr(100)},
		"limits out of order":          {SpendLimits: []int{50, 20}},
		"a limit of nothing":           {SpendLimits: []int{0, 20}},
		"a negative ceiling":           {BalanceCeiling: ptr(-1)},
		"negative months":              {BalanceMonths: ptr(-1)},
	} {
		_, err := planeSet(super, &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise",
			Price: 24000, AnnualTotal: 24000, Interval: "year", Status: "active", Limits: c})
		if statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", name, err)
		}
	}
}

// A restatement keeps the seat bounds and the order figures it leaves out, a 0
// removes one, and a row a checkout could not sell is 400: red's matrix of the saves
// a SuperAdmin client may send over the row the ladder writes.
func TestARestatementKeepsTheSeatsAndZeroRemoves(t *testing.T) {
	no := false
	held := func() *commerceplan.Plan {
		return &commerceplan.Plan{Slug: "enterprise", Category: "enterprise", PerSeat: true, Limits: &commerceplan.Limits{
			MinSeats: ptr(20), MaxMembers: ptr(500), BalanceMonths: ptr(1), BalanceCeiling: ptr(50000), FreeCredit: ptr(5)}}
	}
	set := func(l *client.PlanCapacity) *client.PlanSet {
		return &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise", Price: 24000, AnnualTotal: 24000,
			Interval: "year", Status: "active", Limits: l}
	}
	n := func(p *int) any {
		if p == nil {
			return nil
		}
		return *p
	}

	// A save that names neither seat bound keeps both, and the order and the credit.
	for name, in := range map[string]*client.PlanSet{
		"no limits":              set(nil),
		"capacities only":        set(&client.PlanCapacity{Agents: ptr(10), Bots: ptr(0)}),
		"the order figures only": set(&client.PlanCapacity{BalanceMonths: ptr(2)}),
	} {
		row := held()
		applyPlan(row, in)
		l := row.Limits
		if err := checkOrder(row); err != nil || l == nil || n(l.MinSeats) != 20 || n(l.MaxMembers) != 500 ||
			n(l.BalanceCeiling) != 50000 || n(l.FreeCredit) != 5 {
			t.Errorf("%s: %v, limits %+v; want 20..500 seats, the ceiling and the credit kept", name, err, l)
		}
	}

	// A 0 removes a figure, so a plan can stop being an organization's, and a ceiling
	// of 0 stated on a plan that never held one makes nothing of it.
	row := held()
	applyPlan(row, set(&client.PlanCapacity{BalanceMonths: ptr(0), BalanceCeiling: ptr(0), MinSeats: ptr(0), MaxMembers: ptr(0)}))
	if err := checkOrder(row); err != nil || row.Limits.Ordered() || row.Limits.MinSeats != nil || row.Limits.MaxMembers != nil {
		t.Errorf("zeros: %v, limits %+v; want every one removed and the plan no longer ordered", err, row.Limits)
	}
	team := &commerceplan.Plan{Slug: "team", Category: "team", PerSeat: true, Limits: &commerceplan.Limits{MinSeats: ptr(2)}}
	applyPlan(team, &client.PlanSet{Slug: "team", Name: "Team", Category: "team", Price: 2500, Interval: "month", Status: "active",
		Limits: &client.PlanCapacity{BalanceCeiling: ptr(0), SpendLimit: ptr(0), SpendLimits: []int{}}})
	if err := checkOrder(team); err != nil || team.Limits.Ordered() || n(team.Limits.MinSeats) != 2 {
		t.Errorf("team: %v, limits %+v; want not ordered, its floor kept", err, team.Limits)
	}

	for name, in := range map[string]*client.PlanSet{
		"a floor above the ceiling": set(&client.PlanCapacity{MinSeats: ptr(600)}),
		"a negative floor":          set(&client.PlanCapacity{MinSeats: ptr(-5)}),
		"a negative ceiling":        set(&client.PlanCapacity{MaxMembers: ptr(-2)}),
		"a ceiling in cents":        set(&client.PlanCapacity{BalanceCeiling: ptr(5000000)}),
		"a ceiling past int64/100":  set(&client.PlanCapacity{BalanceCeiling: ptr(184467440737095517)}),
		"months with no ceiling":    set(&client.PlanCapacity{BalanceMonths: ptr(3), BalanceCeiling: ptr(0)}),
		"a spend limit in cents":    set(&client.PlanCapacity{SpendLimits: []int{1, 1 << 62}, SpendLimit: ptr(1)}),
		"a repeated spend limit":    set(&client.PlanCapacity{SpendLimits: []int{20, 20}}),
		"an order not per seat": func() *client.PlanSet {
			s := set(nil)
			s.PerSeat = &no
			return s
		}(),
	} {
		row := held()
		applyPlan(row, in)
		if err := checkOrder(row); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: %v, limits %+v; want 400", name, err, row.Limits)
		}
	}
	// -1 is a ceiling of none, as the catalog states it.
	row = held()
	applyPlan(row, set(&client.PlanCapacity{MaxMembers: ptr(-1)}))
	if err := checkOrder(row); err != nil {
		t.Errorf("no ceiling: %v", err)
	}
}

// An order is stated as the order it was — org, tab (its item_category too),
// seats, order id, payment method, setup, the cents the card was charged and the
// starting balance — and its revenue is the plan's, never the balance. The balance
// is commerce's to credit (keyed on the payment), so the screen posts nothing to
// the spendable ledger, and a replay is neither credited nor stated again.
func TestScreened_AnOrderIsStatedAndItsBalanceIsNotRevenue(t *testing.T) {
	fin := funded(t)
	quiet(t)
	seen := watchSales(t)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(func(context.Context, string, cloud.RiskQuery) (cloud.RiskVerdict, error) {
		return cloud.RiskVerdict{Action: cloud.ActionAllow}, nil
	})
	s := riskGate(luxlog.New("ordertest"))
	sale := &client.Sale{SubscriptionID: "sub_ent", EventID: "sub_ent", PlanID: "enterprise", Interval: "year",
		AmountCents: 520000, BalanceCents: 40000, BalanceID: "txn_balance", Seats: 20, Category: "enterprise",
		Currency: "usd", Status: "ok"}
	answer := &client.Sold{Sale: sale}
	core := func(context.Context, *client.SaleIn) (*client.Sold, error) { return answer, nil }
	worth := func(_ context.Context, in *client.SaleIn) (int64, string) { return 0, in.Currency }
	subject := func(in *client.SaleIn) string { return in.Subject }
	buyer := zip.WithCaller(context.Background(), zip.Caller{Org: gateOrg, User: gateUser})
	in := &client.SaleIn{SourceID: "cnon:ok", PlanID: "enterprise", Interval: "year", Quantity: 20, BalanceCents: 40000, Subject: wallet(gateOrg)}
	buy := screened(s, "/billing/subscribe", false, worth, subject, saleCleared, nil, core)

	if _, err := buy(buyer, in); err != nil {
		t.Fatalf("the order answered %v", err)
	}
	if got := spendable(t, fin, false); got != 0 {
		t.Fatalf("the screen posted %d cents of the order's balance, which commerce credits", got)
	}
	select {
	case got := <-seen:
		want := map[string]string{"org": gateOrg, "tab": "enterprise", "item_category": "enterprise", "seats": "20",
			"order_id": "sub_ent", "payment_method": "card", "setup": "new", "amount_cents": "520000",
			"balance_cents": "40000", "revenue": "4800", "item_price": "4800", "item_id": "enterprise", "item_variant": "annual"}
		for k, v := range want {
			if said(got.in, k) != v {
				t.Errorf("%s = %q, want %q", k, said(got.in, k), v)
			}
		}
		if got.in.Name != orderCompleted {
			t.Errorf("stated %q, want %q", got.in.Name, orderCompleted)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the order was never stated")
	}

	body, _ := json.Marshal(sale)
	answer = &client.Sold{Replayed: body}
	if _, err := buy(buyer, in); err != nil {
		t.Fatalf("the replay answered %v", err)
	}
	select {
	case got := <-seen:
		t.Fatalf("a replay was stated again: %+v", got.in)
	case <-time.After(200 * time.Millisecond):
	}
}

// The screen weighs an order at what the card is charged before it is charged: the
// seats at the catalog's price for the period and the starting balance beside them.
// Twenty seats at $240 a year and a $50,000 starting balance is $54,800.
func TestScreened_AnOrderIsWeighedAtItsWholeCharge(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	yes := true
	order := &client.PlanCapacity{MinSeats: ptr(20), MaxMembers: ptr(500), BalanceMonths: ptr(1), BalanceCeiling: ptr(50000)}
	if _, err := planeCreate(superAdmin(ctx), &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise",
		Price: 24000, AnnualTotal: 24000, Interval: "year", Status: "active", PerSeat: &yes, Limits: order}); err != nil {
		t.Fatalf("add: %v", err)
	}
	funded(t)
	quiet(t)
	asked := make(chan cloud.RiskQuery, 1)
	t.Cleanup(func() { cloud.SetRiskScorer(nil) })
	cloud.SetRiskScorer(func(_ context.Context, _ string, q cloud.RiskQuery) (cloud.RiskVerdict, error) {
		asked <- q
		return cloud.RiskVerdict{Action: cloud.ActionBlock}, nil
	})
	charged := false
	core := func(context.Context, *client.SaleIn) (*client.Sold, error) {
		charged = true
		return &client.Sold{}, nil
	}
	buyer := zip.WithCaller(ctx, zip.Caller{Org: gateOrg, User: gateUser})
	in := &client.SaleIn{SourceID: "cnon:ok", PlanID: "enterprise", Interval: "year", Quantity: 20, BalanceCents: 5000000,
		Subject: wallet(gateOrg)}
	_, err := screened(riskGate(luxlog.New("ordertest")), "/billing/subscribe", false, saleWorth,
		func(in *client.SaleIn) string { return in.Subject }, saleCleared, nil, core)(buyer, in)
	if err == nil || charged {
		t.Fatalf("a denied order answered %v, charged %v; want refused before the card", err, charged)
	}
	if got, want := (<-asked).Signals[client.SignalNano], "54800000000000"; got != want {
		t.Fatalf("the screen was asked about %s nano-USD, want %s: 20 x $240 + $50,000", got, want)
	}

	if _, err := planeCreate(superAdmin(ctx), &client.PlanSet{Slug: "flat", Name: "Flat", Category: "team",
		Price: 1900, AnnualTotal: 19000, Interval: "month", Status: "active"}); err != nil {
		t.Fatalf("add flat: %v", err)
	}
	for name, tc := range map[string]struct {
		in   client.SaleIn
		want int64
	}{
		"a month of each seat":  {client.SaleIn{PlanID: "enterprise", Quantity: 3}, 3 * 24000},
		"seats on a flat plan":  {client.SaleIn{PlanID: "flat", Quantity: 9}, 1900},
		"a year of a flat plan": {client.SaleIn{PlanID: "flat", Interval: "year"}, 19000},
		"a level it lacks":      {client.SaleIn{PlanID: "flat", Level: 4, BalanceCents: 100}, 100},
		"a plan with no row":    {client.SaleIn{PlanID: "nope", BalanceCents: 40000}, 40000},
		"a negative balance":    {client.SaleIn{PlanID: "nope", BalanceCents: -500}, 0},
	} {
		if got, _ := saleWorth(ctx, &tc.in); got != tc.want {
			t.Errorf("%s: worth %d cents, want %d", name, got, tc.want)
		}
	}
}

// An organization's order is sold only for an organization the buyer chose and, by
// IAM's answer today, administers: one who selected none would buy it for their
// default org (400), one IAM no longer names an admin is 403 whatever their token
// says, and one IAM cannot answer for is 503, all before the card. IAM is not asked
// about an order that chose no org. A plan no organization orders, and a plan with no
// row, are not this check's.
func TestAnOrderIsSoldOnlyForAChosenOrganization(t *testing.T) {
	ctx := ae.NewContext()
	defer ctx.Close()
	yes := true
	super := superAdmin(ctx)
	if _, err := planeCreate(super, &client.PlanSet{Slug: "enterprise", Name: "Enterprise", Category: "enterprise",
		Price: 24000, AnnualTotal: 24000, Interval: "year", Status: "active", PerSeat: &yes,
		Limits: &client.PlanCapacity{MinSeats: ptr(20), MaxMembers: ptr(500), BalanceMonths: ptr(1), BalanceCeiling: ptr(50000)}}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := planeCreate(super, &client.PlanSet{Slug: "crew", Name: "Crew", Category: "team", Price: 2500,
		Interval: "month", Status: "active", PerSeat: &yes, Limits: &client.PlanCapacity{MinSeats: ptr(2)}}); err != nil {
		t.Fatalf("add: %v", err)
	}
	prior := administers
	t.Cleanup(func() { administers = prior })
	buyer := zip.WithCaller(ctx, zip.Caller{Org: "acme", User: "ada", OrgAdmin: true})
	for _, tc := range []struct {
		in     client.SaleIn
		admin  bool
		iam    error
		want   int
		asksIt bool
	}{
		{client.SaleIn{PlanID: "enterprise"}, true, nil, http.StatusBadRequest, false},
		{client.SaleIn{PlanID: " enterprise "}, true, nil, http.StatusBadRequest, false},
		{client.SaleIn{PlanID: "enterprise", Chosen: true}, true, nil, 0, true},
		{client.SaleIn{PlanID: "enterprise", Chosen: true}, false, nil, http.StatusForbidden, true},
		{client.SaleIn{PlanID: "enterprise", Chosen: true}, false, errors.New("iam down"), http.StatusServiceUnavailable, true},
		{client.SaleIn{PlanID: "crew"}, false, nil, 0, false},
		{client.SaleIn{PlanID: "nope"}, false, nil, 0, false},
	} {
		asked := false
		administers = func(context.Context) (bool, error) { asked = true; return tc.admin, tc.iam }
		err := orderable(buyer, &tc.in)
		if got := statusOf(err); (tc.want == 0 && err != nil) || (tc.want != 0 && got != tc.want) || asked != tc.asksIt {
			t.Errorf("%+v, IAM admin %v err %v: %v, asked %v; want %d, asked %v", tc.in, tc.admin, tc.iam, err, asked, tc.want, tc.asksIt)
		}
	}
}

// A replayed sale answers the first sale's bytes exactly: the receipt commerce
// sealed, rendered as the fresh answer was, eventId and test included.
func TestSaleView_AReplayIsTheFirstAnswer(t *testing.T) {
	sale := commercebilling.Sale{SubscriptionID: "sub_ent", InvoiceID: "inv_1", PlanID: "enterprise", Interval: "year",
		PaymentMethodID: "pm_1", AmountCents: 520000, BalanceCents: 40000, BalanceID: "txn_1", Seats: 20,
		Category: "enterprise", Currency: "usd", Status: "ok"}
	fresh, _ := json.Marshal(saleView(&sale, true))
	sealed, _ := json.Marshal(sale)
	var prev commercebilling.Sale
	if err := json.Unmarshal(sealed, &prev); err != nil {
		t.Fatal(err)
	}
	replay, _ := json.Marshal(saleView(&prev, true))
	if string(fresh) != string(replay) || !strings.Contains(string(fresh), `"eventId":"sub_ent"`) || !strings.Contains(string(fresh), `"test":true`) {
		t.Fatalf("fresh %s\nreplay %s; want the same bytes, eventId and test included", fresh, replay)
	}
}

// How a sale was paid and whether it replaced a plan are read off the request and
// the receipt, never invented.
func TestGoodsOf_PaymentAndSetup(t *testing.T) {
	for _, tc := range []struct {
		in            client.SaleIn
		replaced      string
		method, setup string
	}{
		{client.SaleIn{SourceID: "cnon:ok"}, "", "card", "new"},
		{client.SaleIn{MethodID: "pm_1"}, "sub_team", "card", "upgrade"},
		{client.SaleIn{SourceID: "wnon:apple", Wallet: true}, "", "wallet", "new"},
	} {
		g := goodsOf(&tc.in, &client.Sale{Category: "team", Seats: 3, Replaced: tc.replaced})
		if g.method != tc.method || g.setup != tc.setup || g.tab != "team" || g.seats != 3 {
			t.Errorf("%+v: %+v, want %s/%s", tc.in, g, tc.method, tc.setup)
		}
	}
}
