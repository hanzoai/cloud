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
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/ai/capability"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// fable is every name a request reaches Claude Fable 5.2 by: OpenRouter's slug, its
// batch spelling, the ~anthropic alias that resolves to it today, and the
// first-party id Anthropic's own key is asked by (apps/ai/capability).
var fable = []string{"anthropic/claude-fable-5.2", "anthropic/claude-fable-5.2:batch",
	"~anthropic/claude-fable-latest", "claude-fable-5-2", "Anthropic/Claude-Fable-5.2"}

// A plan that does not include Claude Fable 5.2 is refused it before any provider
// is asked, by every name the model answers to, however much money the payer holds;
// a payer with no plan is refused it too. The refusal names what is missing and the
// plan that includes it, and counts nothing.
func TestFableNeedsAPlanThatIncludesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, plan := range []string{"dev", ""} {
			w := newWorld(t, plan, 1, "card")
			w.cash = 50 * usd
			for _, model := range fable {
				g, hit := w.priced(model, ClassPremium)
				if g != nil || hit == nil || hit.Code != CodeEntitlement {
					t.Fatalf("plan %q asking %s: grant %+v hit %+v, want entitlement_required", plan, model, g, hit)
				}
			}
			if s := w.read(); s.Session.Used != 0 || s.Day.Used != 0 {
				t.Fatalf("plan %q: a refused request counted in the plan's windows: %+v", plan, s)
			}
		}
	})
}

// Fast mode is refused to a plan that does not include it, and to a payer with no
// plan, whatever the payer holds.
func TestFastModeNeedsAPlanThatIncludesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, plan := range []string{"dev", ""} {
			w := newWorld(t, plan, 1, "card")
			w.cash = 50 * usd
			g, hit := w.admit(Ask{Model: premium, Class: ClassPremium, Priced: true, Spend: true, Fast: true})
			if g != nil || hit == nil || hit.Code != CodeEntitlement {
				t.Fatalf("plan %q asking fast mode: grant %+v hit %+v, want entitlement_required", plan, g, hit)
			}
		}
	})
}

// A plan that includes them is served both as it was before: Claude Fable 5.2 from
// its included usage of premium models, by every name, and fast mode from prepaid.
func TestAPlanThatIncludesThemIsServedAsBefore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "max-20x", 1, "card")
		w.cash = 5 * usd
		for _, model := range fable {
			g, hit := w.priced(model, ClassPremium)
			if hit != nil || g == nil || g.Pays != PaysPlan || g.Class != ClassPremium {
				t.Fatalf("Max 20x asking %s: grant %+v hit %+v, want the plan paying, premium", model, g, hit)
			}
		}
		g, hit := w.admit(Ask{Model: fable[0], Class: ClassPremium, Priced: true, Spend: true, Fast: true})
		if hit != nil || g == nil || g.Pays != PaysPrepaid || !g.Cash {
			t.Fatalf("Max 20x asking Fable 5.2 in fast mode: grant %+v hit %+v, want prepaid, cash only", g, hit)
		}
	})
}

// The refusal says what is missing — the entitlement by name, and the model it was
// asked for — and the cheapest plan on sale that includes it: above the payer's own
// plan in its category, or in any category for a payer with no plan.
func TestTheRefusalNamesTheEntitlementAndAPlanThatIncludesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, plan := range []string{"dev", ""} {
			w := newWorld(t, plan, 1, "card")
			_, hit := w.priced(fable[0], ClassPremium)
			if hit == nil || hit.Name != flags.EntitleFable || hit.Model != fable[0] || hit.Upgrade != "max-20x" {
				t.Fatalf("plan %q asking Fable 5.2: %+v, want the fable entitlement, the model and max-20x", plan, hit)
			}
			for _, says := range []string{"Claude Fable 5.2", "fable entitlement", "Max 20x includes it"} {
				if !strings.Contains(hit.Message, says) {
					t.Fatalf("plan %q: the refusal %q does not say %q", plan, hit.Message, says)
				}
			}
			if mine := strings.Contains(hit.Message, "yours does not"); mine != (plan != "") {
				t.Fatalf("plan %q: the refusal %q speaks of the payer's plan: %v", plan, hit.Message, mine)
			}
			_, hit = w.admit(Ask{Model: premium, Class: ClassPremium, Priced: true, Spend: true, Fast: true})
			if hit == nil || hit.Name != flags.EntitleFast || hit.Model != "" || hit.Upgrade != "max-20x" ||
				!strings.Contains(hit.Message, "Fast mode") {
				t.Fatalf("plan %q asking fast mode: %+v, want the fast entitlement, no model, max-20x", plan, hit)
			}
		}
	})
}

// The usage policy decides: a SuperAdmin who turns a plan's switch on serves it, and
// one who turns it off refuses it, each entitlement apart from the other.
func TestTheUsagePolicysSwitchDecides(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		set := flags.CarriedValues()
		set[flags.EntitlementKey(flags.EntitleFable, "dev")] = "true"
		set[flags.EntitlementKey(flags.EntitleFast, "max-20x")] = "false"
		w := newWorld(t, "dev", 1, "card")
		w.cash = 5 * usd
		w.l.flag = policy(set)
		if g, hit := w.priced(fable[0], ClassPremium); hit != nil || g == nil || g.Pays != PaysPlan {
			t.Fatalf("Pro with fable turned on: grant %+v hit %+v, want the plan paying", g, hit)
		}
		if _, hit := w.admit(Ask{Model: fable[0], Class: ClassPremium, Priced: true, Spend: true, Fast: true}); hit == nil || hit.Name != flags.EntitleFast {
			t.Fatalf("Pro with fable on and fast off asking both: %+v, want the fast entitlement missing", hit)
		}
		m := newWorld(t, "max-20x", 1, "card")
		m.cash = 5 * usd
		m.l.flag = policy(set)
		if _, hit := m.admit(Ask{Model: premium, Class: ClassPremium, Priced: true, Spend: true, Fast: true}); hit == nil || hit.Code != CodeEntitlement {
			t.Fatalf("Max 20x with fast turned off: %+v, want entitlement_required", hit)
		}
		if g, hit := m.priced(fable[0], ClassPremium); hit != nil || g == nil {
			t.Fatalf("Max 20x with fast off asking Fable 5.2 at standard speed: grant %+v hit %+v, want it served", g, hit)
		}
	})
}

// A request nothing bills — no list price — needs no entitlement, and a model no
// entitlement names is decided as it always was.
func TestOnlyABilledRequestNeedsAnEntitlement(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newWorld(t, "dev", 1, "card")
		if _, hit := w.admit(Ask{Model: fable[0], Class: ClassPremium, Spend: true}); hit != nil && hit.Code == CodeEntitlement {
			t.Fatalf("an unpriced request was refused for an entitlement: %+v", hit)
		}
		for _, model := range []string{"anthropic/claude-opus-5.5", "anthropic/claude-fable-5.1", premium} {
			if g, hit := w.priced(model, ClassPremium); hit != nil || g == nil || g.Pays != PaysPlan {
				t.Fatalf("Pro asking %s: grant %+v hit %+v, want the plan paying", model, g, hit)
			}
		}
	})
}

// The plane's door asks the same question and nothing else: a sibling's call for a
// payer whose plan does not include it is refused, one whose plan does is not, a
// payer it cannot name holds no plan, and a call that needs nothing reads no plan.
func TestEntitleAnswersThePlanesDoor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		ask := Ask{Subject: "acme", Org: "acme", Model: fable[3], Priced: true}
		for plan, refused := range map[string]bool{"dev": true, "max-20x": false} {
			w := newWorld(t, plan, 1, "card")
			hit, err := w.l.Entitle(ctx, ask)
			if err != nil || (hit != nil) != refused || (refused && hit.Code != CodeEntitlement) {
				t.Fatalf("plan %s on the plane: hit %+v err %v, want refused %v", plan, hit, err, refused)
			}
			if hit, err := w.l.Entitle(ctx, Ask{Org: "acme", Model: fable[3], Priced: true}); err != nil || hit == nil {
				t.Fatalf("plan %s, no payer named: hit %+v err %v, want refused", plan, hit, err)
			}
			w.subsErr = errors.New("commerce down")
			w.now = w.now.Add(2 * planTTL)
			if _, err := w.l.Entitle(ctx, ask); err == nil {
				t.Fatalf("plan %s: an unreadable plan answered, want an error", plan)
			}
			if hit, err := w.l.Entitle(ctx, Ask{Subject: "acme", Org: "acme", Model: premium, Priced: true}); err != nil || hit != nil {
				t.Fatalf("plan %s: a call that needs nothing: hit %+v err %v, want neither", plan, hit, err)
			}
		}
	})
}

// Every entitlement a capability record names is one the usage policy keeps, every
// Fable 5.2 name carries it, and every plan shipped with one is a plan the policy
// names, so the shipped answer is never for a plan that includes nothing.
func TestTheEntitlementsAgree(t *testing.T) {
	for _, model := range fable {
		r, ok := capability.Lookup(model)
		if !ok || r.Entitlement != flags.EntitleFable {
			t.Fatalf("%s: record %v entitlement %q, want %q", model, ok, r.Entitlement, flags.EntitleFable)
		}
	}
	for name, plans := range flags.SeedEntitled {
		if !slices.Contains(flags.Entitlements, name) || flags.EntitlementName[name] == "" {
			t.Fatalf("entitlement %q is seeded but not listed or not named", name)
		}
		for _, plan := range plans {
			if _, ok := flags.CarriedPlans[plan]; !ok {
				t.Fatalf("entitlement %q is shipped on for %q, which the usage policy does not carry", name, plan)
			}
		}
	}
}

// The standing says which entitlements the plan includes, the same reading Admit
// refuses by, so a model picker can hide what a request would be refused for: every
// entitlement, included or not, and for one that is not, the plan the refusal would
// name. A payer with no plan includes none; a SuperAdmin's switch moves the answer
// exactly as it moves the refusal.
func TestTheStandingSaysWhichEntitlementsThePlanIncludes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, plan := range []string{"dev", ""} {
			s := newWorld(t, plan, 1, "card").read()
			for _, name := range flags.Entitlements {
				e, ok := s.Entitlements[name]
				if !ok || e.Included || e.Upgrade.Plan != "max-20x" || e.Upgrade.Name != "Max 20x" {
					t.Fatalf("plan %q, %s: %+v (present %v), want not included, Max 20x the way", plan, name, e, ok)
				}
			}
		}
		s := newWorld(t, "max-20x", 1, "card").read()
		for _, name := range flags.Entitlements {
			if e := s.Entitlements[name]; !e.Included || e.Upgrade != (Way{}) {
				t.Fatalf("Max 20x, %s: %+v, want included and no way to name", name, e)
			}
		}

		set := flags.CarriedValues()
		set[flags.EntitlementKey(flags.EntitleFable, "dev")] = "true"
		w := newWorld(t, "dev", 1, "card")
		w.l.flag = policy(set)
		s = w.read()
		if !s.Entitlements[flags.EntitleFable].Included || s.Entitlements[flags.EntitleFast].Included {
			t.Fatalf("Pro with fable turned on: %+v, want fable included and fast not", s.Entitlements)
		}
	})
}
