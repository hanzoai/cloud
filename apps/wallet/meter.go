package wallet

// meter.go — who pays for a key that lives on the ring.
//
// Three acts here leave this process for the MPC cluster, and each one is real
// money. Creating a wallet under MPC, treasury or Safe custody runs a distributed
// CGGMP21 keygen across the ring — up to a minute of coordinated compute — and a
// Safe additionally DEPLOYS a contract, which is gas on a live chain. Signing runs
// a threshold round. Proposing a Safe transaction runs a second one. The surface
// declared cloud.Free, so a tenant could deploy contracts at the platform's
// expense with nothing authorized and nothing recorded.
//
// CUSTODY IS THE PREDICATE, and it is better than a deployment flag because it is
// a property of the ACT rather than of the environment. KindKMS is an in-process
// keygen against the embedded client — it buys nothing and stays free, which is
// also the default custody. The other three exist in state.custody only when the
// ring is configured, so "this act costs money" and "this act is possible" are
// already the same condition and no new env has to agree with anything.
//
// THE UNIT IS THE ROUND, not the wallet-month. A keygen, a signature and a
// proposal are each one bounded distributed computation this process starts and
// waits for, so each is one charge. Nothing here bills for a key at rest: this
// surface never learns that a key still exists, and inventing a rental it cannot
// observe would be a number nobody could reconcile.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/metering"
)

// feeEnv is the operator knob: CLOUD_WALLETS_FEE_CENTS_KEYGEN / _SIGN / _PROPOSE
// per act, CLOUD_WALLETS_FEE_CENTS for all three.
const feeEnv = "CLOUD_WALLETS_FEE_CENTS"

// The three billed acts, named once so the gate, the debit and the ledger row all
// say the same word.
const (
	keygen  = "keygen"
	signing = "sign"
	propose = "propose"
)

// price is what each act costs by default, in cents.
//
// A keygen is the platform's ordinary provision fee: it creates a durable
// resource, and under Safe custody it also deploys a contract with real gas
// behind it, which is the most expensive single act this binary performs on a
// tenant's word. A signing round and a proposal are bounded coordinations over an
// existing key, priced like the compute they are. Operators move any of them with
// the knobs above; 0 makes an act free again, and un-gated with it.
var price = map[string]int64{
	keygen:  cloud.DefaultResourceFeeCents,
	signing: 1,
	propose: 1,
}

func fee(act string) int64 { return cloud.FeeCents(feeEnv, act, price[act]) }

// paid reports whether an act on this custody leaves the process. KindKMS is
// in-process and free; everything else is a round on the ring.
func paid(k Kind) bool { return k != KindKMS }

// afford authorizes one ring act BEFORE it is started, so a caller who cannot
// cover it is refused having spent nothing — no keygen, no deploy, no gas.
func (o ops) afford(ctx context.Context, k Kind, act string) (*cloud.Charge, error) {
	if !paid(k) {
		return nil, nil
	}
	return o.s.Bill.Reserve(ctx, cloud.PayerOf(ctx), act, fee(act))
}

// charge debits one ring act, after it has actually completed. A round that
// errored produced no key and no signature, and every caller returns the ring's
// own error instead of reaching this.
func (o ops) charge(ch *cloud.Charge, act string) {
	// Ref is left unset so the meter mints one: it names an ACT, and two signing
	// rounds over the same wallet are two acts. Note the ring itself dedups a
	// REPEATED digest, so an idempotent retry never reaches this line.
	ch.Debit(metering.Usage{Model: act, AmountCents: fee(act)})
}
