package git

import (
	"context"
	"crypto/rand"
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
		if code, _ := do(t, app, http.MethodGet, "/v1/git/x/x.git/info/refs?service=git-upload-pack", o, nil); code != http.StatusForbidden {
			t.Fatalf("info/refs org=%q want 403, got %d", o, code)
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

// TestMirrorCredHostAllowlist proves HIGH-1: the shared GIT_MIRROR_TOKEN is only
// attached to allowlisted hosts, so a tenant pointing /mirror at an
// attacker-controlled https source cannot capture the token.
func TestMirrorCredHostAllowlist(t *testing.T) {
	t.Setenv(mirrorEnvToken, "secret-token")
	if mirrorAuthHeader("https://github.com/hanzoai/cloud.git") == "" {
		t.Fatal("github.com (default allowlist) should receive the credential")
	}
	if h := mirrorAuthHeader("https://attacker.example/x.git"); h != "" {
		t.Fatalf("non-allowlisted host must NOT receive the token, got %q", h)
	}
	if h := mirrorAuthHeader("http://github.com/x.git"); h != "" {
		t.Fatalf("non-TLS source must not receive the token, got %q", h)
	}
	// Explicit override list replaces the default.
	t.Setenv(mirrorAllowHostsEnv, "git.internal.example")
	if mirrorAuthHeader("https://github.com/x.git") != "" {
		t.Fatal("override list must exclude github.com")
	}
	if mirrorAuthHeader("https://git.internal.example/x.git") == "" {
		t.Fatal("override list must include git.internal.example")
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
