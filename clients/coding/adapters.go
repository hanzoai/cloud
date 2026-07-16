package coding

import (
	"context"

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/bot"
	"github.com/hanzoai/cloud/clients/tracker"
)

// adapters.go binds the coding seams to the real in-process packages. This is the
// ONLY file in clients/coding that imports agents/tracker/bot; coding.go stays
// pure so the orchestration is unit-tested against fakes. None of agents/tracker/
// bot imports clients/git or clients/integrations, so these imports are cycle-free.

// NewDispatcher assembles the production Dispatcher: sessions on the live agent
// registry, PRs on the tracker, the runner on the bot-gateway client, plus the
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
		Runner:    botAdapter{},
		CloneURL:  cloneURL,
		VerifyRef: verifyRef,
		Log:       log,
	}
}

// sessionAdapter forwards to the agents in-process session API (inproc.go).
type sessionAdapter struct{}

func (sessionAdapter) Open(ctx context.Context, org, actor, agent, title string) (string, error) {
	return agents.OpenSession(ctx, agents.SessionOpen{Org: org, Actor: actor, Agent: agent, Title: title})
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

// botAdapter forwards to the bot-gateway coding-task client (bot/coding.go),
// bridging the coding Step/RunResult shapes to the bot ones.
type botAdapter struct{}

func (botAdapter) Run(ctx context.Context, org, userID string, req RunRequest, onStep func(Step)) (RunResult, error) {
	res, err := bot.RunCodingTask(ctx, org, userID, bot.CodingTaskRequest{
		CloneURL: req.CloneURL, BaseBranch: req.BaseBranch, Branch: req.Branch,
		Prompt: req.Prompt, SessionID: req.SessionID, RunTimeoutSeconds: req.RunTimeoutSeconds,
		Credential: bot.Credential{Username: req.CredUser, Token: req.CredToken},
	}, func(s bot.CodingStep) {
		if onStep != nil {
			onStep(Step{Type: s.Type, Step: s.Step, Message: s.Message, Status: s.Status})
		}
	})
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{
		Branch: res.Branch, CommitSha: res.CommitSha, Diffstat: res.Diffstat,
		Changed: res.Changed, OK: res.OK, LogTail: res.LogTail, Error: res.Error,
	}, nil
}
