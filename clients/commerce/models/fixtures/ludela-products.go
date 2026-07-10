package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var _ = New("ludela-products", func(c *gin.Context) []*product.Product {
	db := datastore.New(c)

	org := organization.New(db)
	org.Query().Filter("Name=", "ludela").Get()

	nsdb := datastore.New(org.Namespaced(db.Context))

	// Create smart candle
	prod1 := product.New(nsdb)
	prod1.Slug = "Solo-W-I"
	prod1.GetOrCreate("Slug=", prod1.Slug)
	prod1.SetKey("pocm406muPPD")
	prod1.Name = "Solo Starter Kit, Ivory Wax Shell"
	prod1.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-W-I.png", X: 500, Y: 500}
	prod1.Description = "Includes: One (1) LuDela Smart Candle, Ivory Color, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod1.Currency = currency.USD
	prod1.ListPrice = currency.Cents(19900)
	prod1.Price = currency.Cents(9900)
	prod1.Preorder = true
	prod1.Hidden = false
	prod1.EstimatedDelivery = "Early 2017"
	prod1.Update()

	prod2 := product.New(nsdb)
	prod2.Slug = "Solo-W-O"
	prod2.GetOrCreate("Slug=", prod2.Slug)
	prod2.SetKey("rbcrWqzskky")
	prod2.Name = "Solo Starter Kit, Orange Wax Shell Upgrade"
	prod2.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-W-O.png", X: 500, Y: 500}
	prod2.Description = "Includes: One (1) LuDela Smart Candle, Orange Color, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod2.Currency = currency.USD
	prod2.ListPrice = currency.Cents(22900)
	prod2.Price = currency.Cents(11900)
	prod2.Preorder = true
	prod2.Hidden = false
	prod2.EstimatedDelivery = "Early 2017"
	prod2.Update()

	prod3 := product.New(nsdb)
	prod3.Slug = "Solo-W-R"
	prod3.GetOrCreate("Slug=", prod3.Slug)
	prod3.SetKey("7xcwZOeOt88E")
	prod3.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-W-R.png", X: 500, Y: 500}
	prod3.Name = "Solo Starter Kit, Red Wax Shell Upgrade"
	prod3.Description = "Includes: One (1) LuDela Smart Candle, Red Color, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod3.Currency = currency.USD
	prod3.ListPrice = currency.Cents(22900)
	prod3.Price = currency.Cents(11900)
	prod3.Preorder = true
	prod3.Hidden = false
	prod3.EstimatedDelivery = "Early 2017"
	prod3.Update()

	prod4 := product.New(nsdb)
	prod4.Slug = "Solo-W-B"
	prod4.GetOrCreate("Slug=", prod4.Slug)
	prod4.SetKey("qGcZqWKNiWWd")
	prod4.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-W-B.png", X: 500, Y: 500}
	prod4.Name = "Solo Starter Kit, Blue Wax Shell Upgrade"
	prod4.Description = "Includes: One (1) LuDela Smart Candle, Blue Color, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod4.Currency = currency.USD
	prod4.ListPrice = currency.Cents(22900)
	prod4.Price = currency.Cents(11900)
	prod4.Preorder = true
	prod4.Hidden = false
	prod4.EstimatedDelivery = "Early 2017"
	prod4.Update()

	prod5 := product.New(nsdb)
	prod5.Slug = "Solo-W-G"
	prod5.GetOrCreate("Slug=", prod5.Slug)
	prod5.SetKey("69cDlzA7I88g")
	prod5.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-W-G.png", X: 500, Y: 500}
	prod5.Name = "Solo Starter Kit, Green Wax Shell Upgrade"
	prod5.Description = "Includes: One (1) LuDela Smart Candle, Green Color, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod5.Currency = currency.USD
	prod5.ListPrice = currency.Cents(22900)
	prod5.Price = currency.Cents(11900)
	prod5.Preorder = true
	prod5.Hidden = false
	prod5.EstimatedDelivery = "Early 2017"
	prod5.Update()

	prod6 := product.New(nsdb)
	prod6.Slug = "Solo-W-P"
	prod6.GetOrCreate("Slug=", prod6.Slug)
	prod6.SetKey("XXcDZddF44A")
	prod6.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-W-P.png", X: 500, Y: 500}
	prod6.Name = "Solo Starter Kit, Purple Wax Shell Upgrade"
	prod6.Description = "Includes: One (1) LuDela Smart Candle, Purple Color, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod6.Currency = currency.USD
	prod6.ListPrice = currency.Cents(22900)
	prod6.Price = currency.Cents(11900)
	prod6.Preorder = true
	prod6.Hidden = false
	prod6.EstimatedDelivery = "Early 2017"
	prod6.Update()

	prod7 := product.New(nsdb)
	prod7.Slug = "Solo-G-B"
	prod7.GetOrCreate("Slug=", prod7.Slug)
	prod7.SetKey("vyc2xmoNI11k")
	prod7.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-G-B.png", X: 500, Y: 500}
	prod7.Name = "Solo Starter Kit, Black Glass Shell Upgrade"
	prod7.Description = "Includes: One (1) LuDela Smart Candle, Black Color, Glass Shell, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod7.Currency = currency.USD
	prod7.ListPrice = currency.Cents(24900)
	prod7.Price = currency.Cents(12900)
	prod7.Preorder = true
	prod7.Hidden = false
	prod7.EstimatedDelivery = "Early 2017"
	prod7.Update()

	prod8 := product.New(nsdb)
	prod8.Slug = "Solo-G-W"
	prod8.GetOrCreate("Slug=", prod8.Slug)
	prod8.SetKey("lqc2g6N8cNN5")
	prod8.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-G-W.png", X: 500, Y: 500}
	prod8.Name = "Solo Starter Kit, White Glass Shell Upgrade"
	prod8.Description = "Includes: One (1) LuDela Smart Candle, White Color, Glass Shell, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod8.Currency = currency.USD
	prod8.ListPrice = currency.Cents(24900)
	prod8.Price = currency.Cents(12900)
	prod8.Preorder = true
	prod8.Hidden = false
	prod8.EstimatedDelivery = "Early 2017"
	prod8.Update()

	prod9 := product.New(nsdb)
	prod9.Slug = "Solo-G-R"
	prod9.GetOrCreate("Slug=", prod9.Slug)
	prod9.SetKey("eAcpyWm5Hmmd")
	prod9.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-G-R.png", X: 500, Y: 500}
	prod9.Name = "Solo Starter Kit, Red Glass Shell Upgrade"
	prod9.Description = "Includes: One (1) LuDela Smart Candle, Red Color, Glass Shell, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod9.Currency = currency.USD
	prod9.ListPrice = currency.Cents(24900)
	prod9.Price = currency.Cents(12900)
	prod9.Preorder = true
	prod9.Hidden = false
	prod9.EstimatedDelivery = "Early 2017"
	prod9.Update()

	prod10 := product.New(nsdb)
	prod10.Slug = "Solo-G-SM"
	prod10.GetOrCreate("Slug=", prod10.Slug)
	prod10.SetKey("wycJRWyIBBA")
	prod10.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-G-SM.png", X: 500, Y: 500}
	prod10.Name = "Solo Starter Kit, Silver Mercury Shell Upgrade"
	prod10.Description = "Includes: One (1) LuDela Smart Candle, Silver Mercury Shell, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod10.Currency = currency.USD
	prod10.ListPrice = currency.Cents(24900)
	prod10.Price = currency.Cents(12900)
	prod10.Preorder = true
	prod10.Hidden = false
	prod10.EstimatedDelivery = "Early 2017"
	prod10.Update()

	prod11 := product.New(nsdb)
	prod11.Slug = "Solo-G-BM"
	prod11.GetOrCreate("Slug=", prod11.Slug)
	prod11.SetKey("j8cYgR6ltppW")
	prod11.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Solo-G-BM.png", X: 500, Y: 500}
	prod11.Name = "Solo Starter Kit, Bronze Mercury Shell Upgrade"
	prod11.Description = "Includes: One (1) LuDela Smart Candle, Bronze Mercury Shell, Two (2) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod11.Currency = currency.USD
	prod11.ListPrice = currency.Cents(24900)
	prod11.Price = currency.Cents(12900)
	prod11.Preorder = true
	prod11.Hidden = false
	prod11.EstimatedDelivery = "Early 2017"
	prod11.Update()

	prod1d := product.New(nsdb)
	prod1d.Slug = "Duo-W-I"
	prod1d.GetOrCreate("Slug=", prod1d.Slug)
	prod1d.SetKey("yjceyBvtWW8")
	prod1d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-W-I.png", X: 500, Y: 500}
	prod1d.Name = "Duo Starter Kit, Ivory Wax Shell"
	prod1d.Description = "Includes: Two (2) LuDela Smart Candle, Ivory Color, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod1d.Currency = currency.USD
	prod1d.ListPrice = currency.Cents(39800)
	prod1d.Price = currency.Cents(18900)
	prod1d.Preorder = true
	prod1d.Hidden = false
	prod1d.EstimatedDelivery = "Early 2017"
	prod1d.Update()

	prod2d := product.New(nsdb)
	prod2d.Slug = "Duo-W-O"
	prod2d.GetOrCreate("Slug=", prod2d.Slug)
	prod2d.SetKey("D1cDEN0zfOOE")
	prod2d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-W-O.png", X: 500, Y: 500}
	prod2d.Name = "Duo Starter Kit, Orange Wax Shell Upgrade"
	prod2d.Description = "Includes: Two (2) LuDela Smart Candle, Orange Color, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod2d.Currency = currency.USD
	prod2d.ListPrice = currency.Cents(45900)
	prod2d.Price = currency.Cents(22900)
	prod2d.Preorder = true
	prod2d.Hidden = false
	prod2d.EstimatedDelivery = "Early 2017"
	prod2d.Update()

	prod3d := product.New(nsdb)
	prod3d.Slug = "Duo-W-R"
	prod3d.GetOrCreate("Slug=", prod3d.Slug)
	prod3d.SetKey("qGcZqAwySWWd")
	prod3d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-W-R.png", X: 500, Y: 500}
	prod3d.Name = "Duo Starter Kit, Red Wax Shell Upgrade"
	prod3d.Description = "Includes: Two (2) LuDela Smart Candle, Red Color, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod3d.Currency = currency.USD
	prod3d.ListPrice = currency.Cents(45900)
	prod3d.Price = currency.Cents(22900)
	prod3d.Preorder = true
	prod3d.Hidden = false
	prod3d.EstimatedDelivery = "Early 2017"
	prod3d.Update()

	prod4d := product.New(nsdb)
	prod4d.Slug = "Duo-W-B"
	prod4d.GetOrCreate("Slug=", prod4d.Slug)
	prod4d.SetKey("mOck5kpDsJJp")
	prod4d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-W-B.png", X: 500, Y: 500}
	prod4d.Name = "Duo Starter Kit, Blue Wax Shell Upgrade"
	prod4d.Description = "Includes: Two (2) LuDela Smart Candle, Blue Color, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod4d.Currency = currency.USD
	prod4d.ListPrice = currency.Cents(45900)
	prod4d.Price = currency.Cents(22900)
	prod4d.Preorder = true
	prod4d.Hidden = false
	prod4d.EstimatedDelivery = "Early 2017"
	prod4d.Update()

	prod5d := product.New(nsdb)
	prod5d.Slug = "Duo-W-G"
	prod5d.GetOrCreate("Slug=", prod5d.Slug)
	prod5d.SetKey("mOckJOA1cJJp")
	prod5d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-W-G.png", X: 500, Y: 500}
	prod5d.Name = "Duo Starter Kit, Green Wax Shell Upgrade"
	prod5d.Description = "Includes: Two (2) LuDela Smart Candle, Green Color, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod5d.Currency = currency.USD
	prod5d.ListPrice = currency.Cents(45900)
	prod5d.Price = currency.Cents(22900)
	prod5d.Preorder = true
	prod5d.Hidden = false
	prod5d.EstimatedDelivery = "Early 2017"
	prod5d.Update()

	prod6d := product.New(nsdb)
	prod6d.Slug = "Duo-W-P"
	prod6d.GetOrCreate("Slug=", prod6d.Slug)
	prod6d.SetKey("XXcDv5Pc44A")
	prod6d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-W-P.png", X: 500, Y: 500}
	prod6d.Name = "Duo Starter Kit, Purple Wax Shell Upgrade"
	prod6d.Description = "Includes: Two (2) LuDela Smart Candle, Purple Color, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod6d.Currency = currency.USD
	prod6d.ListPrice = currency.Cents(45900)
	prod6d.Price = currency.Cents(22900)
	prod6d.Preorder = true
	prod6d.Hidden = false
	prod6d.EstimatedDelivery = "Early 2017"
	prod6d.Update()

	prod7d := product.New(nsdb)
	prod7d.Slug = "Duo-G-B"
	prod7d.GetOrCreate("Slug=", prod7d.Slug)
	prod7d.SetKey("Owckx62luooO")
	prod7d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-G-B.png", X: 500, Y: 500}
	prod7d.Name = "Duo Starter Kit, Black Glass Shell Upgrade"
	prod7d.Description = "Includes: Two (2) LuDela Smart Candle, Black Color, Glass Shell, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod7d.Currency = currency.USD
	prod7d.ListPrice = currency.Cents(47900)
	prod7d.Price = currency.Cents(24900)
	prod7d.Preorder = true
	prod7d.Hidden = false
	prod7d.EstimatedDelivery = "Early 2017"
	prod7d.Update()

	prod8d := product.New(nsdb)
	prod8d.Slug = "Duo-G-W"
	prod8d.GetOrCreate("Slug=", prod8d.Slug)
	prod8d.SetKey("Owck7Kg2FooO")
	prod8d.Name = "Duo Starter Kit, White Glass Shell Upgrade"
	prod8d.Description = "Includes: Two (2) LuDela Smart Candle, White Color, Glass Shell, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod8d.Currency = currency.USD
	prod8d.ListPrice = currency.Cents(47900)
	prod8d.Price = currency.Cents(24900)
	prod8d.Preorder = true
	prod8d.Hidden = false
	prod8d.EstimatedDelivery = "Early 2017"
	prod8d.Update()

	prod9d := product.New(nsdb)
	prod9d.Slug = "Duo-G-R"
	prod9d.GetOrCreate("Slug=", prod9d.Slug)
	prod9d.SetKey("lqcQ8OofNN5")
	prod9d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-G-R.png", X: 500, Y: 500}
	prod9d.Name = "Duo Starter Kit, Red Glass Shell Upgrade"
	prod9d.Description = "Includes: Two (2) LuDela Smart Candle, Red Color, Glass Shell, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod9d.Currency = currency.USD
	prod9d.ListPrice = currency.Cents(47900)
	prod9d.Price = currency.Cents(24900)
	prod9d.Preorder = true
	prod9d.Hidden = false
	prod9d.EstimatedDelivery = "Early 2017"
	prod9d.Update()

	prod10d := product.New(nsdb)
	prod10d.Slug = "Duo-G-SM"
	prod10d.GetOrCreate("Slug=", prod10d.Slug)
	prod10d.SetKey("vycRGOZh11k")
	prod10d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-G-SM.png", X: 500, Y: 500}
	prod10d.Name = "Duo Starter Kit, Silver Mercury Shell Upgrade"
	prod10d.Description = "Includes: Two (2) LuDela Smart Candle, Silver Mercury Shell, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod10d.Currency = currency.USD
	prod10d.ListPrice = currency.Cents(47900)
	prod10d.Price = currency.Cents(24900)
	prod10d.Preorder = true
	prod10d.Hidden = false
	prod10d.EstimatedDelivery = "Early 2017"
	prod10d.Update()

	prod11d := product.New(nsdb)
	prod11d.Slug = "Duo-G-BM"
	prod11d.GetOrCreate("Slug=", prod11d.Slug)
	prod11d.SetKey("84cy8q8I99w")
	prod11d.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Duo-G-BM.png", X: 500, Y: 500}
	prod11d.Name = "Duo Starter Kit, Bronze Mercury Shell Upgrade"
	prod11d.Description = "Includes: Two (2) LuDela Smart Candle, Bronze Mercury Shell, Four (4) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod11d.Currency = currency.USD
	prod11d.ListPrice = currency.Cents(47900)
	prod11d.Price = currency.Cents(24900)
	prod11d.Preorder = true
	prod11d.Hidden = false
	prod11d.EstimatedDelivery = "Early 2017"
	prod11d.Update()

	prod1t := product.New(nsdb)
	prod1t.Slug = "Trio-W-I"
	prod1t.GetOrCreate("Slug=", prod1t.Slug)
	prod1t.SetKey("KncZdBbCOOG")
	prod1t.Name = "Trio Starter Kit, Ivory Wax Shell"
	prod1t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-W-I.png", X: 500, Y: 500}
	prod1t.Description = "Includes: Three (3) LuDela Smart Candle, Ivory Color, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod1t.Currency = currency.USD
	prod1t.ListPrice = currency.Cents(59700)
	prod1t.Price = currency.Cents(27900)
	prod1t.Preorder = true
	prod1t.Hidden = false
	prod1t.EstimatedDelivery = "Early 2017"
	prod1t.Update()

	prod2t := product.New(nsdb)
	prod2t.Slug = "Trio-W-O"
	prod2t.GetOrCreate("Slug=", prod2t.Slug)
	prod2t.SetKey("Knc9xmxOiOOG")
	prod2t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-W-O.png", X: 500, Y: 500}
	prod2t.Name = "Trio Starter Kit, Orange Wax Shell Upgrade"
	prod2t.Description = "Includes: Three (3) LuDela Smart Candle, Orange Color, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod2t.Currency = currency.USD
	prod2t.ListPrice = currency.Cents(65900)
	prod2t.Price = currency.Cents(32900)
	prod2t.Preorder = true
	prod2t.Hidden = false
	prod2t.EstimatedDelivery = "Early 2017"
	prod2t.Update()

	prod3t := product.New(nsdb)
	prod3t.Slug = "Trio-W-R"
	prod3t.GetOrCreate("Slug=", prod3t.Slug)
	prod3t.SetKey("wycJj06tBBA")
	prod3t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-W-R.png", X: 500, Y: 500}
	prod3t.Name = "Trio Starter Kit, Red Wax Shell Upgrade"
	prod3t.Description = "Includes: Three (3) LuDela Smart Candle, Red Color, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod3t.Currency = currency.USD
	prod3t.ListPrice = currency.Cents(65900)
	prod3t.Price = currency.Cents(32900)
	prod3t.Preorder = true
	prod3t.Hidden = false
	prod3t.EstimatedDelivery = "Early 2017"
	prod3t.Update()

	prod4t := product.New(nsdb)
	prod4t.Slug = "Trio-W-B"
	prod4t.GetOrCreate("Slug=", prod4t.Slug)
	prod4t.SetKey("69cDlBe6c88g")
	prod4t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-W-B.png", X: 500, Y: 500}
	prod4t.Name = "Trio Starter Kit, Blue Wax Shell Upgrade"
	prod4t.Description = "Includes: Three (3) LuDela Smart Candle, Blue Color, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod4t.Currency = currency.USD
	prod4t.ListPrice = currency.Cents(65900)
	prod4t.Price = currency.Cents(32900)
	prod4t.Preorder = true
	prod4t.Hidden = false
	prod4t.EstimatedDelivery = "Early 2017"
	prod4t.Update()

	prod5t := product.New(nsdb)
	prod5t.Slug = "Trio-W-G"
	prod5t.GetOrCreate("Slug=", prod5t.Slug)
	prod5t.SetKey("D1cDEJ78sOOE")
	prod5t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-W-G.png", X: 500, Y: 500}
	prod5t.Name = "Trio Starter Kit, Green Wax Shell Upgrade"
	prod5t.Description = "Includes: Three (3) LuDela Smart Candle, Green Color, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod5t.Currency = currency.USD
	prod5t.ListPrice = currency.Cents(65900)
	prod5t.Price = currency.Cents(32900)
	prod5t.Preorder = true
	prod5t.Hidden = false
	prod5t.EstimatedDelivery = "Early 2017"
	prod5t.Update()

	prod6t := product.New(nsdb)
	prod6t.Slug = "Trio-W-P"
	prod6t.GetOrCreate("Slug=", prod6t.Slug)
	prod6t.SetKey("mOcdyZEuJJp")
	prod6t.Name = "Trio Starter Kit, Purple Wax Shell Upgrade"
	prod6t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-W-P.png", X: 500, Y: 500}
	prod6t.Description = "Includes: Three (3) LuDela Smart Candle, Purple Color, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod6t.Currency = currency.USD
	prod6t.ListPrice = currency.Cents(65900)
	prod6t.Price = currency.Cents(32900)
	prod6t.Preorder = true
	prod6t.Hidden = false
	prod6t.EstimatedDelivery = "Early 2017"
	prod6t.Update()

	prod7t := product.New(nsdb)
	prod7t.Slug = "Trio-G-B"
	prod7t.GetOrCreate("Slug=", prod7t.Slug)
	prod7t.SetKey("ogc8Jy5miGG4")
	prod7t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-G-B.png", X: 500, Y: 500}
	prod7t.Name = "Trio Starter Kit, Black Glass Shell Upgrade"
	prod7t.Description = "Includes: Three (3) LuDela Smart Candle, Black Color, Glass Shell, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod7t.Currency = currency.USD
	prod7t.ListPrice = currency.Cents(68900)
	prod7t.Price = currency.Cents(34900)
	prod7t.Preorder = true
	prod7t.Hidden = false
	prod7t.EstimatedDelivery = "Early 2017"
	prod7t.Update()

	prod8t := product.New(nsdb)
	prod8t.Slug = "Trio-G-W"
	prod8t.GetOrCreate("Slug=", prod8t.Slug)
	prod8t.SetKey("Knc9xAXPcOOG")
	prod8t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-G-W.png", X: 500, Y: 500}
	prod8t.Name = "Trio Starter Kit, White Glass Shell Upgrade"
	prod8t.Description = "Includes: Three (3) LuDela Smart Candle, White Color, Glass Shell, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod8t.Currency = currency.USD
	prod8t.ListPrice = currency.Cents(68900)
	prod8t.Price = currency.Cents(34900)
	prod8t.Preorder = true
	prod8t.Hidden = false
	prod8t.EstimatedDelivery = "Early 2017"
	prod8t.Update()

	prod9t := product.New(nsdb)
	prod9t.Slug = "Trio-G-R"
	prod9t.GetOrCreate("Slug=", prod9t.Slug)
	prod9t.SetKey("g2cwgGXurrl")
	prod9t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-G-R.png", X: 500, Y: 500}
	prod9t.Name = "Trio Starter Kit, Red Glass Shell Upgrade"
	prod9t.Description = "Includes: Three (3) LuDela Smart Candle, Red Color, Glass Shell, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod9t.Currency = currency.USD
	prod9t.ListPrice = currency.Cents(68900)
	prod9t.Price = currency.Cents(34900)
	prod9t.Preorder = true
	prod9t.Hidden = false
	prod9t.EstimatedDelivery = "Early 2017"
	prod9t.Update()

	prod10t := product.New(nsdb)
	prod10t.Slug = "Trio-G-SM"
	prod10t.GetOrCreate("Slug=", prod10t.Slug)
	prod10t.SetKey("j8cYgrkvSppW")
	prod10t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-G-SM.png", X: 500, Y: 500}
	prod10t.Name = "Trio Starter Kit, Silver Mercury Shell Upgrade"
	prod10t.Description = "Includes: Three (3) LuDela Smart Candle, Silver Mercury Shell, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod10t.Currency = currency.USD
	prod10t.ListPrice = currency.Cents(68900)
	prod10t.Price = currency.Cents(34900)
	prod10t.Preorder = true
	prod10t.Hidden = false
	prod10t.EstimatedDelivery = "Early 2017"
	prod10t.Update()

	prod11t := product.New(nsdb)
	prod11t.Slug = "Trio-G-BM"
	prod11t.GetOrCreate("Slug=", prod11t.Slug)
	prod11t.SetKey("4pcPPQB7Snn9")
	prod11t.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/kits/Trio-G-BM.png", X: 500, Y: 500}
	prod11t.Name = "Trio Starter Kit, Bronze Mercury Shell Upgrade"
	prod11t.Description = "Includes: Three (3) LuDela Smart Candle, Bronze Mercury Shell, Six (6) 100% Natural Soy-Beeswax Refill (30-hour burn time)."
	prod11t.Currency = currency.USD
	prod11t.ListPrice = currency.Cents(68900)
	prod11t.Price = currency.Cents(34900)
	prod11t.Preorder = true
	prod11t.Hidden = false
	prod11t.EstimatedDelivery = "Early 2017"
	prod11t.Update()

	prod13 := product.New(nsdb)
	prod13.Slug = "SC-1"
	prod13.GetOrCreate("Slug=", prod13.Slug)
	prod13.SetKey("84cyj9AU99w")
	prod13.Name = "Scent: Vanilla Bliss"
	prod13.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-1.png", X: 500, Y: 500}
	prod13.Description = "A relaxing blend of sweet, buttery vanilla with hints of coconut and tonka bean"
	prod13.Currency = currency.USD
	prod13.ListPrice = currency.Cents(800)
	prod13.Price = currency.Cents(800)
	prod13.Preorder = true
	prod13.Hidden = false
	prod13.EstimatedDelivery = "Early 2017"
	prod13.Update()

	prod14 := product.New(nsdb)
	prod14.Slug = "SC-1-S"
	prod14.GetOrCreate("Slug=", prod14.Slug)
	prod14.SetKey("lqc2g4ARuNN5")
	prod14.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-1.png", X: 500, Y: 500}
	prod14.Name = "Scent: Vanilla Bliss (Subscription)"
	prod14.Description = "A relaxing blend of sweet, buttery vanilla with hints of coconut and tonka bean"
	prod14.Currency = currency.USD
	prod14.ListPrice = currency.Cents(800)
	prod14.Price = currency.Cents(700)
	prod14.Preorder = true
	prod14.Hidden = false
	prod14.EstimatedDelivery = "Early 2017"
	prod14.Update()

	prod15 := product.New(nsdb)
	prod15.Slug = "SC-2"
	prod15.GetOrCreate("Slug=", prod15.Slug)
	prod15.SetKey("RjcQxB0soox")
	prod15.Name = "Scent: Dew Kissed Petal"
	prod15.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-2.png", X: 500, Y: 500}
	prod15.Description = "A delightful blend of fruity pears and peaches with floral tons of jasmine and waterlily"
	prod15.Currency = currency.USD
	prod15.ListPrice = currency.Cents(800)
	prod15.Price = currency.Cents(800)
	prod15.Preorder = true
	prod15.Hidden = false
	prod15.EstimatedDelivery = "Early 2017"
	prod15.Update()

	prod16 := product.New(nsdb)
	prod16.Slug = "SC-2-S"
	prod16.GetOrCreate("Slug=", prod16.Slug)
	prod16.SetKey("vycReEPf11k")
	prod16.Name = "Scent: Dew Kissed Petal (Subscription)"
	prod16.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-2.png", X: 500, Y: 500}
	prod16.Description = "A delightful blend of fruity pears and peaches with floral tons of jasmine and waterlily"
	prod16.Currency = currency.USD
	prod16.ListPrice = currency.Cents(800)
	prod16.Price = currency.Cents(700)
	prod16.Preorder = true
	prod16.Hidden = false
	prod16.EstimatedDelivery = "Early 2017"
	prod16.Update()

	prod17 := product.New(nsdb)
	prod17.Slug = "SC-3"
	prod17.GetOrCreate("Slug=", prod17.Slug)
	prod17.SetKey("g2cw8dYurrl")
	prod17.Name = "Scent: Lavender Escape"
	prod17.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-3.png", X: 500, Y: 500}
	prod17.Description = "A calming blend of lavender with vanilla makes this an excellent choice to reduce stress and help you sleep better"
	prod17.Currency = currency.USD
	prod17.ListPrice = currency.Cents(800)
	prod17.Price = currency.Cents(800)
	prod17.Preorder = true
	prod17.Hidden = false
	prod17.EstimatedDelivery = "Early 2017"
	prod17.Update()

	prod18 := product.New(nsdb)
	prod18.Slug = "SC-3-S"
	prod18.GetOrCreate("Slug=", prod18.Slug)
	prod18.SetKey("69cDlkbWF88g")
	prod18.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-3.png", X: 500, Y: 500}
	prod18.Name = "Scent: Lavender Escape (Subscription)"
	prod18.Description = "A calming blend of lavender with vanilla makes this an excellent choice to reduce stress and help you sleep better"
	prod18.Currency = currency.USD
	prod18.ListPrice = currency.Cents(800)
	prod18.Price = currency.Cents(700)
	prod18.Preorder = true
	prod18.Hidden = false
	prod18.EstimatedDelivery = "Early 2017"
	prod18.Update()

	prod19 := product.New(nsdb)
	prod19.Slug = "SC-4"
	prod19.GetOrCreate("Slug=", prod19.Slug)
	prod19.SetKey("3YcDD0BwtAAx")
	prod19.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-4.png", X: 500, Y: 500}
	prod19.Name = "Scent: Pomegranate Delight"
	prod19.Description = "Fresh red currants and pomegranate touched with a splash of orange and finished with a twist of lemon."
	prod19.Currency = currency.USD
	prod19.ListPrice = currency.Cents(800)
	prod19.Price = currency.Cents(800)
	prod19.Preorder = true
	prod19.Hidden = false
	prod19.EstimatedDelivery = "Early 2017"
	prod19.Update()

	prod20 := product.New(nsdb)
	prod20.Slug = "SC-4-S"
	prod20.GetOrCreate("Slug=", prod20.Slug)
	prod20.SetKey("mOckJ5EDfJJp")
	prod20.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-4.png", X: 500, Y: 500}
	prod20.Name = "Scent: Pomegranate Delight (Subscription)"
	prod20.Description = "Fresh red currants and pomegranate touched with a splash of orange and finished with a twist of lemon."
	prod20.Currency = currency.USD
	prod20.ListPrice = currency.Cents(800)
	prod20.Price = currency.Cents(700)
	prod20.Preorder = true
	prod20.Hidden = false
	prod20.EstimatedDelivery = "Early 2017"
	prod20.Update()

	prod21 := product.New(nsdb)
	prod21.Slug = "SC-5"
	prod21.GetOrCreate("Slug=", prod21.Slug)
	prod21.SetKey("dZcv4rA5tAAd")
	prod21.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-5.png", X: 500, Y: 500}
	prod21.Name = "Scent: Mango Driftwood"
	prod21.Description = "A perfect blend of freshly-sliced mango and oranges combined with woody basenotes of cedarwood and amber"
	prod21.Currency = currency.USD
	prod21.ListPrice = currency.Cents(800)
	prod21.Price = currency.Cents(800)
	prod21.Preorder = true
	prod21.Hidden = false
	prod21.EstimatedDelivery = "Early 2017"
	prod21.Update()

	prod22 := product.New(nsdb)
	prod22.Slug = "SC-5-S"
	prod22.GetOrCreate("Slug=", prod22.Slug)
	prod22.SetKey("3YcDDrZ2sAAx")
	prod22.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-5.png", X: 500, Y: 500}
	prod22.Name = "Scent: Mango Driftwood (Subscription)"
	prod22.Description = "A perfect blend of freshly-sliced mango and oranges combined with woody basenotes of cedarwood and amber"
	prod22.Currency = currency.USD
	prod22.ListPrice = currency.Cents(800)
	prod22.Price = currency.Cents(700)
	prod22.Preorder = true
	prod22.Hidden = false
	prod22.EstimatedDelivery = "Early 2017"
	prod22.Update()

	prod23 := product.New(nsdb)
	prod23.Slug = "SC-6"
	prod23.GetOrCreate("Slug=", prod23.Slug)
	prod23.SetKey("dZcvm6GpCAAd")
	prod23.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-6.png", X: 500, Y: 500}
	prod23.Name = "Scent: Turquoise Bay"
	prod23.Description = "Enjoy a tropical cocktail of island pineapple and coconut combined with blissful basenotes of cedarwood and vanilla"
	prod23.Currency = currency.USD
	prod23.ListPrice = currency.Cents(800)
	prod23.Price = currency.Cents(800)
	prod23.Preorder = true
	prod23.Hidden = false
	prod23.EstimatedDelivery = "Early 2017"
	prod23.Update()

	prod24 := product.New(nsdb)
	prod24.Slug = "SC-6-S"
	prod24.GetOrCreate("Slug=", prod24.Slug)
	prod24.SetKey("eAc2qGJ6Tmmd")
	prod24.Name = "Scent: Turquoise Bay (Subscription)"
	prod24.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-6.png", X: 500, Y: 500}
	prod24.Description = "Enjoy a tropical cocktail of island pineapple and coconut combined with blissful basenotes of cedarwood and vanilla"
	prod24.Currency = currency.USD
	prod24.ListPrice = currency.Cents(800)
	prod24.Price = currency.Cents(700)
	prod24.Preorder = true
	prod24.Hidden = false
	prod24.EstimatedDelivery = "Early 2017"
	prod24.Update()

	prod25 := product.New(nsdb)
	prod25.Slug = "SC-7"
	prod25.GetOrCreate("Slug=", prod25.Slug)
	prod25.SetKey("Nwc7YxQtggK")
	prod25.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-7.png", X: 500, Y: 500}
	prod25.Name = "Scent: White Tea and Ginger"
	prod25.Description = "An intoxicating mixture of white tea notes and pungent, spicy ginger. This exotic mixture is great for every room in the house."
	prod25.Currency = currency.USD
	prod25.ListPrice = currency.Cents(800)
	prod25.Price = currency.Cents(800)
	prod25.Preorder = true
	prod25.Hidden = false
	prod25.EstimatedDelivery = "Early 2017"
	prod25.Update()

	prod26 := product.New(nsdb)
	prod26.Slug = "SC-7-S"
	prod26.GetOrCreate("Slug=", prod26.Slug)
	prod26.SetKey("ogcQnWztGG4")
	prod26.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-7.png", X: 500, Y: 500}
	prod26.Name = "Scent: White Tea and Ginger (Subscription)"
	prod26.Description = "An intoxicating mixture of white tea notes and pungent, spicy ginger. This exotic mixture is great for every room in the house."
	prod26.Currency = currency.USD
	prod26.ListPrice = currency.Cents(800)
	prod26.Price = currency.Cents(700)
	prod26.Preorder = true
	prod26.Hidden = false
	prod26.EstimatedDelivery = "Early 2017"
	prod26.Update()

	prod27 := product.New(nsdb)
	prod27.Slug = "SC-8"
	prod27.GetOrCreate("Slug=", prod27.Slug)
	prod27.SetKey("B4cex1m8hddO")
	prod27.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-8.png", X: 500, Y: 500}
	prod27.Name = "Scent: Sheer Linen and Orchid"
	prod27.Description = "A light, refreshing combination lily and orange flowers with lavender and sheer musks."
	prod27.Currency = currency.USD
	prod27.ListPrice = currency.Cents(800)
	prod27.Price = currency.Cents(800)
	prod27.Preorder = true
	prod27.Hidden = false
	prod27.EstimatedDelivery = "Early 2017"
	prod27.Update()

	prod28 := product.New(nsdb)
	prod28.Slug = "SC-8-S"
	prod28.GetOrCreate("Slug=", prod28.Slug)
	prod28.SetKey("YNcRde5cbb5")
	prod28.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-8.png", X: 500, Y: 500}
	prod28.Name = "Scent: Sheer Linen and Orchid (Subscription)"
	prod28.Description = "A light, refreshing combination lily and orange flowers with lavender and sheer musks."
	prod28.Currency = currency.USD
	prod28.ListPrice = currency.Cents(800)
	prod28.Price = currency.Cents(700)
	prod28.Preorder = true
	prod28.Hidden = false
	prod28.EstimatedDelivery = "Early 2017"
	prod28.Update()

	prod29 := product.New(nsdb)
	prod29.Slug = "SC-9"
	prod29.GetOrCreate("Slug=", prod29.Slug)
	prod29.SetKey("84cy7rgh99w")
	prod29.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-9.png", X: 500, Y: 500}
	prod29.Name = "Scent: Coastal Waters"
	prod29.Description = "Fresh ocean breezes gently blowing over a calm beach. Soft white floral background on a mossy musk base. A fresh coastal fragrance."
	prod29.Currency = currency.USD
	prod29.ListPrice = currency.Cents(800)
	prod29.Price = currency.Cents(800)
	prod29.Preorder = true
	prod29.Hidden = false
	prod29.EstimatedDelivery = "Early 2017"
	prod29.Update()

	prod30 := product.New(nsdb)
	prod30.Slug = "SC-9-S"
	prod30.GetOrCreate("Slug=", prod30.Slug)
	prod30.SetKey("mOcdE4euJJp")
	prod30.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-9.png", X: 500, Y: 500}
	prod30.Name = "Scent: Coastal Waters (Subscription)"
	prod30.Description = "Fresh ocean breezes gently blowing over a calm beach. Soft white floral background on a mossy musk base. A fresh coastal fragrance."
	prod30.Currency = currency.USD
	prod30.ListPrice = currency.Cents(800)
	prod30.Price = currency.Cents(700)
	prod30.Preorder = true
	prod30.Hidden = false
	prod30.EstimatedDelivery = "Early 2017"
	prod30.Update()

	prod31 := product.New(nsdb)
	prod31.Slug = "SC-10"
	prod31.GetOrCreate("Slug=", prod31.Slug)
	prod31.SetKey("ogcQr1qTGG4")
	prod31.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-10.png", X: 500, Y: 500}
	prod31.Name = "Scent: Midnight Showers"
	prod31.Description = "A soothing, masculine of bergamot and citrus with delightful hints of sandlewood and oak moss"
	prod31.Currency = currency.USD
	prod31.ListPrice = currency.Cents(800)
	prod31.Price = currency.Cents(800)
	prod31.Preorder = true
	prod31.Hidden = false
	prod31.EstimatedDelivery = "Early 2017"
	prod31.Update()

	prod32 := product.New(nsdb)
	prod32.Slug = "SC-10-S"
	prod32.GetOrCreate("Slug=", prod32.Slug)
	prod32.SetKey("GAcvDrdIppX")
	prod32.Image = Media{Type: MediaImage, Alt: "", Url: "http://ludela.com/images/product/scent-candles/SC-10.png", X: 500, Y: 500}
	prod32.Name = "Scent: Midnight Showers (Subscription)"
	prod32.Description = "A soothing, masculine of bergamot and citrus with delightful hints of sandlewood and oak moss"
	prod32.Currency = currency.USD
	prod32.ListPrice = currency.Cents(800)
	prod32.Price = currency.Cents(700)
	prod32.Preorder = true
	prod32.Hidden = false
	prod32.EstimatedDelivery = "Early 2017"
	prod32.Update()

	mc := integration.Mailchimp{APIKey: ""}
	client := mailchimp.New(db.Context, mc)
	//developmentStoreId := "ODtkkYuooO"
	defaultStoreId := "ldt6eeKINN5"
	client.CreateProduct(defaultStoreId, prod1)
	client.CreateProduct(defaultStoreId, prod2)
	client.CreateProduct(defaultStoreId, prod3)
	client.CreateProduct(defaultStoreId, prod4)
	client.CreateProduct(defaultStoreId, prod5)
	client.CreateProduct(defaultStoreId, prod6)
	client.CreateProduct(defaultStoreId, prod7)
	client.CreateProduct(defaultStoreId, prod8)
	client.CreateProduct(defaultStoreId, prod9)
	client.CreateProduct(defaultStoreId, prod10)
	client.CreateProduct(defaultStoreId, prod11)

	client.CreateProduct(defaultStoreId, prod1d)
	client.CreateProduct(defaultStoreId, prod2d)
	client.CreateProduct(defaultStoreId, prod3d)
	client.CreateProduct(defaultStoreId, prod4d)
	client.CreateProduct(defaultStoreId, prod5d)
	client.CreateProduct(defaultStoreId, prod6d)
	client.CreateProduct(defaultStoreId, prod7d)
	client.CreateProduct(defaultStoreId, prod8d)
	client.CreateProduct(defaultStoreId, prod9d)
	client.CreateProduct(defaultStoreId, prod10d)
	client.CreateProduct(defaultStoreId, prod11d)

	client.CreateProduct(defaultStoreId, prod1t)
	client.CreateProduct(defaultStoreId, prod2t)
	client.CreateProduct(defaultStoreId, prod3t)
	client.CreateProduct(defaultStoreId, prod4t)
	client.CreateProduct(defaultStoreId, prod5t)
	client.CreateProduct(defaultStoreId, prod6t)
	client.CreateProduct(defaultStoreId, prod7t)
	client.CreateProduct(defaultStoreId, prod8t)
	client.CreateProduct(defaultStoreId, prod9t)
	client.CreateProduct(defaultStoreId, prod10t)
	client.CreateProduct(defaultStoreId, prod11t)

	client.CreateProduct(defaultStoreId, prod13)
	client.CreateProduct(defaultStoreId, prod14)
	client.CreateProduct(defaultStoreId, prod15)
	client.CreateProduct(defaultStoreId, prod16)
	client.CreateProduct(defaultStoreId, prod17)
	client.CreateProduct(defaultStoreId, prod18)
	client.CreateProduct(defaultStoreId, prod19)

	client.CreateProduct(defaultStoreId, prod20)
	client.CreateProduct(defaultStoreId, prod21)
	client.CreateProduct(defaultStoreId, prod22)
	client.CreateProduct(defaultStoreId, prod23)
	client.CreateProduct(defaultStoreId, prod24)
	client.CreateProduct(defaultStoreId, prod25)
	client.CreateProduct(defaultStoreId, prod26)
	client.CreateProduct(defaultStoreId, prod27)
	client.CreateProduct(defaultStoreId, prod28)
	client.CreateProduct(defaultStoreId, prod29)

	client.CreateProduct(defaultStoreId, prod30)
	client.CreateProduct(defaultStoreId, prod31)
	client.CreateProduct(defaultStoreId, prod32)

	return []*product.Product{prod1}
})
