package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestConcurrentMaintenanceKeepsRepositoryReadable(t *testing.T) {
	root := t.TempDir()
	gitSource(t, root, "code", "one", "")
	bare := filepath.Join(root, "code.git")
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		workers.Go(func() {
			<-start
			autoMaintain(luxlog.New("test"), bare)
		})
		workers.Go(func() {
			<-start
			if err := runMaintenance(context.Background(), bare); err != nil {
				t.Errorf("concurrent maintenance: %v", err)
			}
		})
	}
	close(start)
	workers.Wait()
	gitRun(t, "", "--git-dir="+bare, "fsck", "--full")
	gitRun(t, "", "clone", "-q", bare, filepath.Join(t.TempDir(), "clone"))
}

// TestRunMaintenanceWritesBitmapAndCommitGraph proves the clone-speed artifacts
// are produced. A repo populated the way fetch/clone populates one — a pack, no
// reachability bitmap, no commit-graph (index-pack writes neither) — gets both
// after maintenance, and stays fully clonable through the repack.
func TestRunMaintenanceWritesBitmapAndCommitGraph(t *testing.T) {
	root := t.TempDir()
	gitSource(t, root, "code", "one", "") // <root>/code.git via clone --bare: a pack, no bitmap
	bare := filepath.Join(root, "code.git")

	if g, _ := filepath.Glob(filepath.Join(bare, "objects", "pack", "*.bitmap")); len(g) != 0 {
		t.Fatalf("precondition: bitmap present before maintenance: %v", g)
	}

	if err := runMaintenance(context.Background(), bare); err != nil {
		t.Fatalf("runMaintenance: %v", err)
	}

	if g, _ := filepath.Glob(filepath.Join(bare, "objects", "pack", "*.bitmap")); len(g) == 0 {
		t.Fatalf("maintenance wrote no reachability bitmap")
	}
	cg := filepath.Join(bare, "objects", "info", "commit-graph")
	cgs := filepath.Join(bare, "objects", "info", "commit-graphs")
	if _, err := os.Stat(cg); err != nil {
		if _, err2 := os.Stat(cgs); err2 != nil {
			t.Fatalf("maintenance wrote no commit-graph (%v / %v)", err, err2)
		}
	}

	dst := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", "clone", "-q", bare, dst)
	if gitOut(t, dst, "rev-parse", "HEAD") == "" {
		t.Fatalf("repo unclonable after maintenance")
	}
}

// TestAutoMaintainWritesBitmapAndCommitGraph is the background pass a push
// fires. The first call builds the bitmap a clone reuses; the second finds it
// and still refreshes the commit-graph merge-base reads.
func TestAutoMaintainWritesBitmapAndCommitGraph(t *testing.T) {
	root := t.TempDir()
	gitSource(t, root, "code", "one", "")
	bare := filepath.Join(root, "code.git")
	if hasBitmap(bare) {
		t.Fatal("precondition: bitmap already present")
	}
	autoMaintain(luxlog.New("test"), bare)
	if !hasBitmap(bare) {
		t.Fatal("autoMaintain wrote no bitmap")
	}
	cg := filepath.Join(bare, "objects", "info", "commit-graph")
	cgs := filepath.Join(bare, "objects", "info", "commit-graphs")
	if _, err := os.Stat(cg); err != nil {
		if _, err2 := os.Stat(cgs); err2 != nil {
			t.Fatalf("autoMaintain wrote no commit-graph (%v / %v)", err, err2)
		}
	}
	autoMaintain(luxlog.New("test"), bare)
}

// TestMaintainEndpoint proves the /gc control-plane endpoint repacks a pushed
// repo (200 + maintained) and 404s an unknown one — org-scoped like every repo op.
func TestMaintainEndpoint(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		if code, b := do(t, app, "POST", "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
			t.Fatalf("create repo: %d %s", code, b)
		}

		work := t.TempDir()
		gitRun(t, work, "init", "-q", "-b", "main")
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, work, "add", "-A")
		gitRun(t, work, "commit", "-q", "-m", "c")
		gitRun(t, work, "remote", "add", "origin", base+"/v1/git/acme/code.git")
		gitRun(t, work, append(orgHeaderArgs("acme"), "push", "origin", "main")...)

		code, b := do(t, app, "POST", "/v1/git/repos/code/gc", "acme", nil)
		if code != 200 {
			t.Fatalf("gc: %d %s", code, b)
		}
		if !strings.Contains(string(b), `"maintained":true`) {
			t.Fatalf("gc response missing maintained flag: %s", b)
		}

		if code, _ := do(t, app, "POST", "/v1/git/repos/nope/gc", "acme", nil); code != 404 {
			t.Fatalf("gc on missing repo: want 404, got %d", code)
		}
		// Cross-tenant: another org cannot gc acme's repo (org from principal, not path).
		if code, _ := do(t, app, "POST", "/v1/git/repos/code/gc", "other", nil); code != 404 {
			t.Fatalf("cross-tenant gc: want 404, got %d", code)
		}
	})
}
