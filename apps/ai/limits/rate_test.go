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
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// spend asks n requests of person acting in org and answers the last standing.
func spend(t *testing.T, l *Limiter, person, org string, n int) Pace {
	t.Helper()
	var p Pace
	for i := 0; i < n; i++ {
		if p = l.Pace(context.Background(), person, org); !p.Admitted {
			t.Fatalf("%s request %d of %d refused: %+v", person, i+1, n, p)
		}
	}
	return p
}

// A person's 41st model request in a minute is refused, counting nothing, while
// another person is served: the limit is per person, at the shipped 40.
func TestAPersonsFortyFirstRequestInAMinuteIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		w.l.flag = policy(nil)
		if p := spend(t, w.l, "acme/alice", "acme", 40); p.Limit != 40 || p.Remaining != 0 {
			t.Fatalf("after 40: %+v, want limit 40 and nothing left", p)
		}
		p := w.l.Pace(context.Background(), "acme/alice", "acme")
		if p.Admitted || p.Remaining != 0 || p.Limit != 40 || p.Reset <= 0 || p.Reset > time.Minute {
			t.Fatalf("the 41st: %+v, want refused with nothing left and the window turning within a minute", p)
		}
		if again := w.l.Pace(context.Background(), "acme/alice", "acme"); again.Admitted {
			t.Fatalf("a refusal let the next request in: %+v", again)
		}
		if bob := w.l.Pace(context.Background(), "acme/bob", "acme"); !bob.Admitted || bob.Remaining != 39 {
			t.Fatalf("another person of the same org: %+v, want served with 39 left", bob)
		}
		if nobody := w.l.Pace(context.Background(), "", "acme"); !nobody.Admitted || nobody.Limit != 0 {
			t.Fatalf("a request naming no person: %+v, want admitted and counted by nothing", nobody)
		}
	})
}

// A member of several orgs meets the lowest rate among the platform's, the org the
// request acts in and the member's home org: acting in an org that set nothing does
// not get past the home org's, and acting in a stricter org binds a visitor from a
// lax one. Mutation: read only the acting org's rate and carol acts in globex at 40.
func TestAMemberOfSeveralOrgsMeetsTheLowestRate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		w.l.flag = policy(nil)
		ctx := context.Background()
		for org, rpm := range map[string]int{"acme": 5, "initech": 8} {
			if err := w.l.SetRate(ctx, org, rpm, "z@"+org+".test"); err != nil {
				t.Fatal(err)
			}
		}
		// carol's home is acme (5); she acts in globex, which set nothing (40).
		if p := spend(t, w.l, "acme/carol", "globex", 5); p.Limit != 5 {
			t.Fatalf("acme's carol acting in globex stands at %+v, want a limit of 5", p)
		}
		if p := w.l.Pace(ctx, "acme/carol", "globex"); p.Admitted || p.Limit != 5 {
			t.Fatalf("her 6th: %+v, want refused at acme's 5", p)
		}
		// dave's home is globex (40); he acts in initech (8).
		if p := w.l.Pace(ctx, "globex/dave", "initech"); !p.Admitted || p.Limit != 8 {
			t.Fatalf("globex's dave acting in initech: %+v, want a limit of 8", p)
		}
		// ed's home is initech (8); he acts in acme (5): the lower of the two.
		if p := w.l.Pace(ctx, "initech/ed", "acme"); !p.Admitted || p.Limit != 5 {
			t.Fatalf("initech's ed acting in acme: %+v, want a limit of 5", p)
		}
		// A person acting in the org they are from reads that org's rate once.
		if p := w.l.Pace(ctx, "initech/fay", "initech"); !p.Admitted || p.Limit != 8 {
			t.Fatalf("initech's fay in initech: %+v, want a limit of 8", p)
		}
	})
}

// An organization's own setting lowers the limit for its people and is refused
// above the platform's default; the default, lowered under it, still binds.
func TestAnOrgSetsLessThanTheDefaultAndNeverMore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		set := map[string]string{}
		w.l.flag = policy(set)
		ctx := context.Background()
		if err := w.l.SetRate(ctx, "acme", 10, "z@acme.test"); err != nil {
			t.Fatal(err)
		}
		spend(t, w.l, "acme/alice", "acme", 10)
		if p := w.l.Pace(ctx, "acme/alice", "acme"); p.Admitted || p.Limit != 10 {
			t.Fatalf("the 11th under acme's 10: %+v, want refused at 10", p)
		}
		// The same person acting in another org meets that org's limit, the default.
		if p := w.l.Pace(ctx, "globex/carol", "globex"); !p.Admitted || p.Limit != flags.SeedRate {
			t.Fatalf("an org that set nothing: %+v, want the default %d", p, flags.SeedRate)
		}
		for _, rpm := range []int{flags.SeedRate + 1, -1} {
			if err := w.l.SetRate(ctx, "acme", rpm, "z@acme.test"); !errors.Is(err, ErrRate) {
				t.Fatalf("setting %d over a default of %d: %v, want ErrRate", rpm, flags.SeedRate, err)
			}
		}
		if _, def, own := w.l.Rates(ctx, "acme"); def != flags.SeedRate || own != 10 {
			t.Fatalf("after refused raises: default %d own %d, want %d and 10", def, own, flags.SeedRate)
		}
		// A SuperAdmin lowers the default below acme's own: the default binds.
		set[flags.Rate] = "5"
		if got, _, _ := w.l.Rates(ctx, "acme"); got != 5 {
			t.Fatalf("acme's 10 under a default of 5 reads %d, want 5", got)
		}
		// Kept across a restart, and cleared back to the default.
		_ = w.l.store.Close() // a store is written whole when it closes
		again, err := Open(w.dir, w.l.rung, w.l.subs, w.l.catalog, w.l.funds, w.l.flag)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = again.store.Close() })
		again.flag = policy(nil)
		if _, _, own := again.Rates(ctx, "acme"); own != 10 {
			t.Fatalf("acme's setting after a restart: %d, want 10", own)
		}
		if err := again.SetRate(ctx, "acme", 0, "z@acme.test"); err != nil {
			t.Fatal(err)
		}
		// The default the restarted limiter holds is the last one the switch gave, 5.
		if rpm, def, own := again.Rates(ctx, "acme"); own != 0 || def != 5 || rpm != def {
			t.Fatalf("acme cleared: default %d own %d in force %d, want its people at the last known default 5", def, own, rpm)
		}
	})
}

// The default is the SuperAdmin's switch, and a flag plane that cannot answer reads
// as the last value it gave — across a restart too — never as no limit: before any
// answer it is the shipped 40, and a figure below 1 is the shipped 40.
func TestTheDefaultIsTheSwitchAndHoldsThroughAnOutage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "", 0, "")
		ctx := context.Background()
		w.l.flag = policy(map[string]string{flags.Rate: "25"})
		if _, def, _ := w.l.Rates(ctx, "acme"); def != 25 {
			t.Fatalf("the switch set to 25 reads %d", def)
		}
		for how, flag := range map[string]Flag{"down": down, "answering unset": policy(nil)} {
			w.l.flag = flag
			if _, def, _ := w.l.Rates(ctx, "acme"); def != 25 {
				t.Fatalf("flag plane %s: the default reads %d, want the last known 25", how, def)
			}
		}
		_ = w.l.store.Close() // a store is written whole when it closes
		again, err := Open(w.dir, w.l.rung, w.l.subs, w.l.catalog, w.l.funds, w.l.flag)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = again.store.Close() })
		again.flag = down
		if _, def, _ := again.Rates(ctx, "acme"); def != 25 {
			t.Fatalf("a limiter opened again during the outage reads %d, want 25", def)
		}

		fresh := newWorld(t, "", 0, "")
		fresh.l.flag = down
		if _, def, _ := fresh.l.Rates(ctx, "acme"); def != flags.SeedRate {
			t.Fatalf("never answered and down: %d, want the shipped %d", def, flags.SeedRate)
		}
		for _, v := range []string{"0", "-3"} {
			fresh.l.flag = policy(map[string]string{flags.Rate: v})
			if _, def, _ := fresh.l.Rates(ctx, "acme"); def != flags.SeedRate {
				t.Fatalf("the switch at %s reads %d, want the shipped %d, never no limit", v, def, flags.SeedRate)
			}
		}
		fresh.l.flag = policy(map[string]string{flags.Rate: strconv.Itoa(flags.MaxRate + 1)})
		if _, def, _ := fresh.l.Rates(ctx, "acme"); def != flags.MaxRate {
			t.Fatalf("a figure past the most reads %d, want %d", def, flags.MaxRate)
		}
	})
}

// Fast mode is paid from prepaid on a plan whose included usage is unbounded: the
// payer's own money pays, cash only, and the plan counts nothing; with none of its
// own money the payer is told to add funds, whatever credit it holds.
func TestFastModeIsPaidFromPrepaidOnAnUnlimitedPlan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		w.l.flag = policy(saved(nil, "max-20x", 0, 0, 100_000_000, 100_000_000))
		w.cash = 5 * usd
		fast := Ask{Model: premium, Class: ClassPremium, Priced: true, Spend: true, Fast: true}
		g, hit := w.admit(fast)
		if hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash || g.Covered() || g.Counted() {
			t.Fatalf("fast on an unlimited plan: grant %+v hit %+v, want prepaid, cash only, nothing counted", g, hit)
		}
		if s := w.read(); s.Session.Used != 0 || s.Day.Used != 0 {
			t.Fatalf("a fast request counted in the plan's windows: %+v", s)
		}
		if g, _ := w.priced(premium, ClassPremium); g == nil || g.Pays != PaysPlan {
			t.Fatalf("the same request without fast mode: %+v, want the plan paying", g)
		}
		w.cash, w.credit = 0, 5*usd
		if g, hit := w.admit(fast); g != nil || hit == nil || hit.Code != CodePrepaid {
			t.Fatalf("fast with only granted credit: grant %+v hit %+v, want %s", g, hit, CodePrepaid)
		}
		// An unpriced model has nothing for prepaid to pay: fast mode changes nothing.
		if g, hit := w.admit(Ask{Model: free, Class: ClassFree, Family: "enso", Spend: true, Fast: true}); hit != nil || g == nil || g.Pays != PaysPlan {
			t.Fatalf("fast on the free lane: grant %+v hit %+v, want the plan's free lane", g, hit)
		}
	})
}
