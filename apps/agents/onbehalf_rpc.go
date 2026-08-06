// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agents

// onbehalf_rpc.go carries an agent turn across a PROCESS boundary, exactly as
// sessions_rpc.go carries a teardown. onbehalf.go stays the in-process seam and
// keeps its promise to know nothing of zip.Ctx or the wire; this file is the
// door, and both run the same runOnBehalf underneath.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// exposeRunOnBehalf publishes the on-behalf-of run on the internal plane, so a
// chat bridge in ANOTHER PROCESS can reach it.
//
// RunOnBehalf above gates on `mounted`, a package global, and a package global
// is per-PROCESS. When agents and integrations are separate plugins — which is
// the normal deployment, not an exotic one — that global is nil on the bridge's
// side and every @hanzo turn died with ErrNoPeer. The in-process seam is not
// wrong; it was simply the ONLY door, so co-residency had quietly become a
// requirement nothing declared.
//
// Both doors run the SAME runOnBehalf, so the org isolation, the linked-subject
// attribution and the billing that hang off it are identical whichever way the
// call arrived.
func exposeRunOnBehalf() {
	zip.Post[plane.RunOnBehalfIn, plane.RunOnBehalfOut](cloud.Plane(), "/agents/run-on-behalf", planeRunOnBehalf,
		zip.WithOperationID(plane.AgentsRunOnBehalf),
		zip.WithSummary("Run one agent turn as a linked user, for a chat bridge in another process"))
}

// planeRunOnBehalf answers a bridge's turn.
//
// Unlike the session ops, the org travels IN the request rather than being taken
// from the caller's plane identity: the tenant here is the one that connected the
// Slack workspace, resolved by the bridge from the signed team_id, and the bridge
// plugin's own identity is not it. That is safe because this op only SPENDS the
// named org's own balance under its own agent — it reads nothing across tenants —
// and because the subject must be a link the bridge already proved.
//
// An empty subject is refused rather than defaulted. A turn that lost its caller
// must not run AS THE ORG: that would bill the tenant for an unattributable act
// and hand an unlinked user the org's agent.
func planeRunOnBehalf(ctx context.Context, in *plane.RunOnBehalfIn) (*plane.RunOnBehalfOut, error) {
	if mounted == nil {
		return nil, fmt.Errorf("%w: agents", cloud.ErrNoPeer)
	}
	if strings.TrimSpace(in.Subject) == "" {
		return nil, fmt.Errorf("agents: run-on-behalf requires a linked subject")
	}
	run, err := runOnBehalf(mounted, ctx, in.Org, in.Subject, in.Ref, in.Input)
	if err != nil {
		return nil, err
	}
	return &plane.RunOnBehalfOut{Status: run.Status, Output: run.Output, RunID: run.ID}, nil
}
