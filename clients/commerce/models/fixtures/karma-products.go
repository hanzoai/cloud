package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

type size struct {
	name string
	id   string
}

var _ = New("karma-products", func(c *gin.Context) []*product.Product {
	db := datastore.New(c)

	org := organization.New(db)
	org.Query().Filter("Name=", "karma").Get()

	nsdb := datastore.New(org.Namespaced(db.Context))

	SIZES_BOTH_GENDERS := []size{
		size{
			name: "XS (Women's)",
			id:   "W-XS",
		},
		size{
			name: "S (Women's)",
			id:   "W-S",
		},
		size{
			name: "M (Women's)",
			id:   "W-M",
		},
		size{
			name: "L (Women's)",
			id:   "W-L",
		},
		size{
			name: "XL (Women's)",
			id:   "W-XL",
		},
		size{
			name: "XXL (Women's)",
			id:   "W-XXL",
		},
		size{
			name: "XS (Men's)",
			id:   "M-XS",
		},
		size{
			name: "S (Men's)",
			id:   "M-S",
		},
		size{
			name: "M (Men's)",
			id:   "M-M",
		},
		size{
			name: "L (Men's)",
			id:   "M-L",
		},
		size{
			name: "XL (Men's)",
			id:   "M-XL",
		},
		size{
			name: "XXL (Men's)",
			id:   "M-XXL",
		},
	}

	SIZES := []size{
		size{
			id:   "XS",
			name: "XS",
		},
		size{
			id:   "S",
			name: "S",
		},
		size{
			id:   "M",
			name: "M",
		},
		size{
			id:   "L",
			name: "L",
		},
		size{
			id:   "XL",
			name: "XL",
		},
		size{
			id:   "XXL",
			name: "XXL",
		},
	}

	SIZES_W_SHORTS_ONLY := []size{
		size{
			id:   "na",
			name: "(Shorts only)",
		},
		size{
			id:   "XS",
			name: "XS",
		},
		size{
			id:   "S",
			name: "S",
		},
		size{
			id:   "M",
			name: "M",
		},
		size{
			id:   "L",
			name: "L",
		},
		size{
			id:   "XL",
			name: "XL",
		},
		size{
			id:   "XXL",
			name: "XXL",
		},
	}

	TRUNK_STYLES := []size{
		size{
			name: "Trippy Leopard",
			id:   "trippy-leopard",
		},
		size{
			name: "Dragon Blossom",
			id:   "dragon-blossom",
		},
	}

	BIKINI_STYLES := []size{
		size{
			name: "Railay Bikini in trippy leopard",
			id:   "railay-tl",
		},
		size{
			name: "Railay Bikini in dragon blossom",
			id:   "railay-db",
		},
		size{
			name: "Ruby Bikini in trippy leopard",
			id:   "ruby-tl",
		},
		size{
			name: "Ruby Bikini in dragon blossom",
			id:   "ruby-db",
		},
		size{
			name: "Lafayette Bikini in trippy leopard",
			id:   "lafayette-tl",
		},
		size{
			name: "Lafayette Bikini in dragon blossom",
			id:   "lafayette-db",
		},
		size{
			name: "Bikini-n-Chill Bikini in trippy leopard",
			id:   "bikini-n-chill-tl",
		},
		size{
			name: "Bikini-n-Chill Bikini in dragon blossom",
			id:   "bikini-n-chill-db",
		},
	}

	MASK_STYLES := []size{
		size{
			name: "Dragon Blossom",
			id:   "mask-db",
		},
		size{
			name: "Trippy Leopard",
			id:   "mask-tl",
		},
	}

	prods := []*product.Product{}

	for _, s := range MASK_STYLES {
		prod := product.New(nsdb)
		prod.Slug = "less-boring-" + s.id
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Less Boring Summer Mask " + s.name
		prod.Description = "Stay healthy and chic with our sustainable yet lightweight protective measures. Choose from our 2 prints (dragon blossom and trippy leopard) all made from recycled fish nets."
		prod.Currency = currency.USD
		prod.ListPrice = currency.Cents(3500)
		prod.Price = currency.Cents(3500)
		prod.Update()

		prods = append(prods, prod)
	}

	for _, s := range SIZES_BOTH_GENDERS {
		prod := product.New(nsdb)
		prod.Slug = "karma-collab-t-" + s.id
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Karma collaboration Tee " + s.name
		prod.Description = "100% recycled cotton. We have curated an epic design that we are proud to call our first Karma graphic-t. Choose between Men/Women sizing."
		prod.Currency = currency.USD
		prod.ListPrice = currency.Cents(5000)
		prod.Price = currency.Cents(5000)
		prod.Update()

		prods = append(prods, prod)
	}

	for _, s1 := range SIZES {
		for _, s2 := range SIZES {
			prod := product.New(nsdb)
			prod.Slug = "mystery-bikini-" + s1.id + "-" + s2.id
			prod.GetOrCreate("Slug=", prod.Slug)
			prod.Name = "Mystery Bikini " + s1.name + " Top " + s2.name + " Bottom"
			prod.Description = "We choose a sustainable suit in your size. Styles may vary from all the products on our website. (includes top size, product selection, includes bottom size product selection)"
			prod.Currency = currency.USD
			prod.ListPrice = currency.Cents(8000)
			prod.Price = currency.Cents(8000)
			prod.Update()

			prods = append(prods, prod)
		}
	}

	for _, s1 := range TRUNK_STYLES {
		for _, s2 := range SIZES {
			prod := product.New(nsdb)
			prod.Slug = "mens-trunks-" + s1.id + "-" + s2.id
			prod.GetOrCreate("Slug=", prod.Slug)
			prod.Name = "Karma Men’s Swim Trunks " + s1.name + " " + s2.name
			prod.Description = "Eccentric enough to be cool. When you wear these you will stand out, but never stand alone. Functional and versatile with 2 pockets for convenient storage. Made from our ultra soft Italian Carvico fabric."
			prod.Currency = currency.USD
			prod.ListPrice = currency.Cents(8000)
			prod.Price = currency.Cents(8000)
			prod.Update()

			prods = append(prods, prod)
		}
	}

	for _, s1 := range SIZES_W_SHORTS_ONLY {
		for _, s2 := range SIZES {
			prod := product.New(nsdb)
			prod.Slug = "5-custom-designs-" + s1.id + "-" + s2.id
			prod.GetOrCreate("Slug=", prod.Slug)
			prod.Name = "5 Custom designed bikinis " + s1.name + " " + s2.name
			prod.Description = "Work together with our designer to design the most fitted swim wear you will ever have while incorporating unique prints that will make every hot day pool party worthy."
			prod.Currency = currency.USD
			prod.ListPrice = currency.Cents(200000)
			prod.Price = currency.Cents(200000)
			prod.Update()

			prods = append(prods, prod)
		}
	}

	for _, s1 := range SIZES {
		for _, s2 := range SIZES {
			prod := product.New(nsdb)
			prod.Slug = "capsule-collection-" + s1.id + "-" + s2.id
			prod.GetOrCreate("Slug=", prod.Slug)
			prod.Name = "Capsule Collection " + s1.name + " " + s2.name
			prod.Description = "Co-create a collection of sustainable swim and resort wear. Be the face of your own brand with Karma. Karma x You.\n" +
				"A complete collection of 10 seperate pieces tailored and designed just for you.\n" +
				"Work alongside our team and earn 20% of all future revenue from your Capsule Collection."
			prod.Currency = currency.USD
			prod.ListPrice = currency.Cents(1500000)
			prod.Price = currency.Cents(1500000)
			prod.Update()

			prods = append(prods, prod)
		}
	}

	for _, s1 := range BIKINI_STYLES {
		for _, s2 := range SIZES {
			for _, s3 := range SIZES {
				prod := product.New(nsdb)
				prod.Slug = "karma-bikini-" + s1.id + "-" + s2.id + "-" + s3.id
				prod.GetOrCreate("Slug=", prod.Slug)
				prod.Name = "Karma Bikini " + s1.name + " " + s2.name + " Top " + s3.name + " Bottom"
				prod.Description = "Sustainable, chic, lightweight and made from recycled fish nets. All sales from every piece in our Less Boring Summer Collection directly contribute towards our mission to create a fully sustainable supply chain that empowers disadvantaged Women globally. Choose a suit from any piece in our Less Boring Summer Collection."
				prod.Currency = currency.USD
				prod.ListPrice = currency.Cents(20000)
				prod.Price = currency.Cents(20000)
				prod.Update()

				prods = append(prods, prod)
			}
		}
	}

	return prods
})
