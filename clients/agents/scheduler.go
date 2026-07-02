package agents

// The long-running-agent scheduler: it invokes each long-running agent's run on
// its cron cadence, through the SAME svc.runAgent path the HTTP handler uses —
// so a scheduled run is gated (fail-closed on the agent's OWN org balance),
// executed, recorded, and billed identically to an interactive one. There is no
// self-HTTP call: the endpoint's BEHAVIOR is the contract, and calling runAgent
// directly keeps ONE run path (no duplicated gate/meter, no re-crossing the
// identity boundary with a synthetic token).
//
// Cadence: one ticker fires every minute (cron's finest granularity). On each
// tick it loads the long-running work set and, for every agent whose schedule
// matches the current minute, launches a run — subject to two safety controls:
//
//   - Concurrency cap: at most maxConcurrentPerAgent in-flight runs per agent.
//     A slow model must never let cron stack unbounded goroutines for one agent.
//   - Exponential backoff: after a failed run (gate denial OR model error) an
//     agent is skipped for a growing number of ticks (1,2,4,… up to a cap), so a
//     persistently-failing or unfunded agent stops hammering commerce/the model.
//     A success resets the backoff.
//
// All state is in-memory and keyed by org/name: the scheduler is a per-process
// singleton owned by the mounted svc, torn down on Shutdown.

import (
	"context"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
)

const (
	// tickInterval is cron's resolution. Aligned to the top of each minute so a
	// "* * * * *" agent fires once per minute, not on process-start phase.
	tickInterval = time.Minute
	// maxConcurrentPerAgent caps in-flight runs for ONE agent. A cron agent is
	// expected to complete within its period; 1 means "never overlap a run with
	// itself" (the safe default for periodic work). >1 would allow catch-up.
	maxConcurrentPerAgent = 1
	// maxBackoffTicks caps the exponential skip so a failing agent still retries
	// roughly hourly rather than backing off forever.
	maxBackoffTicks = 60
	// runTimeout bounds a single scheduled run so one stuck completion cannot pin
	// a concurrency slot indefinitely.
	runTimeout = 10 * time.Minute
)

// agentState is the per-agent runtime bookkeeping the scheduler keeps between
// ticks: how many runs are in flight, and the backoff countdown after failures.
type agentState struct {
	inFlight   int
	failstreak int // consecutive failures; drives the backoff window.
	skipRemain int // ticks still to skip before the next attempt.
	parsed     schedule
	parsedExpr string // the expression `parsed` was compiled from (recompile on change).
}

type scheduler struct {
	svc  *svc
	log  luxlog.Logger
	stop func()

	mu     sync.Mutex
	states map[string]*agentState // key: org + "\x00" + name
	wg     sync.WaitGroup         // tracks in-flight run goroutines for clean shutdown.

	// now is time.Now, overridable in tests for deterministic cron evaluation.
	now func() time.Time
	// tick, when non-nil, replaces the internal ticker so tests drive cadence.
	tickC <-chan time.Time
}

func newScheduler(s *svc, log luxlog.Logger) *scheduler {
	return &scheduler{
		svc:    s,
		log:    log.New("component", "scheduler"),
		states: map[string]*agentState{},
		now:    time.Now,
	}
}

// start launches the scheduler loop in its own goroutine. Cancelled by stop().
func (sc *scheduler) start() {
	ctx, cancel := context.WithCancel(context.Background())
	sc.stop = func() {
		cancel()
		sc.wg.Wait() // let in-flight runs drain so the store isn't closed under them.
	}
	sc.wg.Add(1)
	go sc.loop(ctx)
}

// loop is the cadence driver. It uses the injected tick channel in tests, else a
// real minute ticker. Each tick evaluates the whole long-running work set.
func (sc *scheduler) loop(ctx context.Context) {
	defer sc.wg.Done()
	tickC := sc.tickC
	if tickC == nil {
		t := time.NewTicker(tickInterval)
		defer t.Stop()
		tickC = t.C
	}
	sc.log.Info("scheduler started", "interval", tickInterval)
	for {
		select {
		case <-ctx.Done():
			sc.log.Info("scheduler stopped")
			return
		case <-tickC:
			sc.tick(ctx, sc.now())
		}
	}
}

// tick evaluates every long-running agent against the wall-clock minute now and
// launches the ones that are due, are not backed off, and have a free
// concurrency slot. It is separated from loop() so tests can invoke it directly.
func (sc *scheduler) tick(ctx context.Context, now time.Time) {
	agents, err := sc.svc.store.ListLongRunning(ctx)
	if err != nil {
		sc.log.Warn("scheduler: list long-running failed", "err", err)
		return
	}
	live := make(map[string]bool, len(agents))
	for _, a := range agents {
		key := stateKey(a.Org, a.Name)
		live[key] = true
		if sc.due(a, key, now) {
			sc.launch(ctx, a, key)
		}
	}
	sc.pruneDeleted(live)
}

// due decides, under a SINGLE lock acquisition, whether agent a should run this
// tick: it (re)compiles the cron on change, decrements a live backoff window,
// checks the cron against now and the per-agent concurrency slot, and — when it
// returns true — has already reserved the slot (inFlight++). All shared state
// (parsed cron, backoff, inFlight) is touched only while holding sc.mu, so there
// is no data race with the completion goroutine in launch().
func (sc *scheduler) due(a Agent, key string, now time.Time) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	st := sc.stateForLocked(key)

	// Recompile the cron only when the expression changed (edits via PATCH).
	if st.parsedExpr != a.Schedule {
		p, err := parseCron(a.Schedule)
		if err != nil {
			// Stored schedule is invalid (create/update validate, but a
			// hand-edited DB could carry garbage). Skip, don't crash.
			sc.log.Warn("scheduler: bad stored schedule, skipping",
				"org", a.Org, "agent", a.Name, "schedule", a.Schedule, "err", err)
			return false
		}
		st.parsed, st.parsedExpr = p, a.Schedule
	}

	if st.skipRemain > 0 { // in a backoff window — consume one tick.
		st.skipRemain--
		return false
	}
	if !st.parsed.matches(now) || st.inFlight >= maxConcurrentPerAgent {
		return false
	}
	st.inFlight++ // reserve the slot before launching.
	return true
}

// launch runs one scheduled invocation in its own goroutine, updating the
// agent's backoff/concurrency state on completion. The run is empty-input (a
// scheduled agent acts on its own instructions) and attributed to its service
// account when bound, else the synthetic scheduler actor.
func (sc *scheduler) launch(ctx context.Context, a Agent, key string) {
	sc.wg.Add(1)
	go func() {
		defer sc.wg.Done()
		runCtx, cancel := context.WithTimeout(ctx, runTimeout)
		defer cancel()

		actor := a.ServiceAccountID
		if actor == "" {
			actor = schedulerActor
		}
		// Scheduled runs carry no HTTP request/IP; requestID/clientIP are empty.
		r, gateErr := sc.svc.runAgent(runCtx, a, "", actor, "", "")

		ok := gateErr == nil && r.Status == "ok"
		sc.mu.Lock()
		st := sc.stateForLocked(key)
		st.inFlight--
		if ok {
			st.failstreakReset()
		} else {
			st.failstreakBump()
		}
		remain := st.skipRemain
		streak := st.failstreak
		sc.mu.Unlock()

		switch {
		case gateErr != nil:
			sc.log.Warn("scheduled run gated (not executed)",
				"org", a.Org, "agent", a.Name, "err", gateErr, "failstreak", streak, "backoffTicks", remain)
		case r.Status != "ok":
			sc.log.Warn("scheduled run errored",
				"org", a.Org, "agent", a.Name, "err", r.Error, "failstreak", streak, "backoffTicks", remain)
		default:
			sc.log.Info("scheduled run ok", "org", a.Org, "agent", a.Name, "durationMs", r.DurationMs)
		}
	}()
}

// stateForLocked returns (creating if needed) the runtime state for an agent
// key. The CALLER MUST hold sc.mu — every read/write of agentState fields is
// serialized by that one lock, so the scheduler has no data race between a tick
// deciding to run and a completion goroutine updating backoff/inFlight.
func (sc *scheduler) stateForLocked(key string) *agentState {
	st := sc.states[key]
	if st == nil {
		st = &agentState{}
		sc.states[key] = st
	}
	return st
}

// pruneDeleted drops runtime state for agents that no longer appear in the work
// set (deleted or switched to one-shot), but keeps any with a run still in
// flight so its completion bookkeeping lands on live state.
func (sc *scheduler) pruneDeleted(live map[string]bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for key, st := range sc.states {
		if !live[key] && st.inFlight == 0 {
			delete(sc.states, key)
		}
	}
}

// failstreakReset clears the failure streak and backoff after a success.
func (st *agentState) failstreakReset() { st.failstreak, st.skipRemain = 0, 0 }

// failstreakBump grows the failure streak and sets the next backoff window to
// 2^(streak-1) ticks, capped — 1,2,4,8,… minutes between retries.
func (st *agentState) failstreakBump() {
	st.failstreak++
	skip := 1 << uint(min(st.failstreak-1, 30)) // guard the shift; 2^30 >> cap.
	if skip > maxBackoffTicks {
		skip = maxBackoffTicks
	}
	st.skipRemain = skip
}

// stateKey namespaces runtime state by org+name. The NUL separator can never
// appear in either (nameRE + org validation forbid it), so keys are injective.
func stateKey(org, name string) string { return org + "\x00" + name }
