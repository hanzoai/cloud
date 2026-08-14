package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/forge"
)

// asked records what the render actually requested of the forge. The tenancy
// property here is a property of the REQUEST — which namespace, which ref, which
// directory — so the tests assert on that and not only on what came back.
type asked struct {
	org, repo, ref, path string
}

// standIn points the tree read at a fixed answer for one test and gives back
// what the render asked for. The forge's own wire is pinned in forge/tree_test.go;
// what is proved here is what this source does with the answer.
func standIn(t *testing.T, tree forge.Tree, fault error) *asked {
	t.Helper()
	got := &asked{}
	prev := read
	read = func(_ context.Context, org, repo, ref, path string) (forge.Tree, error) {
		*got = asked{org: org, repo: repo, ref: ref, path: path}
		return tree, fault
	}
	t.Cleanup(func() { read = prev })
	return got
}

// TestTreeSourceRender proves the no-clone source: bytes in, objects plus the
// revision they came from out, with the revision the FORGE resolved rather than
// the ref that was asked for.
func TestTreeSourceRender(t *testing.T) {
	got := standIn(t, forge.Tree{
		Rev: "9c955a4710000000000000000000000000000000",
		Files: []forge.File{
			{Path: "infra/k8s/a.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")},
			{Path: "infra/k8s/nested/b.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n")},
			{Path: "infra/k8s/kustomization.yaml", Data: []byte("resources:\n  - a.yaml\n")},
			{Path: "infra/k8s/README.md", Data: []byte("# not a manifest\n")},
		},
	}, nil)

	objs, rev, err := treeSource{org: "hanzo", repo: "universe", ref: "main", path: "infra/k8s"}.render(context.Background())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rev != "9c955a4710000000000000000000000000000000" {
		t.Fatalf("rev = %q, want the revision the forge resolved", rev)
	}
	if *got != (asked{org: "hanzo", repo: "universe", ref: "main", path: "infra/k8s"}) {
		t.Fatalf("asked the forge for %+v", *got)
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

// TestTreeSourceRefusesTruncated pins the prune-safety property: a manifest
// listed but not read means the desired set is missing objects, and handing that
// to a pruning reconcile deletes whatever the missing file declared.
func TestTreeSourceRefusesTruncated(t *testing.T) {
	standIn(t, forge.Tree{
		Rev: "abc",
		Files: []forge.File{
			{Path: "k8s/small.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: s\n")},
			{Path: "k8s/huge.yaml", Truncated: true},
		},
	}, nil)

	_, _, err := treeSource{org: "hanzo", repo: "universe", ref: "main", path: "k8s"}.render(context.Background())
	if err == nil {
		t.Fatal("render accepted an incomplete desired set")
	}
	if !strings.Contains(err.Error(), "huge.yaml") {
		t.Fatalf("error does not name the missing manifest: %v", err)
	}
}

// TestTreeSourceRefusesEmpty pins the other half of the prune-safety property: a
// read that SUCCEEDS but resolves to no manifests is an empty desired set, which a
// pruning reconcile reads as "delete everything". Only non-manifest files present —
// a README, a kustomization — is the ordinary shape of a wrong path, and it must be
// an error, not an empty answer. This is the "manifest directory is empty" half that
// TestTreeSourceUnreadableIsError's comment names but a failed read never reaches.
func TestTreeSourceRefusesEmpty(t *testing.T) {
	standIn(t, forge.Tree{
		Rev: "abc",
		Files: []forge.File{
			{Path: "k8s/README.md", Data: []byte("# not a manifest\n")},
			{Path: "k8s/kustomization.yaml", Data: []byte("resources: []\n")},
		},
	}, nil)

	_, _, err := treeSource{org: "hanzo", repo: "universe", ref: "main", path: "k8s"}.render(context.Background())
	if err == nil {
		t.Fatal("render accepted an empty desired set")
	}
	if !strings.Contains(err.Error(), "empty desired set") {
		t.Fatalf("error does not name the empty set: %v", err)
	}
}

// TestTreeSourceUnreadableIsError proves a forge that will not answer surfaces as
// an error. "The forge is unreachable" and "the manifest directory is empty" must
// never look alike: an empty desired set reaching a pruning reconcile sweeps the
// fleet.
func TestTreeSourceUnreadableIsError(t *testing.T) {
	standIn(t, forge.Tree{}, context.DeadlineExceeded)
	_, _, err := treeSource{org: "hanzo", repo: "universe", ref: "main"}.render(context.Background())
	if err == nil {
		t.Fatal("render succeeded with no answer from the forge")
	}
	if !strings.Contains(err.Error(), "hanzo/universe@main") {
		t.Fatalf("error does not name the source it could not read: %v", err)
	}
}

// TestForgeTreeRefusesAnUnmappedTenant pins the tenancy control on the delivery
// read. The tree is read as the MACHINE — a site administrator on the forge — so
// the only thing deciding WHICH namespace it reaches is forge.Owner's closed
// table. An org that is not in it must be refused, never resolved to its own
// name, and refused BEFORE a credential is spent on it.
func TestForgeTreeRefusesAnUnmappedTenant(t *testing.T) {
	for _, org := range []string{"hanzoai", "acme", "admin", ""} {
		if _, err := forgeTree(context.Background(), org, "universe", "main", "k8s"); err == nil {
			t.Fatalf("org %q reached the forge", org)
		}
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

// TestNewSourcePicksByReference fixes the selection rule. It is derived from the
// repo value rather than a mode flag beside it, so a flag and a URL can never
// disagree — a disagreement there renders the wrong desired set, or none.
func TestNewSourcePicksByReference(t *testing.T) {
	native := map[string]struct{ org, repo string }{
		"hanzo/universe":     {"hanzo", "universe"},
		"/hanzo/universe/":   {"hanzo", "universe"},
		"hanzo/universe.git": {"hanzo", "universe"},
		"tenant-acme/deploy": {"tenant-acme", "deploy"},
	}
	for ref, want := range native {
		s := newSource(ref, "main", "k8s")
		got, ok := s.(treeSource)
		if !ok {
			t.Fatalf("newSource(%q) = %T, want treeSource", ref, s)
		}
		if got.org != want.org || got.repo != want.repo {
			t.Fatalf("newSource(%q) = %s/%s, want %s/%s", ref, got.org, got.repo, want.org, want.repo)
		}
	}
	// Anything carrying a scheme or an SSH host is somewhere else and is cloned,
	// including a URL whose tail looks like org/repo.
	for _, ref := range []string{
		"https://github.com/hanzoai/universe",
		"git@github.com:hanzoai/universe.git",
		"universe", // no org
		"a/b/c",    // not an org/repo pair
		"",
	} {
		if s := newSource(ref, "main", "k8s"); !isClone(s) {
			t.Fatalf("newSource(%q) = %T, want gitSource", ref, s)
		}
	}
}

func isClone(s source) bool { _, ok := s.(gitSource); return ok }
