// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package agents

// onbehalf_rpc.go carries an agent turn across a PROCESS boundary, exactly as
// sessions_rpc.go carries a teardown. onbehalf.go stays the in-process client and
// keeps its promise to know nothing of zip.Ctx or the wire; this file is the
// door, and both run the same runOnBehalf underneath.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// exposeRunOnBehalf publishes the on-behalf-of run on the internal plane, so a
// chat bridge in ANOTHER PROCESS can reach it.
//
// RunOnBehalf above gates on `mounted`, a package global, and a package global
// is per-PROCESS. When agents and integrations are separate plugins — which is
// the normal deployment, not an exotic one — that global is nil on the bridge's
// side and every @hanzo turn died with ErrNoPeer. The in-process client is not
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
	// The tenant this run bills is NOT stated here, and the reason is worth writing
	// down because the obvious fix is wrong and was shipped once.
	//
	// A run bills: the balance gate is a plane call to commerce, which takes the org
	// from the CALLER's identity and never from an argument (balance_rpc.go:36), so
	// no caller can name the books it charges. It is tempting to satisfy that with
	// cloud.For(ctx, in.Org) right here. That is a NO-OP. This op is reached over the
	// plane, which is a real request, and zip reads a STATED caller only where there
	// is NO request (caller.go:352-356) — otherwise CallerOf reads the request's own
	// headers. The statement is silently discarded and the gate still answers
	// `authorize: no org on the call`. That is exactly what production did.
	//
	// The org must therefore be on the WIRE, stated by the dispatcher on a detached
	// context before the hop (Caller.headers renders it, caller.go:302). The bridge
	// does that — see the cloud.For(context.Background(), org) at the plane.Ask in
	// apps/integrations/channel.go. By the time we are here it has already arrived as
	// a header and rides onward for free. in.Org remains in the payload because the
	// run RECORD needs it; it is not what authorizes the spend.
	run, err := runOnBehalfModel(mounted, ctx, in.Org, in.Subject, in.Ref, in.Input, in.Model, transcript(in.History))
	if err != nil {
		return nil, err
	}
	return &plane.RunOnBehalfOut{Status: run.Status, Output: run.Output, RunID: run.ID, Error: run.Error}, nil
}

// transcript turns the room's turns into the conversation the model reads.
//
// Self is the whole of it: a turn the assistant SAID has to come back as an
// assistant turn, or the model reads its own answers as things the user told it
// and starts agreeing with itself. The bridge knows which are which because it
// asked the platform; nothing in this process could work it out.
//
// The turns arrived over the plane, which is why the conversion exists at all:
// plane.Turn is the wire's shape and types.ChatMessage is the model's, and
// neither package should have to know the other's.
func transcript(turns []plane.Turn) []types.ChatMessage {
	if len(turns) == 0 {
		return nil
	}
	msgs := make([]types.ChatMessage, 0, len(turns))
	for _, t := range turns {
		if strings.TrimSpace(t.Text) == "" {
			continue // an event with no words is not a turn
		}
		role := types.RoleUser
		if t.Self {
			role = types.RoleAssistant
		}
		msgs = append(msgs, types.ChatMessage{Role: role, Content: t.Text})
	}
	return msgs
}
