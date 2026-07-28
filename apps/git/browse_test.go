package git

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestBrowseJSON proves the console repo-browser's read surface end to end: seed a
// repo with a nested tree + a README + a binary file (straight into the bare repo,
// no push-builder dependency), then assert refs/tree/blob/commits/readme return the
// exact JSON the console GitApi normalizers consume — plus org isolation.
func TestBrowseJSON(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != http.StatusCreated {
		t.Fatalf("create repo: %d %s", code, b)
	}

	// Seed content directly into the bare repo via the hermetic git CLI.
	bareAbs := mounted.Load().State.storage.absRepoPath("acme", "", "code")
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	writeFile(t, work, "README.md", "# Code\nthe readme body\n")
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, work, "src/main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, work, "logo.bin", string([]byte{0, 1, 2, 0, 3, 0})) // NUL bytes ⇒ binary
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "initial commit")
	gitRun(t, work, "push", "-q", bareAbs, "main:refs/heads/main")

	// ── refs ──────────────────────────────────────────────────────────────────
	code, body := do(t, app, http.MethodGet, "/v1/git/repos/code/refs", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("refs: %d %s", code, body)
	}
	var refs refsJSON
	if err := json.Unmarshal(body, &refs); err != nil {
		t.Fatalf("refs json: %v (%s)", err, body)
	}
	if refs.Default != "main" {
		t.Fatalf("refs.default = %q, want main", refs.Default)
	}
	mainSHA := ""
	for _, r := range refs.Branches {
		if r.Name == "main" {
			mainSHA = r.SHA
		}
	}
	if len(mainSHA) != 40 {
		t.Fatalf("main branch sha not resolved: %+v", refs.Branches)
	}

	// ── tree (root): dirs before files; src is a tree, README/logo are blobs ────
	troot := treeOf(t, app, "/v1/git/repos/code/tree?ref=main")
	if len(troot) < 3 {
		t.Fatalf("root tree wants ≥3 entries, got %+v", troot)
	}
	if troot[0].Type != "tree" || troot[0].Name != "src" {
		t.Fatalf("dirs must sort first: got %+v", troot)
	}
	readme := entryByName(troot, "README.md")
	if readme == nil || readme.Type != "blob" || readme.Size == 0 || readme.Mode != "100644" {
		t.Fatalf("README.md entry wrong: %+v", readme)
	}

	// ── tree (subdir) ──────────────────────────────────────────────────────────
	tsub := treeOf(t, app, "/v1/git/repos/code/tree?ref=main&path=src")
	if e := entryByName(tsub, "main.go"); e == nil || e.Type != "blob" || e.Path != "src/main.go" {
		t.Fatalf("src/main.go entry wrong: %+v", tsub)
	}

	// ── blob (text) ────────────────────────────────────────────────────────────
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/blob?ref=main&path=README.md", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("blob: %d %s", code, body)
	}
	var tb blobJSON
	_ = json.Unmarshal(body, &tb)
	if tb.Binary || tb.Encoding != "utf8" || !strings.Contains(tb.Content, "the readme body") {
		t.Fatalf("text blob wrong: %+v", tb)
	}

	// ── blob (binary → base64) ─────────────────────────────────────────────────
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/blob?ref=main&path=logo.bin", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("bin blob: %d %s", code, body)
	}
	var bb blobJSON
	_ = json.Unmarshal(body, &bb)
	if !bb.Binary || bb.Encoding != "base64" {
		t.Fatalf("binary blob must be base64: %+v", bb)
	}
	if raw, err := base64.StdEncoding.DecodeString(bb.Content); err != nil || string(raw) != string([]byte{0, 1, 2, 0, 3, 0}) {
		t.Fatalf("binary blob decode wrong: %v %v", raw, err)
	}

	// ── commits (all + path-filtered) ──────────────────────────────────────────
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/commits?ref=main", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("commits: %d %s", code, body)
	}
	var cw struct {
		Commits []commitJSON `json:"commits"`
	}
	_ = json.Unmarshal(body, &cw)
	if len(cw.Commits) != 1 || cw.Commits[0].Message != "initial commit" {
		t.Fatalf("commits wrong: %+v", cw.Commits)
	}
	if len(cw.Commits[0].SHA) != 40 || cw.Commits[0].ShortSHA != cw.Commits[0].SHA[:7] || cw.Commits[0].AuthorName != "t" {
		t.Fatalf("commit fields wrong: %+v", cw.Commits[0])
	}
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/commits?ref=main&path=src/main.go", "acme", nil)
	_ = json.Unmarshal(body, &cw)
	if code != http.StatusOK || len(cw.Commits) != 1 {
		t.Fatalf("path-filtered commits wrong: %d %+v", code, cw.Commits)
	}

	// ── readme ─────────────────────────────────────────────────────────────────
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/readme?ref=main", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("readme: %d %s", code, body)
	}
	var rm readmeJSON
	_ = json.Unmarshal(body, &rm)
	if rm.Path != "README.md" || rm.Encoding != "utf8" || !strings.Contains(rm.Content, "the readme body") {
		t.Fatalf("readme wrong: %+v", rm)
	}

	// ── isolation + honest 404/403 ─────────────────────────────────────────────
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/code/tree?ref=main", "beta", nil); code != http.StatusNotFound {
		t.Fatalf("cross-org browse must 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/nope/refs", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("missing repo must 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/code/refs", "", nil); code != http.StatusForbidden {
		t.Fatalf("no org must 403, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/code/blob?ref=main&path=nope.txt", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("missing file must 404, got %d", code)
	}
}

// TestBrowseRefsEmptyRepo proves /refs degrades honestly for a fresh, commit-less
// repo (no HEAD): empty branch/tag sets + the default branch name, never a 500.
func TestBrowseRefsEmptyRepo(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "fresh"}); code != http.StatusCreated {
		t.Fatalf("create repo: %d %s", code, b)
	}
	code, body := do(t, app, http.MethodGet, "/v1/git/repos/fresh/refs", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("empty refs: %d %s", code, body)
	}
	var refs refsJSON
	if err := json.Unmarshal(body, &refs); err != nil {
		t.Fatalf("refs json: %v", err)
	}
	if len(refs.Branches) != 0 || len(refs.Tags) != 0 || refs.Default != "main" {
		t.Fatalf("empty-repo refs wrong: %+v", refs)
	}
}

func treeOf(t *testing.T, app *zip.App, path string) []treeEntryJSON {
	t.Helper()
	code, body := do(t, app, http.MethodGet, path, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("tree %s: %d %s", path, code, body)
	}
	var w struct {
		Entries []treeEntryJSON `json:"entries"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("tree json: %v (%s)", err, body)
	}
	return w.Entries
}

func entryByName(entries []treeEntryJSON, name string) *treeEntryJSON {
	for i := range entries {
		if entries[i].Name == name {
			return &entries[i]
		}
	}
	return nil
}
