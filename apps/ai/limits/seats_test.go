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
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// A flat plan is charged once whatever quantity the buyer sent: its windows and
// budget never scale by the row's quantity.
func TestAFlatPlanNeverScalesByItsQuantity(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		w.rows[0].Quantity = 40
		if s := w.read(); ours(s).Limit != 100*usd || s.Day.Limit != 3000 {
			t.Errorf("a flat max-20x with quantity 40 reads $%d and %d a day, want $100 and 3000", ours(s).Limit/usd, s.Day.Limit)
		}
	})
}

// The seats are the ones the period was charged for, never the row's live quantity,
// which the holder may change by PATCH.
func TestSeatsAreTheOnesThePeriodWasChargedFor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "team", 2, "card")
		w.rows[0].Quantity = 30
		if s := w.read(); ours(s).Limit != 2*500*nanosPerCent || s.Day.Limit != 2*150 {
			t.Errorf("team charged for 2 seats with quantity 30 reads %d nano and %d a day, want 2 seats' $10 and 300", ours(s).Limit, s.Day.Limit)
		}
	})
}

// A team's usage is pooled: its seats' figures together, as one payer's windows.
// Two standard seats are twice Pro's: 100 a session, 300 a day.
func TestTwoStandardSeatsAreTwicePros(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newWorld(t, "team", 2, "card").read()
		if s.Plan != "team" || s.Session.Limit != 100 || s.Day.Limit != 300 ||
			ours(s).Limit != 10*usd || s.Classes[ClassPremium].Period.Limit != 10*usd {
			t.Fatalf("two standard seats read plan %q, %d/%d, ours $%d, premium $%d; want team, 100/300, $10 and $10",
				s.Plan, s.Session.Limit, s.Day.Limit, ours(s).Limit/usd, s.Classes[ClassPremium].Period.Limit/usd)
		}
		p := newWorld(t, "team-premium", 2, "card").read()
		if p.Plan != "team-premium" || p.Session.Limit != 500 || p.Day.Limit != 1500 || ours(p).Limit != 50*usd {
			t.Fatalf("two premium seats read plan %q, %d/%d, ours $%d; want team-premium, 500/1500, $50", p.Plan, p.Session.Limit, p.Day.Limit, ours(p).Limit/usd)
		}
	})
}

// mixed is a team of one standard and one premium seat, as commerce answers it: the
// row on team with one line of team-premium, each seat paid for.
func mixed(t *testing.T, charged int64) *world {
	w := newWorld(t, "team", 1, "card")
	r := &w.rows[0]
	r.Seats, r.ChargedCents = 2, charged
	r.Lines = []client.SubscriptionLine{{Plan: "team-premium", Name: "Team Premium", Price: 10000, Quantity: 1, Seats: 1}}
	return w
}

// One standard and one premium seat are 1x(50/150) + 1x(250/750), and each seat's
// budget beside it: $5 + $25 of each class.
func TestAMixedTeamPoolsEachSeatTypesFigures(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := mixed(t, 12500).read()
		if s.Plan != "team" || s.Session.Limit != 300 || s.Day.Limit != 900 {
			t.Fatalf("1 standard + 1 premium reads plan %q, %d a session and %d a day; want team, 300 and 900", s.Plan, s.Session.Limit, s.Day.Limit)
		}
		if ours(s).Limit != 30*usd || s.Classes[ClassPremium].Period.Limit != 30*usd {
			t.Fatalf("1 standard + 1 premium includes ours $%d, premium $%d; want $30 each", ours(s).Limit/usd, s.Classes[ClassPremium].Period.Limit/usd)
		}
		// A team sold at half its list price includes half of every figure.
		h := mixed(t, 6250).read()
		if h.Session.Limit != 150 || h.Day.Limit != 450 || ours(h).Limit != 15*usd {
			t.Fatalf("a half-price mixed team reads %d/%d and $%d, want 150/450 and $15", h.Session.Limit, h.Day.Limit, ours(h).Limit/usd)
		}
		// A seat type whose period was not paid for includes nothing.
		u := mixed(t, 2500)
		u.rows[0].Seats, u.rows[0].Lines[0].Seats = 1, 0
		if s := u.read(); s.Session.Limit != 50 || s.Day.Limit != 150 {
			t.Fatalf("a team whose premium seat was not paid for reads %d/%d, want the standard seat's 50/150", s.Session.Limit, s.Day.Limit)
		}
	})
}

// A model's share of a mixed team is each seat type's share of its own seat,
// together: a TopPremium family takes 30% of every seat's premium allowance.
func TestAModelsShareOfAMixedTeamIsEachSeatsShare(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := mixed(t, 12500)
		p, err := w.l.planOf(context.Background(), "acme", "acme")
		if err != nil {
			t.Fatal(err)
		}
		t0 := w.l.termsOf(context.Background(), "acme", p)
		whole := t0.seat(ClassPremium)
		rule := Rule{Caps: map[string]Cap{"team": {PeriodCents: 150, DailyRequests: 10}, "team-premium": {PeriodCents: 750, DailyRequests: 50}}}
		if c := p.cap(rule, whole); c.PeriodCents != 900 || c.DailyRequests != 60 {
			t.Fatalf("the team's share is %+v, want 900¢ and 60 a day", c)
		}
		// A seat type the rule does not bound takes at most all of its own seat: the
		// premium seat's $25 and 750 a day beside the standard seat's share.
		if c := p.cap(Rule{Caps: map[string]Cap{"team": {PeriodCents: 150, DailyRequests: 10}}}, whole); c.PeriodCents != 150+2500 || c.DailyRequests != 10+750 {
			t.Fatalf("a rule bounding only the standard seat bounds the team at %+v, want 2650¢ and 760 a day", c)
		}
		// A rule that bounds no seat type bounds nothing.
		if c := p.cap(Rule{Caps: map[string]Cap{"max-5x": {PeriodCents: 750}}}, whole); c.bounded() {
			t.Fatalf("a rule naming no seat type of the team bounds it: %+v", c)
		}
	})
}

// A model policy saved before premium seats existed names the standard seat and
// not the premium one. One premium seat beside 99 standard ones lifts nothing off
// the 99: they stay at their share, and the premium seat adds its own seat alone.
func TestOnePremiumSeatLiftsNoCapOffTheStandardSeats(t *testing.T) {
	saved := Rule{Caps: map[string]Cap{"team": {PeriodCents: 150}}}
	whole := func(id string) Cap {
		return map[string]Cap{"team": {PeriodCents: 500, DailyRequests: 50}, "team-premium": {PeriodCents: 2500, DailyRequests: 250}}[id]
	}
	std := plan{id: "team", seats: []seated{{"team", 100}}}
	mix := plan{id: "team", seats: []seated{{"team", 99}, {"team-premium", 1}}}
	if c := std.cap(saved, whole); c.PeriodCents != 15000 || c.DailyRequests != 0 {
		t.Fatalf("100 standard seats: %+v, want 15000¢ and no daily bound", c)
	}
	if c := mix.cap(saved, whole); c.PeriodCents != 99*150+2500 {
		t.Fatalf("99 standard + 1 premium under a policy naming only team: %+v, want %d¢", c, 99*150+2500)
	}
	// A seat type the rule leaves unbounded whose own seat is unbounded leaves
	// the team unbounded: there is no figure to hold it to.
	open := func(id string) Cap { return Cap{} }
	if c := mix.cap(saved, open); c.bounded() {
		t.Fatalf("an unbounded premium seat left the team bounded: %+v", c)
	}
}

// Which seat type a row names first moves nothing: a team on team with a
// premium line reads as a team on team-premium with a standard line.
func TestASeatTypesOrderMovesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a := mixed(t, 12500).read()
		w := newWorld(t, "team-premium", 1, "card")
		r := &w.rows[0]
		r.Seats, r.ChargedCents = 2, 12500
		r.Lines = []client.SubscriptionLine{{Plan: "team", Name: "Team", Price: 2500, Quantity: 1, Seats: 1}}
		b := w.read()
		if a.Session.Limit != b.Session.Limit || a.Day.Limit != b.Day.Limit || ours(a).Limit != ours(b).Limit ||
			a.Classes[ClassPremium].Period.Limit != b.Classes[ClassPremium].Period.Limit {
			t.Fatalf("team+premium reads %d/%d $%d, premium+team %d/%d $%d", a.Session.Limit, a.Day.Limit, ours(a).Limit/usd, b.Session.Limit, b.Day.Limit, ours(b).Limit/usd)
		}
	})
}

// The served rung's own row decides seats, period and payment: commerce names it,
// and no other row lends it anything.
func TestSeatsAreTheServedRungsOwnRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		team := client.Subscription{
			PlanID: "rec-team", Status: "active", Quantity: 30, ID: "sub-team", ProviderType: "square", Settled: "card", Seats: 30, ChargedCents: 75000,
			CurrentPeriodStart: periodStart.Format(time.RFC3339),
			CurrentPeriodEnd:   periodStart.AddDate(0, 1, 0).Format(time.RFC3339),
			Plan:               client.SubscriptionPlan{Interval: "month", Price: 2500},
		}
		w.rows = []client.Subscription{team, w.rows[0]}
		w.l.rung = func(context.Context, string, string) (string, string, error) { return "max-20x", "sub-record-id", nil }
		s := w.read()
		if s.Plan != "max-20x" || ours(s).Limit != 100*usd || s.Day.Limit != 3000 {
			t.Errorf("max-20x read %s at $%d and %d a day, want its own row's $100 and 3000", s.Plan, ours(s).Limit/usd, s.Day.Limit)
		}
	})
}

// A served rung whose own row is trialing, or whose period's payment was refunded or
// disputed (commerce answers it unpaid), is no plan, whatever else the payer holds.
func TestAnUnpaidServingRowIsNoPlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		w.rows[0].Status, w.rows[0].Settled, w.rows[0].ChargedCents = "trialing", "", 0
		w.rows = append(w.rows, client.Subscription{ID: "sub-dev", PlanID: "rec-dev", Status: "active", Settled: "card", Seats: 1, ChargedCents: 2000,
			Plan: client.SubscriptionPlan{Price: 2000, Interval: "month"}})
		if s := w.read(); s.Plan != "" {
			t.Errorf("a trialing max-20x beside a paid Pro read plan %s at $%d", s.Plan, ours(s).Limit/usd)
		}
		refunded := newWorld(t, "max-20x", 1, "")
		if s := refunded.read(); s.Plan != "" {
			t.Errorf("a refunded period read plan %s", s.Plan)
		}
	})
}

// A period sold below list (a promo) includes figures pro-rated by what was charged;
// a window never pro-rates to zero, which would read unbounded.
func TestAPromoPeriodGetsProRatedFigures(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		w.rows[0].ChargedCents = 10000
		s := w.read()
		if ours(s).Limit != 50*usd || s.Day.Limit != 1500 || s.Session.Limit != 500 {
			t.Errorf("half-price max-20x reads $%d, %d a day, %d a session; want $50, 1500, 500", ours(s).Limit/usd, s.Day.Limit, s.Session.Limit)
		}
		w2 := newWorld(t, "dev", 1, "card")
		w2.rows[0].ChargedCents = 1
		if s := w2.read(); s.Session.Limit < 1 || s.Day.Limit < 1 {
			t.Errorf("a 1-cent Pro reads windows %d/%d, want at least 1 each", s.Session.Limit, s.Day.Limit)
		}
		w3 := newWorld(t, "dev", 1, "card")
		w3.rows[0].ChargedCents = 0
		if s := w3.read(); s.Plan != "" {
			t.Errorf("a period charged nothing read plan %s", s.Plan)
		}
	})
}

// No row named, or a named row the payer does not hold, is no plan.
func TestNoServingRowIsNoPlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		w.l.rung = func(context.Context, string, string) (string, string, error) { return "max-20x", "", nil }
		if s := w.read(); s.Plan != "" {
			t.Errorf("no row named read plan %s", s.Plan)
		}
		w2 := newWorld(t, "max-20x", 1, "card")
		w2.l.rung = func(context.Context, string, string) (string, string, error) { return "max-20x", "sub-elsewhere", nil }
		if s := w2.read(); s.Plan != "" {
			t.Errorf("a row the payer does not hold read plan %s", s.Plan)
		}
	})
}
