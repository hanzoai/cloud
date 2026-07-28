package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// source_tree.go is the desired-state source that does NOT clone.
//
// gitSource shallow-clones repo@ref with the git CLI, which needs a writable
// POSIX workdir on every reconcile and a credential for every source it reads.
// Neither is inherent to delivery: rendering needs the bytes of some files at
// one commit, and that is a tree read. Asking for exactly that is what lets the
// repository live on S3, and keeps a large repo off local disk once per sync.
//
// It reaches git through cloud.Dial — the one way an app calls another: native
// ZAP over a unix socket, where the kernel proves the peer and there is no
// credential to mint, rotate or leak. There is deliberately no network
// fallback, so "git is not running here" stays an answer rather than something
// a second transport papers over.
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
// `org/repo` is ours and is read as a tree.
//
// Deriving it removes the failure where a flag and a URL disagree — set the
// mode to native against a github.com URL and you get an error at reconcile
// time, or worse, an empty desired set. There is one value, and it decides.
func newSource(repo, ref, path string, who *zip.Ctx) source {
	if org, name, ok := nativeRepo(repo); ok {
		return treeSource{org: org, repo: name, ref: ref, path: path, who: who}
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

// treeSource renders from the git plane's inventory read.
type treeSource struct {
	org  string // tenant that owns the repo; forwarded so git scopes the answer
	repo string // short repo name
	ref  string // branch/tag/sha; empty means the repo's default
	path string // repo-relative dir of manifests

	// who is the request this render serves. Its principal is delegated to git,
	// so git applies its own rules to the SAME caller rather than trusting this
	// plane to have scoped anything.
	//
	// Nil on the background reconcile loop, which has no request to delegate
	// from. That path still names its tenant through org, so git scopes the
	// answer either way — the difference is only whether a user is attached.
	who *zip.Ctx
}

// render reads every manifest beneath path at ref and returns (objects, revision).
//
// The revision is what git resolved, NOT the ref that was asked for: one resolve
// backs the whole read, so a push landing mid-reconcile cannot produce a desired
// set assembled from two commits.
func (t treeSource) render(ctx context.Context) ([]*unstructured.Unstructured, string, error) {
	glob := "**"
	if t.path != "" {
		glob = t.path + "/**"
	}
	// As delegates the requesting principal so git applies its own rules to the
	// SAME caller; For names the tenant delivery acts for, which is what makes
	// the BACKGROUND reconcile work — it has no request to delegate from. The
	// explicit tenant wins, the old internal-call contract.
	p := cloud.Dial("git")
	if t.who != nil {
		p = p.As(t.who)
	}
	reply, err := p.For(t.org).Call(ctx, "git.files", cloud.PutFilesReq(t.repo, t.ref, glob))
	if err != nil {
		return nil, "", fmt.Errorf("read %s/%s@%s: %w", t.org, t.repo, t.ref, err)
	}
	rev, files, err := cloud.Files(reply)
	if err != nil {
		return nil, "", fmt.Errorf("read %s/%s@%s: %w", t.org, t.repo, t.ref, err)
	}
	if rev == "" {
		// No revision means no commit was resolved. Rendering an empty desired
		// set from that and handing it to a pruning reconcile would sweep the
		// fleet, so it is an error rather than "nothing to deploy".
		return nil, "", fmt.Errorf("read %s/%s@%s: git resolved no revision", t.org, t.repo, t.ref)
	}

	var objs []*unstructured.Unstructured
	for _, f := range files {
		if !isManifestPath(f.Path) {
			continue // a glob selects files; it does not know which are manifests
		}
		if f.Truncated {
			// Listed but not read. An INCOMPLETE desired set deletes whatever
			// the missing file declared, so refuse it by name.
			return nil, "", fmt.Errorf("manifest %s at %s exceeds the read limit — desired set would be incomplete", f.Path, rev)
		}
		items, err := parseManifest(f.Path, f.Data)
		if err != nil {
			return nil, "", err
		}
		objs = append(objs, items...)
	}
	return objs, rev, nil
}
