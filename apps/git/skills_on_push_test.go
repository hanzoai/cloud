package git

import "testing"

func TestIsSkillPath(t *testing.T) {
	yes := []string{".agents/skills/deploy/SKILL.md", ".agents/skills/a-b_1/SKILL.md"}
	no := []string{".agents/skills/SKILL.md", ".agents/skills/a/b/SKILL.md", ".agents/skills/a/README.md", "skills/a/SKILL.md", "x/.agents/skills/a/SKILL.md"}
	for _, p := range yes {
		if !isSkillPath(p) {
			t.Fatalf("%q is a skill file", p)
		}
	}
	for _, p := range no {
		if isSkillPath(p) {
			t.Fatalf("%q is not a skill file", p)
		}
	}
	if skillSource("", "r") != "r" || skillSource("p", "r") != "p/r" {
		t.Fatal("source naming")
	}
}
