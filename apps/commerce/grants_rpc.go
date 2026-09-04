// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// grants_rpc.go — a subject's credit grants and the two summaries of them, over
// the internal plane.
//
// Three projections of ONE entry list, which is why they are one file and why
// each goes through the module's own core rather than deriving the next from the
// previous. The grants are the LEDGER — spent, expired and voided rows included,
// because a grant list that hides its spent rows cannot be reconciled against a
// burn-down. The balance is what is spendable RIGHT NOW, so exactly those hidden
// rows contribute nothing to it. And the breakdown is that balance split by tag,
// which is the question a chat surface asks before it spends any: trial credit
// told apart from bought credit.
//
// A reader that derived the balance from the grants would need its own copy of
// the burn order, and a caller that only wants to know what is spendable should
// never have to reproduce it to find out.

import (
	"context"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// exposeGrants publishes the three credit reads. Mount calls it.
func exposeGrants() {
	zip.Post[client.SubjectIn, client.CreditGrants](cloud.Plane(), "/billing/credits", planeCredits,
		zip.WithOperationID(client.BillingCredits),
		zip.WithSummary("A subject's credit grants"))
	zip.Post[client.SubjectIn, client.CreditBalance](cloud.Plane(), "/billing/credit/balance", planeCreditBalance,
		zip.WithOperationID(client.BillingCreditBalance),
		zip.WithSummary("Spendable credit, per currency"))
	zip.Post[client.SubjectIn, client.CreditBreakdown](cloud.Plane(), "/billing/credit/breakdown", planeCreditBreakdown,
		zip.WithOperationID(client.BillingCreditBreakdown),
		zip.WithSummary("Spendable credit, split by grant tag"))
}

// Lists a subject's credit grants — every one of them, including the spent, the
// lapsed and the voided.
//
// The SUBJECT is required rather than optional-like-a-filter, and that is a
// tenancy property rather than a validation nicety: dropping it does not narrow
// the answer, it WIDENS it to every subject in the org, so one tenant's customers
// would read each other's grants. The endpoint resolves it from the validated
// principal, so a query cannot supply one.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCredits(ctx context.Context, in *client.SubjectIn) (*client.CreditGrants, error) {
	org, err := orgOf(ctx, "credits")
	if err != nil {
		return nil, err
	}
	rows, gerr := commercebilling.ListCreditGrants(ctx, org, in.Subject)
	if gerr != nil {
		return nil, zip.Errorf(502, "credits: %v", gerr)
	}
	out := make([]client.CreditGrant, 0, len(rows))
	for _, g := range rows {
		row := client.CreditGrant{
			ID: g.ID, UserID: g.UserID, Name: g.Name,
			AmountCents: g.AmountCents, RemainingCents: g.RemainingCents,
			Currency: string(g.Currency), Priority: g.Priority,
			EffectiveAt: stamp(g.EffectiveAt), Tags: g.Tags,
			Voided: g.Voided, Active: g.Active, CreatedAt: stamp(g.CreatedAt),
		}
		if g.ExpiresAt != nil {
			row.ExpiresAt = stamp(*g.ExpiresAt)
		}
		out = append(out, row)
	}
	return &client.CreditGrants{Rows: out, Count: len(out)}, nil
}

// Sums a subject's ACTIVE grants per currency — what is spendable right now,
// which is why voided, exhausted and lapsed grants contribute nothing.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCreditBalance(ctx context.Context, in *client.SubjectIn) (*client.CreditBalance, error) {
	org, err := orgOf(ctx, "credit balance")
	if err != nil {
		return nil, err
	}
	bal, berr := commercebilling.ReadCreditBalance(ctx, org, in.Subject)
	if berr != nil {
		return nil, zip.Errorf(502, "credit balance: %v", berr)
	}
	rows := make([]client.CreditEntry, 0, len(bal.Balances))
	for _, e := range bal.Balances {
		rows = append(rows, client.CreditEntry{Currency: string(e.Currency), Available: e.Available})
	}
	return &client.CreditBalance{UserID: bal.UserID, Balances: rows}, nil
}

// Splits that same balance by grant tag, with the earliest expiry under each,
// and the total across all of them.
//
// The tags cross as a SLICE and are published as an object: a map has no fixed
// layout, so it cannot cross this plane at all, and the rendering is done once in
// the contract rather than once per endpoint (client.CreditBreakdown.MarshalJSON).
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeCreditBreakdown(ctx context.Context, in *client.SubjectIn) (*client.CreditBreakdown, error) {
	org, err := orgOf(ctx, "credit breakdown")
	if err != nil {
		return nil, err
	}
	b, berr := commercebilling.ReadCreditBreakdown(ctx, org, in.Subject)
	if berr != nil {
		return nil, zip.Errorf(502, "credit breakdown: %v", berr)
	}
	tags := make([]client.CreditTag, 0, len(b.Breakdown))
	for name, t := range b.Breakdown {
		if t == nil {
			continue
		}
		row := client.CreditTag{Tag: name, Cents: t.Cents}
		if t.ExpiresAt != nil {
			row.ExpiresAt = stamp(*t.ExpiresAt)
		}
		tags = append(tags, row)
	}
	return &client.CreditBreakdown{
		UserID: b.UserID,
		Tags:   tags,
		Total:  client.CreditTotal{Cents: b.Total.Cents},
	}, nil
}
