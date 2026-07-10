package square

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/hanzoai/cloud/clients/commerce/email"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	squarelib "github.com/hanzoai/cloud/clients/commerce/thirdparty/square"
)

var NonSquarePayment = errors.New("only refunds for Square payments are supported via this handler")
var ZeroRefund = errors.New("refund amount cannot be 0")
var NegativeRefund = errors.New("refund amount must be a positive integer")
var NoPaymentsToRefund = errors.New("order has no payments to refund")

// Refund refunds refundAmount against ord's Square payments. idempotencyKey, when
// non-empty, is forwarded (per payment, deterministically) to Square as ITS
// idempotency key so a retried refund is de-duplicated AT THE GATEWAY — the money
// move is idempotent across pods/mutexes/in-flight guards. Empty key = legacy
// non-idempotent behavior (Square generates a random key).
func Refund(org *organization.Organization, ord *order.Order, refundAmount currency.Cents, idempotencyKey string) error {
	if refundAmount == 0 {
		return ZeroRefund
	}
	if refundAmount < 0 {
		return NegativeRefund
	}

	db := ord.Datastore()
	ctx := db.Context

	if int64(refundAmount) > int64(ord.Total) {
		return errors.New("requested refund amount is greater than the order total")
	}
	if ord.Refunded+refundAmount > ord.Total {
		return errors.New("previously refunded amounts and requested refund amount exceed the order total")
	}

	payments, err := ord.GetPayments()
	if err != nil {
		return err
	}

	// Fail closed on a zero-payment order: there is nothing to refund, and the
	// receipt email below dereferences payments[0]. Without this guard an order
	// whose Paid was set without any payment record would panic mid-refund
	// (Red HIGH-3) instead of returning a clean error.
	if len(payments) == 0 {
		return NoPaymentsToRefund
	}

	for _, pay := range payments {
		if pay.Type != accounts.SquareType {
			return NonSquarePayment
		}
	}

	if ord.Paid < refundAmount {
		return errors.New("refund amount exceeds total payment amount")
	}

	sqCfg := org.SquareConfig(org.TestMode())
	proc := squarelib.NewProcessor(squarelib.Config{
		AccessToken:   sqCfg.AccessToken,
		LocationID:    sqCfg.LocationId,
		WebhookSecret: org.Square.WebhookSignatureKey,
		Environment:   org.SquareEnvironment(),
	})

	refundRemaining := refundAmount
	for _, p := range payments {
		var amount currency.Cents
		if p.Amount <= refundRemaining {
			amount = p.Amount
		} else {
			amount = refundRemaining
		}

		if !p.Test {
			// Per-payment DETERMINISTIC gateway key: same (base key, payment,
			// amount) ⇒ same Square idempotency key ⇒ Square de-dupes a retry.
			var gwKey string
			if idempotencyKey != "" {
				sum := sha256.Sum256([]byte(idempotencyKey + "|" + p.Account.Square.PaymentId + "|" + strconv.FormatInt(int64(amount), 10)))
				gwKey = "rfnd_" + hex.EncodeToString(sum[:20])
			}
			_, err := proc.Refund(context.Background(), processor.RefundRequest{
				TransactionID:  p.Account.Square.PaymentId,
				Amount:         amount,
				Reason:         "customer refund",
				IdempotencyKey: gwKey,
			})
			if err != nil {
				return err
			}
		}

		refundRemaining -= amount
		if refundRemaining == 0 {
			break
		}
	}

	log.Info("Square refund amount: %v", refundAmount)
	ord.Refunded = ord.Refunded + refundAmount
	ord.Paid = ord.Paid - refundAmount

	usr := user.New(db)
	usr.GetById(ord.UserId)

	if ord.Total == ord.Refunded {
		email.SendOrderRefunded(ctx, org, ord, usr, payments[0])
		ord.PaymentStatus = payment.Refunded
		ord.Status = order.Cancelled
	} else {
		email.SendOrderRefunded(ctx, org, ord, usr, payments[0])
	}

	return ord.Put()
}
