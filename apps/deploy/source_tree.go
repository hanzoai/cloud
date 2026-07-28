package deploy

import (
	"context"
	"fmt"
	"sort"

	"github.com/hanzoai/cloud"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// source_tree.go is the desired-state source that does NOT clone.
//
// gitSource shallow-clones repo@ref with the git CLI, which needs a writable
// POSIX workdir on every reconcile and a credential for every source it reads.
// Neither is inherent to delivery: rendering needs the bytes of some files at
// one commit, and that is a tree read. Asking for exactly that is what lets the
// repository live on S3, removes the credential entirely when the git plane is
// co-mounted, and keeps a large repo from landing on local disk once per sync.
//
// Both sources produce the same pair — objects and the revision they came from —
// so the engine cannot tell them apart, and both parse through parseManifest so
// the rules for what counts as a manifest cannot drift between them.

// treeSource renders from the git object plane through cloud's tree seam.
type treeSource struct {
	org  string // tenant that owns the repo
	repo string // short repo name
	ref  string // branch/tag/sha; empty means the repo's default
	path string // repo-relative dir of manifests
}

// render reads every manifest beneath path at ref and returns (objects, revision).
//
// The revision is whatever the seam resolved, NOT the ref that was asked for:
// one resolve backs the whole read, so a push landing mid-reconcile cannot
// produce a desired set assembled from two commits.
func (t treeSource) render(ctx context.Context) ([]*unstructured.Unstructured, string, error) {
	glob := "**"
	if t.path != "" {
		glob = t.path + "/**"
	}
	tree, err := cloud.ReadTree(ctx, cloud.TreeQuery{
		Org: t.org, Repo: t.repo, Ref: t.ref, Glob: glob,
	})
	if err != nil {
		return nil, "", fmt.Errorf("read %s/%s@%s: %w", t.org, t.repo, t.ref, err)
	}

	// A file listed but not returned was too large to read. Refusing here is the
	// same instinct as refusing an empty desired set: an INCOMPLETE set handed
	// to a pruning reconcile deletes whatever the missing file declared.
	for _, p := range tree.Paths {
		if _, ok := tree.Files[p]; !ok && isManifestPath(p) {
			return nil, "", fmt.Errorf("manifest %s at %s exceeds the read limit — desired set would be incomplete", p, tree.Rev)
		}
	}

	// Filter through the SAME rule the clone path uses. A glob selects files; it
	// does not know which of them are manifests, so a README or a kustomization
	// input would otherwise reach the parser and fail the whole render.
	//
	// Sorted so a render is byte-stable across calls; map iteration is not.
	paths := make([]string, 0, len(tree.Files))
	for p := range tree.Files {
		if isManifestPath(p) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	var objs []*unstructured.Unstructured
	for _, p := range paths {
		items, err := parseManifest(p, tree.Files[p])
		if err != nil {
			return nil, "", err
		}
		objs = append(objs, items...)
	}
	return objs, tree.Rev, nil
}
