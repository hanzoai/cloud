// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"

	"github.com/hanzoai/cloud"
	credit "github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// The CREDIT — the other side of a settlement, published for the same reason the
// gate and the debit are: the prepaid ledger has one writer and it is this process.
//
// finance_record debits; this credits. They are separate ops because they are
// separate acts with separate idempotency and separate authority, and folding them
// into one signed amount would make a sign error a transfer in the wrong direction.
//
// It exists for x402: a purchase moves money twice, out of the buyer's ledger and
// into the seller's, and until now the second half could only happen in a process
// that had the ledger linked — which the payment rail never does. A settlement that
// can debit but not credit is not a settlement, so this is what makes "both sides
// or neither" reachable from another binary.

// exposeCredit publishes the ledger credit. Mount calls it.
func exposeCredit() {
	zip.Post[plane.CreditIn, plane.Credited](cloud.Plane(), "/finance/credit",
		func(ctx context.Context, in *plane.CreditIn) (*plane.Credited, error) {
			org, err := callerOrg(ctx, "credit")
			if err != nil {
				return nil, err
			}
			amount, err := in.Amount.Parse()
			if err != nil {
				return nil, zip.ErrBadRequest("credit: " + err.Error())
			}
			if amount.Sign() <= 0 {
				return nil, zip.ErrBadRequest("credit: amount must be positive")
			}
			if in.Ref == "" {
				// Without a ref this op is a money printer: a retry — and a settlement
				// retries by construction — would credit twice. The key is required
				// rather than defaulted, because a default nobody chose is a key that
				// collides.
				return nil, zip.ErrBadRequest("credit: ref is required (the idempotency key)")
			}
			subject := in.Subject
			if subject == "" {
				subject = org
			}
			fin, err := books("credit")
			if err != nil {
				return nil, err
			}
			// Org comes from the CALLER, never the argument: a caller that could name
			// the credited org could pay itself out of someone else's books.
			if _, derr := fin.Deposit(ctx, types.DepositInput{
				Org: org, Subject: subject, Currency: amount.Currency().Code,
				// The EXACT decimal, never Minor(): a settlement is routinely a
				// fraction of a cent, and the minor unit would round it to nothing.
				Amount: credit.FromDecimal(amount.Decimal()),
				Ref:    in.Ref, Notes: in.Notes, Tags: in.Tags,
			}); derr != nil {
				return nil, zip.Errorf(500, "credit %s/%s: %v", org, subject, derr)
			}
			return &plane.Credited{Amount: in.Amount}, nil
		},
		zip.WithOperationID(plane.FinanceCredit),
		zip.WithSummary("Credit one subject's prepaid ledger, exactly once per ref"))
}
