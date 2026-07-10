package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referralprogram"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var _ = New("cover-referralprogram", func(c *gin.Context) *referralprogram.ReferralProgram {
	db := datastore.New(c)

	org := organization.New(db)
	org.MustGetById("cover")

	nsDb := datastore.New(org.Namespaced(org.Context()))

	rp := referralprogram.New(nsDb)
	rp.Name = "Cover Referral Program"
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
				Amount:   currency.Cents(100),
			},

			Trigger: referralprogram.Trigger{
				Event: referral.NewOrder,
				Type:  referralprogram.Always,
			},
		},

		// referralprogram.Action{
		// 	Type: referralprogram.SendUserEmail,
		// 	Name: "L1 Referrer",
		// 	Once: true,

		// 	SendTransactionalUserEmailAction: referralprogram.SendTransactionalUserEmailAction{
		// 		EmailTemplate: "referral-level-1-reached",
		// 	},

		// 	Trigger: referralprogram.Trigger{
		// 		Type: referralprogram.CreditGreaterThanOrEquals,

		// 		CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
		// 			Currency:                  currency.PNT,
		// 			CreditGreaterThanOrEquals: currency.Cents(840),
		// 		},
		// 	},
		// },

		// referralprogram.Action{
		// 	Type: referralprogram.SendUserEmail,
		// 	Name: "L2 Referrer",
		// 	Once: true,

		// 	SendTransactionalUserEmailAction: referralprogram.SendTransactionalUserEmailAction{
		// 		EmailTemplate: "referral-level-2-reached",
		// 	},

		// 	Trigger: referralprogram.Trigger{
		// 		Type: referralprogram.CreditGreaterThanOrEquals,

		// 		CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
		// 			Currency:                  currency.PNT,
		// 			CreditGreaterThanOrEquals: currency.Cents(1680),
		// 		},
		// 	},
		// },

		// referralprogram.Action{
		// 	Type: referralprogram.SendUserEmail,
		// 	Name: "L3 Referrer",
		// 	Once: true,

		// 	SendTransactionalUserEmailAction: referralprogram.SendTransactionalUserEmailAction{
		// 		EmailTemplate: "referral-level-3-reached",
		// 	},

		// 	Trigger: referralprogram.Trigger{
		// 		Type: referralprogram.CreditGreaterThanOrEquals,

		// 		CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
		// 			Currency:                  currency.PNT,
		// 			CreditGreaterThanOrEquals: currency.Cents(2520),
		// 		},
		// 	},
		// },

		// referralprogram.Action{
		// 	Type: referralprogram.SendUserEmail,
		// 	Name: "L4 Referrer",
		// 	Once: true,

		// 	SendTransactionalUserEmailAction: referralprogram.SendTransactionalUserEmailAction{
		// 		EmailTemplate: "referral-level-4-reached",
		// 	},

		// 	Trigger: referralprogram.Trigger{
		// 		Type: referralprogram.CreditGreaterThanOrEquals,

		// 		CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
		// 			Currency:                  currency.PNT,
		// 			CreditGreaterThanOrEquals: currency.Cents(3360),
		// 		},
		// 	},
		// },

		// referralprogram.Action{
		// 	Type: referralprogram.SendUserEmail,
		// 	Name: "L5 Referrer",
		// 	Once: true,

		// 	SendTransactionalUserEmailAction: referralprogram.SendTransactionalUserEmailAction{
		// 		EmailTemplate: "referral-level-5-reached",
		// 	},

		// 	Trigger: referralprogram.Trigger{
		// 		Type: referralprogram.CreditGreaterThanOrEquals,

		// 		CreditGreaterThanOrEqualsTrigger: referralprogram.CreditGreaterThanOrEqualsTrigger{
		// 			Currency:                  currency.PNT,
		// 			CreditGreaterThanOrEquals: currency.Cents(4200),
		// 		},
		// 	},
		// },
	}

	rp.MustPut()

	return rp
})
