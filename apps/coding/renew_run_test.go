package coding

// renew_run_test.go — a run's credential, renewed, in the runner that holds it: the box a
// renewal is put in, the turn it reaches, and what becomes of a run whose person is removed or
// whose credential cannot be renewed. The pod, IAM and the sessions are the fakes the rest of the
// package runs against; the clock is the test's.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
)

// longRun is a pod whose agent turn goes on until the test lets it end, as a run that works for
// hours does, and whose branch moves when it is committed.
type longRun struct {
	p       *pod
	started chan struct{}
	release chan struct{}
	once    sync.Once
	freed   sync.Once
	tip     string
	// turn is what the agent's turn ends with once it is released.
	turn client.Ran
}

func newLongRun(t *testing.T) *longRun {
	t.Helper()
	l := &longRun{started: make(chan struct{}), release: make(chan struct{}), tip: theSHA,
		turn: client.Ran{Stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":"Fixed it."}}` + "\n"}}
	l.p = &pod{}
	l.p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			return l.turn
		case slices.Contains(argv, "commit"):
			l.tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: l.tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed, 3 insertions(+)\n"}
		}
		return client.Ran{}
	}
	l.p.during = func(ctx context.Context, argv []string) error {
		if slices.Contains(argv, "--skip-git-repo-check") {
			l.once.Do(func() { close(l.started) })
			select {
			case <-l.release:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		return nil
	}
	servePod(t, l.p)
	t.Cleanup(l.free)
	return l
}

// free lets the agent's turn end.
func (l *longRun) free() { l.freed.Do(func() { close(l.release) }) }

// fly starts the run in a goroutine and returns when its agent's turn is under way.
func (l *longRun) fly(t *testing.T, ctx context.Context, run RunRequest) (done <-chan struct{}, res *RunResult, err *error) {
	t.Helper()
	fin := make(chan struct{})
	res, err = new(RunResult), new(error)
	go func() {
		defer close(fin)
		*res, *err = sandboxRunner{}.Run(ctx, "acme", "alice", run, nil)
	}()
	select {
	case <-l.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent's turn never began")
	}
	return fin, res, err
}

func awaited(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("never ended: %s", what)
	}
}

// outlast moves the clock to each retry in turn until the run is over.
func outlast(t *testing.T, clock *fakeClock, done <-chan struct{}) {
	t.Helper()
	for range 60 {
		var next time.Duration
		eventually(t, "a retry is set or the run is ended", func() bool {
			select {
			case <-done:
				return true
			default:
			}
			if p := clock.pending(); len(p) == 1 {
				next = p[0]
				return true
			}
			return false
		})
		select {
		case <-done:
			return
		default:
		}
		clock.tick(next)
	}
	t.Fatal("the run never ended")
}

// agentExecs is every command that ran the dev harness, with what it was fed.
func agentExecs(p *pod) (argvs [][]string, fed []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range p.ran {
		if slices.Contains(a, "--skip-git-repo-check") {
			argvs, fed = append(argvs, a), append(fed, p.fed[i])
		}
	}
	return
}

// planted is every command that put a renewed credential in the box, as the token it was fed.
func plantedIn(p *pod) (fed, blind []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range p.ran {
		if len(a) > 2 && a[0] == "sh" && strings.HasPrefix(a[2], "IFS= read -r k; ") {
			fed, blind = append(fed, p.fed[i]), append(blind, p.blind[i]...)
		}
	}
	return
}

// A dev turn is started with the credential on stdin, in the environment it always was, and also in the
// file dev's credential command is pointed at; never on a command line. A harness that reads its
// credential once is started as it was: the environment alone.
//
// MUTATION: start every turn as before and the harness never reads a renewal.
func TestADevTurnIsStartedWithTheCredentialFileAndAClaudeTurnIsNot(t *testing.T) {
	for tool, dev := range map[string]bool{"": true, "dev": true, "claude": false} {
		t.Run("tool "+tool, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 3600}
			iam.serve(t)
			newSessionsApp(t)
			holdClock(t)
			p := &pod{answer: func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			run := aRun()
			run.Tool = tool
			if _, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			argvs, fed := func() ([][]string, []string) {
				p.mu.Lock()
				defer p.mu.Unlock()
				var a [][]string
				var f []string
				for i, argv := range p.ran {
					if slices.Contains(argv, "claude") || slices.Contains(argv, "--skip-git-repo-check") {
						a, f = append(a, argv), append(f, p.fed[i])
					}
				}
				return a, f
			}()
			if len(argvs) != 1 {
				t.Fatalf("the harness ran %d times, want once", len(argvs))
			}
			cred, _, _ := strings.Cut(fed[0], "\n")
			joined := strings.Join(argvs[0], " ")
			if cred != jwtWith("ai:inference", "run-1") {
				t.Fatalf("the harness was fed %q", cred)
			}
			if strings.Contains(joined, cred) {
				t.Fatalf("the credential is on the command line: %s", joined)
			}
			if got := strings.Contains(joined, modelPath(run.SessionID)) && strings.Contains(joined, "model_providers.hanzo.auth.command"); got != dev {
				t.Fatalf("tool %q: points dev at the credential file = %v, want %v:\n%s", tool, got, dev, joined)
			}
			if !strings.Contains(joined, "export HANZO_API_KEY") {
				t.Fatalf("the credential is no longer in the harness's environment: %s", joined)
			}
		})
	}
}

// A renewal reaches a turn that is running: put in the box by an exec of its own, on stdin, blinded,
// narrating nothing, while the turn goes on; and when the run ends both tokens are revoked, the
// trail says how many renewals there were, and no timer is left.
//
// MUTATION: renew without planting and the harness's refresh reads the token that ran out.
func TestARenewalReachesATurnThatIsRunning(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 3600}
	iam.serve(t)
	app := newSessionsApp(t)
	clock := holdClock(t)
	l := newLongRun(t)
	run := aRun()
	app.run("acme", "alice", run.SessionID)

	done, res, err := l.fly(t, context.Background(), run)
	clock.tick(credLife - credMargin)
	eventually(t, "the renewal is in the box", func() bool { fed, _ := plantedIn(l.p); return len(fed) == 1 })

	second := jwtWith("ai:inference", "run-2")
	fed, blind := plantedIn(l.p)
	if fed[0] != second+"\n" || !slices.Contains(blind, second) {
		t.Fatalf("the box was fed %q and told to blind %v, want %q on stdin and blinded", fed, blind, second)
	}
	l.p.mu.Lock()
	for i, a := range l.p.ran {
		if strings.Contains(strings.Join(a, " "), second) {
			t.Errorf("command %d carries the renewed token on its command line", i)
		}
	}
	l.p.mu.Unlock()
	if turns, _ := agentExecs(l.p); len(turns) != 1 {
		t.Fatalf("the harness ran %d times: a renewal must not restart the turn", len(turns))
	}

	l.free()
	awaited(t, "the run", done)
	if *err != nil || !res.OK {
		t.Fatalf("run: %+v / %v", res, *err)
	}
	if got := iam.revokedTokens(); len(got) != 2 {
		t.Fatalf("revoked %v, want both tokens the run held", got)
	}
	if due := clock.pending(); len(due) != 0 {
		t.Fatalf("timers %v remain after the run", due)
	}
	clock.tick(10 * time.Hour)
	if n := len(iam.asks()); n != 2 {
		t.Fatalf("IAM was asked %d times, want the mint and the one renewal", n)
	}
	if got := actions(iam.trail); !slices.Equal(got, []string{"coding.run.mint:success", "coding.run.end:success"}) {
		t.Fatalf("the trail holds %v", got)
	}
}

// A person removed from the org mid-run: the run is ended "access removed" through its session, the work
// it did is pushed to its branch, the tokens are revoked, and the sandbox that was theirs is ended.
//
// MUTATION: leave the sandbox kept for a removed person and it stays theirs; stop without the keep and the
// work is lost.
func TestAPersonRemovedMidRunKeepsTheirWorkAndLosesTheirSandbox(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 3600}
	iam.serve(t)
	app := newSessionsApp(t)
	clock := holdClock(t)
	l := newLongRun(t)
	run := aRun()
	app.run("acme", "alice", run.SessionID)

	done, res, err := l.fly(t, context.Background(), run)
	app.revoke("acme", "alice")
	clock.tick(credLife - credMargin)
	awaited(t, "the run", done)

	if *err != nil {
		t.Fatalf("a run ended for its person's access keeps its work rather than failing: %v", *err)
	}
	if ended := app.ended(); len(ended) != 1 || ended[0] != run.SessionID+": "+accessRemoved {
		t.Fatalf("the session was ended %v, want %q", ended, accessRemoved)
	}
	if !res.Changed || res.CommitSha != newSHA || !l.p.gitVerb("push") {
		t.Fatalf("the work was not kept on the branch: %+v", res)
	}
	if got := boxesGivenBack(l.p); got != 1 {
		t.Fatalf("the sandbox was ended %d times, want the removed person's ended once their work was kept", got)
	}
	if got := iam.revokedTokens(); len(got) != 1 {
		t.Fatalf("revoked %v, want the person's token", got)
	}
	if n := len(iam.asks()); n != 1 {
		t.Fatalf("IAM was asked to delegate %d times, want only the mint", n)
	}
	if due := clock.pending(); len(due) != 0 {
		t.Fatalf("timers %v remain after the run", due)
	}
}

// A credential IAM could not renew before it ran out: the run is ended with that said, its work is kept on
// its branch, and its sandbox is kept for its person, who has lost nothing and carries on with `after`.
//
// MUTATION: end the sandbox in this case too and a person IAM merely could not reach loses their box.
func TestACredentialIAMCannotRenewEndsTheRunWithItsWorkKept(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 3600}
	iam.serve(t)
	iam.setAnswer(func(n int, _ url.Values) int {
		if n == 1 {
			return 0
		}
		return http.StatusServiceUnavailable
	})
	app := newSessionsApp(t)
	clock := holdClock(t)
	l := newLongRun(t)
	run := aRun()
	app.run("acme", "alice", run.SessionID)

	done, res, err := l.fly(t, context.Background(), run)
	clock.tick(credLife - credMargin)
	outlast(t, clock, done)
	awaited(t, "the run", done)

	if *err != nil {
		t.Fatalf("a run whose credential ran out keeps its work rather than failing: %v", *err)
	}
	if ended := app.ended(); len(ended) != 1 || ended[0] != run.SessionID+": "+unrenewed {
		t.Fatalf("the session was ended %v, want %q", ended, unrenewed)
	}
	if !res.Changed || !l.p.gitVerb("push") {
		t.Fatalf("the work was not kept on the branch: %+v", res)
	}
	if got := boxesGivenBack(l.p); got != 0 {
		t.Fatalf("the sandbox was ended %d times, want it kept for a person who lost nothing", got)
	}
	if got := actions(iam.trail); !slices.Contains(got, "coding.run.lapse:error") {
		t.Fatalf("the trail holds %v, want the lapse", got)
	}
}

// A harness that reads its credential once cannot take another, so the run is held to the life of the token
// it was started with — IAM's four hours, not the day the run was admitted for — and ends a little short of it
// with its work kept, as a run out of time does. dev, which does take another, is held to nothing but the day.
//
// MUTATION: leave out the hold and a claude run goes on, a model call at a time failing, after its token ends.
func TestAHarnessThatReadsItsCredentialOnceIsHeldToItsLife(t *testing.T) {
	for tool, want := range map[string]struct{ low, high int }{
		"claude": {4*3600 - 600, 4*3600 - 240},
		"":       {20 * 3600, 24 * 3600},
	} {
		t.Run("tool "+tool, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 4 * 3600}
			iam.serve(t)
			newSessionsApp(t)
			clock := holdClock(t)
			p := &pod{answer: func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			ctx, cancel := context.WithTimeout(t.Context(), 24*time.Hour)
			defer cancel()
			run := aRun()
			run.Tool = tool
			run.RunTimeoutSeconds = 24 * 3600
			if _, err := (sandboxRunner{}).Run(ctx, "acme", "alice", run, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			p.mu.Lock()
			most := -1
			for i, argv := range p.ran {
				if slices.Contains(argv, "claude") || slices.Contains(argv, "--skip-git-repo-check") {
					most = p.timeouts[i]
				}
			}
			p.mu.Unlock()
			if most < want.low || most > want.high {
				t.Fatalf("the harness was given %ds, want between %d and %d", most, want.low, want.high)
			}
			if due := clock.pending(); len(due) != 0 {
				t.Fatalf("timers %v remain after the run", due)
			}
		})
	}
}

// A model call refused for a limit or for money parks the run exactly as before, after a renewal as before one:
// its work kept, its sandbox given back, its credential ended (both tokens revoked, no timer), and nothing about
// a longer run adds a cap or a price.
//
// MUTATION: end the credential's timer only when the run finishes and a parked run keeps renewing as a person.
func TestARunRefusedAfterARenewalParksAsBefore(t *testing.T) {
	for name, c := range map[string]struct {
		message string
		reason  string
	}{
		"payment": {"unexpected status 402 Payment Required: add funds or upgrade: https://hanzo.ai/pay", agents.ParkPayment},
		"limit":   {"exceeded retry limit, last status: 429 Too Many Requests, request id: r1", agents.ParkLimit},
	} {
		t.Run(name, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+string(rune('0'+n))) }, life: 3600}
			iam.serve(t)
			app := newSessionsApp(t)
			clock := holdClock(t)
			l := newLongRun(t)
			turn, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": c.message}})
			l.turn = client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n" + string(turn) + "\n"}
			run := aRun()
			app.run("acme", "alice", run.SessionID)

			done, res, err := l.fly(t, context.Background(), run)
			clock.tick(credLife - credMargin)
			eventually(t, "the renewal is in the box", func() bool { fed, _ := plantedIn(l.p); return len(fed) == 1 })
			l.free()
			awaited(t, "the run", done)

			if *err != nil {
				t.Fatalf("a refused model call is the run's reported outcome, not a transport error: %v", *err)
			}
			if res.Park == nil || res.Park.Reason != c.reason || res.OK {
				t.Fatalf("the run reads %+v / %+v, want it parked for %s", res, res.Park, c.reason)
			}
			if got := boxesGivenBack(l.p); got != 1 {
				t.Fatalf("a parked run ended %d sandboxes, want its box given back", got)
			}
			if got := iam.revokedTokens(); len(got) != 2 {
				t.Fatalf("revoked %v, want both tokens ended with the run", got)
			}
			if due := clock.pending(); len(due) != 0 {
				t.Fatalf("a parked run holds timers %v: it would keep renewing as its person", due)
			}
		})
	}
}

// The dev in the box is asked its version once, before the first turn, and only a dev that can be pointed at
// the credential file is: renewsFrom is the first release measured to take the override. An older dev, or one
// whose answer cannot be read, is started as claude is — the environment alone, a token for what IAM grants,
// the run held a little short of it — and the person is told why in the run's own steps. Measured against the
// published release binaries: 0.6.94 / 0.6.108 answer `missing field name` to the override, 0.6.115 and on take it.
//
// MUTATION: skip the probe and every dev in an old image dies before its first model call; read a version
// loosely and 0.6.108 is given the override.
func TestOnlyADevThatCanTakeTheOverrideIsGivenIt(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer client.Ran
		renews bool
		said   string
	}{
		{"0.6.94, the image of sandbox 1.3.4", client.Ran{Stdout: "dev 0.6.94\n"}, false, "older than 0.6.115"},
		{"0.6.108, the last that fails", client.Ran{Stdout: "dev 0.6.108\n"}, false, "older than 0.6.115"},
		{"0.6.115, the first that takes it", client.Ran{Stdout: "dev 0.6.115\n"}, true, ""},
		{"0.7.0", client.Ran{Stdout: "dev 0.7.0\n"}, true, ""},
		{"0.7.10, sandbox 1.3.12", client.Ran{Stdout: "dev 0.7.10\n"}, true, ""},
		{"0.7.14", client.Ran{Stdout: "dev 0.7.14\n"}, true, ""},
		{"1.0.0", client.Ran{Stdout: "dev 1.0.0\n"}, true, ""},
		{"a name that is not a version", client.Ran{Stdout: "dev nightly\n"}, false, "could not be read"},
		{"no answer", client.Ran{}, true, ""}, // the pod's default: a current dev
		{"the exec failed", client.Ran{ExitCode: 127, Stderr: "sh: dev: not found"}, false, "could not be read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }, life: 4 * 3600}
			iam.serve(t)
			newSessionsApp(t)
			holdClock(t)
			var probes int
			p := &pod{answer: func(argv []string) client.Ran {
				switch {
				case slices.Equal(argv, []string{"dev", "--version"}):
					probes++
					return c.answer
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			ctx, cancel := context.WithTimeout(t.Context(), 24*time.Hour)
			defer cancel()
			run := aRun()
			run.RunTimeoutSeconds = 24 * 3600
			var said []string
			if _, err := (sandboxRunner{}).Run(ctx, "acme", "alice", run, steps(&said)); err != nil {
				t.Fatalf("run: %v", err)
			}
			argvs, _ := agentExecs(p)
			if len(argvs) != 1 {
				t.Fatalf("the harness ran %d times", len(argvs))
			}
			joined := strings.Join(argvs[0], " ")
			if got := strings.Contains(joined, "model_providers.hanzo.auth"); got != c.renews {
				t.Fatalf("given the override = %v, want %v:\n%s", got, c.renews, joined)
			}
			asked, _ := strconv.Atoi(iam.asks()[0].Get("lifetime"))
			if c.renews != (asked <= int(credLife.Seconds())) {
				t.Fatalf("the credential was asked for %ds: renews = %v", asked, c.renews)
			}
			note := ""
			for _, l := range said {
				if strings.HasPrefix(l, "credential ") {
					note = l
				}
			}
			if (note != "") == c.renews || (c.said != "" && !strings.Contains(note, c.said)) {
				t.Fatalf("the steps say %q (%v), want a note containing %q only when the credential cannot renew", note, said, c.said)
			}
			if probes != 1 {
				t.Fatalf("dev was asked its version %d times, want once", probes)
			}
		})
	}
}

func TestAVersionIsReadAsDevPrintsIt(t *testing.T) {
	for in, want := range map[string]bool{
		"dev 0.6.94": false, "dev 0.6.114": false, "dev 0.6.115": true, "dev 0.6.122": true, "dev 0.7.0": true,
		"dev 0.7.10": true, "dev 0.10.1": true, "dev 1.0.0": true, "dev 0.5.999": false, "0.7.10-rc1": true,
		"": false, "dev": false, "dev 0.7": false, "garbage 1.2": false,
	} {
		if got := atLeast(in, renewsFrom); got != want {
			t.Errorf("atLeast(%q) = %v, want %v", in, got, want)
		}
	}
}

// A harness that cannot renew its credential — claude and codex read it once, a dev too old to be pointed at a
// file — is ended a little short of the token it holds, with its work kept and THAT said: not "its time ran
// out". The run's result, the session's status event, the thread's last word and the chat turn's account each
// name the credential, and a follow-up (`after`) carries on.
//
// MUTATION: end it with errTimeUp and the run is reported as out of time though it had hours left.
func TestAHarnessThatCannotRenewEndsSayingSo(t *testing.T) {
	for _, c := range []struct{ name, tool, dev string }{
		{"claude", "claude", ""}, {"codex", "codex", ""}, {"an old dev", "", "dev 0.6.94\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }, life: 4 * 3600}
			iam.serve(t)
			newSessionsApp(t)
			holdClock(t)
			l := newLongRun(t)
			if c.dev != "" {
				inner := l.p.answer
				l.p.answer = func(argv []string) client.Ran {
					if slices.Equal(argv, []string{"dev", "--version"}) {
						return client.Ran{Stdout: c.dev}
					}
					return inner(argv)
				}
			}
			// The token IAM clipped to four hours ends before a day's run does; its margin is made longer
			// than the token's life so that the hour comes at once instead of in four.
			was := leaseMargin
			leaseMargin = 5 * time.Hour
			t.Cleanup(func() { leaseMargin = was })
			ctx, cancel := context.WithTimeout(t.Context(), 24*time.Hour)
			defer cancel()
			run := aRun()
			run.Tool = c.tool
			run.RunTimeoutSeconds = 24 * 3600
			res, err := sandboxRunner{}.Run(ctx, "acme", "alice", run, nil)
			if err != nil {
				t.Fatalf("a run held to its credential keeps its work rather than failing: %v", err)
			}
			if !res.Unrenewable || res.TimeUp {
				t.Fatalf("the run reads Unrenewable = %v, TimeUp = %v: %+v", res.Unrenewable, res.TimeUp, res)
			}
			if !res.Changed || !l.p.gitVerb("push") {
				t.Fatalf("the work was not kept on the branch: %+v", res)
			}

			// What the dispatcher makes of it: a stop that says the credential, not the time.
			sess, pr := &fakeSessions{id: "sess_abc123def456"}, &fakePR{}
			d, _ := dispatcherFor(sess, pr, &fakeRunner{result: RunResult{Changed: true, CommitSha: "cafe", Unrenewable: true}}, true)
			var lines []string
			d.Watch = func(_ context.Context, kind string, payload []byte) {
				if l, _ := renderLine(kind, payload); l != "" {
					lines = append(lines, l)
				}
			}
			out := d.Run(context.Background(), baseReq())
			if out.Error != statusStopped || len(pr.inputs) != 0 {
				t.Fatalf("the dispatcher ended it %+v, %d PRs, want a stop that opens none", out, len(pr.inputs))
			}
			last := lines[len(lines)-1]
			for _, want := range []string{"cannot renew", "model credential", "`agent/abc123def456`", "`after`"} {
				if !strings.Contains(last, want) {
					t.Fatalf("the thread's last word is %q, want it to say %q", last, want)
				}
			}
			if strings.Contains(last, "used its time") {
				t.Fatalf("the thread says the time ran out: %q", last)
			}
			var status map[string]any
			for _, e := range sess.events {
				if e.kind == kindStatus {
					_ = json.Unmarshal([]byte(e.payload), &status)
				}
			}
			if status["unrenewable"] != true || status["timeUp"] != false {
				t.Fatalf("the status event reads %v, want unrenewable and not timeUp", status)
			}
		})
	}
}

// A harness that makes no model call is not held to a credential it never spends: a python or node program
// is given the day it asked for. (Adopted from red's check of the same name; see red731_python_test.go.)
//
// A token that covers the run it was asked for is no limit either: a claude run whose token IAM granted whole
// is not ended a margin early.
//
// MUTATION: hold a token IAM did not clip and every short claude run is ended before it starts.
func TestATokenThatCoversTheRunIsNoLimitOnIt(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }, life: 600}
	iam.serve(t)
	newSessionsApp(t)
	holdClock(t)
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	was := leaseMargin
	leaseMargin = 5 * time.Hour
	t.Cleanup(func() { leaseMargin = was })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	run := aRun()
	run.Tool = "claude"
	run.RunTimeoutSeconds = 600
	res, err := sandboxRunner{}.Run(ctx, "acme", "alice", run, nil)
	if err != nil || res.Unrenewable || res.TimeUp {
		t.Fatalf("a ten-minute claude run on a ten-minute token: %+v / %v", res, err)
	}
	p.mu.Lock()
	ran := 0
	for _, a := range p.ran {
		if slices.Contains(a, "claude") {
			ran++
		}
	}
	p.mu.Unlock()
	if ran == 0 {
		t.Fatal("the harness never ran")
	}
}

// A flight's first turn writes the credential file whatever is there, and a later turn of the flight only
// if it is gone: the first is the one that must win over a dead flight's file, the later one holds the
// token of the moment it began and must not put it back over a renewal.
//
// MUTATION: start every turn with the first turn's wrapper and a turn after a renewal puts the old token back.
func TestOnlyAFlightsFirstTurnWritesTheFileWhateverIsThere(t *testing.T) {
	iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }, life: 3600}
	iam.serve(t)
	newSessionsApp(t)
	holdClock(t)
	var mu sync.Mutex
	turns := 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			mu.Lock()
			turns++
			n := turns
			mu.Unlock()
			if n == 1 {
				return client.Ran{ExitCode: 1, Stderr: "unexpected status 402 Payment Required: balance"}
			}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	if _, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	argvs, _ := agentExecs(p)
	if len(argvs) < 2 {
		t.Fatalf("the harness ran %d times, want a second turn", len(argvs))
	}
	script := func(argv []string) string { return argv[2] }
	if strings.Contains(script(argvs[0]), `[ ! -s "$f" ]`) {
		t.Fatalf("the first turn writes the file only if it is gone:\n%s", script(argvs[0]))
	}
	if !strings.Contains(script(argvs[1]), `[ ! -s "$f" ]`) {
		t.Fatalf("a later turn writes the file whatever is there:\n%s", script(argvs[1]))
	}
}

// The run's credential file is taken out of a sandbox that is kept for its person, after the harness has
// finished with it; out of one that was ended there is nothing to take it from, and no exec is made.
//
// MUTATION: erase unconditionally and a parked run's ended sandbox is asked to remove a file.
func TestTheCredentialFileIsRemovedFromAKeptSandboxOnly(t *testing.T) {
	for name, parks := range map[string]bool{"a kept sandbox": false, "a parked run's ended one": true} {
		t.Run(name, func(t *testing.T) {
			iam := &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "run-"+strconv.Itoa(n)) }, life: 3600}
			iam.serve(t)
			newSessionsApp(t)
			holdClock(t)
			p := &pod{answer: func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "--skip-git-repo-check") && parks:
					turn, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": "exceeded retry limit, last status: 429 Too Many Requests, request id: r1"}})
					return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n" + string(turn) + "\n"}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			run := aRun()
			if _, err := (sandboxRunner{}).Run(t.Context(), "acme", "alice", run, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			want := []string{"rm", "-f", "--", modelPath(run.SessionID)}
			p.mu.Lock()
			defer p.mu.Unlock()
			at, turn := -1, -1
			for i, a := range p.ran {
				if slices.Equal(a, want) {
					at = i
				}
				if slices.Contains(a, "--skip-git-repo-check") {
					turn = i
				}
			}
			switch {
			case parks && at >= 0:
				t.Fatalf("a sandbox that was ended was asked to remove the file")
			case !parks && (at < 0 || at < turn):
				t.Fatalf("the file was removed at command %d, the harness's last turn at %d: want it removed after, from a kept sandbox", at, turn)
			}
		})
	}
}
