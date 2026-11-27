package coding

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A run whose budget ran out keeps its work and ends the way a stop does, with
// the budget as the reason and a follow-up as the way on. Without that cause a
// deadline was a failure, the work was never committed, and a task longer than one
// run had nowhere to continue from.
//
// MUTATION: build the run's context with WithTimeout and the cause is
// DeadlineExceeded, which is not kept.
func TestARunThatRunsOutOfTimeKeepsItsWorkAndSaysSo(t *testing.T) {
	ctx, cancel, _ := runContext("acme", 1, ceiling())
	defer cancel()
	<-ctx.Done()
	cause := context.Cause(ctx)
	if !errors.Is(cause, errTimeUp) || !kept(cause) {
		t.Fatalf("a run past its budget ended with %v, want a kept stop that says time ran out", cause)
	}

	sess, pr := &fakeSessions{id: "sess_abc123def456"}, &fakePR{}
	run, stop := context.WithCancelCause(context.Background())
	d, _ := dispatcherFor(sess, pr, nil, true)
	d.Runner = interrupting{stop, errTimeUp}
	var lines []string
	d.Watch = func(_ context.Context, kind string, payload []byte) {
		if l, _ := renderLine(kind, payload); l != "" {
			lines = append(lines, l)
		}
	}
	res := d.Run(run, baseReq())
	if res.Error != statusStopped || !res.Changed || len(pr.inputs) != 0 {
		t.Fatalf("a run out of time: %+v, %d PRs", res, len(pr.inputs))
	}
	last := lines[len(lines)-1]
	for _, want := range []string{"used its time", "`agent/abc123def456`", "`after`"} {
		if !strings.Contains(last, want) {
			t.Fatalf("the thread's last word is %q, want it to say %q", last, want)
		}
	}
}

// A deadline that fires while the runner is delivering is not "time up, work
// kept": the push it was making is what it aborted, and nothing was pushed. It is
// a failed run, said as one.
//
// MUTATION: drop `runRes.TimeUp ||` from the dispatcher's test and it reports stopped.
func TestADeadlineDuringDeliveryIsAFailureNotAKeptStop(t *testing.T) {
	sess, pr := &fakeSessions{id: "sess_abc123def456"}, &fakePR{}
	run, stop := context.WithCancelCause(context.Background())
	d, _ := dispatcherFor(sess, pr, nil, true)
	d.Runner = failing{stop}
	res := d.Run(run, baseReq())
	if res.OK || res.Error == statusStopped || len(pr.inputs) != 0 {
		t.Fatalf("a run whose push the deadline aborted ended %+v, want a failure", res)
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("the session closed %+v, want error", sess.closes)
	}
}

// A run that names no timeout may take as long as any run may, and the run's
// context and the command inside it are given the same number.
func TestARunThatNamesNoTimeoutMayTakeAsLongAsAnyRunMay(t *testing.T) {
	if got, want := timeoutOr(0), int(ceiling().Seconds()); got != want {
		t.Fatalf("a run that named no timeout was given %ds, want %ds", got, want)
	}
	if budget(0) != ceiling() {
		t.Fatalf("the default budget is %s, want %s", budget(0), ceiling())
	}
	for _, asked := range []int{-5, 60, 3600, 10 * 86400} {
		if got, want := timeoutOr(asked), int(budget(asked).Seconds()); got != want {
			t.Fatalf("asked %d: the command gets %ds and the run %ds", asked, got, want)
		}
	}
}
