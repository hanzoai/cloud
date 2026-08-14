package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// source_tree.go is the desired-state source that does NOT clone.
//
// gitSource shallow-clones repo@ref with the git CLI, which needs a writable
// POSIX workdir on every reconcile and a credential for every source it reads.
// Neither is inherent to delivery: rendering needs the bytes of some files at
// one commit, and that is a tree read. Asking for exactly that is what lets the
// repository live on object storage, and keeps a large repo off local disk once
// per sync.
//
// It reads THE FORGE (git.hanzo.ai), which is where the manifests are — the
// projection has named that repository as this plane's source all along
// (deployManifestRepo, projection.go). Reading it through the forge's own API
// rather than by cloning it keeps both properties the tree read exists for: no
// workdir, and no clone credential on disk. The manifest repo is private, so a
// credential-free clone could not read it at all; the forge answers it under the
// deployment's own machine identity, held in KMS.
//
// Both sources produce the same pair — objects and the revision they came from —
// and both parse through parseManifest, so what counts as a manifest cannot
// drift between them.

// source is a desired-state renderer: objects, and the revision they came from.
// The engine cannot tell the two implementations apart, which is the point —
// where a repo is hosted is not a fact the reconcile loop should encode.
type source interface {
	render(ctx context.Context) ([]*unstructured.Unstructured, string, error)
}

// newSource picks a renderer from what the repo reference IS, not from a mode
// flag beside it. A URL is somewhere else and has to be cloned; a bare
// `org/repo` is ours and is read from the forge.
//
// Deriving it removes the failure where a flag and a URL disagree — set the
// mode to native against a github.com URL and you get an error at reconcile
// time, or worse, an empty desired set. There is one value, and it decides.
func newSource(repo, ref, path string) source {
	if org, name, ok := nativeRepo(repo); ok {
		return treeSource{org: org, repo: name, ref: ref, path: path}
	}
	return gitSource{repo: repo, ref: ref, path: path}
}

// nativeRepo splits a bare `org/repo` reference. Anything carrying a scheme or
// an SSH-style host is somewhere else, whatever it is named.
func nativeRepo(s string) (org, repo string, ok bool) {
	if s == "" || strings.Contains(s, "://") || strings.Contains(s, "@") {
		return "", "", false
	}
	org, repo, found := strings.Cut(strings.Trim(s, "/"), "/")
	if !found || org == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return org, strings.TrimSuffix(repo, ".git"), true
}

// treeSource renders from a forge tree read.
type treeSource struct {
	org  string // tenant that owns the repo, mapped to its forge namespace by forge.Owner
	repo string // short repo name
	ref  string // branch/tag/sha; empty means the repo's default
	path string // repo-relative dir of manifests
}

// read is the one call this source makes, held in a variable for the one thing a
// variable buys here: a test can drive a render without a KMS and a forge behind
// it. It is never reassigned in production — the only writer is a test, and the
// compiler holds the stand-in to the same signature.
var read = forgeTree

// forgeTree reads org/repo@ref beneath path, AS THE MACHINE.
//
// The machine, and not a person, because of what this read IS. The coordinate is
// the deployment's own configuration (DEPLOY_ENGINE_REPO, never a request field)
// and what comes back is the fleet's desired state — so an answer that varied
// with which SuperAdmin pressed the button would make the cluster's target
// depend on the operator, which is the one thing a desired-state system must not
// do. The background reconcile has no person to ask as at all.
//
// Two controls still stand between a caller and the forge: the route is
// SuperAdmin-only (guard, deploy.go), and the org reaches a forge namespace only
// through forge.Owner's CLOSED table — an unmapped tenant is refused rather than
// becoming a namespace by being spelled like one.
// The namespace is resolved BEFORE the credential: a tenant with no namespace on
// the forge is refused without spending a KMS read on it.
func forgeTree(ctx context.Context, org, repo, ref, path string) (forge.Tree, error) {
	owner, err := forge.Owner(org)
	if err != nil {
		return forge.Tree{}, err
	}
	c, err := forge.Dial(ctx, cloud.KMSPeer{})
	if err != nil {
		return forge.Tree{}, err
	}
	return c.Machine().Files(ctx, owner, repo, ref, path)
}

// render reads every manifest beneath path at ref and returns (objects, revision).
//
// The revision is the COMMIT the forge resolved, not the ref that was asked for:
// one resolve backs the whole read (forge.Client.Files), so a push landing
// mid-reconcile cannot produce a desired set assembled from two commits.
func (t treeSource) render(ctx context.Context) ([]*unstructured.Unstructured, string, error) {
	tree, err := read(ctx, t.org, t.repo, t.ref, t.path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s/%s@%s: %w", t.org, t.repo, t.ref, err)
	}

	var objs []*unstructured.Unstructured
	for _, f := range tree.Files {
		if !isManifestPath(f.Path) {
			continue // a directory selects files; it does not know which are manifests
		}
		if f.Truncated {
			// Listed but not read. An INCOMPLETE desired set deletes whatever
			// the missing file declared, so refuse it by name.
			return nil, "", fmt.Errorf("manifest %s at %s exceeds the read limit — desired set would be incomplete", f.Path, tree.Rev)
		}
		items, err := parseManifest(f.Path, f.Data)
		if err != nil {
			return nil, "", err
		}
		objs = append(objs, items...)
	}
	if len(objs) == 0 {
		// An empty desired set deletes every object this source manages, so a read
		// that resolved to no manifests is refused rather than reconciled. The cause
		// is almost always a wrong path or a read that returned nothing, not an
		// intent to remove everything — and "remove everything" is not a sentence a
		// source tree should be able to say by accident. Same fail-closed stance as
		// the truncation refusal above.
		return nil, "", fmt.Errorf("no manifests under %q in %s/%s at %s — refusing an empty desired set", t.path, t.org, t.repo, tree.Rev)
	}
	return objs, tree.Rev, nil
}
