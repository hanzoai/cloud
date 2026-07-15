package link

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/clients/agents"
)

// adapters.go binds the Sessions seam to the agents in-process control plane
// (clients/agents). It is the ONLY file in clients/link that imports agents;
// http.go/store.go/route.go stay free of it so the orchestration is unit-tested
// against a fake seam. agents does NOT import link, so this direction is
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

func (sessionAdapter) Stop(ctx context.Context, org string, m SessionMatch) (int, error) {
	return agents.StopSessions(ctx, org, toMatch(org, m))
}

func (sessionAdapter) CountActive(ctx context.Context, org string, m SessionMatch) (int, error) {
	return agents.CountActiveSessions(ctx, org, toMatch(org, m))
}
