package git

import (
	"context"
	"path"
	"slices"
	"sort"
	"strings"
)

// match.go — resolve a glob to the paths it selects at one revision.
//
// This exists for GitOps. A delivery generator's whole question is "which files
// under this commit are the inventory" — `charts/app/values/*/*.yaml` — and the
// answer is a LIST OF PATHS, not a packfile. Serving it as a tree read is what
// lets the repository sit on S3: there is no pack negotiation, no clone, and no
// working copy, so nothing here needs POSIX. The pure-Go transport that buffered
// a whole pack in RAM (a 3 GB clone costing 3 GB) is the path this deliberately
// does not take.
//
// It is a FUNCTION over the Repository interface, not a method on it. Every
// backend gets globbing for free the moment it can list a directory, and a new
// backend implements nothing extra — the interface stays the small read model it
// is, and the traversal policy lives in one place instead of once per backend.

// matchLimit caps how many paths one glob may return. A generator asking for
// `**/*.yaml` against a monorepo should get a bounded answer rather than a
// response that grows without limit; the cap is far above any real inventory.
const matchLimit = 10_000

// matchDirLimit caps how many directories one glob may descend into, bounding a
// `**` walk over a pathological tree. Reached, it stops early — a truncated
// answer is wrong in a way the caller can see, where an unbounded walk is only
// wrong in a way the pod notices.
const matchDirLimit = 5_000

// MatchPaths lists every file path at rev selected by glob, sorted.
//
// The glob is matched SEGMENT BY SEGMENT with path.Match, so `*` never crosses a
// `/` — `values/*/*.yaml` selects `values/hanzo/www.yaml` and not
// `values/a/b/c.yaml`. `**` matches zero or more whole segments; as the final
// segment it selects every file beneath. Only files are returned: a directory is
// something to descend, never a result.
//
// Matching is prefix-pruned — a segment that matches nothing stops that branch —
// so a specific glob reads a handful of trees rather than the whole commit.
func MatchPaths(ctx context.Context, repo Repository, rev Revision, glob string) ([]string, error) {
	segs := splitGlob(glob)
	if len(segs) == 0 {
		return nil, nil
	}
	w := &globWalk{ctx: ctx, repo: repo, rev: rev, segs: segs}
	if err := w.walk("", 0); err != nil {
		return nil, err
	}
	sort.Strings(w.out)
	// `**/**/x` can reach one file down two different segment splits. Dedup here
	// rather than tracking visits during the walk: the walk stays a plain
	// traversal, and a caller can never receive the same path twice.
	return slices.Compact(w.out), nil
}

// splitGlob normalizes a glob to its meaningful segments. A leading `/` or `./`
// and any empty segment are noise from however the caller spelled the path.
func splitGlob(glob string) []string {
	out := make([]string, 0, 8)
	for _, s := range strings.Split(path.Clean("/"+glob), "/") {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

type globWalk struct {
	ctx  context.Context
	repo Repository
	rev  Revision
	segs []string
	out  []string
	dirs int
}

// full reports whether either budget is spent, which ends the walk wherever it is.
func (w *globWalk) full() bool { return len(w.out) >= matchLimit || w.dirs >= matchDirLimit }

// walk lists dir and applies segs[i] to its children.
func (w *globWalk) walk(dir string, i int) error {
	if w.full() || i >= len(w.segs) {
		return nil
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.dirs++

	entries, err := w.repo.Tree(w.ctx, w.rev, dir)
	if err != nil {
		// A path that does not exist at this revision selects nothing. Only a
		// glob's LITERAL segments can miss, and a caller globbing for files that
		// are not there wants an empty list, not a 404.
		return nil
	}

	seg, last := w.segs[i], i == len(w.segs)-1

	if seg == "**" {
		return w.walkDoubleStar(dir, i, last, entries)
	}
	for _, e := range entries {
		if w.full() {
			return nil
		}
		if ok, _ := path.Match(seg, e.Name); !ok {
			continue
		}
		switch {
		case last && !e.Dir:
			w.out = append(w.out, e.Path)
		case !last && e.Dir:
			if err := w.walk(e.Path, i+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkDoubleStar handles `**`: zero segments (retry the rest here) plus one or
// more (descend, still on the same segment). As the last segment it collects
// every file beneath instead, which is what `deploy/**` plainly means.
func (w *globWalk) walkDoubleStar(dir string, i int, last bool, entries []Entry) error {
	if last {
		for _, e := range entries {
			if w.full() {
				return nil
			}
			if !e.Dir {
				w.out = append(w.out, e.Path)
				continue
			}
			if err := w.walk(e.Path, i); err != nil {
				return err
			}
		}
		return nil
	}
	// Zero segments: the rest of the glob may match right here.
	if err := w.walk(dir, i+1); err != nil {
		return err
	}
	// One or more: every subtree, with `**` still in play.
	for _, e := range entries {
		if w.full() {
			return nil
		}
		if e.Dir {
			if err := w.walk(e.Path, i); err != nil {
				return err
			}
		}
	}
	return nil
}
