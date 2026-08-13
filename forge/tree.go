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
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
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
	Path      string
	Data      []byte
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
	if err := validOrg(owner); err != nil {
		return "", err
	}
	if err := validOrg(repo); err != nil {
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
			out = append(out, File{Path: name, Truncated: true})
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxBlob))
		if err != nil {
			return nil, fmt.Errorf("archive: read %s: %w", name, err)
		}
		out = append(out, File{Path: name, Data: data})
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
	return path.Clean(p) == p && !strings.HasPrefix(p, "../")
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
func validRef(ref string) error {
	switch {
	case strings.TrimSpace(ref) == "":
		return errors.New("forge: empty ref")
	case ref != strings.TrimSpace(ref):
		return fmt.Errorf("forge: ref %q has surrounding space", ref)
	case strings.ContainsAny(ref, "?#%\\ \t\n\x00"):
		return fmt.Errorf("forge: ref %q is not addressable", ref)
	case strings.Contains(ref, ".."):
		return fmt.Errorf("forge: ref %q is a range, not a revision", ref)
	case strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasPrefix(ref, "-"):
		return fmt.Errorf("forge: ref %q is not a revision name", ref)
	}
	return nil
}
