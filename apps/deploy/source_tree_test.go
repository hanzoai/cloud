package deploy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	zaprpc "github.com/zap-proto/go/rpc"

	"github.com/hanzoai/cloud/zapface"
)

// fakeGit serves ONE git reply over a Unix socket at the well-known path
// cloud.Dial resolves for the "git" app, so a render exercises the real
// transport it uses in production — frame encode, socket dial, frame decode —
// rather than a stubbed function that would prove none of it.
func fakeGit(t *testing.T, reply any, status int) {
	t.Helper()
	run := t.TempDir()
	t.Setenv("CLOUD_RUN_DIR", run)

	ln, err := net.Listen("unix", filepath.Join(run, "git.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(reply)
		frame := zaprpc.BuildResponse(uint32(status), 1, zapface.EncodeReply(zapface.Reply{
			OK: status < 300, Status: uint32(status), Result: body,
		}))
		w.Header().Set("Content-Type", "application/zap")
		_, _ = w.Write(frame)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// TestTreeSourceRender proves the no-clone source over the real socket
// transport: bytes in, objects plus the revision they came from out, with the
// revision GIT resolved rather than the ref that was asked for.
func TestTreeSourceRender(t *testing.T) {
	fakeGit(t, map[string]any{
		"rev": "9c955a4710000000000000000000000000000000",
		"files": []map[string]any{
			{"path": "infra/k8s/a.yaml", "encoding": "utf8", "content": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n"},
			{"path": "infra/k8s/nested/b.yaml", "encoding": "utf8", "content": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n"},
			{"path": "infra/k8s/kustomization.yaml", "encoding": "utf8", "content": "resources:\n  - a.yaml\n"},
			{"path": "infra/k8s/README.md", "encoding": "utf8", "content": "# not a manifest\n"},
		},
	}, http.StatusOK)

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
	fakeGit(t, map[string]any{
		"rev": "abc",
		"files": []map[string]any{
			{"path": "k8s/small.yaml", "encoding": "utf8", "content": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: s\n"},
			{"path": "k8s/huge.yaml", "encoding": "utf8", "truncated": true},
		},
	}, http.StatusOK)

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
	fakeGit(t, map[string]any{"rev": "", "files": []map[string]any{}}, http.StatusOK)
	if _, _, err := (treeSource{org: "hanzo", repo: "universe", ref: "main"}).render(context.Background()); err == nil {
		t.Fatal("render succeeded with no revision resolved")
	}
}

// TestTreeSourceUnreachableGitIsError proves an absent git plane surfaces as an
// error. "git is not running" and "the inventory is empty" must not look alike.
func TestTreeSourceUnreachableGitIsError(t *testing.T) {
	t.Setenv("CLOUD_RUN_DIR", t.TempDir())           // no git.sock
	t.Setenv("CLOUD_PEER_URL", "http://127.0.0.1:1") // and nothing listening remotely
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
