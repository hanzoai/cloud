package indiegogo

import (
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/csv"

	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

func Import(db *datastore.Datastore, filename string) {
	for record := range csv.Iterator(filename) {
		if config.IsDevelopment && record.Index > 25 {
			break // Only import first 25 in development
		}

		// Parse Row
		r := NewRow(record.Row)

		// Create user
		user := user.New(db)
		user.Email = r.Email
		user.FirstName = r.FirstName
		user.LastName = r.LastName
		user.ShippingAddress = r.ShippingAddress
		user.BillingAddress = r.ShippingAddress

		// No longer updating user information in production, as it would clobber any customized information.
		if !config.IsProduction {
			user.Put()
		}

		// Create token
		// token := &Token{
		// 	Id:     r.TokenID,
		// 	UserId: user.Id,
		// 	Email:  user.Email,
		// }

		// db.PutKind("invite-token", token.Id, token)

		// Save contribution
		// contribution := &Contribution{
		// 	Id:            r.PledgeID,
		// 	Perk:          Perks[r.PerkID],
		// 	Status:        r.FulfillmentStatus,
		// 	FundingDate:   r.FundingDate,
		// 	PaymentMethod: r.PaymentMethod,
		// 	UserId:        user.Id,
		// }
		// db.PutKind("contribution", r.PledgeID, contribution)
	}
}
