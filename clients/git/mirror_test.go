package git

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// seedRepo creates <org>/<name> then pushes one commit on branch main (+ an
// optional lightweight tag) over the live smart-HTTP server — a real external
// git source a mirror can pull from. Returns the pushed commit hash.
func seedRepo(t *testing.T, app *zip.App, base, org, name, content, tag string) plumbing.Hash {
	t.Helper()
	if code, body := do(t, app, http.MethodPost, "/v1/git/repos", org,
		map[string]any{"name": name}); code != http.StatusCreated {
		t.Fatalf("seed create %s/%s: %d %s", org, name, code, body)
	}
	asTenant(org)
	fs := memfs.New()
	local, err := gogit.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatalf("init %s/%s: %v", org, name, err)
	}
	wt, err := local.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	f, err := fs.Create("README.md")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	h, err := wt.Commit("seed "+content, &gogit.CommitOptions{
		Author: &object.Signature{Name: "hanzo-dev", Email: "dev@hanzo.ai", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	refspecs := []config.RefSpec{"refs/heads/master:refs/heads/main"}
	if tag != "" {
		if _, err := local.CreateTag(tag, h, nil); err != nil {
			t.Fatalf("tag: %v", err)
		}
		refspecs = append(refspecs, config.RefSpec("refs/tags/"+tag+":refs/tags/"+tag))
	}
	if _, err := local.CreateRemote(&config.RemoteConfig{
		Name: "origin", URLs: []string{base + "/v1/git/" + org + "/" + name + ".git"},
	}); err != nil {
		t.Fatalf("remote: %v", err)
	}
	if err := local.Push(&gogit.PushOptions{RemoteName: "origin", RefSpecs: refspecs}); err != nil {
		t.Fatalf("push %s/%s: %v", org, name, err)
	}
	return h
}

// callMirror imports source into org's repo :name via the mirror endpoint and
// returns the decoded repoView. asTenant(org) is set first so the server-side
// outbound fetch carries the tenant identity the in-process source authorizes on.
func callMirror(t *testing.T, app *zip.App, org, name, source string) repoView {
	t.Helper()
	asTenant(org)
	code, body := do(t, app, http.MethodPost, "/v1/git/repos/"+name+"/mirror", org,
		map[string]any{"source": source})
	if code != http.StatusOK {
		t.Fatalf("mirror %s/%s want 200, got %d (%s)", org, name, code, body)
	}
	var v repoView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("mirror json: %v (%s)", err, body)
	}
	return v
}

// cloneRepo clones <org>/<name> from base as org and returns the repo + its
// resolved HEAD commit hash.
func cloneRepo(t *testing.T, base, org, name string) (*gogit.Repository, plumbing.Hash) {
	t.Helper()
	asTenant(org)
	repo, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{
		URL: base + "/v1/git/" + org + "/" + name + ".git",
	})
	if err != nil {
		t.Fatalf("clone %s/%s: %v", org, name, err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head %s/%s: %v", org, name, err)
	}
	return repo, head.Hash()
}

// TestMirrorImportCloneIdempotentIsolation is the end-to-end proof of the mirror
// mechanism: import an external repo's refs+objects into the embedded server,
// clone them back and match the commit AND tag, prove a re-mirror is idempotent,
// prove a force re-mirror onto an UNRELATED source updates the refs, and prove
// another tenant cannot read the mirrored repo.
func TestMirrorImportCloneIdempotentIsolation(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)

	// Two independent external sources under the tenant's org.
	ha := seedRepo(t, app, base, "acme", "srca", "# source A\n", "v1.0.0")
	hb := seedRepo(t, app, base, "acme", "srcb", "# source B\n", "")
	srcA := base + "/v1/git/acme/srca.git"
	srcB := base + "/v1/git/acme/srcb.git"

	// 1) First mirror clones source A into a fresh repo <acme/mirror>.
	v := callMirror(t, app, "acme", "mirror", srcA)
	if v.Org != "acme" || v.Name != "mirror" || v.Head != ha.String() {
		t.Fatalf("mirror view: org=%s name=%s head=%s (want head %s)", v.Org, v.Name, v.Head, ha)
	}
	if v.CloneURL != "https://api.hanzo.test/v1/git/acme/mirror.git" {
		t.Fatalf("unexpected cloneUrl: %q", v.CloneURL)
	}

	// Clone it back: the mirrored commit AND tag must be present.
	repo, head := cloneRepo(t, base, "acme", "mirror")
	if head != ha {
		t.Fatalf("cloned HEAD %s != source A %s", head, ha)
	}
	if _, err := repo.Reference(plumbing.NewTagReferenceName("v1.0.0"), false); err != nil {
		t.Fatalf("mirrored tag v1.0.0 missing: %v", err)
	}

	// 2) Idempotent re-mirror of the SAME source is a no-op: HEAD unchanged.
	callMirror(t, app, "acme", "mirror", srcA)
	if _, head = cloneRepo(t, base, "acme", "mirror"); head != ha {
		t.Fatalf("re-mirror changed HEAD: %s != %s", head, ha)
	}

	// 3) Mirror of a DIFFERENT source force-updates refs to unrelated history.
	callMirror(t, app, "acme", "mirror", srcB)
	if _, head = cloneRepo(t, base, "acme", "mirror"); head != hb {
		t.Fatalf("force re-mirror HEAD %s != source B %s", head, hb)
	}

	// 4) Cross-tenant isolation: another org cannot read the mirrored repo.
	asTenant("intruder")
	if _, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{
		URL: base + "/v1/git/acme/mirror.git",
	}); err == nil {
		t.Fatal("intruder clone of acme's mirror must fail, got nil error")
	}
	asTenant("acme")
}

// TestMirrorSelfHostThenPushDeploys proves the self-host wiring: cloud's OWN
// repo is mirrored to hanzoai/cloud through the ordinary mirror path (no special
// case), and a subsequent push to that self-hosted repo fires git-push-to-deploy
// — the whole point of getting off GitHub.
func TestMirrorSelfHostThenPushDeploys(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)

	// Stand in for github.com/hanzoai/cloud, then mirror it to hanzoai/cloud.
	// Mirror-in is import-only: it does NOT fire a deploy, so we register the
	// build trigger only now — the sole event we observe is the later PUSH to
	// the self-hosted repo, which is the behavior under test.
	upstream := seedRepo(t, app, base, "hanzoai", "upstream", "# hanzo cloud\n", "v0.1.0")
	callMirror(t, app, "hanzoai", "cloud", base+"/v1/git/hanzoai/upstream.git")

	var mu sync.Mutex
	var got []cloud.GitPushEvent
	cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) error {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	repo, head := cloneRepo(t, base, "hanzoai", "cloud")
	if head != upstream {
		t.Fatalf("self-hosted HEAD %s != upstream %s", head, upstream)
	}
	if _, err := repo.Reference(plumbing.NewTagReferenceName("v0.1.0"), false); err != nil {
		t.Fatalf("mirrored tag v0.1.0 missing: %v", err)
	}

	// A push to the self-hosted repo must fire the deploy trigger, exactly like a
	// native repo. Push a new branch so it is a clean ref create (no history
	// overlap needed with the imported main).
	asTenant("hanzoai")
	fs := memfs.New()
	local, err := gogit.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, _ := local.Worktree()
	f, _ := fs.Create("ship.txt")
	_, _ = f.Write([]byte("deploy me\n"))
	_ = f.Close()
	if _, err := wt.Add("ship.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	commit, err := wt.Commit("ship", &gogit.CommitOptions{
		Author: &object.Signature{Name: "hanzo-dev", Email: "dev@hanzo.ai", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := local.CreateRemote(&config.RemoteConfig{
		Name: "origin", URLs: []string{base + "/v1/git/hanzoai/cloud.git"},
	}); err != nil {
		t.Fatalf("remote: %v", err)
	}
	if err := local.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/master:refs/heads/deploy"},
	}); err != nil {
		t.Fatalf("push to self-hosted cloud: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want exactly 1 deploy trigger, got %d: %+v", len(got), got)
	}
	ev := got[0]
	if ev.Org != "hanzoai" || ev.Repo != "cloud" || ev.Branch != "deploy" || ev.Commit != commit.String() {
		t.Fatalf("unexpected deploy event: %+v (want commit %s)", ev, commit)
	}
	if ev.CloneURL != "https://api.hanzo.test/v1/git/hanzoai/cloud.git" {
		t.Fatalf("unexpected clone URL: %q", ev.CloneURL)
	}
}
