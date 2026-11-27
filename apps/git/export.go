package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk/env"
)

// export.go — a repository kept in the store, as a directory, for the work that is the
// git CLI's.
//
// A fetch and a push are answered from the store (serve.go). Pushing a branch to a
// downstream host, or every branch and tag to the forge, is `git push` against a remote
// with the credential and transport limits mirror.go builds, and that is the CLI reading
// a directory. So the part of the repository the push reaches is written to a throwaway
// bare directory, the CLI does what it did, and the directory is removed. Nothing in it
// is kept: it is not a second copy of anything.

// transitDir is where a throwaway directory is made: GIT_TRANSIT_DIR, the scratch volume a
// deployment mounts for work that is the CLI's (universe: an emptyDir at
// /var/lib/cloud/transit), else the system temp directory. Nothing in it outlives the call
// that made it.
func transitDir() string { return env.Or("GIT_TRANSIT_DIR", os.TempDir()) }

// exportDir writes refs, and every object they lead to, to a new bare directory under
// the temp dir, HEAD at head when it is not empty. cleanup removes it and must be called.
func exportDir(ctx context.Context, repo *objects.Repo, refs []*plumbing.Reference, head string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp(transitDir(), "git-export-*")
	if err != nil {
		return "", nil, fmt.Errorf("export: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	defer func() {
		if err != nil {
			cleanup()
			dir, cleanup = "", nil
		}
	}()
	if err = gitQuiet(ctx, "init", "--bare", "--quiet", dir); err != nil {
		return "", nil, fmt.Errorf("export: %w", err)
	}
	var wants []plumbing.Hash
	for _, ref := range refs {
		wants = append(wants, ref.Hash())
	}
	if len(wants) > 0 {
		pl, perr := (&request{wants: wants}).plan(repo)
		if perr != nil {
			return "", nil, fmt.Errorf("export: %w", perr)
		}
		if err = indexPack(ctx, repo, dir, pl.oids); err != nil {
			return "", nil, fmt.Errorf("export: %w", err)
		}
	}
	var updates bytes.Buffer
	for _, ref := range refs {
		fmt.Fprintf(&updates, "update %s %s\n", ref.Name(), ref.Hash())
	}
	if updates.Len() > 0 {
		cmd, cerr := gitCmd(ctx, nil, "--git-dir="+dir, "update-ref", "--stdin")
		if cerr != nil {
			return "", nil, cerr
		}
		stderr := &cappedBuffer{cap: stderrCap}
		cmd.Stdin, cmd.Stderr = &updates, stderr
		if err = cmd.Run(); err != nil {
			return "", nil, fmt.Errorf("export refs: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	if head != "" {
		if err = gitQuiet(ctx, "--git-dir="+dir, "symbolic-ref", "HEAD", plumbing.NewBranchReferenceName(head).String()); err != nil {
			return "", nil, fmt.Errorf("export HEAD: %w", err)
		}
	}
	return dir, cleanup, nil
}

// indexPack feeds the pack of oids to `git index-pack --stdin` in dir, under a pack
// slot like every other pack the CLI reads.
func indexPack(ctx context.Context, repo *objects.Repo, dir string, oids []plumbing.Hash) error {
	return withPackSlot(ctx, func() error {
		cmd, err := gitCmd(ctx, nil, "--git-dir="+dir, "index-pack", "--stdin")
		if err != nil {
			return err
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		stderr := &cappedBuffer{cap: stderrCap}
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		werr := repo.Pack(ctx, stdin, oids)
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("index-pack: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return werr
	})
}

// errNoBranch is a branch the repository no longer has.
var errNoBranch = errors.New("git: no such branch")

// exportBranch is exportDir for one branch, the one a mirror push sends.
func exportBranch(ctx context.Context, repo *objects.Repo, branch string) (string, func(), error) {
	tip, err := repo.Reference(plumbing.NewBranchReferenceName(branch))
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", nil, errNoBranch
	}
	if err != nil {
		return "", nil, err
	}
	return exportDir(ctx, repo, []*plumbing.Reference{tip}, headBranch(repo))
}

// hasHistory is whether repo holds any branch or tag.
func hasHistory(repo *objects.Repo) bool {
	refs, err := repo.References()
	if err != nil {
		return true // unreadable is kept as if it held something, as history does for a directory
	}
	for _, ref := range refs {
		if ref.Type() == plumbing.HashReference && (ref.Name().IsBranch() || ref.Name().IsTag()) {
			return true
		}
	}
	return false
}

// tipsAndTags are the branches and tags of repo, the refs a move to another host carries.
func tipsAndTags(repo *objects.Repo) ([]*plumbing.Reference, error) {
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	var out []*plumbing.Reference
	for _, ref := range refs {
		if ref.Type() == plumbing.HashReference && (ref.Name().IsBranch() || ref.Name().IsTag()) {
			out = append(out, ref)
		}
	}
	return out, nil
}
