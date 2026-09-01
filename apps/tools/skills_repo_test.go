package tools

import (
	"strings"
	"testing"
)

func TestSkillFromFile(t *testing.T) {
	body := "---\nname: incident-response\ndescription: \"Run the incident playbook\"\n---\n\n# Incident response\n"
	sk, ok := skillFromFile(".agents/skills/incident-response/SKILL.md", body)
	if !ok || sk.Name != "incident-response" || sk.Description != "Run the incident playbook" || sk.Content != body {
		t.Fatalf("skill: ok=%v %+v", ok, sk)
	}
	if sk, ok := skillFromFile("/.agents/skills/deploy/SKILL.md", "---\ndescription: ship it\n---\nbody"); !ok || sk.Description != "ship it" {
		t.Fatalf("leading slash and bare description: ok=%v %+v", ok, sk)
	}
	if sk, ok := skillFromFile(".agents/skills/plain/SKILL.md", "# no frontmatter"); !ok || sk.Description != "" {
		t.Fatalf("no frontmatter is a skill with no description: ok=%v %+v", ok, sk)
	}
	for _, p := range []string{
		".agents/skills/SKILL.md",          // no directory
		".agents/skills/a/b/SKILL.md",      // nested
		"skills/a/SKILL.md",                // not the layout
		".agents/skills/Bad Name/SKILL.md", // not one lowercase segment
		".agents/skills/a/README.md",       // not the file
		"../.agents/skills/a/SKILL.md",     // escaping
	} {
		if _, ok := skillFromFile(p, "x"); ok {
			t.Fatalf("%q must not be a skill", p)
		}
	}
	if _, ok := skillFromFile(".agents/skills/a/SKILL.md", "   "); ok {
		t.Fatal("an empty body is not a skill")
	}
	if _, ok := skillFromFile(".agents/skills/a/SKILL.md", strings.Repeat("x", maxSkillContent+1)); ok {
		t.Fatal("a body over the cap is not a skill")
	}
}
