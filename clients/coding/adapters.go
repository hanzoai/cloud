package coding

import (
	"context"

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/tracker"
)

// adapters.go binds the session + tracker seams to the real in-process packages.
// This is the ONLY file in clients/coding that imports agents/tracker; coding.go
// stays pure so the orchestration is unit-tested against fakes. Neither agents nor
// tracker imports clients/git or clients/integrations, so these imports are
// cycle-free. The third seam, Runner, is bound in task.go — coding's own wire
// contract with the bot runtime.

// NewDispatcher assembles the production Dispatcher: sessions on the live agent
// registry, PRs on the tracker, the runner on coding's own runtime stub, plus the
// two git seams (cloneURL, verifyRef) the composition root passes from clients/git
// (which coding cannot import directly). log is the structured logger for
// best-effort mirror failures.
func NewDispatcher(
	cloneURL func(org, repo string) string,
	verifyRef func(ctx context.Context, org, repo, branch string) (string, bool),
	log func(msg string, kv ...any),
) Dispatcher {
	return Dispatcher{
		Sessions:  sessionAdapter{},
		Tracker:   trackerAdapter{},
		Runner:    runner{},
		CloneURL:  cloneURL,
		VerifyRef: verifyRef,
		Log:       log,
		// #48 route-work: enqueue a routed run on the ONE embedded tasks engine,
		// gated by the agents liveness check. Both bind to the real in-process
		// packages; a routed run with no live engine/target fails closed.
		Route:      enqueueRoutedRun,
		TargetGate: agents.TargetDispatchable,
	}
}

// sessionAdapter forwards to the agents in-process session API (inproc.go).
type sessionAdapter struct{}

func (sessionAdapter) Open(ctx context.Context, org, actor, agent, title string) (string, error) {
	return agents.OpenSession(ctx, org, actor, agent, title)
}
func (sessionAdapter) OpenOn(ctx context.Context, org, actor, agent, title, target string) (string, error) {
	return agents.OpenSessionOn(ctx, org, actor, agent, title, target)
}
func (sessionAdapter) Log(ctx context.Context, org, sessionID, kind, actor string, payload []byte) error {
	return agents.LogSessionEvent(ctx, org, sessionID, kind, actor, payload)
}
func (sessionAdapter) Close(ctx context.Context, org, sessionID, status string) error {
	return agents.CloseSession(ctx, org, sessionID, status)
}

// trackerAdapter forwards to the tracker in-process agent-PR create (agentpr.go).
type trackerAdapter struct{}

func (trackerAdapter) CreatePR(ctx context.Context, in PRInput) (PRRef, error) {
	pr, err := tracker.CreateAgentPR(ctx, tracker.AgentPRInput{
		Org: in.Org, Project: in.Project, Repo: in.Repo, Base: in.Base,
		Head: in.Head, Title: in.Title, Body: in.Body, Assignee: in.Assignee,
	})
	if err != nil {
		return PRRef{}, err
	}
	return PRRef{Identifier: pr.Identifier, ProjectKey: pr.ProjectKey, Number: pr.Number}, nil
}
