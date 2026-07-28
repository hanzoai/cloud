package git

import (
	"context"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
)

// index_on_import.go closes the one gap that kept /v1/code from covering EVERY
// mirrored repo: a one-shot IMPORT / org /mirror lands a repo's default branch
// without a client push, so it emitted NO lifecycle event and stayed out of the code
// index until its NEXT push. The fix reuses the SAME event the push + inbound-sync
// paths already emit — cloud.LifecyclePushLanded — so there is ONE index path (the
// index_on_push reactor), reached on import too. No second indexer, no special case.
//
// It fans out through the shared lifecycle stream, so it ALSO drives the outbound
// mirror + notify subscribers exactly as a push would — which is why Origin is the
// SOURCE HOST (not a synthetic marker): mirror_out suppresses the echo back to the
// host the refs just arrived from (identical to InboundGitSync), so importing FROM
// github never force-pushes straight back TO github. notify no-ops on a fresh repo
// with no Slack subscriptions. Best-effort + detached by construction: EmitLifecycle
// fans each subscriber out on a cancel-immune goroutine with a panic-recover, so this
// can never block or fail the import/mirror the writer just completed.

// emitImportPush fires push.landed for a freshly imported/mirrored repo's DEFAULT
// branch, so index_on_push indexes its tip on import (not only on the next push).
// srcURL is the upstream the refs came from — its host becomes Origin so the outbound
// mirror suppresses the echo. A repo with no resolvable default-branch tip (empty /
// detached) is a silent no-op. Never returns an error: the import must not fail
// because indexing-on-import could not be triggered.
func emitImportPush(s *cloud.Service[state], ctx context.Context, org, project, repo, srcURL string) {
	r, err := openRepository(s, Repo{Org: org, Project: project, Name: repo})
	if err != nil {
		s.Log.Warn("index-on-import: open repo", "org", org, "repo", repo, "err", err)
		return
	}
	branch, after, ok := defaultBranchTip(ctx, r)
	if !ok {
		return // no default-branch tip yet — nothing to index
	}
	cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
		Kind:    cloud.LifecyclePushLanded,
		Org:     org,
		Project: project,
		Repo:    repo,
		Branch:  branch,
		After:   after,
		Origin:  importHost(srcURL),
	})
	s.Log.Info("index-on-import: emitted push.landed", "org", org, "repo", repo, "branch", branch, "commit", shortSHA(after))
}

// defaultBranchTip resolves a bare repo's default branch NAME (the symbolic-HEAD
// target — the same identity index_on_push's isDefaultBranch checks) and its tip
// commit SHA. ok is false for an empty or detached repo (no branch to index).
func defaultBranchTip(ctx context.Context, r Repository) (branch, sha string, ok bool) {
	branch, err := r.DefaultBranch(ctx)
	if err != nil || branch == "" {
		return "", "", false
	}
	rev, _, err := r.Resolve(ctx, "")
	if err != nil {
		return "", "", false
	}
	return branch, rev.String(), true
}

// importHost returns the lowercased host of a clone URL — the Origin the outbound
// mirror matches to suppress the echo back to the source. "" on a parse miss (⇒ no
// suppression, which for a source with no registered outbound target is harmless).
func importHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
