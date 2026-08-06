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
