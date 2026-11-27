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
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// yearly turns the world's one row into the same rung bought by the year: a
// twelve-month term at the year's price, paid in full.
func yearly(w *world, yearCents int64) {
	r := &w.rows[0]
	r.Plan.Interval, r.Plan.Price = "year", yearCents
	r.ChargedCents = yearCents * int64(max(1, r.Seats))
	r.CurrentPeriodStart = periodStart.Format(time.RFC3339)
	r.CurrentPeriodEnd = periodStart.AddDate(1, 0, 0).Format(time.RFC3339)
}

// moveTo puts the world's clock at t and forgets the plan it read, as a minute's
// cache would.
func moveTo(w *world, t time.Time) {
	w.now = t
	w.l.mu.Lock()
	clear(w.l.plans)
	w.l.mu.Unlock()
}

// A plan bought by the year includes what the same plan includes by the month,
// and it renews every month of the term: Max 20x's $100 of premium models is
// used up in October and whole again in November, with the year still running.
func TestAYearlyPlanRenewsItsUsageEveryMonth(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		yearly(w, 200000)
		allow := int64(flags.CarriedPlans["max-20x"].Premium) * nanosPerCent

		s := w.read()
		firstEnd := periodStart.AddDate(0, 1, 0)
		if s.Plan != "max-20x" || !s.Start.Equal(periodStart) || !s.End.Equal(firstEnd) {
			t.Fatalf("first month: plan %q from %v to %v, want max-20x from %v to %v", s.Plan, s.Start, s.End, periodStart, firstEnd)
		}
		if got := s.Classes[ClassPremium].Period.Limit; got != allow {
			t.Fatalf("first month includes %d nanos of premium, want the monthly plan's %d", got, allow)
		}

		g, _ := w.priced(premium, ClassPremium)
		w.use(g, allow)
		if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodePlanAllowance || !hit.ResetsAt.Equal(firstEnd) {
			t.Fatalf("premium used up: grant %+v hit %+v, want plan_allowance_used until %v, not the term's end", g, hit, firstEnd)
		}

		moveTo(w, firstEnd.Add(time.Hour))
		g, hit := w.priced(premium, ClassPremium)
		if hit != nil || g == nil || g.Pays != PaysPlan || g.Spend != allow {
			t.Fatalf("second month: grant %+v hit %+v, want the plan paying with all %d nanos again", g, hit, allow)
		}
		if s := w.read(); !s.Start.Equal(firstEnd) || !s.End.Equal(periodStart.AddDate(0, 2, 0)) {
			t.Fatalf("second month runs %v to %v, want %v to %v", s.Start, s.End, firstEnd, periodStart.AddDate(0, 2, 0))
		}
	})
}

// A team bought by the year includes Team's monthly figures for every seat it was
// charged for, every month.
func TestAYearlyTeamIncludesTeamsMonthlyFiguresPerSeat(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "team", 3, "card")
		yearly(w, 24000)
		seed := flags.CarriedPlans["team"]
		s := w.read()
		if s.Plan != "team" || ours(s).Limit != int64(3*seed.Ours)*nanosPerCent || s.Day.Limit != int64(3*seed.Day) {
			t.Fatalf("yearly team of three reads plan %q, ours %d, day %d; want team, three seats of %+v", s.Plan, ours(s).Limit, s.Day.Limit, seed)
		}
		if !s.End.Equal(periodStart.AddDate(0, 1, 0)) {
			t.Fatalf("yearly team's usage renews %v, want a month after the term began", s.End)
		}
	})
}

// The months of a term are calendar months from its start, the day held to the
// end of a shorter month; the last whole month runs to the term's end, and once the
// term is over its last month holds.
func TestTheMonthOfATerm(t *testing.T) {
	jan31, termEnd := at(t, "2026-01-31T09:00:00Z"), at(t, "2027-01-31T09:00:00Z")
	for _, c := range []struct{ now, from, to string }{
		{"2026-01-31T09:00:00Z", "2026-01-31T09:00:00Z", "2026-02-28T09:00:00Z"},
		{"2026-02-28T08:59:59Z", "2026-01-31T09:00:00Z", "2026-02-28T09:00:00Z"},
		{"2026-02-28T09:00:00Z", "2026-02-28T09:00:00Z", "2026-03-31T09:00:00Z"},
		{"2026-04-30T12:00:00Z", "2026-04-30T09:00:00Z", "2026-05-31T09:00:00Z"},
		{"2027-01-30T00:00:00Z", "2026-12-31T09:00:00Z", "2027-01-31T09:00:00Z"},
		// Past the term: its last month, not a new one.
		{"2027-01-31T09:00:00Z", "2026-12-31T09:00:00Z", "2027-01-31T09:00:00Z"},
		{"2028-07-01T00:00:00Z", "2026-12-31T09:00:00Z", "2027-01-31T09:00:00Z"},
	} {
		from, to := renewal(jan31, termEnd, at(t, c.now))
		if !from.Equal(at(t, c.from)) || !to.Equal(at(t, c.to)) {
			t.Errorf("at %s: %v to %v, want %s to %s", c.now, from, to, c.from, c.to)
		}
	}
	// A year begun on February 29 ends on March 1: twelve months, the last one
	// carrying the extra day, never a thirteenth.
	feb29, mar1 := at(t, "2028-02-29T00:00:00Z"), at(t, "2029-03-01T00:00:00Z")
	if from, to := renewal(feb29, mar1, at(t, "2029-02-28T12:00:00Z")); !from.Equal(at(t, "2029-01-29T00:00:00Z")) || !to.Equal(mar1) {
		t.Errorf("a leap-day term's last month runs %v to %v, want 2029-01-29 to %v", from, to, mar1)
	}
}

// Only a yearly term renews by the month: a monthly period is one window however
// long it runs, and a period that has not begun is held to the month before it.
func TestAPeriodIsItsOwnWindowUnlessYearly(t *testing.T) {
	row := func(interval, start, end string) client.Subscription {
		return client.Subscription{Plan: client.SubscriptionPlan{Interval: interval}, CurrentPeriodStart: start, CurrentPeriodEnd: end}
	}
	if from, to := period(row("month", "2026-01-01T00:00:00Z", "2026-02-20T00:00:00Z"), at(t, "2026-02-10T00:00:00Z")); !from.Equal(at(t, "2026-01-01T00:00:00Z")) || !to.Equal(at(t, "2026-02-20T00:00:00Z")) {
		t.Errorf("a fifty-day monthly period split into %v to %v", from, to)
	}
	if from, to := period(row("year", "2026-03-15T00:00:00Z", "2027-03-15T00:00:00Z"), at(t, "2026-03-01T00:00:00Z")); !from.Equal(at(t, "2026-02-15T00:00:00Z")) || !to.Equal(at(t, "2026-03-15T00:00:00Z")) {
		t.Errorf("a term not yet begun reads %v to %v, want the month before it", from, to)
	}
}

// A yearly term that is over renews nothing: past its end the last month's use
// stands, however long the row stays active.
func TestAnEndedYearlyTermRenewsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		yearly(w, 200000)
		allow := int64(flags.CarriedPlans["max-20x"].Premium) * nanosPerCent
		moveTo(w, periodStart.AddDate(0, 11, 2))
		g, _ := w.priced(premium, ClassPremium)
		w.use(g, allow)
		for _, later := range []time.Time{periodStart.AddDate(1, 0, 1), periodStart.AddDate(1, 6, 0)} {
			moveTo(w, later)
			if g, hit := w.priced(premium, ClassPremium); g != nil || hit == nil || hit.Code != CodePlanAllowance {
				t.Fatalf("at %v, past the term: grant %+v hit %+v, want plan_allowance_used", later, g, hit)
			}
		}
	})
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
