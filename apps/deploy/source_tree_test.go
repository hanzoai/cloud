package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// stubTree registers a tree reader for one test and restores the previous one.
// Testing through the seam is the point: delivery must render without importing
// the git plane, so the test does not either.
func stubTree(t *testing.T, fn cloud.TreeFunc) {
	t.Helper()
	cloud.RegisterTreeFunc(fn)
	t.Cleanup(func() { cloud.RegisterTreeFunc(nil) })
}

// TestTreeSourceRender proves the no-clone source: bytes in, objects plus the
// revision they came from out, with the revision the SEAM resolved rather than
// the ref that was asked for.
func TestTreeSourceRender(t *testing.T) {
	stubTree(t, func(_ context.Context, q cloud.TreeQuery) (cloud.Tree, error) {
		if q.Glob != "infra/k8s/**" {
			t.Fatalf("glob = %q, want the path's subtree", q.Glob)
		}
		return cloud.Tree{
			Rev:   "9c955a4710000000000000000000000000000000",
			Paths: []string{"infra/k8s/a.yaml", "infra/k8s/nested/b.yaml", "infra/k8s/kustomization.yaml", "infra/k8s/README.md"},
			Files: map[string][]byte{
				"infra/k8s/a.yaml":             []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"),
				"infra/k8s/nested/b.yaml":      []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n"),
				"infra/k8s/kustomization.yaml": []byte("resources:\n  - a.yaml\n"),
				"infra/k8s/README.md":          []byte("# not a manifest\n"),
			},
		}, nil
	})

	objs, rev, err := treeSource{org: "hanzo", repo: "universe", ref: "main", path: "infra/k8s"}.render(context.Background())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rev != "9c955a4710000000000000000000000000000000" {
		t.Fatalf("rev = %q, want the revision the seam resolved", rev)
	}
	// Nested manifests are included — a non-recursive read plus prune deletes
	// whatever the subdirectories declared.
	if len(objs) != 2 {
		names := []string{}
		for _, o := range objs {
			names = append(names, o.GetName())
		}
		t.Fatalf("objects = %v, want a and b only", names)
	}
	for _, o := range objs {
		if o.GetName() != "a" && o.GetName() != "b" {
			t.Fatalf("unexpected object %q — kustomization/README must not render", o.GetName())
		}
	}
}

// TestTreeSourceRefusesIncompleteSet pins the prune-safety property: a manifest
// listed but not returned means the desired set is missing objects, and handing
// that to a pruning reconcile deletes whatever the missing file declared.
func TestTreeSourceRefusesIncompleteSet(t *testing.T) {
	stubTree(t, func(_ context.Context, _ cloud.TreeQuery) (cloud.Tree, error) {
		return cloud.Tree{
			Rev:   "abc",
			Paths: []string{"k8s/small.yaml", "k8s/huge.yaml"},
			Files: map[string][]byte{"k8s/small.yaml": []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: s\n")},
		}, nil
	})

	_, _, err := treeSource{org: "hanzo", repo: "universe", ref: "main", path: "k8s"}.render(context.Background())
	if err == nil {
		t.Fatal("render accepted an incomplete desired set")
	}
	if !strings.Contains(err.Error(), "huge.yaml") {
		t.Fatalf("error does not name the missing manifest: %v", err)
	}
}

// TestTreeSourceUnavailableIsNotEmpty proves an unregistered reader surfaces as
// an error. "The git plane is not mounted" and "the inventory is empty" must
// never look alike to a reconcile that prunes.
func TestTreeSourceUnavailableIsNotEmpty(t *testing.T) {
	stubTree(t, nil)
	if _, _, err := (treeSource{org: "hanzo", repo: "universe"}).render(context.Background()); err == nil {
		t.Fatal("render succeeded with no tree reader registered")
	}
}

// TestIsManifestPath fixes the rule both sources share. It decides what a prune
// sees as absent, so the clone path and the tree path must answer identically.
func TestIsManifestPath(t *testing.T) {
	for path, want := range map[string]bool{
		"a.yaml":                true,
		"deep/nested/b.yml":     true,
		"c.json":                true,
		"kustomization.yaml":    false,
		"k8s/kustomization.yml": false,
		"README.md":             false,
		"values":                false,
		"Chart.YAML":            true, // extension match is case-insensitive
	} {
		if got := isManifestPath(path); got != want {
			t.Errorf("isManifestPath(%q) = %v, want %v", path, got, want)
		}
	}
}
