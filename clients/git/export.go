package git

import "context"

// export.go is git's in-process seam for the coding-agent orchestrator
// (clients/coding). git already imports clients/integrations (notify.go), and
// integrations imports clients/agents — so coding, which integrations calls, can
// NOT import git without a cycle (integrations -> coding -> git -> integrations).
// The composition root wires these two read-only functions into coding's CloneURL
// / VerifyRef seams instead, so coding COMPOSES the git host + a post-push ref
// check without importing the package. Both read the mounted service and are
// fail-safe before Mount (empty / not-found), never panicking on a nil singleton.

// CloneURL returns the HTTPS smart-HTTP clone URL for an org's repo
// (https://<domain>/v1/git/<org>/<repo>.git) — the exact URL the git handlers
// serve. Empty when git is not mounted. The credential is NEVER embedded here;
// the sandbox presents it out of band (env-fed http.extraHeader), so this URL is
// safe to log and to hand to a subprocess on argv.
func CloneURL(org, name string) string {
	s := mounted.Load()
	if s == nil {
		return ""
	}
	return cloneURL(s, org, name)
}

// VerifyRef reports the tip commit of branch in an org's repo, reading the on-disk
// bare repo directly (the shared git storage every cloud replica mounts). It is the
// independent, in-process confirmation that a branch a sandbox claims to have pushed
// actually LANDED in native git — cloud trusts the branch tips it can read, not the
// remote runner's self-report. ok is false when git is unmounted, the repo/branch is
// absent, or the read fails (fail-closed: an unverifiable ref is treated as absent).
func VerifyRef(ctx context.Context, org, repo, branch string) (sha string, ok bool) {
	s := mounted.Load()
	if s == nil || org == "" || repo == "" || branch == "" {
		return "", false
	}
	// git is org-scoped at project "" (storeFor uses ""), so the bare repo lives at
	// the org/"" /repo path — the same absRepoPath the pack handlers operate on.
	bareDir := s.State.storage.absRepoPath(org, "", repo)
	tips := branchTips(ctx, bareDir)
	tip, present := tips[branch]
	if !present || tip == "" {
		return "", false
	}
	return tip, true
}
