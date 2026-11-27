package reward

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/rule"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestTheRulesAreThePublicListOfActiveRulesByPillarInOrder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		// no credential at all: this is the program's public face.
		code, body := r.do(http.MethodGet, "/v1/reward/rules", nobodyHeaders, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /v1/reward/rules = %d %s", code, body)
		}
		var out rulesOut
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, g := range out.Pillars {
			order = append(order, g.ID)
		}
		if !slices.Equal(order, []string{"use", "share", "community", "contribute", "expert"}) {
			t.Fatalf("pillars = %v", order)
		}
		byID := map[string]publicRule{}
		for _, g := range out.Pillars {
			for _, v := range g.Rules {
				if v.Pillar != g.ID {
					t.Errorf("%s is under %s but belongs to %s", v.ID, g.ID, v.Pillar)
				}
				byID[v.ID] = v
			}
		}
		// the seeded values are there, in plain words.
		tok := byID["tokens-100k"]
		if tok.Points != 10 || tok.Event != "Tokens used" || tok.Limits != "once per person" || !tok.Review || tok.Badge == nil || tok.Badge.ID != "first-100k" || tok.Badge.Name != "First 100K" || tok.Badge.Rarity != "common" {
			t.Errorf("tokens-100k = %+v badge %+v", tok, tok.Badge)
		}
		if star := byID["github-star"]; star.Points != 1 || star.Limits != "once per repo" || star.Event != "Repository starred" {
			t.Errorf("github-star = %+v", star)
		}
		if ref := byID["referral-activated"]; ref.Points != 25 || ref.Limits == "" {
			t.Errorf("referral-activated = %+v", ref)
		}
		// an inactive rule is not promised: the weekly share is off until staff price it.
		if _, ok := byID["weekly-share"]; ok {
			t.Error("an inactive rule is on the public list")
		}
		for id, v := range byID {
			if v.CreditCents != 0 {
				t.Errorf("%s promises credit the seed does not grant: %d", id, v.CreditCents)
			}
		}
		// staff turn it on and the list follows; turn it off and it goes.
		w := *mustProgram(t, r).Find("weekly-share")
		w.Active, w.Then = true, []rule.Action{{Type: rule.Do.Points, Points: 5}}
		if _, err := r.s.eng.SaveRule(context.Background(), program, w, "staff"); err != nil {
			t.Fatal(err)
		}
		_, body = r.do(http.MethodGet, "/v1/reward/rules", nobodyHeaders, nil)
		if !json.Valid(body) || !contains(body, `"weekly-share"`) || !contains(body, `"once per network and week"`) {
			t.Errorf("an active rule is missing from the list: %s", body)
		}

	})
}

func contains(b []byte, sub string) bool { return strings.Contains(string(b), sub) }

func mustProgram(t *testing.T, r *rig) *rule.Program {
	t.Helper()
	p, err := r.s.eng.Program(context.Background(), program)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPublicRulesLeaveOutAnInactiveProgramAndShowCreditOnlyWhereARuleGrantsIt(t *testing.T) {
	badge := func(id string) rule.Action { return rule.Action{Type: rule.Do.Badge, Badge: id} }
	on := &rule.Program{Name: "on", Active: true, Badges: []rule.Badge{{ID: "gold", Name: "Gold", Pillar: catalog.Contribute, Rarity: rule.Epic}},
		Rules: []rule.Rule{
			{ID: "pay", Name: "Paid bounty", Pillar: catalog.Contribute, Active: true, Approval: rule.Auto, When: rule.When{Event: "bounty.completed"},
				Then: []rule.Action{{Type: rule.Do.Points, Points: 75}, {Type: rule.Do.Credit, Cents: 2000}, badge("gold"), badge("silver")}, Limits: rule.Limits{PerDay: 2, PerMonth: 10}},
			{ID: "off", Name: "Off", Pillar: catalog.Contribute, Active: false, When: rule.When{Event: "bounty.completed"}, Then: []rule.Action{{Type: rule.Do.Points, Points: 1}}},
		}}
	off := &rule.Program{Name: "off", Active: false, Rules: []rule.Rule{
		{ID: "hidden", Name: "Hidden", Pillar: catalog.Use, Active: true, When: rule.When{Event: "usage.tokens"}, Then: []rule.Action{{Type: rule.Do.Points, Points: 1}}}}}
	out := publicRules([]*rule.Program{on, off})
	var listed []publicRule
	for _, g := range out.Pillars {
		listed = append(listed, g.Rules...)
	}
	if len(listed) != 1 || listed[0].ID != "pay" {
		t.Fatalf("listed = %+v, want only the active rule of the active program", listed)
	}
	v := listed[0]
	if v.Points != 75 || v.CreditCents != 2000 || v.Review || v.Limits != "at most 2 a day, at most 10 a month" || v.Event != "Bounty completed" {
		t.Errorf("rule = %+v", v)
	}
	if v.Badge == nil || v.Badge.Name != "Gold" || v.Badge.Rarity != "epic" {
		t.Errorf("badge = %+v, want the first badge action, named and rare", v.Badge)
	}
	if b, _ := json.Marshal(publicRule{ID: "x", Points: 1}); contains(b, "creditCents") {
		t.Errorf("a rule with no credit action names credit: %s", b)
	}
}
