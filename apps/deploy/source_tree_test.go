package deploy

import (
	"context"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

// fakeGit publishes a git.files method on the internal plane for one test, at
// the socket cloud.Dial resolves for the "git" app. It uses the REAL server —
// Expose plus Listen — so a render exercises the transport it uses in
// production: capability packing, frame out, frame in, payload codec. A stub
// standing in for that would prove none of it, and the codec is exactly where a
// silent mistake becomes a wrong desired set.
func fakeGit(t *testing.T, rev string, files []cloud.File, fault error) {
	t.Helper()
	t.Setenv("CLOUD_RUN_DIR", t.TempDir())

	cloud.Expose("git.files", func(_ context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		if fault != nil {
			return nil, fault
		}
		if who.Org == "" {
			return nil, cloud.Fault(403, "org required")
		}
		if _, _, _, err := cloud.FilesReq(req); err != nil {
			return nil, cloud.Fault(400, "bad request")
		}
		return cloud.PutFiles(rev, files), nil
	})

	ln, err := cloud.Listen("git", luxlog.New("gittest"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}

// TestTreeSourceRender proves the no-clone source over the real socket
// transport: bytes in, objects plus the revision they came from out, with the
// revision GIT resolved rather than the ref that was asked for.
func TestTreeSourceRender(t *testing.T) {
	fakeGit(t, "9c955a4710000000000000000000000000000000", []cloud.File{
		{Path: "infra/k8s/a.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")},
		{Path: "infra/k8s/nested/b.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n")},
		{Path: "infra/k8s/kustomization.yaml", Data: []byte("resources:\n  - a.yaml\n")},
		{Path: "infra/k8s/README.md", Data: []byte("# not a manifest\n")},
	}, nil)

	objs, rev, err := treeSource{org: "hanzo", repo: "universe", ref: "main", path: "infra/k8s"}.render(context.Background())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rev != "9c955a4710000000000000000000000000000000" {
		t.Fatalf("rev = %q, want the revision git resolved", rev)
	}
	// Nested manifests are included — a non-recursive read plus prune deletes
	// whatever the subdirectories declared.
	if len(objs) != 2 {
		got := []string{}
		for _, o := range objs {
			got = append(got, o.GetName())
		}
		t.Fatalf("objects = %v, want a and b only", got)
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
	fakeGit(t, "abc", []cloud.File{
		{Path: "k8s/small.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: s\n")},
		{Path: "k8s/huge.yaml", Truncated: true},
	}, nil)

	_, _, err := treeSource{org: "hanzo", repo: "universe", ref: "main", path: "k8s"}.render(context.Background())
	if err == nil {
		t.Fatal("render accepted an incomplete desired set")
	}
	if !strings.Contains(err.Error(), "huge.yaml") {
		t.Fatalf("error does not name the missing manifest: %v", err)
	}
}

// TestTreeSourceNoRevisionIsError proves a reply with no resolved revision is a
// failure, not "nothing to deploy". An empty desired set reaching a pruning
// reconcile sweeps the fleet, so the two must never look alike.
func TestTreeSourceNoRevisionIsError(t *testing.T) {
	fakeGit(t, "", nil, nil)
	if _, _, err := (treeSource{org: "hanzo", repo: "universe", ref: "main"}).render(context.Background()); err == nil {
		t.Fatal("render succeeded with no revision resolved")
	}
}

// TestTreeSourceUnreachableGitIsError proves an absent git plane surfaces as an
// error. "git is not running" and "the inventory is empty" must not look alike.
func TestTreeSourceUnreachableGitIsError(t *testing.T) {
	t.Setenv("CLOUD_RUN_DIR", t.TempDir()) // no git.sock, and there is no network fallback
	if _, _, err := (treeSource{org: "hanzo", repo: "universe"}).render(context.Background()); err == nil {
		t.Fatal("render succeeded with no git plane reachable")
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
		s := newSource(ref, "main", "k8s", nil)
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
		if s := newSource(ref, "main", "k8s", nil); !isClone(s) {
			t.Fatalf("newSource(%q) = %T, want gitSource", ref, s)
		}
	}
}

func isClone(s source) bool { _, ok := s.(gitSource); return ok }
