package agents

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	"github.com/hanzoai/cloud/sdk"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// skills.go — a skill attached to an agent is read by the agent.
//
// An agent attaches a skill by naming it in its tools as skill_<name>. A skill is
// never called, so the tool loop drops it; what it contributes is its SKILL.md,
// which a run appends to the agent's instructions. The tool plane decides which
// of the named skills the org enabled and which document each name means
// (client.ToolsSkillDocs), so a skill the org has not enabled reaches no prompt.
//
// A run whose skills cannot be read runs without them, and the step span says so
// (hanzo.agent.skills_declared beside hanzo.agent.skills), the same way a run that
// was offered fewer tools than it declared does.

const (
	// skillPrefix marks a skill in an agent's tools.
	skillPrefix = "skill_"
	// maxSkillBytes bounds what skills add to one run's prompt.
	maxSkillBytes = 64 << 10
	// skillDocsTimeout bounds the ask, including waking a cold tool plane.
	skillDocsTimeout = 10 * time.Second
)

// readSkillDocs is the tool plane's documents op; a test substitutes it.
var readSkillDocs = toolpeer.ToolsSkillDocs

// attachedSkills is the skill names an agent's tools carry, in order, once each.
func attachedSkills(a Agent) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range a.Tools {
		t = strings.TrimSpace(t)
		if !strings.HasPrefix(t, skillPrefix) || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// withSkills is the agent's instructions with the documents of its enabled,
// attached skills after them, bounded by maxSkillBytes. An agent that attaches
// none gets its instructions unchanged and asks nothing.
func withSkills(ctx context.Context, org string, a Agent, span trace.Span) string {
	names := attachedSkills(a)
	if len(names) == 0 || org == "" {
		return a.Instructions
	}
	span.SetAttributes(attribute.Int("hanzo.agent.skills_declared", len(names)))
	ctx, cancel := context.WithTimeout(cloud.For(ctx, org), skillDocsTimeout)
	defer cancel()
	out, err := readSkillDocs(ctx, &client.SkillDocsIn{Names: names})
	if err != nil {
		span.SetAttributes(attribute.String("hanzo.agent.skills_error", err.Error()))
		return a.Instructions
	}
	var sections []string
	used := 0
	for _, d := range outSkills(out) {
		section := "## " + d.Name + "\n\n" + strings.TrimSpace(d.Content)
		if used+len(section) > maxSkillBytes {
			break
		}
		sections = append(sections, section)
		used += len(section)
	}
	span.SetAttributes(attribute.Int("hanzo.agent.skills", len(sections)))
	if len(sections) == 0 {
		return a.Instructions
	}
	head := "# Skills\n\nThese are skills you have. Follow one when the request is what it covers."
	if a.Instructions != "" {
		head = a.Instructions + "\n\n" + head
	}
	return head + "\n\n" + strings.Join(sections, "\n\n")
}

// outSkills is a documents answer's skills; a void answer holds none.
func outSkills(out *client.SkillDocs) []client.SkillDoc {
	if out == nil {
		return nil
	}
	return out.Skills
}
