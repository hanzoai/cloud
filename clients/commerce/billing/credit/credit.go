// Package credit provides reusable starter credit logic.
// Shared between api/billing handlers and middleware/iammiddleware.
package credit

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Starter credit constants.
//
// The welcome/starter credit is a TRIAL (non-cash) grant: $5.00, classified by
// its `starter-credit` tag into the Credit bucket (billing/bucket.DepositKind),
// spendable on non-premium metered usage only (never GPUs, never premium models —
// see hanzoai/ai openai_api.go, which gates premium behind a balance ABOVE the
// $5 starter). $5 is the ONE canonical amount, shared across the stack: billing
// app config `trialCreditCents: 500`, ai `StarterCreditDollars = 5.00`, and the
// cloud-api-models `features.starter_credit: 5.0`. Keep them equal.
const (
	StarterCreditCents = 500 // $5.00 USD
	StarterCreditDays  = 365 // expires in 365 days
	StarterCreditTag   = "starter-credit"
)

// GrantIfEligible checks if the user already has a starter credit and
// grants one if not. It is idempotent: duplicate calls for the same
// user are safe. The trigger parameter records what initiated the grant
// (e.g. "payment-method-added", "org-created").
//
// This function is intended to be called from a goroutine; it only logs
// on failure and never panics. For the synchronous form that reports
// whether a grant was created, use GrantIfEligibleNow.
func GrantIfEligible(db *datastore.Datastore, userId, trigger string) {
	_, _ = GrantIfEligibleNow(db, userId, trigger)
}

// GrantIfEligibleNow is the synchronous form of GrantIfEligible. It ensures the
// subject (userId — an IAM "owner/name" per-user key, or an org slug for pooled
// billing) has received exactly one starter-credit Deposit, and reports whether
// THIS call created it (false = one already existed).
//
// Idempotent + race-safe: the check-and-create runs inside a datastore
// transaction so concurrent callers can never double-grant (no bleed). The
// dedupe key is (DestinationId=userId, Tags=StarterCreditTag).
//
// The grant is a real Deposit transaction (tag "starter-credit", $5, 365-day
// expiry) — so it nets into GET /v1/billing/balance, which is the account the
// cloud gateway's balance gate reads and its usage debit withdraws from. (It is
// deliberately NOT a credit-grant record; those live in a separate ledger the
// balance endpoint does not read.)
func GrantIfEligibleNow(db *datastore.Datastore, userId, trigger string) (granted bool, err error) {
	if userId == "" {
		return false, nil
	}

	err = db.RunInTransaction(func(txDb *datastore.Datastore) error {
		rootKey := txDb.NewKey("synckey", "", 1, nil)

		// Check if starter credit was already granted (inside transaction).
		existingTrans := make([]*transaction.Transaction, 0)
		tq := transaction.Query(txDb).Ancestor(rootKey).
			Filter("DestinationId=", userId).
			Filter("Tags=", StarterCreditTag)
		if _, e := tq.Limit(1).GetAll(&existingTrans); e == nil && len(existingTrans) > 0 {
			granted = false
			return nil // already granted
		}

		trans := transaction.New(txDb)
		trans.Type = transaction.Deposit
		trans.DestinationId = userId
		trans.DestinationKind = "iam-user"
		trans.Currency = "usd"
		trans.Amount = currency.Cents(StarterCreditCents)
		trans.Notes = "Welcome credit: $5.00 USD (expires in 365 days)"
		trans.Tags = StarterCreditTag
		trans.ExpiresAt = time.Now().AddDate(0, 0, StarterCreditDays)
		trans.Metadata = Map{
			"creditType": "starter",
			"expiryDays": StarterCreditDays,
			"trigger":    trigger,
		}

		// Server-FIXED (StarterCreditCents) idempotent welcome credit — the amount
		// is not caller-controlled — so authorize this write at the ledger sink.
		trans.SetContext(mintauth.WithAuthorized(trans.Context()))
		if e := trans.Create(); e != nil {
			return e
		}
		granted = true
		return nil
	}, nil)

	if err != nil {
		log.Warn("Failed to auto-grant starter credit for user %s: %v", userId, err)
		return false, err
	}
	return granted, nil
}
