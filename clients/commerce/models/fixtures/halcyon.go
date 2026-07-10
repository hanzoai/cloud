package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referralprogram"
	"github.com/hanzoai/cloud/clients/commerce/models/types/analytics"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/email/provider"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/types/socialmedia"
	"github.com/hanzoai/cloud/clients/commerce/types/website"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var _ = New("halcyon", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "halcyon"
	org.SocialMedia = socialmedia.SocialMedia{
		Facebook:  "https://www.facebook.com/HalcyonBiotech",
		Twitter:   "https://twitter.com/halcyon_biotech",
		Instagram: "https://www.instagram.com/halcyon_biotech",
	}
	org.LogoUrl = "https://neuromethod.halcyon.bio/img/large-logo-sq-blue.png"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "halcyon@hanzo.ai"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Halcyon"
	u.LastName = "User"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("Ki4TDz1ciHCNRsFs")
	u.Put()

	org.FullName = "Halcyon Bio"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{
		website.Website{Type: website.Production, Url: "http://halcyon.bio"},
		website.Website{Type: website.Staging, Url: "http://beta.halcyon.bio"},
		website.Website{Type: website.Test, Url: "http://360.halcyon.bio", PasswordResetPath: "", EmailConfirmPath: "http://360.halcyon.bio/account/registrationsuccess?token="},
	}
	org.SecretKey = []byte("VwqutxegjNfz3kTo6LYMJIDQlUHxPFXHdLdiUdPdrS7v2L7fkmfn8ltqzrUmw58V")

	org.AuthorizeNet.Sandbox.LoginId = ""
	org.AuthorizeNet.Sandbox.TransactionKey = ""
	org.AuthorizeNet.Sandbox.Key = "Simon"

	//org.SendGrid.APIKey = "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"

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 0
	org.Fees.Affiliate.Percent = 0.20

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "Halcyon Bio",
		Address: "hi@halcyon.bio",
	}
	org.Email.Defaults.ProviderId = string(provider.SendGrid)
	org.Email.Order.Confirmation = email.Setting{
		Enabled:    true,
		TemplateId: "d-57f034971aec4beb8137c17b1eb71b02",
	}
	org.Email.Order.Refund = email.Setting{
		Enabled:    true,
		TemplateId: "d-ee5f9eedefd34e8c9d875f5629670047",
	}
	org.Email.Order.RefundPartial = email.Setting{
		Enabled:    true,
		TemplateId: "d-d444b9a1d1b84df0aa8a96fa52010116",
	}
	org.Email.Order.Shipped = email.Setting{
		Enabled:    true,
		TemplateId: "d-78f0f304bb17428eaffa8ff1504ad124",
	}
	org.Email.Order.Updated = email.Setting{
		Enabled:    true,
		TemplateId: "d-cfe9717a682e47a5b70f16fd794bca45",
	}
	org.Email.User.Welcome = email.Setting{
		Enabled:    true,
		TemplateId: "d-21fd5d07d12d4e5284d5e1986dc0b4e8",
	}
	org.Email.User.ConfirmEmail = email.Setting{
		Enabled:    true,
		TemplateId: "d-23166776363e489e898b73c7ec208ebe",
	}
	org.Email.User.Activated = email.Setting{
		Enabled:    true,
		TemplateId: "d-b2b35a2f612c4dfebdf318a4e88737f2",
	}
	org.Email.User.ResetPassword = email.Setting{
		Enabled:    true,
		TemplateId: "d-aae4b56c34a24cd78408e9ae58a75655",
	}
	org.Email.Subscriber.Welcome = email.Setting{
		Enabled:    true,
		TemplateId: "d-21fd5d07d12d4e5284d5e1986dc0b4e8",
	}
	org.Email.Affiliate.Welcome = email.Setting{
		Enabled:    true,
		TemplateId: "d-9f991d70e4124d019ffae88aa1b6f902",
	}

	org.SignUpOptions.ImmediateLogin = true
	org.SignUpOptions.AccountsEnabledByDefault = true

	sendgrid := &integration.Integration{
		Type:    integration.SendGridType,
		Enabled: true,
		SendGrid: integration.SendGrid{
			APIKey: "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",
		},
	}

	if len(org.Integrations.FilterByType(sendgrid.Type)) == 0 {
		org.Integrations = org.Integrations.MustAppend(sendgrid)
	}

	an := analytics.Integration{}
	an.Type = "google-analytics"
	an.Id = "UA-123218175-1"
	an.Event = ""
	an.Sampling = 0
	an.Disabled = false
	an.IntegrationId = "_BNIFVhpgac"

	ans := analytics.Analytics{}
	ans.Integrations = append(ans.Integrations, an)
	org.Analytics = ans

	org.MustUpdate()

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create mailinglist
	f := form.New(nsdb)
	f.Name = "Mini-launch List"
	f.GetOrCreate("Name=", f.Name)
	f.SendWelcome = false
	f.EmailList.Enabled = true
	f.EmailList.Id = "4534419"
	f.MustUpdate()

	f = form.New(nsdb)
	f.Name = "Affiliates"
	f.GetOrCreate("Name=", f.Name)
	f.SendWelcome = false
	f.EmailList.Enabled = true
	f.EmailList.Id = "4780161"
	f.MustUpdate()

	f = form.New(nsdb)
	f.Name = "360 List"
	f.GetOrCreate("Name=", f.Name)
	f.SendWelcome = false
	f.EmailList.Enabled = true
	f.EmailList.Id = "4941545"
	f.MustUpdate()

	// Create earphone product
	prod1 := product.New(nsdb)
	prod1.Slug = "60-caps"
	prod1.GetOrCreate("Slug=", prod1.Slug)
	prod1.Name = "Bottle - 60 Capsules"
	prod1.Description = "30 Day Supply"
	prod1.SKU = "865524000406"
	prod1.Price = currency.Cents(8999)
	prod1.Hidden = false
	prod1.MustUpdate()

	// Create earphone product
	prod2 := product.New(nsdb)
	prod2.Slug = "60-caps-sub"
	prod2.GetOrCreate("Slug=", prod2.Slug)
	prod2.Name = "Bottle - 60 Capsules - Subscription"
	prod2.Description = "30 Day Supply"
	prod2.SKU = "865524000406-sub"
	prod2.Price = currency.Cents(6997)
	prod2.Hidden = false
	prod2.IsSubscribeable = true
	prod2.Interval = Monthly
	prod2.MustUpdate()

	// Create earphone product
	prod3 := product.New(nsdb)
	prod3.Slug = "sample pack"
	prod3.GetOrCreate("Slug=", prod3.Slug)
	prod3.Name = "Sample Pack"
	prod3.Description = "Try Halcyon"
	prod3.SKU = "865524000406"
	prod3.Price = currency.Cents(19999)
	prod3.Inventory = 9000
	prod3.Preorder = true
	prod3.Hidden = false
	prod3.MustUpdate()

	rp := referralprogram.New(nsdb)
	rp.Name = "Halcyon Referral Program"
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
				Amount:   currency.Cents(0),
			},

			Trigger: referralprogram.Trigger{
				Event: referral.NewOrder,
				Type:  referralprogram.Always,
			},
		},
	}

	rp.MustUpdate()

	// Create earphone product
	return org
})
