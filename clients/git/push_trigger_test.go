package git

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hanzoai/cloud"
)

// A real push to refs/heads/main fires cloud.OnGitPush exactly once, with the
// org, repo, branch, tip commit, and the repo's canonical clone URL — the
// event the platform push builder resolves an app from.
func TestPushFiresBuildTrigger(t *testing.T) {
	var mu sync.Mutex
	var got []cloud.GitPushEvent
	cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) error {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	app := mountApp(t)
	base := liveServer(t, app)
	if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "code"}); code != http.StatusCreated {
		t.Fatalf("create repo want 201, got %d (%s)", code, body)
	}
	cloneURL := base + "/v1/git/acme/code.git"
	asTenant("acme")

	fs := memfs.New()
	local, err := gogit.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := local.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	f, err := fs.Create("README.md")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	_, _ = f.Write([]byte("# native\n"))
	_ = f.Close()
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	commit, err := wt.Commit("first", &gogit.CommitOptions{
		Author: &object.Signature{Name: "hanzo-dev", Email: "dev@hanzo.ai", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := local.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{cloneURL}}); err != nil {
		t.Fatalf("remote: %v", err)
	}
	if err := local.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/master:refs/heads/main"},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want exactly 1 push event, got %d: %+v", len(got), got)
	}
	ev := got[0]
	if ev.Org != "acme" || ev.Repo != "code" || ev.Ref != "refs/heads/main" || ev.Commit != commit.String() {
		t.Fatalf("unexpected event: %+v (want commit %s)", ev, commit)
	}
	if ev.CloneURL != "https://api.hanzo.test/v1/git/acme/code.git" {
		t.Fatalf("unexpected clone URL: %q", ev.CloneURL)
	}
}
