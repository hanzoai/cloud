package reward

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestMonthsBetweenCountsWholeCalendarMonths(t *testing.T) {
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 12, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		from, to time.Time
		want     int
	}{
		{d(2026, 1, 15), d(2026, 2, 14), 0}, {d(2026, 1, 15), d(2026, 2, 15), 1}, {d(2026, 1, 15), d(2026, 7, 15), 6},
		{d(2025, 11, 8), d(2026, 11, 8), 12}, {d(2025, 11, 9), d(2026, 11, 8), 11}, {d(2023, 11, 8), d(2026, 11, 8), 36},
		{d(2026, 1, 31), d(2026, 2, 28), 0}, {d(2026, 5, 1), d(2026, 4, 1), 0},
	} {
		if got := monthsBetween(c.from, c.to); got != c.want {
			t.Errorf("%s to %s = %d months, want %d", c.from.Format(time.DateOnly), c.to.Format(time.DateOnly), got, c.want)
		}
	}
}

func TestLongestRunIsTheMostConsecutiveDays(t *testing.T) {
	run := func(start string, n int) []string {
		t0, _ := time.Parse(time.DateOnly, start)
		var out []string
		for i := range n {
			out = append(out, t0.AddDate(0, 0, i).Format(time.DateOnly))
		}
		return out
	}
	if got := longestRun(nil); got != 0 {
		t.Errorf("no days = %d", got)
	}
	if got := longestRun(append(run("2026-01-01", 6), run("2026-02-01", 3)...)); got != 6 {
		t.Errorf("6 then 3 = %d", got)
	}
	if got := longestRun(append(run("2026-01-01", 3), run("2026-01-05", 8)...)); got != 8 {
		t.Errorf("a gap of one day splits the run: %d", got)
	}
	if got := longestRun(run("2026-02-26", 5)); got != 5 {
		t.Errorf("across a month end = %d", got)
	}
}

func TestTenureIsStatedForEveryThresholdAPersonHasReachedHistoryIncluded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		now := time.Now().UTC()
		stamp := func(months int) string { return now.AddDate(0, -months, -1).Format(time.RFC3339) }
		r.directory(
			cloudUser{ID: "sub-new", Owner: "a", Name: "new", Created: stamp(0)},
			cloudUser{ID: "sub-13", Owner: "a", Name: "m13", Created: stamp(13)},
			cloudUser{ID: "sub-40", Owner: "a", Name: "m40", Created: stamp(40)},
			cloudUser{ID: "sub-svc", Owner: "a", Name: "svc", Created: stamp(40), Type: "service-account"},
		)
		ctx := context.Background()
		for range 2 {
			if err := r.s.sweepTenure(ctx); err != nil {
				t.Fatal(err)
			}
		}
		badges := func(sub string) map[int]bool {
			got := map[int]bool{}
			for _, c := range r.claimsOf(sub, "tenure.reached") {
				got[int(c.Fields["months"].(float64))] = true
			}
			return got
		}
		if n := len(badges("sub-new")); n != 0 {
			t.Errorf("a day-old account earned %d tenure claims", n)
		}
		if got := badges("sub-13"); len(got) != 3 || !got[1] || !got[6] || !got[12] {
			t.Errorf("13 months = %v, want 1, 6 and 12", got)
		}
		if got := badges("sub-40"); len(got) != 5 || !got[36] {
			t.Errorf("40 months = %v, want all five", got)
		}
		if n := len(r.claims("sub-svc")); n != 0 {
			t.Errorf("a service account earned %d", n)
		}

	})
}

func TestAStreakIsTheLongestRunOfActiveDaysAndNeverAGapAcrossedTwice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(cloudUser{ID: "sub-a", Owner: "a", Name: "a"}, cloudUser{ID: "sub-b", Owner: "a", Name: "b"}, cloudUser{ID: "sub-c", Owner: "a", Name: "c"})
		var rows []map[string]any
		add := func(user, start string, n int) {
			t0, _ := time.Parse(time.DateOnly, start)
			for i := range n {
				rows = append(rows, map[string]any{"user_id": user, "d": t0.AddDate(0, 0, i).Format(time.DateOnly)})
			}
		}
		add("a/a", "2026-01-01", 8) // a week
		add("a/b", "2026-01-01", 6) // six days, a gap, six more: no week
		add("a/b", "2026-01-08", 6)
		add("a/c", "2026-01-01", 101) // a hundred days
		r.warehouse(map[string][]map[string]any{"ORDER BY user_id, day": rows})
		ctx := context.Background()
		for range 2 {
			if err := r.s.sweepStreaks(ctx); err != nil {
				t.Fatal(err)
			}
		}
		days := func(sub string) []int {
			var got []int
			for _, c := range r.claimsOf(sub, "streak.reached") {
				got = append(got, int(c.Fields["days"].(float64)))
			}
			return got
		}
		if got := days("sub-a"); len(got) != 1 || got[0] != 7 {
			t.Errorf("8 days = %v, want only the week", got)
		}
		if got := days("sub-b"); len(got) != 0 {
			t.Errorf("two runs of six = %v, want none", got)
		}
		if got := days("sub-c"); len(got) != 3 {
			t.Errorf("101 days = %v, want 7, 30 and 100", got)
		}

	})
}

func TestFoundingDevelopersAreTheApplicantsAndTheEarlyOnceStaffSetACutoff(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		ctx := context.Background()
		r.directory(
			cloudUser{ID: "sub-alpha", Owner: "a", Name: "alpha", Created: "2026-06-01T00:00:00Z"},
			cloudUser{ID: "sub-beta", Owner: "a", Name: "beta", Created: "2026-06-01T00:00:00Z"},
			cloudUser{ID: "sub-early", Owner: "a", Name: "early", Created: "2025-01-01T00:00:00Z"},
			cloudUser{ID: "sub-late", Owner: "a", Name: "late", Created: "2026-09-01T00:00:00Z"},
			cloudUser{ID: "sub-both", Owner: "a", Name: "both", Created: "2025-02-01T00:00:00Z"},
		)
		stub(t, &installFn, func(context.Context, string) error { return nil })
		stub(t, &waitingFn, func(context.Context, string) ([]crm.Wait, error) {
			rec := func(v any) crm.Wait { b, _ := json.Marshal(v); return crm.Wait{Data: b} }
			return []crm.Wait{
				rec(map[string]any{"user": "sub-alpha", "program": "beta", "betas": []string{"alpha"}}),
				rec(map[string]any{"user": "sub-beta", "applied": "2026-06-02"}),
				rec(map[string]any{"user": "sub-both", "program": "beta", "betas": []string{"alpha"}}),
				rec(map[string]any{"user": "sub-stranger", "program": "beta"}),
			}, nil
		})
		// no cutoff is set, so only applicants qualify: the owner sets the date.
		if err := r.s.sweepFounders(ctx); err != nil {
			t.Fatal(err)
		}
		reason := func(sub string) string {
			cs := r.claimsOf(sub, "founder.eligible")
			if len(cs) != 1 {
				return fmt.Sprintf("%d claims", len(cs))
			}
			return cs[0].Fields["reason"].(string)
		}
		if reason("sub-alpha") != "alpha" || reason("sub-beta") != "beta" || reason("sub-both") != "alpha" {
			t.Errorf("applicants: %s %s %s", reason("sub-alpha"), reason("sub-beta"), reason("sub-both"))
		}
		if n := len(r.claims("sub-early")) + len(r.claims("sub-late")) + len(r.claims("sub-stranger")); n != 0 {
			t.Errorf("with no cutoff, %d claims for people who never applied", n)
		}
		// staff set the cutoff; accounts before it are early, the applicants keep their reason, and nobody is twice.
		p := mustProgram(t, r)
		p.FoundingCutoff = "2026-01-01"
		if _, err := r.s.eng.SaveProgram(ctx, program, *p, "staff"); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := r.s.sweepFounders(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if reason("sub-early") != "early" || reason("sub-both") != "alpha" || reason("sub-alpha") != "alpha" {
			t.Errorf("after the cutoff: %s %s %s", reason("sub-early"), reason("sub-both"), reason("sub-alpha"))
		}
		if n := len(r.claims("sub-late")); n != 0 {
			t.Errorf("an account after the cutoff earned %d", n)
		}

	})
}

func TestOnlyADepthTwoSponsorEarnsAPointAndEveryFifthFriendIsAMilestone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		users := []cloudUser{
			{ID: "sub-grand", Owner: "grandco", Name: "g", Admin: true, Created: "2026-01-01T00:00:00Z"},
			{ID: "sub-top", Owner: "topco", Name: "t", Admin: true, Created: "2026-01-02T00:00:00Z"},
		}
		edges := []client.ReferralEdge{{Referrer: "grandco", Referee: "topco", CreatedAt: 1}}
		used := []map[string]any{{"organization": "topco", "first": "2026-10-01", "last": "2026-10-02"}}
		// topco refers six orgs; the great-grandparent chain stops at grandco.
		for i := 1; i <= 6; i++ {
			org := fmt.Sprintf("friend%d", i)
			users = append(users, cloudUser{ID: "sub-" + org, Owner: org, Name: "o", Admin: true, Created: "2026-02-01T00:00:00Z"})
			edges = append(edges, client.ReferralEdge{Referrer: "topco", Referee: org, CreatedAt: int64(10 + i)})
			used = append(used, map[string]any{"organization": org, "first": "2026-10-03", "last": "2026-10-03"})
		}
		// one friend refers a friend of their own: depth 3 from grandco, depth 2 from topco.
		users = append(users, cloudUser{ID: "sub-deep", Owner: "deepco", Name: "d", Admin: true, Created: "2026-03-01T00:00:00Z"})
		edges = append(edges, client.ReferralEdge{Referrer: "friend1", Referee: "deepco", CreatedAt: 99})
		used = append(used, map[string]any{"organization": "deepco", "first": "2026-10-04", "last": "2026-10-04"})
		r.directory(users...)
		r.warehouse(map[string][]map[string]any{"min(day)": used})
		stub(t, &edgesFn, func(context.Context, int, int) ([]client.ReferralEdge, error) { return edges, nil })
		ctx := context.Background()
		for range 2 {
			if err := r.s.sweepReferrals(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// grandco sponsors topco: each of topco's six friends is a friend of grandco's friend: +1 each. deepco is
		// a friend of friend1's, which is depth 3 from grandco: nothing.
		net := r.claimsOf("sub-grand", "referral.network")
		if len(net) != 6 {
			t.Fatalf("grandco's network claims = %d, want 6 (one per friend of topco, none for depth 3)", len(net))
		}
		for _, c := range net {
			if c.Rule != "referral-network" || c.Points != 1 || c.Fields["referee"] == "deepco" {
				t.Errorf("claim %+v", c)
			}
		}
		// topco's own sponsor earns for deepco too (friend1 is topco's friend, deepco theirs): depth 2.
		if got := r.claimsOf("sub-top", "referral.network"); len(got) != 1 || got[0].Fields["referee"] != "deepco" {
			t.Errorf("topco's network claims = %+v, want only deepco", got)
		}
		// topco has six activated friends: one milestone, the fifth; not at four or at six.
		ms := r.claimsOf("sub-top", "referral.milestone")
		if len(ms) != 1 || ms[0].Fields["count"] != float64(5) || ms[0].Rule != "referral-milestone-5" {
			t.Fatalf("milestones = %+v, want Spark at 5 only", ms)
		}
		// a seventh through tenth friend: the tenth is Signal, and re-emission claims nothing twice.
		for i := 7; i <= 10; i++ {
			org := fmt.Sprintf("friend%d", i)
			users = append(users, cloudUser{ID: "sub-" + org, Owner: org, Name: "o", Admin: true, Created: "2026-02-01T00:00:00Z"})
			edges = append(edges, client.ReferralEdge{Referrer: "topco", Referee: org, CreatedAt: int64(10 + i)})
			used = append(used, map[string]any{"organization": org, "first": "2026-10-03", "last": "2026-10-03"})
		}
		r.directory(users...)
		r.warehouse(map[string][]map[string]any{"min(day)": used})
		for range 2 {
			if err := r.s.sweepReferrals(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if ms = r.claimsOf("sub-top", "referral.milestone"); len(ms) != 2 {
			t.Errorf("milestones after ten friends = %d, want Spark and Signal", len(ms))
		}

	})
}
