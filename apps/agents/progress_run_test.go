package agents

// progress_run_test.go pins what a run that lives in this process says about
// itself: it reports through the session log, and that report is the run's own
// word on the session — queued while it waits for a place, then running.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// A RUN'S OWN REPORT, LOGGED IN PROCESS, MOVES THE SESSION'S PROGRESS exactly as
// the route that appends a progress turn does: the run says it is queued, and a
// read says queued; it says it is running, and a read says running with what it
// is doing. Neither is an estimate, and neither reads unknown.
func TestARunLoggingItsProgressMovesTheSession(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		ctx := context.Background()
		id, err := OpenSession(ctx, "acme", "acme/u1", "hanzo", "code: site")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		sto := storeOf(t, &mounted.State, "acme")
		read := func() sessionProgress {
			t.Helper()
			x, err := sto.GetSession(ctx, "acme", id)
			if err != nil {
				t.Fatal(err)
			}
			return progressOf(x)
		}
		if got := read(); got.Phase != phaseUnknown {
			t.Fatalf("a session nothing has reported reads %q, want unknown", got.Phase)
		}
		for _, c := range []struct{ report, phase, activity string }{
			{`{"phase":"queued","activity":"waiting for a free sandbox"}`, phaseQueued, "waiting for a free sandbox"},
			{`{"phase":"running","activity":"cloning the repository"}`, phaseRunning, "cloning the repository"},
		} {
			if err := LogSessionEvent(ctx, "acme", id, KindProgress, "acme/u1", []byte(c.report)); err != nil {
				t.Fatalf("log %s: %v", c.report, err)
			}
			got := read()
			if got.Phase != c.phase || got.Activity != c.activity || got.Estimated || got.Pct != nil {
				t.Fatalf("after %s the session reads %+v, want %s %q, not estimated, no pct", c.report, got, c.phase, c.activity)
			}
		}

	})
}

// A REPORT THE SESSION CANNOT READ IS REFUSED WHOLE, as the route refuses it: no
// turn is stored for a report that moved nothing.
func TestAMalformedLoggedReportStoresNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		ctx := context.Background()
		id, err := OpenSession(ctx, "acme", "acme/u1", "hanzo", "code: site")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		for _, bad := range []string{``, `{"phase":"asleep"}`, `{"phase":"running","pct":101}`} {
			if err := LogSessionEvent(ctx, "acme", id, KindProgress, "acme/u1", []byte(bad)); err == nil {
				t.Errorf("the report %q was accepted", bad)
			}
		}
		if n, _ := storeOf(t, &mounted.State, "acme").CountEvents(ctx, "acme", id); n != 0 {
			t.Fatalf("%d turns stored for reports that moved nothing", n)
		}

	})
}

// QUEUED IS THE RUN'S WORD ALONE: a model reading a transcript cannot know a run
// is waiting for a place to start, so an estimate that says it is not one.
func TestAModelCannotEstimateAQueue(t *testing.T) {
	if _, ok := parseEstimate(`{"pct":10,"phase":"queued","activity":"waiting"}`); ok {
		t.Fatal("an estimate named queued, which only the run can know")
	}
	if p, ok := parseEstimate(`{"pct":10,"phase":"running","activity":"working"}`); !ok || p.Phase != phaseRunning {
		t.Fatalf("running read as %+v, %v", p, ok)
	}
}
