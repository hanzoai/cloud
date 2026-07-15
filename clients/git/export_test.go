package git

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestExport_CloneURL(t *testing.T) {
	_ = mountApp(t)
	u := CloneURL("acme", "api")
	if !strings.Contains(u, "/v1/git/acme/api.git") || !strings.HasPrefix(u, "https://") {
		t.Fatalf("clone url shape wrong: %q", u)
	}
}

func TestExport_VerifyRef_OrgScopedTip(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	bareAbs := mounted.Load().State.storage.absRepoPath("acme", "", "code")
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	writeFile(t, work, "a.txt", "one")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "c")
	mainHash := strings.TrimSpace(gitOut(t, work, "rev-parse", "HEAD"))
	gitRun(t, work, "push", "-q", bareAbs, "main:refs/heads/main")

	// Present branch -> its tip.
	sha, ok := VerifyRef(context.Background(), "acme", "code", "main")
	if !ok || sha != mainHash {
		t.Fatalf("verify main: ok=%v sha=%q want %q", ok, sha, mainHash)
	}
	// Absent branch -> false (fail-closed).
	if _, ok := VerifyRef(context.Background(), "acme", "code", "nope"); ok {
		t.Fatal("absent branch must verify false")
	}
	// Wrong org resolves a DIFFERENT on-disk path (org-scoped) -> false: a coding
	// run for org B can never "verify" org A's ref.
	if _, ok := VerifyRef(context.Background(), "beta", "code", "main"); ok {
		t.Fatal("cross-org verify must be false (org-scoped repo path)")
	}
}

func TestExport_NilSafe(t *testing.T) {
	prev := mounted.Load()
	mounted.Store(nil)
	t.Cleanup(func() { mounted.Store(prev) })
	if CloneURL("a", "b") != "" {
		t.Fatal("CloneURL must be empty when unmounted")
	}
	if _, ok := VerifyRef(context.Background(), "a", "b", "c"); ok {
		t.Fatal("VerifyRef must be false when unmounted")
	}
}
