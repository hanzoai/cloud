package sandbox

// The reaper: what makes a lease end.
//
// Everything needed for this already existed and nothing called it. `ExpiresAt`
// was written on every create, `LastUsedAt` was stamped on every call,
// the lease was written on every create, and `stop` was implemented — and a sandbox,
// once created, ran forever. Measured, not inferred: a proof pod from a test run
// was still Running 64 minutes later and had to be deleted by hand.
//
// That is one bug wearing two faces. A sandbox that never sleeps is a sandbox
// that bills forever, so the reaper is simultaneously the idle-sleep feature and
// the honest meter. Neither is a separate component.
//
// WHEN a lease ends is decided in lifecycle.go and nowhere else — the lease it
// was sold, the attention it has had, and a ceiling over both. This file is the
// LOOP: it lists, it asks, and it acts on the answer. Restating the policy here
// is how the expiry ended up half in a WHERE clause and half in a Go loop, with
// neither half naming the other.
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
	"github.com/hanzoai/cloud/apps/k8s"
	"github.com/hanzoai/namespace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// reapEvery is how often we look. A minute is far finer than the smallest lease
// (900s for exec) and coarse enough that the sweep is invisible next to the
// apiserver traffic one active sandbox makes.
const reapEvery = time.Minute

// orphanGrace is how long a pod may exist with no row before this sweep presumes
// it leaked. It is NOT a lifetime clock — those are in lifecycle.go and there are
// three of them — and it does not become shorter when they do.
//
// It answers one narrow question: how wide is the window in which a pod
// legitimately has no row? A create writes its row AFTER the pod exists, so a
// sandbox mid-creation is exactly that, and the cost of the two mistakes is not
// symmetric — a leaked pod costs an hour of a node, a pod deleted out from under
// its owner costs their work. So it is an hour, not one interval, and it stays an
// hour even where the disconnected clock reaps in fifteen minutes: those pods are
// removed by name, through their row, and never reach this sweep at all.
const orphanGrace = time.Hour

// extendWhenUnder is how close to expiry a BUSY sandbox has to be before its
// lease is pushed out. Ten minutes: comfortably more than one reap sweep, so a
// working run is never a tick away from losing its pod, and short enough that a
// lease still means something.
const extendWhenUnder = int64(10 * 60)

// extendBy is how much runway a busy sandbox is granted each time. One hour —
// the same span as the watched idle allowance, so the two rules read against the
// same clock: an hour of silence ends a sandbox, an hour of runway is what work
// buys.
//
// The CEILING this is bounded by is not here. It is clocks.absolute
// (lifecycle.go), the one number the create clamps to and the sweep ends past,
// because without a ceiling extension is not a lease renewal but a lease
// abolition — and a sandbox that has been working for a day is a job that wants
// a Deployment, not a session that wants another hour.
const extendBy = int64(60 * 60)

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
			meterRuntime(ctx, s)
			sweep(ctx, s)
			orphans(ctx, s)
			reclaim(ctx, s)
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
		// WHEN a sandbox ends is not a query the store answers. The store holds
		// facts — the stamps — and lifecycle.go holds the policy that reads them,
		// which is why there is one list here and not one per reason: the expiry
		// used to be a WHERE clause and the idle check a loop, so half the rule
		// lived in SQL and half in Go and neither half named the other.
		//
		// `over` is that rule, and it hands back which clock ran out. The string
		// is what the row's last line says, and it is the only thing an operator
		// has when asked why a sandbox went away.
		running, err := st.List(ctx, ns.ID(), "", "running")
		if err != nil {
			s.Log.Warn("reap: list running", "namespace", ns, "err", err)
			return
		}
		for _, m := range running {
			// END IS ASKED FIRST, and `over` is the whole of it (lifecycle.go):
			// the ceiling from creation, the lease, and the untouched-allowance
			// that is a different length depending on whether anybody is watching.
			// It hands back WHICH clock ran out, which is the string the row's
			// last line carries.
			if done, why := s.State.clk.over(m, now); done {
				end(ctx, s, st, m, why)
				continue
			}
			// A SANDBOX THAT IS WORKING KEEPS ITS COMPUTER.
			//
			// LastUsedAt already tells us the difference between a run that is
			// progressing and one that is abandoned — it is stamped by every exec
			// and every fs call, and the rule above already trusts it to kill.
			// Trusting it in ONE direction only was the bug: activity could
			// shorten a lease and never lengthen it, so a deep-research run or a
			// long build that was demonstrably alive still died at its TTL. That
			// is the one case where reaping destroys the most work, and it fired
			// on exactly the runs worth keeping.
			//
			// So a busy sandbox gets its lease pushed to now+extendBy, bounded by
			// the SAME ceiling everything else reads (clk.absolute, from
			// CREATION), so no amount of activity turns a sandbox into a permanent
			// resident. It runs BEFORE the lease is actually up — extendWhenUnder
			// is ten minutes and the sweep is every minute — so a busy sandbox is
			// carried forward rather than reaped and re-leased. The idle rules
			// above still outrank it, and Extend only ever moves a lease FORWARD,
			// so this can never cut one short.
			if m.ExpiresAt > 0 && m.ExpiresAt-now.Unix() < extendWhenUnder {
				want := now.Unix() + extendBy
				if ceiling := m.CreatedAt + int64(s.State.clk.absolute/time.Second); want > ceiling {
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
	// The tail, BEFORE the delete — the same reason End states: the watermark
	// lives on the row, so a span not charged here is a span nothing can recover.
	bill(s, ctx, st, m, cloud.RuntimeRate(ctx))
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
// A pod younger than `orphanGrace` is left alone — see the constant for why that
// window is what it is, and why it is its own number rather than borrowed from a
// lifetime clock.
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
		if age := time.Since(p.GetCreationTimestamp().Time); age < orphanGrace {
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

// diskCold is how long a project disk may go unleased before it is reclaimed.
//
// Two weeks, and the number is a trade between two real costs rather than a round
// figure. A disk holds a checkout and dependency caches — losing one costs a slow
// first call, not work, because everything on it came from somewhere else and can
// come from there again. Holding one costs its full size every day forever. So the
// bound is generous enough that a project someone returns to after a holiday still
// finds its cache warm, and short enough that a project nobody returns to is not
// billed for a year.
//
// It reads against annLeased, which every lease refreshes, so "cold" means nobody
// asked for this project for a fortnight — not that the disk is old. A disk in
// daily use is never a day closer to being reclaimed.
const diskCold = 14 * 24 * time.Hour

// reclaim frees project disks nobody has leased for diskCold and nothing mounts.
//
// It is the third sweep and it closes the one gap the other two leave open BY
// DESIGN: `end` keeps the volume, deliberately, because ending a lease must never
// destroy what a tenant made and resume-is-cheap is the whole point of a per-project
// disk. That is right for a project someone comes back to. It is also why a disk is
// the one object here with no natural death — the pod goes with the lease, the row
// goes with the pod, and the disk outlives both with nothing left to account for it.
//
// ensureVolume already writes the two facts a reclaim needs — the day of the last
// lease, and the project the disk belongs to — and said in its own comment that
// nothing read them. This reads them.
//
// FAIL-SAFE ON READ, the same way `known` returning nil stops the orphan sweep: a
// pod list that cannot be had is not an empty pod list. "No pod mounts this disk"
// and "we could not find out" are the same sentence to a caller and opposite facts
// to a tenant, so the unreadable case reclaims NOTHING.
//
// Bounded by construction like every other sweep here (bound.go): listed in the
// sandbox namespace under the project label, deleted BY NAME with a UID
// precondition, after checking the object read back is one this bound holds.
func reclaim(ctx context.Context, s *cloud.Service[state]) {
	rt := s.State.rt
	if err := rt.ready(); err != nil {
		return
	}
	mounted := mountedDisks(ctx, s)
	if mounted == nil {
		return
	}
	vols := rt.dyn.Resource(k8s.Volumes).Namespace(rt.ns)
	list, err := vols.List(ctx, rt.bound.disks())
	if err != nil {
		s.Log.Warn("reap: list project disks", "namespace", rt.ns, "err", err)
		return
	}
	cutoff := time.Now().UTC().Add(-diskCold)
	for i := range list.Items {
		d := &list.Items[i]
		if mounted[d.GetName()] {
			continue
		}
		// An undated disk is KEPT. It predates the stamp, so its last use is
		// unrecorded — and a disk that cannot be shown to be dead is not one to
		// delete. It will be dated the next time its project is leased, and become
		// reclaimable then, which is the only honest way for it to get there.
		leased := d.GetAnnotations()[annLeased]
		when, perr := time.Parse(time.DateOnly, leased)
		if leased == "" || perr != nil || when.After(cutoff) {
			continue
		}
		if !rt.bound.holds(d) {
			continue
		}
		if err := vols.Delete(ctx, d.GetName(), precondition(d)); err != nil {
			s.Log.Warn("reap: reclaim disk", "disk", d.GetName(), "err", err)
			continue
		}
		s.Log.Info("reclaimed cold project disk", "disk", d.GetName(), "leased", leased)
	}
}

// mountedDisks is every claim a pod in the sandbox namespace currently mounts, or
// nil when that could not be determined.
//
// It lists the namespace WITHOUT the sandbox label, which is the one place here
// that reads wider than the bound, and deliberately: this answer only ever
// protects a disk. A pod that is not ours holding one of our claims is exactly the
// case a label-narrowed read would miss, and missing it deletes a mounted disk.
// Widening a read that can only say "keep" is safe; the delete below it is still
// bounded.
func mountedDisks(ctx context.Context, s *cloud.Service[state]) map[string]bool {
	pods, err := s.State.rt.pods().List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Log.Warn("reap: list pods for disk sweep", "err", err)
		return nil
	}
	out := map[string]bool{}
	for i := range pods.Items {
		vols, _, verr := unstructured.NestedSlice(pods.Items[i].Object, "spec", "volumes")
		if verr != nil {
			// One unreadable pod spec is not a reason to reclaim nothing, but it IS a
			// reason not to trust this pod's mounts. Skipping it can only fail toward
			// deleting a disk it mounted, so treat the whole answer as unusable.
			s.Log.Warn("reap: read pod volumes", "pod", pods.Items[i].GetName(), "err", verr)
			return nil
		}
		for _, v := range vols {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			c, ok := m["persistentVolumeClaim"].(map[string]any)
			if !ok {
				continue
			}
			if n, ok := c["claimName"].(string); ok && n != "" {
				out[n] = true
			}
		}
	}
	return out
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
