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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// theKey is the push credential a run holds: an OpenSSH private key, minted per
// run and registered on ONE repository as a deploy key. Spelled once, and
// spelled MULTI-LINE on purpose — every assertion about where it may and may not
// appear has to ask about a value that a log can re-wrap, re-indent or print one
// line at a time.
const theKey = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gt
ZWQyNTUxOQAAACBSRUFMTE9PS0lOR1NFQ1JFVEtFWU1BVEVSSUFMMTIzNDU2Nzg5MA
-----END OPENSSH PRIVATE KEY-----`

// theHostKey is the forge's PUBLIC host key, as cloud learned it. It is not a
// secret — it is the pin — so unlike theKey it is expected on argv.
const theHostKey = "git.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHOSTKEYVALUEHERE"

const (
	sshRemote = "git@git.test:acme/api.git"
	theSHA    = "1111111111111111111111111111111111111111"
	newSHA    = "2222222222222222222222222222222222222222"
)

// pod is a fake sandbox that records every command and answers whatever the test
// scripts. It records the argv VERBATIM, because the questions worth asking of
// this file are all questions about arguments.
type pod struct {
	mu  sync.Mutex
	ran [][]string
	// fed is what each command was given on STDIN, index-aligned with ran. The
	// run's key travels this way and nowhere else, so a test that only watched
	// argv could not tell "the secret is confined" from "the secret is absent".
	fed    []string
	answer func(argv []string) client.Ran
	// refuse, when it answers an error, is what the sandbox says to a command in
	// place of its result: a timeout's "context deadline exceeded", for one.
	refuse func(argv []string) error
	// during, when set, runs after a command is recorded and answered and before its result goes
	// back: a command that takes time, which ends early when the run is stopped (its context is done).
	during func(ctx context.Context, argv []string) error
	// leaseErr, when set and it answers an error, is what the sandbox app says to a lease in
	// place of a sandbox (the rig's).
	leaseErr func() error
	// leased is every lease the run asked for, verbatim. A lease is a REQUEST for
	// resources — a class, a ttl, and whether a disk is wanted — and the only place
	// that request is visible is here, before it reaches a cluster.
	leased []client.LeaseIn
	// lessees is who each lease was asked by, index-aligned with leased, and
	// runners who each command was: the sandbox app pays a lease from the stated
	// caller's wallet, and holds the sandbox for them alone.
	lessees []zip.Caller
	runners []zip.Caller
	// stops is how many times the run asked the sandbox to stop what it runs,
	// and ends how many times it ended the sandbox.
	stops, ends int
	// blind is what each command asked the sandbox to keep out of every stream,
	// index-aligned with ran.
	blind [][]string
	// timeouts is the time each command was given, index-aligned with ran.
	timeouts []int
	// broke makes the prepaid gate answer that the org holds no funds, which is the
	// free lane.
	broke bool
	// expires and carried are what the lease answers: when it ends, and whether the
	// sandbox extends it while it works. Zero is a lease that says nothing.
	expires int64
	carried bool
	// endsAt is when the lease says the sandbox is ended whatever it is doing; zero says nothing.
	endsAt int64
}

func (p *pod) exec(argv []string, stdin string, blind ...string) client.Ran {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ran = append(p.ran, append([]string(nil), argv...))
	p.fed = append(p.fed, stdin)
	p.blind = append(p.blind, blind)
	var r client.Ran
	if p.answer != nil {
		r = p.answer(argv)
	}
	// The dev in a sandbox names its version when asked (takesRenewal), as the image's does: the
	// one a test's answer does not name is a current one.
	if r.Stdout == "" && r.ExitCode == 0 && slices.Equal(argv, []string{"dev", "--version"}) {
		r.Stdout = "dev 0.7.10\n"
	}
	return r
}

// lines renders every command as one string, which is how a human reads a
// transcript and how these assertions read it too.
func (p *pod) lines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.ran))
	for _, argv := range p.ran {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// did returns the first command containing want, and whether there was one.
func (p *pod) did(want string) (string, bool) {
	for _, l := range p.lines() {
		if strings.Contains(l, want) {
			return l, true
		}
	}
	return "", false
}

// gitVerb reports whether any command asked git to do verb — matched on the
// argv, so "push" the verb is not confused with "push" inside a URL or a prompt.
func (p *pod) gitVerb(verb string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, argv := range p.ran {
		for i, a := range argv {
			if a == verb && i > 0 && argv[0] == "git" {
				return true
			}
		}
	}
	return false
}

// servePod stands up the two peers a sandbox run reaches — the sandbox itself and
// the prepaid gate — on real unix sockets, so the runner under test is the
// production one with a production transport.
func servePod(t *testing.T, p *pod) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))

	sandboxes := zip.New(zip.Config{AppName: sandboxpeer.App, DisableStartupMessage: true})
	sandboxes.Post("/sandbox/lease",
		func(ctx context.Context, in *client.LeaseIn) (*client.Leased, error) {
			p.mu.Lock()
			p.leased = append(p.leased, *in)
			p.lessees = append(p.lessees, zip.CallerOf(ctx))
			p.mu.Unlock()
			return &client.Leased{ID: "sbx_1", Class: in.Class, Status: "ready", Workdir: "/work",
				ExpiresAt: p.expires, Carried: p.carried, EndsAt: p.endsAt}, nil
		}, zip.WithOperationID(client.SandboxLease))
	sandboxes.Post("/sandbox/run",
		func(ctx context.Context, in *client.RunIn) (*client.Ran, error) {
			p.mu.Lock()
			p.timeouts = append(p.timeouts, in.TimeoutSec)
			p.runners = append(p.runners, zip.CallerOf(ctx))
			p.mu.Unlock()
			r := p.exec(in.Argv, in.Stdin, in.Blind...)
			if p.refuse != nil {
				if err := p.refuse(in.Argv); err != nil {
					return nil, err
				}
			}
			if p.during != nil {
				if err := p.during(ctx, in.Argv); err != nil {
					return nil, err
				}
			}
			return &r, nil
		}, zip.WithOperationID(client.SandboxRun))
	sandboxes.Post("/sandbox/end",
		func(_ context.Context, _ *client.EndIn) (*struct{}, error) {
			p.mu.Lock()
			p.ends++
			p.mu.Unlock()
			return &struct{}{}, nil
		}, zip.WithOperationID(client.SandboxEnd))
	sandboxes.Post("/sandbox/stop",
		func(_ context.Context, _ *client.StopIn) (*client.Stopped, error) {
			p.mu.Lock()
			p.stops++
			p.mu.Unlock()
			return &client.Stopped{Stopped: 1}, nil
		}, zip.WithOperationID(client.SandboxStop))

	commerce := zip.New(zip.Config{AppName: "commerce", DisableStartupMessage: true})
	commerce.Post("/commerce/authorize",
		func(_ context.Context, _ *client.AuthorizeIn) (*client.Verdict, error) {
			return &client.Verdict{OK: !p.broke, NoFunds: p.broke}, nil
		}, zip.WithOperationID(client.FinanceAuthorize))

	for name, app := range map[string]*zip.App{sandboxpeer.App: sandboxes, "commerce": commerce} {
		plane.Bind()
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}
}

// aRun is the request a real dispatch builds: a checkout, the branch cloud issued
// for the session, and the grant that may write exactly that one ref.
func aRun() RunRequest {
	return RunRequest{
		Remote: sshRemote, BaseBranch: "main", Branch: "agent/abc123def456",
		Prompt: "fix the flake", SessionID: "sess_abc123def456",
		RunTimeoutSeconds: 60, Key: theKey, Known: theHostKey,
	}
}

// steps collects what the run said out loud, which is what a person watching in
// Slack actually reads.
func steps(out *[]string) func(Step) {
	return func(s Step) { *out = append(*out, s.Step+" "+s.Message+" "+s.Status) }
}

// A RUN ASKS FOR NO PROJECT, and for a disk of its own, which the sandbox keeps
// across a park and deletes with the lease.
//
// A project names a per-(org, project) 20Gi PVC that apps/sandbox deliberately
// KEEPS when a lease ends, so a checkout survives between sessions. A run used to
// pass its SESSION id there — a name minted per dispatch and never reopened — so
// each run created a disk that could never be addressed again and was then kept
// forever. That is the whole mechanism of the leak, and it is visible only here,
// in what the lease ASKS for; the run's own result looks identical either way.
//
// The assertion is on Project and not on some later cleanup on purpose. A purge on
// the way out cannot cover a run that is killed, OOM'd, or evicted before it says
// goodbye, whereas a lease that never asks for a disk cannot strand one. The pod's
// emptyDir already has exactly the lifetime a run wants.
func TestSandboxRun_LeasesNoDiskBecauseARunIsNotAProject(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.leased) != 1 {
		t.Fatalf("a run took %d leases, want exactly 1", len(p.leased))
	}
	if got := p.leased[0].Project; got != "" {
		t.Fatalf("the run asked for project %q, which mints a 20Gi disk keyed to a name "+
			"no later lease can ever reuse — a run must ask for no project at all", got)
	}
	if !p.leased[0].Disk {
		t.Fatal("the run asked for no disk of its own, so its tree would not survive a park")
	}
}

// A run that edited NOTHING says so, and touches no ref. This is the first test
// because the false PR is the worse failure: a branch with no commits, filed as
// work, teaches a reviewer to ignore the agent.
func TestSandboxRun_NoEditsReportNoChangesAndWriteNoRef(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"} // the tip never moves
		case slices.Contains(argv, "commit"):
			return client.Ran{ExitCode: 1, Stdout: "nothing to commit, working tree clean\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	var said []string
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), steps(&said))
	if err != nil {
		t.Fatalf("a clean run is not an error: %v", err)
	}
	if !res.OK {
		t.Fatalf("a run that changed nothing still succeeded: %+v", res)
	}
	if res.Changed || res.CommitSha != "" || res.Diffstat != "" {
		t.Fatalf(`"no changes" must mean no changes: %+v`, res)
	}
	if p.gitVerb("push") {
		t.Fatalf("nothing changed and something was pushed:\n%s", strings.Join(p.lines(), "\n"))
	}
}

// A run that edited something commits it, pushes it to its own ref, and reports
// what it actually did — the tip it created and the diffstat it measured.
func TestSandboxRun_EditsAreCommittedPushedAndReportedHonestly(t *testing.T) {
	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "commit"):
			tip = newSHA // the commit is what moves it
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 2 files changed, 9 insertions(+), 1 deletion(-)\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)

	var said []string
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), steps(&said))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.OK || !res.Changed {
		t.Fatalf("the run changed something and must say so: %+v", res)
	}
	if res.CommitSha != newSHA {
		t.Fatalf("the reported tip is not the one that was created: %q", res.CommitSha)
	}
	if !strings.Contains(res.Diffstat, "2 files changed") {
		t.Fatalf("the diffstat was not measured: %q", res.Diffstat)
	}

	// THE BRANCH IS THE RUN'S OWN, and it is created before the first edit so
	// nothing is ever committed onto the base.
	if _, ok := p.did("switch -c agent/abc123def456"); !ok {
		t.Fatalf("the run never left the base branch:\n%s", strings.Join(p.lines(), "\n"))
	}
	push, ok := p.did(" push ")
	if !ok {
		t.Fatalf("nothing was pushed:\n%s", strings.Join(p.lines(), "\n"))
	}
	// ONE ref, named in full, never a branch name git could resolve to something
	// else and never a force.
	if !strings.HasSuffix(push, "HEAD:refs/heads/agent/abc123def456") {
		t.Fatalf("the push does not name exactly one ref: %q", push)
	}
	for _, forbidden := range []string{"--force", "-f ", "--mirror", "--all", "--delete"} {
		if strings.Contains(push, forbidden) {
			t.Fatalf("the push carries %q: %q", forbidden, push)
		}
	}
}

// The branch is the one cloud issued, and the runner will not write anywhere
// else even when it is handed a name that says otherwise. The forge refuses this
// too (apps/git/refpolicy.go); neither is the other's backstop.
func TestSandboxRun_RefusesToWriteOutsideTheAgentNamespace(t *testing.T) {
	p := &pod{}
	servePod(t, p)

	req := aRun()
	req.Branch = "main"
	r := sandboxRunner{}
	if _, err := r.Run(context.Background(), "acme", "u_1", req, nil); err == nil {
		t.Fatal("a run pointed at main must be refused")
	}
	if len(p.lines()) != 0 {
		t.Fatalf("a refused run still did work:\n%s", strings.Join(p.lines(), "\n"))
	}
}

// A tool that failed has produced nothing anyone should review, so its checkout
// stays in the sandbox and dies there.
func TestSandboxRun_AFailedToolWritesNoRef(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		if slices.Contains(argv, "dev") {
			return client.Ran{ExitCode: 3, Stderr: "the model gave up\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("a failed tool is a reported failure, not a transport error: %v", err)
	}
	if res.OK || res.Changed {
		t.Fatalf("a failed run must not report work: %+v", res)
	}
	if p.gitVerb("push") {
		t.Fatalf("a failed run pushed:\n%s", strings.Join(p.lines(), "\n"))
	}
	// The failure is what the person who asked is told, so it names the fault in
	// the harness's own last words, not only a status.
	if res.Error != "the dev harness exited 3: the model gave up" {
		t.Fatalf("the failure reads %q, which does not say why", res.Error)
	}
}

// emptyTurn is a dev turn on which the model answered nothing: begun and
// completed with no item and no token.
const emptyTurn = `{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"turn.completed","usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0}}
`

// workedTurn is a dev turn that wrote a file.
const workedTurn = `{"type":"thread.started","thread_id":"t2"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// AN EMPTY TURN IS ASKED ONCE MORE. A model that answered nothing has not said
// the task needs no change, so the harness runs again before the run reports —
// once, so a model that keeps answering nothing cannot hold a run open.
func TestSandboxRun_AnEmptyTurnIsAskedOnceMore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		turns []string
		want  int
	}{
		{"empty then worked", []string{emptyTurn, workedTurn}, 2},
		{"empty twice", []string{emptyTurn, emptyTurn, workedTurn}, 2},
		{"worked", []string{workedTurn, workedTurn}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tip, ran := theSHA, 0
			p := &pod{}
			p.answer = func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "dev") && !slices.Contains(argv, "--version"):
					ran++
					return client.Ran{Stdout: tc.turns[ran-1]}
				case slices.Contains(argv, "commit"):
					if tc.turns[ran-1] == workedTurn {
						tip = newSHA
					}
					return client.Ran{}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: tip + "\n"}
				}
				return client.Ran{}
			}
			servePod(t, p)

			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if ran != tc.want {
				t.Fatalf("the harness ran %d times, want %d", ran, tc.want)
			}
			if worked := tc.turns[ran-1] == workedTurn; res.Changed != worked {
				t.Fatalf("changed = %v after a turn that worked = %v: %+v", res.Changed, worked, res)
			}
		})
	}
}

// announcedTurn is the dev turn measured in sess_33ef32fc… (hanzo-inc/cloud#451):
// it read the repository, then ended on what it would do next, with no tool call
// after it.
const announcedTurn = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Let me create the spec document."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// settledTurn is a turn in loopThread that answers settle: none is needed.
const settledTurn = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"No change needed: the handler already validates it."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":12}}
`

// A BUILD THAT CHANGED NOTHING IS ASKED ONCE MORE (hanzo-inc/cloud#451), in its
// own conversation: to make the change it announced, or to say none is needed.
// One that does neither is Unacted. A plan is never asked, nor a run that
// changed something, nor a turn in no conversation a resume can name.
func TestSandboxRun_ABuildThatChangedNothingIsAskedOnce(t *testing.T) {
	type turn struct {
		out, said string // its events, and the last message dev writes to answerPath
		acts      bool   // its commit moves the tip
	}
	announced := turn{announcedTurn, "Let me create the spec document.", false}
	readMore := turn{`{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Now let me look at the turn function:"}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`, "Now let me look at the turn function:", false}
	for _, c := range []struct {
		name             string
		plan             bool
		turns            []turn
		want             int
		changed, unacted bool
		answer           string
	}{
		{name: "announced, then acted", turns: []turn{announced, {resumed, "Wrote docs/lease.md.", true}},
			want: 2, changed: true, answer: "Wrote docs/lease.md."},
		{name: "announced, read more, then acted", turns: []turn{announced, readMore, {resumed, "Wrote docs/lease.md.", true}},
			want: 3, changed: true, answer: "Wrote docs/lease.md."},
		{name: "announced, then said none is needed", turns: []turn{announced, {settledTurn, "No change needed: the handler already validates it.", false}},
			want: 2, answer: "No change needed: the handler already validates it."},
		{name: "announced twice", turns: []turn{announced, announced},
			want: maxUnacted, unacted: true, answer: "Let me create the spec document."},
		{name: "changed at once", turns: []turn{{resumed, "Wrote docs/lease.md.", true}},
			want: 1, changed: true, answer: "Wrote docs/lease.md."},
		{name: "a plan", plan: true, turns: []turn{announced}, want: 1},
		{name: "no conversation to resume", turns: []turn{{workedTurn, "Looked around.", false}},
			want: 1, answer: "Looked around."},
	} {
		t.Run(c.name, func(t *testing.T) {
			tip, ran := theSHA, 0
			p := &pod{answer: func(argv []string) client.Ran {
				now := c.turns[min(max(ran, 1), len(c.turns))-1]
				switch {
				case slices.Contains(argv, "--skip-git-repo-check"):
					ran++
					return client.Ran{Stdout: c.turns[min(ran, len(c.turns))-1].out}
				case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
					return client.Ran{Stdout: now.said + "\n"}
				case slices.Contains(argv, "commit"):
					if now.acts {
						tip = newSHA
					}
					return client.Ran{}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: tip + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			req := aRun()
			req.Plan = c.plan
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if ran != c.want {
				t.Fatalf("the harness ran %d times, want %d", ran, c.want)
			}
			if res.Changed != c.changed || res.Unacted != c.unacted {
				t.Fatalf("changed %v, unacted %v; want %v, %v: %+v", res.Changed, res.Unacted, c.changed, c.unacted, res)
			}
			if !c.plan && res.Answer != c.answer {
				t.Fatalf("answer %q, want %q", res.Answer, c.answer)
			}
			if c.want < 2 {
				return
			}
			again := harnessRuns(p)[1]
			if r := slices.Index(again, "resume"); r < 0 || r+3 >= len(again) || again[r+1] != "--" || again[r+2] != loopThread || again[r+3] != "-" {
				t.Fatalf("the second turn was not a resume of the first one's conversation: %q", again)
			}
			if told := toldOf(harnessFed(p)[1]); !strings.HasPrefix(told, settle) {
				t.Fatalf("the second turn did not carry settle: %q", told)
			}
		})
	}
}

func TestSettled(t *testing.T) {
	for answer, want := range map[string]bool{
		"No change needed: the handler already validates it.":               true,
		"No changes are needed; the test covers it.":                        true,
		"**No change needed:** it is already there.":                        true,
		"Let me create the spec document.":                                  false,
		"I have finished reading live.go; next I will edit it.":             false,
		"Once that is done, I'll add the regression test.":                  false,
		"I've completed inspecting the codebase and will now apply the fix": false,
		"I made no change.": false,
		"":                  false,
	} {
		if settled(answer) != want {
			t.Errorf("settled(%q) = %v, want %v", answer, !want, want)
		}
	}
}

// THE KEY ARRIVES ON STDIN AND IS NEVER AN ARGUMENT.
//
// Argv is public — every process in the pod can read another's command line out
// of /proc, and a run echoes its own argv into its session narration — and the
// process this pod is about to run executes a language model's output. So the
// one command that carries the key must carry it on stdin, and no command may
// carry it at all.
func TestSandboxRun_TheKeyTravelsOnStdinAndNeverOnArgv(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{Stdout: "done\n"}
	}}
	servePod(t, p)

	var said []string
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), steps(&said))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Every secret-bearing line of the key, not just the whole blob: a value that
	// only matches when un-rewrapped is a value a re-indenting log would leak.
	var secret []string
	for line := range strings.SplitSeq(theKey, "\n") {
		if line = strings.TrimSpace(line); len(line) >= 16 && !strings.HasPrefix(line, "-----") {
			secret = append(secret, line)
		}
	}

	fedTheKey := 0
	for n, argv := range p.ran {
		for i, a := range argv {
			for _, sec := range secret {
				if strings.Contains(a, sec) {
					t.Fatalf("the key is in argv[%d] of %q", i, strings.Join(argv, " "))
				}
			}
		}
		if strings.Contains(p.fed[n], secret[0]) {
			fedTheKey++
			// It is installed by ONE command, and that command writes it with a
			// umask ssh will accept — ssh refuses a key others can read, so the mode
			// is the difference between a run that clones and one that does not.
			line := strings.Join(argv, " ")
			if !strings.Contains(line, "umask 077") {
				t.Fatalf("the key is written without a private umask: %q", line)
			}
			if !strings.Contains(line, keyPath) {
				t.Fatalf("the key is not written to the one path git is told to read: %q", line)
			}
		}
	}
	if fedTheKey != 1 {
		t.Fatalf("the key was fed to %d commands, want exactly 1", fedTheKey)
	}

	// IT IS NOT IN THE CHECKOUT. The checkout is what the model edits and what
	// `git add -A` stages, so a key kept there is one commit away from being
	// pushed to the branch this very grant opens.
	if !strings.HasPrefix(keyPath, "/tmp/") {
		t.Fatalf("the key lives at %q, inside the tree the run stages and pushes", keyPath)
	}

	// Nothing a person reads carries it.
	for _, sec := range secret {
		for _, said := range said {
			if strings.Contains(said, sec) {
				t.Fatalf("the key was narrated: %q", said)
			}
		}
		if strings.Contains(res.LogTail, sec) {
			t.Fatalf("the key came back in the log tail: %q", res.LogTail)
		}
	}
}

// THE HOST IS PINNED TO WHAT CLOUD SAW, and the credential is confined to one
// invocation.
//
// The sandbox's network is the one network in this system carrying untrusted
// output, so a run that accepted whatever host key answered would hand a write
// credential — and its trust in the code it checks out — to anyone standing in
// front of the forge. `accept-new` is trust-on-first-use per run, which for a
// pod that lives once is no pin at all.
func TestSandboxRun_PinsTheForgeHostAndConfinesTheCredential(t *testing.T) {
	// The tip MOVES, so the run reaches its push: clone and push are the two
	// commands that carry the key, and a run that stopped at "no changes" would
	// let this test pass having checked only half of them.
	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "commit"):
			tip = newSHA
			return client.Ran{}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)

	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}

	saw := 0
	for _, argv := range p.ran {
		if len(argv) == 0 || argv[0] != "git" {
			continue
		}
		for i, a := range argv {
			if !strings.HasPrefix(a, "core.sshCommand=") {
				continue
			}
			saw++
			// `git -c` BEFORE the subcommand is not written into the new
			// repository's config, unlike `git clone -c` — so the checkout the model
			// then edits names no identity file.
			if i == 0 || argv[i-1] != "-c" {
				t.Fatalf("the ssh command is not a per-invocation option: %q", strings.Join(argv, " "))
			}
			for _, want := range []string{
				"-i " + keyPath,
				"-o IdentitiesOnly=yes",        // no agent key, no ~/.ssh/id_* first
				"-o StrictHostKeyChecking=yes", // never TOFU
				"-o UserKnownHostsFile=" + knownPath,
			} {
				if !strings.Contains(a, want) {
					t.Fatalf("the ssh command is missing %q: %q", want, a)
				}
			}
			for _, forbidden := range []string{
				"StrictHostKeyChecking=no", "StrictHostKeyChecking=accept-new",
				"UserKnownHostsFile=/dev/null", // the pin must not be thrown away
			} {
				if strings.Contains(a, forbidden) {
					t.Fatalf("the ssh command disables host verification with %q: %q", forbidden, a)
				}
			}
			// The SECOND known_hosts ssh reads is silenced. /etc/ssh/ssh_known_hosts
			// is part of the image, so an entry for the forge there would be a
			// trusted key our pin never sees.
			if !strings.Contains(a, "GlobalKnownHostsFile=/dev/null") {
				t.Fatalf("the system known_hosts is still trusted: %q", a)
			}
		}
		// Nothing may persist a credential past the command that used it. The one
		// config the run writes is safe.directory, which names a path and no secret.
		line := strings.Join(argv, " ")
		if line == "git config --global --add safe.directory /work" {
			continue
		}
		for _, persists := range []string{"git config", "credential.helper", ".git/config"} {
			if strings.Contains(line, persists) {
				t.Fatalf("a command persists a credential: %q", line)
			}
		}
	}
	if saw < 2 {
		t.Fatalf("only %d git commands carried the run's key; clone and push both must", saw)
	}

	// The known_hosts the run pins to is the line CLOUD learned, written by the
	// same command that installs the key.
	if _, ok := p.did(theHostKey); !ok {
		t.Fatalf("the forge host key cloud learned never reached the pod:\n%s", strings.Join(p.lines(), "\n"))
	}
}

// A run with NO host key to pin is refused before anything is leased. Falling
// back to "trust whatever answers" is the one thing this must never do.
func TestSandboxRun_RefusesToCloneUnpinned(t *testing.T) {
	p := &pod{}
	servePod(t, p)

	req := aRun()
	req.Known = ""
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil); err == nil {
		t.Fatal("a run with no host key to pin must be refused, not run unpinned")
	}
	if p.gitVerb("clone") {
		t.Fatalf("an unpinned run still cloned:\n%s", strings.Join(p.lines(), "\n"))
	}
}

// A command's output is scrubbed on the way out — line by line as well as whole
// — so a git that ever echoed its key cannot put live material in a session
// event, a span or a Slack thread.
func TestSandboxRun_AnEchoedKeyIsScrubbedOnTheWayOut(t *testing.T) {
	body := strings.Split(theKey, "\n")[1] // one secret line, as a log would print it
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		if slices.Contains(argv, "dev") {
			return client.Ran{Stdout: "read key: " + body + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(res.LogTail, body) {
		t.Fatalf("an echoed key line survived: %q", res.LogTail)
	}
	if !strings.Contains(res.LogTail, "[redacted]") {
		t.Fatalf("nothing was scrubbed: %q", res.LogTail)
	}
}

// The three properties of argvFor that a run actually dies of, each pinned
// against what the harness was MEASURED to do in a dev-class pod
// (oci.hanzo.ai/hanzoai/sandbox 1.0.1: dev 0.6.91, claude 2.1.223, codex 0.146.1).
