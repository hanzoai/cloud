package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/coding"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
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
// The ORG is the caller's plane identity (cloud.Who), as it is for a turn
// (planeTurnStart). Each argument still names one, and it may repeat the caller's
// and never contradict it: a sibling that could name the org could write into
// another tenant's session, tag it with a sandbox, close it, or read and dispatch
// to its machines. Every caller states the org it acts for on the context it
// calls with — the run's (cloud.For), or the request it is serving — so every op
// below touches only that org's own sessions, machines and queue.
//
// Declared at the app's composition root because the ROUTE op needs both halves
// — this app's mailbox and coding's workflow — and coding imports agents, so
// agents can never import coding. A main is a leaf; that is what makes it the
// one place both can be held. Registering on cloud.Plane() before cloud.Listen
// is safe and ordinary: an op registry is a value, ServePlane binds the socket
// over it afterwards.
//
// The mount declares them (mountAgents), not an init: a registry appends, so a
// program that mounts more than once — a test does — has to be able to start the
// plane over (cloud.ResetPlane) and get every op back from the mount that owns it.
func exposeCoding() {
	cloud.Plane().Post("/agents/session/open", planeSessionOpen,
		zip.WithOperationID(client.AgentsSessionOpen),
		zip.WithSummary("Open the live session a coding run streams into"))

	cloud.Plane().Post("/agents/session/event", planeSessionEvent,
		zip.WithOperationID(client.AgentsSessionEvent),
		zip.WithSummary("Append one event to a live session"))

	cloud.Plane().Post("/agents/session/close", planeSessionClose,
		zip.WithOperationID(client.AgentsSessionClose),
		zip.WithSummary("Transition a live session to its terminal status"))

	cloud.Plane().Post("/agents/session/tag", planeSessionTag,
		zip.WithOperationID(client.AgentsSessionTag),
		zip.WithSummary("Record what run a live session is the view of"))

	cloud.Plane().Post("/agents/resolve-target", planeResolveTarget,
		zip.WithOperationID(client.AgentsResolveTarget),
		zip.WithSummary("Resolve an `on <machine>` reference to one of the org's targets"))

	cloud.Plane().Post("/agents/target-gate", planeTargetGate,
		zip.WithOperationID(client.AgentsTargetGate),
		zip.WithSummary("Whether a run may be dispatched to a machine right now"))

	cloud.Plane().Post("/agents/route-run", planeRouteRun,
		zip.WithOperationID(client.AgentsRouteRun),
		zip.WithSummary("Enqueue a routed coding run on the durable engine"))
}

// planeSessionOpen opens the session. Target is a VALUE of the request, not a
// second op: "no machine" is what an ordinary sandbox run says, and splitting it
// in two would be two ops onto one OpenSessionOn.
func planeSessionOpen(ctx context.Context, in *client.SessionOpenIn) (*client.SessionOpened, error) {
	org, err := callerOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	// A sibling states the LINKED SUBJECT, not the session's composite actor.
	// Every reader forms the actor as billingActor(org, sub) — org/sub — so a
	// session recorded under the bare subject reads as another person's, and the
	// actor who opened it is answered "session not found" by its own events.
	// Normalize at this boundary, the way the in-process paths already do; an
	// empty actor is left to OpenSessionOn, which makes it the org's own work.
	id, err := agents.OpenSessionOn(ctx, org, sessionActor(org, in.Actor), in.Agent, in.Title, in.Target, in.Conversation)
	if err != nil {
		return nil, err
	}
	return &client.SessionOpened{SessionID: id}, nil
}

func planeSessionEvent(ctx context.Context, in *client.SessionEventIn) (*client.CodingAck, error) {
	org, err := callerOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	if err := agents.LogSessionEvent(ctx, org, in.SessionID, in.Kind, in.Actor, in.Payload); err != nil {
		return nil, err
	}
	return &client.CodingAck{OK: true}, nil
}

func planeSessionClose(ctx context.Context, in *client.SessionCloseIn) (*client.CodingAck, error) {
	org, err := callerOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	if err := agents.CloseSession(ctx, org, in.SessionID, in.Status, in.Error); err != nil {
		return nil, err
	}
	return &client.CodingAck{OK: true}, nil
}

// planeSessionTag records run facts on a session: an empty field is left as it
// was, because a run learns its branch after the session exists, its sandbox
// when it leases one, and its pull request only at the end.
func planeSessionTag(ctx context.Context, in *client.SessionTagIn) (*client.CodingAck, error) {
	org, err := callerOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	if err := agents.TagSession(ctx, org, in.SessionID, agents.Session{
		Kind: in.Kind, Repo: in.Repo, Base: in.Base, Branch: in.Branch,
		Environment: in.Environment, PR: in.PR, Mode: in.Mode, Box: in.Sandbox, Project: in.Project,
	}); err != nil {
		return nil, err
	}
	return &client.CodingAck{OK: true}, nil
}

// planeResolveTarget answers with the org's machine, or an error. It returns
// only the id and the label — never the row — so a caller learns nothing about a
// machine it did not already name, and a reference that matches nothing in THIS
// org is not found rather than somebody else's target.
func planeResolveTarget(ctx context.Context, in *client.TargetRefIn) (*client.TargetRef, error) {
	org, err := callerOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	t, err := agents.ResolveTarget(ctx, org, in.Ref)
	if err != nil {
		return nil, err
	}
	return &client.TargetRef{ID: t.ID, Label: t.Label}, nil
}

// planeTargetGate is fail-closed by construction: the only success is a nil
// error from the gate itself, so a machine that is absent, offline or has no
// live runner stops the dispatch here rather than downstream.
func planeTargetGate(ctx context.Context, in *client.TargetGateIn) (*client.CodingAck, error) {
	org, err := callerOrg(ctx, in.Org)
	if err != nil {
		return nil, err
	}
	if err := agents.TargetDispatchable(ctx, org, in.TargetID, in.Dir); err != nil {
		return nil, err
	}
	// A read-only run is admitted only to a machine whose harness advertised the
	// capability. This is a capability check, not a reason to run here.
	if in.ReadOnly {
		if err := agents.TargetReadOnly(ctx, org, in.TargetID); err != nil {
			return nil, err
		}
	}
	return &client.CodingAck{OK: true}, nil
}

// planeRouteRun puts the run on the durable engine IN THIS PROCESS.
//
// That placement is the whole point of the op. The delivery activity offers the
// run to an in-memory mailbox which the machine long-polls through this app's
// HTTP surface; enqueued anywhere else it would be offered to a mailbox nobody
// reads, and the workflow would spend its entire budget before failing. One
// process holds the engine, the mailbox and the completion — this one.
func planeRouteRun(ctx context.Context, in *client.RouteRunIn) (*client.CodingAck, error) {
	run, err := routedAs(ctx, in)
	if err != nil {
		return nil, err
	}
	org := run.Org
	// The queue asks for itself whether the caller reaches the machine, rather
	// than trusting that the dispatcher's gate was asked: a run is put on a held
	// org's machine only for the machine's owner (or a SuperAdmin), whichever
	// caller reached the queue.
	if err := agents.Reaches(ctx, org, in.TargetID); err != nil {
		return nil, err
	}
	// The folder is asked again here too, and this is where a run in one is
	// recorded as started: the queue is the one step every routed run passes.
	if err := agents.Admit(ctx, org, in.TargetID, in.Dir, in.SessionID); err != nil {
		return nil, err
	}
	if err := coding.Enqueue(ctx, run, routeLog); err != nil {
		return nil, fmt.Errorf("agents: could not queue the routed run: %w", err)
	}
	return &client.CodingAck{OK: true}, nil
}

// routedAs is the run as its VALIDATED caller owns it: the org and the person the
// call states, and the forge login the forge agrees is theirs. Whatever the body says
// about who the run is for is checked against those, never taken.
func routedAs(ctx context.Context, in *client.RouteRunIn) (client.RouteRunIn, error) {
	org, err := callerOrg(ctx, in.Org)
	if err != nil {
		return client.RouteRunIn{}, err
	}
	actor, err := callerActor(ctx, in.Actor)
	if err != nil {
		return client.RouteRunIn{}, err
	}
	run := *in
	if run.ForgeActor, err = coding.RoutedForgeActor(ctx, run); err != nil {
		return client.RouteRunIn{}, err
	}
	run.Org, run.Actor = org, actor
	return run, nil
}

// sessionActor is the actor a session is keyed on when a sibling opens it over
// the plane. Sessions are attributed to billingActor(org, subject) everywhere —
// register, talk, runs, and every reader (agents.readerOf) — so the one boundary
// that takes a bare subject must state the same composite value. Even a subject
// that already contains a slash is prefixed: an API key's subject is its holder
// row (hanzo/z) and owns sessions as hanzo/hanzo/z, exactly as agents.readerOf
// keys it. A caller that already resolved the composite actor no longer sends
// one (apps/coding/start.go passes the bare subject); an empty actor stays empty,
// for OpenSessionOn to make the org's own.
func sessionActor(org, actor string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return ""
	}
	return agents.BillingActor(org, actor)
}

// callerOrg is the org a plane op acts in: the caller's, which the argument may
// repeat and never contradict. No org on the call is refused rather than taken
// from the argument, because an argument is what the caller chose.
func callerOrg(ctx context.Context, named string) (string, error) {
	org := strings.TrimSpace(cloud.Who(ctx).Org)
	if org == "" {
		return "", zip.ErrForbidden("agents: this op needs the org on the call")
	}
	if named = strings.TrimSpace(named); named != "" && named != org {
		return "", zip.ErrForbidden("agents: the org named is not the caller's")
	}
	return org, nil
}

// callerActor is the person a routed run is attributed to: the caller's, which the
// argument may repeat and never contradict. The delivery activity states this
// person as its caller (it has no request behind it), so an actor taken from the
// body would let a caller have the run's grant and completion act as someone else,
// and, on a run on GitHub, open its pull request as them. A call that states
// nobody carries no actor, and one that names a person it does not state is refused.
func callerActor(ctx context.Context, named string) (string, error) {
	user := strings.TrimSpace(cloud.Who(ctx).User)
	if named = strings.TrimSpace(named); named != "" && named != user {
		return "", zip.ErrForbidden("agents: the actor named is not the caller")
	}
	return user, nil
}

// codingLog is how coding talks in this process. Deps used to carry a logger and
// no longer does — a logger is not a fact about a deployment — so the subsystem
// names itself here, once, rather than at each call.
var codingLog = luxlog.New("agents").New("subsystem", "coding")

// routeLog carries coding's best-effort failures (a dropped session mirror, a PR
// that would not file) into this process's log instead of dropping them.
func routeLog(msg string, kv ...any) { codingLog.Warn(msg, kv...) }
