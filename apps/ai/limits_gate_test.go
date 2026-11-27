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

package ai

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	aiobject "github.com/hanzoai/ai/object"

	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// payer is one org as the limiter sees it: on plan (none when empty), paid as
// settled, holding cash and credit in nano-dollars.
func payer(t *testing.T, plan, settled string, cash, credit int64) *limits.Limiter {
	t.Helper()
	now := time.Now().UTC()
	var rows []client.Subscription
	if plan != "" {
		rows = []client.Subscription{{ID: "sub-1", PlanID: "rec-" + plan, Status: "active", Quantity: 1, ProviderType: "square", Settled: settled,
			Seats: 1, ChargedCents: 20000, Plan: client.SubscriptionPlan{Price: 20000, Interval: "month"},
			CurrentPeriodStart: now.AddDate(0, 0, -1).Format(time.RFC3339),
			CurrentPeriodEnd:   now.AddDate(0, 1, -1).Format(time.RFC3339)}}
	}
	l, err := limits.Open(t.TempDir(),
		func(context.Context, string, string) (string, string, error) { return plan, "sub-1", nil },
		func(context.Context, string, string) ([]client.Subscription, error) { return rows, nil },
		func(context.Context) ([]client.PlanRow, error) {
			return []client.PlanRow{
				{Slug: "dev", Name: "Pro", Category: "personal", Price: 2000, Status: "active"},
				{Slug: "max-5x", Name: "Max 5x", Category: "personal", Price: 10000, Status: "active"},
				{Slug: "max-20x", Name: "Max 20x", Category: "personal", Price: 20000, Status: "active"},
				{Slug: "max-50x", Name: "Max 50x", Category: "personal", Price: 50000, Status: "active"},
			}, nil
		},
		func(context.Context, string, string) (int64, int64, error) { return cash, credit, nil },
		carried)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

const usd = 1_000_000_000

var (
	enso    = aiobject.LimitAsk{Subject: "acme", Namespace: "acme", Actor: "acme/ann", Model: "enso", Family: "enso", Class: aiobject.ClassFree, Apps: []string{"hanzo-app"}, Spend: true}
	premium = aiobject.LimitAsk{Subject: "acme", Namespace: "acme", Actor: "acme/ann", Model: "anthropic/claude-opus-5.5", Class: aiobject.ClassPremium, Priced: true, Spend: true}
	kai     = aiobject.LimitAsk{Subject: "acme", Namespace: "acme", Actor: "acme/ann", Model: "kai", Class: aiobject.ClassOurs, Priced: true}
	jev     = aiobject.LimitAsk{Subject: "acme", Namespace: "acme", Actor: "acme/ann", Model: "typesafe/jev-1.13", Class: aiobject.ClassOurs, Priced: true}
)

// The hook speaks the ai module's words: who pays, the class, where it stands, and a
// settle that records a covered request's list price against the plan.
func TestTheHookSaysWhoPays(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, plan, settled string
		cash, credit        int64
		q                   aiobject.LimitAsk
		pays, code          string
		cash1               bool
	}{
		{"max under its allowance: the plan pays premium", "max-20x", "card", 0, 0, premium, aiobject.PaysPlan, "", false},
		{"a comp counts", "max-20x", "external:comp", 0, 0, kai, aiobject.PaysPlan, "", false},
		{"bought with credit covers nothing", "max-20x", "credit", 0, 0, premium, "", aiobject.CodeInsufficientBalance, false},
		{"free plan, premium on credit alone", "", "", 0, usd, premium, "", aiobject.CodeInsufficientBalance, false},
		{"free plan, premium on cash: cash only", "", "", usd, usd, premium, aiobject.PaysPrepaid, "", true},
		{"free plan, kai inside its cap, empty wallet", "", "", 0, 0, kai, aiobject.PaysFree, "", false},
		{"free plan, kai inside its cap, granted credit", "", "", 0, usd, kai, aiobject.PaysCredits, "", false},
		{"free plan, jev on credit alone", "", "", 0, usd, jev, "", aiobject.CodeInsufficientBalance, false},
		{"free plan, jev on prepaid: cash only", "", "", usd, usd, jev, aiobject.PaysPrepaid, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, hit, err := limitAsk(payer(t, tc.plan, tc.settled, tc.cash, tc.credit))(ctx, tc.q)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.code != "":
				if hit == nil || hit.Code != tc.code || g != nil {
					t.Fatalf("grant %+v hit %+v, want %s", g, hit, tc.code)
				}
			case tc.pays == "":
				if g != nil || hit != nil {
					t.Fatalf("grant %+v hit %+v, want neither", g, hit)
				}
			default:
				if hit != nil || g == nil || g.Pays != tc.pays || g.Cash != tc.cash1 || g.Class == "" || g.State == "" {
					t.Fatalf("grant %+v hit %+v, want %s (cash only %v)", g, hit, tc.pays, tc.cash1)
				}
			}
		})
	}
}

// A covered request's settle records its list price against its class.
func TestACoveredSettleRecordsAgainstItsClass(t *testing.T) {
	ctx := context.Background()
	l := payer(t, "max-20x", "card", 0, 0)
	g, _, _ := limitAsk(l)(ctx, premium)
	g.Settle(2 * usd)
	s, err := l.Read(ctx, "acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if s.Classes[limits.ClassPremium].Period.Used != 2*usd || s.Classes[limits.ClassOurs].Period.Used != 0 || s.Session.Used != 1 {
		t.Fatalf("standing after one covered premium request of $2: %+v", s)
	}
}

// A spent window comes back as the module's hit, with its code and the plan that
// raises it.
func TestASpentWindowIsTheModulesHit(t *testing.T) {
	ctx := context.Background()
	l := payer(t, "dev", "card", 0, 0)
	for i := 0; i < flags.CarriedPlans["dev"].Session; i++ {
		if _, hit, err := limitAsk(l)(ctx, enso); err != nil || hit != nil {
			t.Fatalf("request %d: %v %+v", i, err, hit)
		}
	}
	_, hit, err := limitAsk(l)(ctx, enso)
	if err != nil || hit == nil || hit.Code != aiobject.CodeUsageCap || hit.Name != "session" || hit.Upgrade != "max-5x" || hit.ResetsAt.IsZero() {
		t.Fatalf("hit %+v err %v, want the session with max-5x as the way on", hit, err)
	}
}

// A call a plan covered draws no wallet: the recorder passes it by, while an
// uncovered call's debit still lands.
func TestACoveredCallNeverReachesTheWallet(t *testing.T) {
	ctx := context.Background()
	var debited []aiobject.UsageEvent
	wallet := func(_ context.Context, u aiobject.UsageEvent) error { debited = append(debited, u); return nil }
	rec := record(nil, wallet)
	if err := rec(ctx, aiobject.UsageEvent{Subject: "acme", Namespace: "acme", Model: "kai", USD: "0.021", Plan: true}); err != nil {
		t.Fatal(err)
	}
	if len(debited) != 0 {
		t.Fatalf("a covered call reached the wallet: %+v", debited)
	}
	if err := rec(ctx, aiobject.UsageEvent{Subject: "acme", Namespace: "acme", Model: "vendor/priced", USD: "0.36"}); err != nil {
		t.Fatal(err)
	}
	if len(debited) != 1 || debited[0].USD != "0.36" {
		t.Fatalf("an uncovered priced call's debit: %+v", debited)
	}
}

// figure is anything in the customer read that could be a dollar, a cent, a token
// count or a cap: a currency sign, a decimal amount, or a key that names one.
var figure = regexp.MustCompile(`\$|"(used|limit|cents|amount|usd|tokens|cap|remaining|balance)"|\d+\.\d+`)

// The usage read publishes shares, states and the ways on — never a dollar, a cent,
// a count or a cap. A grep of its JSON finds no figure.
func TestTheUsageReadCarriesNoFigure(t *testing.T) {
	ctx := context.Background()
	l := payer(t, "max-20x", "external:comp", 0, 0)
	g, _, _ := limitAsk(l)(ctx, premium)
	g.Settle(100 * usd) // premium used up, nothing prepaid: limited
	g, _, _ = limitAsk(l)(ctx, kai)
	g.Settle(4 * usd) // $4 of ours' $100: 5%
	s, err := l.Read(ctx, "acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view(s, "https://hanzo.ai/pay"))
	if err != nil {
		t.Fatal(err)
	}
	var got aiLimits
	_ = json.Unmarshal(raw, &got)
	if got.Plan != "max-20x" || got.State != "limited" || got.Classes["premium"].State != "limited" || got.Classes["premium"].Paying != "none" ||
		got.Classes["ours"].Percent != 5 || got.Classes["ours"].Paying != "plan" || got.Limited == nil || got.Limited.Reason != "plan_allowance_used" {
		t.Fatalf("view %s", raw)
	}
	if len(got.Actions) != 2 || got.Actions[0].Kind != "upgrade" || got.Actions[0].Plan != "max-50x" || got.Actions[1].Kind != "topup" {
		t.Fatalf("actions %+v, want Max 50x, the plan above max-20x, then add prepaid credit", got.Actions)
	}
	if m := figure.FindString(string(raw)); m != "" {
		t.Fatalf("the customer read carries a figure (%q): %s", m, raw)
	}
	free, _ := json.Marshal(view(limits.Standing{Upgrade: limits.Way{Plan: "dev", Name: "Pro"}}, "https://hanzo.ai/pay"))
	if m := figure.FindString(string(free)); m != "" || !strings.Contains(string(free), `"plan":"free"`) {
		t.Fatalf("no plan reads %s", free)
	}
}

// A Max org with nothing prepaid is served embeddings and speech by its plan, as
// Hanzo models; a call that fails is released through the hook, and the plan's
// windows read as they did before it.
func TestAMaxOrgGetsRetrievalAndSpeechFromThePlan(t *testing.T) {
	ctx := context.Background()
	l := payer(t, "max-20x", "external:square", 0, 0)
	for _, model := range []string{"zen-embedding", "zen-rerank", "zen-scribe", "zen-voice-mini"} {
		q := aiobject.LimitAsk{Subject: "acme", Namespace: "acme", Actor: "acme/ann", Model: model, Class: aiobject.ClassOurs, Priced: true}
		g, hit, err := limitAsk(l)(ctx, q)
		if err != nil || hit != nil || g == nil || g.Pays != aiobject.PaysPlan || !g.Covered() {
			t.Fatalf("%s at $0 on Max: grant %+v hit %+v err %v, want the plan paying", model, g, hit, err)
		}
	}
	before, _ := l.Read(ctx, "acme", "acme")
	for i := 0; i < 10; i++ {
		g, _, _ := limitAsk(l)(ctx, enso)
		if g == nil || g.Release == nil {
			t.Fatalf("a counted free-lane grant carries no Release: %+v", g)
		}
		g.Release() // the call answered 503
	}
	after, _ := l.Read(ctx, "acme", "acme")
	if after.Session.Used != before.Session.Used || after.Day.Used != before.Day.Used {
		t.Fatalf("ten unserved calls moved the windows: session %d→%d day %d→%d", before.Session.Used, after.Session.Used, before.Day.Used, after.Day.Used)
	}
}

// A class the plan includes none of reads as used up — paid by credits when the
// payer holds them, else limited — never as an untouched 0%.
func TestAClassThePlanOmitsReadsAsUsed(t *testing.T) {
	end := time.Now().Add(72 * time.Hour)
	s := limits.Standing{Plan: "dev", Start: time.Now(), End: end, Classes: map[string]limits.Class{
		"premium": {Period: limits.Window{ResetsAt: end}},
		"ours":    {Period: limits.Window{Used: 1, Limit: 100, ResetsAt: end}, Paying: limits.PaysPlan},
	}}
	got := view(s, "https://hanzo.ai/pay")
	if p := got.Classes["premium"]; p.Percent != 100 || p.State != "limited" || p.Paying != "none" || got.Limited == nil {
		t.Fatalf("premium omitted by the plan reads %+v (limited %+v)", p, got.Limited)
	}
	s.Funded = true
	c := s.Classes["premium"]
	c.Paying = limits.PaysCredits
	s.Classes["premium"] = c
	if p := view(s, "https://hanzo.ai/pay").Classes["premium"]; p.Paying != "credits" || p.State != "near" {
		t.Fatalf("premium on credits reads %+v", p)
	}
}

// carried is the flag plane as the usage policy's one migration left it: every
// carried plan's figures saved, and nothing else set.
func carried(_ context.Context, _ string, in *client.FlagValueIn) (*client.FlagValue, error) {
	v, ok := flags.CarriedValues()[in.Key]
	if !ok {
		return &client.FlagValue{Value: in.Default}, nil
	}
	return &client.FlagValue{Value: v, Set: true}, nil
}
