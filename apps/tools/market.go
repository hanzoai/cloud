package tools

// market.go — what the marketplace asks of the tool plane: which tools an org
// can reach, whether it owns the thing it lists, and switching one on or off.
//
// The marketplace runs in its own process, and this registry, its providers and
// its activation store live only in this one. Read in the marketplace process,
// the registry is empty: discovery listed nothing, every publish was an unknown
// tool and every install failed on a store that was never opened. So the three
// questions are asked here, as the org on the call, over the plane.
//
// A listing sells something that exists and is the seller's. The stores that
// know are these — an org's authored skills, its registered MCP servers, and
// the tools its scope resolves — so ownership is answered here and never taken
// from the seller's word.
//
// Resolving is not owning. Every org's scope resolves every connector action and
// first-party service, and every org can enable the same server off the public
// shelf, or type its URL in by hand; none of those is any one org's. So a tool is
// answered with the source it resolved from — an org's own row, or the
// platform's — and a server the shelf lists, however it was registered, is the
// shelf's.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// The MCP registry shelf is public: read with no credential, one operation at a
// time, so the write routes beside it — sync and curate — are never exempted by
// their neighbours.
func init() {
	spec.Open("/v1/tool/catalog", "GET")
	spec.Open("/v1/tool/catalog/:id", "GET")
}

// serveMarket publishes the ops. Use calls it once the stores are open.
func serveMarket() {
	cloud.Plane().Post("/tools/owned", planeOwned,
		zip.WithOperationID(client.ToolsOwned),
		zip.WithSummary("Whether the caller's org owns one skill, MCP server or tool"))
	cloud.Plane().Post("/tools/list", planeList,
		zip.WithOperationID(client.ToolsList),
		zip.WithSummary("The tools the caller's org can reach in one project, each marked activated or not"))
	cloud.Plane().Post("/tools/activate", planeActivate,
		zip.WithOperationID(client.ToolsActivate),
		zip.WithSummary("Switch one tool on or off for the caller's org in one project"))
	cloud.Plane().Post("/tools/review", planeReview,
		zip.WithOperationID(client.ToolsReview),
		zip.WithSummary("Switch on the code-review and dev skills for a pull request's repository"))
}

// planeOwned answers whether the org on the call owns one thing: a skill it
// authored, an MCP server it registered by hand (each by id or name), or a tool
// its own scope resolves from one of its own rows (by name). A tool every org
// reaches — a connector action, a first-party service — is answered as the
// platform's, and a server the public shelf lists — enabled off it, or its URL
// typed in by hand — as nobody's. Not found and another org's are the same answer,
// so the caller learns only whether the thing is its own.
func planeOwned(ctx context.Context, in *client.OwnedIn) (*client.Owned, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tools owned: no org on the call")
	}
	s := mounted
	if s == nil || s.State.skills == nil || s.State.servers == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tools owned: the tool plane is not open")
	}
	ref := strings.TrimSpace(in.Ref)
	switch in.Kind {
	case client.OwnedSkill:
		skills, err := s.State.skills.List(ctx, org)
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "tools owned: %v", err)
		}
		for _, sk := range skills {
			if sk.ID == ref || sk.Name == ref {
				return &client.Owned{Mine: true, ID: sk.ID, Name: sk.Name}, nil
			}
		}
		return &client.Owned{}, nil
	case client.OwnedMCP:
		servers, err := s.State.servers.List(ctx, org)
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "tools owned: %v", err)
		}
		for _, srv := range servers {
			if (srv.ID != ref && srv.Name != ref) || srv.Listing != "" {
				continue
			}
			if s.State.catalog == nil {
				return nil, zip.Errorf(http.StatusServiceUnavailable, "tools owned: the catalog is not open")
			}
			host, dir, ok := locate(srv.URL)
			if !ok {
				return &client.Owned{}, nil // an address no resolver reads is nobody's to sell
			}
			shelf, err := s.State.catalog.Serves(ctx, host, dir)
			if err != nil {
				return nil, zip.Errorf(http.StatusServiceUnavailable, "tools owned: %v", err)
			}
			if shelf {
				return &client.Owned{}, nil
			}
			return &client.Owned{Mine: true, ID: srv.ID, Name: srv.Name}, nil
		}
		return &client.Owned{}, nil
	case client.OwnedTool:
		t, _, ok := Default().resolve(ctx, Scope{Org: org, Project: in.Project}, ref)
		if !ok {
			return &client.Owned{}, nil
		}
		out := &client.Owned{ID: t.Name, Name: t.Name, Source: string(t.Source), Platform: t.Source.Platform()}
		out.Mine = !out.Platform && t.Source != SourceSkill // a brand skill is listed to every org
		return out, nil
	}
	return nil, zip.ErrBadRequest("tools owned: kind is skill, mcp or tool")
}

// planeList answers every tool the org on the call can reach in one project,
// from every source, each marked with whether it is activated there — GET
// /v1/tool, asked by a process that has no registry of its own.
func planeList(ctx context.Context, in *client.ToolsIn) (*client.ToolSet, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tools list: no org on the call")
	}
	tools := Default().List(ctx, Scope{Org: org, Project: in.Project})
	out := &client.ToolSet{Tools: make([]client.ToolBrief, 0, len(tools))}
	for _, t := range tools {
		out.Tools = append(out.Tools, client.ToolBrief{Name: t.Name, Source: string(t.Source), Description: t.Description,
			Schema: t.Schema, Dispatchable: t.Dispatchable, Activated: t.Activated})
	}
	return out, nil
}

// planeActivate switches one tool on or off for the org on the call in one
// project: the one activation write, reached from the marketplace's install and
// uninstall. A tool the scope does not resolve is not switched on — an unknown
// name is refused rather than recorded — while switching off anything is allowed.
//
// A skill or a tool of a server the org registered is switched only for an admin
// of the org or a SuperAdmin, on the trail first, exactly as PUT /v1/tool/activation
// switches one (govern.go): the caller is the one the marketplace forwarded.
func planeActivate(ctx context.Context, in *client.ActivateIn) (*client.Activated, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tools activate: no org on the call")
	}
	name := strings.TrimSpace(in.Tool)
	if !validToolName(name) {
		return nil, zip.ErrBadRequest("tools activate: a tool name is required")
	}
	s := mounted
	if s == nil || s.State.servers == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tools activate: the tool plane is not open")
	}
	if err := mayToggle(ctx, s.State.servers, org, name); err != nil {
		return nil, err
	}
	reg := Default()
	if in.On && !reg.Exists(ctx, Scope{Org: org, Project: in.Project}, name) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", name)
	}
	if err := record(s, ctx, "activation.change", org, "activation", map[string]any{
		"project": projectKey(in.Project), "tool": name, "on": in.On,
	}); err != nil {
		return nil, err
	}
	var err error
	if in.On {
		err = reg.Activate(ctx, org, in.Project, name, cloud.Who(ctx).User, administers(ctx))
	} else {
		err = reg.Deactivate(ctx, org, in.Project, name)
	}
	if err != nil {
		if errors.Is(err, ErrUnknownTool) {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", name)
		}
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tools activate: %v", err)
	}
	return &client.Activated{Tool: name, Installed: in.On}, nil
}

// planeReview switches on the code-review and dev skills for the org on the call,
// in its default scope and in the repository's: what a pull request opened on a
// connected repository asks of the tool plane. It switches only those two, so the
// provider that holds the delivery names no tool and needs no admin to ask.
func planeReview(ctx context.Context, in *client.ReviewIn) (*client.Reviewed, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("tools review: no org on the call")
	}
	s := mounted
	if s == nil || s.State.activation == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "tools review: the tool plane is not open")
	}
	// On the trail before it is made, as every change to what the org's runs carry is.
	if err := record(s, ctx, "activation.review", org, "activation", map[string]any{"project": projectKey(in.Project)}); err != nil {
		return nil, err
	}
	return &client.Reviewed{Activated: activateDefaultSkillsForPR(ctx, org, in.Project)}, nil
}
