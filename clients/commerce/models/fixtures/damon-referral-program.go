package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referralprogram"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var _ = New("damon-referral-program", func(c *gin.Context) *referralprogram.ReferralProgram {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "damon"
	org.GetOrCreate("Name=", org.Name)

	nsdb := datastore.New(org.Namespaced(db.Context))

	rp := referralprogram.New(nsdb)
	rp.Name = "Damon Referral Program"
	rp.GetOrCreate("Name=", rp.Name)

	rp.Actions = []referralprogram.Action{
		// referralprogram.Action{
		// 	Type: referralprogram.StoreCredit,
		// 	Name: "Sign Up Action",

		// 	CreditAction: referralprogram.CreditAction{
		// 		Currency: currency.PNT,
		// 		Amount:   currency.Cents(10),
		// 	},

		// 	Trigger: referralprogram.Trigger{
		// 		Event: referral.NewUser,
		// 		Type:  referralprogram.Always,
		// 	},
		// },
		referralprogram.Action{
			Type: referralprogram.StoreCredit,
			Name: "Sale Action",

			CreditAction: referralprogram.CreditAction{
				Currency: currency.PNT,
				Amount:   currency.Cents(1),
			},

			Trigger: referralprogram.Trigger{
				Event: referral.NewOrder,
				Type:  referralprogram.Always,
			},
		},
		referralprogram.Action{
			Type: referralprogram.SendWoopra,
			Name: "Woopra Action",

			SendWoopraEvent: referralprogram.SendWoopraEvent{
				Domain: "damon.com",
			},

			Trigger: referralprogram.Trigger{
				Event: referral.NewOrder,
				Type:  referralprogram.Always,
			},
		},
	}

	rp.MustPut()

	return rp
})
