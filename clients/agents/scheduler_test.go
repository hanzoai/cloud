package agents

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
)

// countingAI records how many completions ran and can be told to fail, so
// scheduler tests can assert on run count + drive backoff deterministically.
type countingAI struct {
	mu    sync.Mutex
	calls int32
	fail  bool
	err   error
	block chan struct{} // when non-nil, ChatCompletion blocks until closed.
}

func (c *countingAI) ChatCompletion(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
	atomic.AddInt32(&c.calls, 1)
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	fail, err := c.fail, c.err
	c.mu.Unlock()
	if fail {
		if err == nil {
			err = errTest
		}
		return nil, err
	}
	return &types.ChatResponse{Content: "done"}, nil
}

func (c *countingAI) count() int32 { return atomic.LoadInt32(&c.calls) }

// schedSvc builds an svc + scheduler with NO billing (gate allows) and the given
// AI, seeded with the supplied agents. Returns the scheduler for direct tick().
func schedSvc(t *testing.T, ai types.AIClient, seed ...Agent) *scheduler {
	t.Helper()
	s := &svc{store: testStore(t), ai: ai, log: luxlog.New("test")}
	for _, a := range seed {
		if err := s.store.Create(context.Background(), a); err != nil {
			t.Fatalf("seed %s/%s: %v", a.Org, a.Name, err)
		}
	}
	sc := newScheduler(s, luxlog.New("test"))
	return sc
}

func longRunning(org, name, cron string) Agent {
	a := mk(org, name)
	a.ExecutionMode, a.Schedule = ModeLongRunning, cron
	return a
}

// waitFor polls a condition briefly (async run goroutines).
func waitFor(cond func() bool) bool {
	for i := 0; i < 200; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestSchedulerFiresDueAgent: a tick at a minute the cron matches launches one
// run; a tick at a non-matching minute launches none.
func TestSchedulerFiresDueAgent(t *testing.T) {
	ai := &countingAI{}
	sc := schedSvc(t, ai, longRunning("acme", "cron", "*/5 * * * *"))
	ctx := context.Background()

	sc.tick(ctx, at(t, "2026-07-01 12:36")) // 36 not multiple of 5 -> no fire
	time.Sleep(20 * time.Millisecond)
	if ai.count() != 0 {
		t.Fatalf("non-matching minute must not fire, got %d", ai.count())
	}

	sc.tick(ctx, at(t, "2026-07-01 12:35")) // matches */5
	if !waitFor(func() bool { return ai.count() == 1 }) {
		t.Fatalf("matching minute must fire once, got %d", ai.count())
	}
}

// TestSchedulerRecordsRun: a scheduled run is persisted to the run history, just
// like an HTTP run — the scheduler shares runAgent.
func TestSchedulerRecordsRun(t *testing.T) {
	ai := &countingAI{}
	sc := schedSvc(t, ai, longRunning("acme", "cron", "* * * * *"))
	ctx := context.Background()
	sc.tick(ctx, at(t, "2026-07-01 12:00"))
	if !waitFor(func() bool {
		runs, _ := sc.svc.store.ListRuns(ctx, "acme", "cron", 10)
		return len(runs) == 1 && runs[0].Status == "ok"
	}) {
		runs, _ := sc.svc.store.ListRuns(ctx, "acme", "cron", 10)
		t.Fatalf("scheduled run not recorded: %+v", runs)
	}
}

// TestSchedulerBackoffOnFailure: after a failed run the agent is skipped for a
// growing number of ticks, so a broken agent stops hammering. The first failing
// tick fires; the immediately-following matching tick is skipped (backoff=1).
func TestSchedulerBackoffOnFailure(t *testing.T) {
	ai := &countingAI{fail: true}
	sc := schedSvc(t, ai, longRunning("acme", "cron", "* * * * *"))
	ctx := context.Background()

	sc.tick(ctx, at(t, "2026-07-01 12:00"))
	if !waitFor(func() bool { return ai.count() == 1 }) {
		t.Fatalf("first tick should attempt the run, got %d", ai.count())
	}
	// Wait for the failure to register the backoff window.
	if !waitFor(func() bool {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		st := sc.states[stateKey("acme", "cron")]
		return st != nil && st.failstreak == 1 && st.skipRemain == 1
	}) {
		t.Fatal("failure should set failstreak=1, skipRemain=1")
	}
	// Next matching tick is consumed by backoff -> no new run.
	sc.tick(ctx, at(t, "2026-07-01 12:01"))
	time.Sleep(20 * time.Millisecond)
	if ai.count() != 1 {
		t.Fatalf("backoff tick must not fire, got %d", ai.count())
	}
	// The tick after that (skip exhausted) fires again.
	sc.tick(ctx, at(t, "2026-07-01 12:02"))
	if !waitFor(func() bool { return ai.count() == 2 }) {
		t.Fatalf("post-backoff tick should fire, got %d", ai.count())
	}
}

// TestSchedulerConcurrencyCap: a slow run holds the single per-agent slot, so a
// second matching tick while it is in flight does NOT start a second run.
func TestSchedulerConcurrencyCap(t *testing.T) {
	ai := &countingAI{block: make(chan struct{})}
	sc := schedSvc(t, ai, longRunning("acme", "cron", "* * * * *"))
	ctx := context.Background()

	sc.tick(ctx, at(t, "2026-07-01 12:00")) // starts run #1, which blocks
	if !waitFor(func() bool { return ai.count() == 1 }) {
		t.Fatalf("first run should start, got %d", ai.count())
	}
	sc.tick(ctx, at(t, "2026-07-01 12:01")) // slot busy -> no second run
	time.Sleep(20 * time.Millisecond)
	if ai.count() != 1 {
		t.Fatalf("concurrency cap breached: %d runs in flight", ai.count())
	}
	close(ai.block) // let run #1 finish
	if !waitFor(func() bool {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		st := sc.states[stateKey("acme", "cron")]
		return st != nil && st.inFlight == 0
	}) {
		t.Fatal("in-flight count should drain to 0 after completion")
	}
}

// TestSchedulerOnlyLongRunning: a one-shot agent is never fired by the
// scheduler even if its (dropped) schedule would have matched.
func TestSchedulerOnlyLongRunning(t *testing.T) {
	ai := &countingAI{}
	one := mk("acme", "one") // one-shot default, no schedule
	sc := schedSvc(t, ai, one)
	sc.tick(context.Background(), at(t, "2026-07-01 12:00"))
	time.Sleep(20 * time.Millisecond)
	if ai.count() != 0 {
		t.Fatalf("one-shot agent must never be scheduled, got %d", ai.count())
	}
}
