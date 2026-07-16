package bots

import (
	"context"

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/bot"
)

// adapters.go binds the bots seams to the real in-process packages. This is the
// ONLY file in clients/bots that imports agents/bot, so the handlers stay pure
// and the whole control plane is unit-tested against fakes. Neither agents nor
// bot imports clients/bots, so these imports are cycle-free.

// agentLabel is the agent every bot run is recorded under. It is what makes a
// bot run findable as a bot run: the registry holds every kind of agent session
// (coding runs, dev sessions), and this label is the ONE thing that says "this
// row is a bot run" — so the bots face reads and stops only its own rows, never
// a coding session that happens to share the org.
const agentLabel = "bot"

// sessionRuns is the run registry on the agents session plane (inproc.go): a bot
// run IS a root session, so its id is the run id and there is no second store,
// no second status vocabulary, and no second stop path to keep in sync.
type sessionRuns struct{}

func (sessionRuns) Open(ctx context.Context, org, actor, task, surface string) (string, error) {
	return agents.OpenSession(ctx, agents.SessionOpen{
		Org: org, Actor: actor, Agent: agentLabel, Title: task, Surface: surface,
	})
}

// listCap bounds the list. The published contract has no pagination, so SOME cap
// is unavoidable; this states it rather than inheriting the store's default. It
// is the store's maximum, so an org sees every live run it could plausibly have —
// past it the list truncates newest-first, which is the direction that keeps the
// runs a caller is most likely to act on.
const listCap = 500

// List returns the org's RUNNING bot runs, newest first. Terminal runs are not
// listed: the contract is the live runs a caller can attach to or stop, and the
// finished ones stay readable on the session plane that owns their history.
func (sessionRuns) List(ctx context.Context, org string) ([]Run, error) {
	rows, err := agents.ListSessions(ctx, org, agents.SessionFilter{
		Agent:  agentLabel,
		Status: agents.StatusRunning,
		Limit:  listCap,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, x := range rows {
		out = append(out, toRun(x))
	}
	return out, nil
}

// Get resolves one of the org's bot runs. A session of this org that is NOT a bot
// run (a coding run, a dev session) is reported not-found, so the bots face can
// never read or stop a neighbouring product's session through a run id.
func (sessionRuns) Get(ctx context.Context, org, runID string) (Run, bool, error) {
	x, found, err := agents.GetSession(ctx, org, runID)
	if err != nil || !found {
		return Run{}, false, err
	}
	if x.Agent != agentLabel {
		return Run{}, false, nil
	}
	return toRun(x), true, nil
}

// Stop records the stop and moves the run terminal, through the SAME forced-stop
// path a login-revoke takes. It re-resolves under org, so it can no more cross a
// tenant than Get can.
func (sessionRuns) Stop(ctx context.Context, org, runID, reason string) (bool, error) {
	return agents.StopSession(ctx, org, runID, reason)
}

func toRun(x agents.Session) Run {
	return Run{
		ID:        x.ID,
		Task:      x.Title,
		Surface:   x.Surface,
		Status:    x.Status,
		StartedAt: x.StartedAt,
	}
}

// gatewayRuntime drives the TS bot runtime over its lifecycle API (bot/runtime.go).
type gatewayRuntime struct{}

func (gatewayRuntime) Stop(ctx context.Context, org, runID string) error {
	return bot.StopRun(ctx, org, runID)
}
