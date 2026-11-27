package forge

// tree.go answers the two questions a reader of code has: which commit does
// this name mean, and what does the code SAY at it.
//
// Both are reads OF THE FORGE, for the reason repo.go states about refs: the
// forge holds the objects, so it is the only thing that can say what is in them.
// What is new here is that a caller no longer needs a working copy to find out.
// A renderer needs the bytes of some files at one commit and a language server
// needs the text of a tree at one commit; neither needs a packfile, a writable
// POSIX workdir, or a credential of its own on disk.
//
// # One request, not one per file
//
// The forge can answer a tree two ways. Listing it (/git/trees/{sha}?recursive)
// and then reading each blob is N+1 requests — for this deployment's monorepo,
// 4,800 of them — and N round trips is minutes of wall time whatever each one
// costs. The ARCHIVE endpoint answers the whole tree in ONE response, already
// compressed, so a whole-repository read costs one request and one transfer.
// That is why [Client.Files] is written on /archive and not on /git/trees.
//
// # The read is pinned to a commit, not to a name
//
// [Client.Files] RESOLVES first and then reads at the resolved sha. A read taken
// at `main` twice can straddle a push and assemble half its answer from each
// side of it; a read taken at a commit cannot. So the revision a caller is told
// about is the revision the bytes came from, by construction rather than by each
// caller remembering to pin one.
//
// # What is bounded, and why each bound is where it is
//
// The forge is trusted, but "trusted" is a statement about intent and not about
// compromise, and an archive is a stream this process decompresses into its own
// memory. [maxTree] bounds the DECOMPRESSED total (a bound on the socket would
// not see an expansion), [maxEntries] bounds the count (a million empty files
// costs no bytes), [maxBlob] bounds one file, and an entry whose path leaves the
// tree is refused outright — apps/lsp writes these paths to a disk.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
)

const (
	// maxBlob is the most of ONE file this reads. It is the same 1 MiB the browse
	// path applies (apps/git/browse.go maxBlobBytes), deliberately: a file that is
	// truncated for a human reading it must be truncated for a renderer too, or
	// "what does this repository contain" has two answers.
	maxBlob = 1 << 20

	// maxTree bounds the DECOMPRESSED bytes one archive may yield. This
	// deployment's largest repository is ~57 MB of tracked content, so 256 MiB is
	// four times the real ceiling — high enough that no honest repository meets
	// it, low enough that a crafted archive cannot take the process down.
	//
	// It also keeps the request inside the transport's own backstop ([New]'s 30s):
	// building and streaming a compressed archive of a repository this size is
	// seconds, and a repository large enough to need longer is one this bound
	// already refuses. The two numbers agree on purpose rather than by accident.
	maxTree = 256 << 20

	// maxEntries bounds how many entries one archive may carry. Entries cost
	// memory whether or not they carry bytes, so a count needs its own bound: the
	// monorepo has ~4,800 files, and 100,000 is past any repository that is still
	// a repository.
	maxEntries = 100_000
)

// File is one file of a repository at one commit.
//
// Truncated marks a file LISTED BUT NOT READ, because it is larger than
// [maxBlob]: its Data is absent, and a consumer assembling a COMPLETE set must
// refuse the whole read rather than proceed without it. Delivery does exactly
// that — a manifest it could not read is an object it would then prune.
type File struct {
	Path string
	Data []byte
	// Size is the file's length in the repository, read or not.
	Size      int64
	Truncated bool
}

// Tree is a repository's files at a RESOLVED commit. Rev is never empty on a
// successful read: a tree with no commit behind it is what makes an empty
// desired set indistinguishable from a failed one.
type Tree struct {
	Rev   string
	Files []File
}

// Resolve turns what a caller named — a branch, a tag, a commit, or nothing —
// into the immutable sha it names.
//
// It is the whole of cache invalidation for anything built on it: a branch
// moves, a commit never does, so a consumer that keys its work by the resolved
// sha never has to ask whether what it holds is still current.
//
// The read is made as THIS CLIENT'S ACTOR, so a repository the caller cannot see
// resolves to nothing rather than to a commit they may not read.
func (c *Client) Resolve(ctx context.Context, owner, repo, ref string) (string, error) {
	if err := validSegment(owner); err != nil {
		return "", err
	}
	if err := validSegment(repo); err != nil {
		return "", fmt.Errorf("forge: repo: %w", err)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		// HEAD is the repository's own default branch and the forge resolves it.
		// Reading the repository first to learn default_branch would be a round
		// trip spent on something the next request answers anyway.
		ref = "HEAD"
	}
	if err := validRef(ref); err != nil {
		return "", err
	}

	var sha string
	var err error
	if strings.Contains(ref, "/") {
		// A slashed name cannot travel through /git/commits/{sha}: that route is a
		// single segment, and escaping the slash sends a ref nobody has. The branch
		// route IS a wildcard, so a slashed ref is asked for there — which is also
		// the only place it can be, since a slash is legal in a branch name and
		// never appears in a sha.
		sha, err = c.branch(ctx, owner, repo, ref)
	} else {
		var out struct {
			SHA string `json:"sha"`
		}
		// stat, verification and files OFF. The whole answer wanted is one sha, and
		// the defaults attach the commit's diff — which for a merge of a large
		// branch is megabytes, on a call apps/lsp makes on EVERY request.
		q := url.Values{"stat": {"false"}, "verification": {"false"}, "files": {"false"}}
		err = c.do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+
			"/git/commits/"+url.PathEscape(ref), q, &out)
		sha = strings.TrimSpace(out.SHA)
	}
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "", fmt.Errorf("forge: %s/%s@%s names no commit", owner, repo, ref)
	}
	if !hex(sha) {
		// The forge's own answer, checked before it is interpolated into the next
		// request's path. It costs one comparison and it is what stops a forge that
		// has been tampered with from choosing which endpoint we call next.
		return "", fmt.Errorf("forge: %s/%s@%s resolved to %q, which is not a commit", owner, repo, ref, sha)
	}
	return sha, nil
}

// Ref is one git reference and the object it names.
type Ref struct {
	// Name is the whole reference, refs/tags/v1.2.3.
	Name string
	// SHA is the object it names: the commit, for a branch or a lightweight tag;
	// the tag object, for an annotated tag, which [Client.Peel] reads through.
	SHA string
	// Annotated says SHA is a tag object rather than a commit.
	Annotated bool
}

// Refs lists owner/repo's references whose names begin with prefix, as this
// client's actor, in ONE request: an annotated tag is listed as the tag object
// it names, and [Client.Peel] reads through only the ones a caller keeps.
// prefix is under refs/ and written without it ("tags/v", "tags/smoked/"); a
// prefix nothing matches is an empty list, not an error.
func (c *Client) Refs(ctx context.Context, owner, repo, prefix string) ([]Ref, error) {
	if err := validSegment(owner); err != nil {
		return nil, err
	}
	if err := validSegment(repo); err != nil {
		return nil, fmt.Errorf("forge: repo: %w", err)
	}
	prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "refs/")
	if prefix == "" || strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\\?#% \t") {
		return nil, fmt.Errorf("%w: ref prefix %q", ErrBadName, prefix)
	}
	segs := strings.Split(prefix, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	var raw json.RawMessage
	err := c.do(ctx, gitPath(owner, repo)+"/refs/"+strings.Join(segs, "/"), nil, &raw)
	if isMissing(err) {
		// The forge answers 404 when nothing matches. As a person that is also its
		// answer for a repository they may not read, so the repository is read to
		// tell the two apart: readable means the prefix simply matches nothing.
		if _, rerr := c.Repo(ctx, owner, repo); rerr != nil {
			return nil, rerr
		}
		return []Ref{}, nil
	}
	if err != nil {
		return nil, err
	}
	// The forge answers ONE object, not a list, when a single reference matches
	// the prefix exactly.
	var refs []gitRef
	if t := strings.TrimSpace(string(raw)); strings.HasPrefix(t, "{") {
		var one gitRef
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, fmt.Errorf("forge: decode refs: %w", err)
		}
		refs = []gitRef{one}
	} else if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, fmt.Errorf("forge: decode refs: %w", err)
	}
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		switch {
		case !hex(r.Object.SHA):
			return nil, fmt.Errorf("forge: %s names %q, which is not an object", r.Ref, r.Object.SHA)
		case r.Object.Type == "commit", r.Object.Type == "tag":
			out = append(out, Ref{Name: r.Ref, SHA: r.Object.SHA, Annotated: r.Object.Type == "tag"})
		}
		// A reference to a tree or a blob names no commit, and is not listed.
	}
	return out, nil
}

// maxPeel bounds how many tag objects [Client.Peel] reads through: a tag of a
// tag is legal git, a chain of them is not a release.
const maxPeel = 4

// Peel is the commit a reference names, reading an annotated tag — and a tag of
// a tag — through to it. A reference that ends at anything but a commit is
// refused with [ErrBadName].
func (c *Client) Peel(ctx context.Context, owner, repo string, r Ref) (string, error) {
	sha, annotated := r.SHA, r.Annotated
	for i := 0; annotated; i++ {
		if i == maxPeel {
			return "", fmt.Errorf("%w: %s is a chain of more than %d tags", ErrBadName, r.Name, maxPeel)
		}
		var t struct {
			Object gitObject `json:"object"`
		}
		if err := c.do(ctx, gitPath(owner, repo)+"/tags/"+url.PathEscape(sha), nil, &t); err != nil {
			return "", err
		}
		switch t.Object.Type {
		case "commit", "tag":
		default:
			return "", fmt.Errorf("%w: %s names a %s, not a commit", ErrBadName, r.Name, t.Object.Type)
		}
		if !hex(t.Object.SHA) {
			return "", fmt.Errorf("forge: %s names %q, which is not an object", r.Name, t.Object.SHA)
		}
		sha, annotated = t.Object.SHA, t.Object.Type == "tag"
	}
	return sha, nil
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitRef struct {
	Ref    string    `json:"ref"`
	Object gitObject `json:"object"`
}

func gitPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/git"
}

// Files reads every file of owner/repo beneath prefix, at the commit ref names.
//
// prefix is a repo-relative DIRECTORY and empty means the whole tree. It is a
// prefix rather than a glob because the two things that ask — a manifest
// directory and a whole repository — are exactly what a prefix expresses, and a
// glob engine here would be a second, richer language for a question nobody
// asks in it.
//
// One resolve backs the whole read (see the file comment), and the returned
// Rev is that commit.
func (c *Client) Files(ctx context.Context, owner, repo, ref, prefix string) (Tree, error) {
	sha, err := c.Resolve(ctx, owner, repo, ref)
	if err != nil {
		return Tree{}, err
	}
	// The archive route is a wildcard (/archive/*), and the ref it carries is the
	// sha just resolved rather than the name that was asked for — so what is read
	// is what was resolved, and a push landing in between changes nothing.
	resp, err := c.open(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+
		"/archive/"+sha+".tar.gz", nil, "application/gzip")
	if err != nil {
		return Tree{}, err
	}
	defer resp.Body.Close()

	files, err := unpack(resp.Body, repo, prefix)
	if err != nil {
		return Tree{}, fmt.Errorf("forge: read %s/%s at %s: %w", owner, repo, sha, err)
	}
	return Tree{Rev: sha, Files: files}, nil
}

// Entry is one child of a directory, as the forge lists it.
type Entry struct {
	Name string
	Path string
	// Dir is a directory. Everything else — a file, a link, a submodule — is not,
	// and is what a reader opens rather than walks into.
	Dir bool
	// Size is a file's length; zero for a directory and a submodule.
	Size int64
}

// ErrNotDir and ErrNotFile are a path that names the other kind of entry: a
// directory where a file was asked for, or a file — or a submodule, which holds
// no bytes here — where a directory was.
var (
	ErrNotDir  = errors.New("forge: not a directory")
	ErrNotFile = errors.New("forge: not a file")
)

// content is one entry of the forge's contents-ext answer, the fields read of it.
type content struct {
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	Type    string  `json:"type"` // file | dir | symlink | submodule
	Size    int64   `json:"size"`
	Content *string `json:"content"`
	Target  *string `json:"target"`
}

// List is the children of dir at ref, one level down, directories first and each
// kind by name. An empty dir is the repository's root, and an empty ref its
// default branch.
//
// It is ONE request for the whole directory. The forge's contents-ext route
// answers a listing with each entry's size and kind and — unlike the classic
// contents route — reads no file's bytes and no last commit to do it, so a
// directory of a thousand files is one cheap answer rather than a thousand reads.
func (c *Client) List(ctx context.Context, owner, repo, ref, dir string) ([]Entry, error) {
	var out struct {
		File *content  `json:"file_contents"`
		Dir  []content `json:"dir_contents"`
	}
	route, q, err := contents(owner, repo, ref, dir)
	if err != nil {
		return nil, err
	}
	if err := c.do(ctx, route, q, &out); err != nil {
		return nil, err
	}
	if out.File != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotDir, dir)
	}
	entries := make([]Entry, 0, len(out.Dir))
	for _, e := range out.Dir {
		entries = append(entries, Entry{Name: e.Name, Path: e.Path, Dir: e.Type == "dir", Size: e.Size})
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return entries, nil
}

// Read is one file at ref: its bytes when it is at most [maxBlob], and listed but
// not read — Truncated, Size set, no Data — when it is larger, the cap [Files]
// and the browse path apply, so "what does this file say" has one answer. A
// link's bytes are its target, as git stores them.
//
// It asks twice, and the first answer decides the second: the forge sends a
// file's bytes only when asked, and asking for a file past the cap would carry
// megabytes this read then drops.
func (c *Client) Read(ctx context.Context, owner, repo, ref, name string) (File, error) {
	if name == "" {
		return File{}, fmt.Errorf("%w: the root is a directory", ErrNotFile)
	}
	route, q, err := contents(owner, repo, ref, name)
	if err != nil {
		return File{}, err
	}
	var meta struct {
		File *content `json:"file_contents"`
	}
	if err := c.do(ctx, route, q, &meta); err != nil {
		return File{}, err
	}
	f := meta.File
	switch {
	case f == nil || (f.Type != "file" && f.Type != "symlink"):
		return File{}, fmt.Errorf("%w: %s", ErrNotFile, name)
	case f.Type == "symlink":
		target := ""
		if f.Target != nil {
			target = *f.Target
		}
		return File{Path: name, Data: []byte(target), Size: int64(len(target))}, nil
	case f.Size > maxBlob:
		return File{Path: name, Size: f.Size, Truncated: true}, nil
	}
	q.Set("includes", "file_content")
	var whole struct {
		File *content `json:"file_contents"`
	}
	if err := c.do(ctx, route, q, &whole); err != nil {
		return File{}, err
	}
	if whole.File == nil || whole.File.Content == nil {
		// The forge keeps a cap of its own on the bytes it will send, and a file
		// it would not send is one this read did not read.
		return File{Path: name, Size: f.Size, Truncated: true}, nil
	}
	data, err := base64.StdEncoding.DecodeString(*whole.File.Content)
	if err != nil {
		return File{}, fmt.Errorf("forge: decode %s: %w", name, err)
	}
	return File{Path: name, Data: data, Size: int64(len(data))}, nil
}

// contents is the contents-ext route and query for one path at one ref. The path
// is checked the way an archive entry is ([inside]) and escaped a segment at a
// time, so its slashes stay separators and nothing in a name becomes one.
func contents(owner, repo, ref, name string) (string, url.Values, error) {
	if err := validSegment(owner); err != nil {
		return "", nil, err
	}
	if err := validSegment(repo); err != nil {
		return "", nil, fmt.Errorf("forge: repo: %w", err)
	}
	q := url.Values{}
	if ref != "" {
		if err := validRef(ref); err != nil {
			return "", nil, err
		}
		q.Set("ref", ref)
	}
	route := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/contents-ext"
	if name != "" {
		if !inside(name) {
			return "", nil, fmt.Errorf("%w: %q is not a path in the repository", ErrBadName, name)
		}
		segs := strings.Split(name, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		route += "/" + strings.Join(segs, "/")
	}
	return route, q, nil
}

// branch is the commit a branch points at, read as THIS client's actor.
//
// The branch travels UNESCAPED after /branches/ because the route is a wildcard
// (routers/api/v1/api.go registers "/branches/*"), so a slashed name like
// agent/x reaches the handler whole. Escaping it would send "agent%2Fx", which
// is a branch nobody has.
func (c *Client) branch(ctx context.Context, owner, repo, name string) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	err := c.do(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/branches/"+name, nil, &b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(b.Commit.ID), nil
}

// unpack turns the forge's tar.gz into the files beneath prefix.
func unpack(r io.Reader, repo, prefix string) ([]File, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer zr.Close()
	// The bound is on the DECOMPRESSED stream, because that is what lands in this
	// process's memory: a few compressed megabytes can expand without limit, and a
	// bound on the socket would never see it.
	//
	// It is a LimitedReader rather than io.LimitReader so that having spent the
	// budget is DISTINGUISHABLE from having reached the end. Cut mid-entry, tar
	// reports an unexpected EOF and the read fails — but an archive whose
	// end-of-archive marker sits exactly at the bound would end cleanly, and the
	// files past it would be missing from a read that returned success. A
	// truncated tree presented as a whole one is the same wrong answer the
	// truncation flag exists to prevent, so the budget is checked after the walk.
	lim := &io.LimitedReader{R: zr, N: maxTree}
	tr := tar.NewReader(lim)

	// git archive is invoked with --prefix=<repo>/ (Forgejo's PrefixArchiveFiles,
	// on by default), which roots every entry under one directory named for the
	// repository AND emits that directory as the archive's first entry. So the
	// prefix is not inferred from the paths — it is the repo name we asked for,
	// stripped only when the archive actually opens with it. A deployment that
	// turns the setting off simply does not open with it, and nothing is stripped.
	root := ""
	trimmed := strings.Trim(prefix, "/")

	var out []File
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive: %w", err)
		}
		if n >= maxEntries {
			return nil, fmt.Errorf("archive holds more than %d entries", maxEntries)
		}
		if n == 0 && hdr.Typeflag == tar.TypeDir && hdr.Name == repo+"/" {
			root = hdr.Name
			continue
		}
		name := hdr.Name
		if root != "" {
			rest, ok := strings.CutPrefix(name, root)
			if !ok {
				// One archive addressing two path spaces. Guessing which one the
				// entry meant is how a manifest ends up filed under a path that is
				// not where it lives.
				return nil, fmt.Errorf("archive entry %q is outside its own root %q", hdr.Name, root)
			}
			name = rest
		}
		// Regular files only. A directory carries no bytes, and a symlink is a
		// path this process would have to follow — apps/lsp writes these paths to
		// a disk, so a link out of the tree is a write wherever it points.
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if !inside(name) {
			return nil, fmt.Errorf("archive entry %q leaves the repository", hdr.Name)
		}
		if !under(name, trimmed) {
			continue
		}
		if hdr.Size > maxBlob {
			// LISTED BUT NOT READ. Its bytes still stream past — tar has to reach
			// the next header — so they still count against [maxTree], which is the
			// honest accounting: they were decompressed either way.
			out = append(out, File{Path: name, Size: hdr.Size, Truncated: true})
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxBlob))
		if err != nil {
			return nil, fmt.Errorf("archive: read %s: %w", name, err)
		}
		out = append(out, File{Path: name, Data: data, Size: hdr.Size})
	}
	if lim.N <= 0 {
		return nil, fmt.Errorf("archive expands past %d bytes", maxTree)
	}
	return out, nil
}

// inside refuses an entry whose path is not a place inside the repository.
//
// An absolute path, a path that climbs out with .., a Windows separator and an
// embedded NUL are each a way of naming somewhere else, and the consumer that
// materialises a tree on disk (apps/lsp's daemon) would name exactly there.
// Cleaning and comparing is the whole check: git emits already-clean relative
// paths, so anything that changes under path.Clean did not come from git.
func inside(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	return p != ".." && path.Clean(p) == p && !strings.HasPrefix(p, "../")
}

// under reports whether p is at or beneath dir, where an empty dir is the whole
// tree. dir is already trimmed of its slashes by the caller.
func under(p, dir string) bool {
	return dir == "" || p == dir || strings.HasPrefix(p, dir+"/")
}

// hex reports whether s is a bare object name — 40 characters for SHA-1, 64 for
// the SHA-256 repositories a forge may hold.
func hex(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// validRef refuses a ref that is not something the forge can be asked for.
//
// A ref reaches this package from an operator's configuration or from a caller's
// request, and it is interpolated into a URL PATH — so a value bearing "?", "#"
// or ".." addresses a different endpoint than the call site wrote, and one
// bearing "%" can smuggle a separator past the escaping. Refusing here means no
// call site can be the place that forgot; ".." is refused twice over, since it
// is also git's range operator and names a set of commits rather than one.
// The refusal wraps [ErrBadName]: a ref is a name the caller chose, so a
// surface can answer 400 rather than blame the forge.
func validRef(ref string) error {
	switch {
	case strings.TrimSpace(ref) == "":
		return fmt.Errorf("%w: empty ref", ErrBadName)
	case ref != strings.TrimSpace(ref):
		return fmt.Errorf("%w: ref %q has surrounding space", ErrBadName, ref)
	case strings.ContainsAny(ref, "?#%\\ \t\n\x00"):
		return fmt.Errorf("%w: ref %q is not addressable", ErrBadName, ref)
	case strings.Contains(ref, ".."):
		return fmt.Errorf("%w: ref %q is a range, not a revision", ErrBadName, ref)
	case strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasPrefix(ref, "-"):
		return fmt.Errorf("%w: ref %q is not a revision name", ErrBadName, ref)
	}
	return nil
}
