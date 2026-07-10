package order

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func Fake(db *datastore.Datastore, lis ...lineitem.LineItem) *Order {
	linetotal := 0
	for _, v := range lis {
		linetotal += int(v.Price) * v.Quantity
	}
	o := New(db)
	o.Email = fake.EmailAddress()
	o.Items = lis
	o.Status = Open
	o.PaymentStatus = payment.Unpaid
	o.Currency = currency.USD
	o.LineTotal = currency.Cents(linetotal)
	o.Subtotal = o.LineTotal
	o.Total = o.Subtotal + o.Shipping + o.Tax
	o.Balance = o.Total
	o.Company = fake.Company()
	o.BillingAddress = Address{Line1: fake.Street(), City: fake.City(), State: fake.State(), PostalCode: fake.Zip(), Country: "US"}
	o.ShippingAddress = Address{Line1: fake.Street(), City: fake.City(), State: fake.State(), PostalCode: fake.Zip(), Country: "US"}

	return o
}

func FakeSubscription(db *datastore.Datastore) *Subscription {
	sub := &Subscription{}
	sub.PlanId = fake.Id()
	sub.UserId = fake.Id()
	sub.FeePercent = fake.Percent
	sub.PeriodStart = time.Now()
	sub.PeriodEnd = time.Now().AddDate(0, 0, 30)
	sub.Canceled = false
	sub.Status = ActiveSubscriptionStatus
	sub.Buyer = Buyer{
		Email:     fake.EmailAddress(),
		FirstName: fake.FirstName(),
		LastName:  fake.LastName(),
		BillingAddress: Address{
			Line1:      fake.Street(),
			City:       fake.City(),
			State:      fake.State(),
			PostalCode: fake.Zip(),
			Country:    "US",
		},
		ShippingAddress: Address{
			Line1:      fake.Street(),
			City:       fake.City(),
			State:      fake.State(),
			PostalCode: fake.Zip(),
			Country:    "US",
		},
	}

	sub.Account.Type = accounts.StripeType
	sub.Account.Number = "4242424242424242"
	sub.Account.CVC = "424"
	sub.Account.Month = 12
	sub.Account.Year = 2024

	return sub
}
