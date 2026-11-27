package skills

import (
	"testing"

	house "github.com/hanzoai/personas"
)

// TestTheTeamsSkillsAreServed holds hanzoai/personas to this catalogue: every
// role skill a member of Hanzo's team names is one the catalogue serves, so a
// seat never attaches a skill_<name> that reaches no prompt, and Dev carries the
// software set.
func TestTheTeamsSkillsAreServed(t *testing.T) {
	team, err := house.Team()
	if err != nil {
		t.Fatal(err)
	}
	carried := 0
	for _, m := range team {
		for _, s := range m.Skills {
			carried++
			if _, ok := builtinSkills[s]; !ok {
				t.Errorf("%s names skill %q, which the catalogue does not serve", m.ID, s)
			}
		}
		if m.ID == "dev" && (len(m.Skills) < 2 || m.Skills[0] != "dev" || m.Skills[1] != "code-review") {
			t.Errorf("dev carries %v, want dev and code-review", m.Skills)
		}
	}
	if carried == 0 {
		t.Fatal("no member names a skill")
	}
}
