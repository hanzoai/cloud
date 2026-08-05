package lsp

// workspace_test.go tests the pool as the arithmetic it is — an injected opener
// and an injected clock, no git, no toolchain, no wall clock — and the input
// narrowing that keeps one tenant's query inside that tenant's checkout.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// clock is a hand-advanced time source, so a TTL test states the elapsed time it
// means instead of sleeping for it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// testPool builds a pool whose opener makes a Tree with a real directory (so
// close() has something to remove) and no server process.
func testPool(t *testing.T, max int, ttl time.Duration) (*pool, *clock, func() int) {
	t.Helper()
	clk := &clock{t: time.Unix(1<<30, 0)}
	root := t.TempDir()

	var mu sync.Mutex
	var opened int

	p := newPool(func(_ context.Context, k key, _ string) (*Tree, error) {
		mu.Lock()
		opened++
		n := opened
		mu.Unlock()
		dir := filepath.Join(root, k.org, k.repo, k.rev, string(rune('a'+n%26)))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		return &Tree{key: k, dir: dir, lang: table["go"]}, nil
	})
	p.max, p.ttl, p.now = max, ttl, clk.now
	t.Cleanup(p.closeAll)

	return p, clk, func() int {
		mu.Lock()
		defer mu.Unlock()
		return opened
	}
}

func get(t *testing.T, p *pool, k key) (*Tree, bool) {
	t.Helper()
	tree, cold, err := p.get(context.Background(), k, "main.go")
	if err != nil {
		t.Fatalf("get %v: %v", k, err)
	}
	return tree, cold
}

// TestWarmWorkspaceIsReused: the second query for the same (org, repo, rev) must
// not check out again. This is the whole reason the pool exists, and it is also
// what the billing model rests on — only a cold start is charged.
func TestWarmWorkspaceIsReused(t *testing.T) {
	p, _, opened := testPool(t, 4, time.Hour)
	k := key{org: "acme", repo: "cloud", rev: "main"}

	if _, cold := get(t, p, k); !cold {
		t.Fatal("first get was not reported as a cold start")
	}
	if _, cold := get(t, p, k); cold {
		t.Fatal("second get reported a cold start — the workspace was not reused")
	}
	if opened() != 1 {
		t.Fatalf("opened %d workspaces for one key, want 1", opened())
	}
}

// TestIdleWorkspaceIsEvictedAtTTL: a workspace holds a subprocess and a checkout,
// so an idle one must not hold them forever.
func TestIdleWorkspaceIsEvictedAtTTL(t *testing.T) {
	p, clk, opened := testPool(t, 4, 10*time.Minute)
	k := key{org: "acme", repo: "cloud", rev: "main"}

	get(t, p, k)
	clk.advance(11 * time.Minute)

	if _, cold := get(t, p, k); !cold {
		t.Fatal("workspace survived its idle TTL")
	}
	if opened() != 2 {
		t.Fatalf("opened %d, want 2 (evicted then rebuilt)", opened())
	}
}

// TestUseKeepsAWorkspaceWarm: the TTL is on IDLE time, so a workspace queried
// steadily must never be evicted out from under its caller.
func TestUseKeepsAWorkspaceWarm(t *testing.T) {
	p, clk, opened := testPool(t, 4, 10*time.Minute)
	k := key{org: "acme", repo: "cloud", rev: "main"}

	get(t, p, k)
	for range 5 {
		clk.advance(6 * time.Minute) // past half the TTL, never past all of it
		if _, cold := get(t, p, k); cold {
			t.Fatal("an actively used workspace was evicted")
		}
	}
	if opened() != 1 {
		t.Fatalf("opened %d, want 1", opened())
	}
}

// TestPoolEvictsLeastRecentlyUsed: over the cap, the workspace nobody has touched
// goes first — not an arbitrary one.
func TestPoolEvictsLeastRecentlyUsed(t *testing.T) {
	p, clk, _ := testPool(t, 2, time.Hour)
	a := key{org: "acme", repo: "a", rev: "main"}
	b := key{org: "acme", repo: "b", rev: "main"}
	c := key{org: "acme", repo: "c", rev: "main"}

	get(t, p, a)
	clk.advance(time.Minute)
	get(t, p, b)
	clk.advance(time.Minute)
	get(t, p, a) // a is now the most recent, b the least
	clk.advance(time.Minute)
	get(t, p, c) // over the cap: b must go

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.warm) != 2 {
		t.Fatalf("pool holds %d workspaces, want max 2", len(p.warm))
	}
	if _, ok := p.warm[b]; ok {
		t.Error("evicted something other than the least-recently-used workspace")
	}
	if _, ok := p.warm[a]; !ok {
		t.Error("evicted the most-recently-used workspace")
	}
}

// TestOrgsDoNotShareAWorkspace is the isolation claim at the pool layer: the same
// repo name and revision in two orgs are two keys, two directories and two
// servers. Nothing about a warm workspace can be reached across the tenant
// boundary because there is no shared entry to reach.
func TestOrgsDoNotShareAWorkspace(t *testing.T) {
	p, _, opened := testPool(t, 4, time.Hour)

	one, _ := get(t, p, key{org: "acme", repo: "cloud", rev: "main"})
	two, cold := get(t, p, key{org: "other", repo: "cloud", rev: "main"})

	if !cold {
		t.Fatal("a second org was served the first org's warm workspace")
	}
	if one.dir == two.dir {
		t.Fatalf("two orgs share a checkout directory %q", one.dir)
	}
	if opened() != 2 {
		t.Fatalf("opened %d, want 2", opened())
	}
}

// TestConcurrentGetsOpenOnce: several requests racing for the same cold workspace
// must produce ONE checkout, not N competing clones into one directory.
func TestConcurrentGetsOpenOnce(t *testing.T) {
	p, _, opened := testPool(t, 4, time.Hour)
	k := key{org: "acme", repo: "cloud", rev: "main"}

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.get(context.Background(), k, "main.go")
		}()
	}
	wg.Wait()

	if opened() != 1 {
		t.Fatalf("opened %d workspaces concurrently for one key, want 1", opened())
	}
}

// TestDropInvalidatesOneRepo: a push makes a branch checkout stale, and drop is
// what releases it — without touching another repo or another org.
func TestDropInvalidatesOneRepo(t *testing.T) {
	p, _, _ := testPool(t, 4, time.Hour)
	stale := key{org: "acme", repo: "cloud", rev: "main"}
	other := key{org: "acme", repo: "other", rev: "main"}
	foreign := key{org: "rival", repo: "cloud", rev: "main"}

	get(t, p, stale)
	get(t, p, other)
	get(t, p, foreign)

	p.drop("acme", "cloud")

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.warm[stale]; ok {
		t.Error("drop left the invalidated workspace warm")
	}
	if _, ok := p.warm[other]; !ok {
		t.Error("drop evicted a different repo")
	}
	if _, ok := p.warm[foreign]; !ok {
		t.Error("drop reached across the org boundary")
	}
}

// TestFailedOpenIsNotCached: a checkout that failed must not be remembered as a
// workspace, or one transient forge outage poisons the key until the TTL.
func TestFailedOpenIsNotCached(t *testing.T) {
	p := newPool(func(context.Context, key, string) (*Tree, error) {
		return nil, errors.New("forge unreachable")
	})
	t.Cleanup(p.closeAll)

	k := key{org: "acme", repo: "cloud", rev: "main"}
	if _, _, err := p.get(context.Background(), k, "main.go"); err == nil {
		t.Fatal("get succeeded despite a failing opener")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.warm) != 0 {
		t.Fatalf("a failed open left %d entries in the pool", len(p.warm))
	}
}

// ── input narrowing ──────────────────────────────────────────────────────────

// TestCleanRefusesEscapes is the containment proof, and the symlink cases are the
// ones that matter: a checkout is TENANT-CONTROLLED content, so a repo can contain
// a symlink pointing anywhere. A lexical ".." check alone would pass every one of
// these.
func TestCleanRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	outside := filepath.Join(elsewhere, "secret.txt")
	if err := os.WriteFile(outside, []byte("another tenant's data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file symlink out of the tree, and a directory symlink out of the tree.
	if err := os.Symlink(outside, filepath.Join(dir, "escape.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "away")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"../../etc/passwd",  // lexical traversal
		"/etc/passwd",       // absolute
		"escape.go",         // symlink to a file outside
		"away/secret.txt",   // through a symlinked directory
		"",                  // empty
		"nope.go",           // absent
		".",                 // the directory itself
		"../" + "elsewhere", // traversal by name
	} {
		if got, err := clean(dir, path); err == nil {
			t.Errorf("clean(%q) allowed %q — it must stay inside the checkout", path, got)
		}
	}

	// The honest case still works.
	got, err := clean(dir, "main.go")
	if err != nil {
		t.Fatalf("clean rejected a legitimate path: %v", err)
	}
	if filepath.Base(got) != "main.go" {
		t.Errorf("clean returned %q", got)
	}
}

// TestRevisionRefusesAFlag: a revision is passed to `git fetch` in the ref
// position, so one beginning with "-" would be read as an OPTION.
// --upload-pack=… there is command execution.
func TestRevisionRefusesAFlag(t *testing.T) {
	for _, bad := range []string{
		"--upload-pack=/bin/sh",
		"-x",
		"main;rm -rf /",
		"main branch",
		"$(whoami)",
		"--",
	} {
		if revision.MatchString(bad) {
			t.Errorf("revision accepted %q", bad)
		}
	}
	for _, ok := range []string{"main", "v1.2.3", "release/2026-01", "a1b2c3d4e5f6"} {
		if !revision.MatchString(ok) {
			t.Errorf("revision rejected the legitimate %q", ok)
		}
	}
}

// TestSlugRefusesAPath: the repo slug becomes a URL segment and a directory name.
// A slug carrying a slash could name another org's repository on the forge; one
// carrying ".." could climb out of the org's data directory.
func TestSlugRefusesAPath(t *testing.T) {
	for _, bad := range []string{
		"rival/private", "../../etc", "..", ".", "a b",
		"https://evil.example/x", "-x", "", "repo.git/../../other",
	} {
		if slug.MatchString(bad) {
			t.Errorf("slug accepted %q", bad)
		}
	}
	for _, ok := range []string{"cloud", "hanzo-node", "go.mod-tools", "a"} {
		if !slug.MatchString(ok) {
			t.Errorf("slug rejected the legitimate %q", ok)
		}
	}
}

// TestScriptsAreOff is the policy, asserted rather than described: no language
// whose dependency fetch executes dependency-authored code may run that fetch.
func TestScriptsAreOff(t *testing.T) {
	for name, l := range table {
		if l.Executes && fetchable(l) {
			t.Errorf("%s: a fetch that runs dependency code is enabled (%v)", name, l.Fetch)
		}
	}
	if !fetchable(table["typescript"]) {
		t.Error("the npm fetch is disabled, but --ignore-scripts makes it safe and it is needed for .d.ts")
	}
	if fetchable(table["python"]) {
		t.Error("the python fetch builds sdists, which runs setup.py as us")
	}
	// npm must never be invoked without --ignore-scripts.
	npm := table["typescript"].Fetch
	var guarded bool
	for _, a := range npm {
		if a == "--ignore-scripts" {
			guarded = true
		}
	}
	if !guarded {
		t.Errorf("npm fetch %v is missing --ignore-scripts", npm)
	}
	// rust-analyzer must be told not to run build scripts or expand proc macros:
	// the server does at load time exactly what `cargo fetch` was chosen to avoid.
	rust := table["rust"].Init
	cargo, _ := rust["cargo"].(map[string]any)
	scripts, _ := cargo["buildScripts"].(map[string]any)
	if scripts["enable"] != false {
		t.Error("rust-analyzer may run build.rs — cargo.buildScripts.enable is not false")
	}
	proc, _ := rust["procMacro"].(map[string]any)
	if proc["enable"] != false {
		t.Error("rust-analyzer may expand proc macros — procMacro.enable is not false")
	}
}

// TestLangForIsDeterministic: map iteration is randomized, so a table walked
// directly would resolve a file to different servers on different requests.
func TestLangForIsDeterministic(t *testing.T) {
	for range 50 {
		l, ok := langFor("apps/lsp/server.go")
		if !ok || l.Name != "go" {
			t.Fatalf("langFor(.go) = %q, %v", l.Name, ok)
		}
	}
	if _, ok := langFor("README"); ok {
		t.Error("langFor matched a file with no extension")
	}
	if _, ok := langFor("notes.txt"); ok {
		t.Error("langFor matched an unknown extension")
	}
}

// TestRootForStopsAtTheCheckout: the deepest marker wins, and the walk may never
// climb above the checkout — a marker outside the tenant's tree must never root a
// server.
func TestRootForStopsAtTheCheckout(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "svc", "api")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{dir, filepath.Join(dir, "svc")} {
		if err := os.WriteFile(filepath.Join(at, "go.mod"), []byte("module x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := rootFor(dir, "svc/api/main.go", table["go"])
	if want := filepath.Join(dir, "svc"); got != want {
		t.Errorf("rootFor = %q, want the deepest marker %q", got, want)
	}
	if got := rootFor(dir, "main.go", table["go"]); got != dir {
		t.Errorf("rootFor = %q, want the checkout root %q", got, dir)
	}
	// No marker anywhere: the checkout root, never a parent.
	bare := t.TempDir()
	if got := rootFor(bare, "a/b/c.go", table["go"]); got != bare {
		t.Errorf("rootFor = %q, want %q — the walk must not climb out", got, bare)
	}
}

// TestMethodsAreAClosedSet: the door forwards a fixed list, so an arbitrary
// string never reaches a language server.
func TestMethodsAreAClosedSet(t *testing.T) {
	for _, m := range methods {
		if !known(m) {
			t.Errorf("known(%q) = false for a listed method", m)
		}
	}
	for _, m := range []string{"", "workspace/executeCommand", "shutdown", "exit", "HOVER"} {
		if known(m) {
			t.Errorf("known(%q) = true for an unlisted method", m)
		}
	}
}

// TestLanguageIDNamesTheDialect: TypeScript's server gets the wrong answer for a
// React file told it is plain TypeScript.
func TestLanguageIDNamesTheDialect(t *testing.T) {
	ts := table["typescript"]
	for path, want := range map[string]string{
		"a.ts": "typescript", "a.tsx": "typescriptreact",
		"a.js": "javascript", "a.jsx": "javascriptreact",
	} {
		if got := ts.ID(path); got != want {
			t.Errorf("ID(%q) = %q, want %q", path, got, want)
		}
	}
	if got := table["go"].ID("main.go"); got != "go" {
		t.Errorf("ID(main.go) = %q, want go", got)
	}
}
