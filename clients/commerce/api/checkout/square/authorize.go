package square

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	squarelib "github.com/hanzoai/cloud/clients/commerce/thirdparty/square"
)

func Authorize(org *organization.Organization, ord *order.Order, usr *user.User, pay *payment.Payment) error {
	sqCfg := org.SquareConfig(org.TestMode())

	proc := squarelib.NewProcessor(squarelib.Config{
		AccessToken:   sqCfg.AccessToken,
		LocationID:    sqCfg.LocationId,
		WebhookSecret: org.Square.WebhookSignatureKey,
		Environment:   org.SquareEnvironment(),
	})

	req := processor.PaymentRequest{
		Amount:      currency.Cents(pay.Amount),
		Currency:    pay.Currency,
		Token:       pay.Account.Number, // Card nonce from client
		CustomerID:  usr.Accounts.Square.CustomerId,
		OrderID:     ord.Id(),
		Description: pay.Description,
	}

	result, err := proc.Authorize(context.Background(), req)
	if err != nil {
		log.Warn("Square authorize failed for payment '%s': %v", pay.Id(), err)
		return err
	}

	// Update payment with Square response
	pay.Account.Square.PaymentId = result.TransactionID
	pay.Account.Square.LocationId = sqCfg.LocationId
	// pay.Live must follow the SAME authority as the charge environment, so a
	// sandbox authorization is never recorded as a live payment (and vice versa).
	pay.Live = !org.TestMode()

	// Update user's Square account
	usr.Accounts.Square.CustomerId = req.CustomerID

	log.Info("Square authorized payment '%s' → transaction '%s'", pay.Id(), result.TransactionID)
	return nil
}
