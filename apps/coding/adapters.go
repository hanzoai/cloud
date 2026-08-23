package coding

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hanzoai/cloud/plane"
)

// adapters.go binds coding's clients to the apps that OWN them — across the
// process boundary, because that is where they are.
//
// It used to bind them to agents and todo in-process, which was right while
// one binary held every subsystem. It is not right now: each app is its own
// process, and every one of those calls reads the callee's `mounted` package
// global. A package global is per-PROCESS, so in the process that runs a coding
// run they are all nil and each client answered its zero value — "todo: not
// mounted", an empty clone URL the dispatcher reads as "git is not available",
// and a VerifyRef that reports every pushed branch absent and fails the run
// closed with no PR. The chat turn died of exactly this shape one file over.
//
// The orchestration in coding.go is untouched. It always reached its
// collaborators through injected clients, which is what makes this a re-binding
// and not a rewrite: same Dispatcher, same order, same fail-closed rules, the
// calls simply land on a socket instead of a nil global.
//
// The Runner is the exception that proves it: the bot-gateway sandbox was
// always an HTTP client (task.go), so it never had a boundary to cross.

// clientTimeout bounds ONE client call. Every client here is a small read or write —
// open a row, append an event, resolve a name — so a call that has not answered
// in this long is a wedged peer, not a slow one, and the run gets an honest
// error instead of hanging inside a step.
//
// It sits UNDER the plane transport's own response-read ceiling, which in a
// plugin process is zaphttp's 30s default (zap-proto/http client.go: readTimeout
// 30s; only the cmd/cloud host re-registers the zap scheme with a longer one,
// and a plugin does not link the host). Under it, the deadline that fires is
// always this one — the one whose error names the client — instead of a bare 502
// from the wire. It is also why the RUN itself is not a plane call: a 25-minute
// coding run cannot be a request, so it stays a bounded goroutine on the trigger
// side and only its clients cross.
const clientTimeout = 20 * time.Second

// NewDispatcher assembles the production Dispatcher: every client a peer call over
// the internal plane, the runner on coding's own bot-gateway wire. log is the
// structured logger for best-effort mirror failures (nil is fine).
//
// It also wires the routed completion client to THIS dispatcher, so the durable
// delivery activity verifies the pushed ref, files the PR and closes the session
// exactly as the local path does.
func NewDispatcher(log func(msg string, kv ...any)) Dispatcher {
	d := Dispatcher{
		Sessions: planeSessions{},
		PR:       planePR{},
		// The SANDBOX runner. The docker-CLI path it replaces could not run at all:
		// bot-gateway has neither that binary nor a socket, so every dispatch 503d
		// while the chain read as configured. Same gVisor/Kata boundary, asked of the
		// apiserver instead of a CLI that is not installed — and over the plane, so
		// there is no HTTP hop to port.
		Runner:     sandboxRunner{},
		CloneURL:   forgeCloneURL,
		VerifyRef:  forgeVerifyRef,
		Log:        log,
		Route:      planeRoute,
		TargetGate: planeTargetGate,
	}
	setRoutedFinalizer(d.finalizeRoutedDurable)
	return d
}

// bounded gives one client call its own deadline without letting it outlive the
// run's. A run already cancelled fails here rather than on the wire.
func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, clientTimeout)
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

// planePR opens the pull request: the work item on our board (todo), and the
// proposal where the code lives (git — a GitHub pull request for a repository
// that mirrors there, the branch's page here otherwise).
//
// Two peers, ONE client. The board row is ours and is filed for every run, whatever
// host the code is on; the address is a property of the host and only git can
// answer it. Splitting them at the caller would put the choice of backend in the
// orchestration, which is exactly where it must not be.
type planePR struct{}

func (planePR) Open(ctx context.Context, in PRInput) (PRRef, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	// The org travels in the ENVELOPE (cloud.For), not in the body. Both spell the
	// same word here, but only the envelope is checked: the plane's identity slot
	// is read back through the same OrgOf rule as the HTTP boundary, so a call
	// crossing the plane cannot be granted an org key the boundary would refuse.
	// A body field would arrive unchecked — which is what made this a
	// cross-tenant write. Same shape as cloud.UpsertIssue's Ask.
	ctx = plane.For(ctx, in.Org)
	out, err := plane.Ask[plane.AgentPRIn, plane.AgentPROut](ctx, todoApp, plane.TodoAgentPR,
		&plane.AgentPRIn{
			Project: in.Project, Repo: in.Repo, Base: in.Base,
			Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee,
		})
	if err != nil {
		return PRRef{}, err
	}
	if out == nil {
		return PRRef{}, fmt.Errorf("coding: todo filed no PR")
	}
	ref := PRRef{Identifier: out.Identifier, ProjectKey: out.ProjectKey, Number: out.Number}

	// The address, opened on the forge AS THE PERSON the run is for. Its failure is
	// returned BESIDE the row rather than instead of it: the work is pushed and
	// tracked either way, and a run that could not open a pull request must say so
	// out loud instead of quietly answering with a link that is not where the
	// review will happen.
	//
	// It is a real pull request for every repository now. The client this replaces
	// answered two different things depending on whether a mirror row pointed at
	// GitHub — a pull request there, and otherwise a link to a branch-browsing
	// page, which nobody can approve. The forge has native pull requests, so the
	// question has one answer.
	url, perr := propose(ctx, in.Org, in.Actor, in.Repo, in.Base, in.Head, in.Title, in.Body)
	if perr != nil {
		return ref, perr
	}
	ref.URL = url
	return ref, nil
}

// forgeCloneURL is the HTTPS address of a repository, for a ROUTED run — one
// executing on a machine the customer owns, which authenticates git with its own
// already-held credentials. The sandbox path does not come through here: it
// clones the SSH remote its grant names, which is the only address its key
// opens.
//
// It is resolved AS THE ACTOR, because the machine that receives this address
// holds credentials broader than the caller's: an address produced by a site
// administrator and handed to that machine is a repository the caller could not
// have opened themselves.
//
// An error is an EMPTY url, which the dispatcher already reads as "git is not
// available" and refuses the run on, so no caller learns a new failure mode.
func forgeCloneURL(ctx context.Context, org, actor, repo string) string {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return remote(ctx, org, actor, repo)
}

// forgeVerifyRef is the integrity gate: the FORGE is asked what the branch
// points at. An unreachable forge is an UNVERIFIABLE ref, which is treated as
// absent — the run fails closed and files no PR, rather than trusting the
// sandbox's claim to have pushed one.
func forgeVerifyRef(ctx context.Context, org, repo, branch string) (string, bool) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	return landed(ctx, org, repo, branch)
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
// routed COMPLETION client (setRoutedFinalizer): when the machine reports, the
// delivery activity has to verify the pushed ref, file the PR and close the
// session, and it reaches those clients through that dispatcher. Enqueueing
// without it would queue runs whose sessions never close.
func Enqueue(ctx context.Context, in plane.RouteRunIn, log func(msg string, kv ...any)) error {
	enqueueOnce.Do(func() { NewDispatcher(log) })
	return enqueueRoutedRun(ctx, RoutedRun{
		Org: in.Org, TargetID: in.TargetID, SessionID: in.SessionID,
		Repo: in.Repo, Project: in.Project, Base: in.Base, Branch: in.Branch,
		Prompt: in.Prompt, CloneURL: in.CloneURL, TimeoutSeconds: in.TimeoutSeconds,
		Actor: in.Actor, AgentRef: in.AgentRef, ForgeActor: in.ForgeActor,
	})
}

var enqueueOnce sync.Once

// The peers, spelled once.
const (
	agentsApp = "agents"
	todoApp   = "todo"
)
