package coding

// What a run may say about itself, and what it may not.
//
// A coding run has two channels back into cloud that are NOT the git protocol:
// the fields the caller sets when starting it, and the fields the sandbox
// reports when it finishes. Both were partly trusted. The sandbox is the thing
// assumed compromised, and the starting caller is only as trustworthy as the
// prompt that reached them — so both are inputs, and both are shaped here.

import (
	"context"
	"strings"
	"testing"
)

// The base branch travels to a `git clone -b <base>` argument on a machine we do
// not own. A value beginning with '-' is not a branch there but a FLAG, and
// `--upload-pack=` / `--config=core.fsmonitor=` each make git run a command of
// the caller's choosing on that machine. Repo and Org were shape-checked; Base
// and Project were not, and Base is the one that reaches an argv.
func TestStartRefusesABaseThatIsNotABranch(t *testing.T) {
	for name, base := range map[string]string{
		"an upload-pack flag":  "--upload-pack=touch /tmp/pwned",
		"a config flag":        "--config=core.fsmonitor=touch /tmp/pwned",
		"a bare dash":          "-",
		"an option-looking -o": "-o",
		"a traversal":          "../../etc/passwd",
		"a space":              "main branch",
		"a newline":            "main\nrm -rf /",
		"a semicolon":          "main;id",
		"a leading dot":        ".hidden",
	} {
		in := startIn("api", "do a thing")
		in.Base = base
		if _, err := Start(context.Background(), "acme", "u", in, nil); err == nil {
			t.Errorf("%s (%q): accepted; it reaches a git argv on a customer's machine", name, base)
		} else if !strings.Contains(err.Error(), "branch name") {
			t.Errorf("%s (%q): refused for the wrong reason: %v", name, base, err)
		}
	}
}

// The control must not refuse ordinary work: a real base, including a nested
// one, has to pass. A branch rule that rejects `release/2.1` gets removed.
func TestStartAcceptsARealBase(t *testing.T) {
	for _, base := range []string{"main", "develop", "release/2.1", "v1.2.3", "feature/JIRA-42_thing"} {
		if !BaseRE.MatchString(base) {
			t.Errorf("%q is an ordinary branch and must be accepted", base)
		}
	}
}

// Project becomes a git scope and a todo key, so it is shaped for the same
// reason Repo is: a value carrying a separator addresses another namespace.
func TestStartRefusesAProjectThatIsAPath(t *testing.T) {
	for _, project := range []string{"../other", "a/b", ".", "..", "a/../b"} {
		in := startIn("api", "do a thing")
		in.Project = project
		if _, err := Start(context.Background(), "acme", "u", in, nil); err == nil {
			t.Errorf("project %q: accepted; it is a path", project)
		}
	}
}

// THE ATTACK: a compromised sandbox reports that it pushed `main`.
//
// The branch was cloud's to decide — BranchFor(sessionID), named after a session
// the sandbox did not choose. Adopting the sandbox's self-report meant the claim
// flowed into VerifyRef, which only asks whether a ref EXISTS (and main does),
// and out the other side as PR.Open{Head: "main"}: a pull request headed at the
// trunk, filed by us, on behalf of a run that never had permission to write
// there.
func TestACompromisedSandboxCannotRenameItsOwnBranch(t *testing.T) {
	sessions := &fakeSessions{id: "sess_abc123def456"}
	todo := &fakePR{ref: PRRef{Identifier: "API-1"}}
	verified := map[string]bool{}

	d := Dispatcher{
		Sessions: sessions,
		PR:       todo,
		Runner: &fakeRunner{result: RunResult{
			OK: true, Changed: true, CommitSha: "deadbeef",
			Branch: "main", // the lie
		}},
		CloneURL: func(context.Context, string, string, string) string { return "https://git.test/acme/api.git" },
		VerifyRef: func(_ context.Context, _, _, branch string) (string, bool) {
			verified[branch] = true
			return "deadbeef", true
		},
	}
	res := d.Run(context.Background(), Req{
		Org: "acme", Repo: "api", Prompt: "do a thing", UserID: "u",
		Remote: "git@git.test:acme/api.git", Key: "k", Known: "git.test ssh-ed25519 AAAAPIN",
	})

	want := BranchFor("sess_abc123def456")
	if res.Branch != want {
		t.Fatalf("the run's branch became %q; it is cloud's to decide and must stay %q", res.Branch, want)
	}
	if verified["main"] {
		t.Fatal("the integrity check was pointed at main by the sandbox's own claim")
	}
	if len(todo.inputs) != 1 {
		t.Fatalf("want one PR, got %d", len(todo.inputs))
	}
	if head := todo.inputs[0].Head; head != want {
		t.Fatalf("A PR WAS FILED HEADED AT %q — the sandbox chose the head", head)
	}
	t.Logf("the sandbox said %q; the PR is headed at %q", "main", head(todo))
}

// The same lie on the ROUTED path, where the reporter is a customer's machine
// rather than our sandbox — a strictly less trusted place.
func TestARoutedMachineCannotRenameItsOwnBranch(t *testing.T) {
	sessions := &fakeSessions{id: "sess_abc123def456"}
	todo := &fakePR{ref: PRRef{Identifier: "API-2"}}
	d := Dispatcher{
		Sessions:  sessions,
		PR:        todo,
		VerifyRef: func(context.Context, string, string, string) (string, bool) { return "deadbeef", true },
	}
	issuedBranch := BranchFor("sess_abc123def456")
	d.finalizeRouted(context.Background(),
		RoutedRun{Org: "acme", Repo: "api", SessionID: "sess_abc123def456", Branch: issuedBranch, Actor: "u"},
		RoutedResult{OK: true, Changed: true, Branch: "main", CommitSha: "deadbeef"})

	if len(todo.inputs) != 1 {
		t.Fatalf("want one PR, got %d", len(todo.inputs))
	}
	if got := todo.inputs[0].Head; got != issuedBranch {
		t.Fatalf("A ROUTED PR WAS FILED HEADED AT %q, want %q", got, issuedBranch)
	}
	t.Logf("the machine said %q; the PR is headed at %q", "main", issuedBranch)
}

func head(f *fakePR) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs[0].Head
}
