package coding

// steer_test.go — a coding run that is stopped, paused or redirected keeps what
// it did, and only its own person can redirect it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A run stopped while its task is working does not lose the work: the task is
// stopped in the pod, and what it left in the checkout is committed and pushed to
// the run's own branch, exactly as a finished run's is.
func TestSandboxRun_AStoppedTaskKeepsItsWork(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"): // the task, stopped while it works
			cancel(fmt.Errorf("%w: by its owner", agents.ErrStopped))
			return client.Ran{ExitCode: 130}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed, 3 insertions(+)\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)

	res, err := sandboxRunner{}.Run(ctx, "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("a stopped run keeps its work rather than failing: %v", err)
	}
	if !res.Changed || res.CommitSha != newSHA {
		t.Fatalf("the work so far must be committed and reported: %+v", res)
	}
	if !p.gitVerb("push") {
		t.Fatalf("the work so far was not pushed:\n%s", strings.Join(p.lines(), "\n"))
	}
	p.mu.Lock()
	stops := p.stops
	p.mu.Unlock()
	if stops != 1 {
		t.Fatalf("the task must be stopped in the pod before its work is taken, stops=%d", stops)
	}
}

// fakeRun registers a flight as Start does, for a run that is working.
func fakeRun(t *testing.T, session, subject string, to client.Reply) (*flight, context.Context) {
	t.Helper()
	ctx, stop := context.WithCancelCause(context.Background())
	f := &flight{org: "acme", subject: subject, session: session, cancel: stop, done: make(chan struct{}), ctx: ctx,
		in: client.CodingStartIn{Repo: "api", Prompt: "add a health check", Reply: to}}
	fly(f)
	t.Cleanup(func() { f.forget() })
	return f, ctx
}

// Stop and pause reach a run from wherever they are asked — the session is held
// with agents — as its context's cause. A paused run waits for a Continue, and a
// Stop lets it go.
func TestAStopOrPauseOfTheSessionReachesTheRun(t *testing.T) {
	f, ctx := fakeRun(t, "sess_p", "u-acme", client.Reply{})
	f.interrupt(agents.ErrPaused)
	if !errors.Is(context.Cause(ctx), agents.ErrPaused) {
		t.Fatalf("the run's cause is %v, want paused", context.Cause(ctx))
	}
	f.land(Result{Changed: true}, context.Cause(ctx))
	if find("acme", client.CodingSteerIn{SessionID: "sess_p"}) != f {
		t.Fatal("a paused run must wait for a Continue")
	}
	f.interrupt(fmt.Errorf("%w: by its owner", agents.ErrStopped))
	if find("acme", client.CodingSteerIn{SessionID: "sess_p"}) != nil {
		t.Fatal("a paused run that is stopped must be let go")
	}
}

// A message said in a run's thread reaches that run only for its own person, and
// only while it is working there; a run named by id is refused to anyone else.
func TestOnlyItsPersonRedirectsARun(t *testing.T) {
	to := client.Reply{Provider: "slack", Channel: "C1", Thread: "T1"}
	_, ctx := fakeRun(t, "sess_r", "u-acme", to)
	prev := followWait
	followWait = time.Millisecond // the run never lands here; its follow-up gives up at once
	t.Cleanup(func() { followWait = prev })

	var he *zip.HTTPError
	_, err := Follow(context.Background(), "acme", "u-other", client.CodingSteerIn{Subject: "u-other", SessionID: "sess_r", Text: "no"}, nil)
	if !errors.As(err, &he) || he.Status != http.StatusForbidden {
		t.Fatalf("another person redirected the run: %v", err)
	}
	for _, in := range []client.CodingSteerIn{
		{Subject: "u-other", Reply: to, Text: "no"},                                           // someone else in the thread
		{Subject: "u-acme", Reply: client.Reply{Provider: "slack", Channel: "C1"}, Text: "x"}, // the room, not the thread
	} {
		if _, err := Follow(context.Background(), "acme", in.Subject, in, nil); !errors.As(err, &he) || he.Status != http.StatusNotFound {
			t.Fatalf("%+v reached the run: %v", in, err)
		}
	}
	if context.Cause(ctx) != nil {
		t.Fatalf("a refused instruction stopped the run: %v", context.Cause(ctx))
	}

	if _, err := Follow(context.Background(), "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", Reply: to, Text: "use v2"}, nil); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(ctx), errFollowed) {
		t.Fatalf("the run was not stopped for its follow-up: %v", context.Cause(ctx))
	}
	if _, err := Follow(context.Background(), "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_r", Text: "again"}, nil); !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("one run was followed up twice: %v", err)
	}
}

// The follow-up is asked the whole task and what was said since, and told the
// work so far is on its branch when it is.
func TestAFollowUpIsTheTaskAndTheInstruction(t *testing.T) {
	got := followUp("add a health check", "use /healthz, not /health", true, false)
	for _, want := range []string{"add a health check", "use /healthz, not /health", "on this branch"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the follow-up's task %q is missing %q", got, want)
		}
	}
	if got := followUp("add a health check", "", false, false); !strings.Contains(got, "Continue") || strings.Contains(got, "branch") {
		t.Fatalf("a continue with nothing pushed: %q", got)
	}
	if got := followUp("add a health check", "use v2", false, true); !strings.Contains(got, "this checkout holds its work") || strings.Contains(got, "on this branch") {
		t.Fatalf("a steered run's follow-up is not told its work is in the checkout: %q", got)
	}
}

// A stopped or paused run says where its work is, and a paused run's message
// carries Continue and Stop while any other ending carries nothing.
func TestAStoppedRunSaysWhereItsWorkIs(t *testing.T) {
	for _, c := range []struct {
		payload, want string
	}{
		{`{"status":"stopped","changed":true,"branch":"agent/abc"}`, "Stopped. Work so far is on `agent/abc`."},
		{`{"status":"paused","changed":true,"branch":"agent/abc"}`, "Paused. Work so far is on `agent/abc`."},
		{`{"status":"stopped","changed":false}`, "Nothing had changed yet."},
		{`{"status":"stopped","changed":true,"branch":"agent/abc","followed":true}`, "follow-up run"},
	} {
		line, terminal := renderLine(kindStatus, []byte(c.payload))
		if !terminal || !strings.Contains(line, c.want) {
			t.Fatalf("%s rendered %q (terminal %v), want %q", c.payload, line, terminal, c.want)
		}
	}
}

// interrupting is a runner stopped mid-task with cause, which keeps its work as
// the sandbox runner does.
type interrupting struct {
	cancel context.CancelCauseFunc
	cause  error
}

func (r interrupting) Run(context.Context, string, string, RunRequest, func(Step)) (RunResult, error) {
	r.cancel(r.cause)
	// A runner that kept the work of a run out of time says so, as the real one does.
	return RunResult{Changed: true, CommitSha: "cafe", TimeUp: errors.Is(r.cause, errTimeUp)}, nil
}

// failing is a runner whose run was past its deadline while it was delivering: the
// push it was making is what the deadline aborted, so it kept nothing.
type failing struct{ cancel context.CancelCauseFunc }

func (r failing) Run(context.Context, string, string, RunRequest, func(Step)) (RunResult, error) {
	r.cancel(errTimeUp)
	return RunResult{}, errors.New("coding: push: context deadline exceeded")
}

// A paused run says where its work is and leaves its session live, with no PR;
// a stopped one ends its session stopped. Neither is reported as a failure.
func TestAnInterruptedRunEndsTheWayItWasAsked(t *testing.T) {
	for _, c := range []struct {
		cause  error
		status string
		closed []closeCall
	}{
		{agents.ErrPaused, statusPaused, nil},
		{fmt.Errorf("%w: by its owner", agents.ErrStopped), statusStopped, []closeCall{{"acme", "sess_abc123def456", statusStopped, ""}}},
	} {
		sess, pr := &fakeSessions{id: "sess_abc123def456"}, &fakePR{}
		ctx, cancel := context.WithCancelCause(context.Background())
		d, _ := dispatcherFor(sess, pr, nil, true)
		d.Runner = interrupting{cancel, c.cause}
		var lines []string
		d.Watch = func(_ context.Context, kind string, payload []byte) {
			if l, _ := renderLine(kind, payload); l != "" {
				lines = append(lines, l)
			}
		}
		res := d.Run(ctx, baseReq())
		if res.Error != c.status || !res.Changed || len(pr.inputs) != 0 {
			t.Fatalf("%s: %+v, %d PRs", c.status, res, len(pr.inputs))
		}
		if !slices.Equal(sess.closes, c.closed) {
			t.Fatalf("%s: closed %+v, want %+v", c.status, sess.closes, c.closed)
		}
		if last := lines[len(lines)-1]; !strings.Contains(last, "Work so far is on `agent/abc123def456`") {
			t.Fatalf("%s: the thread's last word is %q", c.status, last)
		}
	}
}

// Each agent harness is handed the run's ask server, with the time it may take:
// dev and codex as -c overrides, claude as an MCP config and an allowed tool. An
// interpreter has no MCP client and is handed nothing.
func TestAnAgentHarnessIsHandedTheAskServer(t *testing.T) {
	const ask = "https://api.test/v1/agent/ask"
	for _, tool := range []string{"", "codex"} {
		argv := strings.Join(argvFor(tool, ask, "", ""), " ")
		for _, want := range []string{`mcp_servers.run.url="` + ask + `"`, `{"X-Hanzo-Run"="HANZO_RUN"}`, "tool_timeout_sec=1860"} {
			if !strings.Contains(argv, want) {
				t.Fatalf("%q: missing %s in %s", tool, want, argv)
			}
		}
	}
	claude := strings.Join(argvFor("claude", ask, "", ""), " ")
	for _, want := range []string{`"url":"` + ask + `"`, `"X-Hanzo-Run":"${HANZO_RUN}"`, "--allowedTools mcp__run__ask_user", "MCP_TOOL_TIMEOUT=1860000"} {
		if !strings.Contains(claude, want) {
			t.Fatalf("claude: missing %s in %s", want, claude)
		}
	}
	if argv := argvFor("python", "", "", ""); strings.Contains(strings.Join(argv, " "), "mcp") {
		t.Fatalf("an interpreter was handed an MCP server: %q", argv)
	}
	if agentic("python") || agentic("node") || !agentic("") || !agentic("claude") {
		t.Fatal("only agent harnesses are handed the server")
	}
}

// A ticket opens its own run and nothing else: a ticket never minted, or one
// whose run has ended, names no run, and the ask is refused.
func TestATicketOpensOnlyItsOwnRun(t *testing.T) {
	tick := ticket()
	f, _ := fakeRun(t, "sess_k", "u-acme", client.Reply{Provider: "slack", Channel: "C1"})
	f.ticket = ticketKey(tick)
	fly(f)
	holds := func(tick string) bool {
		ok, err := Holds(tick)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !holds(tick) || holds(ticket()) || holds("") {
		t.Fatal("a ticket must name its own run and nothing else")
	}
	f.forget()
	if holds(tick) {
		t.Fatal("a finished run's ticket still opens the ask server")
	}
	if _, err := Ask(context.Background(), tick, client.AskIn{}); !errors.Is(err, ErrNoTicket) {
		t.Fatalf("an ask on a spent ticket: %v", err)
	}
}

// A run blocked on its question is still paused by a Pause: the wait ends with the
// run, and the harness is told it was paused.
func TestAPauseReachesARunWaitingOnItsPerson(t *testing.T) {
	tick := ticket()
	f, ctx := fakeRun(t, "sess_q", "u-acme", client.Reply{Provider: "slack", Channel: "C1"})
	f.ctx, f.ticket = ctx, ticketKey(tick)
	fly(f)
	waited := make(chan context.Context, 1)
	prev := askAgents
	askAgents = func(ctx context.Context, org, session string, to client.Reply, in client.AskIn, wait time.Duration) (string, error) {
		waited <- ctx
		<-ctx.Done()
		return "The run was paused before the question was answered.", nil
	}
	t.Cleanup(func() { askAgents = prev })
	out := make(chan string, 1)
	go func() {
		s, _ := Ask(context.Background(), tick, client.AskIn{Question: "q?"})
		out <- s
	}()
	asking := <-waited
	f.interrupt(agents.ErrPaused)
	if s := <-out; !strings.Contains(s, "paused") || !errors.Is(context.Cause(asking), agents.ErrPaused) {
		t.Fatalf("a paused run's ask ended %q (%v)", s, context.Cause(asking))
	}
}
