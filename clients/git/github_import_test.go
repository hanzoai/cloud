package git

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// github_import_test.go proves the git object-plane half of the GitHub-App sync:
// import (fast-forward mirror-in), the inbound fast-forward-ONLY advance, the
// split-brain guard (a divergence preserves native), loop prevention, and status.
// It drives the cloud.GitImporter seam directly (githubImporter{}) against a REAL
// mutable external git source served over smart-HTTP — a stand-in for github.com.

// ghSource is a mutable external git source: a work tree + a served bare repo.
// commit() adds a commit and publishes it, returning the new tip.
type ghSource struct {
	t    *testing.T
	work string
	url  string
}

func newSource(t *testing.T, root, base, name, content string) *ghSource {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	writeSrcFile(t, work, content)
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "seed")
	bare := filepath.Join(root, name+".git")
	gitRun(t, "", "clone", "-q", "--bare", work, bare)
	gitRun(t, work, "remote", "add", "origin", bare)
	return &ghSource{t: t, work: work, url: base + "/" + name + ".git"}
}

func (s *ghSource) tip() string { return gitOut(s.t, s.work, "rev-parse", "HEAD") }

func (s *ghSource) commit(content string) string {
	writeSrcFile(s.t, s.work, content)
	gitRun(s.t, s.work, "add", "-A")
	gitRun(s.t, s.work, "commit", "-q", "-m", "src change")
	gitRun(s.t, s.work, "push", "-q", "origin", "main")
	return s.tip()
}

func writeSrcFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pushNative pushes a native-only commit (a child of native's current main tip) to
// the native smart-HTTP endpoint, so native's main advances INDEPENDENTLY of the
// upstream — the setup for a real divergence. Returns the new native tip.
func pushNative(t *testing.T, base, org, name, content string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "native-work")
	repoURL := base + "/v1/git/" + org + "/" + name + ".git"
	gitRun(t, "", append(orgHeaderArgs(org), "clone", "-q", repoURL, dst)...)
	if err := os.WriteFile(filepath.Join(dst, "f"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dst, "add", "-A")
	gitRun(t, dst, "commit", "-q", "-m", "native-only")
	tip := gitOut(t, dst, "rev-parse", "HEAD")
	gitRun(t, dst, append(orgHeaderArgs(org), "push", "origin", "HEAD:refs/heads/main")...)
	return tip
}

// captureLifecycle registers a subscriber that forwards push.landed events onto a
// buffered channel with a non-blocking send (so a lingering subscriber from a prior
// test never blocks or panics). No reset — that would clear the git reactors Mount
// registered once for the whole binary.
func captureLifecycle(t *testing.T) <-chan cloud.LifecycleEvent {
	t.Helper()
	ch := make(chan cloud.LifecycleEvent, 16)
	cloud.RegisterLifecycleSubscriber(func(_ context.Context, ev cloud.LifecycleEvent) {
		if ev.Kind == cloud.LifecyclePushLanded {
			select {
			case ch <- ev:
			default:
			}
		}
	})
	return ch
}

func hostFromURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u.Hostname()
}

// TestGitHubImportFastForwardAndStatus proves import: a fresh import mirrors every
// branch in, registers the outbound mirror target, reports imported+synced status,
// and is idempotent (a re-import neither duplicates the mirror nor changes native).
func TestGitHubImportFastForwardAndStatus(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	root := t.TempDir()
	srcBase := serveGitHTTP(t, root)
	src := newSource(t, root, srcBase, "widgets", "v1")
	ctx := context.Background()

	req := cloud.GitImportReq{
		Org: "acme", Repo: "widgets", CloneURL: src.url,
		MirrorURL: "https://github.com/acme/widgets.git", // an allowed outbound target
	}
	if err := (githubImporter{}).ImportRepo(ctx, req); err != nil {
		t.Fatalf("import: %v", err)
	}

	// Native has the imported content.
	if _, head := cloneRepo(t, base, "acme", "widgets"); head.String() != src.tip() {
		t.Fatalf("native HEAD %s != source %s", head, src.tip())
	}

	// The outbound mirror target was registered (so a native push mirrors back).
	store, err := storeFor(mounted.Load(), "acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	mirrors, err := store.ListMirrors(ctx, "acme", "", "widgets")
	if err != nil || len(mirrors) != 1 || mirrors[0].Host != "github.com" {
		t.Fatalf("want 1 github.com mirror, got %+v (err %v)", mirrors, err)
	}

	// Status: imported + synced (no conflict); an unrelated name is not imported.
	st, err := (githubImporter{}).RepoStatus(ctx, "acme", "", []string{"widgets", "other"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st["widgets"].Imported || st["widgets"].Conflict {
		t.Fatalf("widgets should be imported+synced, got %+v", st["widgets"])
	}
	if st["other"].Imported {
		t.Fatalf("other should not be imported, got %+v", st["other"])
	}

	// Idempotent: a re-import neither duplicates the mirror nor errors.
	if err := (githubImporter{}).ImportRepo(ctx, req); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	mirrors, _ = store.ListMirrors(ctx, "acme", "", "widgets")
	if len(mirrors) != 1 {
		t.Fatalf("re-import duplicated the mirror: %+v", mirrors)
	}
}

// TestInboundFastForwardAppliesEmitsOrigin proves the inbound ff path: an upstream
// advance fast-forwards native AND emits push.landed carrying the source host as
// Origin (so push-to-deploy fires and the outbound mirror suppresses the echo).
func TestInboundFastForwardAppliesEmitsOrigin(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	root := t.TempDir()
	srcBase := serveGitHTTP(t, root)
	src := newSource(t, root, srcBase, "svc", "v1")
	ctx := context.Background()

	if err := (githubImporter{}).ImportRepo(ctx, cloud.GitImportReq{Org: "acme", Repo: "svc", CloneURL: src.url}); err != nil {
		t.Fatalf("import: %v", err)
	}
	s0 := src.tip()
	s1 := src.commit("v2") // upstream fast-forward

	events := captureLifecycle(t)
	host := hostFromURL(t, src.url)
	res, err := (githubImporter{}).InboundSync(ctx, cloud.GitInboundReq{
		Org: "acme", Repo: "svc", Branch: "main", CloneURL: src.url, Origin: host,
	})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.Applied || res.Before != s0 || res.After != s1 {
		t.Fatalf("want Applied %s->%s, got %+v", s0, s1, res)
	}
	if _, head := cloneRepo(t, base, "acme", "svc"); head.String() != s1 {
		t.Fatalf("native HEAD %s != upstream %s after ff", head, s1)
	}
	select {
	case ev := <-events:
		if ev.Origin != host || ev.Before != s0 || ev.After != s1 {
			t.Fatalf("emitted event wrong: %+v (want origin %s %s->%s)", ev, host, s0, s1)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inbound ff did not emit push.landed")
	}
}

// TestInboundConflictPreservesNative is THE split-brain guard: native diverges (a
// native-only commit) and the upstream diverges too; the inbound fetch must be
// REJECTED as non-fast-forward, native must be UNCHANGED, no deploy/mirror event
// fires, and the conflict is recorded + surfaced in status.
func TestInboundConflictPreservesNative(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	root := t.TempDir()
	srcBase := serveGitHTTP(t, root)
	src := newSource(t, root, srcBase, "app", "v1")
	ctx := context.Background()

	if err := (githubImporter{}).ImportRepo(ctx, cloud.GitImportReq{Org: "acme", Repo: "app", CloneURL: src.url}); err != nil {
		t.Fatalf("import: %v", err)
	}
	s0 := src.tip()

	// Native diverges: a native-only commit (child of S0).
	n1 := pushNative(t, base, "acme", "app", "native change")
	if n1 == s0 {
		t.Fatal("native-only commit did not advance native")
	}
	// Upstream diverges: a different commit (also a child of S0, a sibling of n1).
	s1 := src.commit("github change")
	if s1 == n1 {
		t.Fatal("source and native must differ for a divergence")
	}

	events := captureLifecycle(t)
	host := hostFromURL(t, src.url)
	res, err := (githubImporter{}).InboundSync(ctx, cloud.GitInboundReq{
		Org: "acme", Repo: "app", Branch: "main", CloneURL: src.url, Origin: host,
	})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.Conflict || res.Applied {
		t.Fatalf("divergence must be a Conflict, got %+v", res)
	}
	// Native is UNCHANGED — still the native-only commit, NOT the upstream's.
	if _, head := cloneRepo(t, base, "acme", "app"); head.String() != n1 {
		t.Fatalf("native was overwritten: HEAD %s (want native %s, upstream was %s)", head, n1, s1)
	}
	// A conflict fires no deploy/mirror event.
	select {
	case ev := <-events:
		t.Fatalf("conflict must NOT emit push.landed, got %+v", ev)
	case <-time.After(700 * time.Millisecond):
	}
	// The conflict is recorded + surfaced.
	st, _ := (githubImporter{}).RepoStatus(ctx, "acme", "", []string{"app"})
	if !st["app"].Imported || !st["app"].Conflict {
		t.Fatalf("app should be imported+conflict, got %+v", st["app"])
	}
}

// TestInboundLoopPreventionTipEqual proves the immediate-echo guard: an inbound push
// whose upstream tip already equals native's tip (the mirror we just pushed OUT
// coming back) is a NO-OP — no ref change, no event — so no ping-pong occurs.
func TestInboundLoopPreventionTipEqual(t *testing.T) {
	app := mountApp(t)
	_ = liveServer(t, app)
	root := t.TempDir()
	srcBase := serveGitHTTP(t, root)
	src := newSource(t, root, srcBase, "loop", "v1")
	ctx := context.Background()

	if err := (githubImporter{}).ImportRepo(ctx, cloud.GitImportReq{Org: "acme", Repo: "loop", CloneURL: src.url}); err != nil {
		t.Fatalf("import: %v", err)
	}
	// native == source now; an inbound sync of the same tip is the loop echo.
	events := captureLifecycle(t)
	res, err := (githubImporter{}).InboundSync(ctx, cloud.GitInboundReq{
		Org: "acme", Repo: "loop", Branch: "main", CloneURL: src.url, Origin: hostFromURL(t, src.url),
	})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.NoOp || res.Applied || res.Conflict {
		t.Fatalf("equal-tip inbound must be NoOp, got %+v", res)
	}
	select {
	case ev := <-events:
		t.Fatalf("loop echo must NOT emit push.landed, got %+v", ev)
	case <-time.After(700 * time.Millisecond):
	}
}

// TestInboundNotImportedNoOp proves a webhook for a repo that was never imported is
// a no-op — inbound sync NEVER auto-creates a native repo behind the user's back.
func TestInboundNotImportedNoOp(t *testing.T) {
	app := mountApp(t)
	_ = liveServer(t, app)
	root := t.TempDir()
	srcBase := serveGitHTTP(t, root)
	src := newSource(t, root, srcBase, "ghost", "v1")
	ctx := context.Background()

	res, err := (githubImporter{}).InboundSync(ctx, cloud.GitInboundReq{
		Org: "acme", Repo: "ghost", Branch: "main", CloneURL: src.url, Origin: hostFromURL(t, src.url),
	})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.NoOp {
		t.Fatalf("un-imported inbound must be NoOp, got %+v", res)
	}
	store, _ := storeFor(mounted.Load(), "acme")
	if _, err := store.Get(ctx, "acme", "", "ghost"); !errors.Is(err, errNotFound) {
		t.Fatalf("inbound must not auto-create the repo; Get err = %v", err)
	}
}
