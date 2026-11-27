package client

import (
	"reflect"
	"testing"
)

func TestRewardFieldsKeepTheirKinds(t *testing.T) {
	in := map[string]any{"tokens": float64(1200000), "labels": []any{"a", "b"}, "repo": "hanzoai/x", "on": true}
	fs, err := RewardFields(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(fs); i++ {
		if fs[i-1].Key >= fs[i].Key {
			t.Fatalf("not sorted: %v", fs)
		}
	}
	got, err := (&RewardEventIn{Fields: fs}).FieldMap()
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip = %v, %v", got, err)
	}
}

func TestOrgTotalsAndPillarsAreSorted(t *testing.T) {
	b := &BountyTotals{Orgs: OrgTotals(map[string]int{"b": 2, "a": 1})}
	if b.Orgs[0].Org != "a" || !reflect.DeepEqual(b.Map(), map[string]int{"a": 1, "b": 2}) {
		t.Fatalf("%v", b.Orgs)
	}
	p := &RewardPerson{Pillars: PillarCounts(map[string]int{"use": 3, "share": 1})}
	if p.Pillars[0].Pillar != "share" || p.Count("use") != 3 || p.Count("none") != 0 {
		t.Fatalf("%v", p.Pillars)
	}
}
