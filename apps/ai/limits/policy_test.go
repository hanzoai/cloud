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
	"strconv"
	"testing"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// A plan no seed names includes the figures a SuperAdmin saved for it at
// /v1/admin/usage/policy, held as one switch per figure (flags.PlanKey).

// policy is the flag plane holding what a SuperAdmin saved: each set switch by key.
func policy(set map[string]string) Flag {
	return func(_ context.Context, _ string, in *client.FlagValueIn) (*client.FlagValue, error) {
		v, ok := set[in.Key]
		if !ok {
			return &client.FlagValue{Value: in.Default}, nil
		}
		n, _ := strconv.Atoi(v)
		on, _ := strconv.ParseBool(v)
		return &client.FlagValue{Value: v, Set: true, N: n, On: on}, nil
	}
}

// saved is the record one save of a plan's usage writes: every figure together.
func saved(set map[string]string, plan string, session, day, premium, ours int) map[string]string {
	if set == nil {
		set = map[string]string{}
	}
	for field, v := range map[string]string{
		"session": strconv.Itoa(session), "day": strconv.Itoa(day),
		"premium": strconv.Itoa(premium), "premium_window": "0", "premium_window_hours": "0",
		"ours": strconv.Itoa(ours), "ours_window": "0", "ours_window_hours": "0", "overage": "true",
	} {
		set[flags.PlanKey(field, plan)] = v
	}
	return set
}

// afterCarry is a store once the flags app's migration has run over it: every
// carried switch (flags.CarriedValues) saved where the store held nothing, the
// figures already held left as they are.
func afterCarry(set map[string]string) map[string]string {
	for k, v := range flags.CarriedValues() {
		if _, held := set[k]; !held {
			set[k] = v
		}
	}
	return set
}

// ladder is the plan authority as scripts/ladder-writes.sh leaves it: the personal
// ladder Go to Ultra on sale, Unlimited private, and the rungs it replaced archived,
// their subscribers keeping them.
func ladder(context.Context) ([]client.PlanRow, error) {
	return []client.PlanRow{
		{Slug: "free", Name: "Free", Category: "personal", Status: "active"},
		{Slug: "go", Name: "Go", Category: "personal", Price: 800, Status: "active"},
		{Slug: "pro-3x", Name: "Pro", Category: "personal", Price: 1500, Status: "active"},
		{Slug: "pro-2x", Name: "Pro", Category: "personal", Price: 1500, Status: "archived"},
		{Slug: "dev", Name: "Pro", Category: "personal", Price: 2000, Status: "archived"},
		{Slug: "dev-5x", Name: "Dev", Category: "personal", Price: 4500, Status: "active"},
		{Slug: "max-15x", Name: "Max", Category: "personal", Price: 9500, Status: "active"},
		{Slug: "max-10x", Name: "Max", Category: "personal", Price: 9500, Status: "archived"},
		{Slug: "max-5x", Name: "Max 5x", Category: "personal", Price: 10000, Status: "archived"},
		{Slug: "solo", Name: "Solo", Category: "personal", Price: 12000, Status: "active"},
		{Slug: "ultra", Name: "Ultra", Category: "personal", Price: 19500, Status: "active"},
		{Slug: "max-20x", Name: "Max 20x", Category: "personal", Price: 20000, Status: "archived"},
		{Slug: "ultimate", Name: "Ultimate", Category: "personal", Price: 49500, Status: "active"},
		{Slug: "max-50x", Name: "Max 50x", Category: "personal", Price: 50000, Status: "archived"},
		{Slug: "unlimited", Name: "Unlimited", Category: "personal", Price: 99500, Status: "active"},
		{Slug: "team", Name: "Team", Category: "team", Price: 2500, PerSeat: true, Status: "active"},
		{Slug: "team-premium", Name: "Team premium", Category: "team", Price: 10000, PerSeat: true, Status: "active"},
		{Slug: "enterprise", Name: "Enterprise", Category: "enterprise", ContactSales: true, Status: "active"},
		{Slug: "dns-pro", Name: "DNS Pro", Category: "dns", Price: 500, Status: "active"},
	}, nil
}

// onLadder is a payer whose card paid one period of a plan on the ladder, under the
// ladder's saved usage.
func onLadder(t *testing.T, plan string) *world {
	t.Helper()
	w := newWorld(t, plan, 1, "card")
	rows, _ := ladder(context.Background())
	for _, r := range rows {
		if r.Slug == plan {
			w.rows[0].ChargedCents, w.rows[0].Plan.Price = r.Price, r.Price
		}
	}
	w.l.flag = policy(personal())
	w.l.catalog = ladder
	return w
}

// goFigures are Go's figures as scripts/ladder-writes.sh saves them: 50 requests a
// session, 150 a day and 500c of each class.
var goFigures = flags.Carried{Session: 50, Day: 150, Premium: 500, Ours: 500}

// multiple is each rung on the ladder as the multiple of Go it includes, in every
// figure (hanzo-inc/cloud#565, owner 2026-11-09; Ultimate 50x, owner 2026-11-12).
// The ladder's rows in the test catalog carry one more personal plan on sale, solo:
// a plan an admin added that the policy does not name and no seed ships.
var multiple = map[string]int{"go": 1, "pro-3x": 3, "dev-5x": 5, "max-15x": 15, "ultra": 25, "ultimate": 50}

// personal is the ladder's usage as scripts/ladder-writes.sh saves it: each rung its
// multiple of Go in every figure, Unlimited unbounded in all of them, and a renamed
// slug its rung's figures (pro-2x Pro's, max-10x Max's).
func personal() map[string]string {
	set := map[string]string{}
	for plan, n := range multiple {
		saved(set, plan, n*goFigures.Session, n*goFigures.Day, n*goFigures.Premium, n*goFigures.Ours)
	}
	for from, to := range map[string]string{"pro-2x": "pro-3x", "max-10x": "max-15x"} {
		n := multiple[to]
		saved(set, from, n*goFigures.Session, n*goFigures.Day, n*goFigures.Premium, n*goFigures.Ours)
	}
	return saved(set, "unlimited", 0, 0, flags.Unbounded, flags.Unbounded)
}

// A paying customer on a plan the policy names is held to that plan's figures and
// paid for by it: never read as on no plan, never sent to the free lane. Each rung is
// its multiple of Go in every figure: Ultra is 25x Go, 1,250 requests a session,
// 3,750 a day and $125 of each class.
func TestAPlanThePolicyNamesIsItsPayersPlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for plan, m := range multiple {
			n := int64(m)
			w := onLadder(t, plan)
			s := w.read()
			if s.Plan != plan || s.Session.Limit != n*int64(goFigures.Session) || s.Day.Limit != n*int64(goFigures.Day) ||
				s.Classes[ClassPremium].Period.Limit != n*int64(goFigures.Premium)*nanosPerCent || ours(s).Limit != n*int64(goFigures.Ours)*nanosPerCent {
				t.Fatalf("%s reads %+v, want %d times Go's figures", plan, s, n)
			}
			if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPlan || g.Plan != plan {
				t.Fatalf("%s: grant %+v hit %+v, want the plan paying", plan, g, hit)
			}
			if g, hit := w.freeLane(); hit != nil || g == nil || g.Plan != plan {
				t.Fatalf("%s: the free lane answered %+v %+v, want the plan's windows counting it", plan, g, hit)
			}
		}
	})
}

// A plan the policy does not name includes nothing: its payer is read as on no plan.
// A plan is named only when every one of its figures is saved, so a save that stopped
// part-way names nothing. That is why the admin op refuses to put such a plan on sale.
func TestAPlanThePolicyDoesNotNameIncludesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := onLadder(t, "solo")
		w.l.flag = policy(nil)
		if s := w.read(); s.Plan != "" {
			t.Fatalf("an unnamed plan reads as %q, want no plan", s.Plan)
		}
		for _, missing := range flags.PlanFields {
			w := onLadder(t, "solo")
			part := saved(personal(), "solo", 100, 300, 1000, 1000)
			delete(part, flags.PlanKey(missing, "solo"))
			w.l.flag = policy(part)
			if s := w.read(); s.Plan != "" {
				t.Errorf("solo saved without its %s reads as %q, want no plan", missing, s.Plan)
			}
		}
	})
}

// down is a flag plane that cannot answer.
func down(context.Context, string, *client.FlagValueIn) (*client.FlagValue, error) {
	return nil, errors.New("flag plane down")
}

// A payer on a plan the policy names keeps the plan's figures through a flag plane
// that cannot answer, or that answers every switch as unset: never read as on no
// plan, never refused. What the plane last answered is kept in the limits store, so a
// limiter opened again during the outage holds the same figures. A plan never saved
// is still no plan.
func TestAFlagOutageKeepsANamedPlansFigures(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		want := func(t *testing.T, l *Limiter, how string) {
			t.Helper()
			s, err := l.Read(context.Background(), "acme", "acme")
			if err != nil || s.Plan != "go" || s.Session.Limit != int64(goFigures.Session) || s.Day.Limit != int64(goFigures.Day) ||
				ours(s).Limit != int64(goFigures.Ours)*nanosPerCent {
				t.Fatalf("%s: go reads %+v, %v; want Go's saved figures", how, s, err)
			}
		}
		dir := t.TempDir()
		w := onLadder(t, "go")
		open := func() *Limiter {
			l, err := Open(dir, w.l.rung, w.l.subs, ladder, w.l.funds, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = l.store.Close() })
			l.now = w.l.now
			return l
		}
		l := open()
		l.flag = policy(personal())
		want(t, l, "the flag plane answering")

		for how, flag := range map[string]Flag{"a flag plane that cannot answer": down, "a flag plane answering unset": policy(nil)} {
			l.flag = flag
			l.plans = map[string]cachedPlan{}
			want(t, l, how)
			if g, hit, err := l.Admit(context.Background(), Ask{Subject: "acme", Org: "acme", Model: premium, Class: ClassPremium, Priced: true, Spend: true}); err != nil || hit != nil || g == nil || g.Pays != PaysPlan {
				t.Fatalf("%s: grant %+v hit %+v err %v, want the plan paying", how, g, hit, err)
			}
		}

		_ = l.store.Close()
		again := open()
		again.flag = down
		want(t, again, "a limiter opened again during the outage")

		never := onLadder(t, "solo")
		never.l.flag = down
		if s := never.read(); s.Plan != "" {
			t.Fatalf("a plan never saved reads as %q during an outage, want no plan", s.Plan)
		}
	})
}

// A plan the shipped seeds do not name and the policy never saved — a DNS plan —
// reads as no plan whether the flag plane answers or not, exactly as it always has.
func TestAPlanWithNoUsageIsNoPlanThroughAnOutage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for how, flag := range map[string]Flag{"answering": policy(nil), "down": down} {
			w := newWorld(t, "", 0, "card")
			w.rows = []client.Subscription{{PlanID: "rec-dns-pro", ID: "sub-record-id", Status: "active", Settled: "card", Seats: 1, ChargedCents: 500,
				Plan: client.SubscriptionPlan{Interval: "month", Price: 500}}}
			w.l.flag = flag
			if s := w.read(); s.Plan != "" {
				t.Errorf("flag plane %s: dns-pro reads as %q, want no plan", how, s.Plan)
			}
			if _, err := w.l.Read(context.Background(), "acme", "acme"); err != nil {
				t.Errorf("flag plane %s: %v, want no plan and no error", how, err)
			}
		}
	})
}

// Ultra is 25x Go in every figure, its request windows too (owner, 2026-11-09): the
// session window holds it to 1,250 requests and names Ultimate as the way on, and
// past its included usage the payer's prepaid money pays with nothing in between. It
// is never read as uncapped.
func TestUltraIsTwentyFiveTimesGo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := onLadder(t, "ultra")
		s := w.read()
		if s.Plan != "ultra" || s.Session.Limit != 1250 || s.Day.Limit != 3750 ||
			s.Classes[ClassPremium].Period.Limit != 12500*nanosPerCent || ours(s).Limit != 12500*nanosPerCent ||
			s.Classes[ClassPremium].Unbounded || s.Classes[ClassOurs].Unbounded {
			t.Fatalf("ultra reads %+v, want 1,250 a session, 3,750 a day and $125 of each class", s)
		}
		for i := 0; i < 1250; i++ {
			if g, hit := w.freeLane(); hit != nil || g == nil {
				t.Fatalf("request %d of the session refused: %+v", i, hit)
			}
		}
		if _, hit := w.freeLane(); hit == nil || hit.Code != CodeUsageCap || hit.Name != "session" || hit.Upgrade != "ultimate" {
			t.Fatalf("request 1,251 of the session: hit %+v, want the session window to hold it, with Ultimate the way on", hit)
		}
		w.now = w.now.Add(flags.Session(0))
		g, _ := w.priced(premium, ClassPremium)
		if g == nil || g.Pays != PaysPlan {
			t.Fatalf("a new session: grant %+v, want the plan paying", g)
		}
		w.use(g, 125*usd)
		w.cash = 3 * usd
		if g, hit := w.priced(premium, ClassPremium); hit != nil || g == nil || g.Pays != PaysPrepaid {
			t.Fatalf("past the included usage: grant %+v hit %+v, want prepaid paying", g, hit)
		}
	})
}

// Unlimited has no caps (owner, 2026-11-09): no request window and every class
// included without limit, so its payer is never refused for usage and never sent to
// prepaid. It is assigned at admin.hanzo.ai as a $0 comp, which counts in full, and it
// is never the way on from another rung: it is private, sold by nothing.
func TestUnlimitedHasNoCaps(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "unlimited", 1, "external:comp")
		w.rows[0].ChargedCents, w.rows[0].Plan.Price = 0, 99500
		w.l.flag = policy(personal())
		w.l.catalog = ladder
		s := w.read()
		if s.Plan != "unlimited" || s.Session.Limit != 0 || s.Day.Limit != 0 {
			t.Fatalf("unlimited reads %+v, want no request window", s)
		}
		for _, class := range []string{ClassPremium, ClassOurs} {
			if c := s.Classes[class]; !c.Unbounded || c.Paying != PaysPlan {
				t.Fatalf("unlimited's %s reads %+v, want unbounded and paid by the plan", class, c)
			}
		}
		for i := 0; i < 2*1250; i++ { // twice Ultra's session
			if g, hit := w.freeLane(); hit != nil || g == nil {
				t.Fatalf("request %d refused: %+v", i, hit)
			}
		}
		const million = int64(1_000_000) * 1_000_000_000 // $1,000,000 in nano-dollars
		for i := range 3 {
			g, hit := w.priced(premium, ClassPremium)
			if hit != nil || g == nil || g.Pays != PaysPlan {
				t.Fatalf("premium call %d: grant %+v hit %+v, want the plan paying", i, g, hit)
			}
			w.use(g, million)
		}
		for _, from := range []string{"", "ultra", "max-15x"} {
			if got := w.l.upgrade(context.Background(), "acme", from); got.Plan == "unlimited" {
				t.Fatalf("the way on from %q is Unlimited, which nothing sells", from)
			}
		}
		if got := w.l.upgrade(context.Background(), "acme", "unlimited"); got != (Way{}) {
			t.Fatalf("the way on from unlimited is %+v, want none", got)
		}
	})
}

// A subscription that recorded a renamed slug includes the rung that replaced it,
// whichever slug commerce's tier names: a pro-2x holder is Pro (3x Go) and a max-10x
// holder Max (15x Go), even where the policy saved nothing under the old slug, and
// is never read as on no plan.
func TestARenamedSlugIsServedAsItsRung(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for from, to := range map[string]string{"pro-2x": "pro-3x", "max-10x": "max-15x"} {
			w := onLadder(t, from)
			set := personal()
			for _, f := range flags.PlanFields {
				delete(set, flags.PlanKey(f, from))
			}
			w.l.flag = policy(set)
			s := w.read()
			n := int64(multiple[to])
			if s.Plan != to || s.Session.Limit != n*int64(goFigures.Session) || s.Day.Limit != n*int64(goFigures.Day) ||
				ours(s).Limit != n*int64(goFigures.Ours)*nanosPerCent {
				t.Errorf("a %s holder reads %+v, want %s: %d times Go's figures", from, s, to, n)
			}
		}

		// The renamed rungs are carried (flags.CarriedPlans): the flags app saves the
		// figures the binary shipped for every switch that holds nothing, before it
		// answers anything. So a store that saved only the old slug, at its old figures
		// (2x and 10x Go), is served the renamed rung's once the carry has run: no deploy
		// reads a renamed holder at the old multiple.
		for from, old := range map[string]int{"pro-2x": 2, "max-10x": 10} {
			to := flags.Renamed[from]
			w := onLadder(t, from)
			w.l.flag = policy(afterCarry(saved(nil, from, old*goFigures.Session, old*goFigures.Day, old*goFigures.Premium, old*goFigures.Ours)))
			s := w.read()
			n := int64(multiple[to])
			if s.Plan != to || s.Session.Limit != n*int64(goFigures.Session) || ours(s).Limit != n*int64(goFigures.Ours)*nanosPerCent {
				t.Errorf("with only the old slug saved, a %s holder reads %+v, want %s's shipped %d times Go's figures", from, s, to, n)
			}
		}
	})
}

// The way on is the cheapest plan on sale in the rung's category priced above it that
// the policy names; for no plan, the cheapest such plan on sale anywhere. A retired
// rung's subscribers are shown the plan on sale above what they pay.
func TestTheWayOnIsTheNextPlanOnSale(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "card")
		w.l.flag = policy(personal())
		w.l.catalog = ladder
		ctx := context.Background()
		for from, want := range map[string]Way{
			"":             {Plan: "go", Name: "Go"},
			"go":           {Plan: "pro-3x", Name: "Pro"},
			"pro-3x":       {Plan: "dev-5x", Name: "Dev"},
			"pro-2x":       {Plan: "dev-5x", Name: "Dev"},
			"dev":          {Plan: "dev-5x", Name: "Dev"},
			"dev-5x":       {Plan: "max-15x", Name: "Max"},
			"max-15x":      {Plan: "ultra", Name: "Ultra"},
			"max-10x":      {Plan: "ultra", Name: "Ultra"},
			"max-5x":       {Plan: "ultra", Name: "Ultra"},
			"max-20x":      {Plan: "ultimate", Name: "Ultimate"},
			"ultra":        {Plan: "ultimate", Name: "Ultimate"},
			"ultimate":     {Plan: "unlimited", Name: "Unlimited"},
			"max-50x":      {Plan: "unlimited", Name: "Unlimited"},
			"unlimited":    {},
			"team":         {},
			"team-premium": {},
			"dns-pro":      {},
			"no-such-plan": {},
		} {
			if got := w.l.upgrade(ctx, "acme", from); got != want {
				t.Errorf("the way on from %q is %+v, want %+v", from, got, want)
			}
		}

		// A plan on sale the policy does not name raises nothing, so it is never the
		// way on: solo, on sale between Max and Ultra, is passed over for Ultra.
		w = newWorld(t, "", 0, "card")
		w.l.flag = policy(personal())
		w.l.catalog = ladder
		if got := w.l.upgrade(ctx, "acme", "max-15x"); got != (Way{Plan: "ultra", Name: "Ultra"}) {
			t.Fatalf("the way on from max-15x is %+v, want Ultra past the unnamed solo", got)
		}
	})
}

// Until the plan authority changes, the way on is the shipped ladder's.
func TestTheShippedLadderLeadsWhereItLed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "card")
		ctx := context.Background()
		for from, want := range map[string]string{"": "dev", "dev": "max-5x", "max-5x": "max-20x", "max-20x": "max-50x", "max-50x": ""} {
			if got := w.l.upgrade(ctx, "acme", from).Plan; got != want {
				t.Errorf("the way on from %q is %q, want %q", from, got, want)
			}
		}
	})
}
