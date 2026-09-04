package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// skills_repo.go — an org's skills are the SKILL.md files in its repositories.
//
// The brand's catalogue is generated and embedded; an org's own skills are rows
// in the skill store, written through POST /v1/tool/skills. That leaves a team
// that keeps its skills in a repository — reviewed, versioned, one pull request
// per change — copying them across by hand. Hanzo Git reads
// `.agents/skills/<name>/SKILL.md` from a default-branch push and hands the set
// here over the plane; this replaces what that repository contributed. No
// registration names the repository first: holding the files is the whole
// declaration.

// skillsDir is where a repository keeps its skills, the Agent Skills layout.
const skillsDir = ".agents/skills"

// serveSkills publishes the replacement on the plane. Mount calls it once the
// store is open.
func serveSkills() {
	zip.Post[client.SkillsIn, client.Skills](cloud.Plane(), "/tools/skills", planeSkills,
		zip.WithOperationID(client.ToolsSkills),
		zip.WithSummary("Replace the skills one repository contributes to the caller's org"))
}

// planeSkills turns the files into skills and makes them the source's whole
// contribution. The ORG is the caller's, from the plane context: a repository
// can only ever write the skills of the org that holds it.
func planeSkills(ctx context.Context, in *client.SkillsIn) (*client.Skills, error) {
	if in == nil || strings.TrimSpace(in.Source) == "" {
		return nil, zip.ErrBadRequest("skills: source is required")
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("skills: no org on the call")
	}
	s := mounted
	if s == nil || s.State.skills == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "skills: the skill store is not open")
	}
	var skills []Skill
	var refused []string
	for _, f := range in.Files {
		sk, ok := skillFromFile(f.Path, f.Content)
		if !ok {
			refused = append(refused, f.Path)
			continue
		}
		skills = append(skills, sk)
	}
	if err := s.State.skills.Replace(ctx, org, strings.TrimSpace(in.Source), skills); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "skills: %v", err)
	}
	return &client.Skills{Count: len(skills), Refused: refused}, nil
}

// skillFromFile reads one repository file as a skill. The name is the directory
// the file sits in, under skillsDir, and must be one lowercase path segment as
// POST /v1/tool/skills requires; the description is the frontmatter's, which
// the brand's generator writes as a JSON string and a person writes bare. The
// content is the whole file, frontmatter included, as the agent will read it.
func skillFromFile(p, content string) (Skill, bool) {
	p = strings.TrimPrefix(p, "/")
	dir, base := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	if base != "SKILL.md" || path.Dir(dir) != skillsDir {
		return Skill{}, false
	}
	name := path.Base(dir)
	if !pluginName.MatchString(name) || strings.TrimSpace(content) == "" || len(content) > maxSkillContent {
		return Skill{}, false
	}
	return Skill{Name: name, Description: frontmatterDescription(content), Content: content}, true
}

// frontmatterDescription is the `description:` line of a leading YAML block,
// unquoted when it was written as a JSON string; "" when there is none.
func frontmatterDescription(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	if !strings.HasPrefix(md, "---\n") {
		return ""
	}
	block, _, ok := strings.Cut(md[len("---\n"):], "\n---")
	if !ok {
		return ""
	}
	for _, line := range strings.Split(block, "\n") {
		if v, found := strings.CutPrefix(line, "description:"); found {
			v = strings.TrimSpace(v)
			if strings.HasPrefix(v, `"`) {
				var s string
				if json.Unmarshal([]byte(v), &s) == nil {
					return s
				}
			}
			return strings.Trim(v, `'`)
		}
	}
	return ""
}
