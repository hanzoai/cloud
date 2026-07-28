// browse.go — Hanzo Git's JSON read/browse surface: the machine-readable twin of
// the server-rendered UI (ui.go). The console repo-browser (hanzoai/console
// components/products/git) consumes THESE endpoints; ui.go serves the same data as
// HTML for a plain browser. Both read through the ONE Repository model
// (repository.go), so the two surfaces can never drift and neither knows which
// version-control system is underneath.
//
// Routes (JSON, org-scoped — distinct trailing segments that never shadow the
// :org/:repo smart-HTTP protocol routes):
//
//	GET /v1/git/repos/:name/refs                 → { branches, tags, default }
//	GET /v1/git/repos/:name/tree?ref&path        → { entries: [ {name,path,type,size,mode} ] }
//	GET /v1/git/repos/:name/files?ref&glob       → { rev, files: [ {path,content,…} ] }  (delivery inventory)
//	GET /v1/git/repos/:name/blob?ref&path        → { path,size,encoding,content,binary,truncated }
//	GET /v1/git/repos/:name/commits?ref&path&limit → { commits: [ {sha,shortSha,message,author*,date} ] }
//	GET /v1/git/repos/:name/readme?ref           → { path, content, encoding }
//
// ref + path ride as ?ref=&path= QUERY params (the UI's own convention), so a
// slashed branch (feature/x) and a nested path are always unambiguous. Isolation is
// identical to the rest of git: the org is the gateway-minted, IAM-validated
// X-Org-Id (org(c)); a repo outside the caller's org is simply not found.
package git

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// maxBlobBytes caps a single file-view response; a larger blob returns truncated=true
// with no content (the browser offers a clone/download instead of megabytes of JSON).
const maxBlobBytes = 1 << 20 // 1 MiB

// ---- JSON DTOs (mirror the console GitApi normalizers verbatim) ----

// refJSON is one named ref and the commit it points at.
type refJSON struct {
	// Name is the short ref name ("main", "v1.2.0"), not the full refs/… path.
	Name string `json:"name"`
	// SHA is the full commit hash the ref resolves to.
	SHA string `json:"sha"`
}

// refsJSON is a repo's ref advertisement for a browser.
type refsJSON struct {
	// Branches are the repo's heads; empty on a repo with no commits.
	Branches []refJSON `json:"branches"`
	// Tags are the repo's tags; empty when there are none.
	Tags []refJSON `json:"tags"`
	// Default is the branch name a caller gets when it asks for no ref.
	Default string `json:"default"`
}

// treeEntryJSON is one immediate child of a directory.
type treeEntryJSON struct {
	// Name is the entry's own name, no directory part.
	Name string `json:"name"`
	// Path is the entry's full repo-relative path.
	Path string `json:"path"`
	// Type is "tree" for a directory, "blob" for a file.
	Type string `json:"type"`
	// Size is the file's byte length; 0 for a directory.
	Size int64 `json:"size"`
	// Mode is the octal git file mode ("100644", "040000", "120000").
	Mode string `json:"mode"`
}

// treeJSON is one directory listing.
type treeJSON struct {
	// Entries are the immediate children, directories before files.
	Entries []treeEntryJSON `json:"entries"`
}

// blobJSON is one file at one revision.
type blobJSON struct {
	// Path is the file's repo-relative path.
	Path string `json:"path"`
	// Size is the file's byte length in the repo, whatever was returned below.
	Size int64 `json:"size"`
	// Encoding is how Content is carried: "utf8" verbatim, or "base64".
	Encoding string `json:"encoding"`
	// Content is the file's bytes, empty when Truncated.
	Content string `json:"content"`
	// Binary marks content git could not treat as text; it comes back base64.
	Binary bool `json:"binary"`
	// Truncated marks a file past the 1 MiB view cap. No content is sent —
	// clone the repo for it.
	Truncated bool `json:"truncated"`
}

// commitJSON is one commit as the history view reports it.
type commitJSON struct {
	// SHA is the full commit hash.
	SHA string `json:"sha"`
	// ShortSHA is the abbreviated hash a UI displays.
	ShortSHA string `json:"shortSha"`
	// Message is the commit's SUBJECT — its first line only.
	Message string `json:"message"`
	// AuthorName is the commit author's name.
	AuthorName string `json:"authorName"`
	// AuthorEmail is the commit author's email.
	AuthorEmail string `json:"authorEmail"`
	// Date is the author date, RFC 3339 UTC.
	Date string `json:"date"`
}

// fileJSON is one selected file and its bytes.
type fileJSON struct {
	// Path is the file's repo-relative path.
	Path string `json:"path"`
	// Size is the file's byte length in the repo.
	Size int64 `json:"size"`
	// Encoding is how Content is carried: "utf8" verbatim, or "base64".
	Encoding string `json:"encoding"`
	// Content is the file's bytes, empty when Truncated.
	Content string `json:"content"`
	// Truncated marks a file past the read cap; no content is sent. A caller
	// assembling a desired set must treat this as INCOMPLETE, never as empty.
	Truncated bool `json:"truncated"`
}

// filesJSON is the inventory a glob selected, at the revision it resolved to.
type filesJSON struct {
	// Rev is the full revision the ref resolved to — pin follow-up reads to it.
	Rev string `json:"rev"`
	// Files are the selected files, sorted by path. Directories are never
	// returned.
	Files []fileJSON `json:"files"`
}

// commitsJSON is one page of history.
type commitsJSON struct {
	// Commits are newest first.
	Commits []commitJSON `json:"commits"`
}

// readmeJSON is the repo's root README.
type readmeJSON struct {
	// Path is the file the README was found at (README.md, README, …).
	Path string `json:"path"`
	// Content is the file's text, verbatim and unrendered.
	Content string `json:"content"`
	// Encoding is always "utf8" — a README is text by definition.
	Encoding string `json:"encoding"`
}

// ---- typed inputs ----

// revRef addresses a repo at a revision: the shared In of the browse ops.
type revRef struct {
	// Name is the repo to read, from the :name path segment.
	Name string `json:"name"`
	// Ref is a branch, tag or commit; empty means the repo's HEAD.
	Ref string `json:"ref"`
}

// pathRef addresses a path inside a repo at a revision.
type pathRef struct {
	// Name is the repo to read, from the :name path segment.
	Name string `json:"name"`
	// Ref is a branch, tag or commit; empty means the repo's HEAD.
	Ref string `json:"ref"`
	// Path is repo-relative; empty is the tree root. Traversal is stripped.
	Path string `json:"path"`
}

// globRef addresses a SET of paths inside a repo at a revision.
type globRef struct {
	// Name is the repo to read, from the :name path segment.
	Name string `json:"name"`
	// Ref is a branch, tag or commit; empty means the repo's HEAD.
	Ref string `json:"ref"`
	// Glob selects files, matched segment by segment so `*` never crosses a `/`.
	// `**` matches zero or more whole segments.
	Glob string `json:"glob"`
}

// logRef addresses a history query.
type logRef struct {
	// Name is the repo to read, from the :name path segment.
	Name string `json:"name"`
	// Ref is the branch, tag or commit to walk back from; empty means HEAD.
	Ref string `json:"ref"`
	// Path narrows the history to commits touching it; empty walks the whole ref.
	Path string `json:"path"`
	// Limit caps the page. Anything not positive means 50; the cap is 100.
	Limit int `json:"limit"`
}

// ---- handlers ----

// browseTarget resolves the named repo, its Repository, and the requested ref
// ("" ⇒ HEAD/default) to a revision — the shared preamble for tree/blob/commits/
// readme. Mirrors the uiTree/uiBlob guard, scoped to the caller's tenant.
func browseTarget(s *cloud.Service[state], ctx context.Context, t tenant, name, ref string) (Repository, Revision, error) {
	r, found := findRepo(s, ctx, t.org, normalizeName(name))
	if !found {
		return nil, "", zip.ErrNotFound("repo not found")
	}
	repo, err := openRepository(s, r)
	if err != nil {
		return nil, "", zip.ErrNotFound("empty repository")
	}
	rev, _, err := repo.Resolve(ctx, strings.TrimSpace(ref))
	if err != nil {
		return nil, "", zip.ErrNotFound("unknown ref")
	}
	return repo, rev, nil
}

// browseRefs lists a repo's branches, tags and default branch — what a branch
// picker needs in one call. Unlike the other read ops it tolerates a repo with no
// commits: the ref sets come back empty and the default branch is still named.
//
// Example: {"name": "widgets"}
//
//	Response: {"branches": [{"name": "main", "sha": "a1b2c3d4"}], "tags": [],
//		"default": "main"}
func (o ops) browseRefs(ctx context.Context, in *repoRef) (*refsJSON, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	r, found := findRepo(o.s, ctx, t.org, normalizeName(in.Name))
	if !found {
		return nil, zip.ErrNotFound("repo not found")
	}
	out := refsJSON{Branches: []refJSON{}, Tags: []refJSON{}, Default: firstNonEmptyStr(r.DefaultBranch, defaultBranchName)}
	if repo, err := openRepository(o.s, r); err == nil {
		branches, tags, _ := repo.Refs(ctx)
		out.Branches, out.Tags = refsToJSON(branches), refsToJSON(tags)
	}
	return &out, nil
}

// browseTree lists the immediate children of one directory at one revision,
// directories before files. It does not recurse — walk down a level at a time.
//
// Example: {"name": "widgets", "ref": "main", "path": "cmd"}
//
//	Response: {"entries": [{"name": "server", "path": "cmd/server", "type": "tree",
//		"size": 0, "mode": "040000"}]}
func (o ops) browseTree(ctx context.Context, in *pathRef) (*treeJSON, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	repo, rev, err := browseTarget(o.s, ctx, t, in.Name, in.Ref)
	if err != nil {
		return nil, err
	}
	entries, err := repo.Tree(ctx, rev, cleanTreePath(in.Path))
	if err != nil {
		return nil, zip.ErrNotFound("no such directory")
	}
	out := make([]treeEntryJSON, 0, len(entries))
	for _, e := range entries {
		kind := "blob"
		if e.Dir {
			kind = "tree"
		}
		out = append(out, treeEntryJSON{Name: e.Name, Path: e.Path, Type: kind, Size: e.Size, Mode: e.Mode})
	}
	return &treeJSON{Entries: out}, nil
}

// browseFiles returns every file a glob selects at one revision, WITH its bytes
// and the revision they came from. It is the read a delivery generator makes:
// one call answers "what is the inventory at this commit, and what does it say",
// where listing and then fetching would be a request per file.
//
// Returning the resolved revision matters as much as the bytes. A generator that
// lists at `main` and then reads at `main` can straddle a push and assemble half
// its inventory from one commit and half from the next; resolving once makes the
// whole read consistent by construction.
//
// A file past the read cap comes back Truncated with no content rather than
// being dropped. A caller building a desired set has to know the difference
// between "this file is empty" and "this file was not read" — silently omitting
// it is how a pruning reconcile deletes what the missing file declared.
//
// Example: {"name": "universe", "ref": "main", "glob": "charts/app/values/*/*.yaml"}
func (o ops) browseFiles(ctx context.Context, in *globRef) (*filesJSON, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rev, files, err := coreFiles(o.s, ctx, t, in.Name, in.Ref, in.Glob)
	switch {
	case errors.Is(err, errBadInput):
		return nil, zip.ErrBadRequest("glob is required")
	case errors.Is(err, errNotFound):
		return nil, zip.ErrNotFound("repo, ref or revision not found")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	// The JSON surface carries bytes as text, or base64 when they are not valid
	// UTF-8. The internal plane carries them verbatim — a browser needs an
	// encoding, a peer does not.
	out := &filesJSON{Rev: rev, Files: make([]fileJSON, 0, len(files))}
	for _, f := range files {
		e := fileJSON{Path: f.Path, Size: int64(len(f.Data)), Encoding: "utf8", Truncated: f.Truncated}
		switch {
		case f.Truncated:
		case !utf8.Valid(f.Data):
			e.Encoding, e.Content = "base64", base64.StdEncoding.EncodeToString(f.Data)
		default:
			e.Content = string(f.Data)
		}
		out.Files = append(out.Files, e)
	}
	return out, nil
}

// browseBlob returns one file's bytes at one revision. Text comes back verbatim,
// binary comes back base64, and a file past the 1 MiB view cap comes back marked
// truncated with NO content — the client is expected to clone instead.
//
// Example: {"name": "widgets", "ref": "main", "path": "go.mod"}
//
//	Response: {"path": "go.mod", "size": 42, "encoding": "utf8",
//		"content": "module widgets\n", "binary": false, "truncated": false}
func (o ops) browseBlob(ctx context.Context, in *pathRef) (*blobJSON, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	repo, rev, err := browseTarget(o.s, ctx, t, in.Name, in.Ref)
	if err != nil {
		return nil, err
	}
	fp := cleanTreePath(in.Path)
	if fp == "" {
		return nil, zip.ErrBadRequest("path is required")
	}
	b, err := repo.Blob(ctx, rev, fp, maxBlobBytes)
	if err != nil {
		if errors.Is(err, ErrNoPath) {
			return nil, zip.ErrNotFound("no such file")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "read blob: %v", err)
	}
	out := blobJSON{Path: b.Path, Size: b.Size, Encoding: "utf8", Truncated: b.Truncated, Binary: b.Binary}
	switch {
	case b.Truncated:
		// no content — the client is expected to clone instead
	case b.Binary:
		out.Encoding = "base64"
		out.Content = base64.StdEncoding.EncodeToString(b.Content)
	default:
		out.Content = string(b.Content)
	}
	return &out, nil
}

// browseCommits walks a ref's history newest first, or one path's history when a
// path is given. There is no cursor: the page is the newest `limit` commits.
//
// Example: {"name": "widgets", "ref": "main", "limit": 2}
//
//	Response: {"commits": [{"sha": "a1b2c3d4e5f6", "shortSha": "a1b2c3d",
//		"message": "add the widget service", "authorName": "Ada",
//		"authorEmail": "ada@hanzo.ai", "date": "2026-07-01T10:00:00Z"}]}
func (o ops) browseCommits(ctx context.Context, in *logRef) (*commitsJSON, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	repo, rev, err := browseTarget(o.s, ctx, t, in.Name, in.Ref)
	if err != nil {
		return nil, err
	}
	limit := 50
	if in.Limit > 0 {
		limit = in.Limit
	}
	if limit > 100 {
		limit = 100
	}
	changes, err := repo.Log(ctx, rev, cleanTreePath(in.Path), limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "log: %v", err)
	}
	out := []commitJSON{}
	for _, cm := range changes {
		out = append(out, commitJSON{
			SHA:         cm.Rev.String(),
			ShortSHA:    ShortRev(cm.Rev),
			Message:     firstLine(cm.Message),
			AuthorName:  cm.AuthorName,
			AuthorEmail: cm.AuthorEmail,
			Date:        cm.When.UTC().Format(time.RFC3339),
		})
	}
	return &commitsJSON{Commits: out}, nil
}

// browseReadme returns the README at the tree root as plain text — unrendered, so
// the caller decides how to present it. A repo with no README is not found.
//
// Example: {"name": "widgets", "ref": "main"}
//
//	Response: {"path": "README.md", "content": "# widgets\n", "encoding": "utf8"}
func (o ops) browseReadme(ctx context.Context, in *revRef) (*readmeJSON, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	repo, rev, err := browseTarget(o.s, ctx, t, in.Name, in.Ref)
	if err != nil {
		return nil, err
	}
	name, content, ok := readmeAt(ctx, repo, rev)
	if !ok {
		return nil, zip.ErrNotFound("no readme")
	}
	return &readmeJSON{Path: name, Content: content, Encoding: "utf8"}, nil
}

// ---- shared read helpers ----

// refsToJSON projects the model's refs onto the wire shape the console expects.
func refsToJSON(refs []Ref) []refJSON {
	out := make([]refJSON, 0, len(refs))
	for _, r := range refs {
		out = append(out, refJSON{Name: r.Name, SHA: r.Rev.String()})
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
