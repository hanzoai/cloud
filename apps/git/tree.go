package git

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
)

// tree.go implements cloud's tree seam — the delivery plane's read of a repo's
// inventory at a revision. Registered at Mount, so apps/deploy reaches it with
// no deploy⇄git import, the same shape as the GitImporter and mirror-controller
// seams above it.
//
// It is the SAME core the /v1/git/repos/:name/paths route serves (MatchPaths +
// Repository.Blob), so the in-process caller and the HTTP/ZAP caller cannot
// drift: two thin adapters, one core.
//
// # Bytes, not a working copy
//
// The delivery engine's other source shallow-clones with the git CLI, which
// needs a POSIX filesystem and a credential per source. This path resolves a
// revision, matches a glob, and reads blobs — no clone, no checkout, no
// credential, and nothing that stops the object store being S3.

// treeFileCap is the default per-file byte limit when a caller names none. A
// values file is kilobytes; anything at this size is not an inventory entry, and
// reporting it in Paths while omitting it from Files makes that visible rather
// than loading it to find out.
const treeFileCap = 1 << 20 // 1 MiB

// readTree answers cloud.ReadTree from the git object plane.
func readTree(ctx context.Context, q cloud.TreeQuery) (cloud.Tree, error) {
	s := mounted.Load()
	if s == nil {
		return cloud.Tree{}, fmt.Errorf("git: not mounted")
	}
	if q.Org == "" || q.Repo == "" {
		return cloud.Tree{}, fmt.Errorf("git: org and repo are required")
	}

	r, found := findRepo(s, ctx, q.Org, normalizeName(q.Repo))
	if !found {
		return cloud.Tree{}, fmt.Errorf("git: repo %q not found in org %q", q.Repo, q.Org)
	}
	repo, err := openRepository(s, r)
	if err != nil {
		return cloud.Tree{}, fmt.Errorf("git: open %q: %w", q.Repo, err)
	}
	rev, _, err := repo.Resolve(ctx, q.Ref)
	if err != nil {
		return cloud.Tree{}, fmt.Errorf("git: resolve %q: %w", q.Ref, err)
	}

	paths, err := MatchPaths(ctx, repo, rev, q.Glob)
	if err != nil {
		return cloud.Tree{}, err
	}

	max := q.MaxBytes
	if max <= 0 {
		max = treeFileCap
	}
	out := cloud.Tree{Rev: rev.String(), Paths: paths, Files: make(map[string][]byte, len(paths))}
	for _, p := range paths {
		blob, err := repo.Blob(ctx, rev, p, max)
		if err != nil {
			// A path the walk just listed and the read cannot open is a broken
			// object store, not an empty file. Failing the whole read is right:
			// a partial inventory handed to a pruning reconcile is how a fleet
			// gets swept.
			return cloud.Tree{}, fmt.Errorf("git: read %q at %s: %w", p, ShortRev(rev), err)
		}
		if blob.Truncated {
			continue // listed in Paths, deliberately absent from Files
		}
		out.Files[p] = blob.Content
	}
	return out, nil
}
