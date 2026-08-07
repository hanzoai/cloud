package main

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/git"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The two facts a coding run needs from git, published on the internal plane.
//
// Both already existed as exported in-process functions (apps/git/export.go),
// and both read git's `mounted` global. That global belongs to THIS process, so
// the coding orchestrator — which runs in the integrations process — read nil
// through them: CloneURL answered "", which the dispatcher renders as "git is
// not available", and VerifyRef answered not-found, which fails a finished run
// closed and files no PR. The functions are right; they were simply being called
// from the wrong side of a process boundary.
//
// They are declared HERE, at the app's own composition root, rather than inside
// apps/git: what this adds is not a git capability but the DOOR one, and this
// file is where the other cross-subsystem wiring already lives (see
// plugin/integrations/seams.go). Registration on cloud.Plane() before
// cloud.Listen is what ServePlane then binds — an op registry is a value, not a
// listener, so declaring it early costs nothing and cannot race the mount.
func init() {
	zip.Post[plane.RepoRefIn, plane.RepoCloneURL](cloud.Plane(), "/git/clone-url", planeCloneURL,
		zip.WithOperationID(plane.GitCloneURL),
		zip.WithSummary("The canonical clone URL of an org's native repo"))

	zip.Post[plane.RefIn, plane.RefTip](cloud.Plane(), "/git/verify-ref", planeVerifyRef,
		zip.WithOperationID(plane.GitVerifyRef),
		zip.WithSummary("The tip of a branch, read from git's own storage"))

	zip.Post[plane.ProposeIn, plane.Proposed](cloud.Plane(), "/git/propose", planePropose,
		zip.WithOperationID(plane.GitPropose),
		zip.WithSummary("Offer a branch for merging, and answer where it is read"))
}

// planePropose opens the pull request for a finished run.
//
// The org is the CALLER's plane identity and never a field, exactly as the grant
// door resolves it: a caller able to name the org could propose a branch into
// another tenant's repository — and, on the GitHub path, mint that tenant's
// installation token to do it.
func planePropose(ctx context.Context, in *plane.ProposeIn) (*plane.Proposed, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git propose: org required")
	}
	url, err := git.Propose(ctx, who.Org, in.Project, in.Repo, in.Base, in.Head, in.Title, in.Body)
	if err != nil {
		return nil, err
	}
	return &plane.Proposed{URL: url}, nil
}

// planeCloneURL answers with the org-scoped clone URL, or an EMPTY one when git
// cannot say. Empty is a real answer here and not an error: the caller already
// treats "no clone URL" as "git is not available" and refuses the run, so
// returning an error would only give that same outcome a second spelling.
func planeCloneURL(_ context.Context, in *plane.RepoRefIn) (*plane.RepoCloneURL, error) {
	return &plane.RepoCloneURL{URL: git.CloneURL(in.Org, in.Repo)}, nil
}

// planeVerifyRef reports whether the branch LANDED, and at which commit. Found
// is carried explicitly so an absent branch cannot arrive as a present one with
// an unknown tip — the integrity gate this feeds is the reason a run's PR exists
// at all.
func planeVerifyRef(ctx context.Context, in *plane.RefIn) (*plane.RefTip, error) {
	sha, ok := git.VerifyRef(ctx, in.Org, in.Repo, in.Branch)
	return &plane.RefTip{SHA: sha, Found: ok}, nil
}
