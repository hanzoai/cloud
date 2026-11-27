package knowledge

// meter.go — who pays for a connector run.
//
// Most of this subsystem is storage and search over the org's own rows, and that
// stays free. One act is not: a long-tail connector's pull is a JavaScript
// connector EXECUTED in auto's runtime (sync_provider.go, over the plane). That is
// the same capacity apps/auto owns and correctly prices; reaching it from another
// app does not make it cheaper, it only makes the charge disappear. A tenant could
// run connectors all day through /v1/knowledge and never appear on auto's ledger.
//
// THE CHARGE BELONGS TO THE CALLER, NOT THE ADDRESS. This is the lesson the
// vendor-credential guard could not have caught: there is no vendor here and no
// key that says "paid", only a service name that resolves inside the cluster. The
// price is a property of the WORK, so it applies wherever the work is asked for.
//
// GITHUB IS FREE, deliberately: that connector is native Go in this process
// (sync.go's syncGitHub) and starts no pod. So the fee is not "a sync" — it is a
// PIECE RUN, and only the providers whose pull is a provider pay it.

import (
	"context"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
)

// feeEnv is the operator knob: CLOUD_KB_FEE_CENTS_PIECE, or CLOUD_KB_FEE_CENTS
// for every billed act on this surface.
const feeEnv = "CLOUD_KB_FEE_CENTS"

// provider is the billed act: one connector provider executed on the engine's pods.
const provider = "provider"

// defaultFeeCents is one cent per provider run.
//
// A policy default sized like the compute it is — one bounded JS execution on a
// pod we already hold — rather than the platform's $1.00 provision fee, which is
// sized for creating a database. Operators move it with the knob above; 0 makes a
// provider run free again, and un-gated with it.
const defaultFeeCents int64 = 1

func fee() int64 { return cloud.FeeCents(feeEnv, provider, defaultFeeCents) }

// afford authorizes one provider run BEFORE the engine is asked, so a caller who
// cannot cover it never occupies a pod.
func afford(s *cloud.Service[state], ctx context.Context) (*cloud.Charge, error) {
	return s.Bill.Reserve(ctx, cloud.PayerOf(ctx), provider, fee())
}

// charge debits one provider run, after the runtime has answered. A run that
// errored — auto unreachable, or no auto in this deployment — bills nothing.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one: it names an ACT, and re-syncing a
	// connector is a second run. Keyed on the provider, only the first would bill.
	ch.Debit(meter.Usage{Model: provider, AmountCents: fee()})
}
