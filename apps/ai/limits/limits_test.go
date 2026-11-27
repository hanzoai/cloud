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

package limits

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"

	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// A test world: one org on one plan, its wallet, and a movable clock.
type world struct {
	t        *testing.T
	l        *Limiter
	dir      string // the limits store's directory, to open it again
	now      time.Time
	rows     []client.Subscription
	subsErr  error
	cash     int64 // nano-dollars the payer paid in
	credit   int64 // nano-dollars the platform granted
	fundsErr error
}

var periodStart = time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

const usd = 1_000_000_000 // nano-dollars in a dollar

// listPrice is each rung's monthly list price per seat, in cents.
var listPrice = map[string]int64{"dev": 2000, "max-5x": 10000, "max-20x": 20000, "max-50x": 50000, "team": 2500,
	"team-premium": 10000, "agency": 20000, "advisory": 499900, "dedicated": 999900}

// shipped is the plan authority as the shipped catalog seeds it: what the way on
// from a rung is chosen among until a SuperAdmin changes it.
func shipped(context.Context) ([]client.PlanRow, error) {
	return []client.PlanRow{
		{Slug: "free", Name: "Free", Category: "personal", Status: "active"},
		{Slug: "dev", Name: "Pro", Category: "personal", Price: 2000, Status: "active"},
		{Slug: "max-5x", Name: "Max 5x", Category: "personal", Price: 10000, Status: "active"},
		{Slug: "max-20x", Name: "Max 20x", Category: "personal", Price: 20000, Status: "active"},
		{Slug: "max-50x", Name: "Max 50x", Category: "personal", Price: 50000, Status: "active"},
		{Slug: "max-100x", Name: "Max 100x", Category: "personal", Price: 100000, Status: "draft"},
		{Slug: "team", Name: "Team", Category: "team", Price: 2500, PerSeat: true, Status: "active"},
		{Slug: "team-premium", Name: "Team premium", Category: "team", Price: 10000, PerSeat: true, Status: "active"},
		{Slug: "advisory", Name: "Advisory", Category: "agency", Price: 499900, Status: "active"},
		{Slug: "dedicated", Name: "Dedicated", Category: "agency", Price: 999900, Status: "active"},
		{Slug: "enterprise", Name: "Enterprise", Category: "enterprise", ContactSales: true, Status: "active"},
		{Slug: "dns-pro", Name: "DNS Pro", Category: "dns", Price: 500, Status: "active"},
	}, nil
}

func newWorld(t *testing.T, planID string, seats int, settled string) *world {
	t.Helper()
	w := &world{t: t, now: periodStart.Add(26*time.Hour + 30*time.Minute)}
	if planID != "" {
		w.rows = []client.Subscription{{
			PlanID: "rec-" + planID, Status: "active", Quantity: seats, ID: "sub-record-id", ProviderType: "square", Settled: settled,
			Seats: seats, ChargedCents: listPrice[planID] * int64(max(1, seats)),
			CurrentPeriodStart: periodStart.Format(time.RFC3339),
			CurrentPeriodEnd:   periodStart.AddDate(0, 1, 0).Format(time.RFC3339),
			Plan:               client.SubscriptionPlan{Interval: "month", Price: listPrice[planID]},
		}}
		if !paid(settled) {
			w.rows[0].Seats, w.rows[0].ChargedCents = 0, 0
		}
	}
	w.dir = t.TempDir()
	empty(t, w.dir)
	l, err := Open(w.dir,
		func(context.Context, string, string) (string, string, error) {
			// commerce serves the rung by slug and names the row that serves it
			if len(w.rows) == 0 {
				return "", "", w.subsErr
			}
			return strings.TrimPrefix(w.rows[0].PlanID, "rec-"), w.rows[0].ID, w.subsErr
		},
		func(context.Context, string, string) ([]client.Subscription, error) { return w.rows, w.subsErr },
		shipped,
		func(context.Context, string, string) (int64, int64, error) { return w.cash, w.credit, w.fundsErr },
		// The usage policy as the one migration left every deployment: each carried
		// plan's figures saved, and the model policy with each costly family's share.
		policy(flags.CarriedValues()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.store.Close() })
	l.now = func() time.Time { return w.now }
	w.l = l
	return w
}

// empty leaves the limits store with no rows. A world is a limiter over a store of its
// own, and a file in a directory of its own was. On hanzo-sql there is one database per
// test, so a second world in one test would find the first's requests, spend and the
// flag plane's last values: each starts by clearing them.
func empty(t *testing.T, dir string) {
	t.Helper()
	s, err := openLimits(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"request", "session", "spend", "share", "known", "cap", "keyspend", "rate"} {
		if _, err := s.db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatalf("empty %s: %v", table, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// premium is a third-party model no rule names; free is the free lane.
const (
	premium = "openai/gpt-5.4-mini"
	free    = "enso-free"
)

// admit decides one request from acme unless q says otherwise.
func (w *world) admit(q Ask) (*Grant, *Hit) {
	w.t.Helper()
	q.Subject, q.Org = "acme", "acme"
	g, hit, err := w.l.Admit(context.Background(), q)
	if err != nil {
		w.t.Fatal(err)
	}
	return g, hit
}

// priced asks for a priced model in the class the catalog reads it in.
func (w *world) priced(model, class string) (*Grant, *Hit) {
	w.t.Helper()
	return w.admit(Ask{Model: model, Class: class, Priced: true, Spend: true})
}

// freeLane asks for the free model.
func (w *world) freeLane() (*Grant, *Hit) {
	w.t.Helper()
	return w.admit(Ask{Model: free, Class: ClassFree, Family: "enso", Spend: true})
}

// use settles a grant at nanos.
func (w *world) use(g *Grant, nanos int64) {
	w.t.Helper()
	if err := w.l.Settle(context.Background(), g, nanos); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) read() Standing {
	w.t.Helper()
	s, err := w.l.Read(context.Background(), "acme", "acme")
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

// capped asks for a premium model under a model policy of the test's own.
func (w *world) capped(rs map[string]Rule, model string) (*Grant, *Hit) {
	w.t.Helper()
	prev := w.l.policy
	w.l.policy = func(context.Context, string) (map[string]Rule, error) { return rs, nil }
	defer func() { w.l.policy = prev }()
	return w.priced(model, ClassPremium)
}

// ours is the standing's period window on Hanzo's models.
func ours(s Standing) Window { return s.Classes[ClassOurs].Period }

// The seeds are the owner's numbers. A Max rung includes the multiple of Pro its
// name carries, in every figure: Max 20x ($200) is 20x Pro — $100 of premium models,
// $100 of Hanzo's, 400 requests a session and 1,000 a day — and Max 50x ($500) is 50x
// Pro, 1,000 and 2,500. Every other rung includes at most half its price, so it keeps
// at least half as margin at list price; Max 20x and Max 50x are held to their names
// instead, which is their whole price, and never more.
func TestTheSeedsMakeEachMaxItsMultipleOfPro(t *testing.T) {
	pro := flags.CarriedPlans["dev"]
	for plan, n := range map[string]int{"max-5x": 5, "max-20x": 20, "max-50x": 50} {
		want := flags.Carried{Session: n * pro.Session, Day: n * pro.Day, Premium: n * pro.Premium, Ours: n * pro.Ours}
		if s := flags.CarriedPlans[plan]; s != want {
			t.Fatalf("%s seeds %+v, want %dx Pro's %+v", plan, s, n, want)
		}
	}
	// Team's standard seat is Pro's usage per member and its premium seat Max 5x's:
	// 5x a standard seat, in every figure (owner, 2026-10-16).
	if s := flags.CarriedPlans["team"]; s != pro {
		t.Fatalf("a standard team seat seeds %+v, want Pro's %+v", s, pro)
	}
	if s, want := flags.CarriedPlans["team-premium"], flags.CarriedPlans["max-5x"]; s != want {
		t.Fatalf("a premium team seat seeds %+v, want 5x a standard seat, %+v", s, want)
	}
	for plan, cents := range listPrice {
		s, ok := flags.CarriedPlans[plan]
		if !ok {
			t.Fatalf("no seeds for %s", plan)
		}
		if int64(s.Premium+s.Ours) > cents || (plan != "max-20x" && plan != "max-50x" && int64(s.Premium+s.Ours)*2 > cents) {
			t.Errorf("%s includes %d¢ of its %d¢ price", plan, s.Premium+s.Ours, cents)
		}
		if s.Session <= 0 || s.Day <= 0 || s.Session > s.Day {
			t.Errorf("%s windows %+v: a session must be positive and no larger than a day", plan, s)
		}
	}
}

func TestTheServedRungNamesThePlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// A rung bought by the year reads the same figures as by the month: the term is
		// not part of the name.
		for served, want := range map[string]string{
			"max-20x": "max-20x", "max-50x": "max-50x", "max": "max-5x", "team": "team", "team-premium": "team-premium", "dev": "dev",
			"agency": "agency", "advisory": "advisory", "dedicated": "dedicated",
			"enterprise": "", "free": "", "": "",
		} {
			w := newWorld(t, "", 0, "card")
			if got := w.l.ladder(context.Background(), "acme", served); got != want {
				t.Errorf("ladder(%q) = %q, want %q", served, got, want)
			}
		}
	})
}

// A plan counts when a card paid its period, or when SuperAdmins recorded it —
// Square, wire, or a comp. A plan bought with credit or from the balance, or one
// whose period nobody paid, confers its tier and includes nothing.
func TestWhichPlansCount(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, c := range []struct {
			settled string
			counts  bool
		}{
			{"card", true},
			{"wallet", true},
			{"external:square", true},
			{"external:wire", true},
			{"external:comp", true},
			{"EXTERNAL:Comp", true},
			{"", false},
			{"credit", false},
			{"balance", false},
			{"mixed", false},
			{"trial", false},
			{"external:", false},
		} {
			w := newWorld(t, "max-20x", 1, c.settled)
			g, _ := w.priced(premium, ClassPremium)
			if counts := g != nil && g.Pays == PaysPlan; counts != c.counts {
				t.Errorf("settled %q: plan pays=%v (grant %+v), want %v", c.settled, counts, g, c.counts)
			}
			if got := w.read().Plan != ""; got != c.counts {
				t.Errorf("settled %q: Read names a plan=%v, want %v", c.settled, got, c.counts)
			}
		}
	})
}

// A comp is recorded at $0, so commerce charges its period nothing; it still counts,
// at its plan's full figures, never pro-rated to the $0 it was charged. A $0 period
// settled any other way still counts for nothing.
func TestACompCountsAtListPrice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for plan := range listPrice {
			seed := flags.CarriedPlans[plan]
			w := newWorld(t, plan, 1, "external:comp")
			w.rows[0].ChargedCents = 0
			if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPlan {
				t.Errorf("a $0 comp of %s, a premium model: grant %+v hit %+v, want the plan", plan, g, hit)
				continue
			}
			s := w.read()
			if s.Plan != plan || s.Session.Limit != int64(seed.Session) || s.Day.Limit != int64(seed.Day) ||
				ours(s).Limit != int64(seed.Ours)*nanosPerCent || s.Classes[ClassPremium].Period.Limit != int64(seed.Premium)*nanosPerCent {
				t.Errorf("a $0 comp of %s reads plan %q, %d/%d, ours %d¢, premium %d¢; want its full %+v", plan, s.Plan,
					s.Session.Limit, s.Day.Limit, ours(s).Limit/nanosPerCent, s.Classes[ClassPremium].Period.Limit/nanosPerCent, seed)
			}
		}

		w := newWorld(t, "max-20x", 1, "card")
		w.rows[0].ChargedCents = 0
		if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("a $0 card period, a premium model: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
	})
}

// The row commerce names as serving the rung decides: a card-paid Pro row beside it
// lends it nothing.
func TestOnlyTheServingRowsPaymentCounts(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "credit")
		w.rows = append(w.rows, client.Subscription{ID: "sub-dev", PlanID: "rec-dev", Status: "active", Quantity: 1, Settled: "card", Seats: 1, ChargedCents: 2000,
			Plan: client.SubscriptionPlan{Price: 2000, Interval: "month"}})
		if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("no counted plan, an empty wallet, a premium model: %+v %+v, want insufficient_balance", g, hit)
		}
		if s := w.read(); s.Plan != "" {
			t.Fatalf("Read names plan %q, want none", s.Plan)
		}
	})
}

// A plan's included usage pays for a request from any channel: an API key's request
// is the plan's exactly as a consumer app's is.
func TestAPlanCoversEveryChannel(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		for _, apps := range [][]string{nil, {"hanzo-app"}, {"hanzo-platform"}} {
			g, hit := w.admit(Ask{Model: premium, Class: ClassPremium, Priced: true, Apps: apps})
			if hit != nil || g == nil || g.Pays != PaysPlan {
				t.Errorf("apps %v: grant %+v hit %+v, want the plan", apps, g, hit)
			}
		}
	})
}

// Max under its allowance: a premium request is paid by the plan, settles its list
// price against premium, and the bar reads the share used, rounded up to five.
func TestMaxUnderItsAllowanceIsPaidByThePlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		g, hit := w.priced(premium, ClassPremium)
		if hit != nil || g == nil || g.Pays != PaysPlan || g.Class != ClassPremium || g.State != "ok" || g.Spend != 100*usd {
			t.Fatalf("grant %+v hit %+v, want the plan paying premium with $100 left", g, hit)
		}
		w.use(g, 20*usd)
		s := w.read()
		if p := s.Classes[ClassPremium].Period; p.Used != 20*usd || p.Percent() != 20 || s.Classes[ClassPremium].Paying != PaysPlan {
			t.Fatalf("premium %+v at %d%% paying %q, want $20 used, 20%%, the plan", p, p.Percent(), s.Classes[ClassPremium].Paying)
		}
		if ours(s).Used != 0 {
			t.Fatalf("premium use was counted against ours: %+v", ours(s))
		}
	})
}

// Past the allowance with prepaid cash, prepaid pays, with no choice to make: usage
// credits a paid plan bought pay for the model it asked for. The class reads near and
// paid by credits, and a premium model, which granted credit never pays for, is
// debited from cash alone.
func TestPastTheAllowancePrepaidPays(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		g, _ := w.priced(premium, ClassPremium)
		w.use(g, 100*usd)
		w.cash = 3 * usd
		g, hit := w.priced(premium, ClassPremium)
		if hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash || g.State != "near" {
			t.Fatalf("grant %+v hit %+v, want prepaid paying, cash only, near", g, hit)
		}
		if s := w.read(); !s.Funded || s.Classes[ClassPremium].Paying != PaysCredits || s.Classes[ClassPremium].Period.Percent() != 100 {
			t.Fatalf("reads funded %v premium %+v", s.Funded, s.Classes[ClassPremium])
		}
	})
}

// A request the gate had no room for on the plan is asked past it (Past): the plan's
// included usage is not offered, so the payer's cash pays, and with none the plan's
// refusal says when its usage reopens. Nothing is counted against the plan.
func TestARequestPastThePlanIsPaidByCashOrRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		past := Ask{Model: premium, Class: ClassPremium, Priced: true, Spend: true, Past: true}
		g, hit := w.admit(past)
		if g != nil || hit == nil || hit.Code != CodePlanAllowance || !hit.ResetsAt.Equal(periodStart.AddDate(0, 1, 0)) {
			t.Fatalf("past the plan with no cash: grant %+v hit %+v, want plan_allowance_used until the period ends", g, hit)
		}
		w.cash = usd
		if g, hit := w.admit(past); hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash || g.Counted() {
			t.Fatalf("past the plan with cash: grant %+v hit %+v, want prepaid, cash only, counting nothing", g, hit)
		}
		if s := w.read(); s.Classes[ClassPremium].Period.Used != 0 || s.Day.Used != 0 {
			t.Fatalf("a request past the plan was counted against it: %+v day %+v", s.Classes[ClassPremium].Period, s.Day)
		}
	})
}

// Past the allowance with nothing prepaid: limited mode. A premium request is refused
// plan_allowance_used naming the class and nothing else, and the free model still
// answers inside the plan's request windows.
func TestPastTheAllowanceWithNoPrepaidIsLimited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		g, _ := w.priced(premium, ClassPremium)
		w.use(g, 100*usd)
		g, hit := w.priced(premium, ClassPremium)
		if g != nil || hit == nil || hit.Code != CodePlanAllowance || hit.Class != ClassPremium || !hit.ResetsAt.Equal(periodStart.AddDate(0, 1, 0)) {
			t.Fatalf("grant %+v hit %+v, want plan_allowance_used on premium until the period ends", g, hit)
		}
		if strings.ContainsAny(hit.Message, "0123456789$") {
			t.Errorf("the refusal names a figure: %q", hit.Message)
		}
		if g, hit := w.freeLane(); hit != nil || g == nil || g.Pays != PaysPlan || g.Class != ClassFree {
			t.Fatalf("the free model in limited mode: grant %+v hit %+v", g, hit)
		}
		// Hanzo's models are their own allowance and still answer.
		if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysPlan || g.Class != ClassOurs {
			t.Fatalf("kai with ours untouched: grant %+v hit %+v", g, hit)
		}
		// Granted credit never pays for a premium model.
		w.credit = usd
		if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodePlanAllowance {
			t.Fatalf("premium on granted credit: grant %+v hit %+v, want plan_allowance_used", g, hit)
		}
	})
}

// Granted credit never pays for Jev. A free plan holding only credit is told Jev
// needs a paid plan or prepaid; a Max plan whose included usage is used is in limited
// mode; prepaid cash pays and the debit is cash only.
func TestGrantsNeverPayForJev(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		free := newWorld(t, "", 0, "")
		free.credit = 100 * usd
		for _, jev := range []string{"typesafe/jev-1.13", "~typesafe/jev-latest"} {
			if g, hit := free.priced(jev, ClassOurs); g != nil || hit == nil || hit.Code != CodeInsufficient || !strings.HasPrefix(hit.Message, "Jev is paid from funds you add") {
				t.Fatalf("%s on credit alone: grant %+v hit %+v, want insufficient_balance", jev, g, hit)
			}
		}
		max := newWorld(t, "max-20x", 1, "card")
		max.credit = 100 * usd
		g, _ := max.priced("typesafe/jev-1.13", ClassOurs)
		if g == nil || g.Pays != PaysPlan {
			t.Fatalf("jev on max's included usage: %+v", g)
		}
		max.use(g, 100*usd)
		if g, hit := max.priced("typesafe/jev-1.13", ClassOurs); g != nil || hit == nil || hit.Code != CodePlanAllowance {
			t.Fatalf("jev past max's included usage on credit alone: grant %+v hit %+v, want plan_allowance_used", g, hit)
		}
		max.cash = usd
		if g, hit := max.priced("typesafe/jev-1.13", ClassOurs); hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash {
			t.Fatalf("jev on prepaid: grant %+v hit %+v, want prepaid, cash only", g, hit)
		}
	})
}

// A free plan asks Kai 500 times a day: each inside the cap is free with an empty
// wallet, or drawn from granted credit when it holds some; the 501st is
// refused free_plan_cap until midnight UTC. Prepaid cash is uncapped.
func TestTheFreePlanKaiCapIsFifty(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		for i := 1; i <= 500; i++ {
			g, hit := w.priced("kai", ClassOurs)
			if hit != nil || g == nil || g.Pays != PaysFree {
				t.Fatalf("kai request %d: grant %+v hit %+v, want free", i, g, hit)
			}
			if i == 400 && g.State != "near" {
				t.Errorf("request 400 of 500 reads %q, want near", g.State)
			}
		}
		g, hit := w.priced("kai", ClassOurs)
		if g != nil || hit == nil || hit.Code != CodeFreePlanCap || !hit.ResetsAt.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("kai request 501: grant %+v hit %+v, want free_plan_cap until midnight", g, hit)
		}
		if hit.Message != "Free plan: today's Kai requests are used. Upgrade for more:" {
			t.Errorf("message %q", hit.Message)
		}
		w.cash = usd
		if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysPrepaid || g.Cash {
			t.Fatalf("kai on prepaid past the cap: grant %+v hit %+v", g, hit)
		}

		credited := newWorld(t, "", 0, "")
		credited.credit = usd
		if g, _ := credited.priced("kai", ClassOurs); g == nil || g.Pays != PaysCredits {
			t.Fatalf("kai inside the cap with granted credit: %+v, want credits", g)
		}
		w.now = w.now.Add(24 * time.Hour)
		w.cash = 0
		if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysFree {
			t.Fatalf("kai the next day: grant %+v hit %+v", g, hit)
		}
	})
}

// A payer with no plan that holds granted credit pays with it for a model that takes
// credit, past that model's free daily cap and where the cap is 0: Kai's 501st
// request and every Zen decision are drawn from credit, never refused free_plan_cap
// or insufficient_balance. The free requests it did not use stay for when credit is gone.
func TestGrantedCreditPaysPastTheFreeCap(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		for i := 1; i <= 500; i++ {
			if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysFree {
				t.Fatalf("kai request %d: grant %+v hit %+v, want free", i, g, hit)
			}
		}
		w.credit = usd
		for i := 501; i <= 2000; i++ {
			if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysCredits {
				t.Fatalf("kai request %d on granted credit: grant %+v hit %+v, want credits", i, g, hit)
			}
		}
		if g, hit := w.priced("zenlm/zen6", ClassOurs); hit != nil || g == nil || g.Pays != PaysCredits {
			t.Fatalf("zen decision on granted credit: grant %+v hit %+v, want credits", g, hit)
		}

		fresh := newWorld(t, "", 0, "")
		fresh.credit = usd
		for i := 1; i <= 600; i++ {
			if g, hit := fresh.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysCredits {
				t.Fatalf("kai request %d on granted credit: grant %+v hit %+v, want credits", i, g, hit)
			}
		}
		fresh.credit = 0
		if g, hit := fresh.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysFree {
			t.Fatalf("kai once credit is gone: grant %+v hit %+v, want the untouched free cap", g, hit)
		}
		if g, hit := fresh.priced("zenlm/zen6", ClassOurs); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("zen decision with neither credit nor cash: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
	})
}

// Past a paid plan's included usage, granted credit pays with nothing asked of the
// payer, before a plan's daily cap on the model is read; with no credit, the cap
// serves its free requests and then refuses.
func TestPastThePlanGrantedCreditPaysBeforeTheCap(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		rules, err := ParseRules(`{"kai":{"class":"ours","grantsPay":true,"dailyCaps":{"max-20x":2}}}`)
		if err != nil {
			t.Fatal(err)
		}
		w.l.policy = func(context.Context, string) (map[string]Rule, error) { return rules, nil }
		g, _ := w.priced("kai", ClassOurs)
		w.use(g, 1_000_000*usd)
		w.credit = usd
		for i := 1; i <= 10; i++ {
			if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysCredits {
				t.Fatalf("kai %d past the plan on granted credit: grant %+v hit %+v, want credits", i, g, hit)
			}
		}
		w.credit = 0
		for i := 1; i <= 2; i++ {
			if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysFree {
				t.Fatalf("kai %d inside the plan's cap with nothing to pay: grant %+v hit %+v, want free", i, g, hit)
			}
		}
		if g, hit := w.priced("kai", ClassOurs); g != nil || hit == nil || hit.Code != CodeFreePlanCap {
			t.Fatalf("kai at the plan's cap: grant %+v hit %+v, want free_plan_cap", g, hit)
		}
	})
}

// A model no rule names takes granted credit as its class does. Hanzo's own keep the
// wallet's answer: granted credit pays, and with nothing the policy says nothing. A
// premium model is paid by cash alone: on credit it is refused, on cash it is debited
// cash only. An operator who lets credit pay for premium is obeyed.
func TestAModelNoRuleNamesTakesCreditAsItsClassDoes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const ours = "zen5-pro"
		w := newWorld(t, "", 0, "")
		if g, hit := w.priced(ours, ClassOurs); g != nil || hit != nil {
			t.Fatalf("ours, empty wallet: grant %+v hit %+v, want neither", g, hit)
		}
		if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("premium, empty wallet: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
		w.credit = usd
		if g, hit := w.priced(ours, ClassOurs); hit != nil || g == nil || g.Pays != PaysCredits {
			t.Fatalf("ours on granted credit: grant %+v hit %+v, want credits", g, hit)
		}
		if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("premium on granted credit: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
		w.cash = usd
		if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash {
			t.Fatalf("premium with cash: grant %+v hit %+v, want prepaid, cash only", g, hit)
		}
		w.cash = 0
		prev := w.l.credit
		w.l.credit = func(_ context.Context, _, class string) bool { return true }
		defer func() { w.l.credit = prev }()
		if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysCredits {
			t.Fatalf("premium on granted credit an operator allows: grant %+v hit %+v, want credits", g, hit)
		}
	})
}

// A wallet that cannot be read when the plan cannot pay leaves the decision to the
// wallet's own gate: the policy errs rather than guessing.
func TestAnUnreadableWalletIsAnError(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		w.fundsErr = errors.New("ledger down")
		if _, _, err := w.l.Admit(context.Background(), Ask{Subject: "acme", Org: "acme", Model: premium, Class: ClassPremium, Priced: true}); err == nil {
			t.Fatal("an unreadable wallet decided")
		}
		plan := newWorld(t, "max-20x", 1, "card")
		plan.fundsErr = errors.New("ledger down")
		if g, hit := plan.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPlan {
			t.Fatalf("the plan pays without reading the wallet: grant %+v hit %+v", g, hit)
		}
	})
}

// A short window binds inside the period: past it the class has nothing left until
// the window rolls, though the period does.
func TestAShortWindowBindsInsideThePeriod(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		ctx := context.Background()
		tm := terms{block: 5 * time.Hour, start: periodStart, end: periodStart.AddDate(0, 1, 0),
			allow: map[string]allowance{ClassPremium: {period: 50 * usd, window: 5 * usd, span: 5 * time.Hour}}}
		if rec, err := w.l.store.settle(ctx, "acme", "acme", ClassPremium, "", 9*usd, w.now, tm); err != nil || rec != 5*usd {
			t.Fatalf("settled %d (%v) into a $5 window, want $5", rec, err)
		}
		a, err := w.l.store.admit(ctx, "acme", "acme", ClassPremium, w.now, tm, true, true, true)
		if err != nil || a.spend != 0 {
			t.Fatalf("inside the spent window: %d left (%v), want none", a.spend, err)
		}
		a, err = w.l.store.admit(ctx, "acme", "acme", ClassPremium, w.now.Add(6*time.Hour), tm, true, true, true)
		if err != nil || a.spend != 5*usd {
			t.Fatalf("after the window rolled: %d left (%v), want $5", a.spend, err)
		}
	})
}

// The free lane counts once in the session and the day for a plan holder, and the
// bars read what was counted, rounded up to five.
func TestTheFreeLaneCountsInBothWindows(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "external:comp")
		for i := 0; i < 3; i++ {
			g, hit := w.freeLane()
			if hit != nil || g == nil || g.Plan != "max-20x" {
				t.Fatalf("request %d: grant %+v hit %+v", i, g, hit)
			}
		}
		s := w.read()
		if s.Plan != "max-20x" || s.Session.Used != 3 || s.Day.Used != 3 {
			t.Fatalf("standing %+v, want 3 used in session and day", s)
		}
		if s.Session.Limit != 1000 || s.Day.Limit != 3000 {
			t.Fatalf("limits %d/%d, want 1000/3000", s.Session.Limit, s.Day.Limit)
		}
		if want := w.now.Add(5 * time.Hour); !s.Session.ResetsAt.Equal(want) {
			t.Fatalf("session resets %v, want %v", s.Session.ResetsAt, want)
		}
		if want := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC); !s.Day.ResetsAt.Equal(want) {
			t.Fatalf("day resets %v, want %v", s.Day.ResetsAt, want)
		}
		if !s.End.Equal(periodStart.AddDate(0, 1, 0)) || s.Upgrade != (Way{Plan: "max-50x", Name: "Max 50x"}) {
			t.Fatalf("period end %v upgrade %+v", s.End, s.Upgrade)
		}
		if s.Session.Percent() != 5 || s.Day.Percent() != 5 {
			t.Fatalf("percents %d/%d, want 5/5 (3 of 800 and of 2000, rounded up to five)", s.Session.Percent(), s.Day.Percent())
		}
		none := newWorld(t, "", 0, "")
		if g, hit := none.freeLane(); g != nil || hit != nil {
			t.Fatalf("no plan: the free lane is the free allowance's, got %+v %+v", g, hit)
		}
	})
}

// A full session refuses the free lane with the moment it ends and the next plan —
// limited mode cannot be farmed; it starts again at the first request after it ended.
func TestASpentSessionRefusesUntilItEnds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "dev", 1, "card")
		limit := flags.CarriedPlans["dev"].Session
		opened := w.now
		for i := 0; i < limit; i++ {
			if _, hit := w.freeLane(); hit != nil {
				t.Fatalf("request %d refused: %+v", i, hit)
			}
			w.now = w.now.Add(time.Minute)
		}
		_, hit := w.freeLane()
		if hit == nil || hit.Code != CodeUsageCap || hit.Name != "session" || !hit.ResetsAt.Equal(opened.Add(5*time.Hour)) || hit.Upgrade != "max-5x" {
			t.Fatalf("hit %+v, want session resetting at %v with upgrade max-5x", hit, opened.Add(5*time.Hour))
		}
		if s := w.read(); s.Session.Used != int64(limit) {
			t.Fatalf("a refused request was counted: %d used", s.Session.Used)
		}
		w.now = opened.Add(5 * time.Hour)
		if _, hit := w.freeLane(); hit != nil {
			t.Fatalf("the next session refused: %+v", hit)
		}
		if s := w.read(); s.Session.Used != 1 || !s.Session.ResetsAt.Equal(w.now.Add(5*time.Hour)) {
			t.Fatalf("new session %+v, want 1 used resetting five hours on", s.Session)
		}
	})
}

// The day turns at midnight UTC whatever the session says.
func TestASpentDayRefusesUntilMidnight(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-5x", 1, "card")
		seed := flags.CarriedPlans["max-5x"]
		for used := 0; used < seed.Day; {
			for i := 0; i < seed.Session && used < seed.Day; i++ {
				if _, hit := w.freeLane(); hit != nil {
					t.Fatalf("request %d refused: %+v", used, hit)
				}
				used++
			}
			w.now = w.now.Add(5 * time.Hour)
		}
		_, hit := w.freeLane()
		midnight := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
		if hit == nil || hit.Name != "day" || !hit.ResetsAt.Equal(midnight) || hit.Upgrade != "max-20x" {
			t.Fatalf("hit %+v, want day resetting at %v, upgrade max-20x", hit, midnight)
		}
		// A priced request past the plan's windows is not the plan's: prepaid pays it.
		w.cash = usd
		if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPrepaid {
			t.Fatalf("premium past the plan's windows with prepaid: grant %+v hit %+v", g, hit)
		}
		w.now = midnight
		if _, hit := w.freeLane(); hit != nil {
			t.Fatalf("after midnight refused: %+v", hit)
		}
	})
}

// Team windows and included usage are per seat.
func TestTeamLimitsArePerSeat(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "team", 4, "card")
		s := w.read()
		seed := flags.CarriedPlans["team"]
		if s.Session.Limit != int64(4*seed.Session) || s.Day.Limit != int64(4*seed.Day) {
			t.Fatalf("limits %d/%d, want 4 seats of %+v", s.Session.Limit, s.Day.Limit, seed)
		}
		if ours(s).Limit != int64(4*seed.Ours)*nanosPerCent || s.Classes[ClassPremium].Period.Limit != int64(4*seed.Premium)*nanosPerCent {
			t.Fatalf("classes %+v, want four seats of %+v", s.Classes, seed)
		}
	})
}

// A covered family request may spend what Hanzo's models have left on paid upstream,
// and settling records it there.
func TestAFamilyRequestMaySpendWhatOursHasLeft(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "external:comp")
		g, hit := w.freeLane()
		if hit != nil || g == nil || g.Spend != 100*usd {
			t.Fatalf("grant %+v hit %+v, want $100 of paid upstream", g, hit)
		}
		w.use(g, 20*usd)
		if g, _ := w.freeLane(); g.Spend != 80*usd {
			t.Fatalf("after $20, may spend %d, want $80", g.Spend)
		}
		if s := w.read(); ours(s).Used != 20*usd {
			t.Fatalf("ours used %d, want $20", ours(s).Used)
		}
	})
}

// Settling never records past what a class includes. There is no debt.
func TestSettlingNeverPassesTheAllowance(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "dev", 1, "card")
		g, _ := w.priced("kai", ClassOurs)
		w.use(g, 50*usd)
		if used := ours(w.read()).Used; used != int64(flags.CarriedPlans["dev"].Ours)*nanosPerCent {
			t.Fatalf("used %d, want exactly ours' %d¢", used, flags.CarriedPlans["dev"].Ours)
		}
	})
}

// Concurrent requests never record more than a class includes between them.
func TestConcurrentSettlesNeverPassTheAllowance(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "dev", 1, "card")
		allow := int64(flags.CarriedPlans["dev"].Premium) * nanosPerCent
		var wg sync.WaitGroup
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				g, hit, err := w.l.Admit(context.Background(), Ask{Subject: "acme", Org: "acme", Model: premium, Class: ClassPremium, Priced: true})
				if err != nil || hit != nil || g == nil {
					return // a request admitted after the class ran out is refused, which is the point
				}
				if err := w.l.Settle(context.Background(), g, allow/10); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		used, err := w.l.store.spent(context.Background(), "acme", "acme", ClassPremium, periodStart.Unix())
		if err != nil || used != allow {
			t.Fatalf("recorded %d (%v) across concurrent requests, want exactly %d", used, err, allow)
		}
	})
}

// The allowance is the billing period's: a new period starts from nothing.
func TestTheAllowanceIsTheBillingPeriods(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "dev", 1, "card")
		g, _ := w.priced(premium, ClassPremium)
		w.use(g, 50*usd)
		if _, hit := w.priced(premium, ClassPremium); hit == nil {
			t.Fatal("a used allowance paid again")
		}
		next := periodStart.AddDate(0, 1, 0)
		w.rows[0].CurrentPeriodStart, w.rows[0].CurrentPeriodEnd = next.Format(time.RFC3339), next.AddDate(0, 1, 0).Format(time.RFC3339)
		w.now = next.Add(time.Hour)
		w.l.mu.Lock()
		clear(w.l.plans)
		w.l.mu.Unlock()
		if g, _ := w.priced(premium, ClassPremium); g == nil || g.Pays != PaysPlan {
			t.Fatal("the next period paid nothing")
		}
	})
}

// A plan that cannot be read decides nothing: priced routes and Read fail closed,
// while the free lane falls through to the free allowance counter (which holds an
// unnamed tier to the session floor).
func TestAnUnreadablePlanFailsClosed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		w.subsErr = errors.New("commerce down")
		if _, _, err := w.l.Admit(context.Background(), Ask{Subject: "acme", Org: "acme", Model: premium, Class: ClassPremium, Priced: true}); err == nil {
			t.Fatal("Admit admitted a priced model on an unreadable plan")
		}
		if g, hit, err := w.l.Admit(context.Background(), Ask{Subject: "acme", Org: "acme", Model: free, Class: ClassFree}); err != nil || g != nil || hit != nil {
			t.Fatalf("Admit on ClassFree with unreadable plan = (%+v, %+v, %v), want (nil, nil, nil) so allowance bounds it", g, hit, err)
		}
		if _, err := w.l.Read(context.Background(), "acme", "acme"); err == nil {
			t.Fatal("Read answered on an unreadable plan")
		}
	})
}

// Reserve answers what a Zen request may spend without counting it: the co-resident
// zen counts in its gate and asks in its spend.
func TestReserveAnswersWithoutCounting(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		g, err := w.l.Reserve(context.Background(), Ask{Subject: "acme", Org: "acme", Family: "zen"})
		if err != nil || g == nil || g.Spend != int64(flags.CarriedPlans["max-20x"].Ours)*nanosPerCent {
			t.Fatalf("grant %+v err %v", g, err)
		}
		if s := w.read(); s.Session.Used != 0 || s.Day.Used != 0 {
			t.Fatalf("Reserve counted a request: %+v", s)
		}
		none := newWorld(t, "", 0, "")
		if g, err := none.l.Reserve(context.Background(), Ask{Subject: "acme", Org: "acme", Family: "zen"}); err != nil || g != nil {
			t.Fatalf("without a plan: grant %+v err %v, want neither", g, err)
		}
	})
}

// Payers are apart: one org's windows and allowances never bind another's.
func TestPayersAreApart(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "dev", 1, "card")
		for i := 0; i < flags.CarriedPlans["dev"].Session; i++ {
			w.freeLane()
		}
		if _, hit, err := w.l.Admit(context.Background(), Ask{Subject: "other", Org: "other", Model: free, Class: ClassFree, Family: "enso"}); err != nil || hit != nil {
			t.Fatalf("another payer refused: %+v %v", hit, err)
		}
	})
}

// A share reads in fives, up: any use shows, and no figure can be read back out.
func TestASharedReadsInFives(t *testing.T) {
	for _, c := range []struct {
		used, limit int64
		want        int
	}{{0, 100, 0}, {1, 1000, 5}, {50, 1000, 5}, {51, 1000, 10}, {999, 1000, 100}, {5, 0, 0}, {2000, 1000, 100}} {
		if got := (Window{Used: c.used, Limit: c.limit}).Percent(); got != c.want {
			t.Errorf("%d of %d reads %d%%, want %d%%", c.used, c.limit, got, c.want)
		}
	}
}

// A capped family takes at most its share of the plan's class allowance: Max 20x
// holds $30 of its $100 of premium for Claude Opus. Past it, Opus is refused
// model_cap naming its glob and Enso, with no figure; another premium model still
// runs on the plan; and the share comes back with the billing period.
func TestACappedModelTakesOnlyItsShareOfThePlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		opus := "anthropic/claude-opus-4.7"
		g, hit := w.priced(opus, ClassPremium)
		if hit != nil || g == nil || g.Pays != PaysPlan {
			t.Fatalf("opus under its share: grant %+v hit %+v", g, hit)
		}
		w.use(g, 30*usd)
		g, hit = w.priced(opus, ClassPremium)
		if g != nil || hit == nil || hit.Code != CodeModelCap || hit.Model != "anthropic/claude-opus*" || hit.Fallback != DefaultFallback ||
			!hit.ResetsAt.Equal(periodStart.AddDate(0, 1, 0)) {
			t.Fatalf("opus past its share: grant %+v hit %+v, want model_cap on the opus glob, enso, until the period ends", g, hit)
		}
		if strings.ContainsAny(hit.Message, "0123456789$") {
			t.Errorf("the refusal names a figure: %q", hit.Message)
		}
		// Another premium model is untouched: $70 of premium is left.
		if g, hit := w.priced("openai/gpt-5.4-mini", ClassPremium); hit != nil || g == nil || g.Pays != PaysPlan {
			t.Fatalf("another premium model: grant %+v hit %+v", g, hit)
		}
		s := w.read()
		if len(s.Paused) != 1 || s.Paused[0].Model != "anthropic/claude-opus*" || s.Paused[0].Fallback != DefaultFallback {
			t.Fatalf("paused %+v", s.Paused)
		}
		// The next period, Opus has its share again.
		w.now = periodStart.AddDate(0, 1, 1)
		w.rows[0].CurrentPeriodStart = periodStart.AddDate(0, 1, 0).Format(time.RFC3339)
		w.rows[0].CurrentPeriodEnd = periodStart.AddDate(0, 2, 0).Format(time.RFC3339)
		w.l.plans = map[string]cachedPlan{}
		if g, hit := w.priced(opus, ClassPremium); hit != nil || g == nil || g.Pays != PaysPlan {
			t.Fatalf("opus in the next period: grant %+v hit %+v", g, hit)
		}
	})
}

// A spent request window refuses with that window and when it reopens, not with the
// plan's whole usage: usage_cap_exceeded until the session ends. Cash pays past it.
func TestASpentWindowNamesItselfNotThePlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		for i := 0; i < 1000; i++ { // Max 20x covers 1000 requests a session
			if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil {
				t.Fatalf("request %d: grant %+v hit %+v", i, g, hit)
			}
		}
		g, hit := w.priced(premium, ClassPremium)
		if g != nil || hit == nil || hit.Code != CodeUsageCap || hit.Name != "session" || hit.ResetsAt.IsZero() || !hit.ResetsAt.Before(periodStart.AddDate(0, 1, 0)) {
			t.Fatalf("past the session's requests: grant %+v hit %+v, want usage_cap_exceeded until the session ends", g, hit)
		}
		// Granted credit does not lift it: the window stands.
		w.credit = 10 * usd
		if g, hit := w.priced("kai", ClassOurs); g != nil || hit == nil || hit.Code != CodeUsageCap || hit.Name != "session" {
			t.Fatalf("past the session's requests on granted credit: grant %+v hit %+v, want usage_cap_exceeded", g, hit)
		}
		w.cash = usd
		if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPrepaid {
			t.Fatalf("past the session's requests with cash: grant %+v hit %+v, want prepaid", g, hit)
		}
	})
}

// Granted credit pays past the plan's included usage, with nothing asked of the payer,
// but is no payer for a request the paid lane must seat (Past): a payer holding only
// granted credit is refused there with the plan's reset, and its cash, when it has
// some, pays.
func TestPastThePlanGrantedCreditPaysNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		g, _ := w.priced("kai", ClassOurs)
		w.use(g, 100*usd)
		w.credit = 10 * usd
		if g, hit := w.priced("kai", ClassOurs); hit != nil || g == nil || g.Pays != PaysCredits {
			t.Fatalf("kai past ours on granted credit: grant %+v hit %+v, want credits", g, hit)
		}
		past := Ask{Model: "kai", Class: ClassOurs, Priced: true, Past: true}
		if g, hit := w.admit(past); g != nil || hit == nil || hit.Code != CodePlanAllowance || !hit.ResetsAt.Equal(periodStart.AddDate(0, 1, 0)) {
			t.Fatalf("kai past the plan on the paid lane: grant %+v hit %+v, want plan_allowance_used until the period ends", g, hit)
		}
		w.cash = usd
		if g, hit := w.admit(past); hit != nil || g == nil || g.Pays != PaysPrepaid {
			t.Fatalf("kai past the plan with cash: grant %+v hit %+v, want prepaid", g, hit)
		}
	})
}

// Past a model's share, cash the payer bought keeps that same model, paid by prepaid,
// with no choice to make; with none it is refused model_cap, offering nothing granted
// credit could pay for a premium model.
func TestPastItsShareCashKeepsTheSameModel(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		opus := "anthropic/claude-opus-5.5"
		g, _ := w.priced(opus, ClassPremium)
		w.use(g, 30*usd)
		if g, hit := w.priced(opus, ClassPremium); g != nil || hit == nil || hit.Code != CodeModelCap {
			t.Fatalf("past opus's share with nothing: grant %+v hit %+v, want model_cap", g, hit)
		}
		w.cash = 2 * usd
		if g, hit := w.priced(opus, ClassPremium); hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash {
			t.Fatalf("past opus's share with cash: grant %+v hit %+v, want opus on prepaid", g, hit)
		}
	})
}

// A share may bound requests a day: the next request that day is refused model_cap
// until midnight UTC.
func TestADailyShareRefusesUntilMidnight(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		raw := `{"anthropic/claude-opus*":{"class":"premium","grantsPay":true,"caps":{"max-20x":{"dailyRequests":2}},"fallback":"zen5-pro"}}`
		rules, err := ParseRules(raw)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if g, hit := w.capped(rules, "anthropic/claude-opus-4.7"); hit != nil || g == nil || g.Pays != PaysPlan {
				t.Fatalf("request %d: grant %+v hit %+v", i, g, hit)
			}
		}
		_, hit := w.capped(rules, "anthropic/claude-opus-4.7")
		if hit == nil || hit.Code != CodeModelCap || hit.Fallback != "zen5-pro" || !hit.ResetsAt.Equal(midnight(w.now)) {
			t.Fatalf("third request: %+v, want model_cap to zen5-pro until midnight", hit)
		}
	})
}

// The most specific rule wins: an exact id beats a glob, a longer glob a shorter.
func TestTheMostSpecificRuleWins(t *testing.T) {
	rs := map[string]Rule{"anthropic/*": {Class: "premium"}, "anthropic/claude-opus*": {Class: "premium", Fallback: "a"}, "anthropic/claude-opus-4.7": {Class: "ours"}}
	for model, want := range map[string]string{
		"anthropic/claude-opus-4.7": "anthropic/claude-opus-4.7",
		"anthropic/claude-opus-5":   "anthropic/claude-opus*",
		"anthropic/claude-sonnet-4": "anthropic/*",
		"openai/gpt-5":              "",
	} {
		if k, _, _ := Match(rs, model); k != want {
			t.Errorf("Match(%q) = %q, want %q", model, k, want)
		}
	}
	if !glob("openai/gpt-*-pro*", "openai/gpt-5.5-pro:batch") || glob("openai/gpt-*-pro*", "openai/gpt-5.5") {
		t.Error("glob")
	}
}

// A request that was not served gives back what it was counted against: ten
// admissions released leave every request window, daily cap, share and allowance
// as they were.
func TestUnservedRequestsLeaveEveryWindowUnchanged(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		g, _ := w.priced(premium, ClassPremium) // one served request, so the windows are open
		w.use(g, usd)
		before := w.read()
		raw := `{"anthropic/claude-opus*":{"class":"premium","grantsPay":true,"caps":{"max-20x":{"dailyRequests":3}}},"kai":{"class":"ours","grantsPay":true,"dailyCaps":{"max-20x":5}}}`
		rules, err := ParseRules(raw)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 10; i++ {
			for _, ask := range []func() (*Grant, *Hit){
				w.freeLane,
				func() (*Grant, *Hit) { return w.priced(premium, ClassPremium) },
				func() (*Grant, *Hit) { return w.capped(rules, "anthropic/claude-opus-4.7") },
			} {
				g, hit := ask()
				if hit != nil || g == nil || !g.Counted() {
					t.Fatalf("round %d: grant %+v hit %+v, want a counted grant", i, g, hit)
				}
				if err := w.l.Release(context.Background(), g); err != nil {
					t.Fatal(err)
				}
			}
		}
		after := w.read()
		if after.Session.Used != before.Session.Used || after.Day.Used != before.Day.Used ||
			after.Classes[ClassPremium].Period.Used != before.Classes[ClassPremium].Period.Used ||
			after.Classes[ClassOurs].Period.Used != before.Classes[ClassOurs].Period.Used {
			t.Fatalf("windows moved: session %d→%d day %d→%d premium %d→%d", before.Session.Used, after.Session.Used,
				before.Day.Used, after.Day.Used, before.Classes[ClassPremium].Period.Used, after.Classes[ClassPremium].Period.Used)
		}
		// The share's daily count came back too: three more opus requests are admitted.
		for i := 0; i < 3; i++ {
			if g, hit := w.capped(rules, "anthropic/claude-opus-4.7"); hit != nil || g == nil {
				t.Fatalf("opus %d after the releases: grant %+v hit %+v", i, g, hit)
			}
		}
		if _, hit := w.capped(rules, "anthropic/claude-opus-4.7"); hit == nil || hit.Code != CodeModelCap {
			t.Fatalf("the fourth opus request: %+v, want model_cap", hit)
		}
	})
}

// A request that continues one already counted — a live transcript's push — is
// decided but counted in nothing.
func TestASessionRequestCountsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		g, _ := w.priced(premium, ClassPremium)
		w.use(g, usd)
		before := w.read()
		for i := 0; i < 20; i++ {
			g, hit := w.admit(Ask{Model: "zen-scribe", Class: ClassOurs, Priced: true, Session: true})
			if hit != nil || g == nil || g.Pays != PaysPlan || g.Counted() {
				t.Fatalf("push %d: grant %+v hit %+v, want the plan paying and nothing counted", i, g, hit)
			}
		}
		if after := w.read(); after.Session.Used != before.Session.Used || after.Day.Used != before.Day.Used {
			t.Fatalf("a session's pushes moved the windows: session %d→%d day %d→%d", before.Session.Used, after.Session.Used, before.Day.Used, after.Day.Used)
		}
	})
}

// Jev is premium and Kai is Hanzo's on every path: Jev's router on chat and its
// decision model, which the catalog reads one each way, both meet the one Jev rule;
// Kai under either name meets the Kai rule, whichever class the catalog read.
func TestJevIsPremiumAndKaiOursOnEveryPath(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		for _, c := range []struct {
			q    Ask
			want string
		}{
			{Ask{Model: "typesafe/jev-router", Class: ClassPremium, Priced: true, Spend: true}, ClassPremium}, // /v1/chat/completions
			{Ask{Model: "typesafe/jev-1.13", Class: ClassOurs, Priced: true}, ClassPremium},                   // /v1/decisions
			{Ask{Model: "~typesafe/jev-latest", Class: ClassOurs, Priced: true}, ClassPremium},
			{Ask{Model: "kai", Class: ClassOurs, Priced: true}, ClassOurs},                       // /v1/decisions
			{Ask{Model: "hanzo/kai", Class: ClassPremium, Priced: true, Spend: true}, ClassOurs}, // /v1/chat/completions
		} {
			g, hit := w.admit(c.q)
			if hit != nil || g == nil || g.Class != c.want || g.Pays != PaysPlan {
				t.Fatalf("%s (catalog %s): grant %+v hit %+v, want %s paid by the plan", c.q.Model, c.q.Class, g, hit, c.want)
			}
		}
		free := newWorld(t, "", 0, "")
		free.credit = 100 * usd
		if g, hit := free.priced("typesafe/jev-router", ClassPremium); g != nil || hit == nil || hit.Code != CodeInsufficient {
			t.Fatalf("jev's router on credit alone: grant %+v hit %+v, want insufficient_balance", g, hit)
		}
	})
}

// Zen answering decisions is Hanzo's own model with no free daily allowance: the
// seeded rule matches both ids, unlike Kai's, whose free plan may ask 50 times a day.
// A glob that missed either id would leave it on the default of whoever holds credit
// or cash, which nobody chose.
func TestSeedPolicyZenDecisionsHaveNoFreeCap(t *testing.T) {
	rules, err := ParseRules(flags.SeedModelPolicy)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"zenlm/zen6", "zenlm/zen6-flash", "ZenLM/Zen6"} {
		key, rule, ok := Match(rules, model)
		if !ok || key != "zenlm/zen6*" {
			t.Fatalf("%s matched %q (found %v), want the zenlm/zen6* rule", model, key, ok)
		}
		if rule.Class != ClassOurs || !rule.GrantsPay {
			t.Errorf("%s: class %s grantsPay %v, want ours that takes granted credit", model, rule.Class, rule.GrantsPay)
		}
		if cap, has := rule.DailyCaps["free"]; !has || cap != 0 {
			t.Errorf("%s: free daily cap %d (set %v), want 0 — a free plan needs credit or cash", model, cap, has)
		}
	}
	if _, kai, _ := Match(rules, "kai"); kai.DailyCaps["free"] != 500 {
		t.Errorf("kai's free daily cap moved to %d", kai.DailyCaps["free"])
	}
}

// Speech and transcription models (zen-voice*, zen-scribe*, koe, oto) include a free
// daily allowance on the Free plan even when an existing deployment's ai_model_policy
// flag does not name them, so signed-in Free-tier users with $0 balance can use voice
// input and speech playback without a 402 refusal.
func TestFreePlanAdmitsSpeechModels(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		for _, model := range []string{"zen-voice-mini", "zen-voice-pro", "zen-scribe", "koe", "oto", "hanzo/zen-voice-mini"} {
			g, hit := w.admit(Ask{Model: model, Class: ClassOurs, Priced: true})
			if hit != nil || g == nil || g.Pays != PaysFree || !g.Covered() {
				t.Fatalf("%s on $0 Free plan: grant %+v hit %+v, want covered by PaysFree", model, g, hit)
			}
		}
	})
}
