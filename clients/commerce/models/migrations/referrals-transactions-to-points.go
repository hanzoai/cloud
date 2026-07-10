package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("referrals-transactions-to-points",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "bellabeat")

		db := ds.New(c)
		org := organization.New(db)
		if _, err := org.Query().Filter("Name=", "bellabeat").Get(); err != nil {
			panic(err)
		}
		return NoArgs
	},
	func(db *ds.Datastore, ref *referrer.Referrer) {
		for i, _ := range ref.Program.Actions {
			ref.Program.Actions[i].Currency = currency.PNT
		}
		ref.Put()
	},
	func(db *ds.Datastore, trans *transaction.Transaction) {
		trans.Currency = currency.PNT
		trans.Put()
	},
)
