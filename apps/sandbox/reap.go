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

// extendWhenUnder is how close to expiry a BUSY sandbox has to be before its
// lease is pushed out. Ten minutes: comfortably more than one reap sweep, so a
// working run is never a tick away from losing its pod, and short enough that a
// lease still means something.
const extendWhenUnder = int64(10 * 60)

// extendBy is how much runway a busy sandbox is granted each time. One hour —
// the same span as idleAfter, so the two rules read against the same clock: an
// hour of silence ends a sandbox, an hour of runway is what work buys.
const extendBy = int64(60 * 60)

// maxLifetime is the absolute ceiling from CREATION, whatever the activity. A
// day. Without it, extension is not a lease renewal but a lease abolition — and
// a sandbox that has been working for 24 hours is a job that wants a Deployment,
// not a session that wants another hour.
const maxLifetime = int64(24 * 60 * 60)

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
			orphans(ctx, s)
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
				continue
			}
			// A SANDBOX THAT IS WORKING KEEPS ITS COMPUTER.
			//
			// LastUsedAt already tells us the difference between a run that is
			// progressing and one that is abandoned — it is stamped by every exec
			// and every fs call, and the branch above already trusts it to kill.
			// Trusting it in ONE direction only was the bug: activity could
			// shorten a lease and never lengthen it, so a deep-research run or a
			// long build that was demonstrably alive still died at its TTL. That
			// is the one case where reaping destroys the most work, and it fired
			// on exactly the runs worth keeping.
			//
			// So a busy sandbox gets its lease pushed to now+extendBy, bounded by
			// maxLifetime from CREATION. It stays a lease: the ceiling is absolute
			// and measured from the start, so no amount of activity turns a
			// sandbox into a permanent resident. The idle rule above still
			// outranks this — untouched for an hour ends it whatever the lease
			// says — and Extend only ever moves a lease FORWARD, so this can never
			// cut one short.
			if m.ExpiresAt > 0 && m.ExpiresAt-now.Unix() < extendWhenUnder {
				want := now.Unix() + extendBy
				if ceiling := m.CreatedAt + maxLifetime; want > ceiling {
					want = ceiling
				}
				if want > m.ExpiresAt {
					if err := st.Extend(ctx, ns.ID(), m.ID, want); err != nil {
						s.Log.Warn("reap: extend busy lease", "id", m.ID, "err", err)
					}
				}
			}
		}
	})
}

// end retires a sandbox whose lease is over: the pod goes, the row goes, the
// volume stays.
func end(ctx context.Context, s *cloud.Service[state], st *Store, m Sandbox, why string) {
	// THE CREDENTIAL DIES FIRST, and before anything that can fail.
	//
	// Expiry alone would get there eventually, and eventually is the gap: a
	// sandbox ended EARLY — a finished run, a user who stopped it, the idle
	// sweep — would otherwise leave a working inference grant for the rest of its
	// window, belonging to a pod that no longer exists. Revoking here makes "the
	// sandbox is over" and "its credential is worthless" one event rather than two
	// that usually coincide.
	//
	// First, because stop() and Delete() can both fail and this must not be
	// skipped when they do: a sandbox we could not tear down is precisely the one
	// whose credential should already be dead.
	s.State.grants.revoke(m.ID)
	if serr := s.State.rt.stop(ctx, m); serr != nil {
		s.Log.Warn("reap: stop", "id", m.ID, "err", serr)
	}
	if derr := st.Delete(ctx, m.Org, m.ID); derr != nil {
		s.Log.Warn("reap: delete", "id", m.ID, "err", derr)
		return
	}
	s.Log.Info("reaped sandbox", "id", m.ID, "org", m.Org, "why", why)
}

// orphans removes sandbox pods no row claims any more.
//
// It is the sweep that has to exist, because the two above only reach a pod a ROW
// names: a stop that failed, a process that died between deleting the row and
// deleting the pod, a row lost to a restored backup — each leaves a pod running
// submitted code that nothing will ever ask about again. Without this, the only
// remedy is somebody at a terminal, and the remedy somebody at a terminal reaches
// for is `kubectl delete pods` with a selector they wrote in a hurry. That has
// already happened once and it took kube-system DaemonSets with it.
//
// So the sweep is written down, and BOUNDED BY CONSTRUCTION rather than by the care
// of whoever runs it (bound.go):
//
//   - it lists in the SANDBOX NAMESPACE, which bindTo has already refused to let be
//     a system namespace,
//   - with the SANDBOX LABEL, so a neighbour scheduled there is not even returned,
//   - and it deletes by NAME with a UID precondition, after checking the object it
//     read back carries that label — never by handing a selector to a bulk delete.
//
// A pod younger than `idleAfter` is left alone. A create writes its row after the
// pod exists, so a sandbox mid-creation legitimately has a pod and no row for a
// moment — but the grace is an HOUR and not one interval, because the cost of the
// two mistakes is not symmetric: a leaked pod costs an hour of a node, and a pod
// deleted out from under its owner costs their work. The same hour the idle clock
// uses, for the same reason.
//
// ONE DEPLOYMENT PER SANDBOX NAMESPACE. This sweep's whole claim is "no row of ours
// names this pod", so a second cloud pointed at the same SANDBOX_NAMESPACE would
// read the first one's live sandboxes as orphans. That is the same invariant the
// stores already have — one writer per store — and it is stated here because this is
// where breaking it deletes something.
func orphans(ctx context.Context, s *cloud.Service[state]) {
	rt := s.State.rt
	if err := rt.ready(); err != nil {
		return
	}
	list, err := rt.pods().List(ctx, rt.bound.list())
	if err != nil {
		s.Log.Warn("reap: list sandbox pods", "namespace", rt.bound.Namespace, "err", err)
		return
	}
	claimed := known(ctx, s)
	if claimed == nil {
		// Not "no sandboxes are claimed" — the stores could not be read, and treating
		// an unreadable store as an empty one would delete every live sandbox in the
		// fleet. The one answer this sweep must never guess at.
		return
	}
	for i := range list.Items {
		p := &list.Items[i]
		id := p.GetLabels()[labSandbox]
		if id == "" || claimed[id] {
			continue
		}
		if age := time.Since(p.GetCreationTimestamp().Time); age < idleAfter {
			continue
		}
		if !rt.bound.covers(p) {
			continue
		}
		if err := rt.pods().Delete(ctx, p.GetName(), precondition(p)); err != nil {
			s.Log.Warn("reap: delete orphan", "pod", p.GetName(), "err", err)
			continue
		}
		s.Log.Info("reaped orphan sandbox pod", "pod", p.GetName(), "id", id)
	}
}

// known is every sandbox id the stores still claim, or nil when they could not all
// be read. nil is the load-bearing value: a partial answer here is indistinguishable
// from "these sandboxes are orphans", and acting on it would delete live work.
func known(ctx context.Context, s *cloud.Service[state]) map[string]bool {
	out, failed := map[string]bool{}, false
	_ = s.State.stores.Each(func(ns namespace.Namespace, st *Store, openErr error) {
		if openErr != nil {
			s.Log.Warn("reap: open store", "namespace", ns, "err", openErr)
			failed = true
			return
		}
		ids, err := st.IDs(ctx, ns.ID())
		if err != nil {
			s.Log.Warn("reap: list for orphan sweep", "namespace", ns, "err", err)
			failed = true
			return
		}
		for id := range ids {
			out[id] = true
		}
	})
	if failed {
		return nil
	}
	return out
}
