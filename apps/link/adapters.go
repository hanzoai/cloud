package link

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/plane"
	agentspeer "github.com/hanzoai/cloud/plane/agent"
)

// adapters.go binds the Sessions client to the agents in-process control plane
// (clients/agents). It is the ONLY file in clients/link that imports agents;
// http.go/store.go/route.go stay free of it so the orchestration is unit-tested
// against a fake client. agents does NOT import link, so this direction is
// cycle-free. It is also where the revoking user's Subject becomes the session
// Actor (agents.BillingActor) — the single place that binds a login-manager stop to
// the caller's own sessions, so http.go never needs to know the actor format.

type sessionAdapter struct{}

// toMatch turns a link match into an agents match, mapping the revoking user's
// Subject to the session Actor. An empty Subject yields an empty Actor, which the
// agents guard treats as "stop nothing" — so a match that lost its caller identity
// fails closed rather than sweeping the org.
func toMatch(org string, m SessionMatch) agents.SessionMatch {
	actor := ""
	if s := strings.TrimSpace(m.Subject); s != "" {
		actor = agents.BillingActor(org, s)
	}
	return agents.SessionMatch{Actor: actor, Host: m.Host, Provider: m.Provider, Account: m.Account}
}

// Stop tears down the sessions a revoke invalidates, wherever the session store
// happens to be.
//
// The direct call was the ONLY leg, and agents ships as its own binary — so in
// the fleet this always reached an unmounted package, which answered (0, nil),
// and the revoke reported 200 {"sessionsStopped":0} while the sessions kept
// running under the revoked credential. Co-resident takes the cost-0 leg; every
// other deployment asks the process that owns the store, and a failure to ask is
// an error rather than a zero.
func (sessionAdapter) Stop(ctx context.Context, org string, m SessionMatch) (int, error) {
	if agents.Ready() {
		return agents.StopSessions(ctx, org, toMatch(org, m))
	}
	out, err := agentspeer.AgentsSessionsStop(ctx, planeMatch(m))
	if err != nil {
		return 0, err
	}
	return out.Count, nil
}

func (sessionAdapter) CountActive(ctx context.Context, org string, m SessionMatch) (int, error) {
	if agents.Ready() {
		return agents.CountActiveSessions(ctx, org, toMatch(org, m))
	}
	out, err := agentspeer.AgentsSessionsCount(ctx, planeMatch(m))
	if err != nil {
		return 0, err
	}
	return out.Count, nil
}

// planeMatch is the plane shape of a link match. It carries the raw Subject and
// NO org: the peer qualifies the subject into an actor with the org the plane
// proved, which is what keeps a revoke bounded to its own user's sessions when
// the caller is another process.
func planeMatch(m SessionMatch) *plane.SessionMatchIn {
	return &plane.SessionMatchIn{
		Subject:  strings.TrimSpace(m.Subject),
		Host:     m.Host,
		Provider: m.Provider,
		Account:  m.Account,
	}
}
