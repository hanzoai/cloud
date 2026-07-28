package cloud

import (
	"context"
	"errors"
)

// tree_seam.go is the inversion layer between the git object plane (apps/git)
// and the delivery plane (apps/deploy). Delivery needs one thing from git: the
// set of files a glob selects at a revision, and their bytes. It gets that here
// WITHOUT importing git — the SAME idiom as sync_seam.go and tracker_seam.go:
// the provider registers itself at Mount, the consumer calls the package func,
// one seam, one direction, no cycles.
//
// # Why this is a tree read and not a clone
//
// The delivery engine's existing source shallow-clones repo@ref with the git
// CLI. That drags in a working copy, a POSIX filesystem, and a credential for
// every source it reads — and it is the reason a repository cannot simply live
// on S3. A generator never needs a packfile: it needs "which files are the
// inventory at this commit, and what do they say". Asking exactly that is what
// makes object storage a natural backing store instead of a compromise.
//
// # Why one call returns paths AND bytes
//
// A generator that lists at `main` and then reads files at `main` can straddle
// a push and build half its inventory from one commit and half from the next.
// Resolving once and returning the pinned revision with the bytes makes the
// whole read consistent by construction, so no caller has to remember to pin.
//
// # Transport
//
// Co-mounted in one binary, the registered implementation is a direct call —
// no transport at all, which is the cheapest correct answer. Split across
// processes, the registrant is a ZAP client against the same git procedures.
// The seam is identical either way, so which deployment shape is in use is not
// a fact any caller has to know.

// TreeQuery selects files in one repo at one revision.
type TreeQuery struct {
	// Org is the tenant. A repo outside it is simply not found — isolation is
	// the git plane's, not re-implemented here.
	Org string
	// Repo is the short repo name.
	Repo string
	// Ref is a branch, tag or commit; empty means the repo's default.
	Ref string
	// Glob selects files, matched segment by segment so `*` never crosses a
	// `/`. `**` matches zero or more whole segments.
	Glob string
	// MaxBytes caps EACH file. A file past it is reported in Paths and omitted
	// from Files, so an oversized blob is visible without being loaded.
	MaxBytes int64
}

// Tree is the inventory at a pinned revision.
type Tree struct {
	// Rev is the full revision Ref resolved to. Pin any follow-up read to it.
	Rev string
	// Paths are every file the glob selected, sorted — including any omitted
	// from Files for size.
	Paths []string
	// Files maps path to content for the files that were read.
	Files map[string][]byte
}

// TreeFunc reads a repo tree. The one implementation (apps/git) registers it at
// Mount; delivery reaches it via ReadTree.
type TreeFunc func(ctx context.Context, q TreeQuery) (Tree, error)

// ErrNoTreeReader reports that nothing has registered a reader — the git plane
// is not mounted in this binary and no remote client was configured. Delivery
// treats it as "this source is unavailable", never as "the inventory is empty":
// an empty desired set with prune on would sweep the fleet.
var ErrNoTreeReader = errors.New("cloud: no tree reader registered")

var treeReader TreeFunc

// RegisterTreeFunc installs the reader. Called once at Mount by whichever side
// can answer — the git plane in-process, or a ZAP client when split.
func RegisterTreeFunc(fn TreeFunc) { treeReader = fn }

// ReadTree returns the files a glob selects at a revision.
func ReadTree(ctx context.Context, q TreeQuery) (Tree, error) {
	if treeReader == nil {
		return Tree{}, ErrNoTreeReader
	}
	return treeReader(ctx, q)
}
