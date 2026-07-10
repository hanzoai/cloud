// Package allotment grants a plan's recurring monthly included-usage credit
// to a tenant's prepaid balance.
//
// Design (decomplected from the rest of billing):
//
//   - The included monthly allotment is granted as an ordinary, tag-scoped,
//     expiring Deposit transaction — the SAME mechanism as billing/credit's
//     welcome credit. It therefore flows through TallyTransactions →
//     balance → available with NO new balance-computation path, and the
//     Hanzo gateway's prepaid balance gate (available > 0) honors it
//     transparently. Usage withdrawals burn the deposit down; overage draws
//     down purchased balance / is billed via the payment processor.
//
//   - Non-accumulating: each grant expires at the end of its UTC month, so an
//     unused remainder does not roll over. TallyTransactions already excludes
//     expired deposits, so expiry needs no extra gate logic.
//
//   - Idempotent per (user, period): the deposit is tagged with a
//     period-scoped tag ("included-credit:YYYY-MM"). Re-running the grant for
//     the same user in the same month is a safe no-op, which makes the monthly
//     scheduler and any manual re-trigger naturally exactly-once.
//
// The dollar amount per plan is catalog-derived (see billing.IncludedMonthlyCents,
// sourced from @hanzo/plans limits.includedCreditUsd). This package never
// hardcodes plan economics — callers pass the resolved cents in.
package allotment

import (
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// TagPrefix is the stable prefix for monthly-allotment deposit tags. The full
// tag is TagPrefix + ":" + period (e.g. "included-credit:2026-06").
const TagPrefix = "included-credit"

// Kind is the destination kind for IAM-bound balance transactions.
const Kind = "iam-user"

// Period returns the canonical period key for a time, in UTC ("YYYY-MM").
func Period(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// Tag returns the period-scoped grant tag for a given time.
func Tag(t time.Time) string {
	return TagPrefix + ":" + Period(t)
}

// PeriodEnd returns the first instant of the month AFTER t's month (UTC). A
// deposit with this ExpiresAt is fully available for the whole of t's month
// and is excluded from balance the moment the next month begins.
func PeriodEnd(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
}

// Result describes the outcome of a grant attempt.
type Result struct {
	Granted       bool   `json:"granted"`
	Reason        string `json:"reason,omitempty"`
	TransactionId string `json:"transactionId,omitempty"`
	AmountCents   int64  `json:"amountCents"`
	Period        string `json:"period"`
	Tag           string `json:"tag"`
}

// Grant idempotently grants `cents` of included monthly usage credit to `user`
// for the UTC month containing `at`. It is a no-op (Granted=false) when:
//   - cents <= 0 (plan has no included allotment), or
//   - a grant for the same (user, period) already exists.
//
// The check-and-create runs inside a datastore transaction so concurrent
// schedulers cannot double-grant. `test` marks the deposit test-only (mirrors
// the org.Live convention used elsewhere in billing).
func Grant(db *datastore.Datastore, user, plan string, cents int64, at time.Time, test bool) (Result, error) {
	period := Period(at)
	tag := Tag(at)
	res := Result{AmountCents: cents, Period: period, Tag: tag}

	if cents <= 0 {
		res.Reason = "no_included_allotment"
		return res, nil
	}

	var created *transaction.Transaction
	err := db.RunInTransaction(func(txDb *datastore.Datastore) error {
		rootKey := txDb.NewKey("synckey", "", 1, nil)

		// Already granted for this (user, period)? — no-op.
		existing := make([]*transaction.Transaction, 0)
		q := transaction.Query(txDb).Ancestor(rootKey).
			Filter("DestinationId=", user).
			Filter("Tags=", tag)
		if _, err := q.Limit(1).GetAll(&existing); err == nil && len(existing) > 0 {
			created = nil
			return nil
		}

		trans := transaction.New(txDb)
		trans.Type = transaction.Deposit
		trans.DestinationId = user
		trans.DestinationKind = Kind
		trans.Currency = "usd"
		trans.Amount = currency.Cents(cents)
		trans.Notes = fmt.Sprintf(
			"Included monthly usage credit: %s plan, %s (%d cents, expires end of month)",
			plan, period, cents,
		)
		trans.Tags = tag
		trans.ExpiresAt = PeriodEnd(at)
		trans.Metadata = Map{
			"creditType": "included-monthly",
			"plan":       plan,
			"period":     period,
		}
		trans.Test = test

		// The granted amount is subscription-clamped by the caller (planForGrant /
		// grantOrgAllotments derive `cents` from the user's REAL plan, never a
		// client-supplied figure), so authorize this write at the ledger sink.
		trans.SetContext(mintauth.WithAuthorized(trans.Context()))
		if err := trans.Create(); err != nil {
			return err
		}
		created = trans
		return nil
	}, nil)
	if err != nil {
		return res, err
	}

	if created == nil {
		res.Reason = "already_granted"
		return res, nil
	}

	res.Granted = true
	res.TransactionId = created.Id()
	return res, nil
}

// GrantedCents returns the total included-allotment credit currently granted to
// `user` for the UTC month containing `at` (sum of non-voided period-tagged
// deposits). Zero when none granted. Used by the rollup to report the included
// amount actually on the balance, independent of the catalog.
func GrantedCents(db *datastore.Datastore, user string, at time.Time, test bool) int64 {
	rootKey := db.NewKey("synckey", "", 1, nil)
	transs := make([]*transaction.Transaction, 0)
	q := transaction.Query(db).Ancestor(rootKey).
		Filter("Test=", test).
		Filter("DestinationId=", user).
		Filter("Tags=", Tag(at))
	if _, err := q.GetAll(&transs); err != nil {
		return 0
	}
	var total int64
	for _, t := range transs {
		if t.Type == transaction.Deposit {
			total += int64(t.Amount)
		}
	}
	return total
}
