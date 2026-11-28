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
	"errors"
	"strings"
	"testing"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
)

// Past a model's share (when a rule explicitly caps a plan's class share), usage
// credits the payer bought keep that model, with no choice to make. With none,
// the paused model reaches the customer read as names, states and actions only —
// and no figure.
func TestAPausedModelReadsWithoutAFigure(t *testing.T) {
	ctx := context.Background()
	withCappedPremium := func(t *testing.T, cash int64) *limits.Limiter {
		t.Helper()
		vals := flags.CarriedValues()
		vals[flags.PlanKey("premium", "max-20x")] = "10000"
		vals[flags.ModelPolicy] = `{"anthropic/claude-opus*":{"class":"premium","grantsPay":false,"caps":{"max-20x":{"periodCents":3000}}}}`
		now := time.Now().UTC()
		rows := []client.Subscription{{ID: "sub-1", PlanID: "rec-max-20x", Status: "active", Quantity: 1, ProviderType: "square", Settled: "external:comp",
			Seats: 1, ChargedCents: 20000, Plan: client.SubscriptionPlan{Price: 20000, Interval: "month"},
			CurrentPeriodStart: now.AddDate(0, 0, -1).Format(time.RFC3339),
			CurrentPeriodEnd:   now.AddDate(0, 1, -1).Format(time.RFC3339)}}
		l, err := limits.Open(t.TempDir(),
			func(context.Context, string, string) (string, string, error) { return "max-20x", "sub-1", nil },
			func(context.Context, string, string) ([]client.Subscription, error) { return rows, nil },
			func(context.Context) ([]client.PlanRow, error) {
				return []client.PlanRow{
					{Slug: "max-20x", Name: "Max 20x", Category: "personal", Price: 20000, Status: "active"},
					{Slug: "max-50x", Name: "Max 50x", Category: "personal", Price: 50000, Status: "active"},
				}, nil
			},
			func(context.Context, string, string) (int64, int64, error) { return cash, 0, nil },
			func(_ context.Context, _ string, in *client.FlagValueIn) (*client.FlagValue, error) {
				v, ok := vals[in.Key]
				if !ok {
					return &client.FlagValue{Value: in.Default}, nil
				}
				return &client.FlagValue{Value: v, Set: true}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}

	cash := withCappedPremium(t, 3*usd)
	g, _, _ := limitAsk(cash)(ctx, premium)
	g.Settle(30 * usd)
	if g, hit, _ := limitAsk(cash)(ctx, premium); hit != nil || g == nil || g.Pays != aiobject.PaysPrepaid || !g.Cash {
		t.Fatalf("opus past its share with cash: grant %+v hit %+v, want opus on the cash", g, hit)
	}
	l := withCappedPremium(t, 0)
	g, _, _ = limitAsk(l)(ctx, premium)
	g.Settle(30 * usd)
	_, hit, _ := limitAsk(l)(ctx, premium)
	if hit == nil || hit.Code != aiobject.CodeModelCap || hit.Fallback != "enso-auto" {
		t.Fatalf("opus past its share: %+v, want model_cap to enso-auto, offering nothing granted", hit)
	}
	s, err := l.Read(ctx, "acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view(s, "https://hanzo.ai/pay"))
	var got aiLimits
	_ = json.Unmarshal(raw, &got)
	if len(got.Paused) != 1 || got.Paused[0].Model != "anthropic/claude-opus*" || got.Paused[0].Fallback != "enso-auto" {
		t.Fatalf("view %s", raw)
	}
	kinds := []string{}
	for _, a := range got.Actions {
		kinds = append(kinds, a.Kind)
	}
	if strings.Join(kinds, ",") != "upgrade,topup" {
		t.Fatalf("actions %v, want Max 50x above it, then buying usage credits", kinds)
	}
	if m := figure.FindString(string(raw)); m != "" {
		t.Fatalf("the customer read carries a figure (%q): %s", m, raw)
	}
}

// A user gets 1 free self-service usage reset per 7 days: resetting clears spend,
// session, and daily counts and enters cooldown until 7 days later.
func TestFreeWeeklyResetClearsUsageAndEnforcesCooldown(t *testing.T) {
	ctx := context.Background()
	l := payer(t, "max-20x", "card", 0, 0)
	g, hit, err := limitAsk(l)(ctx, kai)
	if err != nil || hit != nil || g == nil {
		t.Fatalf("admit kai: %+v %+v %v", g, hit, err)
	}
	g.Settle(50 * usd)

	before, err := l.Read(ctx, "acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if before.Classes[limits.ClassOurs].Period.Used != 50*usd || before.Reset == nil || !before.Reset.Available {
		t.Fatalf("before reset: used=%d reset=%+v", before.Classes[limits.ClassOurs].Period.Used, before.Reset)
	}

	st, err := l.FreeReset(ctx, "acme", "acme")
	if err != nil || st.Available || st.NextAt.IsZero() {
		t.Fatalf("first FreeReset: st=%+v err=%v", st, err)
	}

	after, err := l.Read(ctx, "acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if after.Classes[limits.ClassOurs].Period.Used != 0 || after.Session.Used != 0 || after.Reset == nil || after.Reset.Available {
		t.Fatalf("after reset: used=%d sess=%d reset=%+v", after.Classes[limits.ClassOurs].Period.Used, after.Session.Used, after.Reset)
	}

	v := view(after, "https://hanzo.ai/pay")
	if v.Reset == nil || v.Reset.Available || v.Reset.NextAt == nil || v.Reset.LastAt == nil {
		t.Fatalf("view reset after reset: %+v", v.Reset)
	}

	// Second reset within 7 days is rejected with ResetCooldownError.
	_, err = l.FreeReset(ctx, "acme", "acme")
	var cool *limits.ResetCooldownError
	if !errors.As(err, &cool) || cool.NextAt.IsZero() {
		t.Fatalf("second FreeReset within cooldown: err=%v, want ResetCooldownError", err)
	}
}
