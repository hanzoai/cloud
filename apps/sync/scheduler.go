package sync

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/namespace"
)

// scheduler.go is the FRESHNESS driver: a periodic reconcile loop that keeps every
// `poll`-triggered sync current WITHOUT waiting for a webhook. The engine (engine.go)
// already reconciles on demand — a GitHub/Hanzo Git webhook (cloud.Sync), a manual /run,
// or a chained propagation; but a sync whose upstream fires no webhook (or whose
// webhook is missed) would drift stale. The `poll` trigger is that sync's opt-in to a
// timer, and THIS loop is its driver — the third and final leg of the trigger enum
// (webhook→events, manual→/run, poll→scheduler), so the enum is complete.
//
// It is modelled on apps/social/scheduler.go (env-gated ticker + idempotent stop)
// and the operator's GITOPS_RECONCILE_ENABLED reconcile loop: one periodic sweep that
// folds over every registered intent and drives it toward agreement. A sweep is a full
// pass over every org's poll syncs, each reconciled through the SAME runOne core the
// webhook/manual paths use (Manual mode: no cursor short-circuit, so the upstream fetch
// itself is the change detector), bounded by the SAME global reconcileSem (cap 4) so
// the scheduler and the API never collectively exceed the git plane's concurrency
// ceiling.
//
// Leader-safe by construction: subsystems mount ONLY on the single writer (a Reader
// role is a store-less reverse proxy; a surge writer blocks on the writer lease before
// MountAll), so exactly one scheduler exists per writer — the same single-writer
// guarantee social relies on. Under horizontal sharding (CLOUD_PEERS) each writer's
// RWO PVC holds only the orgs routed to it, so the filesystem enumeration below sweeps
// exactly this writer's orgs — no cross-pod double-reconcile.

const (
	// reconcileIntervalEnv sets the sweep cadence (a Go duration). Empty / "0" / "off"
	// / "false" DISABLES the scheduler — the fail-safe default, so the binary ships
	// dormant and freshness is turned on by setting this in the sync App CR env (the
	// GITOPS_RECONCILE_ENABLED gating idiom, one knob that both enables and paces).
	reconcileIntervalEnv = "CLOUD_SYNC_RECONCILE_INTERVAL"

	// reconcileTimeout bounds ONE sync's reconcile inside a sweep (a slow / wedged
	// upstream fetch never bleeds past this). Matches the API's background reconcile
	// bound so the two paths behave identically. A poll fetch is normally seconds;
	// the initial import of a large repo is the outlier this covers.
	reconcileTimeout = 15 * time.Minute

	// settle delays the FIRST sweep after boot. Long enough that a starting pod is
	// not swept while it is still mounting stores, and that a rollout does not turn
	// into an immediate fetch storm against every upstream at once; short enough
	// that freshness does not wait on the interval. Clamped to the interval below,
	// so a short test cadence is not stretched to minutes.
	settle = 2 * time.Minute
)

// reconcileInterval resolves the sweep cadence from env. Empty / "0" / "off" / "false"
// disables; an unparseable value disables (fail-safe — never silently pick a surprising
// cadence, and never poll-storm a provider because of a typo).
func reconcileInterval(log luxlog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv(reconcileIntervalEnv))
	switch strings.ToLower(raw) {
	case "", "0", "off", "false":
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Warn("invalid "+reconcileIntervalEnv+" — sync reconcile scheduler disabled", "value", raw, "err", err)
		return 0
	}
	return d
}

// startScheduler launches the periodic poll-sync sweep and returns its stop function
// (never nil, idempotent). Disabled (interval 0) ⇒ a no-op stop, so the caller wires
// it unconditionally. A tick that lands while the previous sweep is still running is
// SKIPPED (a slow sweep never stacks), and stop cancels the loop AND waits for an
// in-flight sweep to drain, so Shutdown never closes a store out from under a reconcile.
// firstSweep is how long after boot the first sweep waits.
//
// Never a full interval when the interval is long — that is the whole point: a
// fleet redeploying faster than its cadence would otherwise never sweep. And never
// LONGER than the interval either, so a deliberately tight cadence is not stretched
// to minutes by a settle meant for an hourly one.
func firstSweep(interval time.Duration) time.Duration {
	if interval < settle {
		return interval
	}
	return settle
}

func startScheduler(s *cloud.Service[state]) func() {
	interval := reconcileInterval(s.Log)
	if interval == 0 {
		s.Log.Info("sync reconcile scheduler disabled", "env", reconcileIntervalEnv)
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var inflight sync.WaitGroup // tracks the currently-running sweep goroutine
	var running atomic.Bool     // overlap guard: at most one sweep in flight
	// THE FIRST SWEEP HAPPENS AT BOOT, not one interval later.
	//
	// A bare ticker's first tick is a full interval away, so a fleet that rolls out
	// more often than its interval NEVER SWEEPS AT ALL. Measured on this deployment:
	// eight rollouts inside five hours, with gaps of 13, 16, 17, 40, 43, 78 and 84
	// minutes against a 60m interval — five of the seven shorter than the interval.
	// The scheduler was enabled and logged nothing wrong, and 1685 declared syncs sat
	// unimported because the tick that would have imported them was reset every time.
	// Freshness that only survives a quiet fleet is not freshness.
	//
	// Delayed by settle rather than immediate: a pod that has just started is still
	// mounting stores, and every replica in a rollout would otherwise fetch at once.
	go func() {
		defer close(done)
		// SETTLE ONCE, THEN SWEEP AND KEEP SWEEPING — the shape apps/risk/baseline
		// already uses for a daily job, so there is one way to say "not one whole
		// interval from now" rather than two.
		//
		// The sweep is LAUNCHED rather than called here: it runs in its own goroutine
		// under the overlap guard so a long sweep never blocks the ticker, which is
		// the one way this differs from baseline's synchronous run.
		settleFirst := time.NewTimer(firstSweep(interval))
		defer settleFirst.Stop()
		select {
		case <-ctx.Done():
			return
		case <-settleFirst.C:
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			if !running.CompareAndSwap(false, true) {
				s.Log.Warn("sync reconcile: previous sweep still running — skipping tick")
			} else {
				inflight.Add(1)
				go func() {
					defer inflight.Done()
					defer running.Store(false)
					sweep(s, ctx)
				}()
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	s.Log.Info("sync reconcile scheduler started", "interval", interval, "firstSweepIn", firstSweep(interval))

	var once bool
	return func() {
		if once {
			return
		}
		once = true
		cancel()        // stop the ticker AND signal an in-flight sweep to drain
		<-done          // the ticker goroutine has returned
		inflight.Wait() // the in-flight sweep (and its workers) have finished
	}
}

// sweep reconciles every poll sync across every org whose store lives on this writer.
// It folds over OrgStore.Each (the filesystem is the source of truth for which orgs
// have a sync store, and each handle is the SAME one the request path uses — no second
// open), reads every sync via ListAll (the file is one org's, so the org rides on each
// row), and reconciles each poll candidate in Manual mode (the upstream fetch is the
// change detector), bounded by reconcileSem so at most 4 run at once. One sync's
// failure is logged and never aborts the sweep; ctx cancellation (Shutdown) stops
// enqueuing and the pass waits for in-flight reconciles before returning.
func sweep(s *cloud.Service[state], ctx context.Context) {
	var wg sync.WaitGroup
	var candidates, reconciled int64
	err := s.State.stores.Each(func(ns namespace.Namespace, st *store, openErr error) {
		if ctx.Err() != nil {
			return
		}
		if openErr != nil {
			s.Log.Warn("sync reconcile: open store", "namespace", ns, "err", openErr)
			return
		}
		syncs, err := st.ListAll(ctx)
		if err != nil {
			s.Log.Warn("sync reconcile: list", "namespace", ns, "err", err)
			return
		}
		for _, sy := range syncs {
			if !pollable(sy) {
				continue
			}
			candidates++
			select {
			case reconcileSem <- struct{}{}:
			case <-ctx.Done():
				return // stop enqueuing; wg.Wait below drains what is already in flight
			}
			wg.Add(1)
			go func(sy Sync) {
				defer wg.Done()
				defer func() { <-reconcileSem }()
				defer func() { _ = recover() }()
				// st is this org's cached handle (correct path); sy.Org is the real
				// principal value off the row — used for the native repo owner + token,
				// while the cursor write lands through st. Both name the same file.
				rctx, rcancel := context.WithTimeout(ctx, reconcileTimeout)
				defer rcancel()
				if runOne(rctx, st, sy, Event{Provider: sy.Source.Provider, Org: sy.Org, Manual: true}) {
					atomic.AddInt64(&reconciled, 1)
				}
			}(sy)
		}
	})
	wg.Wait()
	if err != nil {
		s.Log.Warn("sync reconcile: enumerate orgs", "err", err)
	}
	if candidates > 0 {
		s.Log.Info("sync reconcile sweep", "candidates", candidates, "reconciled", reconciled)
	}
}

// pollable reports whether a sync is the scheduler's to drive: a git sync that is not
// paused (off) and has opted into the timer via the `poll` trigger. A webhook sync is
// driven by its events; a manual sync only by an explicit /run — neither is swept.
func pollable(sy Sync) bool {
	return sy.Kind == "git" && sy.Direction != dirOff && sy.Trigger == trigPoll
}
