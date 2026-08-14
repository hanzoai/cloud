// push.go — git-push-to-deploy: turn a push landed on the embedded git server
// (clients/git) into a build for every app that tracks that repo+branch.
//
// Wiring is inverted so git never imports platform: platform registers
// buildFromPush as the cloud.PushBuilder in Mount; clients/git calls
// cloud.OnGitPush after a push lands, which dispatches here. Best-effort by
// contract — a build-trigger failure never fails the push the client committed.

package platform

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// buildFromPush launches a build for every git-source app whose RepoURL + branch
// matches the pushed ref, reusing the ONE build-launch core (startGitBuild). It is
// the registered cloud.PushBuilder. A push that maps to no app is the common case
// and returns nil; an error is returned only for a store read the caller may log.
//
// It RETURNS THE NUMBER OF BUILDS IT LAUNCHED, because a caller that cannot tell
// "built" from "did nothing" has to guess, and the guess is always the optimistic
// one: the forge shows a delivery green whenever the receiver answered 2xx, so a
// push that matched no application looked exactly like a push that built. That is
// the same shape as the 204 this whole path was rebuilt to end, one layer up. The
// plane leg carries it too (plane.Built.Builds), which is what that type's comment
// was waiting for — "the day something needs the count".
func buildFromPush(s *cloud.Service[state], ctx context.Context, ev cloud.GitPushEvent) (int, error) {
	// Cloud's own upstream is not an Application — no row tracks it — so the
	// self-release is dispatched from the same event, before the app scan. This is
	// what makes a merge to main produce an image: the ONE image owner
	// (release.go) is now driven by the push instead of waiting for someone to
	// call /v1/runner by hand.
	if isReleasePush(ev) {
		if _, image, err := launchRelease(s, ctx, ev.Commit, releaseRepoURL, ""); err != nil {
			// Never fail the push over a release we could not start — the commit is
			// already landed, and a conflict just means one is already running.
			s.Log.Warn("push release: not started", "commit", ev.Commit, "err", err)
		} else {
			s.Log.Info("push release: started", "commit", ev.Commit, "image", image)
		}
	}

	apps, err := s.State.store.ListAllApplications(ctx, ev.Org)
	if err != nil {
		return 0, err
	}
	// Detach from the push request's lifetime — the git handler returns as soon as
	// the ref lands, but the build must outlive it. Bounded (per-org build cap in
	// launchBuildJob), so nothing leaks.
	ctx = context.WithoutCancel(ctx)

	// The event carries a FULL ref so tags reach us; an app tracks a BRANCH, so
	// name the branch once, here, rather than making every caller think in refs.
	// CutPrefix answers both questions at once: a tag yields isBranch false and
	// never rebuilds an app.
	branch, isBranch := strings.CutPrefix(ev.Ref, "refs/heads/")

	// Two counts, because they answer different questions and one number cannot
	// carry both: matched is how many applications TRACK this repo+ref, launched is
	// how many builds actually STARTED. They used to be one variable incremented on
	// the match, which reported an app whose build failed to start as an app that
	// built — the smaller version of the same lie the caller was told.
	matched, launched := 0, 0
	for _, a := range apps {
		if a.Source != "git" || !sameRepo(a.RepoURL, ev.CloneURL) || !isBranch || !tracksBranch(a, branch) {
			continue
		}
		matched++
		now := time.Now().Unix()
		version, verr := s.State.store.NextVersion(ctx, a.ID)
		if verr != nil {
			s.Log.Warn("push build: version alloc failed", "org", ev.Org, "app", a.Slug, "err", verr)
			continue
		}
		depID := genID("dep")
		_, jobName, _, berr := startGitBuild(s, ctx, ev.Org, a, depID, version, now, ev.Commit, s.State.k8s.ready())
		if berr != nil {
			s.Log.Warn("push build failed", "org", ev.Org, "app", a.Slug, "err", berr)
			continue
		}
		launched++
		s.Log.Info("build launched (git push)", "org", ev.Org, "app", a.Slug, "job", jobName,
			"repo", ev.Repo, "ref", ev.Ref, "commit", shortTag(ev.Commit))
	}
	switch {
	case matched == 0:
		// WARN, not Debug. A push that reaches here and matches nothing is the
		// fleet's commonest real defect and its quietest — an application's RepoURL
		// is free text a person typed. At Debug that line is off in production,
		// which is the same as not having it.
		s.Log.Warn("git push: no app tracks this repo+ref — nothing was built",
			"org", ev.Org, "repo", ev.Repo, "ref", ev.Ref, "cloneUrl", ev.CloneURL)
	case launched == 0:
		// Worse, and distinct: applications DO track this ref and not one build
		// started. That is an outage in the build path, not a mismatch in a URL.
		s.Log.Error("git push: every matching app failed to start a build",
			"org", ev.Org, "repo", ev.Repo, "ref", ev.Ref, "matched", matched)
	}
	return launched, nil
}

// sameRepo reports whether two URLs name the same repository. An empty one
// never matches anything.
func sameRepo(a, b string) bool {
	return a != "" && normRepo(a) == normRepo(b)
}

// releaseBranch is the only branch that cuts a release. A release publishes the
// next version of the image the whole fleet runs, so it follows the one branch
// that is reviewed and merged into, never a feature branch.
// releaseRef is the full ref whose merges publish the next version. Matching on
// the full ref rather than a short name keeps a tag named "main" from cutting a
// release: refs/tags/main is not refs/heads/main.
const releaseRef = "refs/heads/main"

// isReleasePush reports whether a landed push is a merge to cloud's own upstream
// default branch — the event that should publish the next version.
//
// It matches on the repo URL rather than the org, because the org that owns the
// installation is not what identifies this repo. A push carrying no commit is
// ignored: the pipeline pins an exact commit, and resolving a branch name again
// here could build something newer than the event describes.
//
// It needs no forge clause of its own: hanzoai/cloud on the forge and
// hanzoai/cloud on the upstream are ONE repository under [normRepo], so a merge
// to main publishes a version whichever host the merge landed on. That was the
// point of the migration and it was the half that did not work.
func isReleasePush(ev cloud.GitPushEvent) bool {
	return ev.Ref == releaseRef && ev.Commit != "" && sameRepo(releaseRepoURL, ev.CloneURL)
}

// upstream is where the estate's repositories are published besides the forge.
// One repository, two spellings, for as long as the migration runs.
const upstream = "github.com"

// forgeHost is this deployment's own forge, resolved once at Mount from the ONE
// derivation of it (forge.Host), beside selfGitHost and for the same reason.
// Empty in a deployment that names no forge, which simply leaves the forge
// spelling unrecognised — fewer matches, never more.
var forgeHost string

// mirrored reports whether host holds the estate's repositories under a path
// that is the whole of their identity: the upstream, and the forge it is
// migrating to.
func mirrored(host string) bool {
	return host == upstream || (forgeHost != "" && host == forgeHost)
}

// normRepo is a repository's identity, and it is the whole of what [sameRepo]
// compares: the host and the path, lowercased, without scheme, trailing slash
// or ".git" — except on a MIRRORED host, which contributes no identity at all.
//
// DROPPING THE HOST THERE IS THE POINT. github.com/hanzoai/cloud and
// git.hanzo.ai/hanzoai/cloud are one repository mirrored across the migration;
// an application's RepoURL is what a person typed and the estate's habit is the
// upstream spelling, while a forge delivery derives the forge one. Compared with
// the host in, those two were never equal: every forge push matched nothing,
// built nothing, and showed green on the forge's delivery page.
//
// THE PATH IS COMPARED WHOLE, never by its last two segments, and that is what
// keeps the collapse from being an escalation. The embedded git server's clone
// URL is https://api.hanzo.ai/v1/git/<org>/<project>/<repo>, whose tail is named
// by a tenant: a project `hanzoai` holding a repo `cloud` would spell the
// release repository's own coordinate, and a push to its main would cut a
// release of the binary the whole fleet runs. Whole paths, so /v1/git/... is
// simply a different repository — and every host outside the mirrored pair keeps
// host + path, because two repositories that merely share a path on different
// servers are two repositories.
func normRepo(u string) string {
	u = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(u)), "/")
	u = strings.TrimSuffix(u, ".git")
	if u == "" {
		return ""
	}
	if _, rest, scheme := strings.Cut(u, "://"); scheme {
		u = rest
	}
	host, path, _ := strings.Cut(u, "/")
	if mirrored(host) {
		return "/" + path
	}
	return host + "/" + path
}

// tracksBranch reports whether app a's tracked branch is the pushed branch. An
// empty RepoBranch defaults to "main", matching app-create (branchDefault).
func tracksBranch(a Application, branch string) bool {
	return cmp.Or(a.RepoBranch, "main") == branch
}
