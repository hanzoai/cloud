// push.go — git-push-to-deploy: turn a push landed on the embedded git server
// (clients/git) into a build for every app that tracks that repo+branch.
//
// Wiring is inverted so git never imports platform: platform registers
// buildFromPush as the cloud.PushBuilder in Mount; clients/git calls
// cloud.OnGitPush after a push lands, which dispatches here. Best-effort by
// contract — a build-trigger failure never fails the push the client committed.
package platform

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// buildFromPush launches a build for every git-source app whose RepoURL + branch
// matches the pushed ref, reusing the ONE build-launch core (startGitBuild). It is
// the registered cloud.PushBuilder. A push that maps to no app is the common case
// and returns nil; an error is returned only for a store read the caller may log.
func (s *svc) buildFromPush(ctx context.Context, ev cloud.GitPushEvent) error {
	apps, err := s.store.ListAllApplications(ctx, ev.Org)
	if err != nil {
		return err
	}
	// Detach from the push request's lifetime — the git handler returns as soon as
	// the ref lands, but the build must outlive it. Bounded (per-org build cap in
	// launchBuildJob), so nothing leaks.
	ctx = context.WithoutCancel(ctx)

	n := 0
	for _, a := range apps {
		if a.Source != "git" || !sameRepo(a.RepoURL, ev.CloneURL) || !tracksBranch(a, ev.Branch) {
			continue
		}
		n++
		now := time.Now().Unix()
		version, verr := s.store.NextVersion(ctx, a.ID)
		if verr != nil {
			s.log.Warn("push build: version alloc failed", "org", ev.Org, "app", a.Slug, "err", verr)
			continue
		}
		depID, derr := genID("dep")
		if derr != nil {
			s.log.Warn("push build: rng failed", "org", ev.Org, "app", a.Slug, "err", derr)
			continue
		}
		_, jobName, _, berr := s.startGitBuild(ctx, ev.Org, a, depID, version, now, ev.Commit, s.k8s.ready())
		if berr != nil {
			s.log.Warn("push build failed", "org", ev.Org, "app", a.Slug, "err", berr)
			continue
		}
		s.log.Info("build launched (git push)", "org", ev.Org, "app", a.Slug, "job", jobName,
			"repo", ev.Repo, "branch", ev.Branch, "commit", shortTag(ev.Commit))
	}
	if n == 0 {
		s.log.Debug("git push: no app tracks this repo+branch", "org", ev.Org, "repo", ev.Repo, "branch", ev.Branch)
	}
	return nil
}

// sameRepo compares two repo URLs ignoring a trailing ".git" and slash. The app's
// RepoURL and the git server's clone URL address the same host, so exact match
// after normalization is right. An empty RepoURL never matches.
func sameRepo(a, b string) bool {
	return a != "" && normRepo(a) == normRepo(b)
}

func normRepo(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	return strings.ToLower(strings.TrimSuffix(u, ".git"))
}

// tracksBranch reports whether app a's tracked branch is the pushed branch. An
// empty RepoBranch defaults to "main", matching app-create (branchDefault).
func tracksBranch(a Application, branch string) bool {
	return firstNonEmpty(a.RepoBranch, "main") == branch
}
