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
// that project gets a fresh pod against the same disk.
//
// A sandbox with a disk of its own is PARKED instead (suspends): that disk dies
// with the lease, so ending it would take its files, and a park keeps them for
// parkHold for a resume (park.go) to find.

import (
	"context"
	"errors"
	"time"

	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
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

// failedGrace is how long a sandbox pod that has already exited — Failed or
// Succeeded — may keep its place before this sweep deletes it. It is not a
// lifetime. A pod that never came up, or came up and died, is not doing work,
// and an hour of grace is an hour of a node spent on a corpse.
const failedGrace = 2 * time.Minute

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

// sweep is one pass over the sandboxes of every org THIS REPLICA OWNS.
//
// Errors are per-org and never abort the pass: one org with an unopenable store
// must not stop every other org's leases from ending. `Each` hands us the error
// rather than throwing, which is what makes that the easy shape to write.
//
// OWNERSHIP GATES THE ACT, and here the act reaches outside the file: `end` stops a
// POD. A file write can be fenced after the fact — the loser's ship is refused and
// its copy discarded — but a pod that has been deleted is not coming back, and the
// work inside it is gone. So the org's elected writer is the one that ends its
// leases, and the others do not touch them.
func sweep(ctx context.Context, s *cloud.Service[state]) {
	now := time.Now()
	read := balances{}
	ask := budgets{s: s, silent: map[string]bool{}}
	_ = s.State.stores.Each(func(ns namespace.Namespace, st *Store, openErr error) {
		if ctx.Err() != nil {
			return
		}
		if openErr != nil {
			s.Log.Warn("reap: open store", "namespace", ns, "err", openErr)
			return
		}
		if !s.State.stores.Owned(ns) {
			return // another replica ends this org's leases
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
		// EVERY ROW, AND NO LIMIT. This read was `List(org, "", "running")`, which
		// got both halves wrong. `List` is `ORDER BY created_at DESC LIMIT 200`, so
		// an org past two hundred sandboxes had its OLDEST — the ones furthest past
		// their lease — dropped from every sweep: billed for ever, ended never. And
		// filtering to `running` skipped the rows whose start failed, which are the
		// ones still holding a pod nothing else will stop (see clocks.over).
		// `Rows` is the sweep's own set, unbounded, like IDs and Held beside it.
		since := time.Now()
		rows, err := st.Rows(ctx, ns.ID())
		if err != nil {
			s.Log.Warn("reap: list sandboxes", "namespace", ns, "err", err)
			return
		}
		if len(rows) == 0 {
			// No sandbox, and none holding an unbilled span: nothing for any sweep.
			if held, err := st.Held(ctx, ns.ID()); err == nil && len(held) == 0 {
				s.State.stores.Settle(ns, since)
			}
			return
		}
		for _, m := range rows {
			// END IS ASKED FIRST, and `over` is the whole of it (lifecycle.go):
			// the ceiling from creation, the lease, and the untouched-allowance
			// that is a different length depending on whether anybody is watching.
			// It hands back WHICH clock ran out, which is the string the row's
			// last line carries.
			//
			// `ends` asks the money too (lifecycle.go): a paid lease its wallet or
			// its org's budget stopped carrying, or one past
			// SANDBOX_MAX_SPEND_CENTS, ends as `cost-ceiling` rather than as a clock.
			if done, why, detail := s.State.clk.closes(m, now, read.purse(ctx, m, ask)); done {
				if suspends(m) {
					if _, err := park(s, ctx, st, m, why); err != nil {
						s.Log.Warn("reap: park", "id", m.ID, "org", m.Org, "why", why, "err", err)
					} else {
						s.Log.Info("parked sandbox", "id", m.ID, "org", m.Org, "why", why)
					}
					continue
				}
				end(ctx, s, st, m, why, detail)
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
			//
			// A LEASE IS CARRIED WHILE ITS WALLET COVERS THE NEXT HOUR AND ITS ORG'S
			// BUDGET HAS ROOM FOR IT. The meter bills the hours already held; nothing
			// else asked whether another one can be paid for, so a run could be
			// carried into a negative balance. And the wallet is not the budget
			// (#806): a lease admitted under budget was carried on its wallet alone
			// until its max-life, a day, and a desktop has none. An hour either one
			// refuses is not granted, and the lease ends at its own expiry (parked
			// when it keeps a disk) as a cost ceiling.
			if carried(m, now.Unix()) && read.covers(ctx, m) && ask.allow(ctx, m) {
				want := now.Unix() + extendBy
				// For free leases without a payer, cap at absolute ceiling; paid leases keep running as long as they pay.
				if m.Payer == "" && s.State.clk.absolute > 0 {
					if ceiling := m.CreatedAt + int64(s.State.clk.absolute/time.Second); want > ceiling {
						want = ceiling
					}
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

// carried reports whether the sweep pushes a busy lease forward: a running lease
// within extendWhenUnder of its end. A FREE LEASE IS NOT CARRIED: it bills nobody,
// and freeTTL is the whole of what the free lane grants (prepaid.go), so activity
// keeps it only to its end.
func carried(m Sandbox, now int64) bool {
	return m.Payer != "" && !parked(m) && m.ExpiresAt > 0 && m.ExpiresAt-now < extendWhenUnder
}

// balanceWait bounds one read of a payer's money inside the sweep, its balance or
// its org's budget. The sweep ends every org's leases in turn, so a finance plane
// that does not answer must cost one payer a few seconds and not the fleet a
// quarter of an hour.
const balanceWait = 3 * time.Second

// balances is what a sweep has read of the payers' balances, once each: a payer
// with a dozen leases is asked once. A balance that could not be read is kept as
// unknown (-1) for the rest of the sweep, so the next minute asks again.
type balances map[string]int64

// covers reports whether the lease's payer can cover one more hour of it. An
// unknown balance answers no for this sweep, not for good: the extension window is
// ten minutes and the sweep runs every minute, so a finance plane that is back
// within the window costs the lease nothing.
func (w balances) covers(ctx context.Context, m Sandbox) bool {
	have := w.of(ctx, m)
	return have >= 0 && have >= hourly(ctx, m.Class, diskSize(m))
}

// short reports that the lease's payer was READ and cannot cover one more hour:
// the opposite of covers for a balance that answered, and false for one that did
// not, which is not a verdict on the wallet.
func (w balances) short(ctx context.Context, m Sandbox) bool {
	have := w.of(ctx, m)
	return have >= 0 && have < hourly(ctx, m.Class, diskSize(m))
}

// of is the payer's balance in cents, read once a sweep, or -1 when it could not
// be read.
func (w balances) of(ctx context.Context, m Sandbox) int64 {
	have, read := w[m.Payer]
	if !read {
		payer := account.PayerOf("", m.Payer)
		ask, cancel := context.WithTimeout(ctx, balanceWait)
		cents, err := balanceCents(ask, payer.Org(), payer.Subject())
		cancel()
		have = cents
		if err != nil {
			have = -1
		}
		w[m.Payer] = have
	}
	return have
}

// purse is what `ends` may ask of this lease's money, read only if it asks.
func (w balances) purse(ctx context.Context, m Sandbox, b budgets) purse {
	return purse{
		hour:   func() int64 { return hourly(ctx, m.Class, diskSize(m)) },
		short:  func() bool { return w.short(ctx, m) },
		budget: func() bool { return b.spent(ctx, m) },
	}
}

// budgets is what a sweep has asked the orgs' ledgers about carrying their leases.
//
// The wallet is not the budget (prepaid.go budgeted): an org's budget is its
// enforced spend cap, which only its ledger weighs, so a lease is carried into its
// next hour when the ledger has room for that hour too. The ask is per lease, at
// that lease's own hour, since leases of one payer cost different amounts. A payer
// whose ledger did not answer is not asked again this sweep, so a wedged ledger
// costs one payer a few seconds and not the fleet a quarter of an hour.
type budgets struct {
	s      *Service
	silent map[string]bool
}

// errSilentLedger is a payer whose ledger already did not answer this sweep.
var errSilentLedger = errors.New("sandbox: the org's ledger did not answer this sweep")

// allow reports whether the lease's org has budget for one more hour of it. A
// ledger that did not answer is no for this sweep, not for good, as an unread
// balance is: the carry window is ten minutes and the sweep runs every minute.
func (b budgets) allow(ctx context.Context, m Sandbox) bool {
	return b.ask(ctx, m) == nil
}

// spent reports that the lease's org's ledger ANSWERED and refused another hour at
// its budget. A ledger that did not answer is not a verdict on the budget.
func (b budgets) spent(ctx context.Context, m Sandbox) bool {
	return errors.Is(b.ask(ctx, m), meter.ErrSpendCapExceeded)
}

// ask weighs one more hour of the lease (hourly, the figure the wallet is weighed
// at) against its org's books, bounded like a balance read. A refusal is an
// answer; any other error marks the payer silent for the rest of the sweep.
func (b budgets) ask(ctx context.Context, m Sandbox) error {
	if b.silent[m.Payer] {
		return errSilentLedger
	}
	ask, cancel := context.WithTimeout(ctx, balanceWait)
	defer cancel()
	err := ledgerAllows(b.s, ask, account.PayerOf("", m.Payer), hourly(ctx, m.Class, diskSize(m)))
	if err != nil && !errors.Is(err, meter.ErrSpendCapExceeded) && !errors.Is(err, meter.ErrInsufficientBalance) {
		b.silent[m.Payer] = true
	}
	return err
}

// suspends reports whether a running sandbox whose time is up is PARKED (suspended)
// rather than ended: one with a disk of its own (Spec.Disk), or a desktop sandbox
// (Class == "desktop") which is built for long-running suspend/resume.
// Its pod goes to stop compute billing, and its volume and lease row stay in parked
// state so resume brings it back tree and state intact as long as they can pay.
func suspends(m Sandbox) bool {
	return m.Status == statusRunning && m.Payer != "" && m.Volume != "" && (m.Project == "" || m.Class == "desktop")
}

// end retires a sandbox whose lease is over: the pod goes, the row goes, the
// volume stays, and a tombstone says why.
func end(ctx context.Context, s *cloud.Service[state], st *Store, m Sandbox, why, detail string) {
	// THE REASON OUTLIVES THE ROW. It is buried before the retire, so the one ship
	// that makes the deletion durable carries it too; a retire that fails leaves the
	// row, which the next sweep ends again and buries again (Bury is an upsert).
	why, detail = cause(m, why, detail)
	if err := st.Bury(ctx, tombstone{
		ID: m.ID, Org: m.Org, Actor: m.Actor, Class: m.Class, Project: m.Project,
		Reason: why, Detail: detail, EndedAt: time.Now().Unix(),
	}); err != nil {
		s.Log.Warn("reap: record why the sandbox ended", "id", m.ID, "org", m.Org, "why", why, "err", err)
	}
	// One settlement, in one order (meter.go retire): claim the tail, stop the pod,
	// drop the row, ship the file that now says both, and bill only if that ship was
	// acknowledged. The volume survives a reap, so nothing is purged here.
	retire(s, ctx, st, m, false)
	s.Log.Info("reaped sandbox", "id", m.ID, "org", m.Org, "why", why, "detail", detail)
}

// cause is the reason a tombstone keeps. A park the REAPER took wrote its reason
// into Error, and the hold that later retires it is only `parked`; what ended the
// run was the reason it was parked for. A failed start keeps what the cluster said.
func cause(m Sandbox, why, detail string) (string, string) {
	switch {
	case why == "parked" && m.Error != "":
		return m.Error, "parked, then retired after its hold"
	case why == "start-failed" && detail == "":
		return why, m.Error
	}
	return why, detail
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
// ONE REPLICA PER ORG, WHICH IS WHY THE POD'S OWN ORG LABEL DECIDES. This sweep's
// claim is "no row of ours names this pod", and on more than one pod that claim is
// only true about the orgs THIS replica owns: a peer holds the rows for the orgs it
// owns, so a pod belonging to one of those is unclaimed HERE and very much alive
// there. Reading the whole namespace and differencing it against a partial set is
// how one pod deletes another's live sandboxes — the same shape as the incident
// this file's bound was written for, one layer up.
//
// The fix needs no completeness assumption, because the answer is already ON the
// object: every sandbox pod carries `hanzo.ai/sandbox-org`, written at create from
// the validated org. A pod whose org this replica does not own is not this replica's
// to judge, so it is skipped before any question about rows is asked.
//
// A pod with NO org label cannot be billed. Leaving it alone is how a failed start
// and an idle run with a missing label held a node for hours. It is reaped on a
// cost ceiling: a failed or finished pod after failedGrace, anything else after
// orphanGrace. That ceiling is the longest an unaccountable pod may cost a node.
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
	claimed, mine := known(ctx, s)
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
		org := p.GetLabels()[labOrg]
		// The namespace this replica owns, as known() read it off the org's own store,
		// and not one rebuilt from the label: a label written by an older image is a
		// slug that folds to another org's namespace, and a lease asked for there is
		// not the lease that was meant.
		var owned namespace.Namespace
		if org != "" {
			var ok bool
			if owned, ok = mine[org]; !ok {
				if owned, ok = owns(s, org); !ok {
					continue // another replica owns this org, so its pods are not ours to judge
				}
			}
		}
		grace := orphanGrace
		if podOver(p) {
			grace = failedGrace
		}
		if age := time.Since(p.GetCreationTimestamp().Time); age < grace {
			continue
		}
		if !rt.bound.covers(p) {
			continue
		}
		// ASKED AGAIN, HERE, because this is the line that cannot be undone. `mine` was
		// built by known() over a whole pass of the orgs (on hanzo-sql, an open, a
		// migration and a close for each), which is minutes at thousands of orgs, and
		// a lease can pass to another replica in that time. The answer that matters is
		// the one given now, with the margin every answer carries.
		if org != "" && !s.State.stores.Owned(owned) {
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

// podOver reports a sandbox pod whose containers have finished: Failed or
// Succeeded. Pending and Running are not over — a slow pull is still a start.
func podOver(p *unstructured.Unstructured) bool {
	phase, _, _ := unstructured.NestedString(p.Object, "status", "phase")
	return phase == "Failed" || phase == "Succeeded"
}

// owns answers ownership for an org the sweep did not visit because it had
// nothing pending: a pod labelled for it is exactly the reason to open it now.
func owns(s *cloud.Service[state], org string) (namespace.Namespace, bool) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil || !s.State.stores.Has(ns) {
		return ns, false
	}
	if _, err := s.State.stores.For(ns); err != nil {
		return ns, false
	}
	return ns, s.State.stores.Owned(ns)
}

// known is every sandbox id the stores still claim, beside the orgs this replica
// OWNS, each under every label a pod of it may carry and with the namespace that owns
// it — or (nil, nil) when the stores could not all be read.
//
// nil is the load-bearing value: a partial answer here is indistinguishable from
// "these sandboxes are orphans", and acting on it would delete live work.
//
// The two answers come back together because they are read in one pass and are only
// meaningful together. The id set says which pods are accounted for; the org set
// says which pods this replica is entitled to have an opinion about. Claimed ids
// are collected for EVERY org on the volume, owned or not — a pod claimed by a row
// we can see is not an orphan whoever owns it, and that direction can only ever
// protect a pod.
func known(ctx context.Context, s *cloud.Service[state]) (claimed map[string]bool, owned map[string]namespace.Namespace) {
	out, mine, failed := map[string]bool{}, map[string]namespace.Namespace{}, false
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
		if !s.State.stores.Owned(ns) {
			return
		}
		// EVERY RENDERING THE LABEL HAS EVER CARRIED, and that is a permanent rule
		// rather than a migration. A pod outlives the image that created it, so a
		// sweep that recognises only what the RUNNING image would write is a sweep
		// that cannot see half the cluster. The label was `slug(org)` and is now
		// `orgKey(org)`; the two agree on a clean lowercase name and disagree on
		// every other one (namespace.Sanitize appends a digest, slug does not), so
		// matching only the current fold left an orphan of `acme.co` unreachable —
		// not until the next deploy, but for ever, because an orphan is by
		// definition never recreated.
		//
		// The raw org comes from the rows, which is the only place it survives: a
		// namespace cannot be unfolded back into the name it was built from.
		mine[ns.ID()] = ns
		raw, oerr := st.Org(ctx)
		if oerr != nil {
			// The claimed set is already suspect if a read failed, and `failed`
			// above governs that. Here it only means one org's older label is
			// unmatched, which can leak a pod but can never delete one.
			s.Log.Warn("reap: read org for the orphan sweep", "namespace", ns, "err", oerr)
			return
		}
		if raw != "" {
			mine[slug(raw)] = ns
		}
	})
	if failed {
		return nil, nil
	}
	return out, mine
}
