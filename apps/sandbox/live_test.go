package sandbox

// The live proof: this is the ONLY test here that talks to a real cluster, and
// it is the one that answers the question the unit tests cannot — does a
// sandbox actually start, run a command, and hold an edit.
//
// It is skipped unless SANDBOX_LIVE=1, because a test that needs a kubeconfig
// is a test that fails on a laptop for a reason that has nothing to do with the
// code. Run it with:
//
//	SANDBOX_LIVE=1 SANDBOX_IMAGE_REPO=node SANDBOX_IMAGE_TAG_EXEC=22 \
//	  go test ./apps/sandbox/ -run TestLive -v
//
// The image is an env var on purpose. Proving the mechanism does not require
// our own image to exist yet — a stock `node:22` exercises exactly the same
// create/exec/edit path, so the loop can be proven before the image pipeline
// that will eventually feed it.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveSandboxRunsRealCode(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("set SANDBOX_LIVE=1 to run against a real cluster")
	}
	r := newRuntime()
	if err := r.ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}

	m := Sandbox{
		ID:  "live-proof",
		Org: "hanzo",
		// exec() refuses a sandbox that is not running, and start() cannot set
		// this for us — Sandbox is a value, so the caller owns the status the
		// same way the handler does after it writes the row.
		Status: "running",
		Class:  "exec",
		Pod:    fmt.Sprintf("sandbox-live-proof-%d", time.Now().Unix()),
		// The FULL image reference, not imageFor(). imageFor builds our own
		// one-image-three-tags naming (repo:class-tag), which is right in
		// production and wrong here: proving the mechanism must not require our
		// image to exist yet, and a stock node:22 exercises the identical
		// create/exec/edit path.
		Image: envOr("SANDBOX_LIVE_IMAGE", "node:22"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Logf("starting %s image=%s ns=%s runtimeClass=%q", m.Pod, m.Image, r.ns, r.runtimeClass)
	if err := r.start(ctx, m); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Always clean up: a leaked pod on a shared cluster is somebody else's
	// problem tomorrow.
	defer func() {
		if err := r.purge(context.Background(), m); err != nil {
			t.Logf("purge: %v", err)
		}
	}()

	// 1. It runs code at all.
	res, err := r.exec(ctx, m, []string{"node", "-e", "console.log('SANDBOX-RUNS-CODE', process.version)"}, nil, 60)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "SANDBOX-RUNS-CODE") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
	t.Logf("RUNS CODE: %s", strings.TrimSpace(res.Stdout))

	// 2. It holds an EDIT — which is the whole product. Write a file, then read
	//    it back through a SEPARATE exec, because a write that only survives
	//    inside one call is not a filesystem.
	const src = "export const answer = 42; // edited by the agent\n"
	if res, err = r.exec(ctx, m, []string{"sh", "-c", "mkdir -p " + workdir + "/src && cat > " + workdir + "/src/app.js"},
		strings.NewReader(src), 60); err != nil || res.ExitCode != 0 {
		t.Fatalf("write: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}
	if res, err = r.exec(ctx, m, []string{"cat", workdir + "/src/app.js"}, nil, 60); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if res.Stdout != src {
		t.Fatalf("edit did not persist across calls: got %q want %q", res.Stdout, src)
	}
	t.Logf("EDIT PERSISTS: %s", strings.TrimSpace(res.Stdout))

	// 3. The edited code actually runs. Reading a file back proves storage;
	//    executing it proves the edit reached the same filesystem the runtime
	//    uses, which is the thing an agent depends on.
	if res, err = r.exec(ctx, m, []string{"node", "-e",
		"import('" + workdir + "/src/app.js').then(m=>console.log('ANSWER='+m.answer))"}, nil, 60); err != nil {
		t.Fatalf("run edited code: %v", err)
	}
	if !strings.Contains(res.Stdout, "ANSWER=42") {
		t.Fatalf("edited code did not run: exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
	t.Logf("EDITED CODE RUNS: %s", strings.TrimSpace(res.Stdout))

	// 4. A failing command is DATA, not an error. An agent has to be able to see
	//    a test suite fail without the call itself failing, or it cannot tell
	//    "your code is broken" from "the sandbox is broken".
	if res, err = r.exec(ctx, m, []string{"sh", "-c", "echo to-stderr >&2; exit 3"}, nil, 60); err != nil {
		t.Fatalf("a non-zero exit must not be a transport error: %v", err)
	}
	if res.ExitCode != 3 || !strings.Contains(res.Stderr, "to-stderr") {
		t.Fatalf("exit=%d stderr=%q — want exit 3 and the stderr text", res.ExitCode, res.Stderr)
	}
	t.Logf("FAILURE IS DATA: exit=%d stderr=%s", res.ExitCode, strings.TrimSpace(res.Stderr))
}

// TestLiveSandboxDoesGit answers the half the first test does not: an agent that
// can edit but cannot commit has done nothing durable. Same skip switch.
func TestLiveSandboxDoesGit(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("set SANDBOX_LIVE=1 to run against a real cluster")
	}
	r := newRuntime()
	if err := r.ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}
	m := Sandbox{
		ID: "live-git", Org: "hanzo", Status: "running", Class: "exec",
		Pod:   fmt.Sprintf("sandbox-live-git-%d", time.Now().Unix()),
		Image: envOr("SANDBOX_LIVE_IMAGE", "node:22"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := r.start(ctx, m); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = r.purge(context.Background(), m) }()

	// git present at all — an image without it cannot host a coding agent.
	res, err := r.exec(ctx, m, []string{"git", "--version"}, nil, 60)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("git missing from the image: err=%v exit=%d stderr=%q", err, res.ExitCode, res.Stderr)
	}
	t.Logf("GIT PRESENT: %s", strings.TrimSpace(res.Stdout))

	// A real commit over a real edit. Identity is set locally rather than
	// globally: a sandbox is per-tenant, and a global identity would be one more
	// thing to reset between leases.
	script := `set -e
cd ` + workdir + `
git init -q .
git config user.email agent@hanzo.ai
git config user.name "Hanzo Agent"
printf 'edited\n' > file.txt
git add file.txt
git commit -q -m "the agent committed this"
git log --oneline -1`
	if res, err = r.exec(ctx, m, []string{"sh", "-c", script}, nil, 120); err != nil {
		t.Fatalf("git flow: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("git flow exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	t.Logf("COMMIT MADE: %s", strings.TrimSpace(res.Stdout))

	// Can it REACH the forge? Not a push — an unauthenticated ls-remote, which
	// separates "the network allows it" from "we have a credential". Those are
	// different gaps and conflating them sends someone to fix the wrong one.
	res, _ = r.exec(ctx, m, []string{"sh", "-c",
		"git ls-remote https://git.hanzo.ai/hanzo/universe HEAD 2>&1 | head -2"}, nil, 60)
	t.Logf("FORGE REACHABLE: exit=%d out=%q", res.ExitCode, strings.TrimSpace(res.Stdout+res.Stderr))
}
