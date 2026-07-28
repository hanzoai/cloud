package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
)

// TestReadTreeSeam proves the delivery plane's read: one call returns the pinned
// revision, the inventory, and the bytes — with no clone, no working copy and no
// credential. This is the seam apps/deploy uses, so it is tested through
// cloud.ReadTree rather than the package func: registration at Mount is half of
// what has to work.
func TestReadTreeSeam(t *testing.T) {
	mountApp(t) // registers the tree reader

	st := mounted.Load()
	if _, err := coreCreate(st, context.Background(), "acme", "", createReq{Name: "universe"}); err != nil {
		t.Fatalf("create repo: %v", err)
	}

	bareAbs := st.State.storage.absRepoPath("acme", "", "universe")
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	for p, body := range map[string]string{
		"charts/app/values/hanzo/www.yaml": "image:\n  repository: ghcr.io/hanzoai/cloud-www\n",
		"charts/app/values/hanzo/iam.yaml": "image:\n  repository: ghcr.io/hanzoai/iam\n",
		"charts/app/README.md":             "not an inventory entry\n",
	} {
		if err := os.MkdirAll(filepath.Join(work, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, work, p, body)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "seed")
	gitRun(t, work, "push", "-q", bareAbs, "main:refs/heads/main")

	tree, err := cloud.ReadTree(context.Background(), cloud.TreeQuery{
		Org: "acme", Repo: "universe", Ref: "main", Glob: "charts/app/values/*/*.yaml",
	})
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	if len(tree.Rev) != 40 {
		t.Fatalf("rev not pinned: %q", tree.Rev)
	}
	if len(tree.Paths) != 2 {
		t.Fatalf("paths = %v, want the two values files", tree.Paths)
	}
	// Bytes arrive with the listing — one call, one consistent revision.
	got := string(tree.Files["charts/app/values/hanzo/www.yaml"])
	if got != "image:\n  repository: ghcr.io/hanzoai/cloud-www\n" {
		t.Fatalf("content = %q", got)
	}
	if _, ok := tree.Files["charts/app/README.md"]; ok {
		t.Fatal("glob leaked a file it did not select")
	}

	// A tenant that does not own the repo cannot read it — isolation is the git
	// plane's, and the seam must not become a way around it.
	if _, err := cloud.ReadTree(context.Background(), cloud.TreeQuery{
		Org: "other", Repo: "universe", Ref: "main", Glob: "**",
	}); err == nil {
		t.Fatal("cross-org ReadTree succeeded, want not-found")
	}

	// An unknown repo is an error, NEVER an empty tree. Delivery hands this set
	// to a pruning reconcile; "no files" and "no repo" must not look alike.
	if _, err := cloud.ReadTree(context.Background(), cloud.TreeQuery{
		Org: "acme", Repo: "nope", Ref: "main", Glob: "**",
	}); err == nil {
		t.Fatal("unknown repo returned a tree, want an error")
	}
}
