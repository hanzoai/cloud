package compliance

// meter.go — who pays for an identity check.
//
// Everything else on this surface is the org's own rows: subjects, cases, watch
// hits, a reviewer's decision. One act is not. Starting a verification opens an
// INQUIRY at Persona, Onfido or Stripe, and the vendor bills for it whether or not
// the person ever finishes it — dollars per inquiry, more than any other single
// call this platform makes on a tenant's word.
//
// AND IT IS THE DEPLOYMENT'S KEY, which is the whole reason it needed a meter.
// apps/idv resolves CLOUD_IDV_KEY_REF from process env once at Mount, so the
// account is Hanzo's and the invoice is Hanzo's, for whoever happens to call. Had
// it been the org's own credential — the orgs/<org>/… shape apps/notify uses to
// send SMS through the tenant's own Twilio — there would be nothing here to bill,
// because the tenant would already be paying their own vendor. Scope, not
// location, is what decides.
//
// A verification is ORG-SCOPED with no admin gate, and it should be: proving who
// your customer is, is the product. The gate is on the money, not on the endpoint.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// feeEnv is the operator knob: CLOUD_COMPLIANCE_FEE_CENTS_INQUIRY, or
// CLOUD_COMPLIANCE_FEE_CENTS for every billed act on this surface.
const feeEnv = "CLOUD_COMPLIANCE_FEE_CENTS"

// inquiry is the billed act: one identity verification opened at the provider.
const inquiry = "inquiry"

// fee is the platform's ordinary provision default, which is also the right order
// of magnitude here — an inquiry costs the platform dollars, not cents, so this is
// the one surface in the fleet where the $1.00 default is on the low side rather
// than absurdly high. Operators set the real number per deployment.
func fee() int64 { return cloud.ResourceFeeCents(feeEnv, inquiry) }

// afford authorizes one inquiry BEFORE the provider is asked, so a caller who
// cannot cover it never opens one on our account.
func afford(s *cloud.Service[state], ctx context.Context) (*cloud.Charge, error) {
	return s.Bill.Allow(ctx, cloud.PayerOf(ctx), inquiry, fee())
}

// charge debits one inquiry, after the provider has actually opened it. A start
// that errored opened nothing and is a 502 to the caller.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one: it names an ACT. Keyed on the
	// subject, re-verifying the same person a year later would be free.
	ch.Debit(metering.Usage{Model: inquiry, AmountCents: fee()})
}
