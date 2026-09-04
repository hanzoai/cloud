package git

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	toolspeer "github.com/hanzoai/cloud/plane/tool"
)

// skills_on_push.go — a push to a repository's default branch replaces the
// skills that repository contributes to its org.
//
// The files are `.agents/skills/<name>/SKILL.md`, the Agent Skills layout. Git
// reads them from the pushed tree and hands them to tools over the plane
// (plane.ToolsSkills); tools decides what is a skill. Reading is bounded the
// way the code index is: one file cap, one count cap. Like the other reactors
// it is detached and best-effort — a tools plane that is not answering is
// logged, and the push has already landed.

const (
	skillsDir         = ".agents/skills"
	maxSkillFileBytes = 256 << 10 // one SKILL.md, the cap tools applies
	maxSkillFiles     = 256
	skillsCallTimeout = 30 * time.Second
)

// skillsOnPush reads the pushed tip's skill files and replaces the repository's
// contribution. A feature-branch push is not the repository's skills.
func skillsOnPush(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	if ev.Kind != cloud.LifecyclePushLanded || ev.Org == "" || ev.Repo == "" || ev.Branch == "" {
		return
	}
	repo, err := openRepository(s, Repo{Org: ev.Org, Project: ev.Project, Name: ev.Repo})
	if err != nil {
		s.Log.Warn("skills: open failed", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if !isDefaultBranch(ctx, repo, ev.Branch) {
		return
	}
	files, err := treeSkillFiles(ctx, repo, ev.After)
	if err != nil {
		s.Log.Warn("skills: read failed", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(cloud.For(ctx, ev.Org), skillsCallTimeout)
	defer cancel()
	out, err := toolspeer.ToolsSkills(ctx, &plane.SkillsIn{Source: skillSource(ev.Project, ev.Repo), Files: files})
	if err != nil {
		s.Log.Warn("skills: replace failed", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	s.Log.Info("skills: replaced", "org", ev.Org, "repo", ev.Repo, "count", out.Count, "refused", len(out.Refused))
}

// skillSource names the repository the way tools records it: "<project>/<name>",
// or "<name>" for an org-level repository.
func skillSource(project, repo string) string {
	if project == "" {
		return repo
	}
	return project + "/" + repo
}

// isSkillPath reports whether a repo-relative path is a skill file: exactly
// `.agents/skills/<name>/SKILL.md`, one directory deep.
func isSkillPath(p string) bool {
	rest, ok := strings.CutPrefix(p, skillsDir+"/")
	if !ok {
		return false
	}
	name, file, ok := strings.Cut(rest, "/")
	return ok && name != "" && file == "SKILL.md"
}

// treeSkillFiles resolves the pushed tip and returns its skill files, bounded.
func treeSkillFiles(ctx context.Context, repo Repository, after string) ([]plane.SkillFile, error) {
	rev, _, err := repo.Resolve(ctx, after)
	if err != nil {
		return nil, err
	}
	var out []plane.SkillFile
	err = repo.WalkText(ctx, rev, maxSkillFileBytes, func(path, content string) error {
		if !isSkillPath(path) {
			return nil
		}
		if len(out) >= maxSkillFiles {
			return StopWalk
		}
		out = append(out, plane.SkillFile{Path: path, Content: content})
		return nil
	})
	return out, err
}
