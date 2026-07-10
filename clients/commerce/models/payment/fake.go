package payment

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func Fake(db *datastore.Datastore) *Payment {
	pay := New(db)
	pay.Amount = currency.Cents(0).Fake()
	pay.Account.Type = accounts.NullType
	pay.Buyer = Buyer{
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
	pay.Status = Unpaid
	pay.Currency = currency.USD

	pay.Account.Number = "4242424242424242"
	pay.Account.CVC = "424"
	pay.Account.Month = 12
	pay.Account.Year = 2024

	return pay
}
