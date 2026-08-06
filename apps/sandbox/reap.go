package sandbox

// The reaper: what makes a lease end.
//
// Everything needed for this already existed and nothing called it. `ExpiresAt`
// was written on every create, `LastUsedAt` was stamped on every call,
// `Store.Expired` was implemented, and `stop` was implemented — and a sandbox,
// once created, ran forever. Measured, not inferred: a proof pod from a test run
// was still Running 64 minutes later and had to be deleted by hand.
//
// That is one bug wearing two faces. A sandbox that never sleeps is a sandbox
// that bills forever, so the reaper is simultaneously the idle-sleep feature and
// the honest meter. Neither is a separate component.
//
// TWO CLOCKS, because they answer different questions:
//
//	ExpiresAt   the LEASE. Set at create from ttlSec. When it passes the sandbox
//	            is over — the caller said how long they wanted it and that is how
//	            long they got.
//	LastUsedAt  ATTENTION. Stamped by every fs and exec call. A sandbox nobody
//	            has touched for `idle` is asleep even if its lease has hours left,
//	            because holding a pod for a session someone walked away from is
//	            the whole cost this exists to stop.
//
// ONE ACTION, TWO TRIGGERS. Both end the pod and both keep the volume, because
// `purge` is a separate opt-in and ending a lease must never destroy what the
// tenant made. So the checkout and the caches survive, and the next call for
// that project gets a fresh pod against the same disk — which is "resume is
// cheap" without a suspended state to define or a resume route to serve.
//
// A `status: suspended` was the obvious other design and it is worse: `Status`
// is pending|running|error, nothing resumes, and a row in a fourth state that
// no route can leave is a dead row that looks like a live sandbox.

import (
	"context"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/namespace"
)

// reapEvery is how often we look. A minute is far finer than the smallest lease
// (900s for exec) and coarse enough that the sweep is invisible next to the
// apiserver traffic one active sandbox makes.
const reapEvery = time.Minute

// idleAfter is how long a sandbox may go untouched before it sleeps. An hour,
// because that is also the longest single command the runtime allows: a sandbox
// quiet for longer than its own maximum command is not working, it is abandoned.
const idleAfter = time.Hour

// reap runs until ctx is done. It is started once by Mount and never returns a
// value — a sweep that fails is logged and retried next minute, because the
// alternative is a reaper that dies quietly and a fleet that looks fine while
// leaking pods, which is the state this replaces.
func reap(ctx context.Context, s *cloud.Service[state]) {
	t := time.NewTicker(reapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep(ctx, s)
		}
	}
}

// sweep is one pass over every org's sandboxes.
//
// Errors are per-org and never abort the pass: one org with an unopenable store
// must not stop every other org's leases from ending. `Each` hands us the error
// rather than throwing, which is what makes that the easy shape to write.
func sweep(ctx context.Context, s *cloud.Service[state]) {
	now := time.Now()
	_ = s.State.stores.Each(func(ns namespace.Namespace, st *Store, openErr error) {
		if ctx.Err() != nil {
			return
		}
		if openErr != nil {
			s.Log.Warn("reap: open store", "namespace", ns, "err", openErr)
			return
		}
		expired, err := st.Expired(ctx, ns.ID(), now.Unix())
		if err != nil {
			s.Log.Warn("reap: list expired", "namespace", ns, "err", err)
			return
		}
		for _, m := range expired {
			end(ctx, s, st, m, "expired")
		}
		// Idle is not a query the store answers, because "untouched for an hour"
		// is a policy and the store holds facts. Listing the running ones and
		// checking the stamp keeps the policy in one place — here — where the
		// constant that defines it also lives.
		running, err := st.List(ctx, ns.ID(), "", "running")
		if err != nil {
			s.Log.Warn("reap: list running", "namespace", ns, "err", err)
			return
		}
		for _, m := range running {
			if m.LastUsedAt > 0 && now.Sub(time.Unix(m.LastUsedAt, 0)) > idleAfter {
				end(ctx, s, st, m, "idle")
			}
		}
	})
}

// end retires a sandbox whose lease is over: the pod goes, the row goes, the
// volume stays.
func end(ctx context.Context, s *cloud.Service[state], st *Store, m Sandbox, why string) {
	if serr := s.State.rt.stop(ctx, m); serr != nil {
		s.Log.Warn("reap: stop", "id", m.ID, "err", serr)
	}
	if derr := st.Delete(ctx, m.Org, m.ID); derr != nil {
		s.Log.Warn("reap: delete", "id", m.ID, "err", derr)
		return
	}
	s.Log.Info("reaped sandbox", "id", m.ID, "org", m.Org, "why", why)
}
