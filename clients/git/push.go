package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
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

type pushFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"` // "" | "utf-8" | "base64"
}

type pushReq struct {
	Branch  string     `json:"branch"`
	Message string     `json:"message"`
	Files   []pushFile `json:"files"`
}

type pushResp struct {
	Commit   string `json:"commit"`
	Branch   string `json:"branch"`
	CloneURL string `json:"cloneUrl"`
	SSHURL   string `json:"sshUrl"`
}

// maxPushFiles / maxPushFileBytes bound a single client-less push so a hostile
// body cannot exhaust memory. A real monorepo push uses git-receive-pack (chunked
// negotiation) — this endpoint is for the builder's generated-file set.
const (
	maxPushFiles     = 5000
	maxPushFileBytes = 32 << 20 // 32 MiB per file
)

// pushFiles handles POST /v1/git/repos/:name/push.
func pushFiles(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := normalizeName(c.Param("name"))
	if name == "" || !nameRE.MatchString(name) {
		return zip.ErrBadRequest("invalid repo name")
	}
	project := projectScope(c)

	var body pushReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	commit, branch, err := corePush(s, c.Context(), org, project, name, body)
	switch {
	case errors.Is(err, errBadInput):
		return zip.ErrBadRequest(strings.TrimPrefix(err.Error(), "git: invalid input: "))
	case err != nil:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return c.JSON(http.StatusOK, pushResp{
		Commit: commit, Branch: branch,
		CloneURL: cloneURL(s, org, name), SSHURL: sshURL(s, org, name),
	})
}

// corePush is the transport-agnostic client-less push: it ensures the repo
// exists, materializes the files into a tree merged onto the branch tip, writes
// a commit, advances refs/heads/<branch> (and HEAD on a fresh branch), records
// usage, and fires the build hook. Returns the new commit hash + resolved branch.
func corePush(s *cloud.Service[state], ctx context.Context, org, project, name string, in pushReq) (commitHash, branch string, err error) {
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

	// Advance the branch ref. On a brand-new branch that is the repo default,
	// point HEAD at it too (matching `git push` making the first branch the head).
	newRef := plumbing.NewHashReference(branchRef, ch)
	if err := st.SetReference(newRef); err != nil {
		return "", "", fmt.Errorf("update ref: %w", err)
	}
	if _, herr := st.Reference(plumbing.HEAD); herr != nil {
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
