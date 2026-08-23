package agents

import "context"

// ListForOrg returns the org's agents from the in-process store — the ONE
// exported client other in-process subsystems use to read the canonical agent
// registry WITHOUT an HTTP hop back through the gateway.
//
// It is the decompleced replacement for the old bots-as-members path, which
// enumerated agents over HTTP (/v1/agents with a forwarded bearer) and broke
// when HANZO_API_KEY was rejected (bot_members=0). clients/team calls this
// directly to project each agent as a workspace Employee.
//
// ISOLATION: org is the ONLY tenant key and is used VERBATIM (Store.List filters
// WHERE org=?), so a caller for org A can never enumerate org B's agents. The
// caller MUST pass an org it already validated (principal.Org / a verified
// token claim), never a raw client header. Fails closed (nil, error) when the
// agents subsystem is not mounted or the org is empty/oversized.
func ListForOrg(ctx context.Context, org string) ([]Agent, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return nil, err
	}
	return sto.List(ctx, org)
}
