package git

// The ref policy, proved ON THE WIRE with the REAL git CLI against the REAL
// server — not a unit test of a pure function, but the thing an attacker would
// actually type, refused by the thing that actually receives it.
//
// This is the distinction that matters for a security control. checkRefPolicy
// passing its own table tells you the rule is written correctly. Only a real
// `git push --force` coming back with a refusal tells you the rule is REACHED:
// that it sits on the path a client takes, that it reads the body git actually
// sends, and that refusing does not corrupt the pack for the pushes it allows.
//
// Every case below is an attack a prompt-injected coding run would attempt with
// the org's credential in hand, run end to end.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedAgentBranch creates a repo, pushes an agent branch to it the way a coding
// run does, and returns (repo url, local worktree, the pushed commit).
func seedAgentBranch(t *testing.T, org, repo, branch string) (string, string) {
	t.Helper()
	app := mountApp(t)
	base := liveServer(t, app)
	if code, b := do(t, app, "POST", "/v1/git/repos", org, map[string]any{"name": repo}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	url := base + "/v1/git/" + org + "/" + repo + ".git"

	work := t.TempDir()
	gitRun(t, work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "first")
	gitRun(t, work, "remote", "add", "origin", url)
	gitRun(t, work, append(orgHeaderArgs(org), "push", "origin", "main")...)

	// The run's own branch, created exactly as the sandbox does it.
	gitRun(t, work, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("agent work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "agent change")
	gitRun(t, work, append(orgHeaderArgs(org), "push", "origin", "HEAD:refs/heads/"+branch)...)
	return url, work
}

// pushFails runs a push that MUST be refused, and returns git's own output.
func pushFails(t *testing.T, org, dir string, args ...string) string {
	t.Helper()
	out, err := gitTestCmd(dir, append(orgHeaderArgs(org), args...)...).CombinedOutput()
	if err == nil {
		t.Fatalf("git %s SUCCEEDED; it must be refused\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// A coding run creates its branch and pushes it. This is the whole legitimate
// path and it must work — a control that breaks normal work gets turned off.
func TestARealAgentPushLands(t *testing.T) {
	url, work := seedAgentBranch(t, "acme", "code", "agent/abc123def456")
	commit := gitOut(t, work, "rev-parse", "HEAD")

	dst := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", "-b", "agent/abc123def456", url, dst)...)
	if got := gitOut(t, dst, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("the agent branch did not land: cloned %s != pushed %s", got, commit)
	}
	t.Logf("PUSHED and re-cloned agent/abc123def456 at %s", commit)
}

// THE ATTACK, for real: the run opened a clean PR, a human is reading it, and
// the run force-pushes the payload onto the same branch before the merge.
func TestARealForcePushOverAnAgentBranchIsRefused(t *testing.T) {
	_, work := seedAgentBranch(t, "acme", "code", "agent/abc123def456")

	// Rewrite history the way a swap would: amend the commit the reviewer read.
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "--amend", "-m", "agent change")

	out := pushFails(t, "acme", work, "push", "--force", "origin", "HEAD:refs/heads/agent/abc123def456")
	if !strings.Contains(out, "may be created and never rewritten") {
		t.Fatalf("refused, but not by the ref policy — the rule may not be on this path:\n%q", out)
	}
	t.Logf("REFUSED force-push over an agent branch:\n%s", strings.TrimSpace(out))
}

// A run cannot erase its own branch, or anyone else's, in the agent namespace.
func TestARealAgentBranchDeleteIsRefused(t *testing.T) {
	_, work := seedAgentBranch(t, "acme", "code", "agent/abc123def456")
	out := pushFails(t, "acme", work, "push", "origin", "--delete", "refs/heads/agent/abc123def456")
	if !strings.Contains(out, "may be created and never rewritten") {
		t.Fatalf("refused, but not by the ref policy:\n%q", out)
	}
	t.Logf("REFUSED delete of an agent branch:\n%s", strings.TrimSpace(out))
}

// Nobody deletes the trunk with a push.
func TestARealDefaultBranchDeleteIsRefused(t *testing.T) {
	_, work := seedAgentBranch(t, "acme", "code", "agent/abc123def456")
	out := pushFails(t, "acme", work, "push", "origin", "--delete", "refs/heads/main")
	if !strings.Contains(out, "cannot be deleted by a push") {
		t.Fatalf("refused, but not by the ref policy:\n%q", out)
	}
	t.Logf("REFUSED delete of the default branch:\n%s", strings.TrimSpace(out))
}

// An ordinary human push must be completely unaffected — including a force-push
// to a feature branch, which is normal work and is deliberately NOT policed.
func TestRealOrdinaryPushesStillWork(t *testing.T) {
	url, work := seedAgentBranch(t, "acme", "code", "agent/abc123def456")
	gitRun(t, work, "checkout", "-q", "-b", "feature/x")
	if err := os.WriteFile(filepath.Join(work, "x.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "x")
	gitRun(t, work, append(orgHeaderArgs("acme"), "push", "origin", "feature/x")...)

	// Amend and force — a human rewriting their own feature branch.
	if err := os.WriteFile(filepath.Join(work, "x.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "--amend", "-m", "x")
	gitRun(t, work, append(orgHeaderArgs("acme"), "push", "--force", "origin", "feature/x")...)

	// And a normal update to trunk.
	gitRun(t, work, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(work, "y.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-q", "-m", "y")
	gitRun(t, work, append(orgHeaderArgs("acme"), "push", "origin", "main")...)

	dst := filepath.Join(t.TempDir(), "clone")
	gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", url, dst)...)
	if gitOut(t, dst, "rev-parse", "HEAD") != gitOut(t, work, "rev-parse", "HEAD") {
		t.Fatal("an ordinary push to trunk did not land")
	}
	t.Log("ordinary pushes (feature create, feature force, trunk update) all still land")
}
