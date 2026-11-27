package agents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"go.opentelemetry.io/otel/trace/noop"
)

// A run reads the documents of the skills its agent attaches, after the agent's
// own instructions; an agent that attaches none asks nothing.
func TestAnAttachedSkillReachesTheRunsInstructions(t *testing.T) {
	span := noop.Span{}
	prev := readSkillDocs
	t.Cleanup(func() { readSkillDocs = prev })

	var asked []string
	readSkillDocs = func(_ context.Context, in *client.SkillDocsIn) (*client.SkillDocs, error) {
		asked = append(asked, in.Names...)
		return &client.SkillDocs{Skills: []client.SkillDoc{{Name: "deploy", Content: "Ship with the release train.\n"}}}, nil
	}

	plain := Agent{Name: "a", Instructions: "Be brief.", Tools: []string{"slack_post_message"}}
	if got := withSkills(context.Background(), "acme", plain, span); got != "Be brief." || len(asked) != 0 {
		t.Fatalf("no skill attached: %q asked=%v", got, asked)
	}

	a := Agent{Name: "a", Instructions: "Be brief.", Tools: []string{"skill_deploy", "slack_post_message", "skill_deploy", "skill_triage"}}
	got := withSkills(context.Background(), "acme", a, span)
	if !strings.HasPrefix(got, "Be brief.\n\n# Skills") || !strings.HasSuffix(got, "## deploy\n\nShip with the release train.") {
		t.Fatalf("instructions: %q", got)
	}
	if strings.Join(asked, ",") != "skill_deploy,skill_triage" {
		t.Fatalf("asked for %v", asked)
	}
}

// A run whose skills cannot be read runs on its own instructions; skills never
// grow a prompt past maxSkillBytes.
func TestSkillsFailOpenToTheAgentsInstructionsAndAreBounded(t *testing.T) {
	span := noop.Span{}
	prev := readSkillDocs
	t.Cleanup(func() { readSkillDocs = prev })
	a := Agent{Name: "a", Instructions: "Be brief.", Tools: []string{"skill_a", "skill_b"}}

	readSkillDocs = func(context.Context, *client.SkillDocsIn) (*client.SkillDocs, error) { return nil, errors.New("down") }
	if got := withSkills(context.Background(), "acme", a, span); got != "Be brief." {
		t.Fatalf("an unreadable skill set: %q", got)
	}

	big := strings.Repeat("x", maxSkillBytes-100)
	readSkillDocs = func(context.Context, *client.SkillDocsIn) (*client.SkillDocs, error) {
		return &client.SkillDocs{Skills: []client.SkillDoc{{Name: "a", Content: big}, {Name: "b", Content: big}}}, nil
	}
	got := withSkills(context.Background(), "acme", a, span)
	if !strings.Contains(got, "## a\n\n") || strings.Contains(got, "## b\n\n") {
		t.Fatal("the second skill does not fit and must be left out")
	}
}
