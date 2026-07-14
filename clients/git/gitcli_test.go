package git

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// gitcli_test.go proves the PRODUCTION path end to end with the REAL git CLI on
// both sides — a git-CLI client cloning/pushing over smart-HTTP against the
// git-CLI-backed server — plus the bounded-memory property that motivated the
// rewrite (a large clone streams straight through, never buffered in Go heap).
// It also provides the external-git-source helpers the mirror tests fetch from.

// gitTestCmd builds a hermetic git command for tests: isolated config (no system
// / global gitconfig), fixed identity, no interactive prompts. dir may be "".
func gitTestCmd(dir string, args ...string) *exec.Cmd {
	c := exec.Command("git", args...)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.io",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.io",
	)
	return c
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := gitTestCmd(dir, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	c := gitTestCmd(dir, args...)
	c.Stdout = &out
	if err := c.Run(); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(out.String())
}

// orgHeaderArgs injects the gateway-minted identity headers a git CLI client must
// carry to reach our org-scoped smart-HTTP server (git accumulates repeated
// http.extraHeader values). This simulates, test-side, what the gateway mints in
// front of git.hanzo.ai in production.
func orgHeaderArgs(org string) []string {
	return []string{
		"-c", "http.extraHeader=X-Org-Id: " + org,
		"-c", "http.extraHeader=X-User-Id: u_" + org,
	}
}

// gitSource creates a bare source repo <root>/<name>.git holding one commit on
// branch main (+ optional lightweight tag), returning the commit hash — a real
// external git repo a mirror can fetch from.
func gitSource(t *testing.T, root, name, content, tag string) string {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "seed "+content)
	if tag != "" {
		gitRun(t, work, "tag", tag)
	}
	hash := gitOut(t, work, "rev-parse", "HEAD")
	gitRun(t, "", "clone", "-q", "--bare", work, filepath.Join(root, name+".git"))
	return hash
}

// serveGitHTTP serves the bare repos under root over an unauthenticated git
// smart-HTTP endpoint (git-http-backend CGI) — a stand-in for an external host
// like github.com that a mirror fetches from. Returns the base URL.
func serveGitHTTP(t *testing.T, root string) string {
	t.Helper()
	execPath := gitOut(t, "", "--exec-path")
	srv := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestGitCLIClonePushRoundTrip is the end-to-end proof with the REAL git CLI as
// the client: a git push over smart-HTTP lands in the git-CLI-backed server,
// fires push-to-deploy exactly once with the right branch + commit, meters the
// bytes, and a fresh git clone sees the pushed commit — the whole production
// round-trip, distinct from the go-git-client tests.
func TestGitCLIClonePushRoundTrip(t *testing.T) {
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
	if code, b := do(t, app, "POST", "/v1/git/repos", "acme", map[string]any{"name": "code"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	url := base + "/v1/git/acme/code.git"

	// Build locally, push over HTTPS with the real git CLI.
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# git cli native\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "first")
	commit := gitOut(t, work, "rev-parse", "HEAD")
	gitRun(t, work, "remote", "add", "origin", url)
	gitRun(t, work, append(orgHeaderArgs("acme"), "push", "origin", "main")...)

	// Fresh clone with the real git CLI — the pushed commit must be there.
	dst := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", url, dst)...)
	if got := gitOut(t, dst, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("cloned HEAD %s != pushed %s", got, commit)
	}

	// Push-to-deploy fired exactly once with the right branch + tip commit.
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("want exactly 1 push event, got %d: %+v", len(events), events)
	}
	if events[0].Org != "acme" || events[0].Repo != "code" || events[0].Branch != "main" || events[0].Commit != commit {
		t.Fatalf("unexpected event: %+v (want commit %s)", events[0], commit)
	}

	// The push was metered.
	_, ub := do(t, app, "GET", "/v1/git/usage", "acme", nil)
	if !bytes.Contains(ub, []byte(`"totalBytes"`)) || bytes.Contains(ub, []byte(`"totalBytes":0`)) {
		t.Fatalf("expected metered bytes after push: %s", ub)
	}
}

// TestGitCLILargeCloneStreamsBounded proves the fix: cloning a repo whose pack is
// far larger than any single buffer streams straight from `git upload-pack`
// stdout to the client without the pack ever landing in this process's Go heap.
// The old go-git server transport built the whole pack in a bytes.Buffer (a
// 3 GB clone = 3 GB), which OOM-killed the pod; here the server's live heap must
// stay a small fraction of the pack size while a 48 MiB clone is in flight.
func TestGitCLILargeCloneStreamsBounded(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, "POST", "/v1/git/repos", "acme", map[string]any{"name": "big"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	url := base + "/v1/git/acme/big.git"

	// Seed a large, incompressible blob by pushing straight to the on-disk bare
	// repo over a LOCAL path (bypasses the edge body limit; the test exercises the
	// large CLONE/response, which has no such limit).
	const blobSize = 48 << 20
	blob := make([]byte, blobSize)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "big.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "big blob")
	bareAbs := mounted.State.storage.absRepoPath("acme", "", "big")
	gitRun(t, work, "push", bareAbs, "main:refs/heads/main")

	// Sample the server process's live heap while the git CLI (a separate
	// subprocess) clones the 48 MiB pack over HTTP.
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	baseline := m.HeapInuse
	var peak uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				var s runtime.MemStats
				runtime.ReadMemStats(&s)
				if s.HeapInuse > peak {
					peak = s.HeapInuse
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()

	dst := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", url, dst)...)
	close(stop)
	<-done

	// Content round-trips byte-for-byte.
	got, err := os.ReadFile(filepath.Join(dst, "big.bin"))
	if err != nil {
		t.Fatalf("read cloned blob: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("cloned blob mismatch: %d bytes vs %d", len(got), len(blob))
	}

	// The pack streamed through — the server's live heap never grew by anything
	// close to the pack size. A buffered (old) implementation would spike ~48 MiB;
	// streaming stays well under half that. Generous threshold → not flaky.
	grew := int64(peak) - int64(baseline)
	if grew > blobSize/2 {
		t.Fatalf("server heap grew %d bytes during a %d-byte clone — pack was buffered, not streamed", grew, blobSize)
	}
	t.Logf("48 MiB clone streamed with server heap growth %d bytes (< %d threshold)", grew, blobSize/2)
}
