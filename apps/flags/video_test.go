// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package flags

import (
	"encoding/json"
	"testing"
)

// Every video family is premium and paid in real money only, and the policy the
// migration saves caps it at the share of each carried plan's premium allowance
// it took by default; a plan that includes all premium usage (Unbounded) has no
// allowance to share out and caps no family. The policy with nothing saved states
// no plan's share.
func TestEveryVideoFamilyIsCappedPremium(t *testing.T) {
	var rules map[string]struct {
		Class     string `json:"class"`
		GrantsPay bool   `json:"grantsPay"`
		Caps      map[string]struct {
			PeriodCents int `json:"periodCents"`
		} `json:"caps"`
	}
	if err := json.Unmarshal([]byte(CarriedValues()[ModelPolicy]), &rules); err != nil {
		t.Fatal(err)
	}
	for _, glob := range VideoPremium {
		r, ok := rules[glob]
		if !ok || r.Class != "premium" || r.GrantsPay {
			t.Errorf("%s: %+v, want premium that granted credit never pays", glob, r)
			continue
		}
		for plan, s := range CarriedPlans {
			if s.Premium == Unbounded {
				if c, capped := r.Caps[plan]; capped {
					t.Errorf("%s caps %s, which includes all premium usage, at %d¢", glob, plan, c.PeriodCents)
				}
				continue
			}
			if got, want := r.Caps[plan].PeriodCents, (s.Premium*carriedShare/100+5)/10*10; got != want || got <= 0 {
				t.Errorf("%s on %s takes %d¢, want %d¢", glob, plan, got, want)
			}
		}
	}
	var bare map[string]struct {
		Class string         `json:"class"`
		Caps  map[string]any `json:"caps"`
	}
	if err := json.Unmarshal([]byte(SeedModelPolicy), &bare); err != nil {
		t.Fatal(err)
	}
	for _, glob := range VideoPremium {
		if r, ok := bare[glob]; !ok || r.Class != "premium" || len(r.Caps) != 0 {
			t.Errorf("%s with nothing saved: %+v, want premium and no plan's share", glob, r)
		}
	}
}
