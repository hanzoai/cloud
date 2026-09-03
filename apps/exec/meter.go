package exec

// meter.go — who pays to run a snippet.
//
// Running code leases a sandbox and executes a program in it: a pod, a runtime
// and a slice of a node for as long as the program takes. The surface declared
// cloud.Free, so an authenticated caller at zero balance could run programs
// without limit and nothing anywhere recorded that it had happened.
//
// IT IS METERED HERE, NOT IN Run. The interpreter is exported because a second
// subsystem in this process runs snippets too — apps/functions invokes a customer
// function, which is this operation with a shorter lease — and it ALREADY gates
// and debits its own per-invoke fee around that call. A meter inside Run would
// charge that path twice for one execution. So the charge sits on THIS
// subsystem's own endpoint, where the callers are this subsystem's callers, and
// composition stays free: a subsystem that composes the interpreter prices its
// own product and is not silently taxed for reusing the implementation.
//
// The sandbox lease underneath is metered by apps/sandbox on its own terms; that
// is a different act with a different price, and it is deliberately not folded in
// here — one act, one charge, at the layer that owns it.

import (
	"context"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/metering"
)

// feeEnv is the operator knob for what one run costs: CODE_EXEC_FEE_CENTS_RUN, or
// CODE_EXEC_FEE_CENTS for every billed act on this surface.
const feeEnv = "CODE_EXEC_FEE_CENTS"

// run names the billed act: one program executed in one sandbox.
const runKind = "run"

// defaultFeeCents is one cent per run.
//
// A policy default sized like the compute it is — seconds of a pod on capacity we
// already hold — rather than the platform's $1.00 provision fee, which is sized
// for creating a database and would price a one-line snippet above the answer it
// produces. Operators move it with the knob above; 0 makes running free again,
// and un-gated with it.
const defaultFeeCents int64 = 1

// meter is the per-org gate and debit, bound once by Mount.
//
// Package-level and an atomic pointer because this subsystem has no service
// value to hang it off: its handlers are free functions and Mount builds no
// cloud.Base. It is the shape apps/websearch uses, for the same reason — a
// dependency every handler needs and none of them can be handed.
var meter atomic.Pointer[cloud.Meter]

// bindMeter installs the process-wide meter. A nil meter leaves the interpreter
// fully functional and unbilled — Meter's own contract is that an absent
// ledger allows.
func bindMeter(m *cloud.Meter) { meter.Store(m) }

func fee() int64 { return cloud.FeeCents(feeEnv, runKind, defaultFeeCents) }

// afford authorizes one run BEFORE a sandbox is leased, so a caller who cannot
// cover it is refused having spent nothing.
//
// It must be asked with the handler's OWN context. Run detaches to a background
// context as its first act (callCtx), because the program must outlive a client
// that hangs up — and a detached context carries the org and nothing else, so the
// payer has to be resolved on this side of that line.
func afford(ctx context.Context) (*cloud.Charge, error) {
	return meter.Load().Reserve(ctx, cloud.PayerOf(ctx), runKind, fee())
}

// charge debits one run, after the program has actually run. A lease that failed
// or a program that never started produced nothing and bills nothing.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one: it names an ACT, and running the
	// same snippet twice is two runs.
	ch.Debit(metering.Usage{Model: runKind, AmountCents: fee()})
}
