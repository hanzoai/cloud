package subscription

import (
	"math/rand"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/deprecated/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func Fake(db *datastore.Datastore) *Subscription {
	sub := New(db)
	sub.PlanId = fake.Id()
	sub.UserId = fake.Id()
	sub.FeePercent = fake.Percent
	sub.PeriodStart = time.Now()
	sub.PeriodEnd = time.Now().AddDate(0, 0, 30)
	sub.Canceled = false
	sub.Quantity = rand.Intn(10)
	sub.Status = Active
	sub.Plan = *plan.Fake(db)
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
