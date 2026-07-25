package git

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// import_test.go proves the "adopt as primary" spine end to end: POST
// /v1/git/repos/:name/import mirrors an upstream in, injects native .hanzo/workflows
// CI (synced from the repo's GitHub CI, or generated), and folds the repo into the
// code index — all through the endpoints that already exist, composed once.

// multiSource builds a bare git source <root>/<name>.git holding one commit whose
// tree is `files` (slash-path → content), returning nothing (the caller serves root
// over HTTP). Reuses the git-CLI helpers the mirror tests use (gitcli_test.go).
func multiSource(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	for p, content := range files {
		full := filepath.Join(work, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "seed")
	gitRun(t, "", "clone", "-q", "--bare", work, filepath.Join(root, name+".git"))
}

// TestImportInjectsNativeCIAndIndexes: a repo with GitHub CI + Go code is imported;
// its .github/workflows is synced to .hanzo/workflows (runner retargeted, original
// preserved) and the whole tree — code AND injected workflow — is indexed.
func TestImportInjectsNativeCIAndIndexes(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)

	// Capture what the code plane is asked to index — proof that import indexes.
	indexed := make(chan []IndexedFile, 4)
	SetIndexer(func(_ context.Context, _, _, _, _ string, files []IndexedFile) error {
		indexed <- files
		return nil
	})
	t.Cleanup(func() { SetIndexer(nil) })

	srcRoot := t.TempDir()
	multiSource(t, srcRoot, "widget", map[string]string{
		".github/workflows/ci.yml": "name: CI\non:\n  push:\n    branches: [main]\njobs:\n" +
			"  build:\n    runs-on: ubuntu-latest\n    steps:\n" +
			"      - uses: actions/checkout@v4\n      - run: go test ./...\n",
		"go.mod":  "module example.com/widget\n\ngo 1.22\n",
		"main.go": "package main\n\n// SpindleWidget is a distinctive symbol for the search proof.\nfunc SpindleWidget() string { return \"hi\" }\n",
	})
	srcBase := serveGitHTTP(t, srcRoot)

	asTenant("acme")
	code, body := do(t, app, http.MethodPost, "/v1/git/repos/widget/import", "acme",
		map[string]any{"source": srcBase + "/widget.git"})
	if code != http.StatusOK {
		t.Fatalf("import want 200, got %d (%s)", code, body)
	}
	var v importView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("import json: %v (%s)", err, body)
	}
	if !v.Imported || v.Org != "acme" || v.Name != "widget" {
		t.Fatalf("unexpected import view: %+v", v)
	}
	if len(v.Workflows) != 1 || v.Workflows[0] != "ci.yml" {
		t.Fatalf("want workflows=[ci.yml], got %v", v.Workflows)
	}

	// Clone back: the native workflow landed (runner retargeted) AND the original
	// GitHub workflow is preserved (sync, not move).
	tree := cloneTree(t, base, "acme", "widget")
	native := fileContent(t, tree, ".hanzo/workflows/ci.yml")
	if !strings.Contains(native, "runs-on: ["+nativeRunnerLabel+"]") {
		t.Fatalf(".hanzo/workflows/ci.yml missing retargeted runner:\n%s", native)
	}
	if strings.Contains(native, "ubuntu-latest") {
		t.Fatalf("native workflow still targets ubuntu-latest:\n%s", native)
	}
	if _, err := tree.File(".github/workflows/ci.yml"); err != nil {
		t.Fatalf("original .github/workflows/ci.yml must be preserved: %v", err)
	}

	// The import indexed the repo: the indexer received the tree, including the code
	// AND the injected workflow (infra-as-code is searchable too).
	files := waitIndex(t, indexed)
	if !hasPath(files, "main.go") {
		t.Fatalf("indexed files missing main.go: %v", paths(files))
	}
	if !hasPath(files, ".hanzo/workflows/ci.yml") {
		t.Fatalf("indexed files missing injected workflow: %v", paths(files))
	}
}

// TestImportGeneratesDefaultWorkflow: a repo with NO GitHub CI gets one generated,
// language-detected (Go) default workflow — never a fabricated .github/ file.
func TestImportGeneratesDefaultWorkflow(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)

	srcRoot := t.TempDir()
	multiSource(t, srcRoot, "svc", map[string]string{
		"go.mod":  "module example.com/svc\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() {}\n",
	})
	srcBase := serveGitHTTP(t, srcRoot)

	asTenant("acme")
	code, body := do(t, app, http.MethodPost, "/v1/git/repos/svc/import", "acme",
		map[string]any{"source": srcBase + "/svc.git"})
	if code != http.StatusOK {
		t.Fatalf("import want 200, got %d (%s)", code, body)
	}
	var v importView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("import json: %v (%s)", err, body)
	}
	if len(v.Workflows) != 1 || v.Workflows[0] != "ci.yml" {
		t.Fatalf("want generated [ci.yml], got %v", v.Workflows)
	}

	tree := cloneTree(t, base, "acme", "svc")
	gen := fileContent(t, tree, ".hanzo/workflows/ci.yml")
	for _, want := range []string{"runs-on: [" + nativeRunnerLabel + "]", "go build ./...", "go test ./..."} {
		if !strings.Contains(gen, want) {
			t.Fatalf("generated workflow missing %q:\n%s", want, gen)
		}
	}
	if _, err := tree.File(".github/workflows/ci.yml"); err == nil {
		t.Fatal(".github/workflows/ci.yml must not be fabricated")
	}
}

// TestImportRegistersMirrorAndIsIdempotent: import registers an outbound mirror
// target, and a re-import re-writes NO workflow (native is primary; the injected CI
// commit is preserved by the fast-forward-only fetch).
func TestImportRegistersMirrorAndIsIdempotent(t *testing.T) {
	app := mountApp(t)

	srcRoot := t.TempDir()
	multiSource(t, srcRoot, "lib", map[string]string{"README.md": "# lib\n"})
	srcBase := serveGitHTTP(t, srcRoot)
	asTenant("acme")

	code, body := do(t, app, http.MethodPost, "/v1/git/repos/lib/import", "acme", map[string]any{
		"source": srcBase + "/lib.git",
		"mirror": "https://github.com/hanzo-community/lib.git",
	})
	if code != http.StatusOK {
		t.Fatalf("import want 200, got %d (%s)", code, body)
	}
	var v importView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("import json: %v (%s)", err, body)
	}
	if v.Mirror != "github.com" {
		t.Fatalf("want mirror host github.com, got %q", v.Mirror)
	}
	// A generic repo (no CI, no manifest) still gets a default workflow.
	if len(v.Workflows) != 1 || v.Workflows[0] != "ci.yml" {
		t.Fatalf("want default [ci.yml] on first import, got %v", v.Workflows)
	}

	// The outbound target is registered (visible via the mirrors list).
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/lib/mirrors", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list mirrors want 200, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), "github.com") {
		t.Fatalf("mirror target not registered: %s", body)
	}

	// Re-import: native (with the CI commit) has diverged from upstream, so the
	// fast-forward fetch preserves native and the workflow is already present — nothing
	// is re-written. Proves the "adopt as primary" idempotency.
	code, body = do(t, app, http.MethodPost, "/v1/git/repos/lib/import", "acme",
		map[string]any{"source": srcBase + "/lib.git"})
	if code != http.StatusOK {
		t.Fatalf("re-import want 200, got %d (%s)", code, body)
	}
	var v2 importView
	if err := json.Unmarshal(body, &v2); err != nil {
		t.Fatalf("re-import json: %v (%s)", err, body)
	}
	if len(v2.Workflows) != 0 {
		t.Fatalf("re-import must re-write nothing, got %v", v2.Workflows)
	}
}

// ---- test helpers ----

func cloneTree(t *testing.T, base, org, name string) *object.Tree {
	t.Helper()
	repo, head := cloneRepo(t, base, org, name)
	commit, err := repo.CommitObject(head)
	if err != nil {
		t.Fatalf("commit %s: %v", head, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	return tree
}

func fileContent(t *testing.T, tree *object.Tree, path string) string {
	t.Helper()
	f, err := tree.File(path)
	if err != nil {
		t.Fatalf("file %s: %v", path, err)
	}
	c, err := f.Contents()
	if err != nil {
		t.Fatalf("contents %s: %v", path, err)
	}
	return c
}

func waitIndex(t *testing.T, ch chan []IndexedFile) []IndexedFile {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for import to index")
		return nil
	}
}

func hasPath(files []IndexedFile, p string) bool {
	for _, f := range files {
		if f.Path == p {
			return true
		}
	}
	return false
}

func paths(files []IndexedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}
