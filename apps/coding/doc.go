// Package coding orchestrates ONE autonomous coding run: open a live agent
// session, dispatch the job to the bot-gateway sandbox runtime, mirror the
// sandbox's progress into the session, verify the pushed branch landed in native
// git, open a native "PR" work item, and return a Result the caller renders.
//
// It is a LIBRARY, not an app: no route, no plugin, no manifest row. Its one
// caller is plugin/agents, which registers the one endpoint. It touches its
// collaborators only through interface clients (Sessions, PR, Runner) plus two
// git functions (CloneURL, VerifyRef), so the whole orchestration is unit-testable
// with fakes and — critically — coding does NOT import apps/git: git imports
// apps/integrations, integrations calls coding, so coding->git would cycle.
// The composition root assembles the real Dispatcher (adapters.go) and injects
// it into the trigger surface.
//
// ISOLATION: org is the ONLY tenant key and is threaded to every client call
// (session, todo, git, and the bot-gateway X-Org-Id). A run for org A can only
// ever open A's session, read/verify A's repo, and file A's PR. The clone URL is
// built from (org, repo) so the sandbox is pointed only at this org's namespace,
// and the credential (write-only) is scoped by IAM to this org at the edge.
package coding
