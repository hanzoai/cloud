package billing

// statement.go serves the four statement reads of /v1/billing — the billing
// account, its roster, the org's payouts and one page of the caller's ledger.
//
// Each is a relay onto the process that owns the merchant store (peer.go). The
// org rides the caller; the SUBJECT is resolved here, by the one rule that turns
// a request into a wallet, and never read from the query. That is the pin eight
// separate middleware links used to apply per route, now a property of the
// types: the plane input has a subject field and this file is the only thing
// that fills it.
//
// THREE OF THE FOUR ANSWER A BARE ARRAY, which is why their Out types are slice
// types rather than structs. The array is the wire these addresses have always
// had, and an envelope would be a shape change dressed as a tidy-up.

import (
	"context"
	"strconv"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountStatement registers the statement reads. Called from routes.
func mountStatement(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/billing/accounts", o.accounts)
	zip.Get(zapp, "/v1/billing/accounts/:id/members", o.accountMembers)
	zip.Get(zapp, "/v1/billing/payouts", o.payouts)
	zip.Get(zapp, "/v1/billing/transactions", o.transactions)
}

// accountsView is the caller's billing accounts — one, because an org IS its
// billing account here.
//
// The name carries a View suffix because the bare noun is already a published
// schema on this app: usage_accounts.go answers /v1/billing/usage/accounts with
// a per-provider usage breakdown it calls `accounts`. Two shapes under one
// schema name is what openapi.Compose refuses, and the incumbent is the one that
// is already published, so this is the one that moves.
type accountsView []plane.BillingAccount

// members is one billing account's roster.
type members []plane.Holder

// payouts is the org's outbound payouts.
type payouts []plane.Payout

// Answers the caller's billing accounts: the org itself, its currency, when it
// was opened, and the caller's own standing in it.
//
// The standing is the caller's, resolved from the validated principal here and
// sent to the store rather than looked up there — the membership roster is IAM's
// and commerce keeps none, so a callee that answered "what role is this" would
// be inventing it. An anonymous read gets the account with no role rather than
// an implied membership.
//
// Scoped to the caller's own org, which is the whole tenancy story: there is no
// org field on the wire and none on the input.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) accounts(ctx context.Context, _ *noInput) (*accountsView, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	out, err := ask(ctx, org, "accounts", func(ctx context.Context) (*plane.Accounts, error) {
		return commercepeer.BillingAccounts(ctx, callerIn(ctx))
	})
	if err != nil {
		return nil, err
	}
	rows := accountsView(out.Rows)
	if rows == nil {
		rows = accountsView{}
	}
	return &rows, nil
}

// Answers one billing account's roster.
//
// commerce stores no roster — that is IAM's — so the only member it can name is
// the caller, and that is what comes back. What it does enforce is that the
// account named in the path is the caller's own: a foreign id is 403, not an
// empty list, because "no members" and "not your account" are different answers.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) accountMembers(ctx context.Context, in *accountRef) (*members, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	c := callerIn(ctx)
	out, err := ask(ctx, org, "account members", func(ctx context.Context) (*plane.Holders, error) {
		return commercepeer.BillingAccountMembers(ctx, &plane.HoldersIn{
			Account: in.ID, Subject: c.Subject, Email: c.Email, Role: c.Role,
		})
	})
	if err != nil {
		return nil, err
	}
	rows := members(out.Rows)
	if rows == nil {
		rows = members{}
	}
	return &rows, nil
}

// accountRef names the billing account a roster is asked for.
type accountRef struct {
	// ID is the billing account id, which for this store is the org's own id.
	ID string `json:"id"`
}

// Answers the org's outbound payouts, newest first — amount, destination,
// status, and the failure reason where one applies.
//
// A payout is ORG-scoped rather than subject-scoped, so there is nothing to pin
// beyond the tenant the caller already is, and no query can widen it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) payouts(ctx context.Context, _ *noInput) (*payouts, error) {
	org, err := principalOrg(ctx)
	if err != nil {
		return nil, err
	}
	out, err := ask(ctx, org, "payouts", func(ctx context.Context) (*plane.Payouts, error) {
		return commercepeer.BillingPayouts(ctx)
	})
	if err != nil {
		return nil, err
	}
	rows := payouts(out.Rows)
	if rows == nil {
		rows = payouts{}
	}
	return &rows, nil
}

// ledgerPage narrows the transactions read: which currency, and how much of the
// history. There is deliberately no user field — the wallet is the caller's own,
// resolved server-side — so a query that named one would be naming a wallet it
// does not hold.
type ledgerPage struct {
	// Currency filters to one currency. Empty reads every currency.
	Currency string `json:"currency,omitempty"`
	// Limit is the page size; absent or non-positive takes the default 100.
	Limit string `json:"limit,omitempty"`
	// Offset is how far into the history the page starts.
	Offset string `json:"offset,omitempty"`
}

// Answers one page of the caller's own ledger, newest first: what moved, how
// much, when, and what it was tagged with.
//
// `count` is the size of the WHOLE history rather than of the page, which is how
// a reader knows there is more to ask for, and `user` echoes the wallet the page
// was read for — the same subject the spend gate debits, so a customer can see
// which account answered rather than guessing from their own token.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) transactions(ctx context.Context, in *ledgerPage) (*plane.Transactions, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "transactions", func(ctx context.Context) (*plane.Transactions, error) {
		return commercepeer.BillingTransactions(ctx, &plane.TransactionsIn{
			Subject:  subject,
			Currency: in.Currency,
			Limit:    atoiOr(in.Limit, 0),
			Offset:   atoiOr(in.Offset, 0),
		})
	})
}

// atoiOr reads a numeric query value, falling back to def for anything that is
// not a number. A typo widens nothing: the core clamps a non-positive limit to
// its own default and a negative offset to the start.
func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// callerIn is the identity the door already validated, packed for the store.
//
// It is not a tenant claim: there is no org on it, and the subject names a
// wallet inside the org the caller's own principal pinned. It travels because
// commerce cannot resolve it — the roster is IAM's and the request is this
// app's.
//
// The role is derived from the two authority bits the identity carries rather
// than from a role list, because a role list is not among the nine fields an
// internal call forwards.
func callerIn(ctx context.Context) *plane.CallerIn {
	c, ok := cloud.Request(ctx)
	if !ok {
		return &plane.CallerIn{}
	}
	role := "member"
	if c.IsOrgAdmin() || c.IsAdmin() {
		role = "admin"
	}
	return &plane.CallerIn{Subject: c.User(), Email: c.UserEmail(), Role: role}
}
