package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/runs"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/coord"
)

// THREE PODS, ONE SESSION. A stop lands on the pod that took the request, a sweep on
// the pod that won the lease, and the run is on whichever pod started it. Each pod
// has a registry of its own; they share only the KV.

// podOf is a registry that answers for its own runs and the other pods'.
func podOf(c *coord.Coord) *registry {
	r := newRegistry()
	r.attach(c)
	return r
}

// heldRun is a session held on r and reports how its context was cancelled.
type heldRun struct {
	mu    sync.Mutex
	cause error
	done  chan struct{}
}

func holdOn(t *testing.T, r *registry, id string) *heldRun {
	t.Helper()
	x := &heldRun{done: make(chan struct{})}
	if !r.hold(id, func(cause error) {
		x.mu.Lock()
		x.cause = cause
		x.mu.Unlock()
		close(x.done)
	}) {
		t.Fatalf("%s was already held", id)
	}
	t.Cleanup(func() { r.release(id) })
	return x
}

func (x *heldRun) stopped(t *testing.T) error {
	t.Helper()
	select {
	case <-x.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run was never told")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.cause
}

// A stop or a pause asked on any pod reaches the pod running the session, as the
// same cause the run would be given at home — and one asked of a session nobody
// runs is a prompt no.
func TestAStopOrPauseReachesARunOnAnotherPod(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		a, b, c := podOf(pod("cloud-0")), podOf(pod("cloud-1")), podOf(pod("cloud-2"))
		defer a.attach(nil)
		defer b.attach(nil)
		defer c.attach(nil)
		ctx := context.Background()

		stopped := holdOn(t, b, "sess_stop")
		if !a.interrupt(ctx, "sess_stop", stoppedFor("by its owner")) {
			t.Fatal("a stop asked on pod a never found the run on pod b")
		}
		if cause := stopped.stopped(t); !errors.Is(cause, ErrStopped) {
			t.Fatalf("the run was cancelled with %v, want stopped", cause)
		}

		// From a pod that runs nothing of its own, to one that does.
		other := holdOn(t, b, "sess_c")
		if !c.interrupt(ctx, "sess_c", stoppedFor("logout")) {
			t.Fatal("a stop asked on pod c never found the run on pod b")
		}
		if cause := other.stopped(t); !errors.Is(cause, ErrStopped) {
			t.Fatalf("the run was cancelled with %v, want stopped", cause)
		}

		paused := holdOn(t, c, "sess_pause")
		if !a.interrupt(ctx, "sess_pause", ErrPaused) {
			t.Fatal("a pause asked on pod a never found the run on pod c")
		}
		if cause := paused.stopped(t); !errors.Is(cause, ErrPaused) || errors.Is(cause, ErrStopped) {
			t.Fatalf("the run was cancelled with %v, want paused", cause)
		}

		start := time.Now()
		if a.interrupt(ctx, "sess_nobody", ErrStopped) {
			t.Fatal("a session nobody runs was interrupted")
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("a stop of a session nobody runs took %v: it waited for a timer", took)
		}
	})
}

// A stop carries its reason across, so the session says why it ended wherever it ran.
func TestAStopCarriesItsReasonAcrossPods(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		a, b := podOf(pod("cloud-0")), podOf(pod("cloud-1"))
		defer a.attach(nil)
		defer b.attach(nil)
		x := holdOn(t, b, "sess_1")
		if !a.interrupt(context.Background(), "sess_1", stoppedFor("stopped by its owner")) {
			t.Fatal("not found")
		}
		got := x.stopped(t)
		if !errors.Is(got, ErrStopped) || !strings.HasSuffix(got.Error(), "stopped by its owner") {
			t.Fatalf("cancelled with %q, want stopped for its owner's reason", got)
		}
	})
}

// stoppedFor is the cause a stop is given: ErrStopped, and why.
func stoppedFor(reason string) error { return fmt.Errorf("%w: %s", ErrStopped, reason) }

// Every pod's runs are one set to whoever asks.
func TestHeldIsEveryPodsRuns(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		a, b, c := podOf(pod("cloud-0")), podOf(pod("cloud-1")), podOf(pod("cloud-2"))
		defer a.attach(nil)
		defer b.attach(nil)
		defer c.attach(nil)
		holdOn(t, a, "sess_a")
		holdOn(t, b, "sess_b")
		held, err := c.held(context.Background())
		if err != nil || len(held) != 2 || !held["sess_a"] || !held["sess_b"] {
			t.Fatalf("pod c sees %v (%v), want a's and b's runs", held, err)
		}
	})
}

// THE SWEEP. The pod that sweeps an org is not the pod that runs its sessions. A run
// executing on another pod is not an orphan; one no pod runs is; and when the other
// pods cannot all be asked the sweep leaves it, because a run it cannot see may be
// working.
func TestTheSweepLeavesARunOnAnotherPodAlone(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
			st, sto := meterStore(t)
			sweeper, runner := pod("cloud-0"), podOf(pod("cloud-1"))
			defer runner.attach(nil)
			running.attach(sweeper)
			defer running.attach(nil)

			codingSession(t, sto, "sess_far", time.Now().Add(-orphanGrace-time.Minute).Unix())
			holdOn(t, runner, "sess_far")

			reap(context.Background(), st, &record{}, testClocks)
			if x := reload(t, sto, "sess_far"); x.Status != StatusRunning {
				t.Fatalf("the sweep ended a run executing on another pod: %q", x.Status)
			}

			runner.release("sess_far")
			reap(context.Background(), st, &record{}, testClocks)
			if x := reload(t, sto, "sess_far"); x.Status != StatusError {
				t.Fatalf("a run no pod executes was left %q, want an orphan ended", x.Status)
			}
		})
	})
}

// A pod that does not answer is not a run that is not there.
func TestTheSweepLeavesWhatItCannotCheck(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
			st, sto := meterStore(t)
			sweeper, mute := pod("cloud-0"), pod("cloud-1")
			was := runs.Wait
			runs.Wait = 200 * time.Millisecond
			defer func() { runs.Wait = was }()
			running.attach(sweeper)
			defer running.attach(nil)
			release := make(chan struct{})
			defer close(release)
			defer mute.Serve(runs.Held, func(context.Context, string, []byte) ([]byte, bool) { <-release; return nil, false })()

			codingSession(t, sto, "sess_far", time.Now().Add(-orphanGrace-time.Minute).Unix())
			reap(context.Background(), st, &record{}, testClocks)
			if x := reload(t, sto, "sess_far"); x.Status != StatusRunning {
				t.Fatalf("the sweep ended a session on the word of a pod that never answered: %q", x.Status)
			}
		})
	})
}

// However many pods find a run lost, one of them carries it on.
func TestALostRunIsClaimedByExactlyOnePod(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		pods := []*registry{podOf(pod("cloud-0")), podOf(pod("cloud-1")), podOf(pod("cloud-2"))}
		for _, p := range pods {
			defer p.attach(nil)
		}
		x := Session{ID: "sess_lost", UpdatedAt: 1000}
		var mu sync.Mutex
		mine := 0
		var wg sync.WaitGroup
		for _, p := range pods {
			for range 3 {
				wg.Go(func() {
					ok, err := p.claim(context.Background(), x)
					if err != nil {
						t.Error(err)
					}
					if ok {
						mu.Lock()
						mine++
						mu.Unlock()
					}
				})
			}
		}
		wg.Wait()
		if mine != 1 {
			t.Fatalf("%d claims of one lost run said yes, want exactly one", mine)
		}
		// Lost again after it was carried on (it was heard from since): a new loss, a new claim.
		if ok, err := pods[1].claim(context.Background(), Session{ID: "sess_lost", UpdatedAt: 2000}); err != nil || !ok {
			t.Fatalf("a run lost a second time was not claimable: %v %v", ok, err)
		}
	})
}

// A rolling deploy. Pod A runs a coding run; pod B boots and sweeps, and leaves it
// alone however quiet it has been. When A is gone, its run is resumed — once, whoever
// sweeps next — in the sandbox it left, and not ended.
func TestARunIsResumedOnlyWhenItsPodIsGone(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
			st, sto := meterStore(t)
			id := "sess_deploy_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) // the KV is shared by the subtests
			a, b := podOf(pod("cloud-0")), pod("cloud-1")
			running.attach(b)
			defer running.attach(nil)
			var resumed []string
			var mu sync.Mutex
			was := ResumeCoding
			ResumeCoding = func(_ context.Context, _ *Store, x Session) bool {
				mu.Lock()
				resumed = append(resumed, x.ID)
				mu.Unlock()
				return true
			}
			defer func() { ResumeCoding = was }()

			quiet := time.Now().Add(-orphanGrace - time.Minute).Unix()
			if err := sto.CreateSession(context.Background(), Session{
				ID: id, Org: "acme", Agent: "hanzo", Actor: "acme/z", Status: StatusRunning, RootID: id,
				Kind: kindCoding, Branch: "agent/" + id, Environment: "sandbox", Box: "sbx_1",
				StartedAt: quiet, CreatedAt: quiet, UpdatedAt: quiet, MeteredAt: quiet,
			}); err != nil {
				t.Fatal(err)
			}
			holdOn(t, a, id)

			for range 2 {
				reap(context.Background(), st, &record{}, testClocks)
			}
			if x := reload(t, sto, id); x.Status != StatusRunning || len(resumed) != 0 {
				t.Fatalf("pod b, booting, touched a run pod a is executing: status %q, resumed %v", x.Status, resumed)
			}

			a.release(id)
			a.attach(nil) // pod a is gone
			for range 3 {
				reap(context.Background(), st, &record{}, testClocks)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(resumed) != 1 || resumed[0] != id {
				t.Fatalf("resumed %v, want the lost run once", resumed)
			}
			if x := reload(t, sto, id); x.Status != StatusRunning {
				t.Fatalf("a run that was resumed was ended: %q", x.Status)
			}
		})
	})
}
