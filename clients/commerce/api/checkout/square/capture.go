package square

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	squarelib "github.com/hanzoai/cloud/clients/commerce/thirdparty/square"
)

var FailedToCapturePayment = errors.New("failed to capture Square payment")

func Capture(org *organization.Organization, ord *order.Order) (*order.Order, []*payment.Payment, error) {
	db := ord.Datastore()

	payments := make([]*payment.Payment, 0)
	if err := payment.Query(db).Ancestor(ord.Key()).GetModels(&payments); err != nil {
		return nil, payments, err
	}

	sqCfg := org.SquareConfig(org.TestMode())
	proc := squarelib.NewProcessor(squarelib.Config{
		AccessToken:   sqCfg.AccessToken,
		LocationID:    sqCfg.LocationId,
		WebhookSecret: org.Square.WebhookSignatureKey,
		Environment:   org.SquareEnvironment(),
	})

	for _, p := range payments {
		if !p.Captured {
			result, err := proc.Capture(context.Background(), p.Account.Square.PaymentId, p.Amount)
			if err != nil {
				return nil, payments, err
			}

			if result.Status != "captured" && !result.Success {
				return nil, payments, FailedToCapturePayment
			}

			p.Captured = true
			p.Status = payment.Paid
			p.AmountTransferred = p.Amount
			p.CurrencyTransferred = p.Currency
			if result.Fee > 0 {
				p.Fee = result.Fee
			}

			log.Info("Square captured payment '%s' → '%s'", p.Id(), result.TransactionID)
		}
	}

	return ord, payments, nil
}
