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
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	"k8s.io/apimachinery/pkg/api/resource"
)

// holding reports whether this lease is occupying capacity right now.
//
// It is the SAME predicate Live and LivePaid use for "this sandbox is holding a
// pod", so what the fleet counts as occupied and what it charges for cannot
// disagree — and a lease that never came up (status "error") is billed for nothing,
// which is the rule the lease fee already follows one file over.
func holding(m Sandbox) bool { return m.Status == statusRunning || m.Status == statusPending }

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
// StorageGiBHourMicros is the compiled floor for a gibibyte of attached disk,
// held for an hour, in micro-USD. $0.0002 — about $0.14 a month per GiB, so the
// 20 GiB a lease gets by default is roughly $2.90 a month held parked.
//
// IT IS A FLOOR, in [cloud.RateMicros]'s sense: an operator retunes it at
// admin.hanzo.ai with an audit trail, and until they do this is charged. The
// number is written down here rather than read from a file for the reason the
// runtime floor gives one line up — money is not a float, and a price parsed on
// every debit rounds differently than one written once.
const StorageGiBHourMicros int64 = 200

// diskSuffix is what makes the storage span its OWN claim. RuntimeSweep's advance
// callback is keyed by the row's ID, so two rows for one lease sharing an ID would
// each be routed to the same compare-and-set and undo the other. The suffix is on
// the invoice too, which is the honest reading: a lease and its disk are two lines.
const diskSuffix = "#disk"

// advance routes a claim to the watermark that owns it. ONE function for the three
// sweeps, so the retirement, the park and the org pass cannot disagree about which
// column a span belongs to.
func advance(st *Store, org string) func(context.Context, string, int64, int64) (bool, error) {
	return func(ctx context.Context, id string, was, now int64) (bool, error) {
		if before, ok := strings.CutSuffix(id, diskSuffix); ok {
			return st.AdvanceDisk(ctx, org, before, was, now)
		}
		return st.Advance(ctx, org, id, was, now)
	}
}

// spans is every billable span of one lease at this moment: the runtime while a
// pod is up, and the disk for as long as the volume exists. Built in one place so
// a caller cannot bill half a lease by forgetting the other meter.
func spans(ctx context.Context, m Sandbox) []cloud.Running {
	out := make([]cloud.Running, 0, 2)
	if holding(m) {
		out = append(out, running(ctx, m))
	}
	if storing(m) {
		out = append(out, stored(ctx, m))
	}
	return out
}

// storageMeter names the storage span on the invoice, beside runtime-hour.
const storageMeter = "storage-gib-hour"

// diskSize is how big a lease's disk is, as its claim asks for it (ensureVolume),
// and "" when it has none. ONE ANSWER, read by the claim and by both prices of it
// (stored, quote), so the size billed is the size made — a second constant here
// would drift the day an operator changed the disk and not the price.
//
// A project's disk holds a checkout and its caches: SANDBOX_VOLUME_SIZE. A disk a
// sandbox with no project owns holds what that sandbox's working directory always
// held, only now on a disk that outlives the pod — so it is the working
// directory's size, SANDBOX_WORKDIR_SIZE, and small.
func diskSize(m Sandbox) string {
	switch {
	case m.Volume == "":
		return ""
	case m.Project == "":
		return workdirSize()
	}
	return env.Or("SANDBOX_VOLUME_SIZE", "20Gi")
}

// workdirSize is how much a scratch sandbox's working directory may hold: the
// bound on its emptyDir (podSpec), and the size of its own disk when it has one.
func workdirSize() string { return env.Or("SANDBOX_WORKDIR_SIZE", "2Gi") }

// gib is a disk size in whole GiB, rounded UP: a gibibyte begun is a gibibyte
// held. It reads the size the way Kubernetes does, so "512Mi" is one GiB and not
// a parse failure. A size that cannot be read prices as the project default rather
// than as nothing — an unreadable size is still a disk.
func gib(size string) int64 {
	q, err := resource.ParseQuantity(strings.TrimSpace(size))
	if err != nil || q.Sign() <= 0 {
		return 20
	}
	return (q.Value() + 1<<30 - 1) >> 30
}

// storing reports whether this lease is holding disk somebody has to pay for.
//
// It is DELIBERATELY not holding(): runtime stops when the pod does and storage
// does not. A parked lease holds no pod and its whole remaining cost is this — so
// a parked lease that billed nothing at all, which is what shipping park without
// this meter did, hands out a volume for free.
func storing(m Sandbox) bool { return m.Volume != "" && m.Status != statusError }

// storageRate is the price of one GiB-hour, the authority's if it has one.
func storageRate(ctx context.Context) int64 {
	return cloud.RateMicros(ctx, cloud.RuntimeProduct, storageMeter, StorageGiBHourMicros)
}

// stored is the storage meter's row: the whole volume's price per hour, on its
// own watermark so it does not fight the runtime meter's.
func stored(ctx context.Context, m Sandbox) cloud.Running {
	return cloud.Running{
		ID:        m.ID + diskSuffix,
		Payer:     m.Payer,
		Project:   m.Project,
		Model:     "sandbox/" + m.Class + "/disk",
		MeteredAt: m.DiskMeteredAt,
		Rate:      storageRate(ctx) * gib(diskSize(m)),
	}
}

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
	ns, err := principal.OrgNamespace(m.Org, "")
	if err != nil {
		s.Log.Warn("the lease names no namespace, so it can be neither billed nor retired",
			"sandbox", m.ID, "org", m.Org, "err", err)
		return
	}
	// The claim, the two destructions and the ship are ONE settlement, so the
	// destructions ride inside it: RuntimeSweep's contract is claim → make durable →
	// emit, and at the end of a lease what has to be made durable is the retirement.
	// A lease that never came up is billed for nothing — the rule the lease fee
	// already follows — so it is retired without a claim. Both meters answer that
	// the same way, through spans.
	shipped := false
	_, err = cloud.RuntimeSweep(ctx, spans(ctx, m), time.Now().Unix(),
		advance(st, m.Org),
		func() (bool, error) { shipped = true; return settle(s, ctx, st, ns, m, purge) },
		s.State.debit,
	)
	if err != nil {
		s.Log.Warn("the lease was retired and its tail not billed (nothing is durable, so the next owner bills it)",
			"sandbox", m.ID, "org", m.Org, "err", err)
		return
	}
	// A lease that owed nothing still has to be retired, and RuntimeSweep does not
	// ship a pass that claimed nothing — rightly, since an idle org owes no round
	// trip. So the settlement is taken directly whenever the sweep did not take it:
	// a row that never came up, and a row retired inside the second its watermarks
	// last moved, which claims nothing and was otherwise left standing, pod and all.
	if !shipped {
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
	// the top. On a deployment of one writer it is a lock and an HRW over the live
	// member set; on more, it also reads the org's lease from the object store, at
	// most once a membership refresh and never twice in a row against a store that
	// did not answer (org.Durable.Owned), so it is cheap to ask per row. It still
	// answers during an object-store outage, on the store's last answer while this
	// pod is live, which is exactly when a replica must go on ending its own expired
	// leases.
	if !s.State.stores.Owned(ns) {
		return false, fmt.Errorf("sandbox: %s is no longer this replica's to end", ns)
	}
	if serr := s.State.rt.stop(ctx, m); serr != nil {
		s.Log.Warn("stop sandbox", "id", m.ID, "err", serr)
	}
	// A disk with no project is the lease's OWN (diskName): nothing but this row
	// names it, so once the row is gone nothing could, and it goes with it. A
	// project's disk outlives every lease and goes only when purge is asked for.
	if m.Volume != "" && (purge || m.Project == "") {
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
		rows := make([]cloud.Running, 0, len(held)*2)
		for _, m := range held {
			rows = append(rows, spans(ctx, m)...)
		}
		org := ns.ID()
		_, err = cloud.RuntimeSweep(ctx, rows, time.Now().Unix(),
			advance(st, org),
			func() (bool, error) { return s.State.stores.Sync(ns) },
			s.State.debit,
		)
		if err != nil {
			s.Log.Warn("runtime meter: the org's spans were claimed and not billed (the watermarks are not durable, so the next owner bills them)",
				"namespace", ns, "err", err)
		}
	})
}
