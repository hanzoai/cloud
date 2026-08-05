package flow

// billing.go — what a workflow run costs, and the gate that refuses one the
// caller cannot pay for.
//
// THE GAP THIS CLOSES. This surface declared cloud.Metered, which means one
// thing and only one: "the charge for this surface is owned by a meter
// DOWNSTREAM of the edge" (cloud/price.go). The edge therefore adds nothing —
// and there was no meter downstream either, so every workflow this plane ran was
// free, in a way no test could see. The price was declared, standing was
// required, and nothing ever debited. A surface that promises a meter and keeps
// none is worse than one that declares Free, because it reads as paid.
//
// WHAT IS BILLED, AND WHY ONLY THAT. A RUN is the unit. Running a workflow
// executes its whole component graph upstream — and those components call model
// providers, so the run spends real money on a synchronous path that blocks for
// as long as the graph takes (timeoutRun, not timeout). Everything else here is
// bookkeeping against rows we already hold: listing workflows, fetching one,
// creating or deleting a definition, reading past builds. So the meter sits on
// POST /v1/flow/runs and nowhere else.
//
// ONE KNOB, TWO USES. The gate authorizes exactly the amount the meter debits,
// read from the same ResourceFeeCents call, so the number a caller is refused
// for and the number they are charged cannot drift. A deployment that prices a
// run at 0 is un-gated exactly as it is un-billed — ResourceMeter's own
// costCents<=0 short-circuit — which is the honest reading of "this operator
// gives runs away".

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
)

const (
	// feeEnvPrefix prices one workflow run per deployment (0 ⇒ free).
	// CLOUD_FLOW_FEE_CENTS_RUN overrides CLOUD_FLOW_FEE_CENTS overrides the
	// platform default (cloud.DefaultResourceFeeCents).
	feeEnvPrefix = "CLOUD_FLOW_FEE_CENTS"
	// meterKind is the billed unit inside this product — the ledger's per-item
	// attribution, so a run is distinguishable from any later footprint charge.
	meterKind = "run"
)

// gate refuses a run the caller's balance cannot cover, BEFORE any upstream byte
// is sent — an unfunded org must not start a graph whose components bill a model
// provider.
//
// Off the HTTP path (a CLI LocalInvoke, an MCP tools/call) there is no request
// and therefore no payer to charge, so there is nothing to gate. That is the
// same silence meter keeps below, for the same reason, and it is a KNOWN hole in
// the edge rather than a decision made here: cloud sets no zip authorizer, so no
// transport but HTTP reaches a money gate anywhere in the fleet.
//
// The refusal is cloud.Denied, not DenyResource: a typed op cannot write a
// response body — zip stamps the op's own status over a hand-written one — so
// the money wire's status and sentence travel as an error instead.
func (o ops) gate(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil
	}
	project, validated := principal.ValidatedProject(c)
	fee := cloud.ResourceFeeCents(feeEnvPrefix, meterKind)
	if err := o.s.Bill.Gate(c.Context(), principal.Payer(c), project, validated, meterKind, fee); err != nil {
		return cloud.Denied(err)
	}
	return nil
}

// meter debits the one run this call performed. It runs only after the upstream
// returned a result: work that never ran is work nobody owes for.
//
// The debit is fire-and-forget on a background context (ResourceMeter.Meter) —
// the run already happened, so the charge must never block the reply nor be
// cancelled by a client disconnect.
func (o ops) meter(ctx context.Context) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return
	}
	o.s.Bill.Meter(principal.Payer(c), principal.Project(c), meterKind,
		cloud.ResourceFeeCents(feeEnvPrefix, meterKind), c.RequestID(), cloud.ClientIP(c))
}
