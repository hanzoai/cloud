package sandbox

// meter.go — a sandbox somebody is HOLDING is an agent running, so it is billed by
// the hour, at the price of the ENVELOPE it is holding.
//
// PER CLASS, because the classes are not one size. The runtime rate is the
// platform's one agentic-runtime price and it is what an agent session and a
// resident bot pay; a sandbox pays it too where it holds the default envelope,
// and more where it holds more. An android lease holds twelve times the memory
// of an exec lease, and charging both the same hour would sell most of a node
// for the price of a sixteenth of one.
//
// The RULE is [cloud.RateMicros] and only the DEFAULT is local, which is the
// shape ResourceFee already uses one file over: an operator retunes any class at
// admin.hanzo.ai with an audit trail, and until they do the floor written in the
// class table is charged.
//
// TWO ACTS, TWO CHARGES, and they are deliberately not folded together. The LEASE
// FEE (api.go, ResourceFee) is what taking a sandbox costs, once, and it is zero
// until somebody prices it. RUNTIME is what HOLDING one costs, per hour, and it is
// the same rate an agent session and a resident bot pay — because the platform
// sells one thing in all three and apps/exec already records the rule this obeys:
// one act, one charge, at the layer that owns it.
//
// THE ROW IS DELETED WHEN THE LEASE ENDS, which is what makes this different from
// every other duration meter in the tree. `provisioning` can say "a dropped
// instance is never charged — that is how delete stops the meter" because its rows
// persist until the drop; here the row is gone a line later, so the final partial
// span has to be CLAIMED before the delete or it is lost — and, because the delete
// is a durable fact too, SHIPPED after it. Both delete sites go through [retire]:
// End (a caller ended it) and reap's end (the reaper did).

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/namespace"
)

// holding reports whether this lease is occupying capacity right now.
//
// It is the SAME predicate Live and LiveOfClass use for "this sandbox is holding a
// pod", so what the fleet counts as occupied and what it charges for cannot
// disagree — and a lease that never came up (status "error") is billed for nothing,
// which is the rule the lease fee already follows one file over.
func holding(m Sandbox) bool { return m.Status == "running" || m.Status == "pending" }

// running describes one lease to the runtime meter. Payer, never Org: the lease fee
// landed on the ledger the caller was gated against, and a recurring charge on a
// different wallet would bill one lease to two payers.
// The rate rides the ROW rather than the pass, because a sweep holds many classes
// at once and one rate read per tick was one rate for all of them.
func running(ctx context.Context, m Sandbox) cloud.Running {
	return cloud.Running{
		ID:        m.ID,
		Payer:     m.Payer,
		Project:   m.Project,
		Model:     "sandbox/" + m.Class,
		MeteredAt: m.MeteredAt,
		Rate:      rate(ctx, m.Class),
	}
}

// rate is the published price of one hour of `class`, in micro-USD, falling back
// to the floor the class table carries. A class nobody has heard of is not free:
// it falls back to the platform runtime rate, because an unknown envelope is at
// least an envelope, and billing it nothing is the one answer certainly wrong.
func rate(ctx context.Context, class string) int64 {
	floor := cloud.RuntimeHourMicros
	if c, ok := classes[class]; ok && c.micros > 0 {
		floor = c.micros
	}
	return cloud.RateMicros(ctx, cloud.RuntimeProduct, cloud.RuntimeMeter+"-"+class, floor)
}

// retire is the WHOLE of ending a lease, and it is one function because the order
// of its five steps is the thing that has to be right. Both end-of-lease paths —
// a caller's End and the reaper's end — go through it.
//
//	claim   the tail, on the row's own watermark
//	stop    the pod (and the volume too, if the caller asked to purge)
//	delete  the row
//	ship    the file, which now carries BOTH the advance and the deletion
//	bill    only if that ship was acknowledged
//
// SHIPPING BEFORE THE DELETE WAS THE BUG, and it was worse than not shipping at
// all. The old order billed the tail, shipped, then deleted — so the last
// acknowledged snapshot held the lease as `running` with a watermark at the moment
// it ended, and nothing ever shipped the deletion. A successor hydrating that
// snapshot found a live lease for a sandbox that had not existed since, and its
// first sweep charged the customer from the end of the lease to whenever the
// successor happened to wake up. Not a double charge of one span: a charge for a
// span in which nothing ran, growing with the length of the outage.
//
// One ship, taken after both writes, makes the two facts arrive together or not at
// all. If it is refused nothing is billed and nothing is durable, so the successor
// re-reaps the lease and bills the tail once — which is the same answer, reached by
// the replica that can prove it.
//
// A failure at any step is logged and dropped rather than returned: the lease is
// already over, and a debit that could not be recorded must never turn ending a
// sandbox into a refusal. It is the same posture Record takes.
func retire(s *Service, ctx context.Context, st *Store, m Sandbox, purge bool) {
	ns, err := cloud.OrgNamespace(m.Org, "")
	if err != nil {
		s.Log.Warn("the lease names no namespace, so it can be neither billed nor retired",
			"sandbox", m.ID, "org", m.Org, "err", err)
		return
	}
	// The claim, the two destructions and the ship are ONE settlement, so the
	// destructions ride inside it: RuntimeSweep's contract is claim → make durable →
	// emit, and at the end of a lease what has to be made durable is the retirement.
	rows := []cloud.Running{}
	if holding(m) {
		// A lease that never came up is billed for nothing — the rule the lease fee
		// already follows — so it is retired without a claim.
		rows = append(rows, running(ctx, m))
	}
	_, err = cloud.RuntimeSweep(ctx, rows, time.Now().Unix(),
		func(ctx context.Context, id string, was, now int64) (bool, error) {
			return st.Advance(ctx, m.Org, id, was, now)
		},
		func() (bool, error) { return settle(s, ctx, st, ns, m, purge) },
		s.State.debit,
	)
	if err != nil {
		s.Log.Warn("the lease was retired and its tail not billed (nothing is durable, so the next owner bills it)",
			"sandbox", m.ID, "org", m.Org, "err", err)
	}
	// A row that owed nothing still has to be retired, and RuntimeSweep does not
	// ship a pass that claimed nothing — rightly, since an idle org owes no round
	// trip. So the settlement is taken directly for that case.
	if len(rows) == 0 {
		if _, serr := settle(s, ctx, st, ns, m, purge); serr != nil {
			s.Log.Warn("the lease could not be retired", "sandbox", m.ID, "org", m.Org, "err", serr)
		}
	}
}

// settle stops the sandbox, drops its row, and ships the file that now says so.
//
// The pod goes FIRST and the ship goes LAST, which is the only order in which a
// failure is recoverable. A pod stopped and a row that outlives it is the state the
// orphan sweep and the reaper are both built to converge; a row deleted while its
// pod runs is a pod nothing will ever ask about again.
func settle(s *Service, ctx context.Context, st *Store, ns namespace.Namespace, m Sandbox, purge bool) (bool, error) {
	// ASKED AGAIN, HERE, because this is the line that cannot be undone. The sweep
	// asked before it began and then walked a whole org's rows; a lease can be
	// re-elected away inside that walk, and the answer that mattered was the one at
	// the top. It is a lock and an HRW over the live member set — no I/O, so it is
	// free to ask per row and it still answers during an object-store outage, which
	// is exactly when a replica must go on ending its own expired leases.
	if !s.State.stores.Owned(ns) {
		return false, fmt.Errorf("sandbox: %s is no longer this replica's to end", ns)
	}
	if serr := s.State.rt.stop(ctx, m); serr != nil {
		s.Log.Warn("stop sandbox", "id", m.ID, "err", serr)
	}
	if purge && m.Volume != "" {
		if perr := s.State.rt.purge(ctx, m); perr != nil {
			s.Log.Warn("purge volume", "volume", m.Volume, "err", perr)
		}
	}
	if derr := st.Delete(ctx, m.Org, m.ID); derr != nil {
		return false, derr
	}
	return s.State.stores.Sync(ns)
}

// meterRuntime bills every lease every org is holding, for the span since the last
// tick. reap's ticker fires it; the amount is (now − watermark), so the cadence
// decides only how often money moves and never how much.
//
// It reads Held and NOT List, because List is LIMIT 200 — right for a page, and for
// a set money is computed over it means every lease past row 200 is free.
//
// IT BILLS THE ORGS THIS REPLICA OWNS AND NO OTHERS. Every pod holds every org's
// file its volume carries, so an ungated sweep is two pods billing one span twice —
// and the watermark CAS cannot stop it, because the two pods are CASing two
// different files. The fence answers who owns the org; the ship settles it.
func meterRuntime(ctx context.Context, s *Service) {
	// A PUBLISHED ZERO IS A PRICE, AND A PRICE STILL MOVES THE CLOCK. Runtime is
	// free this week, so nothing is charged — but the span still HAPPENED, and the
	// watermark is what says it is accounted for. Returning here instead left the
	// watermark where it was, so restoring the price on Monday billed every free
	// hour of the promotion RETROACTIVELY at the new rate, in one debit, to every
	// tenant holding a lease. cloud.RuntimeCharge already does the right thing with
	// a zero: it advances first and emits only when the span is worth something, so
	// the whole fix is to let it be asked. The two end-of-lease paths never had this
	// bug — they call bill() unconditionally — which is why it showed up here alone.
	_ = s.State.stores.Each(func(ns namespace.Namespace, st *Store, openErr error) {
		if ctx.Err() != nil {
			return
		}
		if openErr != nil {
			// A store that could not be opened has not said "no leases" — it has
			// said nothing, and billing nothing for it is the safe direction.
			s.Log.Warn("runtime meter: open store", "namespace", ns, "err", openErr)
			return
		}
		if !s.State.stores.Owned(ns) {
			return // another replica bills this org, or nobody can prove who does
		}
		held, err := st.Held(ctx, ns.ID())
		if err != nil {
			s.Log.Warn("runtime meter: list held leases", "namespace", ns, "err", err)
			return
		}
		// ONE SHIP FOR THE WHOLE ORG, not one per lease. A ship copies the org's
		// whole database to the object store, so an org holding two hundred leases
		// would otherwise send that file two hundred times to bill one minute. Each
		// row carries its own class rate, so batching prices nothing wrong.
		rows := make([]cloud.Running, 0, len(held))
		for _, m := range held {
			rows = append(rows, running(ctx, m))
		}
		org := ns.ID()
		_, err = cloud.RuntimeSweep(ctx, rows, time.Now().Unix(),
			func(ctx context.Context, id string, was, now int64) (bool, error) {
				return st.Advance(ctx, org, id, was, now)
			},
			func() (bool, error) { return s.State.stores.Sync(ns) },
			s.State.debit,
		)
		if err != nil {
			s.Log.Warn("runtime meter: the org's spans were claimed and not billed (the watermarks are not durable, so the next owner bills them)",
				"namespace", ns, "err", err)
		}
	})
}
