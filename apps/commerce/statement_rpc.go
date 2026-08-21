// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// statement_rpc.go — the customer's statement, over the internal plane: the
// billing account, its roster, the org's payouts and one page of a subject's
// ledger.
//
// All four rows live in commerce's per-tenant datastore, which has one owner, so
// the door that publishes them (apps/billing, at /v1/billing) asks this process
// by name. Each op is a thin adapter over a value-taking core the module exports
// — the query is written once, in the module, and this file only moves the
// answer onto the wire.
//
// TWO THINGS THE ADAPTER MUST DO, and they are the whole reason it is not a cast.
//
// A TIME BECOMES TEXT. A time.Time crosses this plane as an EMPTY struct: the
// layout is derived from the type and every field of time.Time is unexported, so
// reflection cannot read one and the value arrives as the zero instant with
// nothing reporting the loss. RFC3339Nano is what a time.Time marshals to, so
// text is both the shape that survives and the shape that renders identically.
//
// A DOMAIN TYPE BECOMES ITS TEXT. currency.Type and payout.Status are string
// kinds and marshal as their text, so the plane carries the text and the wire is
// unchanged — without every reader of the contract having to link commerce's
// models to read a currency code.

import (
	"context"
	"time"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposeStatement publishes the four statement reads. Mount calls it.
func exposeStatement() {
	zip.Post[plane.CallerIn, plane.Accounts](cloud.Plane(), "/billing/accounts", planeAccounts,
		zip.WithOperationID(plane.BillingAccounts),
		zip.WithSummary("The caller's billing accounts"))
	zip.Post[plane.HoldersIn, plane.Holders](cloud.Plane(), "/billing/account/members", planeMembers,
		zip.WithOperationID(plane.BillingAccountMembers),
		zip.WithSummary("The roster of one billing account"))
	zip.Post[struct{}, plane.Payouts](cloud.Plane(), "/billing/payouts", planePayouts,
		zip.WithOperationID(plane.BillingPayouts),
		zip.WithSummary("Outbound payouts for this org"))
	zip.Post[plane.TransactionsIn, plane.Transactions](cloud.Plane(), "/billing/transactions", planeTransactions,
		zip.WithOperationID(plane.BillingTransactions),
		zip.WithSummary("One page of a subject's ledger"))
}

// Lists the caller's billing accounts — one, because in commerce an org IS its
// billing account.
//
// The caller's own standing travels IN, which looks backwards until you ask who
// else could supply it: the membership roster is IAM's, not commerce's, so a
// callee that answered "what role does this person hold" would be inventing the
// answer. The door validated it; this reports it. The org is still the caller's
// and cannot be named, so the account described is always the caller's own.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeAccounts(ctx context.Context, in *plane.CallerIn) (*plane.Accounts, error) {
	org, err := payingOrg(ctx, "accounts")
	if err != nil {
		return nil, err
	}
	rows, aerr := commercebilling.ListAccounts(ctx, org, in.Subject, in.Role)
	if aerr != nil {
		return nil, zip.Errorf(502, "accounts: %v", aerr)
	}
	out := make([]plane.Account, 0, len(rows))
	for _, a := range rows {
		out = append(out, plane.Account{
			ID: a.Id, Name: a.Name, OrgID: a.OrgId, OrgName: a.OrgName,
			Currency: string(a.Currency), CreatedAt: stamp(a.CreatedAt), Role: a.Role,
		})
	}
	return &plane.Accounts{Rows: out}, nil
}

// Lists one billing account's roster.
//
// commerce stores no roster, so the only member it can name is the caller — and
// that is what it names. What it DOES enforce is that the account asked about is
// the caller's own: a foreign id is refused rather than answered empty, because
// an empty roster and somebody else's account are different facts and only one
// of them is a refusal.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeMembers(ctx context.Context, in *plane.HoldersIn) (*plane.Holders, error) {
	org, err := payingOrg(ctx, "account members")
	if err != nil {
		return nil, err
	}
	rows, merr := commercebilling.ListMembers(ctx, org, in.Account, in.Subject, in.Email, in.Role)
	if merr != nil {
		// The core's one error is the foreign-account refusal, and 403 is what
		// the door has always answered it with. It is not a 404: the caller named
		// an account, and saying "not yours" tells them nothing they did not
		// already know about their own org.
		return nil, zip.Errorf(403, "account members: %v", merr)
	}
	out := make([]plane.Member, 0, len(rows))
	for _, m := range rows {
		out = append(out, plane.Holder{
			ID: m.Id, UserID: m.UserId, Email: m.Email, Role: m.Role, AddedAt: stamp(m.AddedAt),
		})
	}
	return &plane.Holders{Rows: out}, nil
}

// Lists the org's outbound payouts, newest first.
//
// It takes NO input: the org comes from the caller and a payout is org-scoped
// rather than subject-scoped, so there is nothing else to name and one org can
// never list another's.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planePayouts(ctx context.Context, _ *struct{}) (*plane.Payouts, error) {
	org, err := payingOrg(ctx, "payouts")
	if err != nil {
		return nil, err
	}
	rows, perr := commercebilling.ListPayouts(ctx, org)
	if perr != nil {
		return nil, zip.Errorf(502, "payouts: %v", perr)
	}
	out := make([]plane.Payout, 0, len(rows))
	for _, p := range rows {
		row := plane.Payout{
			ID: p.Id, Amount: p.Amount,
			Currency: string(p.Currency), Status: string(p.Status),
			DestinationType: p.DestinationType, DestinationID: p.DestinationId,
			Created: stamp(p.Created), Description: p.Description,
			ProviderRef: p.ProviderRef, FailureCode: p.FailureCode,
			FailureMessage: p.FailureMessage, Metadata: p.Metadata,
		}
		if p.ArrivalDate != nil {
			row.ArrivalDate = stamp(*p.ArrivalDate)
		}
		out = append(out, row)
	}
	return &plane.Payouts{Rows: out}, nil
}

// Reads one page of a subject's ledger, newest first.
//
// The SUBJECT travels and the org does not, which is the tenancy rule this plane
// keeps everywhere: a subject is a wallet inside the caller's own org, so naming
// one can reach another account of that org and nothing beyond it. The door
// resolves it from the validated principal, so a query cannot widen the read.
//
// Count is the size of the whole history rather than of the page, because that
// difference is how a reader knows there is more to ask for.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTransactions(ctx context.Context, in *plane.TransactionsIn) (*plane.Transactions, error) {
	org, err := payingOrg(ctx, "transactions")
	if err != nil {
		return nil, err
	}
	page, terr := commercebilling.ListTransactions(ctx, org, in.Subject, in.Currency, in.Limit, in.Offset)
	if terr != nil {
		return nil, zip.Errorf(502, "transactions: %v", terr)
	}
	out := make([]plane.Transaction, 0, len(page.Transactions))
	for _, t := range page.Transactions {
		out = append(out, plane.Transaction{
			ID: t.Id, Type: t.Type, Amount: t.Amount, Currency: t.Currency,
			Tags: t.Tags, Notes: t.Notes, Metadata: t.Metadata,
			CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		})
	}
	return &plane.Transactions{Rows: out, Count: page.Count, User: page.User}, nil
}

// stamp renders a time the way a time.Time marshals itself, which is what makes
// the text on this plane and the text on the wire the same bytes. The ZERO
// instant renders too, rather than becoming an empty string: a shape that always
// carried a date must keep carrying one, and "0001-01-01T00:00:00Z" is the date
// it carried.
func stamp(t time.Time) string { return t.Format(time.RFC3339Nano) }
