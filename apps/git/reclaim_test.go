package git

// The bound, proved the way refwriters_wire_test.go proves the ref policy: with
// the real client, against the real server, doing the thing the volume actually
// does.
//
// The property under test is not "a number went down". It is that releasing a
// repo NEVER loses one — the capability keeps working across an eviction, and a
// repo nothing can fetch back is never a candidate however cold or large it is.
// Those two are the whole design, so each has a test that fails if it stops
// holding.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// service reaches the mounted service so a test can state the policy the way an
// operator states it — a byte ceiling — rather than by waiting out the idle
// window.
func service(t *testing.T) *cloud.Service[state] {
	t.Helper()
	s := mounted.Load()
	if s == nil {
		t.Fatal("no mounted service")
	}
	return s
}

// bound states the ceiling and shortens the idle window, and returns the cache.
// Shortening idle is not a shortcut around the safety criterion: the tests below
// still have to be past it, they just do not have to take five minutes about it.
func bound(t *testing.T, bytes int64, idle time.Duration) *cache {
	t.Helper()
	c := service(t).State.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bound, c.idle = bytes, idle
	return c
}

// mirroredIn creates repo `name` in org `acme` by mirroring a real bare repo
// served over git smart-HTTP — the one door that records an origin, and a real
// upstream so a release is genuinely reversible rather than reversible in a stub.
func mirroredIn(t *testing.T, app *zip.App, name, content string) (url, commit string) {
	t.Helper()
	root := t.TempDir()
	commit = gitSource(t, root, name, content, "")
	url = serveGitHTTP(t, root) + "/" + name + ".git"
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/"+name+"/mirror", "acme",
		map[string]any{"name": name, "source": url}); code != http.StatusOK {
		t.Fatalf("mirror %s: %d %s", name, code, b)
	}
	return url, commit
}

// touch enters a repo into the cache the way a serve does and lets the reader go
// again, WITHOUT running reclaim — so a test states the policy and then decides
// when the pass happens, instead of racing the one materialize() runs on release.
func touch(t *testing.T, c *cache, s *cloud.Service[state], org, project, name string) {
	t.Helper()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Get(context.Background(), org, project, name)
	if err != nil {
		t.Fatal(err)
	}
	if r.SizeBytes == 0 {
		if n, err := s.State.storage.sizeBytes(org, project, name); err == nil {
			r.SizeBytes = n
		}
	}
	c.enter(org, project, name, r.Origin, r.SizeBytes)()
}

// ── the two properties ───────────────────────────────────────────────────────

// A MIRRORED repo can be fetched again, so releasing it costs a refetch — and
// the clone after the release must still answer with the same commit. This is
// the capability proof: an evicted repo is SLOW, never missing.
func TestAReleasedRepoIsFetchedBackAndServesTheSameCommit(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	_, commit := mirroredIn(t, app, "code", "# mirrored\n")

	s := service(t)
	dir := s.State.storage.absRepoPath("acme", "", "code")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the mirror should be on disk: %v", err)
	}

	// A ceiling of one byte with the idle window already past: the repo has an
	// origin, so it is a candidate and must go.
	c := bound(t, 1, 0)
	touch(t, c, s, "acme", "", "code")
	c.reclaim(context.Background(), s)

	if n := c.evictions.Load(); n != 1 {
		t.Fatalf("want exactly one release, got %d (pinned=%d)", n, c.pinned.Load())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the release must remove the directory, stat: %v", err)
	}

	// THE POINT. A real git clone, after the release, still gets the commit.
	dst := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", base+"/v1/git/acme/code.git", dst)...)
	if got := gitOut(t, dst, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("after a release the clone gave %s, want %s", got, commit)
	}
}

// A repo with NO origin is the only copy of itself, so the bound must yield to
// it. This is the half that protects the 553 repos on the live volume that no
// mirror brought in — they carry no origin and nothing can infer one.
func TestARepoWithNoOriginIsNeverReleased(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "only"}); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	// Give it real content through the client-less push, so this is a repo with
	// something to lose rather than an empty directory.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/only/push", "acme", map[string]any{
		"branch": "main", "message": "the only copy",
		"files": []map[string]any{{"path": "a.txt", "content": "irreplaceable\n"}},
	}); code != http.StatusOK {
		t.Fatalf("push: %d %s", code, b)
	}

	s := service(t)
	dir := s.State.storage.absRepoPath("acme", "", "only")
	c := bound(t, 1, 0)
	touch(t, c, s, "acme", "", "only")
	c.reclaim(context.Background(), s)

	if n := c.evictions.Load(); n != 0 {
		t.Fatalf("a repo with no origin was released %d times", n)
	}
	if c.pinned.Load() == 0 {
		t.Fatal("over the bound with nothing releasable must be SAID, not silent")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the only copy must still be on disk: %v", err)
	}
}

// ── the safety criterion, both halves ────────────────────────────────────────

// A repo somebody is inside is not a candidate, however cold the clock says it
// is. This is the half that stops a release landing under a streaming clone.
func TestARepoWithALiveReaderIsNeverReleased(t *testing.T) {
	app := mountApp(t)
	mirroredIn(t, app, "busy", "# busy\n")

	s := service(t)
	c := bound(t, 1, 0)

	store, err := storeFor(s, "acme")
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Get(context.Background(), "acme", "", "busy")
	if err != nil {
		t.Fatal(err)
	}
	// Hold a reader, exactly as a clone in flight does.
	dir, done, err := materialize(context.Background(), s, r)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// Backdate it past the idle window so ONLY the reader is keeping it.
	c.mu.Lock()
	c.repos["acme//busy"].used = time.Now().Add(-time.Hour)
	c.mu.Unlock()

	c.reclaim(context.Background(), s)
	if n := c.evictions.Load(); n != 0 {
		t.Fatalf("released a repo with a live reader (%d)", n)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the directory went away under its reader: %v", err)
	}

	// Once the reader leaves it becomes a candidate, which is what proves the
	// refusal above was the READER and not something else declining.
	done()
	c.reclaim(context.Background(), s)
	if c.evictions.Load() != 1 {
		t.Fatalf("want one release after the reader left, got %d", c.evictions.Load())
	}
}

// A repo touched inside the idle window is not a candidate either — materialize
// hands back a bare path its caller uses after the call returns, so recency is
// the only thing standing between a release and a use-after-free.
func TestARecentlyUsedRepoIsNeverReleased(t *testing.T) {
	app := mountApp(t)
	mirroredIn(t, app, "warm", "# warm\n")

	s := service(t)
	c := bound(t, 1, time.Hour) // an hour of idle required; it was just used
	touch(t, c, s, "acme", "", "warm")

	c.reclaim(context.Background(), s)
	if n := c.evictions.Load(); n != 0 {
		t.Fatalf("released a repo used moments ago (%d)", n)
	}
	if c.pinned.Load() == 0 {
		t.Fatal("held above the bound, which must be reported")
	}
}

// The default is UNBOUNDED, because a binary rolling out must not start deleting
// repositories nobody asked it to delete.
func TestTheBoundIsOffUntilAnOperatorPicksANumber(t *testing.T) {
	if got := cacheBytes(); got != 0 {
		t.Fatalf("with %s unset the bound must be 0, got %d", cacheEnv, got)
	}
	for _, v := range []string{"", "nonsense", "-1"} {
		t.Setenv(cacheEnv, v)
		if got := cacheBytes(); got != 0 {
			t.Fatalf("%s=%q must read as unbounded, got %d", cacheEnv, v, got)
		}
	}
	t.Setenv(cacheEnv, "20971520")
	if got := cacheBytes(); got != 20971520 {
		t.Fatalf("want 20971520, got %d", got)
	}

	app := mountApp(t)
	mirroredIn(t, app, "kept", "# kept\n")
	s := service(t)
	c := s.State.cache
	c.mu.Lock()
	c.bound, c.idle = 0, 0 // unbounded, and nothing is too recent
	c.mu.Unlock()
	touch(t, c, s, "acme", "", "kept")
	c.reclaim(context.Background(), s)
	if n := c.evictions.Load(); n != 0 {
		t.Fatalf("an unbounded store released %d repos", n)
	}
}

// ── the capability, end to end, ACROSS a release ─────────────────────────────

// A push is still a deploy after the repo has been released and fetched back.
// The clone, the push and the hook are the three things the bound may not cost,
// so all three are exercised with the real git CLI against the real server, with
// an eviction in the middle.
//
// THIS TEST FOUND THE DEFECT THE DESIGN HAD. The first version of the bound
// released a repo on the ORIGIN alone, so a push landed, the reader let go,
// reclaim released the repo, and the next clone refetched the upstream and
// answered with ITS tip — the pushed commit gone, 200 OK, nothing logged.
// fireBranchBuild ends the copy now, and the last two assertions here are what
// hold that: after a push the repo is PINNED, and the pushed commit survives.
func TestPushIsStillADeployAfterARelease(t *testing.T) {
	var mu sync.Mutex
	var events []cloud.GitPushEvent
	cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) (int, error) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		return 0, nil
	})
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	app := mountApp(t)
	base := liveServer(t, app)
	_, seeded := mirroredIn(t, app, "deployed", "# deployed\n")
	url := base + "/v1/git/acme/deployed.git"

	// Release it while nothing is looking.
	s := service(t)
	c := bound(t, 1, 0)
	touch(t, c, s, "acme", "", "deployed")
	c.reclaim(context.Background(), s)
	if c.evictions.Load() != 1 {
		t.Fatalf("setup: want the repo released, evictions=%d pinned=%d", c.evictions.Load(), c.pinned.Load())
	}

	// Clone it back with the real client. The refetch happens inside the
	// advertisement, before a single object is negotiated.
	work := filepath.Join(t.TempDir(), "work")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", url, work)...)
	if got := gitOut(t, work, "rev-parse", "HEAD"); got != seeded {
		t.Fatalf("clone after release gave %s, want the mirrored tip %s", got, seeded)
	}

	// Push a new commit over smart-HTTP.
	if err := os.WriteFile(filepath.Join(work, "NEW.md"), []byte("# after the release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "-c", "user.email=t@hanzo.test", "-c", "user.name=t", "commit", "-q", "-m", "after")
	pushed := gitOut(t, work, "rev-parse", "HEAD")
	gitRun(t, work, append(orgHeaderArgs("acme"), "push", "-q", "origin", "main")...)

	// THE HOOK. A push is a deploy, on the branch and the commit that landed.
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("want exactly one push event across the release, got %d: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Org != "acme" || ev.Repo != "deployed" || ev.Ref != "refs/heads/main" || ev.Commit != pushed {
		t.Fatalf("hook fired with the wrong event: %+v (want commit %s)", ev, pushed)
	}

	// The push ENDED THE COPY: this repo now holds a commit the upstream does
	// not, so its origin is cleared and reclaim may never release it again.
	store, err := storeFor(s, "acme")
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.Get(context.Background(), "acme", "", "deployed")
	if err != nil {
		t.Fatal(err)
	}
	if row.Origin != "" {
		t.Fatalf("a pushed-to repo still claims an origin (%q) — a refetch would "+
			"discard the pushed commit", row.Origin)
	}
	before := c.evictions.Load()
	touch(t, c, s, "acme", "", "deployed")
	c.reclaim(context.Background(), s)
	if got := c.evictions.Load(); got != before {
		t.Fatalf("released a repo holding an unreplicable commit (%d releases past %d)", got, before)
	}

	// And the pushed commit survives a fresh clone, so the refetched repo is a
	// real repository and not a shell that accepted a pack into nothing.
	dst := filepath.Join(t.TempDir(), "verify")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", url, dst)...)
	if got := gitOut(t, dst, "rev-parse", "HEAD"); got != pushed {
		t.Fatalf("re-clone gave %s, want the pushed commit %s", got, pushed)
	}
}

// ── the origin itself ────────────────────────────────────────────────────────

// A mirror that SUCCEEDED records where it fetched from, and a repo created any
// other way records nothing. That difference is the whole eviction rule, so it
// is asserted rather than assumed.
func TestOnlyAMirrorRecordsAnOrigin(t *testing.T) {
	app := mountApp(t)
	url, _ := mirroredIn(t, app, "fromupstream", "# up\n")
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "native"}); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}

	store, err := storeFor(service(t), "acme")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "acme", "", "fromupstream")
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin != url {
		t.Fatalf("mirror origin = %q, want %q", got.Origin, url)
	}
	native, err := store.Get(context.Background(), "acme", "", "native")
	if err != nil {
		t.Fatal(err)
	}
	if native.Origin != "" {
		t.Fatalf("a natively created repo must record no origin, got %q", native.Origin)
	}
}

// A credential in a source URL must not reach the row. mirrorSource strips
// userinfo before the fetch, and this is the assertion that keeps it stripped —
// the origin is persisted, so a regression there would write a password to disk.
func TestARecordedOriginCarriesNoCredential(t *testing.T) {
	app := mountApp(t)
	root := t.TempDir()
	gitSource(t, root, "creds", "# creds\n", "")
	src := serveGitHTTP(t, root)
	withUser := "http://user:hunter2@" + src[len("http://"):] + "/creds.git"

	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/creds/mirror", "acme",
		map[string]any{"name": "creds", "source": withUser}); code != http.StatusOK {
		t.Fatalf("mirror: %d %s", code, b)
	}
	store, err := storeFor(service(t), "acme")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "acme", "", "creds")
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin == "" {
		t.Fatal("no origin recorded")
	}
	if strings.Contains(got.Origin, "hunter2") || strings.Contains(got.Origin, "user:") {
		t.Fatalf("a credential reached the stored origin: %q", got.Origin)
	}
}

// N callers missing at the same instant cost ONE fetch, and every one of them
// gets a working repository. Without the single flight the second caller runs
// `git init --bare` over the directory the first just made, which is an error,
// and past that two `git fetch` share one objects directory.
func TestConcurrentMissesCostOneFetch(t *testing.T) {
	app := mountApp(t)
	_, commit := mirroredIn(t, app, "hot", "# hot\n")

	s := service(t)
	c := bound(t, 1, 0)
	touch(t, c, s, "acme", "", "hot")
	c.reclaim(context.Background(), s)
	if c.evictions.Load() != 1 {
		t.Fatalf("setup: want it released, got %d", c.evictions.Load())
	}
	// Take the bound away so the winner's own release does not race the losers.
	c.mu.Lock()
	c.bound = 0
	c.mu.Unlock()

	store, err := storeFor(s, "acme")
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Get(context.Background(), "acme", "", "hot")
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	dirs := make([]string, callers)
	wg.Add(callers)
	for i := range callers {
		go func() {
			defer wg.Done()
			dir, done, err := materialize(context.Background(), s, r)
			if err != nil {
				errs[i] = err
				return
			}
			defer done()
			dirs[i] = dir
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d failed a concurrent miss: %v", i, err)
		}
	}
	// Every caller got a real repository holding the mirrored commit.
	for i, dir := range dirs {
		if got := gitOut(t, "", "--git-dir="+dir, "rev-parse", "HEAD"); got != commit {
			t.Fatalf("caller %d saw %s, want %s", i, got, commit)
		}
	}
}
