package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/referralprogram"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var Referrer = New("referrer", func(c *gin.Context) *referrer.Referrer {
	// Get namespaced db
	db := getNamespaceDb(c)

	u := User(c)

	ref := referrer.New(db)
	ref.UserId = u.Id()
	ref.GetOrCreate("UserId=", ref.UserId)
	ref.Program.Triggers = []int{0}
	ref.Program.Actions = []referralprogram.Action{referralprogram.Action{Type: referralprogram.StoreCredit}}
	ref.Program.Actions[0].CreditAction = referralprogram.CreditAction{Currency: currency.USD, Amount: currency.Cents(1000)}
	ref.MustPut()

	return ref
})
