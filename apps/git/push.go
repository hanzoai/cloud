package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	gogitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// A linked repository's push goes to its host (link.go send), and fires nothing
// here: its host's own events are its builds.
//
// push.go adds POST /v1/git/repos/:name/push — a client-less push. The hanzo.app
// builder (and any caller without a local git binary) posts a set of files and
// the server builds the tree + commit, updates the branch ref, and fires the
// EXACT same push-to-deploy hook a real receive-pack fires (smart_http.go
// firePushBuilds), so a client-less push is indistinguishable downstream from a
// `git push`.
//
// It is org-scoped identically to the other routes (principal.Org → X-Org-Id).
// It composes the SAME provision() a create does when the repo is absent, so
// there is one way a repo comes into being.

// pushFile is one file in a client-less push.
type pushFile struct {
	// Path is repo-relative. Absolute or traversing paths, and paths inside .git,
	// are refused. A path that changes CI (.hanzo/workflows, .github/workflows,
	// .forgejo, .gitea) needs an org admin.
	Path string `json:"path"`
	// Content is the file's bytes, carried per Encoding.
	Content string `json:"content"`
	// Encoding is "base64", or "utf-8" (the default, also "utf8" / "text").
	Encoding string `json:"encoding"`
}

// pushReq is a client-less push: a set of files landed as one commit.
type pushReq struct {
	// Name is the repo to push into, from the :name path segment. It is CREATED
	// on first push if it does not exist.
	Name string `json:"name"`
	// Branch to advance; empty means "main". A fresh branch that is the repo's
	// first also becomes HEAD.
	Branch string `json:"branch"`
	// Message is the commit message; empty gets a generated one.
	Message string `json:"message"`
	// Files are added to or overwritten on the branch tip — files already there
	// and not listed SURVIVE. At least one, at most 5000, 32 MiB each.
	Files []pushFile `json:"files"`
}

// pushResp reports the commit a client-less push landed.
type pushResp struct {
	// Commit is the new commit's full hash.
	Commit string `json:"commit"`
	// Branch is the branch that was advanced, resolved (never empty).
	Branch string `json:"branch"`
	// CloneURL is the repo's HTTPS remote.
	CloneURL string `json:"cloneUrl"`
	// SSHURL is the repo's scp-style SSH remote.
	SSHURL string `json:"sshUrl"`
}

// maxPushFiles / maxPushFileBytes bound a single client-less push so a hostile
// body cannot exhaust memory. A real monorepo push uses git-receive-pack (chunked
// negotiation) — this endpoint is for the builder's generated-file set.
const (
	maxPushFiles     = 5000
	maxPushFileBytes = 32 << 20 // 32 MiB per file
)

// pushFiles lands a set of files as one commit without a git client — the
// hanzo.app builder's push. The repo is CREATED on first push, the files are
// merged onto the branch tip (unlisted files survive), and the same
// push-to-deploy hook a real receive-pack fires is fired, so downstream this is
// indistinguishable from a `git push`.
//
// It is for generated content. Changing CI through it needs an org admin (403
// otherwise), and a workflow whose newest change came in through it is not run
// automatically; CI written through git runs as usual.
//
// Example: {"name": "widgets", "branch": "main", "message": "generated build",
//
//	"files": [{"path": "index.html", "content": "<h1>hi</h1>"}]}
//
//	Response: {"commit": "a1b2c3d4e5f6", "branch": "main",
//		"cloneUrl": "https://api.hanzo.ai/v1/git/acme/widgets.git",
//		"sshUrl": "git@git.hanzo.ai:acme/widgets.git"}
func (o ops) pushFiles(ctx context.Context, in *pushReq) (*pushResp, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	// Normalize in place: the request value IS the core's input, so there is one
	// repo name in play and no second, unnormalized copy for the core to read.
	in.Name = normalizeName(in.Name)
	if in.Name == "" || !nameRE.MatchString(in.Name) {
		return nil, zip.ErrBadRequest("invalid repo name")
	}
	commit, branch, err := corePush(o.s, ctx, t.org, t.project, t.admin, *in)
	var he *zip.HTTPError
	switch {
	case errors.Is(err, errCI):
		return nil, zip.ErrForbidden(strings.TrimPrefix(err.Error(), "git: forbidden: "))
	case errors.Is(err, errBadInput):
		return nil, zip.ErrBadRequest(strings.TrimPrefix(err.Error(), "git: invalid input: "))
	case errors.Is(err, errMoved):
		return nil, zip.ErrConflict(strings.TrimPrefix(err.Error(), "git: "))
	case errors.As(err, &he):
		return nil, he
	case err != nil:
		return nil, internalErr(err)
	}
	var ssh string
	if store, err := storeFor(o.s, t.org); err == nil {
		if r, err := store.Get(ctx, t.org, t.project, in.Name); err == nil {
			ssh = sshURL(r)
		}
	}
	return &pushResp{
		Commit: commit, Branch: branch,
		CloneURL: cloneURL(o.s, t.org, t.project, in.Name), SSHURL: ssh,
	}, nil
}

// corePush is the transport-agnostic client-less push: it ensures the repo
// exists, materializes the files into a tree merged onto the branch tip, writes
// a commit, advances refs/heads/<branch> (and HEAD on a fresh branch), records
// usage, and fires the build hook. Returns the new commit hash + resolved branch.
// in.Name is the repo, already normalized and identifier-checked by the caller.
// admin is whether the caller may change the repository's CI (tenant.admin).
func corePush(s *cloud.Service[state], ctx context.Context, org, project string, admin bool, in pushReq) (commitHash, branch string, err error) {
	name := in.Name
	branch = strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = defaultBranchName
	}
	if !branchRE.MatchString(branch) {
		return "", "", badInput("branch must match ^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$")
	}
	if len(in.Files) == 0 {
		return "", "", badInput("files is required (at least one)")
	}
	if len(in.Files) > maxPushFiles {
		return "", "", badInput("too many files (max %d)", maxPushFiles)
	}

	// Decode + validate files first, so a bad file fails BEFORE we touch storage.
	blobs, err := decodePushFiles(in.Files)
	if err != nil {
		return "", "", err
	}
	if !admin {
		for _, b := range blobs {
			if ci(b.path) {
				return "", "", fmt.Errorf("%w: %s changes CI, which only an org admin may push here", errCI, b.path)
			}
		}
	}

	// Ensure the repo exists (create on first push, composing the ONE provision).
	store, err := storeFor(s, org)
	if err != nil {
		return "", "", fmt.Errorf("open store: %w", err)
	}
	r, gerr := store.Get(ctx, org, project, name)
	if errors.Is(gerr, errNotFound) {
		now := time.Now().Unix()
		r = Repo{ID: mint.ID("repo"), Org: org, Project: project, Name: name, DefaultBranch: branch, CreatedAt: now, UpdatedAt: now}
		if perr := provision(s, ctx, store, r); perr != nil && !errors.Is(perr, errConflict) {
			return "", "", fmt.Errorf("provision: %w", perr)
		}
		r, gerr = store.Get(ctx, org, project, name)
	}
	if gerr != nil {
		return "", "", fmt.Errorf("get: %w", gerr)
	}
	// A linked repository's commit is built on its host's tip, read through the
	// cache as the caller may read it, and goes up to the host (send below).
	k, linked := linkOf(r)
	if linked {
		_, done, err := materialize(ctx, s, r)
		if err != nil {
			return "", "", err
		}
		defer done()
	}

	// A repository kept in the store is written in one transaction: the blobs, the
	// trees, the commit and the branch land together or not at all, and the branch moves
	// by compare and set from the tip this push built on. Anything else (a linked
	// repository's cache, a directory that could not be brought in) is written in place.
	var st gogitstorage.Storer
	var tx *objects.Tx
	defaultBranch := ""
	objs, err := s.State.vault.home(ctx, s, r)
	if err != nil {
		return "", "", fmt.Errorf("open storer: %w", err)
	}
	if objs != nil {
		if tx, err = objs.Begin(ctx, r.ID); err != nil {
			return "", "", fmt.Errorf("open storer: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		st, defaultBranch = tx, headBranch(tx.Repo)
	} else {
		if st, err = s.State.storage.storer(org, project, name); err != nil {
			return "", "", fmt.Errorf("open storer: %w", err)
		}
		defaultBranch = defaultBranchOf(ctx, s.State.storage.absRepoPath(org, project, name))
	}

	// Resolve the current branch tip (parent commit + base tree), if any. The tip
	// is also the lifecycle "before" for this branch's advance.
	branchRef := plumbing.NewBranchReferenceName(branch)
	var parents []plumbing.Hash
	var before string
	var baseTree *object.Tree
	if ref, rerr := st.Reference(branchRef); rerr == nil && ref.Hash() != plumbing.ZeroHash {
		parents = append(parents, ref.Hash())
		before = ref.Hash().String()
		if pc, cerr := object.GetCommit(st, ref.Hash()); cerr == nil {
			if bt, terr := pc.Tree(); terr == nil {
				baseTree = bt
			}
		}
	}

	// Build the new root tree = base tree with the pushed files added/overwritten,
	// then write the commit.
	rootHash, err := writeTreeWithFiles(st, baseTree, blobs)
	if err != nil {
		return "", "", fmt.Errorf("build tree: %w", err)
	}
	msg := strings.TrimSpace(in.Message)
	if msg == "" {
		msg = "push via /v1/git/repos/" + name + "/push"
	}
	now := time.Now()
	commit := &object.Commit{
		Author:       object.Signature{Name: pushAuthor.Name, Email: pushAuthor.Email, When: now},
		Committer:    object.Signature{Name: pushAuthor.Name, Email: pushAuthor.Email, When: now},
		Message:      msg,
		TreeHash:     rootHash,
		ParentHashes: parents,
	}
	commitObj := st.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		return "", "", fmt.Errorf("encode commit: %w", err)
	}
	ch, err := st.SetEncodedObject(commitObj)
	if err != nil {
		return "", "", fmt.Errorf("store commit: %w", err)
	}

	// THE REF POLICY, on the client-less endpoint.
	//
	// This is the write Red walked through when the policy guarded only
	// receive-pack: one JSON POST, no git wire protocol, and a FAST-FORWARD CHILD
	// lands on the branch a reviewer just approved. Worse than a force-push,
	// because it is append-only and so trips no "the branch was rewritten"
	// signal — the reviewer approved A and merges A+B.
	//
	// The command is stated in the same value the wire endpoint parses out of
	// pkt-lines, and judged by the same function, so the two endpoints cannot come
	// to different answers about the same request. `before` is empty when the branch
	// is new, which is exactly what Creates() reads.
	//
	// It runs here, after the objects are written and before the ref moves,
	// because the new commit's hash is part of what is being asked. A refused
	// push therefore leaves loose objects behind, which the repo's own
	// housekeeping collects; it does NOT leave a ref.
	old := before
	if old == "" {
		old = zeroOID // the ref does not exist on our side: this is a create
	}
	if verr := checkRefPolicy([]refCommand{{Old: old, New: ch.String(), Ref: branchRef.String()}},
		defaultBranch, ""); verr != nil {
		return "", "", badInput("%s", verr.Error())
	}

	// A linked repository's branch moves on its host, without force, and nothing
	// moves here: the cache reads the commit on its next fetch, the next read.
	// Its builds are its host's to fire.
	if linked {
		if err := send(ctx, org, k, st, ch.String(), branchRef.String()); err != nil {
			return "", "", err
		}
		s.State.cache.stale(org, project, name)
		return ch.String(), branch, nil
	}

	// Advance the branch ref. On a brand-new branch that is the repo default,
	// point HEAD at it too (matching `git push` making the first branch the head).
	newRef := plumbing.NewHashReference(branchRef, ch)
	if tx != nil {
		var tip *plumbing.Reference
		if before != "" {
			tip = plumbing.NewHashReference(branchRef, plumbing.NewHash(before))
		}
		if err := setBranch(tx, newRef, tip); err != nil {
			return "", "", err
		}
	} else if err := st.SetReference(newRef); err != nil {
		return "", "", fmt.Errorf("update ref: %w", err)
	}
	// …but never point it at a machine branch. On a repository with no HEAD yet
	// the FIRST branch pushed becomes the default, and a run's branch becoming
	// the default is how unreviewed work turns into what a clone checks out —
	// and, since the deploy reactors gate on the default branch, into what
	// deploys. Same rule the importers apply to the HEAD they are handed.
	if _, herr := st.Reference(plumbing.HEAD); herr != nil && checkHeadRef(branchRef.String()) == nil {
		_ = st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branchRef))
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return "", "", fmt.Errorf("commit: %w", err)
		}
	}

	// Same side effects as a receive-pack push: meter storage + fire the reactions
	// for the one branch this push advanced (the ONE fireBranchBuild every push
	// uses). A client-less push has no gateway user, so the pusher is "".
	bg := context.WithoutCancel(ctx)
	recordUsage(s, bg, org, project, name)
	fireBranchBuild(s, bg, org, project, name, branch, before, ch.String(), "")

	return ch.String(), branch, nil
}

// blob is a decoded file ready to be written: its repo-relative path + content.
type blob struct {
	path    string
	content []byte
}

// decodePushFiles validates + decodes the request files (utf-8 default, or
// base64 when Encoding=="base64"), rejecting empty/absolute/traversing paths.
func decodePushFiles(files []pushFile) ([]blob, error) {
	out := make([]blob, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for i, f := range files {
		p := cleanRepoPath(f.Path)
		if p == "" {
			return nil, badInput("files[%d].path is invalid (empty, absolute, traversing, or inside .git)", i)
		}
		if _, dup := seen[p]; dup {
			return nil, badInput("files[%d].path %q is duplicated", i, p)
		}
		seen[p] = struct{}{}

		var content []byte
		switch strings.ToLower(strings.TrimSpace(f.Encoding)) {
		case "", "utf-8", "utf8", "text":
			content = []byte(f.Content)
		case "base64":
			b, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				return nil, badInput("files[%d].content is not valid base64", i)
			}
			content = b
		default:
			return nil, badInput("files[%d].encoding must be utf-8 or base64", i)
		}
		if len(content) > maxPushFileBytes {
			return nil, badInput("files[%d] exceeds %d bytes", i, maxPushFileBytes)
		}
		out = append(out, blob{path: p, content: content})
	}
	return out, nil
}

// cleanRepoPath normalizes a file path to a safe repo-relative slash path, or ""
// if it is empty, absolute, escapes the root, or names a .git directory. This is
// the traversal guard on the client-less push boundary.
func cleanRepoPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") {
		return ""
	}
	// Reject any traversal, empty segment, or .git segment.
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." || dotGit(seg) {
			return ""
		}
	}
	return p
}

// dotGit reports whether a path segment names a .git directory on some checkout:
// git's own rule, case-insensitive, with the trailing dots and spaces Windows
// drops and the 8.3 short name it answers to.
func dotGit(seg string) bool {
	return strings.EqualFold(strings.TrimRight(seg, ". "), ".git") || strings.EqualFold(seg, "git~1")
}

// ciDirs are where a repository's CI is declared: the runner here reads the
// first two (journal.go), and a forge the repository is mirrored to reads all
// four.
var ciDirs = []string{".hanzo/workflows", ".github/workflows", ".forgejo", ".gitea"}

// errCI refuses a file push that changes CI to a caller who does not administer
// the org. Transports answer 403.
var errCI = errors.New("git: forbidden")

// errMoved is a branch that moved while a client-less push was built on its old tip.
var errMoved = errors.New("git: branch moved")

// ci reports whether writing a file at p changes CI: p is inside a CI directory,
// is one, or is above one — a file placed at a directory's path replaces the
// directory, which deletes the workflows under it. Case is folded,
// since a checkout on a case-insensitive disk reads the two spellings as one.
func ci(p string) bool {
	p = strings.ToLower(p)
	for _, d := range ciDirs {
		if p == d || strings.HasPrefix(p, d+"/") || strings.HasPrefix(d, p+"/") {
			return true
		}
	}
	return false
}

// pushAuthor signs every commit this route writes. journal.go reads it back to
// know a workflow's newest change came in through here.
var pushAuthor = object.Signature{Name: "hanzo-cloud", Email: "git@hanzo.ai"}

// change is one directory's worth of a push: files written here, and the
// subdirectories that themselves change. A name is never in both.
type change struct {
	files map[string][]byte
	dirs  map[string]*change
}

func newChange() *change {
	return &change{files: map[string][]byte{}, dirs: map[string]*change{}}
}

// changesOf groups a push by directory. Later entries win, and a file and a
// directory of the same name cannot both survive: the one that arrives last
// replaces the other, which is what a checkout of the result would contain.
func changesOf(blobs []blob) *change {
	root := newChange()
	for _, b := range blobs {
		segs := strings.Split(b.path, "/")
		n := root
		for _, s := range segs[:len(segs)-1] {
			c, ok := n.dirs[s]
			if !ok {
				c = newChange()
				n.dirs[s] = c
			}
			delete(n.files, s)
			n = c
		}
		name := segs[len(segs)-1]
		n.files[name] = b.content
		delete(n.dirs, name)
	}
	return root
}

// writeTreeWithFiles builds the new root tree. Unchanged entries are copied by
// hash, so an edit rewrites the directories on the path to the file and no
// others, and an untouched executable or symlink keeps its mode. The previous
// form loaded every tree and wrote them all back as regular files.
func writeTreeWithFiles(st storer.EncodedObjectStorer, baseTree *object.Tree, blobs []blob) (plumbing.Hash, error) {
	return putChanged(st, baseTree, changesOf(blobs))
}

// putChanged writes the trees a change touches and returns the new tree hash.
func putChanged(st storer.EncodedObjectStorer, base *object.Tree, ch *change) (plumbing.Hash, error) {
	entries := make([]object.TreeEntry, 0)
	seen := map[string]struct{}{}
	if base != nil {
		for _, e := range base.Entries {
			seen[e.Name] = struct{}{}
			if content, ok := ch.files[e.Name]; ok {
				h, err := writeBlob(st, content)
				if err != nil {
					return plumbing.ZeroHash, err
				}
				entries = append(entries, object.TreeEntry{Name: e.Name, Mode: filemode.Regular, Hash: h})
				continue
			}
			if sub, ok := ch.dirs[e.Name]; ok {
				var child *object.Tree
				if e.Mode == filemode.Dir {
					var err error
					child, err = object.GetTree(st, e.Hash)
					if err != nil {
						return plumbing.ZeroHash, err
					}
				}
				h, err := putChanged(st, child, sub)
				if err != nil {
					return plumbing.ZeroHash, err
				}
				entries = append(entries, object.TreeEntry{Name: e.Name, Mode: filemode.Dir, Hash: h})
				continue
			}
			entries = append(entries, e)
		}
	}
	for name, content := range ch.files {
		if _, ok := seen[name]; ok {
			continue
		}
		h, err := writeBlob(st, content)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
	}
	for name, sub := range ch.dirs {
		if _, ok := seen[name]; ok {
			continue
		}
		h, err := putChanged(st, nil, sub)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
	}
	sort.Sort(object.TreeEntrySorter(entries))
	tree := &object.Tree{Entries: entries}
	obj := st.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return st.SetEncodedObject(obj)
}

// writeBlob stores a blob object and returns its hash.
func writeBlob(st storer.EncodedObjectStorer, content []byte) (plumbing.Hash, error) {
	obj := st.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return st.SetEncodedObject(obj)
}

// branchRE constrains a push branch name to a safe ref — nested (feature/foo)
// allowed, so it mirrors nameRE plus "/". The ref-update path uses it verbatim,
// so this is the traversal/injection guard on the branch name.
var branchRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// refRE accepts a FULL ref under heads or tags and nothing else, so an inbound
// event can never name refs/pull/*, a remote-tracking ref, or anything outside
// those two namespaces. The suffix carries branchRE's own shape, which is what
// rejects a traversal or an empty segment.
var refRE = regexp.MustCompile(`^refs/(heads|tags)/[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// setBranch moves a branch to next from tip (nil when the branch is new) by compare and
// set: a branch that moved since this push read it is a conflict, and nothing is kept.
func setBranch(tx *objects.Tx, next, tip *plumbing.Reference) error {
	var err error
	if tip == nil {
		err = tx.CreateReference(next)
	} else {
		err = tx.CheckAndSetReference(next, tip)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gogitstorage.ErrReferenceHasChanged):
		return fmt.Errorf("%w: %s moved while this push was built; push again", errMoved, next.Name().Short())
	}
	return fmt.Errorf("update ref: %w", err)
}
