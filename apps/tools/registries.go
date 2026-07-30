package tools

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// The separately-listed registries: /v1/skills, /v1/mcp, /v1/plugins.
//
// /v1/skills and /v1/mcp are the SAME registry as /v1/tools viewed through one
// Source each, so a client asking "what skills does this org have" does not
// have to know to pass ?source=skill. They are views, not stores — a tool is
// still registered in exactly one place (tools.Register) and activation lives
// in exactly one place (ActivationStore), which is what keeps a source from
// drifting into its own half-parallel plane.
//
// /v1/mcp additionally owns the EXTERNAL MCP SERVER registry (the connection
// records), because a server is a thing an org creates and deletes, not a tool
// the registry enumerates. That surface MOVED here from /v1/tools/servers — it
// was not copied.
//
// /v1/plugins is deliberately NOT a tool source. A plugin here is a mounted
// subsystem (cloud.Plugin: Name, Mount, Price, Prefixes) — code that extends
// the deployment's own surface — whereas a tool is something an agent calls
// through that surface. Its inventory is cloud.Subsystems(), the boot snapshot
// Declare installs, so this reports what the binary actually mounted rather
// than a second list that could disagree with it.

// listBySource renders the caller's tools for exactly one Source. Filtering
// happens against the per-principal list, so activation and precedence are
// already applied — a source view can never widen what the caller may see.
func listBySource(src Source) func(*cloud.Service[state], *zip.Ctx) error {
	return func(_ *cloud.Service[state], c *zip.Ctx) error {
		p, ok := PrincipalFrom(c)
		if !ok {
			return zip.ErrForbidden("a validated principal is required")
		}
		all := Default().List(c.Context(), Scope{Org: p.Org, Project: p.Project})
		activatedOnly := c.Query("activated") == "true"
		out := make([]Tool, 0, len(all))
		for _, t := range all {
			if t.Source != src {
				continue
			}
			if activatedOnly && !t.Activated {
				continue
			}
			out = append(out, t)
		}
		return c.JSON(http.StatusOK, map[string]any{
			"source": string(src),
			"tools":  out,
		})
	}
}

// putSkill — POST /v1/skills: add or revise one of the caller org's skills.
// Writing the same name again revises it, so an org converges on a set rather
// than accumulating near-duplicates that would collide in the registry.
func putSkill(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if s.State.skills == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "the skill store is not open")
	}
	var in Skill
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("malformed body: " + err.Error())
	}
	in.Name = strings.TrimSpace(in.Name)
	if !pluginName.MatchString(in.Name) {
		return zip.ErrBadRequest("name must be one lowercase path segment (a-z0-9, _ or -)")
	}
	if strings.TrimSpace(in.Content) == "" {
		return zip.ErrBadRequest("content is required (the SKILL.md body)")
	}
	if len(in.Content) > maxSkillContent {
		return zip.ErrBadRequest("content too large")
	}
	in.Org = p.Org
	out, err := s.State.skills.Put(c.Context(), in)
	if err != nil {
		return err
	}
	audrecordAction(s, c, "skill.put", p.Org, out.Name, "success", http.StatusCreated)
	return c.JSON(http.StatusCreated, map[string]any{"skill": out})
}

// deleteSkill — DELETE /v1/skills/:id.
func deleteSkill(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if s.State.skills == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "the skill store is not open")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("missing skill id")
	}
	if err := s.State.skills.Delete(c.Context(), p.Org, id); err != nil {
		return err
	}
	audrecordAction(s, c, "skill.delete", p.Org, id, "success", http.StatusOK)
	return c.JSON(http.StatusOK, map[string]any{"deleted": id})
}

// listAuthoredSkills — GET /v1/skills/authored: the org's own skills with their
// bodies. GET /v1/skills is the registry view (brand + org, activation flags);
// this is the editable set, so it carries content the registry view omits.
func listAuthoredSkills(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if s.State.skills == nil {
		return c.JSON(http.StatusOK, map[string]any{"skills": []Skill{}})
	}
	out, err := s.State.skills.List(c.Context(), p.Org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"skills": out})
}

// listPlugins reports the mounted-subsystem inventory: every plugin the
// composition root declared and whether it is switched on. Read off the same
// boot snapshot every traced request resolves its subsystem label against, so
// this surface cannot drift from what is actually serving.
//
// Enabled-only by default — a caller asking what this deployment can do wants
// what is running. ?all=true includes the configured-but-off ones.
func listPlugins(_ *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := PrincipalFrom(c); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	all := c.Query("all") == "true"
	subs := cloud.Subsystems()
	out := make([]map[string]any, 0, len(subs))
	for _, s := range subs {
		if !all && !s.Enabled {
			continue
		}
		out = append(out, map[string]any{
			"name":     s.Name,
			"prefixes": s.Prefixes,
			"enabled":  s.Enabled,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"plugins": out})
}
