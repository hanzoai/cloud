// repository.go — the value at the centre of /v1/git: a Repository is a named,
// versioned content tree. Refs name revisions; a revision has a tree of paths;
// a path holds bytes. That is the whole model, and it is what every reader
// actually needs — the JSON browse surface (browse.go), the HTML twin (ui.go),
// and the code-intelligence feeder that hands content to /v1/code
// (index_on_push.go).
//
// Git is ONE implementation of this model, not the model itself. It is a very
// good one — a content-addressed Merkle DAG, so the store falls out for free —
// but "revision" is a commit sha only because git says so; another backend may
// number its revisions or name them. Nothing above this file may assume
// otherwise, which is why Revision is an opaque string and why no go-git type
// (*gogit.Repository, *object.Commit, plumbing.Hash) appears in this file or in
// any signature a consumer touches. Those types live behind the backend, in
// gitbackend.go.
//
// The protocol adapters are deliberately NOT expressed here. smart_http.go,
// ssh.go, pack.go and gitexec.go speak the git wire protocol and stream packs
// through the git CLI; they are git-by-definition and each sits at its own slot.
// Adding hg or svn means adding a backend beside gitbackend.go plus its own
// adapter — it does not mean touching a single reader.
package git

import (
	"context"
	"errors"
	"time"
)

// Revision identifies one immutable state of a Repository. It is OPAQUE:
// consumers pass it back to the Repository that produced it and never parse it.
// The git backend fills it with a commit sha; that is an implementation detail
// and the reason this is a distinct type rather than a bare string.
type Revision string

// String renders a revision for display and for JSON. Callers that want a short
// form use ShortRev, which is display-only and must never be fed back as input.
func (r Revision) String() string { return string(r) }

// Ref is a human-facing name — a branch or a tag — bound to the revision it
// currently points at.
type Ref struct {
	Name string
	Rev  Revision
}

// Entry is one immediate child of a directory in a revision's tree.
// Dir distinguishes a subtree from a file; Mode is the backend's own permission
// string (octal for git), carried through for display only.
type Entry struct {
	Name string
	Path string
	Dir  bool
	Size int64
	Mode string
}

// Blob is the content of one path at one revision. Binary reports that the
// bytes are not valid text; Truncated reports that the file exceeded the caller's
// limit, in which case Content is empty — a large file is offered as a clone,
// never as megabytes of JSON.
type Blob struct {
	Path      string
	Size      int64
	Content   []byte
	Binary    bool
	Truncated bool
}

// Change is one entry of a revision's history.
type Change struct {
	Rev         Revision
	Message     string
	AuthorName  string
	AuthorEmail string
	When        time.Time
}

// The read model. Every method is safe to call on an empty repository: an
// unborn tree answers ErrNoRevision rather than panicking, which is what lets
// Refs report an empty-but-valid repo instead of a 500.
type Repository interface {
	// Refs lists branches and tags, each sorted by name.
	Refs(ctx context.Context) (branches, tags []Ref, err error)

	// Resolve turns a ref name into a revision. An empty name means the
	// repository's own default (HEAD, else the configured default branch). It
	// returns the revision plus the label that was actually used, so a caller
	// can echo "which branch am I looking at" without re-deriving it.
	Resolve(ctx context.Context, ref string) (Revision, string, error)

	// Tree lists the immediate children of dir at rev, directories first then
	// files, each group sorted by name. The root is "".
	Tree(ctx context.Context, rev Revision, dir string) ([]Entry, error)

	// Blob reads one path at rev. A file larger than maxBytes comes back with
	// Truncated set and no content; maxBytes <= 0 means no limit.
	Blob(ctx context.Context, rev Revision, path string, maxBytes int64) (Blob, error)

	// Log walks history from rev, newest first, at most limit entries. A
	// non-empty path restricts the walk to changes touching that path.
	Log(ctx context.Context, rev Revision, path string, limit int) ([]Change, error)

	// DefaultBranch reports the branch the repository points HEAD at.
	DefaultBranch(ctx context.Context) (string, error)

	// WalkText visits every TEXT file in rev's tree. Binary files are skipped —
	// the one consumer is code intelligence, which indexes source, not blobs.
	// A file larger than maxFileBytes is skipped WITHOUT being read, so a huge
	// blob never lands in memory (maxFileBytes <= 0 means no limit); this is why
	// the cap belongs here and not in the callback. Returning an error from fn
	// stops the walk and surfaces that error; returning StopWalk stops it cleanly.
	WalkText(ctx context.Context, rev Revision, maxFileBytes int64, fn func(path, content string) error) error
}

// Errors a Repository returns, so handlers can map to HTTP without knowing
// which backend produced them. Before this, browse.go turned ANY error from
// resolveRef into 404 "unknown ref", which quietly reported a broken repository
// as a missing branch.
var (
	// ErrNoRepository — the repository does not exist, or has no object store yet.
	ErrNoRepository = errors.New("git: no such repository")
	// ErrNoRevision — the ref or revision does not resolve. An empty repository
	// with no commits answers this too.
	ErrNoRevision = errors.New("git: no such revision")
	// ErrNoPath — the path does not exist at that revision.
	ErrNoPath = errors.New("git: no such path")
	// StopWalk ends a WalkText early without being an error.
	StopWalk = errors.New("git: stop walk")
)

// ShortRev is the abbreviated form used for display. It is a projection, never
// an identifier: pass the full Revision back to the Repository. Seven characters,
// matching the shortSha the browse API has always emitted.
func ShortRev(r Revision) string {
	if len(r) >= 7 {
		return string(r[:7])
	}
	return string(r)
}
