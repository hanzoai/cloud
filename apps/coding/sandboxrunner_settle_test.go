package coding

// sandboxrunner_test.go drives the REAL sandboxRunner against a fake sandbox on a
// real socket, and asserts the exact sequence of commands a run sends into the
// pod.
//
// That sequence IS the product. A run whose edits never leave the checkout is a
// model talking to itself: the sandbox is reaped when the lease ends and the work
// dies with it. So the assertions here are about commands and not about a return
// value — a runner can be made to answer Changed:true with no test noticing that
// nothing was ever pushed.
//
// The worse of the two failures is the FALSE POSITIVE. A run that reports "no
// changes" when it changed something is a lost afternoon; a run that reports
// changes when it made none files a PR against an empty branch, and a reviewer
// learns to distrust every PR the agent opens. Both are covered, and the
// no-changes case is first.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func TestArgvForNonInteractiveVerb(t *testing.T) {
	// `-p` is --profile to dev, not "prompt". `dev -p "<task>"` answered
	// `config profile "<task>" not found` and exited before asking a model
	// anything — which is what every coding run cloud dispatched used to do.
	argv := argvFor("", "", "", "")
	if len(argv) < 2 || argv[0] != "dev" || argv[1] != "exec" {
		t.Fatalf("the default harness must start with `dev exec`, got %q", argv)
	}
}

func TestArgvForLeavesTheSandboxToThePod(t *testing.T) {
	// dev's own Landlock/seccomp sandbox kills every command under gVisor, so
	// the default harness must hand confinement to the pod, not stack its own.
	argv := argvFor("", "", "", "")
	if !slices.Contains(argv, "--dangerously-bypass-approvals-and-sandbox") || slices.Contains(argv, "--full-auto") {
		t.Fatalf("dev must run unsandboxed inside the pod, got %q", argv)
	}
}

func TestArgvForCarriesNoPlanFlag(t *testing.T) {
	// dev 0.7.9 answers `unexpected argument '--include-plan-tool'` and exits 2,
	// so a turn carrying it, first or resumed, never reaches a model.
	for _, argv := range [][]string{argvFor("", "", "", ""), resumeFor("0199c3a1-7b2e-7f00-8000-000000000001", "", "", "")} {
		if slices.Contains(argv, "--include-plan-tool") {
			t.Fatalf("dev 0.7 refuses --include-plan-tool: %q", argv)
		}
	}
}

// THE TASK IS NEVER AN ARGUMENT: every harness reads it on stdin, in its own
// spelling, measured in the dev image (sandbox 1.3.7: dev 0.6.94, codex
// 0.146.1, claude 2.1.223). An argument is in /proc/<pid>/cmdline, where the
// model's own `pkill -f "go build"` matched a task that said "run go build" and
// killed its harness (sess_06e8f87b…).
func TestTheTaskIsReadFromStdin(t *testing.T) {
	for tool, tail := range map[string][]string{
		"":       {"--", "-"},
		"dev":    {"--", "-"},
		"gen":    {"--", "-"},
		"codex":  {"--", "-"},
		"python": {"python3", "-"},
		"node":   {"node", "-"},
	} {
		argv := argvFor(tool, "", "", "")
		if got := argv[len(argv)-len(tail):]; !slices.Equal(got, tail) {
			t.Fatalf("%q: argv ends %q, want %q (the task read from stdin): %q", tool, got, tail, argv)
		}
	}
	// claude -p reads its prompt from stdin when it is given none.
	if argv := argvFor("claude", "", "", ""); !slices.Equal(argv, []string{"claude", "-p"}) {
		t.Fatalf("claude: want [claude -p] and the task on stdin, got %q", argv)
	}
	if argv := resumeFor("0199c3a1-7b2e-7f00-8000-000000000001", "", "", ""); !slices.Equal(argv[len(argv)-4:],
		[]string{"resume", "--", "0199c3a1-7b2e-7f00-8000-000000000001", "-"}) {
		t.Fatalf("a resume must read what it is told from stdin: %q", argv)
	}
}

func TestCheckToolRefusesAHarnessWeDoNotCarry(t *testing.T) {
	// Empty is dev, which is the whole default path, so it is not an error.
	if err := CheckTool(""); err != nil {
		t.Fatalf("empty tool must be accepted as dev: %v", err)
	}
	for _, ok := range []string{"dev", "gen", "claude", "codex", "python", "node"} {
		if err := CheckTool(ok); err != nil {
			t.Fatalf("%s is carried by the dev image: %v", ok, err)
		}
	}
	// Without this, `default:` in classFor and argvFor read a typo as dev and
	// answered as though the asked-for harness had run.
	if err := CheckTool("clade"); err == nil {
		t.Fatal("an unknown harness must be refused, not silently run as dev")
	}
}

func TestClassForCarriesTheBinaryTheToolNeeds(t *testing.T) {
	// Measured `command -v` in each published 1.0.1 class: exec carries dev,
	// hanzo and hanzo-mcp but NOT claude/codex/gh; dev and desktop carry all.
	for tool, want := range map[string]string{
		"": "dev", "dev": "dev", "gen": "dev", "claude": "dev", "codex": "dev",
		"python": "exec", "node": "exec",
	} {
		if got := classFor(tool, false); got != want {
			t.Fatalf("classFor(%q): want %s, got %s", tool, want, got)
		}
	}
	// A screen is an image variant, so it wins over every harness — desktop is
	// dev plus an X server and therefore carries every binary dev does.
	for _, tool := range []string{"", "dev", "gen", "claude", "python"} {
		if got := classFor(tool, true); got != "desktop" {
			t.Fatalf("classFor(%q, desktop): want desktop, got %s", tool, got)
		}
	}
}

// The workdir arrives root-owned, so the run marks it safe for its own git
// before the first git command that reads the checkout.
func TestTheCheckoutIsTrustedBeforeItIsRead(t *testing.T) {
	p := &pod{}
	servePod(t, p)
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	lines := p.lines()
	trust, clone := -1, -1
	for i, l := range lines {
		if trust < 0 && l == "git config --global --add safe.directory /work" {
			trust = i
		}
		if clone < 0 && strings.Contains(l, " clone ") {
			clone = i
		}
	}
	if trust < 0 || clone < 0 || trust > clone {
		t.Fatalf("safe.directory must be set before the clone, got %q", lines)
	}
}

// A run with a ticket hands its agent the ask server and the sentence that says
// what it is for, and the ticket reaches the agent on stdin and nowhere else.
func TestSandboxRun_TheAgentIsHandedItsTicket(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	run := aRun()
	run.Ticket = "run_" + strings.Repeat("ab", 32)
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", run, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, argv := range p.ran {
		line := strings.Join(argv, " ")
		if strings.Contains(line, run.Ticket) {
			t.Fatalf("command %d carries the ticket on argv: %q", i, line)
		}
		if !slices.Contains(argv, "--skip-git-repo-check") {
			continue
		}
		if !strings.Contains(toldOf(p.fed[i]), askLine+shellLine+shallowLine+deliverLine+run.Prompt) || !strings.Contains(line, "mcp_servers.run.url") {
			t.Fatalf("the agent was not handed the ask server and what it is for: %q", line)
		}
		if lines := strings.SplitN(p.fed[i], "\n", 3); len(lines) != 3 || lines[1] != run.Ticket {
			t.Fatalf("the ticket did not reach the agent on stdin: %q", p.fed[i])
		}
		return
	}
	t.Fatal("the agent never ran")
}

// A clone that fails names its fault on its first line, because the first line is
// all of a failure that reaches the person who asked (reason, apps/agents). A
// clone that announces itself first said "Cloning into '.'..." and nothing else.
func TestAFailedCloneNamesItsFaultFirst(t *testing.T) {
	argv := cloneArgv(RunRequest{Remote: "file://" + filepath.Join(t.TempDir(), "missing.git")})
	if !slices.Contains(argv, "--single-branch") || !slices.Contains(argv, "--depth") {
		t.Fatalf("cloneArgv must take one commit of one branch, got %q", argv)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a clone of a missing repository succeeded: %s", out)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if !strings.HasPrefix(first, "fatal:") {
		t.Fatalf("the clone's first line is %q, not its fault (all: %q)", first, out)
	}
}

// A run checks out every file of its commit, so the clone asks for the commit and
// its files in one fetch; a blob filter would make a second, separate request for
// every blob (the clone of a remote with N files is then two fetches, not one).
func TestCloneTakesOneCommitWithItsFiles(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, a := range cloneArgv(RunRequest{Remote: "https://example.test/o/r"}) {
		if strings.HasPrefix(a, "--filter") {
			t.Fatalf("cloneArgv asks for a partial clone: %q", a)
		}
	}
	src := gitRepo(t, map[string]string{"a.txt": "one\n", "dir/b.txt": "two\n"})
	work := t.TempDir()
	argv := cloneArgv(RunRequest{Remote: "file://" + src})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", work}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if got := git("rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("the clone must be shallow, is-shallow-repository = %q", got)
	}
	if got := git("config", "--get", "remote.origin.promisor"); got != "" {
		t.Errorf("the clone is partial (promisor = %q): its blobs would need a second fetch", got)
	}
	if b, err := os.ReadFile(filepath.Join(work, "dir", "b.txt")); err != nil || string(b) != "two\n" {
		t.Errorf("the checkout lacks the tree's files: %q %v", b, err)
	}
	if got := git("status", "--porcelain"); got != "" {
		t.Errorf("a fresh clone must be clean: %q", got)
	}
}


// Cloning must leave an unavailable LFS object as its checked-in pointer.
// Disable global config so the test does not rely on a developer's LFS setup.
func TestCloneKeepsMissingLFSPointer(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	const pointer = "version https://git-lfs.github.com/spec/v1\noid sha256:0000000000000000000000000000000000000000000000000000000000000000\nsize 123\n"
	src := gitRepo(t, map[string]string{
		".gitattributes": "*.bin filter=lfs diff=lfs merge=lfs -text\n",
		"missing.bin":    pointer,
	})
	// Model a machine with LFS configured but its helper unavailable.
	for key, value := range map[string]string{
		"filter.lfs.process":  "missing-lfs-command",
		"filter.lfs.smudge":   "missing-lfs-command %f",
		"filter.lfs.required": "true",
	} {
		if out, err := exec.Command("git", "config", "--global", key, value).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v: %s", err, out)
		}
	}
	work := t.TempDir()
	argv := cloneArgv(RunRequest{Remote: "file://" + src})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone with missing LFS object: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(work, "missing.bin"))
	if err != nil || string(got) != pointer {
		t.Fatalf("missing LFS object must remain its pointer: %q (%v)", got, err)
	}
	// Clone-scoped overrides must not change the agent's later LFS behavior.
	out, err := exec.Command("git", "-C", work, "config", "--get", "filter.lfs.required").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("clone changed persistent LFS configuration: %q (%v)", out, err)
	}
}

// The settings the clone writes into the checkout are the ones every later git
// command of the run inherits.
func TestCloneTunesTheCheckoutItWrites(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	src := gitRepo(t, map[string]string{"a.txt": "one\n"})
	work := t.TempDir()
	argv := cloneArgv(RunRequest{Remote: "file://" + src})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for key, want := range map[string]string{
		"core.untrackedcache": "true",
		"gc.auto":             "0",
		"maintenance.auto":    "false",
	} {
		out, err := exec.Command("git", "-C", work, "config", "--local", "--get", key).Output()
		if got := strings.TrimSpace(string(out)); err != nil || got != want {
			t.Errorf("%s in the checkout's own configuration = %q (%v), want %q", key, got, err, want)
		}
	}
	// git's default for fsync is already right: nothing here may turn on a
	// flush per loose object, which costs an fsync for every file staged.
	if out, err := exec.Command("git", "-C", work, "config", "--local", "--get-regexp", `^core\.fsync`).Output(); err == nil {
		t.Errorf("the clone must leave fsync at git's default, wrote: %s", out)
	}
}

// gitRepo makes a repository holding the given files and returns its path.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "--quiet", "-m", "tree")
	return dir
}

// The lease is announced as a FACT: the step names the sandbox's id and class as
// fields, which is what the dispatcher tags the run's session with.
func TestSandboxRun_TheLeasedStepCarriesTheSandbox(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	var got []Step
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), func(s Step) { got = append(got, s) }); err != nil {
		t.Fatalf("run: %v", err)
	}
	var named []Step
	for _, s := range got {
		if s.Sandbox != "" {
			named = append(named, s)
		}
	}
	if len(named) != 1 || named[0].Step != "leased" || named[0].Sandbox != "sbx_1" || named[0].Class != "dev" ||
		named[0].Message != "sandbox sbx_1 (dev)" {
		t.Fatalf("steps naming a sandbox: %+v, want the one leased step naming sbx_1 (dev)", named)
	}
}

// THE PERSON AUTHORS THE COMMIT AND THE AGENT COMMITS IT: a run made for
// somebody names them in the field git keeps for who wrote a change, and keeps
// the agent in the one for who made the commit. With nobody to name, the agent
// is both, as it always was.
func TestSandboxRun_ThePersonAuthorsTheCommit(t *testing.T) {
	for _, c := range []struct {
		author Author
		want   string
	}{
		{Author{Name: "Zoe Q", Email: "zoe@acme.test"}, "--author=Zoe Q <zoe@acme.test>"},
		{Author{}, ""},
	} {
		tip := theSHA
		p := &pod{}
		p.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "commit"):
				tip = newSHA
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			}
			return client.Ran{}
		}
		servePod(t, p)
		run := aRun()
		run.Author = c.author
		if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", run, nil); err != nil {
			t.Fatalf("run: %v", err)
		}
		commit, ok := p.did(" commit ")
		if !ok {
			t.Fatalf("nothing was committed:\n%s", strings.Join(p.lines(), "\n"))
		}
		if !strings.Contains(commit, "user.name=hanzo-agent") || !strings.Contains(commit, "user.email=agent@hanzo.ai") {
			t.Fatalf("the agent is not the committer: %s", commit)
		}
		if c.want != "" && !strings.Contains(commit, c.want) {
			t.Fatalf("the commit %q does not name its author %q", commit, c.want)
		}
		if c.want == "" && strings.Contains(commit, "--author") {
			t.Fatalf("a run for nobody named an author: %s", commit)
		}
	}
}

// THE CHECKOUT IS MEASURED BEFORE THE AGENT SEES IT: dev's scratch directory is
// kept out of the run's commit, and a clone with no code in it is reported as
// Empty; a turn whose answer file is empty is reported Unanswered. Both are what
// the run is judged on when it changed nothing (coding.go hollow).
func TestSandboxRun_ReportsAnEmptyCheckoutAndAnUnansweredTurn(t *testing.T) {
	for _, c := range []struct {
		name              string
		code, answer      string
		empty, unanswered bool
	}{
		{name: "a placeholder, no last message", code: "0\n", answer: "", empty: true, unanswered: true},
		{name: "code, answered", code: "1\n", answer: "The test already covers it.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &pod{answer: func(argv []string) client.Ran {
				line := strings.Join(argv, " ")
				switch {
				case strings.Contains(line, "/.code/"):
					return client.Ran{Stdout: c.code}
				case slices.Contains(argv, "--skip-git-repo-check"):
					return client.Ran{Stdout: workedTurn}
				case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
					return client.Ran{Stdout: c.answer}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.Empty != c.empty || res.Unanswered != c.unanswered {
				t.Fatalf("empty %v, unanswered %v; want %v, %v", res.Empty, res.Unanswered, c.empty, c.unanswered)
			}
			scratch, harness := at(p, "/.code/"), at(p, "--skip-git-repo-check")
			if scratch < 0 || scratch > harness {
				t.Fatalf("dev's scratch was not excluded before the agent ran:\n%s", strings.Join(p.lines(), "\n"))
			}
		})
	}
}

// narratingTwiceThenActs is a turn that narrates with U+2019 apostrophes (like production)
const narratingTwiceThenActs = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I\u2019m reading the contracts and the tier lookup before patching them together."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// narratingAgain is a second narrating turn (message-only, items<=1)
const narratingAgain = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Now I\u2019m patching those and the required tests."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// actedTurn is a turn that actually does work (file_change item)
const actedTurn = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Wrote the fix."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// threeNarratingTurns - three consecutive narrating turns (message-only)
const narratingThird = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"I\u2019ll run the tests next."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// A RUN THAT NARRATES TWICE THEN ACTS SUCCEEDS. Two message-only check turns that
// are still narrating must not break the settle loop; on the third turn the agent
// acts (file_change) and the run succeeds with Changed=true, Unacted=false.
func TestSandboxRun_TwoNarratingTurnsThenActsSucceeds(t *testing.T) {
	tip, ran := theSHA, 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			ran++
			switch ran {
			case 1:
				return client.Ran{Stdout: narratingTwiceThenActs}
			case 2:
				return client.Ran{Stdout: narratingAgain}
			case 3:
				return client.Ran{Stdout: actedTurn}
			}
			return client.Ran{Stdout: actedTurn}
		case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
			switch ran {
			case 1:
				return client.Ran{Stdout: "I\u2019m reading the contracts and the tier lookup before patching them together.\n"}
			case 2:
				return client.Ran{Stdout: "Now I\u2019m patching those and the required tests.\n"}
			case 3:
				return client.Ran{Stdout: "Wrote the fix.\n"}
			}
			return client.Ran{Stdout: "Wrote the fix.\n"}
		case slices.Contains(argv, "commit"):
			if ran >= 3 {
				tip = newSHA
			}
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ran != 3 {
		t.Fatalf("the harness ran %d times, want 3 (two narrating + one acting)", ran)
	}
	if !res.Changed {
		t.Fatalf("run should be Changed after acting turn: %+v", res)
	}
	if res.Unacted {
		t.Fatalf("run should NOT be Unacted after acting: %+v", res)
	}
}

// A RUN THAT NARRATES THREE TIMES IS UNACTED. Three consecutive message-only check
// turns while Unacted must mark the run Unacted with the existing error message.
func TestSandboxRun_ThreeNarratingTurnsIsUnacted(t *testing.T) {
	tip, ran := theSHA, 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			ran++
			switch ran {
			case 1:
				return client.Ran{Stdout: narratingTwiceThenActs}
			case 2:
				return client.Ran{Stdout: narratingAgain}
			case 3:
				return client.Ran{Stdout: narratingThird}
			}
			return client.Ran{Stdout: narratingThird}
		case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
			switch ran {
			case 1:
				return client.Ran{Stdout: "I\u2019m reading the contracts and the tier lookup before patching them together.\n"}
			case 2:
				return client.Ran{Stdout: "Now I\u2019m patching those and the required tests.\n"}
			case 3:
				return client.Ran{Stdout: "I\u2019ll run the tests next.\n"}
			}
			return client.Ran{Stdout: "I\u2019ll run the tests next.\n"}
		case slices.Contains(argv, "commit"):
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ran != 3 {
		t.Fatalf("the harness ran %d times, want 3 (three narrating)", ran)
	}
	if res.Changed {
		t.Fatalf("run should NOT be Changed after only narrating: %+v", res)
	}
	if !res.Unacted {
		t.Fatalf("run should be Unacted after three narrating turns: %+v", res)
	}
	if why := hollow(Req{}, res); !strings.Contains(why, "the agent said what it would do and changed nothing") {
		t.Fatalf("Unacted hollow message should mention the agent said what it would do: %q", why)
	}
}

// A SETTLED "NO CHANGE NEEDED" STOPS IMMEDIATELY. A check turn that answers settle
// with "No change needed: ..." must stop the settle loop immediately without
// counting toward maxUnacted.
func TestSandboxRun_SettledNoChangeStopsImmediately(t *testing.T) {
	tip, ran := theSHA, 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			ran++
			switch ran {
			case 1:
				return client.Ran{Stdout: announcedTurn} // first turn announces
			case 2:
				return client.Ran{Stdout: settledTurn} // second turn says settled
			}
			return client.Ran{Stdout: settledTurn}
		case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
			switch ran {
			case 1:
				return client.Ran{Stdout: "Let me create the spec document.\n"}
			case 2:
				return client.Ran{Stdout: "No change needed: the handler already validates it.\n"}
			}
			return client.Ran{Stdout: "No change needed: the handler already validates it.\n"}
		case slices.Contains(argv, "commit"):
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ran != 2 {
		t.Fatalf("the harness ran %d times, want 2 (announced + settled)", ran)
	}
	if res.Changed {
		t.Fatalf("run should NOT be Changed: %+v", res)
	}
	if res.Unacted {
		t.Fatalf("run should NOT be Unacted when settled: %+v", res)
	}
	if !strings.Contains(res.Answer, "No change needed") {
		t.Fatalf("answer should contain settled message: %q", res.Answer)
	}
}

// THE UNACTED COUNTER IS RESET WHEN A TOOL CALL IS MADE. If the agent narrates
// once, then makes a tool call (items>1), the unacted counter resets.
func TestSandboxRun_UnactedCounterResetsOnToolCall(t *testing.T) {
	tip, ran := theSHA, 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			ran++
			switch ran {
			case 1:
				return client.Ran{Stdout: narratingTwiceThenActs} // narrating, message-only
			case 2:
				// Second turn: tool call (command_execution) - this resets the counter
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"ls","exit_code":0}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Let me check the files first."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`}
			case 3:
				// Third turn: narrating again - counter should be 1, not 2
				return client.Ran{Stdout: narratingAgain}
			case 4:
				// Fourth turn: acts
				return client.Ran{Stdout: actedTurn}
			}
			return client.Ran{Stdout: actedTurn}
		case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
			switch ran {
			case 1:
				return client.Ran{Stdout: "I\u2019m reading the contracts and the tier lookup before patching them together.\n"}
			case 2:
				return client.Ran{Stdout: "Let me check the files first.\n"}
			case 3:
				return client.Ran{Stdout: "Now I\u2019m patching those and the required tests.\n"}
			case 4:
				return client.Ran{Stdout: "Wrote the fix.\n"}
			}
			return client.Ran{Stdout: "Wrote the fix.\n"}
		case slices.Contains(argv, "commit"):
			if ran >= 4 {
				tip = newSHA
			}
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Should have: 1st narrating, 2nd tool call (resets), 3rd narrating (counter=1), 4th acts
	if ran != 4 {
		t.Fatalf("the harness ran %d times, want 4 (narrate, tool, narrate, act)", ran)
	}
	if !res.Changed {
		t.Fatalf("run should be Changed after acting: %+v", res)
	}
	if res.Unacted {
		t.Fatalf("run should NOT be Unacted: %+v", res)
	}
}

func TestConfigureGoEnvIdempotentAcrossRearm(t *testing.T) {
	dir := t.TempDir()
	gitconfig := filepath.Join(dir, "gitconfig")
	goenv := filepath.Join(dir, "goenv")
	p := &pod{answer: func(argv []string) client.Ran {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(),
			"HOME="+dir,
			"GIT_CONFIG_GLOBAL="+gitconfig,
			"GIT_CONFIG_NOSYSTEM=1",
			"GOENV="+goenv,
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			code := 1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
			return client.Ran{ExitCode: code, Stderr: string(out)}
		}
		return client.Ran{Stdout: string(out)}
	}}
	servePod(t, p)
	in := sandbox{ctx: context.Background(), id: "sbx_1", dir: dir}
	req := RunRequest{
		Tool:    "hanzo",
		Remote:  "https://github.com/hanzoai/cloud.git",
		ReadKey: "ghs_test",
	}
	if err := in.configureGoEnv(req); err != nil {
		t.Fatalf("first configureGoEnv: %v", err)
	}
	// Second invocation (e.g. rearm on a reused pod where url.https://github.com/.insteadOf
	// already has two values) must succeed without exit 5.
	if err := in.configureGoEnv(req); err != nil {
		t.Fatalf("second configureGoEnv (rearm): %v", err)
	}
	got, err := exec.Command("git", "config", "--file", gitconfig, "--get-all", "url.https://github.com/.insteadOf").Output()
	if err != nil {
		t.Fatalf("read insteadOf: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	want := []string{"git@github.com:", "ssh://git@github.com/"}
	if !slices.Equal(lines, want) {
		t.Fatalf("insteadOf = %v, want %v", lines, want)
	}
}
