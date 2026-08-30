package validator

// meter.go — who pays for a node.
//
// Claiming a slot materializes a LuxNetwork CR: a validator node on our cluster,
// with 200Gi of storage attached, running until somebody deletes it. That is the
// longest-lived thing a tenant can create anywhere in this fleet, and the surface
// declared cloud.Free — so nothing authorized it and nothing recorded it.
//
// AN NFT IS NOT A PAYMENT. Ownership of a Validator-tier token is what makes a
// caller ELIGIBLE, and it bounds the total to the token supply, which is why this
// was a revenue leak rather than a denial-of-service one. But eligibility is not
// settlement: the node runs on capacity somebody rents by the hour whether or not
// a token changed hands years ago, and the ledger had no record that it existed.
//
// THE UNIT IS THE MATERIALIZATION, not the claim. A slot claimed against a
// cluster that is not resolved answers "node_pending" and starts nothing — no CR,
// no storage, no cost — so it is not billed. Neither is a REPROVISION: that
// re-applies the CR for a slot this org already holds, which is the same node
// being reconciled rather than a second one.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// feeEnv is the operator knob: VALIDATORS_FEE_CENTS_NODE, or VALIDATORS_FEE_CENTS
// for every billed act on this surface.
const feeEnv = "VALIDATORS_FEE_CENTS"

// node is the billed act: one validator node materialized on the cluster.
const node = "node"

// fee is the platform's ordinary provision fee. This IS a provision — a durable
// workload with storage attached — so it is priced like every other create in the
// fleet rather than invented here.
func fee() int64 { return cloud.ResourceFeeCents(feeEnv, node) }

// afford authorizes one node BEFORE the CR is applied, so a caller who cannot
// cover it never gets a pod scheduled or a volume bound.
func afford(s *cloud.Service[state], ctx context.Context) (*cloud.Charge, error) {
	return s.Bill.Reserve(ctx, cloud.PayerOf(ctx), node, fee())
}

// charge debits one node, after the CR has actually been applied. A claim that
// stayed pending — no cluster, or a failed apply — started nothing and bills
// nothing.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one: it names an ACT. Keyed on the slot,
	// a node torn down and rebuilt would be free the second time.
	ch.Debit(metering.Usage{Model: node, AmountCents: fee()})
}
