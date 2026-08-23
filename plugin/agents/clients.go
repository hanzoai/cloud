package main

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Everything a coding run needs from agents, published on the internal plane.
//
// A coding run opens a live SESSION, streams its progress into it, resolves and
// gates the MACHINE an `on <machine>` run was addressed to, and enqueues that run
// on the durable engine. All four were direct calls into this package from the
// integrations process, and all four read `mounted` — this process's global,
// nil over there — so the whole coding path answered "agents: not mounted"
// before it ever reached a model. The @hanzo chat turn failed in exactly this
// shape until it moved onto the plane; this is the rest of that move.
//
// The ORG rides in each argument rather than on the caller's plane identity.
// That is the exception plane.RunOnBehalfIn documents, and it holds for the same
// reason: the tenant is the one that connected the Slack workspace, resolved
// server-side from the signature-verified team_id, and the bridge plugin's own
// identity is not it. Every op below touches only the named org's own session,
// its own machines and its own queue.
//
// Declared at the app's composition root because the ROUTE op needs both halves
// — this app's mailbox and coding's workflow — and coding imports agents, so
// agents can never import coding. A main is a leaf; that is what makes it the
// one place both can be held. Registering on cloud.Plane() before cloud.Listen
// is safe and ordinary: an op registry is a value, ServePlane binds the socket
// over it afterwards.
func init() {
	zip.Post[plane.SessionOpenIn, plane.SessionOpened](cloud.Plane(), "/agents/session/open", planeSessionOpen,
		zip.WithOperationID(plane.AgentsSessionOpen),
		zip.WithSummary("Open the live session a coding run streams into"))

	zip.Post[plane.SessionEventIn, plane.CodingAck](cloud.Plane(), "/agents/session/event", planeSessionEvent,
		zip.WithOperationID(plane.AgentsSessionEvent),
		zip.WithSummary("Append one event to a live session"))

	zip.Post[plane.SessionCloseIn, plane.CodingAck](cloud.Plane(), "/agents/session/close", planeSessionClose,
		zip.WithOperationID(plane.AgentsSessionClose),
		zip.WithSummary("Transition a live session to its terminal status"))

	zip.Post[plane.TargetRefIn, plane.TargetRef](cloud.Plane(), "/agents/resolve-target", planeResolveTarget,
		zip.WithOperationID(plane.AgentsResolveTarget),
		zip.WithSummary("Resolve an `on <machine>` reference to one of the org's targets"))

	zip.Post[plane.TargetGateIn, plane.CodingAck](cloud.Plane(), "/agents/target-gate", planeTargetGate,
		zip.WithOperationID(plane.AgentsTargetGate),
		zip.WithSummary("Whether a run may be dispatched to a machine right now"))

	zip.Post[plane.RouteRunIn, plane.CodingAck](cloud.Plane(), "/agents/route-run", planeRouteRun,
		zip.WithOperationID(plane.AgentsRouteRun),
		zip.WithSummary("Enqueue a routed coding run on the durable engine"))
}

// planeSessionOpen opens the session. Target is a VALUE of the request, not a
// second op: "no machine" is what an ordinary sandbox run says, and splitting it
// in two would be two doors onto one OpenSessionOn.
func planeSessionOpen(ctx context.Context, in *plane.SessionOpenIn) (*plane.SessionOpened, error) {
	id, err := agents.OpenSessionOn(ctx, in.Org, in.Actor, in.Agent, in.Title, in.Target)
	if err != nil {
		return nil, err
	}
	return &plane.SessionOpened{SessionID: id}, nil
}

func planeSessionEvent(ctx context.Context, in *plane.SessionEventIn) (*plane.CodingAck, error) {
	if err := agents.LogSessionEvent(ctx, in.Org, in.SessionID, in.Kind, in.Actor, in.Payload); err != nil {
		return nil, err
	}
	return &plane.CodingAck{OK: true}, nil
}

func planeSessionClose(ctx context.Context, in *plane.SessionCloseIn) (*plane.CodingAck, error) {
	if err := agents.CloseSession(ctx, in.Org, in.SessionID, in.Status); err != nil {
		return nil, err
	}
	return &plane.CodingAck{OK: true}, nil
}

// planeResolveTarget answers with the org's machine, or an error. It returns
// only the id and the label — never the row — so a caller learns nothing about a
// machine it did not already name, and a reference that matches nothing in THIS
// org is not found rather than somebody else's target.
func planeResolveTarget(ctx context.Context, in *plane.TargetRefIn) (*plane.TargetRef, error) {
	t, err := agents.ResolveTarget(ctx, in.Org, in.Ref)
	if err != nil {
		return nil, err
	}
	return &plane.TargetRef{ID: t.ID, Label: t.Label}, nil
}

// planeTargetGate is fail-closed by construction: the only success is a nil
// error from the gate itself, so a machine that is absent, offline or has no
// live runner stops the dispatch here rather than downstream.
func planeTargetGate(ctx context.Context, in *plane.TargetGateIn) (*plane.CodingAck, error) {
	if err := agents.TargetDispatchable(ctx, in.Org, in.TargetID); err != nil {
		return nil, err
	}
	return &plane.CodingAck{OK: true}, nil
}

// planeRouteRun puts the run on the durable engine IN THIS PROCESS.
//
// That placement is the whole point of the op. The delivery activity offers the
// run to an in-memory mailbox which the machine long-polls through this app's
// HTTP surface; enqueued anywhere else it would be offered to a mailbox nobody
// reads, and the workflow would spend its entire budget before failing. One
// process holds the engine, the mailbox and the completion — this one.
func planeRouteRun(ctx context.Context, in *plane.RouteRunIn) (*plane.CodingAck, error) {
	if err := coding.Enqueue(ctx, *in, routeLog); err != nil {
		return nil, fmt.Errorf("agents: could not queue the routed run: %w", err)
	}
	return &plane.CodingAck{OK: true}, nil
}

// codingLog is how coding talks in this process. Deps used to carry a logger and
// no longer does — a logger is not a fact about a deployment — so the subsystem
// names itself here, once, rather than at each call.
var codingLog = luxlog.New("agents").New("subsystem", "coding")

// routeLog carries coding's best-effort failures (a dropped session mirror, a PR
// that would not file) into this process's log instead of dropping them.
func routeLog(msg string, kv ...any) { codingLog.Warn(msg, kv...) }
