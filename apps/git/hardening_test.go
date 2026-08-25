package git

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hardening_test.go covers the Red-team findings: org path-traversal rejection
// (MED-3), subprocess reaping on early client disconnect (MED-6), the mirror
// credential host allowlist (HIGH-1), and the mirror SSRF guard + userinfo strip
// (MED-4 / LOW-7).

// TestOrgTraversalRejected proves a caller (e.g. a SuperAdmin switching X-Org-Id)
// cannot use a path-traversal org to escape the storage root: org() gates on
// orgRE before any handler reaches absRepoPath, on every write/entry surface.
func TestOrgTraversalRejected(t *testing.T) {
	app := mountApp(t)
	bad := []string{"../../../../etc", "..", ".", "a/b", "a/../b", "../etc", "/etc", "", "a\\b"}
	for _, o := range bad {
		if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", o, map[string]any{"name": "x"}); code != http.StatusForbidden {
			t.Fatalf("create org=%q want 403, got %d", o, code)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/x/mirror", o, map[string]any{"source": "https://github.com/a/b.git"}); code != http.StatusForbidden {
			t.Fatalf("mirror org=%q want 403, got %d", o, code)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/git/repos/x/push", o, map[string]any{"files": []map[string]any{{"path": "a", "content": "x"}}}); code != http.StatusForbidden {
			t.Fatalf("push org=%q want 403, got %d", o, code)
		}
		// Push advertisement: a malformed org is never authenticated → 403.
		if code, _ := do(t, app, http.MethodGet, "/v1/git/x/x.git/info/refs?service=git-receive-pack", o, nil); code != http.StatusForbidden {
			t.Fatalf("receive info/refs org=%q want 403, got %d", o, code)
		}
		// Fetch advertisement: a malformed org degrades to the ANONYMOUS public-
		// read path, where the traversal string is DISCARDED in favor of the
		// orgRE-validated :org path segment ("x") — the repo doesn't exist (and
		// would be private), so the uniform 404. The traversal never reaches
		// storage on either branch.
		if code, _ := do(t, app, http.MethodGet, "/v1/git/x/x.git/info/refs?service=git-upload-pack", o, nil); code != http.StatusNotFound {
			t.Fatalf("fetch info/refs org=%q want 404, got %d", o, code)
		}
	}
	// A valid org still works — the gate rejects only unsafe segments.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "ok"}); code != http.StatusCreated {
		t.Fatalf("valid org create want 201, got %d", code)
	}
}

// TestUploadPackStreamReapsOnEarlyClose proves the disconnect fix (MED-6): when a
// client abandons a clone mid-stream, gitPackStream.Close reaps the pipe-blocked
// git subprocess promptly instead of hanging on Wait(), and releases the pack
// slot.
func TestUploadPackStreamReapsOnEarlyClose(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "reap"}); code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, b)
	}
	bareAbs := mounted.Load().State.storage.absRepoPath("acme", "", "reap")

	// Seed ~8 MiB incompressible so upload-pack output far exceeds the ~64 KiB
	// pipe buffer and git blocks writing once we stop reading.
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	blob := make([]byte, 8<<20)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "big.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "big")
	gitRun(t, work, "push", bareAbs, "main:refs/heads/main")
	sha := gitOut(t, "", "--git-dir="+bareAbs, "rev-parse", "refs/heads/main")

	// Minimal v0 clone request: want <sha>\n, flush, done\n.
	body := string(packetWrite("want "+sha+"\n")) + "0000" + string(packetWrite("done\n"))
	stream, err := startPackRPC(context.Background(), mounted.Load().Log, bareAbs, svcUploadPack, "", strings.NewReader(body))
	if err != nil {
		t.Fatalf("startPackRPC: %v", err)
	}

	// Read a little, then abandon (a client disconnect mid-clone).
	if _, err := io.ReadFull(stream, make([]byte, 4096)); err != nil {
		t.Fatalf("read: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- stream.Close() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("gitPackStream.Close hung — subprocess not reaped on early disconnect")
	}
	if stream.cmd.ProcessState == nil {
		t.Fatal("git subprocess not reaped (ProcessState nil after Close)")
	}
	// The pack slot was released — a fresh acquire must succeed immediately.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := acquirePackSlot(ctx); err != nil {
		t.Fatalf("pack slot not released after Close: %v", err)
	}
	releasePackSlot()
}

// TestMirrorCredNeverLeavesItsHost proves HIGH-1 — a tenant pointing /mirror at
// an attacker-controlled source cannot capture a credential — and the stronger
// property the allowlist it replaces did NOT have: a token minted for one host is
// never offered to another.
//
// That second half was the live bug. One env var served every allowlisted host,
// so our GitHub token went to git.hanzo.ai, authenticated nothing, and the fetch
// died reporting a MISSING credential ("could not read Username") when the real
// fault was a wrong one. The old test passed throughout, because it asserted the
// mechanism (a host list) rather than the property (a credential stays on the
// host it was minted for).
func TestMirrorCredNeverLeavesItsHost(t *testing.T) {
	t.Setenv("GIT_MIRROR_TOKEN_GITHUB_COM", "github-token")

	if mirrorAuthHeader("https://github.com/hanzoai/cloud.git") == "" {
		t.Fatal("github.com holds a credential and must receive it")
	}
	// THE BUG: the forge is ours and was allowlisted, and it STILL must not be
	// handed GitHub's token — a credential for one forge is not one for another.
	if h := mirrorAuthHeader("https://git.hanzo.ai/hanzoai/cloud.git"); h != "" {
		t.Fatalf("git.hanzo.ai got a token minted for github.com: %q", h)
	}
	if h := mirrorAuthHeader("https://attacker.example/x.git"); h != "" {
		t.Fatalf("a host we hold nothing for must fetch anonymously, got %q", h)
	}
	if h := mirrorAuthHeader("http://github.com/x.git"); h != "" {
		t.Fatalf("non-TLS source must not receive the token, got %q", h)
	}

	// Each host names its own, so adding one is a secret rather than a code change.
	t.Setenv("GIT_MIRROR_TOKEN_GIT_HANZO_AI", "forge-token")
	if mirrorAuthHeader("https://git.hanzo.ai/hanzoai/cloud.git") == "" {
		t.Fatal("git.hanzo.ai now holds a credential and must receive it")
	}
	if want := base64Cred("x-access-token", "forge-token"); mirrorAuthHeader("https://git.hanzo.ai/x.git") != want {
		t.Fatal("git.hanzo.ai must receive ITS OWN token, not another host's")
	}
	if want := base64Cred("x-access-token", "github-token"); mirrorAuthHeader("https://github.com/x.git") != want {
		t.Fatal("github.com must still receive its own token")
	}

	// A token is presented under the username its host ACCEPTS. GitLab rejects a
	// personal or OAuth token offered as anything but oauth2, so the credential
	// held in the environment must be spelled the way an explicitly-supplied one
	// already is — one rule, not one rule per path.
	t.Setenv("GIT_MIRROR_TOKEN_GITLAB_COM", "gitlab-token")
	if want := base64Cred("oauth2", "gitlab-token"); mirrorAuthHeader("https://gitlab.com/g/x.git") != want {
		t.Fatal("gitlab.com must receive its token under oauth2")
	}
}

// base64Cred renders the credential mirrorAuthHeader is expected to produce, so a
// test asserts WHICH token arrived under WHICH username rather than merely that
// one did.
func base64Cred(user, tok string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + tok))
}

// envHost turns a hostname into the tail of an env var name; a host that differs
// only by case or by a character an env name cannot hold must not become a
// DIFFERENT credential slot, or a deployment would set one and the fetch would
// read another.
func TestEnvHostNamesOneSlotPerHost(t *testing.T) {
	for _, c := range []struct{ host, want string }{
		{"github.com", "GITHUB_COM"},
		{"GitHub.com", "GITHUB_COM"},
		{"git.hanzo.ai", "GIT_HANZO_AI"},
		{"git-host.example", "GIT_HOST_EXAMPLE"},
	} {
		if got := envHost(c.host); got != c.want {
			t.Errorf("envHost(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

// TestMirrorSourceSSRFGuardAndUserinfo proves MED-4 (SSRF) + LOW-7 (userinfo
// strip): internal targets are refused, and any credentials embedded in the URL
// are removed before it becomes a git argv.
func TestMirrorSourceSSRFGuardAndUserinfo(t *testing.T) {
	for _, s := range []string{
		"http://127.0.0.1/x.git",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/x.git",
		"http://192.168.1.1/x.git",
		"http://[::1]/x.git",
		"http://0.0.0.0/x.git",
	} {
		if _, err := mirrorSource(s); err == nil {
			t.Fatalf("SSRF guard must reject %q", s)
		}
	}
	// Userinfo is stripped (use an allowlisted-private host to avoid DNS).
	t.Setenv(mirrorAllowPrivateEnv, "host.internal.test")
	got, err := mirrorSource("https://user:secret@host.internal.test:8443/x.git")
	if err != nil {
		t.Fatalf("allowlisted host must pass: %v", err)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "user") || strings.Contains(got, "@") {
		t.Fatalf("userinfo must be stripped from source argv, got %q", got)
	}
}
