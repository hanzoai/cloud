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
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

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
	// Path is repo-relative. Absolute or traversing paths are refused.
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
	commit, branch, err := corePush(o.s, ctx, t.org, t.project, *in)
	switch {
	case errors.Is(err, errBadInput):
		return nil, zip.ErrBadRequest(strings.TrimPrefix(err.Error(), "git: invalid input: "))
	case err != nil:
		return nil, internalErr(err)
	}
	return &pushResp{
		Commit: commit, Branch: branch,
		CloneURL: cloneURL(o.s, t.org, t.project, in.Name), SSHURL: sshURL(o.s, t.org, t.project, in.Name),
	}, nil
}

// corePush is the transport-agnostic client-less push: it ensures the repo
// exists, materializes the files into a tree merged onto the branch tip, writes
// a commit, advances refs/heads/<branch> (and HEAD on a fresh branch), records
// usage, and fires the build hook. Returns the new commit hash + resolved branch.
// in.Name is the repo, already normalized and identifier-checked by the caller.
func corePush(s *cloud.Service[state], ctx context.Context, org, project string, in pushReq) (commitHash, branch string, err error) {
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

	// Ensure the repo exists (create on first push, composing the ONE provision).
	store, err := storeFor(s, org)
	if err != nil {
		return "", "", fmt.Errorf("open store: %w", err)
	}
	if _, gerr := store.Get(ctx, org, project, name); errors.Is(gerr, errNotFound) {
		id, ierr := genID("repo")
		if ierr != nil {
			return "", "", fmt.Errorf("rng: %w", ierr)
		}
		now := time.Now().Unix()
		r := Repo{ID: id, Org: org, Project: project, Name: name, DefaultBranch: branch, CreatedAt: now, UpdatedAt: now}
		if perr := provision(s, ctx, store, r); perr != nil && !errors.Is(perr, errConflict) {
			return "", "", fmt.Errorf("provision: %w", perr)
		}
	} else if gerr != nil {
		return "", "", fmt.Errorf("get: %w", gerr)
	}

	st, err := s.State.storage.storer(org, project, name)
	if err != nil {
		return "", "", fmt.Errorf("open storer: %w", err)
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
		Author:       object.Signature{Name: "hanzo-cloud", Email: "git@hanzo.ai", When: now},
		Committer:    object.Signature{Name: "hanzo-cloud", Email: "git@hanzo.ai", When: now},
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

	// THE REF POLICY, on the client-less door.
	//
	// This is the write Red walked through when the policy guarded only
	// receive-pack: one JSON POST, no git wire protocol, and a FAST-FORWARD CHILD
	// lands on the branch a reviewer just approved. Worse than a force-push,
	// because it is append-only and so trips no "the branch was rewritten"
	// signal — the reviewer approved A and merges A+B.
	//
	// The command is stated in the same value the wire door parses out of
	// pkt-lines, and judged by the same function, so the two doors cannot come to
	// different answers about the same request. `before` is empty when the branch
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
		defaultBranchOf(ctx, s.State.storage.absRepoPath(org, project, name)), ""); verr != nil {
		return "", "", badInput("%s", verr.Error())
	}

	// Advance the branch ref. On a brand-new branch that is the repo default,
	// point HEAD at it too (matching `git push` making the first branch the head).
	newRef := plumbing.NewHashReference(branchRef, ch)
	if err := st.SetReference(newRef); err != nil {
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
			return nil, badInput("files[%d].path is invalid (empty, absolute, or traversing)", i)
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
// if it is empty, absolute, or escapes the root. This is the traversal guard on
// the client-less push boundary.
func cleanRepoPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	if p == "" || strings.HasPrefix(p, "/") {
		return ""
	}
	// Reject any traversal or empty segments.
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ""
		}
	}
	return p
}

// treeNode is a mutable tree used to accumulate blobs into a hierarchy before
// serializing bottom-up. subDirs keeps child directories; files keeps leaf blob
// hashes.
type treeNode struct {
	subDirs map[string]*treeNode
	files   map[string]plumbing.Hash
}

func newTreeNode() *treeNode {
	return &treeNode{subDirs: map[string]*treeNode{}, files: map[string]plumbing.Hash{}}
}

// writeTreeWithFiles builds the new root tree: it seeds from baseTree (so a push
// adds/overwrites onto the existing content), writes each file's blob, layers the
// files into a directory hierarchy, then serializes the trees bottom-up and
// returns the root tree hash.
func writeTreeWithFiles(st storer.EncodedObjectStorer, baseTree *object.Tree, blobs []blob) (plumbing.Hash, error) {
	root := newTreeNode()
	// Seed from the base tree so unchanged files survive the push.
	if baseTree != nil {
		if err := seedFromTree(st, root, baseTree, ""); err != nil {
			return plumbing.ZeroHash, err
		}
	}
	// Write each pushed blob and place it in the hierarchy (overwriting).
	for _, b := range blobs {
		h, err := writeBlob(st, b.content)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		placeFile(root, strings.Split(b.path, "/"), h)
	}
	return writeTreeNode(st, root)
}

// seedFromTree copies the base tree's entries into the mutable node hierarchy so
// a push merges onto existing content rather than replacing it.
func seedFromTree(st storer.EncodedObjectStorer, node *treeNode, t *object.Tree, prefix string) error {
	for _, e := range t.Entries {
		if e.Mode == filemode.Dir {
			sub, err := object.GetTree(st, e.Hash)
			if err != nil {
				return err
			}
			child := newTreeNode()
			node.subDirs[e.Name] = child
			if err := seedFromTree(st, child, sub, prefix+e.Name+"/"); err != nil {
				return err
			}
			continue
		}
		node.files[e.Name] = e.Hash
	}
	return nil
}

// placeFile inserts a file hash at the given path segments, creating intermediate
// directories. A file overwrites any prior file of the same name.
func placeFile(node *treeNode, segs []string, h plumbing.Hash) {
	if len(segs) == 1 {
		delete(node.subDirs, segs[0]) // a file replaces a dir of the same name
		node.files[segs[0]] = h
		return
	}
	dir := segs[0]
	child, ok := node.subDirs[dir]
	if !ok {
		child = newTreeNode()
		node.subDirs[dir] = child
	}
	delete(node.files, dir) // a dir replaces a file of the same name
	placeFile(child, segs[1:], h)
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

// writeTreeNode serializes a mutable tree node bottom-up (children first) into a
// git tree object and returns its hash. Entries are sorted per the git tree
// ordering contract (object.TreeEntrySorter).
func writeTreeNode(st storer.EncodedObjectStorer, node *treeNode) (plumbing.Hash, error) {
	tree := &object.Tree{}
	for name, h := range node.files {
		tree.Entries = append(tree.Entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
	}
	for name, child := range node.subDirs {
		ch, err := writeTreeNode(st, child)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		tree.Entries = append(tree.Entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: ch})
	}
	sort.Sort(object.TreeEntrySorter(tree.Entries))
	obj := st.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
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
