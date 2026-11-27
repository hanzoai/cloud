package coding

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// pkillable is a prompt whose words a model's `pkill -f` would match.
const pkillable = "Run `go build ./...` and fix what fails (marker-7f3a)"

// THE TASK IS IN NO COMMAND A RUN SENDS. The harness reads it on stdin after its
// two credentials, and the commit made of its words reads its message on stdin
// too, so no argv the sandbox was handed — and so no /proc/<pid>/cmdline in the
// pod — carries a word of it.
//
// MUTATION: hand argvFor the task as its last argument again, or commit with
// `-m`, and the marker is found in an argv.
func TestARunPutsItsTaskInNoCommandLine(t *testing.T) {
	tip := theSHA
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{Stdout: workedTurn}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	req := aRun()
	req.Prompt = pkillable
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	harness, committed := -1, -1
	for i, l := range p.lines() {
		if strings.Contains(l, "marker-7f3a") {
			t.Fatalf("command %d carries the task: %s", i, l)
		}
		switch {
		case strings.Contains(l, "--skip-git-repo-check") && harness < 0:
			harness = i
		case slices.Contains(p.ran[i], "commit") && slices.Contains(p.ran[i], "-F"):
			committed = i
		}
	}
	if harness < 0 || !strings.HasSuffix(p.fed[harness], pkillable) {
		t.Fatalf("the harness was not told its task on stdin: %q", p.fed[max(harness, 0)])
	}
	if committed < 0 || !strings.Contains(p.fed[committed], "go build") {
		t.Fatalf("the commit did not read its message on stdin:\n%s", strings.Join(p.lines(), "\n"))
	}
}

// A HARNESS KILLED MID-TURN IS NAMED AS ONE, and what its turn left running is
// stopped. Exit 137 is SIGKILL: the run fails saying so, Terminated carries the
// signal the session's status reports, and the turn's own group record — the
// path its wrapper was handed — is what end was asked to stop. It is not retried.
//
// MUTATION: drop killedBy from turn's condition and no end runs; drop the case
// from the failure switch and the run says "exited 137".
func TestAKilledHarnessIsNamedAndItsTurnStopped(t *testing.T) {
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{ExitCode: 137}
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
	if res.OK || res.Terminated != "SIGKILL" || !strings.Contains(res.Error, "killed by SIGKILL (exit 137)") {
		t.Fatalf("ok %v, terminated %q, error %q: want a run failed naming SIGKILL", res.OK, res.Terminated, res.Error)
	}
	group := groupArg(t, p)
	stopped, turns := false, 0
	for _, argv := range p.ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			turns++
		}
		if len(argv) == 5 && argv[2] == ended && argv[4] == group {
			stopped = true
		}
	}
	if !stopped || turns != 1 {
		t.Fatalf("stopped %v after %d turns, want the turn's group %s stopped and no retry:\n%s",
			stopped, turns, group, strings.Join(p.lines(), "\n"))
	}
}

// A TURN THAT RAN OUT OF TIME IS STOPPED BEFORE ITS WORK IS KEPT. Closing the
// exec stops nothing in the pod, so the harness would go on writing while keep
// committed the tree; end stops the turn's group first.
//
// MUTATION: drop `err != nil` from turn's condition and the commit runs while
// nothing stopped the turn.
func TestATurnOutOfTimeIsStoppedBeforeItsWorkIsKept(t *testing.T) {
	p := &pod{
		answer: func(argv []string) client.Ran {
			if slices.Contains(argv, "rev-parse") {
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		},
		refuse: func(argv []string) error {
			if slices.Contains(argv, "--skip-git-repo-check") {
				return zip.Errorf(http.StatusBadGateway, "exec: %v", context.DeadlineExceeded)
			}
			return nil
		},
	}
	servePod(t, p)
	if _, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	group := groupArg(t, p)
	stop, commit := -1, -1
	for i, argv := range p.ran {
		if len(argv) == 5 && argv[2] == ended && argv[4] == group && stop < 0 {
			stop = i
		}
		if slices.Contains(argv, "commit") && commit < 0 {
			commit = i
		}
	}
	if stop < 0 || commit < 0 || stop > commit {
		t.Fatalf("end at %d, commit at %d: the turn must be stopped before its work is kept:\n%s",
			stop, commit, strings.Join(p.lines(), "\n"))
	}
}

// groupArg is the group record the harness's wrapper was handed: the argument
// after solo's script.
func groupArg(t *testing.T, p *pod) string {
	t.Helper()
	for _, argv := range p.ran {
		if !slices.Contains(argv, "--skip-git-repo-check") {
			continue
		}
		for i, a := range argv {
			if strings.HasPrefix(a, "g=$1; shift") && i+2 < len(argv) {
				if g := argv[i+2]; strings.HasPrefix(g, "/tmp/hanzo-run/turn.") {
					return g
				}
			}
		}
	}
	t.Fatal("no harness turn was handed a group record")
	return ""
}

// THE SESSION TELLS A KILLED RUN FROM A FAILED ONE. Its status is error, as a
// failure's is — every surface already ends on it — and it carries the signal,
// as a stop carries timeUp; a run that failed on its own carries none.
//
// MUTATION: drop the terminated field from fail and the killed run's status
// reads like any failure.
func TestAKilledRunsStatusCarriesItsSignal(t *testing.T) {
	for _, c := range []struct {
		name, signal string
	}{{"killed", "SIGKILL"}, {"failed", ""}} {
		t.Run(c.name, func(t *testing.T) {
			sess := &fakeSessions{id: "sess_abc123def456"}
			d, _ := dispatcherFor(sess, &fakePR{}, &fakeRunner{result: RunResult{
				Error: "the dev harness exited 1", Terminated: c.signal}}, true)
			if res := d.Run(context.Background(), baseReq()); res.OK || res.Terminated != c.signal {
				t.Fatalf("result %+v, want a failure terminated by %q", res, c.signal)
			}
			var status map[string]any
			for _, e := range sess.events {
				if e.kind == kindStatus {
					_ = json.Unmarshal([]byte(e.payload), &status)
				}
			}
			if status["status"] != "error" {
				t.Fatalf("the last status is %v, want error", status)
			}
			if got, _ := status["terminated"].(string); got != c.signal {
				t.Fatalf("the status says terminated %q, want %q: %v", got, c.signal, status)
			}
			if len(sess.closes) != 1 || sess.closes[0].status != statusError {
				t.Fatalf("the session closed %+v, want error", sess.closes)
			}
		})
	}
}

// A KILLED TURN IS NOT A REFUSAL FOR MONEY, whatever its stderr said: it is
// named as killed and not asked again on the free model.
//
// MUTATION: drop killedBy from the 402 retry's condition and the turn runs twice.
func TestAKilledTurnIsNotRetriedForMoney(t *testing.T) {
	turns := 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			turns++
			return client.Ran{ExitCode: 143, Stderr: "error: 402 Payment Required\n"}
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
	if turns != 1 || res.Terminated != "SIGTERM" {
		t.Fatalf("%d turns, terminated %q: want one turn named as killed by SIGTERM", turns, res.Terminated)
	}
}

// A RESUME KILLED BEFORE IT NAMED ITS CONVERSATION IS A KILLED TURN, not a lost
// one: it printed no thread.started because it died, not because it answered in
// another conversation, and the run says what killed it.
//
// MUTATION: drop killedBy from the loop's lost check and the run reads "did not
// resume the conversation it was told to".
func TestAResumeKilledBeforeItsThreadIsKilledNotLost(t *testing.T) {
	turns := 0
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			turns++
			if turns == 1 {
				return client.Ran{ExitCode: 1, Stdout: looped(loopThread)}
			}
			return client.Ran{ExitCode: 143}
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
	if turns != 2 || res.Terminated != "SIGTERM" || !strings.Contains(res.Error, "killed by SIGTERM") {
		t.Fatalf("%d turns, terminated %q, error %q: want the resume named as killed", turns, res.Terminated, res.Error)
	}
}
