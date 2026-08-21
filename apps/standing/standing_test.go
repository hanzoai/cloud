package standing

import (
	"strings"
	"testing"
	"time"
)

// THE CLAIM THIS SURFACE EXISTS TO MAKE. Delaware is cheaper to FORM than
// Wyoming for a corporation and dearer to KEEP. A founder shown only the
// formation fee is shown the half that reverses, so if this ever stops being
// true the surface is telling people the wrong thing.
func TestUpkeep_WyomingIsCheaperToKeepThanDelaware(t *testing.T) {
	wy, err := UpkeepOf(StructureLLC, JurisdictionWY, false)
	if err != nil {
		t.Fatal(err)
	}
	de, err := UpkeepOf(StructureLLC, JurisdictionDE, false)
	if err != nil {
		t.Fatal(err)
	}
	if wy.YearlyCents >= de.YearlyCents {
		t.Fatalf("WY $%d/yr is not cheaper than DE $%d/yr — the incorporation advice this surface gives has inverted",
			wy.YearlyCents/100, de.YearlyCents/100)
	}
}

// An entity quoted at nothing per year reads as free to keep, which is the most
// expensive wrong answer here.
func TestUpkeep_RefusesAJurisdictionItCannotPrice(t *testing.T) {
	if _, err := UpkeepOf(StructureLLC, Jurisdiction("ZZ"), false); err == nil {
		t.Fatal("reported obligations for a jurisdiction with none known — an entity that costs nothing to keep")
	} else if !strings.Contains(err.Error(), "ZZ") {
		t.Fatalf("the refusal must name the jurisdiction, got: %v", err)
	}
}

// A FRANCHISE TAX THAT SCALES IS A FLOOR, NOT A PRICE. Quoting the minimum as
// final is how a bill later gets disputed.
func TestUpkeep_ScalingTaxIsMarkedAMinimum(t *testing.T) {
	de, err := UpkeepOf(StructureCCorp, JurisdictionDE, false)
	if err != nil {
		t.Fatal(err)
	}
	if !de.AtLeast {
		t.Error("DE corporate franchise tax scales with shares; the total must report as a floor")
	}
	var found bool
	for _, o := range de.Obligations {
		if o.Code == "franchise_tax" && o.Minimum {
			found = true
		}
	}
	if !found {
		t.Error("no franchise_tax obligation marked minimum")
	}
}

// Every state line is money we collect and remit, and carries the authority that
// publishes it. Without a source and a date nobody can tell a checked figure from
// a guessed one.
func TestUpkeep_StateObligationsCarryProvenance(t *testing.T) {
	for _, j := range []Jurisdiction{JurisdictionDE, JurisdictionWY} {
		c, err := UpkeepOf(StructureLLC, j, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Obligations) == 0 {
			t.Fatalf("%s reported no obligations", j)
		}
		for _, o := range c.Obligations {
			if !o.PassThrough {
				t.Errorf("%s: %s is not marked pass-through", j, o.Code)
			}
			if o.Source == "" || o.AsOf == "" {
				t.Errorf("%s: %s has no provenance (source=%q asOf=%q)", j, o.Code, o.Source, o.AsOf)
			}
		}
	}
}

// The agent fee is OURS, so it carries no source — and it is added only when
// asked for, because an entity may already have an agent.
func TestUpkeep_AgentIsOptionalAndNotPassThrough(t *testing.T) {
	without, err := UpkeepOf(StructureLLC, JurisdictionWY, false)
	if err != nil {
		t.Fatal(err)
	}
	with, err := UpkeepOf(StructureLLC, JurisdictionWY, true)
	if err != nil {
		t.Fatal(err)
	}
	if with.YearlyCents-without.YearlyCents != agentFeeCents() {
		t.Fatalf("agent changed the yearly by %d, want %d", with.YearlyCents-without.YearlyCents, agentFeeCents())
	}
	for _, o := range with.Obligations {
		if o.Code == "agent_of_record" {
			if o.PassThrough {
				t.Error("our own agent fee is not money we remit to a state")
			}
			if o.Source != "" {
				t.Error("a price of ours needs no external authority")
			}
		}
	}
}

// Staleness is the point of AsOf: a figure nobody has checked inside the window
// says so rather than looking as fresh as one checked yesterday.
func TestStale(t *testing.T) {
	now := time.Now()
	if stale(now.Format("2006-01-02"), now) {
		t.Error("a figure checked today is not stale")
	}
	if !stale(now.Add(-2*reviewWindow).Format("2006-01-02"), now) {
		t.Error("a figure older than the review window must report stale")
	}
	if !stale("not-a-date", now) {
		t.Error("an unparseable date is not a date anyone checked")
	}
	if stale("", now) {
		t.Error("an amount with no review date (ours) is never stale")
	}
}
