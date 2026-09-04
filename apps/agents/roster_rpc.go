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

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// exposeRoster publishes the org's agent roster on the internal plane, so a
// membership projection in ANOTHER PROCESS can read it. Mount calls it, beside
// the in-process client.
func exposeRoster() {
	zip.Post[client.RosterIn, client.AgentRoster](cloud.Plane(), "/agents/roster", planeRoster,
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
	out := make([]client.AgentBrief, 0, len(ags))
	for _, a := range ags {
		out = append(out, client.AgentBrief{ID: a.ID, Name: a.Name, Status: a.Status})
	}
	return &client.AgentRoster{Agents: out}, nil
}
