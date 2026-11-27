package tools

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	skillspeer "github.com/hanzoai/cloud/client/skills"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// skill_docs.go — the documents of the skills an agent names.
//
// An agent attaches a skill by naming it in its tools (skill_<name>). A skill is
// never dispatched; what the agent needs is the SKILL.md, in its prompt. This op
// answers those documents for the org on the call, and it is the tool plane that
// answers because the plane holds the two facts that decide it: whether the org
// enabled the skill, and whether the brand's catalogue or the org's own store
// owns the name. The brand's wins, exactly as it does in a listing.

// maxSkillDocs bounds one answer, whatever the agent names.
const maxSkillDocs = 16

// brandDocs is the skills app's documents op; a test substitutes it.
var brandDocs = skillspeer.SkillsDocuments

// serveSkillDocs publishes the op. Mount calls it once the stores are open.
func serveSkillDocs() {
	cloud.Plane().Post("/tools/skill-docs", planeSkillDocs,
		zip.WithOperationID(client.ToolsSkillDocs),
		zip.WithSummary("Read the documents of the enabled skills an agent names"))
}

// planeSkillDocs answers the documents of the enabled skills among the names, in
// the order asked. Activation is read in the org's default scope, where an
// agent's own dispatches run. A chat agent carries what a coding run carries
// (skillDocs): a skill an admin enabled, and of the org's own only what an admin
// wrote.
func planeSkillDocs(ctx context.Context, in *client.SkillDocsIn) (*client.SkillDocs, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("skills: no org on the call")
	}
	s := mounted
	if s == nil || s.State.skills == nil || s.State.activation == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "skills: the tool plane is not open")
	}
	return skillDocs(ctx, s.State.activation, s.State.skills, org, in.Names)
}

// skillDocs is the op's body over its two stores, and the one rule for what an
// agent carries of the org's skills, a chat agent's and a coding run's (kit.go)
// alike: a skill an admin of the org enabled, and of the org's own only one an
// admin wrote. A repository push writes a skill nobody administering the org
// attested, and a member enables nothing an agent carries. The brand's is
// carried wherever an admin enabled it.
func skillDocs(ctx context.Context, act *ActivationStore, store *SkillStore, org string, names []string) (*client.SkillDocs, error) {
	enabled, err := act.Admitted(ctx, org, "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "skills: %v", err)
	}
	var want []string
	seen := map[string]bool{}
	for _, n := range names {
		name := strings.TrimPrefix(strings.TrimSpace(n), "skill_")
		if name == "" || seen[name] || len(want) == maxSkillDocs {
			continue
		}
		seen[name] = true
		if slices.Contains(enabled, "skill_"+name) || slices.Contains(defaultSkillNames, name) {
			want = append(want, name)
		}
	}
	out := &client.SkillDocs{Skills: []client.SkillDoc{}}
	if len(want) == 0 {
		return out, nil
	}

	content := map[string]string{}
	brand, err := brandDocs(ctx, &client.SkillDocsIn{Names: want})
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
	case err != nil:
		return nil, zip.Errorf(http.StatusBadGateway, "skills: the brand's documents: %v", err)
	case brand != nil:
		for _, d := range brand.Skills {
			content[d.Name] = strings.Clone(d.Content)
		}
	}
	own, err := store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "skills: %v", err)
	}
	for _, sk := range own {
		if !sk.Admitted {
			continue
		}
		if _, taken := content[sk.Name]; !taken && seen[sk.Name] {
			content[sk.Name] = sk.Content
		}
	}
	for _, name := range want {
		if c, ok := content[name]; ok {
			out.Skills = append(out.Skills, client.SkillDoc{Name: name, Content: c})
		}
	}
	return out, nil
}
