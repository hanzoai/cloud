package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// files.go — the delivery inventory read, and its two adapters.
//
// ONE core (coreFiles) answers "which files does this glob select at this
// commit, and what do they say". The REST route serves it to browsers and the
// CLI; the internal plane serves it to delivery. Two thin adapters, one core —
// so the answer cannot differ by who asked.
//
// This is what replaces cloning for delivery. A generator never needs a
// packfile; it needs the bytes of some files at one revision. Serving that as a
// tree read is what lets a repository sit on object storage — no pack
// negotiation, no working copy, nothing on this path that needs POSIX.

// coreFiles resolves ref once and reads every path the glob selects.
//
// ONE resolve backs the whole reply. A caller that listed at `main` and then
// read at `main` could straddle a push and assemble half an inventory from one
// commit and half from the next; pinning here makes the read consistent by
// construction rather than by every caller remembering to.
func coreFiles(s *cloud.Service[state], ctx context.Context, t tenant, name, ref, glob string) (rev string, files []plane.File, err error) {
	if strings.TrimSpace(glob) == "" {
		return "", nil, errBadInput
	}
	r, found := findRepo(s, ctx, t.org, normalizeName(name))
	if !found {
		return "", nil, errNotFound
	}
	repo, err := openRepository(s, r)
	if err != nil {
		return "", nil, errNotFound
	}
	res, _, err := repo.Resolve(ctx, strings.TrimSpace(ref))
	if err != nil {
		return "", nil, errNotFound
	}

	paths, err := MatchPaths(ctx, repo, res, glob)
	if err != nil {
		return "", nil, err
	}
	out := make([]plane.File, 0, len(paths))
	for _, p := range paths {
		blob, err := repo.Blob(ctx, res, p, maxBlobBytes)
		if err != nil {
			// A path the walk just listed and the read cannot open is a broken
			// object store, not an empty file. Failing the whole read is right:
			// a partial inventory is the dangerous answer, because a caller
			// assembling a desired set would prune whatever went missing.
			return "", nil, fmt.Errorf("read %s at %s: %w", p, ShortRev(res), err)
		}
		f := plane.File{Path: p, Truncated: blob.Truncated}
		if !blob.Truncated {
			f.Data = blob.Content
		}
		out = append(out, f)
	}
	return res.String(), out, nil
}

// exposeFiles publishes the inventory read on the internal plane. Called from
// Mount, beside the other cross-app seams.
//
// The tenant comes from the CALLER, never the argument: the identity is what the
// edge minted, so an argument cannot widen the org it is answered for. Anonymous
// is refused rather than defaulted — delivery reaching git with no principal
// must fail, not read someone's repo.
func exposeFiles() {
	zip.Post[plane.FilesIn, plane.Files](cloud.Plane(), "/git/files", planeFiles,
		zip.WithOperationID(plane.GitFiles),
		zip.WithSummary("A repo's files at one revision"))
}

// planeFiles reads the glob-selected files of one of the caller's repos at one
// revision, returning the resolved commit and each file's path and contents.
// The org is the CALLER's plane identity, never the argument — an anonymous
// caller is refused — and the whole reply is read at one resolved commit, so a
// caller can never assemble half an inventory from each side of a push. A named
// handler, not a closure, so zipdoc can lift this prose into the registry.
func planeFiles(ctx context.Context, in *plane.FilesIn) (*plane.Files, error) {
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("git files: org required")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(503, "git not mounted")
	}
	rev, files, err := coreFiles(s, ctx, tenant{org: who.Org, project: who.Project}, in.Repo, in.Ref, in.Glob)
	switch {
	case errors.Is(err, errBadInput):
		return nil, zip.ErrBadRequest("glob is required")
	case errors.Is(err, errNotFound):
		return nil, zip.ErrNotFound("repo, ref or revision not found")
	case err != nil:
		return nil, zip.Errorf(500, "%v", err)
	}
	return &plane.Files{Rev: rev, Files: files}, nil
}
