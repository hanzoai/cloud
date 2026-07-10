package fixtures

import (
	// "time"
	"bytes"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("karma", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "karma"
	org.GetOrCreate("Name=", org.Name)

	usr := user.New(db)
	usr.Email = "karma@hanzo.ai"
	usr.GetOrCreate("Email=", usr.Email)
	usr.FirstName = "karma"
	usr.LastName = ""
	usr.Organizations = []string{org.Id()}
	usr.PasswordHash, _ = password.Hash("pp2karma!zO")
	usr.MustUpdate()

	org.FullName = "Karma Inc"
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://karmabikinis.online"}}
	org.EmailWhitelist = "*.hanzo.ai *.karmabikinis.online"
	if bytes.Compare(org.SecretKey, []byte("1gML2pOHK4PW8xMc")) != 0 {
		org.SecretKey = []byte("1gML2pOHK4PW8xMc")
		org.AddDefaultTokens()
	}

	org.Fees.Card.Flat = 0
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 50
	org.Fees.Affiliate.Percent = 0.30

	// org.Mailchimp.APIKey = ""
	// org.Mailchimp.ListId = "7849878695"

	// Email configuration
	// org.Mandrill.APIKey = ""

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "Karma",
		Address: "hi@karmabikinis.online",
	}

	// org.Email.Order.Confirmation.Subject = "karma Earphones Order Confirmation"
	// org.Email.Order.Confirmation.HTML = readEmailTemplate("/resources/karma/emails/order-confirmation.html")
	// org.Email.Order.Confirmation.Enabled = true

	// Save org into default namespace
	org.MustUpdate()

	// Save namespace so we can decode keys for this organization later
	ns := namespace.New(db)
	ns.Name = org.Name
	ns.GetOrCreate("Name=", ns.Name)
	ns.IntId = org.Key().IntID()
	ns.MustUpdate()

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create default store
	stor := store.New(nsdb)
	stor.Name = "Website"
	stor.GetOrCreate("Name=", stor.Name)
	stor.Prefix = "/"
	stor.Currency = currency.USD
	// stor.Mailchimp.APIKey = ""
	// stor.Mailchimp.ListId = "7849878695"
	stor.MustUpdate()

	{
		prod := product.New(nsdb)
		prod.Slug = "postcard"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Postcard"
		prod.Description = "We will select one of our best shots to make into a postcard with a hand written appreciation note from our designer."
		prod.Price = currency.Cents(1000)
		prod.ListPrice = currency.Cents(1000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "mug"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Good Karma mug"
		prod.Description = "A sweet reminder of our gratitude along with every morning cup o' joe."
		prod.Price = currency.Cents(1000)
		prod.ListPrice = currency.Cents(1000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "less-boring"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Less Boring Summer Mask"
		prod.Description = "Stay healthy and chic with our sustainable yet lightweight protective measures. Choose from our 2 prints (dragon blossom and trippy leopard) all made from recycled fish nets."
		prod.Price = currency.Cents(3500)
		prod.ListPrice = currency.Cents(3500)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "karma-collab-t"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Karma collaboration T"
		prod.Description = "100% recycled cotton. We have curated an epic design that we are proud to call our first Karma graphic-t. Choose between Men/Women sizing."
		prod.Price = currency.Cents(5000)
		prod.ListPrice = currency.Cents(5000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "mystery-bikini"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Mystery Bikini"
		prod.Description = "We choose a sustainable suit in your size. Styles may vary from all the products on our website. (includes top size, product selection, includes bottom size product selection)"
		prod.Price = currency.Cents(8000)
		prod.ListPrice = currency.Cents(8000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "mens-trunks"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Karma Men’s Swim Trunks"
		prod.Description = "Eccentric enough to be cool. When you wear these you will stand out, but never stand alone. Functional and versatile with 2 pockets for convenient storage. Made from our ultra soft Italian Carvico fabric."
		prod.Price = currency.Cents(8000)
		prod.ListPrice = currency.Cents(8000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "dial-a-backer"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Dial-a-backer"
		prod.Description = "Schedule some time for a 30-minute phone call with our designer, ask for tips on making a project happen, fashion design, sustainability or even just about their day."
		prod.Price = currency.Cents(12000)
		prod.ListPrice = currency.Cents(12000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "karma-bikini"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Karma Bikini"
		prod.Description = "Sustainable, chic, lightweight and made from recycled fish nets. All sales from every piece in our Less Boring Summer Collection directly contribute towards our mission to create a fully sustainable supply chain that empowers disadvantaged Women globally. Choose a suit from any piece in our Less Boring Summer Collection."
		prod.Price = currency.Cents(20000)
		prod.ListPrice = currency.Cents(20000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "trikini"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Trikini"
		prod.Description = "Guess what it’s 2020 and the only way to look cute and safe at the beach is with your bikini and mask, a.k.a. the tri-kini matching set. Choose a bikini style from the Less Boring Summer Collection and any mask. Available in our Trippy Leopard print/Dragon Blossom print."
		prod.Price = currency.Cents(23500)
		prod.ListPrice = currency.Cents(23500)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "save-30"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "save-30"
		prod.Description = "Enjoy some good old fashioned retail therapy and support our mission, get $1000 worth of credit applicable to anything on our site for just $700. The code lasts forever and you can use the remaining amount to purchase items from our future collections."
		prod.Price = currency.Cents(70000)
		prod.ListPrice = currency.Cents(70000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "custom-designs"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Custom bikini designed and named after you"
		prod.Description = "Work alongside our designer and design the outfits of your dreams. We will work with you to create the best possible construction, fit and design."
		prod.Price = currency.Cents(100000)
		prod.ListPrice = currency.Cents(100000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "5-custom-designs"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "5 Custom designed bikinis (or shorts)"
		prod.Description = "Work together with our designer to design the most fitted swim wear you will ever have while incorporating unique prints that will make every hot day pool party worthy."
		prod.Price = currency.Cents(100000)
		prod.ListPrice = currency.Cents(100000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "sponsor"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Sponsor a Karma Shoot"
		prod.Description = "Let us have our next photoshoot at your place or another glamorous location. We will hire the best team of creatives and make beautiful art all sponsored by you! Get behind the scenes as you watch the magic happen. Each shoot sponsored by x) We will create a page on our site that will showcase all the sponsored shoots with a dedication to you!"
		prod.Price = currency.Cents(500000)
		prod.ListPrice = currency.Cents(500000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "capsule-collection"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Capsule Collection"
		prod.Description = "Co-create a collection of sustainable swim and resort wear. Be the face of your own brand with Karma. Karma x You. A complete collection of 10 seperate pieces tailored and designed just for you. Work alongside our team and earn 20% of all future revenue from your Capsule Collection."
		prod.Price = currency.Cents(1500000)
		prod.ListPrice = currency.Cents(1500000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "louge-wear"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Fund our Loungewear Collection"
		prod.Description = "We are making a collection of Bamboo Lyocell which is almost 100% sustainable. The extended range will go beyond swimwear and carry products like dresses, tanks, skirts, pants and even some Men’s recycled linen suits. You will in turn earn 20% of all sales made from the first two years."
		prod.Price = currency.Cents(4000000)
		prod.ListPrice = currency.Cents(4000000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	{
		prod := product.New(nsdb)
		prod.Slug = "travel"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Fund a sustainable women run operation in Morocco"
		prod.Description = "Help support our mission as we expand production to Morocco. We will employ women in our new production house, giving them access to work which otherwise is unavailable. We will use the most sustainable techniques, materials and sourcing which are at least 99% sustainable. Additionally we will be able to offer childcare to our workers and produce our extended range of lounge wear!"
		prod.Price = currency.Cents(10000000)
		prod.ListPrice = currency.Cents(10000000)
		prod.Inventory = 9000
		prod.Preorder = true
		prod.MustUpdate()
	}

	return org
})
