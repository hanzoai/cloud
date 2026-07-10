package authorizenet

import (
	"errors"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/authorizenet"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

var NothingToAuthorizeError = errors.New("Nothing to Authorize (Items or Subscriptions)")

func Authorize(org *organization.Organization, ord *order.Order, usr *user.User, pay *payment.Payment) error {
	ctx := ord.Datastore().Context

	// Create Authorize.Net client
	con := org.AuthorizeNetToken(ord.Test)

	log.Warn("Connection: %v", con, ctx)
	log.Warn("Test?: %v", !org.Live, ctx)

	loginId := con.LoginId
	transactionKey := con.TransactionKey
	key := con.Key

	pay.Amount = ord.Total

	client := authorizenet.New(ctx, loginId, transactionKey, key, !org.Live)

	if ord.Total > 0 {
		// Do authorization
		_, err := client.Authorize(pay)
		if err != nil {
			log.Error("Failed to authorize payment '%s'", pay.Id(), ctx)
			log.JSON(pay)
			return err
		}
	} else if len(ord.Subscriptions) == 0 {
		return NothingToAuthorizeError
	}

	usr.Accounts.AuthorizeNet = pay.Account.AuthorizeNet
	pay.Live = org.Live

	now := time.Now()

	// TODO: FIGURE THIS OUT LATER
	for i, sub := range ord.Subscriptions {
		log.Info("Before New Subscription %v\n %v", i, json.Encode(sub), ctx)

		sub.PeriodStart = now
		sub.Buyer = pay.Buyer
		sub.Account = pay.Account
		sub2, err := client.NewSubscription(&sub)

		log.Info("After New Subscription %v\n %v", i, json.Encode(sub2), ctx)
		if err != nil {
			return err
		}

		ord.Subscriptions[i] = *sub2
	}

	return nil
}
