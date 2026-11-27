package coding

// reader_test.go pins what the agent's git may do in a GitHub checkout (#553): fetch what the clone
// lacks, with a token that can only read, and push nothing. The run's own token stays what it was:
// fed to the commands that reach the remote and to no other.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
)

// A clone the agent holds a read-only token for keeps every commit and no tree, with the helper that
// reads that token named in the checkout's own config; any other clone is one commit, as it was.
func TestACloneTheAgentCanReadFromKeepsEveryCommit(t *testing.T) {
	read := cloneArgv(RunRequest{Remote: "https://github.com/o/r.git", Key: "push", ReadKey: "read"})
	if !slices.Contains(read, "--filter=tree:0") || slices.Contains(read, "--depth") || !slices.Contains(read, "--single-branch") {
		t.Fatalf("a clone with a read token must take every commit and no tree, got %q", read)
	}
	clone := slices.Index(read, "clone")
	if !slices.Contains(read[clone:], "credential.https://github.com.helper="+readHelper(readPath)) {
		t.Fatalf("the checkout does not name the helper that reads the agent's token, got %q", read[clone:])
	}
	for name, req := range map[string]RunRequest{
		"no read token": {Remote: "https://github.com/o/r.git", Key: "push"},
		"a public repo": {Remote: "https://github.com/o/r.git"},
		"the forge":     {Remote: sshRemote, Key: theKey, ReadKey: "read"},
	} {
		argv := cloneArgv(req)
		if !slices.Contains(argv, "--depth") || slices.Contains(argv, "--filter=tree:0") {
			t.Errorf("%s: a clone the agent cannot fetch for must stay one commit, got %q", name, argv)
		}
		if slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, readPath) }) {
			t.Errorf("%s: the checkout names a read helper with no token to read: %q", name, argv)
		}
	}
}

// git, as a person's, with no config of the box's own.
func gitCredential(t *testing.T, global string, argv []string, stdin string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+global, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "refused: " + string(out)
	}
	return string(out)
}

// The helper answers github.com, and only github.com, with the token in the file.
func TestTheReadHelperAnswersGitHubWithTheTokenInItsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "read")
	if err := os.WriteFile(file, []byte("READTOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(global, []byte("[credential \"https://github.com\"]\n\thelper = "+strconv.Quote(readHelper(file))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := gitCredential(t, global, []string{"git", "credential", "fill"}, "protocol=https\nhost=github.com\n\n")
	if !strings.Contains(got, "username=x-access-token\n") || !strings.Contains(got, "password=READTOKEN\n") {
		t.Fatalf("github.com was answered %q, want the token in the file", got)
	}
	if got := gitCredential(t, global, []string{"git", "credential", "fill"}, "protocol=https\nhost=evil.example\n\n"); strings.Contains(got, "READTOKEN") {
		t.Fatalf("another host was answered the read token: %q", got)
	}
}

// A command that carries the run's own token is answered with it, whatever the checkout's config
// names: a push made with the read token would be refused.
//
// MUTATION: drop the reset of the url-scoped helper in gitAs and the push is offered the read token.
func TestAPushIsNeverOfferedTheReadToken(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "read")
	if err := os.WriteFile(file, []byte("READTOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(global, []byte("[credential \"https://github.com\"]\n\thelper = "+strconv.Quote(readHelper(file))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask := "protocol=https\nhost=github.com\n\n"
	if got := gitCredential(t, global, []string{"git", "credential", "fill"}, ask); !strings.Contains(got, "password=READTOKEN") {
		t.Fatalf("the control is not one: the checkout's helper answered %q", got)
	}
	push := append(gitAs(RunRequest{Remote: "https://github.com/o/r.git", Key: "PUSHTOKEN", ReadKey: "READTOKEN"}), "credential", "fill")
	got := gitCredential(t, global, push, "PUSHTOKEN\n"+ask)
	if !strings.Contains(got, "password=PUSHTOKEN\n") || strings.Contains(got, "READTOKEN") {
		t.Fatalf("the run's own command was answered %q, want its own token", got)
	}
}

// lend writes the token on stdin, never on argv, to the file the helper reads, and only when it is
// new: a renewed one is written, an unchanged one is not asked of the sandbox again.
func TestLendWritesTheReadTokenOnStdinOnlyWhenItChanged(t *testing.T) {
	p := &pod{}
	servePod(t, p)
	token := "READ-ONE"
	in := sandbox{ctx: context.Background(), id: "sbx_1", ttl: 30, session: "sess_1", extraSecrets: new([]string), lent: new(string),
		readKey: "READ-ONE", renewRead: func(context.Context, bool) (string, error) { return token, nil }}
	for range 2 {
		if err := in.lend(false); err != nil {
			t.Fatalf("lend: %v", err)
		}
	}
	if len(p.ran) != 1 || p.fed[0] != "READ-ONE" || !slices.Contains(p.ran[0], readPath) || strings.Contains(strings.Join(p.ran[0], " "), "READ-ONE") {
		t.Fatalf("the token was written as %q fed %q, want once, on stdin, to %s", p.ran, p.fed, readPath)
	}
	if !slices.Contains(p.blind[0], "READ-ONE") {
		t.Fatalf("the command that carries the token is not blinded to it: %q", p.blind[0])
	}
	token = "READ-TWO"
	if err := in.lend(false); err != nil || len(p.ran) != 2 || p.fed[1] != "READ-TWO" {
		t.Fatalf("a renewed token was not written: %v %q", err, p.fed)
	}
	if err := in.lend(true); err != nil || len(p.ran) != 3 {
		t.Fatalf("a replaced pod's file was not written again: %v %d", err, len(p.ran))
	}
	if !slices.Contains(*in.extraSecrets, "READ-ONE") || !slices.Contains(*in.extraSecrets, "READ-TWO") {
		t.Fatalf("a token it wrote is not blinded in later streams: %q", *in.extraSecrets)
	}
	if err := (sandbox{ctx: context.Background(), id: "sbx_1"}).lend(true); err != nil || len(p.ran) != 3 {
		t.Fatalf("a run with no read token wrote one: %v %d", err, len(p.ran))
	}
}

// A run on GitHub is given two tokens for its repository: its own, which pushes, and one that can
// only read, written for the agent's git. The agent is told it can fetch and merge, the clone keeps
// every commit, no stream shows the read token, and both are revoked when the run ends.
//
// MUTATION: feed the read token to the push, or leave the helper out of the clone, and the agent
// is as it was: one commit, no credential.
func TestStart_ARunsAgentCanReadGitHubAndCannotPush(t *testing.T) {
	r := gitHubRun(t, map[string]string{})
	acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "merge main into the branch", Pull: true})
	if err != nil || acc.Repo != "hanzo-apps/site" {
		t.Fatalf("start: %+v %v", acc, err)
	}
	r.pod.mu.Lock()
	defer r.pod.mu.Unlock()
	lent, pushed, told := 0, 0, false
	for i, argv := range r.pod.ran {
		line := strings.Join(argv, " ")
		if strings.Contains(line, ghRead) || strings.Contains(line, ghToken) {
			t.Fatalf("command %d carries a token on argv: %q", i, line)
		}
		switch {
		case slices.Contains(argv, readPath):
			lent++
			if r.pod.fed[i] != ghRead {
				t.Fatalf("the read token file was written from %q, want the read token on stdin", r.pod.fed[i])
			}
		case strings.Contains(line, " clone "):
			if !strings.Contains(line, "--filter=tree:0") || strings.Contains(line, "--depth") ||
				!strings.Contains(line[strings.Index(line, " clone "):], "credential.https://github.com.helper=!f()") {
				t.Fatalf("the clone does not keep every commit with the read helper: %q", line)
			}
		case slices.Contains(argv, "push"):
			pushed++
			if !strings.HasPrefix(r.pod.fed[i], ghToken+"\n") {
				t.Fatalf("the push was not fed the run's own token: %q", r.pod.fed[i])
			}
		case slices.Contains(argv, "--skip-git-repo-check"):
			told = strings.Contains(toldOf(r.pod.fed[i]), partialLine) && !strings.Contains(toldOf(r.pod.fed[i]), shallowLine)
			if !slices.Contains(r.pod.blind[i], ghRead) {
				t.Fatalf("the agent's turn is not blinded to the read token: %q", r.pod.blind[i])
			}
		}
		if strings.Contains(r.pod.fed[i], ghRead) && !slices.Contains(argv, readPath) {
			t.Fatalf("command %d was fed the read token and writes it nowhere: %q", i, line)
		}
	}
	if lent != 1 || pushed != 1 || !told {
		t.Fatalf("the read token was written %d times, the branch pushed %d, the agent told it can fetch: %v\n%s",
			lent, pushed, told, strings.Join(r.pod.lines(), "\n"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.revoked, []string{"Bearer " + ghRead, "Bearer " + ghToken}) {
		t.Fatalf("revoked %v, want the read-only token and then the run's own", r.revoked)
	}
	if !slices.Equal(r.reads, []bool{false, true}) {
		t.Fatalf("GitHub was asked to admit %v, want a token to push and one to read", r.reads)
	}
	for _, e := range r.sessions.events {
		if strings.Contains(e.payload, ghRead) {
			t.Fatalf("the read token is in a session event: %s", e.payload)
		}
	}
}
