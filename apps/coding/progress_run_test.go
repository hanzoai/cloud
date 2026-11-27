package coding

// progress_run_test.go pins what a run says about its own progress: that it has
// started, and what step it is on. The session carries it, so a caller polling the
// run reads running and the step, never a phase nobody estimated.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// phases is the progress a run reported, in order, as "phase: activity".
func phases(t *testing.T, sess *fakeSessions) []string {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	var out []string
	for _, e := range sess.events {
		if e.kind != kindProgress {
			continue
		}
		var p struct{ Phase, Activity string }
		if err := json.Unmarshal([]byte(e.payload), &p); err != nil {
			t.Fatalf("a progress report %q is not JSON: %v", e.payload, err)
		}
		out = append(out, p.Phase+": "+p.Activity)
	}
	return out
}

// A RUN REPORTS RUNNING FROM THE MOMENT IT STARTS, and the step it is on as each
// begins. A step with no sentence says nothing, and a log line is not a step.
func TestRun_ReportsItsOwnProgress(t *testing.T) {
	sess := &fakeSessions{id: "sess_abc123def456"}
	run := &fakeRunner{
		steps: []Step{
			{Type: "step", Step: "lease", Message: "leasing a dev sandbox", Status: "running"},
			{Type: "log", Message: "editing handler.go"},
			{Type: "step", Step: "push"},
		},
		result: RunResult{Branch: "agent/abc123def456", CommitSha: "deadbeef", Changed: true, OK: true},
	}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	d.Run(context.Background(), baseReq())
	got := phases(t, sess)
	want := []string{"running: starting", "running: leasing a dev sandbox"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("the run reported %q, want %q", got, want)
	}
}

// WHAT A STEP SAYS IS BOUNDED to what a session will take: a longer line would be
// refused whole, and the run would report nothing at all for that step.
func TestRun_ReportsALongStepInOneLine(t *testing.T) {
	sess := &fakeSessions{id: "sess_abc123def456"}
	long := "cloning\n" + strings.Repeat("é", 200)
	run := &fakeRunner{
		steps:  []Step{{Type: "step", Step: "clone", Message: long}},
		result: RunResult{Branch: "agent/abc123def456", CommitSha: "deadbeef", Changed: true, OK: true},
	}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	d.Run(context.Background(), baseReq())
	got := phases(t, sess)
	if len(got) != 2 {
		t.Fatalf("the run reported %q", got)
	}
	activity := strings.TrimPrefix(got[1], "running: ")
	if len(activity) > activityMost || strings.Contains(activity, "\n") || !strings.HasPrefix(activity, "cloning é") {
		t.Fatalf("the long step reported %d bytes %q", len(activity), activity)
	}
}
