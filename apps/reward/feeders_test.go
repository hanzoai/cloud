package reward

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestMilestonesAreOneEventForEachThresholdCrossedNeverPerRequest(t *testing.T) {
	if got := crossed(0, 99_999); len(got) != 0 {
		t.Errorf("below the first: %v", got)
	}
	if got := crossed(99_999, 100_000); len(got) != 1 || got[0] != 100_000 {
		t.Errorf("onto the first: %v", got)
	}
	if got := crossed(50_000, 6_000_000); len(got) != 3 || got[0] != 100_000 || got[1] != 1_000_000 || got[2] != 5_000_000 {
		t.Errorf("three at once: %v", got)
	}
	if got := crossed(1_000_000, 4_999_999); len(got) != 0 {
		t.Errorf("between two: %v", got)
	}
	if got := crossed(2_000_000_000_000, 3_000_000_000_000); len(got) != 0 {
		t.Errorf("past the last: %v", got)
	}
}

func TestAUsageSweepCoalescesAndStatesEachCrossingOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(cloudUser{ID: "sub-ann", Owner: "acme", Name: "ann"}, cloudUser{ID: "sub-bot", Owner: "acme", Name: "svc", Type: "service-account"})
		// ann went from 50K to 1.2M since the cursor: two thresholds in one sweep. A
		// service account never earns. A person nobody knows is skipped.
		r.warehouse(map[string][]map[string]any{"sumIf": {
			{"user_id": "acme/ann", "total": uint64(1_200_000), "recent": uint64(1_150_000)},
			{"user_id": "acme/svc", "total": uint64(9_000_000), "recent": uint64(9_000_000)},
			{"user_id": "ghost/nobody", "total": uint64(9_000_000), "recent": uint64(9_000_000)},
		}})
		ctx := context.Background()
		if err := r.s.sweepUsage(ctx); err != nil {
			t.Fatal(err)
		}
		got := r.claimsOf("sub-ann", "usage.tokens")
		if len(got) != 2 {
			t.Fatalf("claims = %d, want the 100K and 1M milestones", len(got))
		}
		rules := map[string]bool{}
		for _, c := range got {
			rules[c.Rule] = true
			if c.Fields["tokens"] != float64(1_200_000) {
				t.Errorf("tokens = %v, want the lifetime total", c.Fields["tokens"])
			}
		}
		if !rules["tokens-100k"] || !rules["tokens-1m"] {
			t.Errorf("rules = %v", rules)
		}
		if n := len(r.claims("sub-bot")) + len(r.claims("ghost/nobody")); n != 0 {
			t.Errorf("a service account or an unknown person earned %d claims", n)
		}
		// the same sweep again, and a sweep on a later day, claim nothing twice.
		if err := r.s.sweepUsage(ctx); err != nil {
			t.Fatal(err)
		}
		if n := len(r.claimsOf("sub-ann", "usage.tokens")); n != 2 {
			t.Errorf("a second sweep made it %d claims", n)
		}
		if day, _ := r.s.cursorOf(ctx, "usage"); day != today() {
			t.Errorf("cursor = %q, want today", day)
		}

	})
}

func TestAFirstUseOfAModelIsStatedOnceAndOnlyForAHanzoModel(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(cloudUser{ID: "sub-ann", Owner: "acme", Name: "ann"})
		r.warehouse(map[string][]map[string]any{"min(day)": {
			{"user_id": "acme/ann", "model": "Zen5"},
			{"user_id": "acme/ann", "model": "openai/gpt-6-luna"},
			{"user_id": "acme/ann", "model": ""},
		}})
		for range 2 {
			if err := r.s.sweepModels(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		got := r.claimsOf("sub-ann", "model.used")
		// the seed's model rules are off and price nothing, so the engine makes no
		// claim; the event is what is stated, so the mark is the proof it was.
		if len(got) != 0 {
			t.Fatalf("an inactive rule made %d claims", len(got))
		}
		for model, want := range map[string]bool{"zen5": true, "openai/gpt-6-luna": false} {
			if have, _ := r.s.mark(context.Background(), "sent:model.used:sub-ann:"+model); have != want {
				t.Errorf("model %q stated = %v, want %v", model, have, want)
			}
		}

	})
}

func TestAReferralActivatesOnFirstUseAndIsRetainedAWeekLater(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(
			cloudUser{ID: "sub-ref", Owner: "acme", Name: "boss", Admin: true, Created: "2026-01-01T00:00:00Z"},
			cloudUser{ID: "sub-new", Owner: "newco", Name: "owner", Admin: true, Created: "2026-02-01T00:00:00Z"},
			cloudUser{ID: "sub-self", Owner: "selfco", Name: "me", Admin: true, Created: "2026-02-01T00:00:00Z"},
		)
		day := func(s string) string { return s }
		r.warehouse(map[string][]map[string]any{"min(day)": {
			{"organization": "newco", "first": day("2026-10-01"), "last": day("2026-10-12")},
			{"organization": "later", "first": day("2026-10-01"), "last": day("2026-10-03")},
			{"organization": "selfco", "first": day("2026-10-01"), "last": day("2026-10-20")},
		}})
		stub(t, &edgesFn, func(context.Context, int, int) ([]client.ReferralEdge, error) {
			return []client.ReferralEdge{
				{Referrer: "acme", Referee: "newco", Status: "qualified"},
				{Referrer: "acme", Referee: "later", Status: "signup"},
				{Referrer: "acme", Referee: "unused", Status: "signup"},
				{Referrer: "selfco", Referee: "selfco", Status: "signup"},
				{Referrer: "ghostorg", Referee: "newco", Status: "signup"},
			}, nil
		})
		if err := r.s.sweepReferrals(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := r.s.sweepReferrals(context.Background()); err != nil {
			t.Fatal(err)
		}
		act, ret := r.claimsOf("sub-ref", "referral.activated"), r.claimsOf("sub-ref", "referral.retained")
		if len(act) != 2 {
			t.Errorf("activations = %d, want newco and later (not the unused org)", len(act))
		}
		if len(ret) != 1 || ret[0].EventID != "referral.retained:newco" {
			t.Errorf("retained = %+v, want only newco (12 days on)", ret)
		}
		if n := len(r.claims("sub-self")); n != 0 {
			t.Errorf("a self-referral earned %d claims", n)
		}
		// the referee is welcomed once for the same first task: +10, never for a self-referral.
		if w := r.claimsOf("sub-new", "referral.welcomed"); len(w) != 1 || w[0].Rule != "welcome-bonus" || w[0].Points != 10 || w[0].Fields["sponsor"] != "sub-ref" {
			t.Errorf("welcome = %+v", w)
		}
		_ = time.Now

	})
}

func TestAReferralLinksTheSponsorAndTheUplineEarnsAShareOfWhatTheirMemberIsAwarded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(
			cloudUser{ID: "sub-top", Owner: "topco", Name: "top", Admin: true, Created: "2026-01-01T00:00:00Z"},
			cloudUser{ID: "sub-mid", Owner: "midco", Name: "mid", Admin: true, Created: "2026-02-01T00:00:00Z"},
			cloudUser{ID: "sub-low", Owner: "lowco", Name: "low", Admin: true, Created: "2026-03-01T00:00:00Z"},
		)
		r.warehouse(nil)
		stub(t, &edgesFn, func(context.Context, int, int) ([]client.ReferralEdge, error) {
			return []client.ReferralEdge{
				{Referrer: "topco", Referee: "midco", CreatedAt: 1},
				{Referrer: "midco", Referee: "lowco", CreatedAt: 2},
				// a loop back to the top is refused by the engine and left, not retried and not fatal.
				{Referrer: "lowco", Referee: "topco", CreatedAt: 3},
			}, nil
		})
		ctx := context.Background()
		for range 2 {
			if err := r.s.sweepReferrals(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// the low member crosses 1M tokens; both milestone claims are approved by staff.
		if _, err := r.s.emit(ctx, catalogEvent("usage.tokens:sub-low:1000000", "usage.tokens", "sub-low", map[string]any{"tokens": 1_000_000, "model": ""})); err != nil {
			t.Fatal(err)
		}
		for _, c := range r.claimsOf("sub-low", "usage.tokens") {
			if _, err := r.s.eng.Approve(ctx, c.Id(), "staff"); err != nil {
				t.Fatal(err)
			}
		}
		// 10 + 20 points: the mid member (level 1, 10%) earns 1 + 2, the top (level 2, 5%) 0 + 1.
		mid, _ := r.s.eng.Standing(ctx, program, "sub-mid")
		top, _ := r.s.eng.Standing(ctx, program, "sub-top")
		low, _ := r.s.eng.Standing(ctx, program, "sub-low")
		if low.Lifetime != 30 || mid.Lifetime != 3 || top.Lifetime != 1 {
			t.Errorf("lifetime low %d mid %d top %d, want 30, 3, 1", low.Lifetime, mid.Lifetime, top.Lifetime)
		}
		if mid.Redeemable != 0 || top.Redeemable != 0 {
			t.Errorf("upline was redeemable: mid %d top %d", mid.Redeemable, top.Redeemable)
		}
		// and a member's /v1/reward/me shows the network under them.
		code, body := r.do("GET", "/v1/reward/me", memberHeaders("sub-top", "topco"), nil)
		if code != 200 || !strings.Contains(string(body), `"network":{"level1":1,"level2":1,"level3":0,"points":1}`) {
			t.Errorf("me = %d %s", code, body)
		}

	})
}

func TestAnOrgThatUsedHanzoBeforeItClaimedTheCodeIsNoSignUp(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		r.directory(
			cloudUser{ID: "sub-ref", Owner: "acme", Name: "boss", Admin: true, Created: "2026-01-01T00:00:00Z"},
			cloudUser{ID: "sub-old", Owner: "oldco", Name: "owner", Admin: true, Created: "2025-01-01T00:00:00Z"},
		)
		r.warehouse(map[string][]map[string]any{"min(day)": {
			{"organization": "oldco", "first": "2025-02-01", "last": "2026-10-20"},
		}})
		// the code was claimed on 2026-10-01 by an org whose first use was a year before.
		claimed := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).Unix()
		stub(t, &edgesFn, func(context.Context, int, int) ([]client.ReferralEdge, error) {
			return []client.ReferralEdge{{Referrer: "acme", Referee: "oldco", CreatedAt: claimed}}, nil
		})
		if err := r.s.sweepReferrals(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := len(r.claims("sub-ref")) + len(r.claims("sub-old")); n != 0 {
			t.Errorf("an org that was already here earned the referrer %d claims", n)
		}
		if have, _ := r.s.mark(context.Background(), "sent:referral.linked:oldco"); have {
			t.Error("an existing org was linked to a sponsor")
		}

	})
}
