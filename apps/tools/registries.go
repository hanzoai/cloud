package tools

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// The separately-listed registries: /v1/tool/skills, /v1/tool/mcp/servers, /v1/tool/plugins.
//
// /v1/tool/skills is the SAME registry as /v1/tool viewed through one Source, so a
// client asking "what skills does this org have" does not have to know to pass
// ?source=skill. It is a view, not a store — a tool is still registered in
// exactly one place (tools.Register) and activation lives in exactly one place
// (ActivationStore), which is what keeps a source from drifting into its own
// half-parallel plane.
//
// /v1/tool/mcp/servers owns the EXTERNAL MCP SERVER registry (the connection records),
// because a server is a thing an org creates and deletes, not a tool the registry
// enumerates. The tools those servers offer are reported by GET /v1/tool with
// ?source=mcp — there is no second view of them, and /v1/mcp itself is the
// FLEET's one agent MCP address, served by the host.
//
// /v1/tool/plugins is deliberately NOT a tool source. A plugin here is a mounted
// subsystem (cloud.Plugin: Name, Mount, Price, Prefixes) — code that extends
// the deployment's own surface — whereas a tool is something an agent calls
// through that surface. Its inventory is cloud.Subsystems(), the boot snapshot
// Declare installs, so this reports what the binary actually mounted rather
// than a second list that could disagree with it.

// sourceQuery narrows a single-source listing. Activated is a STRING and not a
// bool for the same reason it is on GET /v1/tool: the route has always compared
// the raw query value to the literal "true", and a bool field would make zip's
// binder read `?activated=1` and a bare `?activated` as true, which is a
// different set of tools for the same URL.
type sourceQuery struct {
	// Activated keeps only the tools activated for the caller's org and project,
	// and only when it is exactly the string "true".
	Activated string `json:"activated"`
}

// sourceToolList is one source's slice of the registry, with the source named so
// the answer is self-describing.
type sourceToolList struct {
	// Source is the source these tools came from.
	Source string `json:"source"`
	// Tools is the caller's tools from that source. Never null.
	Tools []Tool `json:"tools"`
}

// ListSkills lists the skills the caller's org can reach — the brand's embedded
// catalogue plus the org's own authored ones — with each one's activation flag.
// A skill is discovery and activation metadata attached to an agent, never called
// directly, so every entry here is non-dispatchable. It is GET /v1/tool narrowed
// to one source, not a second store: a name a caller sees here is the same entry,
// with the same activation state, that discovery reports.
func (o toolOps) listSkills(ctx context.Context, in *sourceQuery) (*sourceToolList, error) {
	return o.bySource(ctx, SourceSkill, in)
}

// bySource is the ONE body behind the source view: it filters the
// PER-PRINCIPAL list, so activation and precedence are already applied and a
// source view can never widen what the caller may see.
func (o toolOps) bySource(ctx context.Context, src Source, in *sourceQuery) (*sourceToolList, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	all := Default().List(ctx, scope)
	activatedOnly := in.Activated == "true"
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
	return &sourceToolList{Source: string(src), Tools: out}, nil
}

// skillIn is one skill to write. The store derives the id from the name and
// stamps the creation time, and the org is the caller's validated one — so a body
// carrying id, org or createdAt has those values ignored, exactly as before.
type skillIn struct {
	// Name is the skill's id within the org: one lowercase path segment
	// (a-z0-9, _ or -). Writing an existing name REVISES that skill.
	Name string `json:"name"`
	// Description is the one-line summary discovery shows for the skill.
	Description string `json:"description"`
	// Content is the SKILL.md body. Required, at most 256 KiB.
	Content string `json:"content"`
}

// skillWritten acknowledges a skill write with the stored record.
type skillWritten struct {
	// Skill is the skill as stored, with its derived id and creation time.
	Skill Skill `json:"skill"`
}

// PutSkill adds or revises one of the caller org's own skills, and answers 201
// with the stored record. The id is derived from the name, so writing the same
// name again REVISES that skill rather than accumulating near-duplicates that
// would then collide in the registry. An org's skills are private to it by
// construction — they live in a different store from the brand's embedded
// catalogue and have no path into the public gallery — and a brand skill always
// wins a name collision against an org's.
//
// Example: {"name": "triage", "description": "how we triage", "content": "# Triage\n…"}
func (o toolOps) putSkill(ctx context.Context, in *skillIn) (*skillWritten, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.State.skills == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the skill store is not open")
	}
	name := strings.TrimSpace(in.Name)
	if !pluginName.MatchString(name) {
		return nil, zip.ErrBadRequest("name must be one lowercase path segment (a-z0-9, _ or -)")
	}
	if strings.TrimSpace(in.Content) == "" {
		return nil, zip.ErrBadRequest("content is required (the SKILL.md body)")
	}
	if len(in.Content) > maxSkillContent {
		return nil, zip.ErrBadRequest("content too large")
	}
	out, err := o.s.State.skills.Put(ctx, Skill{
		Org: org, Name: name, Description: in.Description, Content: in.Content,
	})
	if err != nil {
		return nil, err
	}
	o.audit(ctx, "skill.put", org, out.Name, "success", http.StatusCreated)
	return &skillWritten{Skill: out}, nil
}

// skillRef addresses one authored skill. The id is the path segment: the URL is
// the addressing authority.
type skillRef struct {
	// ID is the skill to remove, from the path. It is the skill's name.
	ID string `json:"id"`
}

// skillDeleted acknowledges a skill removal.
type skillDeleted struct {
	// Deleted is the skill id that is now gone.
	Deleted string `json:"deleted"`
}

// DeleteSkill removes one of the caller org's authored skills. Scoped to the
// caller's org, so an id belonging to another tenant is never reached. Removing
// what is not there is not an error — the caller's intent is "gone", and it is.
func (o toolOps) deleteSkill(ctx context.Context, in *skillRef) (*skillDeleted, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.State.skills == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the skill store is not open")
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("missing skill id")
	}
	if err := o.s.State.skills.Delete(ctx, org, id); err != nil {
		return nil, err
	}
	o.audit(ctx, "skill.delete", org, id, "success", http.StatusOK)
	return &skillDeleted{Deleted: id}, nil
}

// authoredSkillList is the caller org's own skills, bodies included. Never null.
type authoredSkillList struct {
	// Skills is every skill this org authored, each with its SKILL.md content.
	Skills []Skill `json:"skills"`
}

// ListAuthoredSkills lists the caller org's OWN skills with their SKILL.md
// bodies. GET /v1/tool/skills is the registry view — the brand's catalogue plus this
// org's, with activation flags and no bodies; this is the EDITABLE set, so it
// carries the content that view omits and nothing the org did not write.
func (o toolOps) listAuthoredSkills(ctx context.Context, _ *cloud.Unit) (*authoredSkillList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.State.skills == nil {
		return &authoredSkillList{Skills: []Skill{}}, nil
	}
	out, err := o.s.State.skills.List(ctx, org)
	if err != nil {
		return nil, err
	}
	return &authoredSkillList{Skills: out}, nil
}

// pluginQuery widens the mounted-subsystem inventory. All is a STRING and not a
// bool for the same reason the activated filters are: the route has always
// compared the raw query value to the literal "true".
type pluginQuery struct {
	// All includes the configured-but-disabled subsystems too, but only when it is
	// exactly the string "true". Otherwise only the running ones are reported.
	All string `json:"all"`
}

// pluginMount is one mounted subsystem of this deployment. The fields are
// alphabetical because the wire they replace was a Go map, which encoding/json
// writes in sorted key order.
type pluginMount struct {
	// Enabled is whether this subsystem is switched on in this deployment.
	Enabled bool `json:"enabled"`
	// Name is the subsystem's name, the same label a traced request resolves to.
	Name string `json:"name"`
	// Prefixes are the URL prefixes this subsystem serves.
	Prefixes []string `json:"prefixes"`
}

// pluginMountList is this deployment's mounted-subsystem inventory. Never null.
type pluginMountList struct {
	// Plugins is every subsystem the composition root declared, filtered to the
	// enabled ones unless all=true.
	Plugins []pluginMount `json:"plugins"`
}

// ListPlugins reports what this deployment actually mounted: every subsystem the
// composition root declared and whether it is switched on. A plugin here is
// MOUNTED CODE that extends the deployment's own surface — not a tool an agent
// calls — so this is an inventory and not a tool source. It is read off the same
// boot snapshot every traced request resolves its subsystem label against, so it
// cannot drift from what is serving. Enabled-only by default, because a caller
// asking what this deployment can do wants what is running; ?all=true adds the
// configured-but-off ones.
func (o toolOps) listPlugins(ctx context.Context, in *pluginQuery) (*pluginMountList, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	all := in.All == "true"
	subs := cloud.Subsystems()
	out := make([]pluginMount, 0, len(subs))
	for _, s := range subs {
		if !all && !s.Enabled {
			continue
		}
		out = append(out, pluginMount{Enabled: s.Enabled, Name: s.Name, Prefixes: s.Prefixes})
	}
	return &pluginMountList{Plugins: out}, nil
}
