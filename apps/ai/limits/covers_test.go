// Copyright © 2026 Hanzo AI. MIT License.

package limits

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// Covers answers read-only whether a payer's active paid plan would issue a
// PaysPlan grant right now: never mutates counters, refuses third-party models on
// Hanzo-only plans, refuses fast mode on every plan (Unlimited included), and
// flips to false once the plan's monthly allowance, rolling window, or request
// window is exhausted.
func TestCoversIsReadOnlyAndMatchesAdmitPlanRules(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()

		// 1. Pro (pro-3x) with $0 wallet: covers ClassOurs, never ClassPremium, never fast.
		pro := newWorld(t, "pro-3x", 1, "card")
		pro.rows[0].ChargedCents, pro.rows[0].Plan.Price = 1500, 1500
		pro.l.flag = policy(saved(nil, "pro-3x", 2, 10, 0, 1500)) // $15 Hanzo, $0 premium, 2/session

		oursAsk := Ask{Subject: "acme", Org: "acme", Model: "enso-auto", Family: "enso", Class: ClassOurs, Priced: true, Spend: true}
		premAsk := Ask{Subject: "acme", Org: "acme", Model: "anthropic/claude-opus-4.7", Class: ClassPremium, Priced: true, Spend: true}
		fastAsk := Ask{Subject: "acme", Org: "acme", Model: "enso-auto", Family: "enso", Class: ClassOurs, Priced: true, Spend: true, Fast: true}

		// Calling Covers repeatedly MUST NOT mutate session/day/spend counters.
		for range 10 {
			ok, err := pro.l.Covers(ctx, oursAsk)
			if err != nil || !ok {
				t.Fatalf("pro-3x Covers(enso-auto) = %v, %v; want true", ok, err)
			}
		}
		st := pro.read()
		if st.Session.Used != 0 || st.Day.Used != 0 || st.Classes[ClassOurs].Period.Used != 0 {
			t.Fatalf("Covers mutated counters: session=%d day=%d used=%d",
				st.Session.Used, st.Day.Used, st.Classes[ClassOurs].Period.Used)
		}

		// Third-party premium model on Hanzo-only plan → false.
		if ok, err := pro.l.Covers(ctx, premAsk); err != nil || ok {
			t.Fatalf("pro-3x Covers(premium) = %v, %v; want false", ok, err)
		}
		// Fast mode → false (never covered by plan).
		if ok, err := pro.l.Covers(ctx, fastAsk); err != nil || ok {
			t.Fatalf("pro-3x Covers(fast) = %v, %v; want false", ok, err)
		}

		// Exhaust the $15 allowance via Admit + Settle → Covers flips to false.
		g, hit, err := pro.l.Admit(ctx, oursAsk)
		if err != nil || hit != nil || !g.Covered() {
			t.Fatalf("pro-3x Admit = %+v %+v %v; want covered grant", g, hit, err)
		}
		pro.use(g, 15*usd)
		if ok, err := pro.l.Covers(ctx, oursAsk); err != nil || ok {
			t.Fatalf("pro-3x Covers after exhausting $15 = %v, %v; want false", ok, err)
		}

		// 2. Pro with rolling weekly window exhausted → Covers flips to false even when monthly has room.
		weekly := newWorld(t, "pro-3x", 1, "card")
		weekly.rows[0].ChargedCents, weekly.rows[0].Plan.Price = 1500, 1500
		sw := saved(nil, "pro-3x", 150, 450, 0, 1500)
		sw[flags.PlanKey("ours_window", "pro-3x")] = "375"       // $3.75/wk
		sw[flags.PlanKey("ours_window_hours", "pro-3x")] = "168" // 7d
		weekly.l.flag = policy(sw)
		if ok, err := weekly.l.Covers(ctx, oursAsk); err != nil || !ok {
			t.Fatalf("weekly pro-3x Covers before spend = %v, %v; want true", ok, err)
		}
		wg, _, _ := weekly.l.Admit(ctx, oursAsk)
		weekly.use(wg, 375*nanosPerCent)
		if ok, err := weekly.l.Covers(ctx, oursAsk); err != nil || ok {
			t.Fatalf("weekly pro-3x Covers after $3.75 weekly spend = %v, %v; want false", ok, err)
		}

		// 3. Unlimited (Hanzo unbounded, premium 0): covers ClassOurs forever, never premium, never fast.
		unlim := newWorld(t, "unlimited", 1, "external:comp")
		unlim.l.flag = policy(saved(nil, "unlimited", 0, 0, 0, flags.Unbounded))
		if ok, err := unlim.l.Covers(ctx, oursAsk); err != nil || !ok {
			t.Fatalf("unlimited Covers(enso-auto) = %v, %v; want true", ok, err)
		}
		if ok, err := unlim.l.Covers(ctx, premAsk); err != nil || ok {
			t.Fatalf("unlimited Covers(premium) = %v, %v; want false", ok, err)
		}
		if ok, err := unlim.l.Covers(ctx, fastAsk); err != nil || ok {
			t.Fatalf("unlimited Covers(fast) = %v, %v; want false", ok, err)
		}

		// 4. Free plan (no subscription) → Covers is false.
		free := newWorld(t, "", 0, "")
		if ok, err := free.l.Covers(ctx, oursAsk); err != nil || ok {
			t.Fatalf("free Covers(enso-auto) = %v, %v; want false", ok, err)
		}
	})
}
