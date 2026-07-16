package platform

// pushbuild_e2e_test.go proves the whole "cloud IS the git forge" seam in ONE
// process: a REAL `git push` over smart-HTTP to the embedded git forge
// (clients/git) drives the REAL platform push builder (buildFromPush) and enqueues
// a build for the app tracking that repo+branch.
//
// The two halves are each covered in isolation elsewhere — clients/git's
// TestPushFiresBuildTrigger proves a push reaches cloud.OnGitPush, and this
// package's TestBuildFromPush_LaunchesMatchingApp proves buildFromPush enqueues a
// deployment — but neither wires a live push through the real builder. This test
// connects them through the ACTUAL production seam (cloud.RegisterPushBuilder ⇄
// cloud.OnGitPush) so a regression on either side, or in the global inversion
// point between them, fails here.
//
// Wiring mirrors platform.Mount exactly: the forge is mounted on its own live
// server (its own store, its own Domain-derived clone URL); the platform Service
// registers buildFromPush as the cloud.PushBuilder; the two subsystems share
// NOTHING but the cloud globals and a matching clone URL — the same decoupling
// that lets one binary carry both. Pure-Go dev build (CGO_ENABLED=0) so the git
// store opens without a KMS master key.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hanzoai/cloud"
	gitforge "github.com/hanzoai/cloud/clients/git"
	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// e2ePushOrg carries the org the in-process go-git client sends. go-git v5.19's
// Push options have no per-call header hook, so a single guarded value plus a
// globally-installed header-injecting transport reproduces the gateway's
// X-Org-Id / X-User-Id (the validated-principal pair org() gates on) on every
// git request — the same idiom clients/git's own test harness uses.
var e2ePushOrg = struct {
	sync.Mutex
	org string
}{}

type e2eHeaderRT struct{ base http.RoundTripper }

func (h *e2eHeaderRT) RoundTrip(req *http.Request) (*http.Response, error) {
	e2ePushOrg.Lock()
	org := e2ePushOrg.org
	e2ePushOrg.Unlock()
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // Validated() gates on X-User-Id
	}
	return h.base.RoundTrip(req)
}

func init() {
	rt := &e2eHeaderRT{base: http.DefaultTransport}
	c := githttp.NewClient(&http.Client{Transport: rt, Timeout: 30 * time.Second})
	client.InstallProtocol("http", c)
}

// e2eLiveServer serves app over a real loopback TCP listener (fasthttp) so an
// in-process go-git client can push against the smart-HTTP endpoints.
func e2eLiveServer(t *testing.T, app *zip.App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	return "http://" + ln.Addr().String()
}

// TestPushToForgeEnqueuesBuild is the end-to-end proof: create a repo on the
// embedded forge, push a commit to it over smart-HTTP, and assert the matching
// app's build was enqueued — a "building" deployment for the exact pushed commit
// — with NO direct call into buildFromPush. Only the real push drives it.
func TestPushToForgeEnqueuesBuild(t *testing.T) {
	ctx := context.Background()

	// The forge's clone URL host is Domain-derived; the app's RepoURL and the
	// builder's self-git trust must name the same host for the push→build match.
	const gitHost = "git.hanzo.ai"

	// 1. Platform Service over a READY fake cluster, and register the REAL push
	//    builder exactly as platform.Mount does. This is the one production seam.
	_, s := mountSvcK8s(t, fakeK8s())
	prev := selfGitHost
	selfGitHost = gitHost
	t.Cleanup(func() { selfGitHost = prev })
	cloud.RegisterPushBuilder(func(ctx context.Context, ev cloud.GitPushEvent) error {
		return buildFromPush(s, ctx, ev)
	})
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	// 2. Mount the embedded git forge on its own app + live server. Ephemeral SSH
	//    port so it never collides with :2222.
	t.Setenv("GIT_SSH_ADDR", "127.0.0.1:0")
	gitApp := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := gitforge.Mount(gitApp, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Domain: gitHost}); err != nil {
		t.Fatalf("git.Mount: %v", err)
	}
	t.Cleanup(func() { _ = gitforge.Shutdown() })
	base := e2eLiveServer(t, gitApp)

	// 3. Seed a git-source app tracking git.hanzo.ai/v1/git/acme/code @ main.
	//    RepoURL omits the ".git" the clone URL carries — sameRepo must still match.
	a := seedGitApp(t, s, "acme", "code", "https://"+gitHost+"/v1/git/acme/code", "main")

	// 4. Create the repo on the forge (the push target must exist).
	e2eCreateRepo(t, base, "acme", "code")

	// 5. A REAL git push: local commit on master → remote refs/heads/main.
	e2ePushOrg.Lock()
	e2ePushOrg.org = "acme"
	e2ePushOrg.Unlock()
	commit := e2ePushCommit(t, base+"/v1/git/acme/code.git")

	// 6. The push is synchronous through receivePack → OnGitPush → buildFromPush →
	//    startGitBuild, so the enqueue has already landed. Exactly one "building"
	//    deployment for the pushed commit, sourced from git.
	deps, err := s.State.store.ListDeployments(ctx, "acme", a.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("want exactly 1 deployment enqueued by the push, got %d: %+v", len(deps), deps)
	}
	d := deps[0]
	if d.Status != "building" || d.Source != "git" || d.Commit != commit {
		t.Fatalf("unexpected deployment: %+v (want status=building source=git commit=%s)", d, commit)
	}

	// The app itself flipped to building — the same terminal state the HTTP deploy
	// path reaches, proving the push drove the identical build-launch core.
	got, err := s.State.store.GetApplicationByID(ctx, "acme", a.ID)
	if err != nil {
		t.Fatalf("reload app: %v", err)
	}
	if got.Status != "building" {
		t.Fatalf("app status: want building, got %q", got.Status)
	}
}

// e2eCreateRepo creates a repo through the forge's real REST create path. It uses
// net/http directly (not the go-git transport) with the validated-principal
// header pair, so it exercises the same org gate a gateway request would.
func e2eCreateRepo(t *testing.T, base, org, name string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name})
	req, err := http.NewRequest(http.MethodPost, base+"/v1/git/repos", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create repo want 201, got %d (%s)", resp.StatusCode, b)
	}
}

// e2ePushCommit builds an in-memory repo with one commit and pushes it to
// cloneURL (local master → remote main), returning the pushed commit hash.
func e2ePushCommit(t *testing.T, cloneURL string) string {
	t.Helper()
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
	return commit.String()
}
