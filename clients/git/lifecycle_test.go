package git

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// ── subscription CRUD + tenant isolation ─────────────────────────────────────

func TestSubscriptionCRUDAndIsolation(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}

	// Missing principal → 403 fail-closed.
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/code/subscriptions", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}
	// Cross-tenant: another org cannot see acme's repo → 404 (never reaches the store).
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "beta",
		map[string]any{"channel": "#x"}); code != http.StatusNotFound {
		t.Fatalf("cross-tenant subscribe want 404, got %d", code)
	}
	// Malformed channel → 400 (injection guard).
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "bad chan\"}"}); code != http.StatusBadRequest {
		t.Fatalf("bad channel want 400, got %d", code)
	}
	// Unknown event kind → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "#ok", "events": []string{"nope"}}); code != http.StatusBadRequest {
		t.Fatalf("bad event want 400, got %d", code)
	}

	// Create a valid subscription.
	code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "#deploys", "events": []string{"push.landed", "deploy.live"}})
	if code != http.StatusCreated {
		t.Fatalf("subscribe want 201, got %d (%s)", code, b)
	}
	var sub subscriptionView
	if err := json.Unmarshal(b, &sub); err != nil {
		t.Fatalf("subscribe json: %v (%s)", err, b)
	}
	if sub.Channel != "#deploys" || len(sub.Events) != 2 {
		t.Fatalf("unexpected subscription: %+v", sub)
	}

	// Duplicate channel → 409.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "#deploys"}); code != http.StatusConflict {
		t.Fatalf("duplicate subscribe want 409, got %d", code)
	}

	// List (owner sees it).
	code, b = do(t, app, http.MethodGet, "/v1/git/repos/code/subscriptions", "acme", nil)
	if code != 200 {
		t.Fatalf("list want 200, got %d", code)
	}
	var listed struct {
		Data []subscriptionView `json:"data"`
	}
	_ = json.Unmarshal(b, &listed)
	if len(listed.Data) != 1 {
		t.Fatalf("acme should see 1 subscription, got %+v", listed.Data)
	}

	// Cross-tenant delete cannot touch acme's subscription (beta → 404 on the repo).
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/code/subscriptions/"+sub.ID, "beta", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete want 404, got %d", code)
	}
	// Owner delete → 204, re-delete → 404.
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/code/subscriptions/"+sub.ID, "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/code/subscriptions/"+sub.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("re-delete want 404, got %d", code)
	}
}

// ── mirror-target CRUD + allowlist + tenant isolation ────────────────────────

func TestMirrorTargetCRUDAndIsolation(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}

	// Non-allowlisted host → 400 (fail-closed; token can never reach it).
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/mirrors", "acme",
		map[string]any{"url": "https://evil.example.com/acme/code.git"}); code != http.StatusBadRequest {
		t.Fatalf("non-allowlisted target want 400, got %d", code)
	}
	// http (non-https) → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/mirrors", "acme",
		map[string]any{"url": "http://github.com/acme/code.git"}); code != http.StatusBadRequest {
		t.Fatalf("http target want 400, got %d", code)
	}
	// host-vs-url mismatch → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/mirrors", "acme",
		map[string]any{"host": "gitlab.com", "url": "https://github.com/acme/code.git"}); code != http.StatusBadRequest {
		t.Fatalf("host mismatch want 400, got %d", code)
	}

	// Valid GitLab target (allowlist now includes gitlab.com). Userinfo is stripped.
	code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/mirrors", "acme",
		map[string]any{"url": "https://user:secret@gitlab.com/acme/code.git"})
	if code != http.StatusCreated {
		t.Fatalf("gitlab target want 201, got %d (%s)", code, b)
	}
	var mir mirrorTargetView
	_ = json.Unmarshal(b, &mir)
	if mir.Host != "gitlab.com" || strings.Contains(mir.URL, "secret") {
		t.Fatalf("mirror target leaked creds or wrong host: %+v", mir)
	}

	// GitHub target too.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/mirrors", "acme",
		map[string]any{"url": "https://github.com/acme/code.git"}); code != http.StatusCreated {
		t.Fatalf("github target want 201, got %d", code)
	}
	// Duplicate host → 409.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/mirrors", "acme",
		map[string]any{"url": "https://gitlab.com/acme/other.git"}); code != http.StatusConflict {
		t.Fatalf("duplicate host want 409, got %d", code)
	}

	// Cross-tenant cannot list acme's targets (repo 404).
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/code/mirrors", "beta", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant list want 404, got %d", code)
	}
	// Owner lists both.
	code, b = do(t, app, http.MethodGet, "/v1/git/repos/code/mirrors", "acme", nil)
	var listed struct {
		Data []mirrorTargetView `json:"data"`
	}
	_ = json.Unmarshal(b, &listed)
	if code != 200 || len(listed.Data) != 2 {
		t.Fatalf("owner should see 2 targets, got %d %+v", code, listed.Data)
	}
	// Delete + re-delete.
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/code/mirrors/"+mir.ID, "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/code/mirrors/"+mir.ID, "beta", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete want 404, got %d", code)
	}
}

// ── push → Slack notify (via the slackNotify seam) ───────────────────────────

type notifyCall struct {
	org, channel, text string
	blocks             []any
}

func captureSlack(t *testing.T) *[]notifyCall {
	t.Helper()
	calls := &[]notifyCall{}
	old := slackNotify
	// The append and every snapshot read share snapMu — the notify subscriber runs
	// on a detached goroutine, so writer and reader MUST use the one lock.
	slackNotify = func(_ context.Context, org, channel, text string, blocks []any) error {
		snapMu.Lock()
		*calls = append(*calls, notifyCall{org, channel, text, blocks})
		snapMu.Unlock()
		return nil
	}
	t.Cleanup(func() { slackNotify = old })
	return calls
}

func TestPushNotifiesSubscribedChannel(t *testing.T) {
	calls := captureSlack(t)
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	// #all gets every kind; #deployonly only deploy.live — a push must NOT reach it.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "#all"}); code != 201 {
		t.Fatalf("subscribe #all failed: %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "#deployonly", "events": []string{"deploy.live"}}); code != 201 {
		t.Fatalf("subscribe #deployonly failed: %d", code)
	}

	commit := pushCommit(t, base, "acme", "code", "main", "hello world subject")

	// The notify subscriber runs detached — wait for the #all delivery.
	got := waitForNotify(t, calls, "#all", 3*time.Second)
	if got.org != "acme" {
		t.Fatalf("notify org: %q", got.org)
	}
	blob, _ := json.Marshal(got.blocks)
	for _, want := range []string{"main", commit[:7], "u_acme", "hello world subject", "acme/code"} {
		if !strings.Contains(string(blob), want) {
			t.Fatalf("notify blocks missing %q; blocks=%s", want, blob)
		}
	}
	// Grace, then assert the filtered channel was NOT delivered a push event.
	time.Sleep(100 * time.Millisecond)
	for _, c := range snapshot(calls) {
		if c.channel == "#deployonly" {
			t.Fatalf("#deployonly (deploy.live-only) must NOT receive a push.landed event")
		}
	}
}

// waitForNotify blocks until a captured Slack call targets channel, or fails.
func waitForNotify(t *testing.T, calls *[]notifyCall, channel string, d time.Duration) notifyCall {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, c := range snapshot(calls) {
			if c.channel == channel {
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no Slack notify to %s within %s", channel, d)
	return notifyCall{}
}

var snapMu sync.Mutex

func snapshot(calls *[]notifyCall) []notifyCall {
	snapMu.Lock()
	defer snapMu.Unlock()
	out := make([]notifyCall, len(*calls))
	copy(out, *calls)
	return out
}

// pushCommit pushes one commit on branch to <org>/<name> over smart-HTTP and
// returns the commit hash.
func pushCommit(t *testing.T, base, org, name, branch, msg string) string {
	t.Helper()
	asTenant(org)
	fs := memfs.New()
	local, err := gogit.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, _ := local.Worktree()
	f, _ := fs.Create("f.txt")
	_, _ = f.Write([]byte("x"))
	_ = f.Close()
	if _, err := wt.Add("f.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	commit, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "hanzo-dev", Email: "dev@hanzo.ai", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := local.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{base + "/v1/git/" + org + "/" + name + ".git"}}); err != nil {
		t.Fatalf("remote: %v", err)
	}
	if err := local.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/master:refs/heads/" + branch)},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	return commit.String()
}

// ── mirror-out: only the advanced branch + loop prevention ───────────────────

func TestMirrorOutboundPushesOnlyAdvancedBranch(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	bareAbs := mounted.State.storage.absRepoPath("acme", "", "code")

	// Seed the source bare repo with TWO branches (main + feature) by pushing over a
	// local path (bypasses the edge; we only need on-disk refs).
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	writeFile(t, work, "a.txt", "one")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "main commit")
	mainHash := gitOut(t, work, "rev-parse", "HEAD")
	gitRun(t, work, "checkout", "-q", "-b", "feature")
	writeFile(t, work, "b.txt", "two")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "feature commit")
	gitRun(t, work, "push", "-q", bareAbs, "main:refs/heads/main", "feature:refs/heads/feature")

	// A writable downstream bare repo, served over http (a stand-in for GitHub).
	downRoot := t.TempDir()
	downBare := filepath.Join(downRoot, "down.git")
	gitRun(t, "", "init", "-q", "--bare", downBare)
	gitRun(t, downBare, "config", "http.receivepack", "true")
	downBase := serveGitHTTP(t, downRoot)
	downURL := downBase + "/down.git"

	store, err := storeFor(mounted, "acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.CreateMirror(context.Background(), MirrorTarget{
		ID: "mir_test", Org: "acme", Repo: "code", Host: "127.0.0.1", URL: downURL, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("create mirror: %v", err)
	}

	// Fire a PushLanded for main ONLY — feature must NOT be mirrored.
	mirrorOutbound(mounted, context.Background(), cloud.LifecycleEvent{
		Kind: cloud.LifecyclePushLanded, Org: "acme", Repo: "code", Branch: "main", After: mainHash,
	})
	refs := downstreamRefs(t, downBare)
	if refs["refs/heads/main"] != mainHash {
		t.Fatalf("downstream main = %q, want %q (only-advanced push failed)", refs["refs/heads/main"], mainHash)
	}
	if _, ok := refs["refs/heads/feature"]; ok {
		t.Fatalf("downstream got refs/heads/feature — mirror-out pushed more than the advanced branch")
	}

	// Loop prevention: an event whose Origin IS this target host must be suppressed.
	gitRun(t, work, "checkout", "-q", "-b", "loop")
	writeFile(t, work, "c.txt", "three")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "loop commit")
	gitRun(t, work, "push", "-q", bareAbs, "loop:refs/heads/loop")
	mirrorOutbound(mounted, context.Background(), cloud.LifecycleEvent{
		Kind: cloud.LifecyclePushLanded, Org: "acme", Repo: "code", Branch: "loop",
		After: gitOut(t, work, "rev-parse", "loop"), Origin: "127.0.0.1",
	})
	if _, ok := downstreamRefs(t, downBare)["refs/heads/loop"]; ok {
		t.Fatalf("loop-prevention failed: refs/heads/loop was mirrored back to its origin host")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// downstreamRefs snapshots a bare repo's branch refs → hashes.
func downstreamRefs(t *testing.T, bare string) map[string]string {
	t.Helper()
	out := gitOut(t, bare, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if sp := strings.IndexByte(line, ' '); sp > 0 {
			refs[line[:sp]] = line[sp+1:]
		}
	}
	return refs
}
