package fixtures

import (
	// "time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/discount"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/rule"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/scope"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/target"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"

	// "github.com/hanzoai/cloud/clients/commerce/types/accesstoken"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("ludela", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	// Create user
	usr := user.New(db)
	usr.Email = "jamie@ludela.com"
	usr.GetOrCreate("Email=", usr.Email)
	usr.FirstName = "Jamie"
	usr.LastName = "Bianchini"
	usr.PasswordHash, _ = password.Hash("1Ludela23")

	// Create organization
	org := organization.New(db)
	org.Name = "ludela"
	org.GetOrCreate("Name=", org.Name)
	org.SetKey("V9OT22mI0a")

	// Set organization on user
	usr.Organizations = []string{org.Id()}

	org.FullName = "Ludela Inc"
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{{Type: website.Production, Url: "https://ludela.com"}}
	org.SecretKey = []byte("EU8E011iX2Bp5lv481N2STd1d999cU58")
	org.AddDefaultTokens()

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30

	// Email configuration
	org.Mailchimp.APIKey = ""
	org.Mailchimp.ListId = "262350bdb1"
	org.Mandrill.APIKey = "40gP4DdLRLHo1QX_A8mfHw"

	// Enable accounts by default
	org.SignUpOptions.AccountsEnabledByDefault = true
	org.SignUpOptions.NoNameRequired = true
	org.SignUpOptions.NoPasswordRequired = true
	org.SignUpOptions.TwoStageEnabled = true
	org.SignUpOptions.ImmediateLogin = true

	// API Tokens
	// org.Tokens = []accesstoken.AccessToken{
	// 	accesstoken.AccessToken{
	// 		EntityId:    "V9OT22mI0a",
	// 		Id:          "XodGra0dirg",
	// 		IssuedAt:    time.Now(),
	// 		Name:        "live-secret-key",
	// 		Permissions: 20,
	// 		Secret:      []byte("EU8E011iX2Bp5lv481N2STd1d999cU58"),
	// 	},
	// 	accesstoken.AccessToken{
	// 		EntityId:    "V9OT22mI0a",
	// 		Id:          "z2ZCUCxkfhE",
	// 		IssuedAt:    time.Now(),
	// 		Name:        "live-published-key",
	// 		Permissions: 4503617075675172,
	// 		Secret:      []byte("EU8E011iX2Bp5lv481N2STd1d999cU58"),
	// 	},
	// 	accesstoken.AccessToken{
	// 		EntityId:    "V9OT22mI0a",
	// 		Id:          "hwsF9-4etJ4",
	// 		IssuedAt:    time.Now(),
	// 		Name:        "test-secret-key",
	// 		Permissions: 24,
	// 		Secret:      []byte("EU8E011iX2Bp5lv481N2STd1d999cU58"),
	// 	},
	// 	accesstoken.AccessToken{
	// 		EntityId:    "V9OT22mI0a",
	// 		Id:          "GjpBDnTuDUk",
	// 		IssuedAt:    time.Now(),
	// 		Name:        "test-published-key",
	// 		Permissions: 4503617075675176,
	// 		Secret:      []byte("EU8E011iX2Bp5lv481N2STd1d999cU58"),
	// 	},
	// }

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "LuDela",
		Address: "hi@ludela.com",
	}

	org.Email.Order.Confirmation.Subject = "LuDela Order Confirmation"
	org.Email.Order.Confirmation.Enabled = true

	// Save org into default namespace
	org.Put()

	// Save namespace so we can decode keys for this organization later
	ns := namespace.New(db)
	ns.Name = org.Name
	ns.GetOrCreate("Name=", ns.Name)
	ns.IntId = org.Key().IntID()
	ns.Update()

	// Create namespaced context
	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create new store
	stor := store.New(nsdb)
	stor.Name = "default"
	stor.GetOrCreate("Name=", stor.Name)
	stor.SetKey("ldt6eeKINN5")
	stor.Prefix = "/"
	stor.Currency = currency.USD
	stor.Mailchimp.APIKey = ""
	stor.Mailchimp.ListId = "262350bdb1"
	stor.MustUpdate()

	// Set default store on org
	org.DefaultStore = stor.Id()

	// Create smart candle
	prod := product.New(nsdb)
	prod.Slug = "ludela"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.SetKey("Knc9wlZJUOOG")
	prod.Name = "LuDela Candle"
	prod.Description = "Includes: One (1) LuDela Smart Candle, Ivory Color, One (1) 100% Natural Soy-Beeswax Refill (30-hour burn time). Special Offer: One-month trial subscription to LuDela’s EssentialRefill Program of two (2) 30-hour refills per month, per LuDela ordered. $9.99 per month per LuDela thereafter. Modify or cancel anytime."
	prod.Currency = currency.USD
	prod.ListPrice = currency.Cents(19900)
	prod.Price = currency.Cents(9900)
	prod.Preorder = true
	prod.Hidden = false
	prod.EstimatedDelivery = "Early 2017"
	prod.Update()

	// Create discount rules for ludela
	dis := discount.New(db)
	dis.Name = "LuDela Bulk Discount"
	dis.GetOrCreate("Name=", dis.Name)
	dis.Scope.Type = scope.Product
	dis.Scope.ProductId = prod.Id()
	dis.Target.Type = target.Product
	dis.Target.ProductId = prod.Id()

	// Create Jamie's rules
	rule1 := discount.Rule{
		Trigger: rule.Trigger{
			Quantity: rule.Quantity{
				Start: 2,
			},
		},
		Action: rule.Action{
			Discount: rule.Discount{
				Flat: 5,
			},
		},
	}
	// aka...
	rule1.Trigger.Quantity.Start = 2
	rule1.Action.Discount.Flat = 5

	rule2 := discount.Rule{}
	rule2.Trigger.Quantity.Start = 3
	rule2.Action.Discount.Flat = 16

	dis.Rules = []discount.Rule{rule1, rule2}
	dis.Update()

	// Save entities
	usr.MustUpdate()
	org.MustUpdate()
	stor.MustUpdate()
	prod.MustUpdate()

	// Create corresponding Mailchimp entities
	client := mailchimp.New(db.Context, org.Mailchimp)
	client.CreateStore(stor)
	client.CreateProduct(stor.Id(), prod)

	return org
})
