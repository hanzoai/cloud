package link

import (
	"context"

	"github.com/hanzoai/cloud/clients/agents"
)

// adapters.go binds the Sessions seam to the agents in-process control plane
// (clients/agents). It is the ONLY file in clients/link that imports agents;
// http.go/store.go/route.go stay free of it so the orchestration is unit-tested
// against a fake seam. agents does NOT import link, so this direction is
// cycle-free.

type sessionAdapter struct{}

func (sessionAdapter) Stop(ctx context.Context, org string, m SessionMatch) (int, error) {
	return agents.StopSessions(ctx, org, agents.SessionMatch{
		Host: m.Host, Provider: m.Provider, Account: m.Account,
	})
}

func (sessionAdapter) CountActive(ctx context.Context, org string, m SessionMatch) (int, error) {
	return agents.CountActiveSessions(ctx, org, agents.SessionMatch{
		Host: m.Host, Provider: m.Provider, Account: m.Account,
	})
}
