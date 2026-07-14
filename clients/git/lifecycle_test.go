package git

import (
	"context"
	"encoding/json"
	"net"
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
	// build.started is a real kind but never delivered to Slack → reject at subscribe.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "#ok", "events": []string{"build.started"}}); code != http.StatusBadRequest {
		t.Fatalf("build.started subscribe want 400, got %d", code)
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
	// The LOCAL git host is NOT a permitted outbound target (Red MED-1: internal
	// SSRF + privileged-cred presentation) even though it IS an inbound source.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/mirrors", "acme",
		map[string]any{"url": "https://git.hanzo.ai/v1/git/acme/code.git"}); code != http.StatusBadRequest {
		t.Fatalf("local-host target want 400, got %d", code)
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

// ── MED-2: notify + mirror route on the FULL (org,project,repo) identity ──────

func TestNotifyRoutingIsProjectScoped(t *testing.T) {
	calls := captureSlack(t)
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	store, err := storeFor(mounted, "acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	// Same repo NAME, two DIFFERENT project scopes → two distinct identities.
	must(t, store.CreateSubscription(ctx, Subscription{ID: "s_org", Org: "acme", Project: "", Repo: "code", Channel: "#orglevel", CreatedAt: 1}))
	must(t, store.CreateSubscription(ctx, Subscription{ID: "s_b", Org: "acme", Project: "projB", Repo: "code", Channel: "#projb", CreatedAt: 1}))

	// A push in the ORG-LEVEL scope must reach ONLY #orglevel — never projB's channel
	// (that would be within-org cross-project code disclosure).
	notifyLifecycle(mounted, ctx, cloud.LifecycleEvent{
		Kind: cloud.LifecyclePushLanded, Org: "acme", Project: "", Repo: "code",
		Branch: "main", After: strings.Repeat("a", 40),
	})
	got := snapshot(calls)
	if len(got) != 1 || got[0].channel != "#orglevel" {
		t.Fatalf("org-level push must notify ONLY #orglevel, got %+v", got)
	}
}

// ── MED-3: repo delete cascades subscriptions + mirrors (no exfil-on-recreate) ─

func TestRepoDeleteCascadesLifecycleConfig(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	store, err := storeFor(mounted, "acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	must(t, store.CreateSubscription(ctx, Subscription{ID: "s1", Org: "acme", Project: "", Repo: "code", Channel: "#c", CreatedAt: 1}))
	must(t, store.CreateMirror(ctx, MirrorTarget{ID: "m1", Org: "acme", Project: "", Repo: "code", Host: "github.com", URL: "https://github.com/evil/x.git", CreatedAt: 1}))

	// Delete the repo via the route.
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/repos/code", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete repo want 204, got %d", code)
	}
	// Subscriptions AND mirror targets are gone (cascade).
	if subs, _ := store.ListSubscriptions(ctx, "acme", "", "code"); len(subs) != 0 {
		t.Fatalf("subscriptions not cascade-deleted: %+v", subs)
	}
	if mirs, _ := store.ListMirrors(ctx, "acme", "", "code"); len(mirs) != 0 {
		t.Fatalf("mirror targets not cascade-deleted: %+v", mirs)
	}
	// Re-create the repo of the same name → it inherits NOTHING (no orphan exfil).
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("recreate repo failed")
	}
	if mirs, _ := store.ListMirrors(ctx, "acme", "", "code"); len(mirs) != 0 {
		t.Fatalf("re-created repo inherited a prior mirror target: %+v", mirs)
	}
}

// ── MED-4: user-derived text is Slack-mrkdwn-escaped ─────────────────────────

func TestNotifyEscapesMrkdwn(t *testing.T) {
	calls := captureSlack(t)
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/code/subscriptions", "acme",
		map[string]any{"channel": "#all"}); code != 201 {
		t.Fatalf("subscribe failed: %d", code)
	}
	// A hostile commit subject: a channel broadcast + a disguised link + an ampersand.
	pushCommit(t, base, "acme", "code", "main", "<!channel> <https://evil.example|invoice> & danger")

	got := waitForNotify(t, calls, "#all", 3*time.Second)
	text := allBlockText(got.blocks)
	for _, raw := range []string{"<!channel>", "<https://evil.example|invoice>"} {
		if strings.Contains(text, raw) {
			t.Fatalf("unescaped mrkdwn %q leaked into a posted block: %s", raw, text)
		}
	}
	for _, esc := range []string{"&lt;!channel&gt;", "&amp; danger"} {
		if !strings.Contains(text, esc) {
			t.Fatalf("expected escaped %q in block text; got %s", esc, text)
		}
	}

	// A hostile BRANCH name is the other sink: git refnames allow < > & !, and the
	// receive-pack fire path (branchTips → for-each-ref) applies NO branchRE, so the
	// raw name flows into the summary AND the *Branch* field unless escaped. Push a
	// branch named x<!channel>y via the real git CLI (go-git rejects such a refspec).
	const hostileBranch = "x<!channel>y"
	pushBranchCLI(t, base, "acme", "code", hostileBranch)
	bgot := waitForNotifyContaining(t, calls, "x&lt;!channel&gt;y", 3*time.Second)
	btext := allBlockText(bgot.blocks)
	if strings.Contains(bgot.text, hostileBranch) || strings.Contains(btext, hostileBranch) {
		t.Fatalf("raw hostile branch leaked (summary=%q blocks=%s)", bgot.text, btext)
	}
	if !strings.Contains(bgot.text, "x&lt;!channel&gt;y") {
		t.Fatalf("branch not escaped in summary: %q", bgot.text)
	}
	if !strings.Contains(btext, "x&lt;!channel&gt;y") {
		t.Fatalf("branch not escaped in the Branch field: %s", btext)
	}
}

// pushBranchCLI pushes one commit to an arbitrary (possibly hostile) branch name on
// <org>/<name> over smart-HTTP with the real git CLI — used to exercise branch
// names go-git's refspec parser rejects.
func pushBranchCLI(t *testing.T, base, org, name, branch string) {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "work")
	writeFile(t, work, "bfile.txt", "branch payload")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "a safe subject")
	gitRun(t, work, "remote", "add", "origin", base+"/v1/git/"+org+"/"+name+".git")
	gitRun(t, work, append(orgHeaderArgs(org), "push", "origin", "HEAD:refs/heads/"+branch)...)
}

// waitForNotifyContaining blocks until a captured Slack call's summary OR blocks
// contain needle, or fails.
func waitForNotifyContaining(t *testing.T, calls *[]notifyCall, needle string, d time.Duration) notifyCall {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, c := range snapshot(calls) {
			if strings.Contains(c.text, needle) || strings.Contains(allBlockText(c.blocks), needle) {
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no Slack notify containing %q within %s", needle, d)
	return notifyCall{}
}

// allBlockText collects every mrkdwn "text" string from a Block Kit block slice
// (section text + section fields), so a test can assert on the raw Go strings
// BEFORE JSON transport (avoiding json.Marshal's separate HTML-escaping).
func allBlockText(blocks []any) string {
	var b strings.Builder
	for _, blk := range blocks {
		m, ok := blk.(map[string]any)
		if !ok {
			continue
		}
		if txt, ok := m["text"].(map[string]any); ok {
			if s, ok := txt["text"].(string); ok {
				b.WriteString(s + "\n")
			}
		}
		if fields, ok := m["fields"].([]any); ok {
			for _, f := range fields {
				if fm, ok := f.(map[string]any); ok {
					if s, ok := fm["text"].(string); ok {
						b.WriteString(s + "\n")
					}
				}
			}
		}
	}
	return b.String()
}

// ── HIGH-1: a stalled downstream cannot starve the shared pack plane ─────────

func TestMirrorOutStalledDownstreamDoesNotStarveClones(t *testing.T) {
	t.Setenv("GIT_MIRROR_OUT_TIMEOUT", "2") // bound the stalled push tightly for the test
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	// Seed the repo by a LOCAL push (does not fire the server-side reactors).
	bareAbs := mounted.State.storage.absRepoPath("acme", "", "code")
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	writeFile(t, work, "a.txt", "one")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "seed")
	commit := gitOut(t, work, "rev-parse", "HEAD")
	gitRun(t, work, "push", "-q", bareAbs, "main:refs/heads/main")

	// A blackhole downstream: accepts the TCP connection but NEVER responds, so a
	// push to it hangs until the per-push timeout fires.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var heldMu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			heldMu.Lock()
			held = append(held, c)
			heldMu.Unlock()
		}
	}()
	t.Cleanup(func() {
		heldMu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		heldMu.Unlock()
	})

	store, err := storeFor(mounted, "acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	must(t, store.CreateMirror(context.Background(), MirrorTarget{
		ID: "m_bh", Org: "acme", Project: "", Repo: "code", Host: "blackhole",
		URL: "http://" + ln.Addr().String() + "/x.git", CreatedAt: time.Now().Unix(),
	}))

	// Fire the outbound mirror — it stalls on the blackhole, holding the dedicated
	// mirror slot (NOT the pack slot).
	done := make(chan struct{})
	go func() {
		mirrorOutbound(mounted, context.Background(), cloud.LifecycleEvent{
			Kind: cloud.LifecyclePushLanded, Org: "acme", Repo: "code", Branch: "main", After: commit,
		})
		close(done)
	}()

	// A clone must still succeed promptly — it uses the pack semaphore, untouched by
	// the stalled mirror push. This is the isolation proof.
	asTenant("acme")
	cloneErr := make(chan error, 1)
	go func() {
		_, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: base + "/v1/git/acme/code.git"})
		cloneErr <- err
	}()
	select {
	case err := <-cloneErr:
		if err != nil {
			t.Fatalf("clone failed while a mirror push stalled: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("clone starved by a stalled mirror push (>10s) — semaphores not isolated")
	}

	// The stalled push is bounded by the timeout — mirrorOutbound returns.
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("mirrorOutbound never returned — stalled push not bounded by timeout")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
