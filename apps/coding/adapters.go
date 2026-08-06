package coding

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/cloud/plane"
)

// adapters.go binds coding's seams to the apps that OWN them — across the
// process boundary, because that is where they are.
//
// It used to bind them to agents and tracker in-process, which was right while
// one binary held every subsystem. It is not right now: each app is its own
// process, and every one of those calls reads the callee's `mounted` package
// global. A package global is per-PROCESS, so in the process that runs a coding
// run they are all nil and each seam answered its zero value — "tracker: not
// mounted", an empty clone URL the dispatcher reads as "git is not available",
// and a VerifyRef that reports every pushed branch absent and fails the run
// closed with no PR. The chat turn died of exactly this shape one file over.
//
// The orchestration in coding.go is untouched. It always reached its
// collaborators through injected seams, which is what makes this a re-binding
// and not a rewrite: same Dispatcher, same order, same fail-closed rules, the
// calls simply land on a socket instead of a nil global.
//
// The Runner is the exception that proves it: the bot-gateway sandbox was
// always an HTTP client (task.go), so it never had a boundary to cross.

// seamTimeout bounds ONE seam call. Every seam here is a small read or write —
// open a row, append an event, resolve a name — so a call that has not answered
// in this long is a wedged peer, not a slow one, and the run gets an honest
// error instead of hanging inside a step.
//
// It sits UNDER the plane transport's own response-read ceiling, which in a
// plugin process is zaphttp's 30s default (zap-proto/http client.go: readTimeout
// 30s; only the cmd/cloud host re-registers the zap scheme with a longer one,
// and a plugin does not link the host). Under it, the deadline that fires is
// always this one — the one whose error names the seam — instead of a bare 502
// from the wire. It is also why the RUN itself is not a plane call: a 25-minute
// coding run cannot be a request, so it stays a bounded goroutine on the trigger
// side and only its seams cross.
const seamTimeout = 20 * time.Second

// NewDispatcher assembles the production Dispatcher: every seam a peer call over
// the internal plane, the runner on coding's own bot-gateway wire. log is the
// structured logger for best-effort mirror failures (nil is fine).
//
// It also wires the routed completion seam to THIS dispatcher, so the durable
// delivery activity verifies the pushed ref, files the PR and closes the session
// exactly as the local path does.
func NewDispatcher(log func(msg string, kv ...any)) Dispatcher {
	d := Dispatcher{
		Sessions:   planeSessions{},
		Tracker:    planeTracker{},
		Runner:     runner{},
		CloneURL:   planeCloneURL,
		VerifyRef:  planeVerifyRef,
		Log:        log,
		Route:      planeRoute,
		TargetGate: planeTargetGate,
	}
	setRoutedFinalizer(d.finalizeRoutedDurable)
	return d
}

// bounded gives one seam call its own deadline without letting it outlive the
// run's. A run already cancelled fails here rather than on the wire.
func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, seamTimeout)
}

// planeSessions is the live agent-session registry, in the agents process.
type planeSessions struct{}

func (planeSessions) Open(ctx context.Context, org, actor, agent, title string) (string, error) {
	return planeSessions{}.OpenOn(ctx, org, actor, agent, title, "")
}

// OpenOn opens the session, tagged with the machine a routed run was sent to so
// mission-control shows it where it is executing. An empty target is the
// ordinary sandbox session — ONE op, because "no machine" is a value of the
// target and not a different question.
func (planeSessions) OpenOn(ctx context.Context, org, actor, agent, title, target string) (string, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := plane.Ask[plane.SessionOpenIn, plane.SessionOpened](ctx, agentsApp, plane.AgentsSessionOpen,
		&plane.SessionOpenIn{Org: org, Actor: actor, Agent: agent, Title: title, Target: target})
	if err != nil {
		return "", err
	}
	if out == nil || out.SessionID == "" {
		return "", fmt.Errorf("coding: agents opened no session")
	}
	return out.SessionID, nil
}

func (planeSessions) Log(ctx context.Context, org, sessionID, kind, actor string, payload []byte) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	_, err := plane.Ask[plane.SessionEventIn, plane.CodingAck](ctx, agentsApp, plane.AgentsSessionEvent,
		&plane.SessionEventIn{Org: org, SessionID: sessionID, Kind: kind, Actor: actor, Payload: payload})
	return err
}

func (planeSessions) Close(ctx context.Context, org, sessionID, status string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	_, err := plane.Ask[plane.SessionCloseIn, plane.CodingAck](ctx, agentsApp, plane.AgentsSessionClose,
		&plane.SessionCloseIn{Org: org, SessionID: sessionID, Status: status})
	return err
}

// planeTracker files the native PR work item, in the tracker process.
type planeTracker struct{}

func (planeTracker) CreatePR(ctx context.Context, in PRInput) (PRRef, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := plane.Ask[plane.AgentPRIn, plane.AgentPROut](ctx, trackerApp, plane.TrackerAgentPR,
		&plane.AgentPRIn{
			Org: in.Org, Project: in.Project, Repo: in.Repo, Base: in.Base,
			Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee,
		})
	if err != nil {
		return PRRef{}, err
	}
	if out == nil {
		return PRRef{}, fmt.Errorf("coding: tracker filed no PR")
	}
	return PRRef{Identifier: out.Identifier, ProjectKey: out.ProjectKey, Number: out.Number}, nil
}

// planeCloneURL asks git for the org's clone URL. An error is an EMPTY url,
// which the dispatcher already reads as "git is not available" and refuses the
// run on — the same fail-closed answer the in-process seam gave when git was
// absent, so no caller learns a new failure mode.
func planeCloneURL(ctx context.Context, org, repo string) string {
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := plane.Ask[plane.RepoRefIn, plane.RepoCloneURL](ctx, gitApp, plane.GitCloneURL,
		&plane.RepoRefIn{Org: org, Repo: repo})
	if err != nil || out == nil {
		return ""
	}
	return out.URL
}

// planeVerifyRef is the integrity gate: git reads the tip off its own storage.
// An unreachable git is an UNVERIFIABLE ref, which is treated as absent — the
// run fails closed and files no PR, rather than trusting the sandbox's claim.
func planeVerifyRef(ctx context.Context, org, repo, branch string) (string, bool) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	out, err := plane.Ask[plane.RefIn, plane.RefTip](ctx, gitApp, plane.GitVerifyRef,
		&plane.RefIn{Org: org, Repo: repo, Branch: branch})
	if err != nil || out == nil || !out.Found {
		return "", false
	}
	return out.SHA, true
}

// planeTargetGate is the fail-closed existence+liveness check for a routed run's
// machine: it exists in THIS org, is online, and has a live runner.
func planeTargetGate(ctx context.Context, org, targetID string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	_, err := plane.Ask[plane.TargetGateIn, plane.CodingAck](ctx, agentsApp, plane.AgentsTargetGate,
		&plane.TargetGateIn{Org: org, TargetID: targetID})
	return err
}

// planeRoute hands the routed run to agents to enqueue.
//
// It does NOT enqueue the workflow here. The durable delivery activity offers
// the run to an in-memory mailbox that the machine long-polls through agents'
// HTTP surface, so an enqueue in any other process would hand the run to a
// mailbox nobody reads and burn the whole budget before failing. The engine and
// the mailbox have to be one process; agents is that process.
func planeRoute(ctx context.Context, run RoutedRun) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	_, err := plane.Ask[plane.RouteRunIn, plane.CodingAck](ctx, agentsApp, plane.AgentsRouteRun,
		&plane.RouteRunIn{
			Org: run.Org, TargetID: run.TargetID, SessionID: run.SessionID,
			Repo: run.Repo, Project: run.Project, Base: run.Base, Branch: run.Branch,
			Prompt: run.Prompt, CloneURL: run.CloneURL, TimeoutSeconds: run.TimeoutSeconds,
			Actor: run.Actor, AgentRef: run.AgentRef,
		})
	return err
}

// Enqueue is the SERVER half of planeRoute, run by the agents process: put the
// routed run on the durable engine THERE, where the mailbox the machine polls
// lives and where the delivery activity can therefore hand the run over.
//
// It builds the process's Dispatcher on first use, which is what binds the
// routed COMPLETION seam (setRoutedFinalizer): when the machine reports, the
// delivery activity has to verify the pushed ref, file the PR and close the
// session, and it reaches those seams through that dispatcher. Enqueueing
// without it would queue runs whose sessions never close.
func Enqueue(ctx context.Context, in plane.RouteRunIn, log func(msg string, kv ...any)) error {
	enqueueOnce.Do(func() { NewDispatcher(log) })
	return enqueueRoutedRun(ctx, RoutedRun{
		Org: in.Org, TargetID: in.TargetID, SessionID: in.SessionID,
		Repo: in.Repo, Project: in.Project, Base: in.Base, Branch: in.Branch,
		Prompt: in.Prompt, CloneURL: in.CloneURL, TimeoutSeconds: in.TimeoutSeconds,
		Actor: in.Actor, AgentRef: in.AgentRef,
	})
}

var enqueueOnce sync.Once

// The peers, spelled once.
const (
	agentsApp  = "agents"
	gitApp     = "git"
	trackerApp = "tracker"
)
