package checkout

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/checkout/authorizenet"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/balance"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/null"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/square"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/tasks"
	"github.com/hanzoai/cloud/clients/commerce/api/checkout/util"
	"github.com/hanzoai/cloud/clients/commerce/email"
	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/webhook"
)

// Make the context less ambiguous, saveReferral needs org context for example
func capture(c *gin.Context, org *organization.Organization, ord *order.Order) error {
	var err error
	var payments []*payment.Payment

	switch ord.Type {
	case accounts.AuthorizeNetType:
		ord, payments, err = authorizenet.Capture(org, ord)
	case accounts.BalanceType:
		ord, payments, err = balance.Capture(org, ord)
	case accounts.NullType:
		ord, payments, err = null.Capture(org, ord)
	case accounts.SquareType:
		ord, payments, err = square.Capture(org, ord)
	case accounts.PayPalType:
		payments = ord.Payments
	default:
		ord, payments, err = square.Capture(org, ord)
	}

	if err != nil {
		ord.CancelReservations()

		log.Error("Capture failed: %v", err, c)
		return err
	}

	ctx := ord.Context()

	util.UpdateOrder(ctx, ord, payments)

	if err := util.UpdateOrderPayments(ctx, ord, payments); err != nil {
		ord.CancelReservations()

		log.Error("Capture could not update order/payments: %v", err, c)
		return err
	}

	// TODO: Run in task(CaptureAsync), no need to block call on rest of this
	util.SaveRedemptions(ctx, ord)
	util.UpdateReferral(org, ord)
	util.UpdateCart(ctx, ord)
	util.UpdateStats(ctx, org, ord, payments)
	util.UpdateWoopraIntegration(ctx, org, ord)
	util.HandleDeposit(ord)

	tasks.CaptureAsync.Call(org.Context(), org.Id(), ord.Id())

	usr := user.New(ord.Datastore())
	err = usr.GetById(ord.UserId)
	if err != nil {
		log.Error("Capture could not find User %v: %v", usr.Id(), err, c)
		return err
	}

	email.SendOrderConfirmation(org.Context(), org, ord, usr)

	webhook.Emit(ctx, org.Name, "order.paid", ord)

	// Publish order.completed to NATS/JetStream with GA4 + Facebook CAPI (fire and forget)
	if pub, ok := c.Get("publisher"); ok {
		if p, ok := pub.(*events.Publisher); ok {
			orderItems := orderLineItemInfos(ord)
			go func() {
				if pubErr := p.PublishOrderCompleted(context.Background(), ord.Id(), org.Name, usr.Id(), usr.Email,
					int64(ord.Total), string(ord.Currency), events.ToOrderItems(orderItems)); pubErr != nil {
					log.Error("PublishOrderCompleted: %v", pubErr, c)
				}
			}()
		}
	}

	return nil
}
