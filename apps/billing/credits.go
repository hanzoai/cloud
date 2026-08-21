package billing

// credits.go serves the three credit reads of /v1/billing — the grant list and
// its two summaries.
//
// They are three projections of ONE entry list, relayed from the process that
// owns it. The subject is resolved here and never read from the query, which is
// what makes the read the caller's own: an unpinned subject on this family is
// not a widened filter, it is one customer reading another's grants.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountCredits registers the credit reads. Called from routes.
func mountCredits(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/billing/credits", o.credits)
	zip.Get(zapp, "/v1/billing/credit-balance", o.creditBalance)
	zip.Get(zapp, "/v1/billing/credit-balance/breakdown", o.creditBreakdown)
}

// Lists the caller's credit grants — every one of them, spent and lapsed and
// voided included.
//
// That is deliberate and it is what makes the list useful: a grant list is a
// LEDGER, and one that hid its spent rows could not be reconciled against a
// burn-down. What is spendable right now is the sibling read, /v1/billing/
// credit-balance, and the two are different questions.
//
// Scoped to the caller's own wallet, resolved server-side.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) credits(ctx context.Context, _ *noInput) (*plane.CreditGrants, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "credits", func(ctx context.Context) (*plane.CreditGrants, error) {
		return commercepeer.BillingCredits(ctx, &plane.SubjectIn{Subject: subject})
	})
}

// Answers what the caller can spend right now, one entry per currency.
//
// Only ACTIVE grants count: a voided, exhausted or lapsed grant contributes
// nothing, which is why this number can be smaller than the grant list suggests
// and why the two reads exist separately. It is credit, not prepaid balance —
// /v1/billing/balance is the wallet, and the two are added by the gate, never by
// a reader.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) creditBalance(ctx context.Context, _ *noInput) (*plane.CreditBalance, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "credit balance", func(ctx context.Context) (*plane.CreditBalance, error) {
		return commercepeer.BillingCreditBalance(ctx, &plane.SubjectIn{Subject: subject})
	})
}

// Answers that same spendable credit split by grant tag, with the earliest
// expiry under each and the total across all of them.
//
// The split is the point: it is how trial credit is told apart from bought
// credit, which is what a surface asks before it decides whether to spend any.
// An unregistered address answers 404 and a caller reads that as "no credit", so
// this being served is the difference between a customer with a trial grant
// being offered their trial and being told they have none.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) creditBreakdown(ctx context.Context, _ *noInput) (*plane.CreditBreakdown, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "credit breakdown", func(ctx context.Context) (*plane.CreditBreakdown, error) {
		return commercepeer.BillingCreditBreakdown(ctx, &plane.SubjectIn{Subject: subject})
	})
}
