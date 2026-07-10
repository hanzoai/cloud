package util

import (
	"context"
	"strconv"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/cart"
	"github.com/hanzoai/cloud/clients/commerce/models/multi"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"

	"github.com/hanzoai/cloud/clients/commerce/thirdparty/woopra"
	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func UpdateOrder(ctx context.Context, ord *order.Order, payments []*payment.Payment) {
	totalPaid := 0

	for _, pay := range payments {
		totalPaid += int(pay.Amount)
	}

	ord.Paid = currency.Cents(int(ord.Paid) + totalPaid)
	if ord.Paid >= ord.Total {
		ord.PaymentStatus = payment.Paid
	}

	ord.MustUpdate()
}

func UpdateOrderPayments(ctx context.Context, ord *order.Order, payments []*payment.Payment) error {
	vals := []interface{}{}

	for _, pay := range payments {
		vals = append(vals, pay)
	}

	return multi.Update(vals)
}

func SaveRedemptions(ctx context.Context, ord *order.Order) {
	// Save coupon redemptions
	ord.GetCoupons()
	if len(ord.Coupons) > 0 {
		for _, coup := range ord.Coupons {
			if err := coup.SaveRedemption(); err != nil {
				log.Warn("Unable to save redemption: %v", err, ctx)
			}
		}
	}
}

type Referrent struct {
	id    string
	kind  string
	total currency.Cents
}

func (r *Referrent) Id() string {
	return r.id
}

func (r *Referrent) Kind() string {
	return r.kind
}

func (r *Referrent) Total() currency.Cents {
	return r.total
}

func UpdateReferral(org *organization.Organization, ord *order.Order) {
	ctx := org.Context()
	db := ord.Datastore()

	// Check for referrer
	if ord.ReferrerId == "" {
		return // No referrer
	}

	// Search for referrer
	ref := referrer.New(db)
	if err := ref.GetById(ord.ReferrerId); err != nil {
		log.Warn("Order referenced non-existent referrer '%s'", ord.ReferrerId, ctx)
		ord.ReferrerId = ""
		return
	}

	// Total the order
	total := ord.Total
	for _, sub := range ord.Subscriptions {
		total += sub.Total
	}

	// Save referral
	rfl, err := ref.SaveReferral(ctx, org.Id(), referral.NewOrder, &Referrent{
		ord.Id(),
		ord.Kind(),
		total,
	}, ord.UserId, !org.Live)

	if err != nil {
		log.Warn("Unable to save referral: %v", err, ctx)
		ord.ReferrerId = ""
		return
	}

	ord.ReferralId = rfl.Id()

	if err := ord.Update(); err != nil {
		log.Warn("Unable to save order: %v", err, ctx)
		return
	}

	if !ord.Test {
		if err := counter.IncrReferrerFees(ctx, org, ref.Id(), rfl); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}
	}

	// Update statistics
	if ref.AffiliateId != "" {
		if err := counter.IncrAffiliateFees(ctx, org, ref.AffiliateId, rfl); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}
	}
}

func UpdateCart(ctx context.Context, ord *order.Order) {
	// Update cart
	car := cart.New(ord.Datastore())

	if ord.CartId != "" {
		if err := car.GetById(ord.CartId); err != nil {
			log.Warn("Unable to find cart: %v", err, ctx)
		} else {
			car.Status = cart.Ordered
			if err := car.Update(); err != nil {
				log.Warn("Unable to save cart: %v", err, ctx)
			}
		}
	}
}

func UpdateStats(ctx context.Context, org *organization.Organization, ord *order.Order, payments []*payment.Payment) {
	log.Debug("Incrementing Counters? %v", ord.Test, ctx)

	if !ord.Test {
		log.Debug("Incrementing Counters", ctx)
		t := ord.CreatedAt
		if err := counter.IncrTotalOrders(ctx, org, t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}
		if err := counter.IncrTotalSales(ctx, org, payments, t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}
		if err := counter.IncrTotalProductOrders(ctx, org, ord, t); err != nil {
			log.Warn("Counter Error %s", err, ctx)
		}

		if err := counter.IncrOrder(ctx, ord); err != nil {
			log.Error("IncrOrder Error %v", err, ctx)
		}

		if ord.StoreId != "" {
			if err := counter.IncrStoreOrders(ctx, org, ord.StoreId, t); err != nil {
				log.Warn("Counter Error %s", err, ctx)
			}
			if err := counter.IncrStoreSales(ctx, org, ord.StoreId, payments, t); err != nil {
				log.Warn("Counter Error %s", err, ctx)
			}
			if err := counter.IncrStoreProductOrders(ctx, org, ord.StoreId, ord, t); err != nil {
				log.Warn("Counter Error %s", err, ctx)
			}
		}
	}
}

func UpdateMailchimp(ctx context.Context, org *organization.Organization, ord *order.Order, usr *user.User) {
	// Save user as customer in Mailchimp if configured
	if org.Mailchimp.APIKey != "" {
		// Create new mailchimp client
		client := mailchimp.New(ctx, org.Mailchimp)

		// Update cart
		car := cart.New(ord.Datastore())

		// Determine store to use
		storeId := ord.StoreId
		if storeId == "" {
			storeId = org.DefaultStore
		}

		if ord.CartId != "" {
			if err := car.GetById(ord.CartId); err != nil {
				log.Warn("Unable to find cart: %v", err, ctx)
			} else {
				// Delete cart in mailchimp
				if err := client.DeleteCart(storeId, car); err != nil {
					log.Warn("Failed to delete Mailchimp cart: %v", err, ctx)
				}
			}
		}

		stor := store.New(ord.Datastore())
		stor.MustGetById(storeId)

		// Subscribe user to list
		buy := Buyer{
			Email:           usr.Email,
			FirstName:       usr.FirstName,
			LastName:        usr.LastName,
			Phone:           usr.Phone,
			BillingAddress:  ord.BillingAddress,
			ShippingAddress: ord.ShippingAddress,
		}

		referralLink := ""

		referrers := make([]referrer.Referrer, 0)
		if _, err := referrer.Query(ord.Datastore()).Filter("UserId=", usr.Id()).GetAll(&referrers); err != nil {
			log.Warn("Failed to load referrals for user: %v", err, ctx)
		}

		if len(referrers) > 0 {
			referralLink = stor.ReferralBaseUrl + referrers[0].Id_
		}

		log.Warn("Referral Link: %v from %v", referralLink, usr.Referrers, ctx)

		if err := client.SubscribeCustomer(stor.Mailchimp.ListId, buy, referralLink); err != nil {
			log.Warn("Failed to create Mailchimp customer - status: %v", err.Status, ctx)
			log.Warn("Failed to create Mailchimp customer - unknown error: %v", err.Unknown, ctx)
			log.Warn("Failed to create Mailchimp customer - mailchimp error: %v", err.Mailchimp, ctx)
		}

		// Create order in mailchimp
		if err := client.CreateOrder(storeId, ord); err != nil {
			log.Warn("Failed to create Mailchimp order - status: %v", err.Status, ctx)
			log.Warn("Failed to create Mailchimp order - unknown error: %v", err.Unknown, ctx)
			log.Warn("Failed to create Mailchimp order - mailchimp error: %v", err.Mailchimp, ctx)
		}
	}
}

func HandleDeposit(ord *order.Order) {
	// Handle Deposit Logic
	if ord.Mode == order.DepositMode && ord.PaymentStatus == payment.Paid {
		trans := transaction.New(ord.Datastore())
		trans.DestinationId = ord.UserId
		trans.DestinationKind = "user"
		trans.Type = transaction.Deposit
		trans.Currency = ord.Currency
		trans.Amount = ord.Subtotal
		trans.Test = ord.Test
		trans.Notes = "Deposit from Order '" + ord.Id() + "'"
		trans.MustCreate()
	}
}

func UpdateWoopraIntegration(ctx context.Context, org *organization.Organization, ord *order.Order) {
	{
		in := org.Integrations.FindByType(integration.WoopraType)
		if in != nil {
			usr := user.New(ord.Datastore())
			if err := usr.GetById(ord.UserId); err != nil {
				log.Error("no user found %v", err, ctx)
				return
			}

			if err := usr.LoadOrders(); err != nil {
				log.Error("loadorders error %v", err, ctx)
				return
			}

			domain := in.Woopra.Domain
			wt, _ := woopra.NewTracker(map[string]string{

				// `host` is domain as registered in Woopra, it identifies which
				// project environment to receive the tracking request
				"host": domain,

				// In milliseconds, defaults to 30000 (equivalent to 30 seconds)
				// after which the event will expire and the visit will be marked
				// as offline.
				"timeout": "30000",
			})

			cancelledOrders := 0
			creditOrders := 0
			disputedOrders := 0
			failedOrders := 0
			fraudOrders := 0
			paidOrders := 0
			refundedOrders := 0
			unpaidOrders := 0
			for _, v := range usr.Orders {
				switch ps := v.PaymentStatus; ps {
				case payment.Cancelled:
					cancelledOrders++
				case payment.Credit:
					creditOrders++
				case payment.Disputed:
					disputedOrders++
				case payment.Failed:
					failedOrders++
				case payment.Fraudulent:
					fraudOrders++
				case payment.Paid:
					paidOrders++
				case payment.Refunded:
					refundedOrders++
				case payment.Unpaid:
					unpaidOrders++
				}
			}

			values := make(map[string]string)
			values["first_name"] = usr.FirstName
			values["last_name"] = usr.LastName
			values["city"] = usr.ShippingAddress.City
			values["country"] = usr.ShippingAddress.Country
			values["referred_by"] = usr.ReferrerId
			values["referrals"] = strconv.Itoa(len(usr.Referrals))
			values["orders"] = strconv.Itoa(len(usr.Orders))
			values["cancelled_orders"] = strconv.Itoa(cancelledOrders)
			values["credit_orders"] = strconv.Itoa(creditOrders)
			values["disputed_orders"] = strconv.Itoa(disputedOrders)
			values["failed_orders"] = strconv.Itoa(failedOrders)
			values["fraud_orders"] = strconv.Itoa(fraudOrders)
			values["paid_orders"] = strconv.Itoa(paidOrders)
			values["refunded_orders"] = strconv.Itoa(refundedOrders)
			values["unpaid_orders"] = strconv.Itoa(unpaidOrders)

			person := woopra.Person{
				Id:     usr.Id(),
				Name:   usr.Name(),
				Email:  usr.Email,
				Values: values,
			}

			// identifying current visitor in Woopra
			ident := wt.Identify(
				ctx,
				person,
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_11_6) AppleWebKit/601.7.7 (KHTML, like Gecko) Version/9.1.2 Safari/601.7.7",
			)

			// Tracking custom event in Woopra. Each event can has additional data
			ident.Track(
				"Order Create", // event name
				map[string]string{ // custom data
					"order_id":     ord.Id(),
					"order_number": strconv.Itoa(ord.Number),
					"name":         usr.Name(),
					"email":        usr.Email,
					"city":         usr.ShippingAddress.City,
					"country":      usr.ShippingAddress.Country,
					"referred_by":  usr.ReferrerId,
					"currency":     string(ord.Currency),
					"revenue":      ord.Currency.ToStringNoSymbol(ord.Total),
					"refunded":     ord.Currency.ToStringNoSymbol(ord.Refunded),
					"cart_id":      ord.CartId,
				})

			// it's possible to send only visitor's data to Woopra, without sending
			// any custom event and/or data
			ident.Push()
		}
	}
}
