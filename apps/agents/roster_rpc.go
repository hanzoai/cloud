// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agents

// roster_rpc.go carries the agent ROSTER across a process boundary, exactly as
// onbehalf_rpc.go carries a turn. listfororg.go stays the in-process client;
// this file is the endpoint, and both read the same store underneath.
//
// It exists for the same reason its sibling does, one verb over. ListForOrg
// gates on `mounted`, a package global, and a package global is per-PROCESS —
// so a surface in another plugin that asks "which agents does this org have"
// got ErrNoPeer. The difference from the run is what it cost: a failed run is
// visibly a failed run, while a failed ROSTER READ is an empty list, and a
// caller renders an empty list as "this org has no agents". apps/team's Chunter
// membership projection did exactly that — GET /v1/team/bots answered [] for an
// org holding agents, and the mention responder found no bot to address and
// silently answered nothing, in a deployment whose logs said the responder was
// ENABLED.

import (
	"context"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
)

// exposeRoster publishes the org's agent roster on the internal plane, so a
// membership projection in ANOTHER PROCESS can read it. Mount calls it, beside
// the in-process client.
func exposeRoster() {
	cloud.Plane().Post("/agents/roster", planeRoster,
		zip.WithOperationID(client.AgentsRoster),
		zip.WithSummary("The agents of the caller's org, as a membership projection reads them"))
}

// planeRoster answers the caller's own agents.
//
// The org is the caller's plane identity and NEVER an argument — client.RosterIn
// has no fields at all, so a cross-tenant read is unrepresentable here rather
// than merely refused. That is the opposite choice from planeRunOnBehalf, which
// does take an org, and the difference is the direction of the act: that op
// SPENDS the named org's own balance under its own agent and reads nothing
// across tenants, while this one ENUMERATES rows and is exactly the shape a
// stated org would leak.
//
// Anonymous is refused rather than defaulted: a roster read arriving with no
// principal must fail, not pick a tenant.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRoster(ctx context.Context, _ *client.RosterIn) (*client.AgentRoster, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agents roster: org required")
	}
	ags, err := ListForOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	// A never-nil slice, because the wire distinction that matters downstream is
	// "no agents" versus "could not ask", and an error is how the second is said.
	// A nil slice marshals to null and reads as neither.
	out := make([]iam.AgentBrief, 0, len(ags))
	for _, a := range ags {
		out = append(out, rosterOf(a))
	}
	return &client.AgentRoster{Agents: out}, nil
}

func rosterOf(a Agent) iam.AgentBrief {
	return iam.AgentBrief{ID: a.ID, Name: a.Name, Status: a.Status, Parent: a.Parent}
}

// exposeLineage publishes one agent's lineage on the internal plane, for the
// process that attests which principal an agent answers to (/v1/principal). Mount
// calls it.
func exposeLineage() {
	cloud.Plane().Post("/agents/lineage", planeLineage,
		zip.WithOperationID(client.AgentsLineage),
		zip.WithSummary("One of the caller's agents and the agents that spawned it"))
}

// planeLineage answers one of the caller's agents and every agent above it,
// nearest first.
//
// The org is the caller's and never an argument — client.LineageIn names the
// agent, and the agent is resolved in the caller's own file — so an agent of
// another org is not found here, and neither is any ancestor that is not in the
// caller's org. An ancestor deleted since the agent was spawned is answered by id
// with no name: the lineage is a fact about how the agent came to exist, and a
// later delete does not rewrite it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeLineage(ctx context.Context, in *client.LineageIn) (*client.Lineage, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agents lineage: org required")
	}
	sto, org, err := mountedStore(org)
	if err != nil {
		return nil, err
	}
	a, err := sto.Resolve(ctx, org, in.Ref)
	if err == errNotFound {
		return nil, zip.ErrNotFound("agent not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "agents lineage: %v", err)
	}
	out := &client.Lineage{Agent: rosterOf(a), Parents: make([]iam.AgentBrief, 0, len(a.Lineage))}
	for _, id := range a.Lineage {
		p, err := sto.ByID(ctx, org, id)
		switch {
		case err == errNotFound:
			out.Parents = append(out.Parents, iam.AgentBrief{ID: id, Status: "deleted"})
		case err != nil:
			return nil, zip.Errorf(http.StatusInternalServerError, "agents lineage: %v", err)
		default:
			out.Parents = append(out.Parents, rosterOf(p))
		}
	}
	return out, nil
}
