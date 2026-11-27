package account

// An org's agent identity is what a sandboxed agent runs as. These pin the two
// properties that make it safe, because both are easy to lose in a refactor and
// neither shows up as a failure until much later.

import "testing"

// The name is the <org>-<app> convention, and it is the whole reason an agent
// cannot reach another tenant: the application is owned by the org, so there is
// no cross-tenant scope for it to have.
func TestAgentAppIsNamedForItsOrg(t *testing.T) {
	if got := agentAppName("acme"); got != "acme-agent" {
		t.Fatalf("agentAppName(acme) = %q, want acme-agent", got)
	}
	if agentAppName("acme") == agentAppName("globex") {
		t.Fatal("two orgs must not share one agent identity")
	}
}

// A missing org is not an identity to provision. It is the shape that would make
// an application named "-agent" owned by nobody.
func TestNoOrgNoAgent(t *testing.T) {
	called := false
	giveOrgAnAgent(t.Context(), nil, func(string, ...any) { called = true }, "")
	if called {
		t.Fatal("an empty org must not be reported as a failure to provision")
	}
}
