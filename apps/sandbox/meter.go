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
// span has to be charged BEFORE the delete or it is lost. Both delete sites call
// [bill]: End (a caller ended it) and reap's end (the reaper did).

import (
	"context"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
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
func running(m Sandbox) cloud.Running {
	return cloud.Running{
		ID:        m.ID,
		Payer:     m.Payer,
		Project:   m.Project,
		Model:     "sandbox/" + m.Class,
		MeteredAt: m.MeteredAt,
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

// bill charges one lease for the time since it was last billed, and is the ONE
// place this package moves runtime money. Both the sweep and the two end-of-lease
// paths go through it, so there is one arithmetic, one act name and one debit
// shape whatever ended the lease.
//
// A failure is logged and dropped rather than returned: the lease has already been
// taken or already ended, and a debit that could not be recorded must never turn a
// working sandbox into a refusal. It is the same posture MeterUsage takes.
// It reads the rate ITSELF rather than being handed one, so the three call sites
// cannot disagree about which class they are pricing — the sweep holds many
// classes at once, and one rate read per tick was one rate for all of them.
func bill(s *Service, ctx context.Context, st *Store, m Sandbox) {
	if !holding(m) {
		return
	}
	_, err := cloud.RuntimeCharge(ctx, running(m), time.Now().Unix(), rate(ctx, m.Class),
		func(ctx context.Context, id string, was, now int64) (bool, error) {
			return st.Advance(ctx, m.Org, id, was, now)
		},
		func(payer string, u metering.Usage) { s.Bill.MeterUsage(payer, "sandbox", u) },
	)
	if err != nil {
		s.Log.Warn("runtime meter: the watermark would not move (lease held, not billed)",
			"sandbox", m.ID, "org", m.Org, "err", err)
	}
}

// meterRuntime bills every lease every org is holding, for the span since the last
// tick. reap's ticker fires it; the amount is (now − watermark), so the cadence
// decides only how often money moves and never how much.
//
// It reads Held and NOT List, because List is LIMIT 200 — right for a page, and for
// a set money is computed over it means every lease past row 200 is free.
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
		held, err := st.Held(ctx, ns.ID())
		if err != nil {
			s.Log.Warn("runtime meter: list held leases", "namespace", ns, "err", err)
			return
		}
		for _, m := range held {
			bill(s, ctx, st, m)
		}
	})
}
