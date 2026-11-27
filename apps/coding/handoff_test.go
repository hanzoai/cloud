package coding

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/coord"
)

// A pod being replaced parks the runs it drives: each is stopped with its work kept, parked
// to go on at once, and the next pod goes on with it in the same session, from its branch.
//
// MUTATION: end a drained run stopped (the old behaviour) and there is no park row, the
// session is closed, and nothing goes on; skip keeping the work and the branch is empty.
func TestADrainParksARunInFlightAndTheNextPodGoesOnWithIt(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		r := newRig()
		r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
		r.refs = map[string]string{}
		tip := theSHA
		var turns atomic.Int32
		working, release := make(chan struct{}), make(chan struct{})
		r.pod.answer = func(argv []string) client.Ran {
			switch {
			case slices.Contains(argv, "--skip-git-repo-check"):
				if turns.Add(1) == 1 { // the first turn is working when the pod is replaced
					close(working)
					<-release
					return client.Ran{ExitCode: 130}
				}
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"t2"}` + "\n" +
					`{"type":"item.completed","item":{"type":"agent_message","text":"Fixed the header and pushed."}}` + "\n"}
			case slices.Contains(argv, "commit"):
				tip = newSHA
			case slices.Contains(argv, "push"):
				ref := argv[len(argv)-1]
				r.mu.Lock()
				r.refs[strings.TrimPrefix(ref, "HEAD:refs/heads/")] = newSHA
				r.mu.Unlock()
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: tip + "\n"}
			case slices.Contains(argv, "diff"):
				return client.Ran{Stdout: " 1 file changed\n"}
			}
			return client.Ran{}
		}
		r.serve(t)
		app := newSessionsApp(t)
		authorities(t, resetsAt(0), nil)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		stop := flights.watch(c)
		t.Cleanup(stop)

		app.run("hanzo", "sub-zoe", "sess_abc123def456")
		engineOnce = sync.Once{}
		t.Cleanup(func() { engineOnce = sync.Once{} })
		acc, err := Start(asCaller("sub-zoe"), "hanzo", "sub-zoe", theTask(), func(string, ...any) {})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		<-working

		landed := make(chan int, 1)
		go func() { landed <- Handoff(context.Background()) }()
		flights.Lock()
		f := flights.m[acc.SessionID]
		flights.Unlock()
		waitFor(t, "the drain to reach the run", func() bool { return context.Cause(f.ctx) != nil })
		close(release)
		if n := <-landed; n != 1 {
			t.Fatalf("the drain landed %d runs, want 1", n)
		}

		// Parked for the restart, its work on its branch, its box given back, not closed.
		r.mu.Lock()
		held := r.refs[acc.Branch]
		r.mu.Unlock()
		if held != newSHA {
			t.Fatalf("the drained run's work is not on %s:\n%s", acc.Branch, strings.Join(r.pod.lines(), "\n"))
		}
		x := app.get("hanzo", acc.SessionID)
		if !x.Parked() || x.Park != agents.ParkRestart {
			t.Fatalf("the session reads %+v, want parked for the restart", x)
		}
		if boxesGivenBack(r.pod) != 1 {
			t.Fatalf("a parked run ended %d sandboxes, want its box given back", boxesGivenBack(r.pod))
		}
		r.sessions.mu.Lock()
		closes := len(r.sessions.closes)
		r.sessions.mu.Unlock()
		if closes != 0 || slices.Contains(statusesOf(r), "error") || slices.Contains(statusesOf(r), "stopped") {
			t.Fatalf("a drained run was ended: closes=%d statuses=%v", closes, statusesOf(r))
		}

		// The next pod goes on with it, in the same session, and finishes it.
		waitFor(t, "the run to be unparked", func() bool { return app.unparked() == 1 })
		waitFor(t, "the run to finish", func() bool {
			if slices.Contains(statusesOf(r), "error") {
				t.Fatalf("the run that went on failed: %v", eventsOf(r, kindStatus))
			}
			return slices.Contains(statusesOf(r), "done")
		})
		letGo(t, flights, acc.SessionID)
		if got := statusesOf(r); !slices.Contains(got, statusContinued) {
			t.Fatalf("the stream never said the run continued: %v", got)
		}
	})
}

// Every run in flight is drained, one that is paused is left, and the drain stops waiting when
// its context ends.
//
// MUTATION: cancel only the first flight and the others never see errDrain.
func TestADrainReachesEveryRunInFlight(t *testing.T) {
	var fs []*flight
	var ctxs []context.Context
	for _, id := range []string{"sess_d1", "sess_d2", "sess_d3"} {
		f, ctx := fakeRun(t, id, "u-acme", client.Reply{})
		fs, ctxs = append(fs, f), append(ctxs, ctx)
	}
	paused, pctx := fakeRun(t, "sess_dp", "u-acme", client.Reply{})
	paused.interrupt(agents.ErrPaused)
	paused.land(Result{}, context.Cause(pctx))

	landed := make(chan int, 1)
	go func() { landed <- Handoff(context.Background()) }()
	for i, ctx := range ctxs {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d never saw the drain", i)
		}
		if !strings.Contains(context.Cause(ctx).Error(), "restarting") {
			t.Fatalf("run %d was stopped for %v, want the restart", i, context.Cause(ctx))
		}
	}
	if pctx.Err() == nil || !strings.Contains(context.Cause(pctx).Error(), "paused") {
		t.Fatalf("the paused run was touched: %v", context.Cause(pctx))
	}
	for _, f := range fs {
		f.land(Result{}, context.Cause(f.ctx))
	}
	select {
	case n := <-landed:
		if n != 3 {
			t.Fatalf("the drain landed %d runs, want 3", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never finished")
	}

	// A drain bounded by its context stops waiting for a run that does not land.
	stuck, _ := fakeRun(t, "sess_ds", "u-acme", client.Reply{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if n := Handoff(ctx); n != 0 {
		t.Fatalf("a run that never landed was counted: %d", n)
	}
	_ = stuck
}
