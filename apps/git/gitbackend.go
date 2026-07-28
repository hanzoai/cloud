// gitbackend.go — git as ONE implementation of Repository (repository.go).
//
// This is the only file on the read path that knows go-git exists. Everything
// go-git-shaped stops here: *gogit.Repository, *object.Commit, plumbing.Hash,
// filemode.FileMode. Readers above it see refs, revisions, trees and bytes.
//
// The logic is lifted verbatim from the helpers this replaces (openGit,
// resolveRef, treeEntriesJSON, readmeAt, treeTextFiles) so the observable
// behaviour — ref-resolution fallbacks, dirs-before-files ordering, the binary
// check, the README candidate list — is unchanged. The one deliberate
// difference is that failures now come back as the model's sentinel errors
// instead of raw go-git errors, so a handler can tell "no such ref" from "this
// repository is broken" rather than reporting both as 404.
package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/hanzoai/cloud"
)

// gitRepository adapts a bare go-git repository to the Repository model. It
// keeps the stored Repo alongside it for the configured default branch, which
// is metadata we hold, not something the object store knows.
type gitRepository struct {
	repo *gogit.Repository
	meta Repo
}

// compile-time proof the backend satisfies the model.
var _ Repository = (*gitRepository)(nil)

// openRepository opens the bare repository backing a stored Repo and returns it
// as the model. This is the ONE door readers use; the go-git handle is not
// reachable through the returned value.
func openRepository(s *cloud.Service[state], r Repo) (Repository, error) {
	st, err := s.State.storage.storer(r.Org, r.Project, r.Name)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoRepository, err)
	}
	repo, err := gogit.Open(st, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoRepository, err)
	}
	return &gitRepository{repo: repo, meta: r}, nil
}

func (g *gitRepository) Refs(context.Context) (branches, tags []Ref, err error) {
	return collectGitRefs(g.repo.Branches()), collectGitRefs(g.repo.Tags()), nil
}

// collectGitRefs folds a go-git reference iterator into name+revision rows,
// sorted by name. An iterator error yields an empty set rather than a failure:
// an empty repository has no branches, and that is not an error to report.
func collectGitRefs(iter storer.ReferenceIter, err error) []Ref {
	out := []Ref{}
	if err != nil {
		return out
	}
	defer iter.Close()
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		out = append(out, Ref{Name: ref.Name().Short(), Rev: Revision(ref.Hash().String())})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (g *gitRepository) Resolve(_ context.Context, ref string) (Revision, string, error) {
	label := ref
	if label == "" {
		if h, err := g.repo.Head(); err == nil {
			label = h.Name().Short()
		} else {
			label = firstNonEmptyStr(g.meta.DefaultBranch, defaultBranchName)
		}
	}
	hash, err := g.repo.ResolveRevision(plumbing.Revision(label))
	if err != nil {
		// Fall back to the branch ref form for a bare branch name.
		ref2, e2 := g.repo.Reference(plumbing.NewBranchReferenceName(label), true)
		if e2 != nil {
			return "", label, fmt.Errorf("%w: %s", ErrNoRevision, label)
		}
		h := ref2.Hash()
		hash = &h
	}
	if _, err := g.repo.CommitObject(*hash); err != nil {
		return "", label, fmt.Errorf("%w: %s", ErrNoRevision, label)
	}
	return Revision(hash.String()), label, nil
}

// commitAt re-hydrates the go-git commit for an opaque revision. Every read
// method funnels through it, so a stale or bogus revision fails one way.
func (g *gitRepository) commitAt(rev Revision) (*object.Commit, error) {
	c, err := g.repo.CommitObject(plumbing.NewHash(string(rev)))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoRevision, rev)
	}
	return c, nil
}

func (g *gitRepository) Tree(_ context.Context, rev Revision, dir string) ([]Entry, error) {
	commit, err := g.commitAt(rev)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoRevision, err)
	}
	if dir != "" {
		if tree, err = tree.Tree(dir); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrNoPath, dir)
		}
	}
	dirs, files := []Entry{}, []Entry{}
	for i := range tree.Entries {
		e := tree.Entries[i]
		full := e.Name
		if dir != "" {
			full = dir + "/" + e.Name
		}
		if e.Mode == filemode.Dir {
			dirs = append(dirs, Entry{Name: e.Name, Path: full, Dir: true, Mode: modeString(e.Mode)})
			continue
		}
		var size int64
		if f, ferr := tree.TreeEntryFile(&e); ferr == nil {
			size = f.Size
		}
		files = append(files, Entry{Name: e.Name, Path: full, Size: size, Mode: modeString(e.Mode)})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return append(dirs, files...), nil
}

func (g *gitRepository) Blob(_ context.Context, rev Revision, path string, maxBytes int64) (Blob, error) {
	commit, err := g.commitAt(rev)
	if err != nil {
		return Blob{}, err
	}
	file, err := commit.File(path)
	if err != nil {
		return Blob{}, fmt.Errorf("%w: %s", ErrNoPath, path)
	}
	out := Blob{Path: path, Size: file.Size}
	if maxBytes > 0 && file.Size > maxBytes {
		out.Truncated = true
		return out, nil
	}
	bin, _ := file.IsBinary()
	out.Binary = bin
	reader, err := file.Reader()
	if err != nil {
		return Blob{}, fmt.Errorf("read blob %s: %w", path, err)
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(reader)
	if err != nil {
		return Blob{}, fmt.Errorf("read blob %s: %w", path, err)
	}
	out.Content = raw
	return out, nil
}

func (g *gitRepository) Log(_ context.Context, rev Revision, path string, limit int) ([]Change, error) {
	commit, err := g.commitAt(rev)
	if err != nil {
		return nil, err
	}
	opts := &gogit.LogOptions{From: commit.Hash}
	if path != "" {
		opts.FileName = &path
	}
	iter, err := g.repo.Log(opts)
	if err != nil {
		return nil, fmt.Errorf("log %s: %w", rev, err)
	}
	defer iter.Close()
	out := []Change{}
	for i := 0; i < limit; i++ {
		cm, e := iter.Next()
		if e != nil {
			break
		}
		out = append(out, Change{
			Rev:         Revision(cm.Hash.String()),
			Message:     cm.Message,
			AuthorName:  cm.Author.Name,
			AuthorEmail: cm.Author.Email,
			When:        cm.Author.When,
		})
	}
	return out, nil
}

func (g *gitRepository) WalkText(_ context.Context, rev Revision, maxFileBytes int64, fn func(path, content string) error) error {
	commit, err := g.commitAt(rev)
	if err != nil {
		return err
	}
	iter, err := commit.Files()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoRevision, err)
	}
	defer iter.Close()
	err = iter.ForEach(func(f *object.File) error {
		// Size first: skipping before Contents() is what keeps a multi-GB blob
		// out of the pod's memory.
		if maxFileBytes > 0 && f.Size > maxFileBytes {
			return nil
		}
		if bin, _ := f.IsBinary(); bin {
			return nil
		}
		txt, e := f.Contents()
		if e != nil {
			return nil // an unreadable blob is skipped, never fatal to the walk
		}
		if e := fn(f.Name, txt); e != nil {
			if errors.Is(e, StopWalk) {
				return storer.ErrStop
			}
			return e
		}
		return nil
	})
	if errors.Is(err, storer.ErrStop) {
		return nil
	}
	return err
}

// DefaultBranch reads the symbolic HEAD target. initBare points HEAD at the
// default branch, so this holds for a bare repository with no checkout.
func (g *gitRepository) DefaultBranch(context.Context) (string, error) {
	ref, err := g.repo.Reference(plumbing.HEAD, false) // false: keep the symbolic ref
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoRepository, err)
	}
	return ref.Target().Short(), nil
}

// readmeAt returns the tree-root README's filename + text content, if any. The
// ONE readme scan — the HTML repo home (ui.go) and the JSON /readme both use it.
func readmeAt(ctx context.Context, r Repository, rev Revision) (string, string, bool) {
	for _, cand := range []string{"README.md", "README.MD", "Readme.md", "README", "readme.md"} {
		b, err := r.Blob(ctx, rev, cand, 0)
		if err != nil || b.Binary {
			continue
		}
		return cand, string(b.Content), true
	}
	return "", "", false
}

func modeString(m filemode.FileMode) string { return fmt.Sprintf("%06o", uint32(m)) }
