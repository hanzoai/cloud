package git

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hanzoai/cloud"
)

// TestRESTPushCreatesCommit proves the client-less push: POST files, a commit is
// built + the ref advanced, a second push updates the ref (with the first as
// parent), and the build hook fires on each push. Then a real git clone sees the
// pushed content — proving corePush wrote a valid commit/tree/blob graph.
func TestRESTPushCreatesCommit(t *testing.T) {
	var mu sync.Mutex
	var events []cloud.GitPushEvent
	cloud.RegisterPushBuilder(func(_ context.Context, ev cloud.GitPushEvent) error {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { cloud.RegisterPushBuilder(nil) })

	app := mountApp(t)
	base := liveServer(t, app)

	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "app"}); code != http.StatusCreated {
		t.Fatalf("create repo: %d %s", code, b)
	}

	// First push: two files, one nested, one base64.
	code, body := do(t, app, http.MethodPost, "/v1/git/repos/app/push", "acme", map[string]any{
		"branch":  "main",
		"message": "initial",
		"files": []map[string]any{
			{"path": "README.md", "content": "# hello\n"},
			{"path": "src/app.js", "content": "console.log(1)\n"},
			{"path": "logo.bin", "content": base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3}), "encoding": "base64"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("push want 200, got %d (%s)", code, body)
	}
	var r1 pushResp
	if err := json.Unmarshal(body, &r1); err != nil {
		t.Fatalf("push resp json: %v (%s)", err, body)
	}
	if r1.Commit == "" || r1.Branch != "main" {
		t.Fatalf("unexpected push resp: %+v", r1)
	}
	if r1.CloneURL != "https://api.hanzo.test/v1/git/acme/app.git" {
		t.Fatalf("unexpected cloneUrl: %q", r1.CloneURL)
	}
	if r1.SSHURL != "git@git.hanzo.test:acme/app.git" {
		t.Fatalf("unexpected sshUrl: %q", r1.SSHURL)
	}

	// Second push: updates one file — the ref advances, first commit is the parent.
	code, body = do(t, app, http.MethodPost, "/v1/git/repos/app/push", "acme", map[string]any{
		"branch":  "main",
		"message": "update",
		"files":   []map[string]any{{"path": "README.md", "content": "# hello v2\n"}},
	})
	if code != http.StatusOK {
		t.Fatalf("second push want 200, got %d (%s)", code, body)
	}
	var r2 pushResp
	_ = json.Unmarshal(body, &r2)
	if r2.Commit == r1.Commit {
		t.Fatalf("second push must produce a new commit, got same %s", r2.Commit)
	}

	// Both pushes fired the build hook, with the right branch + tip commit.
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("want 2 build events, got %d: %+v", len(events), events)
	}
	if events[0].Repo != "app" || events[0].Branch != "main" || events[0].Commit != r1.Commit {
		t.Fatalf("event[0] = %+v (want commit %s)", events[0], r1.Commit)
	}
	if events[1].Commit != r2.Commit {
		t.Fatalf("event[1].Commit = %s, want %s", events[1].Commit, r2.Commit)
	}

	// A real clone sees the merged content: updated README + the untouched nested
	// file + the base64 file survive the second push (merge, not replace).
	cloneURL := base + "/v1/git/acme/app.git"
	asTenant("acme")
	cloned, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: cloneURL})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	head, err := cloned.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Hash().String() != r2.Commit {
		t.Fatalf("cloned HEAD %s != last push %s", head.Hash(), r2.Commit)
	}
	commit, err := cloned.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit obj: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	// README updated.
	readme, err := tree.File("README.md")
	if err != nil {
		t.Fatalf("README.md missing after 2nd push: %v", err)
	}
	rc, _ := readme.Contents()
	if rc != "# hello v2\n" {
		t.Fatalf("README content = %q, want updated", rc)
	}
	// Nested file from the FIRST push survived (merge onto base tree).
	if _, err := tree.File("src/app.js"); err != nil {
		t.Fatalf("src/app.js should survive the second push (merge): %v", err)
	}
	// Base64 file decoded to raw bytes.
	logo, err := tree.File("logo.bin")
	if err != nil {
		t.Fatalf("logo.bin missing: %v", err)
	}
	lc, _ := logo.Contents()
	if lc != string([]byte{0, 1, 2, 3}) {
		t.Fatalf("logo.bin content = %q, want raw bytes", lc)
	}
}

// TestRESTPushCreatesRepoOnFirstPush proves a push to a not-yet-existing repo
// provisions it (composing the ONE provision path), and the repo is then listed.
func TestRESTPushCreatesRepoOnFirstPush(t *testing.T) {
	app := mountApp(t)

	code, body := do(t, app, http.MethodPost, "/v1/git/repos/fresh/push", "acme", map[string]any{
		"files": []map[string]any{{"path": "main.go", "content": "package main\n"}},
	})
	if code != http.StatusOK {
		t.Fatalf("push to fresh repo want 200, got %d (%s)", code, body)
	}
	// The repo now exists in the tenant listing.
	code, body = do(t, app, http.MethodGet, "/v1/git/repos", "acme", nil)
	var listed struct {
		Data []repoView `json:"data"`
	}
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 1 || listed.Data[0].Name != "fresh" {
		t.Fatalf("fresh repo not created by push: %d %+v", code, listed.Data)
	}
}

// TestRESTPushValidation covers the input guards: empty files, bad branch,
// traversing path, and bad base64.
func TestRESTPushValidation(t *testing.T) {
	app := mountApp(t)
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "v"}); code != http.StatusCreated {
		t.Fatal("setup create failed")
	}

	cases := []map[string]any{
		{"files": []map[string]any{}}, // empty files
		{"branch": "bad branch", "files": []map[string]any{{"path": "a", "content": "x"}}}, // bad branch
		{"files": []map[string]any{{"path": "../escape", "content": "x"}}},                 // traversal
		{"files": []map[string]any{{"path": "a", "content": "!!!", "encoding": "base64"}}}, // bad base64
	}
	for i, c := range cases {
		if code, body := do(t, app, http.MethodPost, "/v1/git/repos/v/push", "acme", c); code != http.StatusBadRequest {
			t.Fatalf("case %d want 400, got %d (%s)", i, code, body)
		}
	}
}

// TestRESTPushCrossTenantIsolation proves a push is org-scoped: beta pushing to a
// repo name acme owns creates beta's OWN repo, never touching acme's.
func TestRESTPushCrossTenantIsolation(t *testing.T) {
	app := mountApp(t)
	// acme owns "shared".
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "shared"}); code != http.StatusCreated {
		t.Fatal("acme create failed")
	}
	// beta pushes to "shared" — provisions beta's own, does not reach acme's.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/shared/push", "beta", map[string]any{
		"files": []map[string]any{{"path": "b.txt", "content": "beta\n"}},
	}); code != http.StatusOK {
		t.Fatalf("beta push want 200, got %d %s", code, b)
	}
	// acme's repo is still empty (no HEAD) — beta never wrote into it.
	branches, head := mounted.refState("acme", "", "shared")
	if head != "" || len(branches) != 0 {
		t.Fatalf("acme repo must be untouched, got head=%q branches=%v", head, branches)
	}
	// beta's repo has the commit.
	_, betaHead := mounted.refState("beta", "", "shared")
	if betaHead == "" {
		t.Fatalf("beta repo should have a HEAD after its push")
	}
}
