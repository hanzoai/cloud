package platform

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// A deploy that names no commit builds the branch's head COMMIT: the image tag, the
// recorded commit and the checkout are one commit and not the name "main", which moves
// under every push.
func TestDeployRefIsTheBranchTipNotTheBranch(t *testing.T) {
	const tip = "0123456789abcdef0123456789abcdef01234567"
	st := &state{tip: func(_ context.Context, repo, ref string) (string, error) {
		if repo != "https://github.com/acme/api" || ref != "main" {
			t.Fatalf("asked %q %q", repo, ref)
		}
		return tip, nil
	}}
	got, err := deployRef(st, context.Background(), "https://github.com/acme/api", "", "")
	if err != nil || got != tip {
		t.Fatalf("an unnamed commit on the default branch must build %s, got %q err=%v", tip, got, err)
	}
	if tag := shortTag(got); strings.Contains(tag, "main") || tag == "" {
		t.Fatalf("the image tag must name the commit, got %q", tag)
	}
}

func TestDeployRefKeepsWhatItCannotImprove(t *testing.T) {
	ctx := context.Background()
	const sha = "fedcba9876543210fedcba9876543210fedcba98"
	asked := false
	st := &state{tip: func(context.Context, string, string) (string, error) { asked = true; return "x", nil }}

	// A full commit is already what a build wants: no read, no change.
	if got, err := deployRef(st, ctx, "https://github.com/acme/api", sha, "main"); err != nil || got != sha || asked {
		t.Fatalf("a full commit is kept untouched, got %q asked=%v err=%v", got, asked, err)
	}
	// No resolver wired (every test, and a host with no git): the branch, as before.
	if got, err := deployRef(&state{}, ctx, "https://github.com/acme/api", "", "dev"); err != nil || got != "dev" {
		t.Fatalf("no resolver builds the branch by name, got %q err=%v", got, err)
	}
	// A branch that cannot be read (a private repository) is built by name, and says why.
	st = &state{tip: func(context.Context, string, string) (string, error) {
		return "", errors.New("authentication required")
	}}
	got, err := deployRef(st, ctx, "https://github.com/acme/private", "", "release")
	if got != "release" || err == nil {
		t.Fatalf("an unreadable tip falls back to the branch with the reason, got %q err=%v", got, err)
	}
}

// lsRemoteTip reads a real repository: the commit the branch names, never a tag or a
// longer ref that merely ends the same way, and an error for a branch that is not there.
func TestLsRemoteTipReadsTheBranchHead(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("commit", "-q", "--allow-empty", "-m", "one")
	want := run("rev-parse", "HEAD")
	run("tag", "main-tag")
	run("branch", "release/main")

	got, err := lsRemoteTip(context.Background(), dir, "main")
	if err != nil || got != want {
		t.Fatalf("lsRemoteTip(main) = %q err=%v, want %s", got, err, want)
	}
	if _, err := lsRemoteTip(context.Background(), dir, "nope"); err == nil {
		t.Fatal("a branch that is not there must be an error")
	}
}
