package deploy

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"

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
// It reaches git through cloud.Dial — the one way an app calls another. Local
// resolves to a Unix socket, where the kernel proves the peer and no credential
// exists to mint or rotate; remote resolves to TLS. This file never says which,
// which is the point: git can move hosts and nothing here changes.
//
// Both sources produce the same pair — objects and the revision they came from —
// and both parse through parseManifest, so what counts as a manifest cannot
// drift between them.

// gitFile is one file of the inventory, mirroring git's fileJSON.
type gitFile struct {
	Path      string `json:"path"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// gitFiles is git's reply to the inventory read: the pinned revision and bytes.
type gitFiles struct {
	Rev   string    `json:"rev"`
	Files []gitFile `json:"files"`
}

// treeSource renders from the git plane's inventory read.
type treeSource struct {
	org  string // tenant that owns the repo, for error messages
	repo string // short repo name
	ref  string // branch/tag/sha; empty means the repo's default
	path string // repo-relative dir of manifests

	// who is the request this render serves, whose principal is delegated to
	// git so git scopes the answer to the SAME caller rather than trusting this
	// plane to have scoped it.
	//
	// Nil on the background reconcile loop, which has no request to delegate
	// from. That call reaches git with no identity and git refuses it — the
	// correct failure, and the one dial.go names as its remaining gap: a
	// background caller needs a real service credential, not a forwarded
	// header. Failing closed is right; rendering an unscoped or empty desired
	// set into a pruning reconcile is not.
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
	q := url.Values{"ref": {t.ref}, "glob": {glob}}
	route := fmt.Sprintf("GET /v1/git/repos/%s/files?%s", url.PathEscape(t.repo), q.Encode())

	var out gitFiles
	if err := cloud.Dial("git").As(t.who).Call(ctx, route, nil, &out); err != nil {
		return nil, "", fmt.Errorf("read %s/%s@%s: %w", t.org, t.repo, t.ref, err)
	}
	if out.Rev == "" {
		// No revision means no commit was resolved. Rendering an empty desired
		// set from that and handing it to a pruning reconcile would sweep the
		// fleet, so it is an error rather than "nothing to deploy".
		return nil, "", fmt.Errorf("read %s/%s@%s: git resolved no revision", t.org, t.repo, t.ref)
	}

	var objs []*unstructured.Unstructured
	for _, f := range out.Files {
		if !isManifestPath(f.Path) {
			continue // a glob selects files; it does not know which are manifests
		}
		if f.Truncated {
			// Listed but not read. An INCOMPLETE desired set deletes whatever
			// the missing file declared, so refuse it by name.
			return nil, "", fmt.Errorf("manifest %s at %s exceeds the read limit — desired set would be incomplete", f.Path, out.Rev)
		}
		data := []byte(f.Content)
		if f.Encoding == "base64" {
			b, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				return nil, "", fmt.Errorf("decode %s: %w", f.Path, err)
			}
			data = b
		}
		items, err := parseManifest(f.Path, data)
		if err != nil {
			return nil, "", err
		}
		objs = append(objs, items...)
	}
	return objs, out.Rev, nil
}
