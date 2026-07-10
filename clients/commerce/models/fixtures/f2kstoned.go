package fixtures

// import (
// 	"github.com/gin-gonic/gin"

// 	"github.com/hanzoai/cloud/clients/commerce/datastore"
// 	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
// 	"github.com/hanzoai/cloud/clients/commerce/models/order"
// 	"github.com/hanzoai/cloud/clients/commerce/models/organization"
// 	"github.com/hanzoai/cloud/clients/commerce/models/payment"
// 	"github.com/hanzoai/cloud/clients/commerce/models/product"
// 	"github.com/hanzoai/cloud/clients/commerce/models/user"

// 	. "github.com/hanzoai/cloud/clients/commerce/types"
// )

// var _ = New("sa-f2k", func(c *gin.Context) *organization.Organization {
// 	db := datastore.New(c)

// 	org := organization.New(db)
// 	org.Query().Filter("Name=", "stoned").Get()

// 	nsdb := datastore.New(org.Namespaced(db.Context))

// 	var u *user.User
// 	var o *order.Order

// 	p := product.New(nsdb)
// 	p.MustGetById("earphone")

// 	// Cedric
// 	u = user.New(nsdb)
// 	u.FirstName = "Cedric Robin Stefan"
// 	u.LastName = "Sander"
// 	u.Email = "senfglas@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "Hartmannstraße 4",
// 		City:       "Ulm",
// 		PostalCode: "89073",
// 		Country:    "DE",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Tim
// 	u = user.New(nsdb)
// 	u.FirstName = "Tim"
// 	u.LastName = "Bergmann"
// 	u.Email = "theude@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "Moerser Str. 167",
// 		City:       "Moers",
// 		PostalCode: "47447",
// 		Country:    "DE",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Denada
// 	u = user.New(nsdb)
// 	u.FirstName = "Denada"
// 	u.LastName = "Nuzi"
// 	u.Email = "deniasaur@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "3 Sandal Street",
// 		Line2:      "Stratford",
// 		City:       "London",
// 		PostalCode: "E15 3NP",
// 		Country:    "GB",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Jack Hutton
// 	u = user.New(nsdb)
// 	u.FirstName = "Jack"
// 	u.LastName = "Hutton"
// 	u.Email = "j4ckiechan@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "17 Crossman Street",
// 		City:       "Nottingham",
// 		PostalCode: "NG5 2HR",
// 		Country:    "GB",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Allie Grace Macpherson
// 	u = user.New(nsdb)
// 	u.FirstName = "Allie Grace"
// 	u.LastName = "Macpherson"
// 	u.Email = "alliestrasza@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "13347 Orange Blossom Way",
// 		City:       "San Diego",
// 		PostalCode: "92130",
// 		State:      "CA",
// 		Country:    "US",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Wesley Metten
// 	u = user.New(nsdb)
// 	u.FirstName = "Wesley"
// 	u.LastName = "Metten"
// 	u.Email = "shadybunny@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "Vordensteinstraat 133",
// 		City:       "Schoten",
// 		PostalCode: "2900",
// 		Country:    "BE",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Elizabeth Carolyn Sanz
// 	u = user.New(nsdb)
// 	u.FirstName = "Elizabeth Carolyn"
// 	u.LastName = "Sanz"
// 	u.Email = "okayitsrosh@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "9830 Dale Avenue",
// 		Line2:      "Apt #128",
// 		City:       "Spring Valley",
// 		PostalCode: "91977",
// 		State:      "CA",
// 		Country:    "US",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Jesse Chrysler
// 	u = user.New(nsdb)
// 	u.FirstName = "Jesse"
// 	u.LastName = "Chrysler"
// 	u.Email = "control@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "9455 151st St",
// 		City:       "Surrey",
// 		PostalCode: "V3R8K8",
// 		State:      "BC",
// 		Country:    "CA",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Robert L Rusch
// 	u = user.New(nsdb)
// 	u.FirstName = "Robert L"
// 	u.LastName = "Rusch"
// 	u.Email = "varranis@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "515 W 7th St",
// 		Line2:      "Apt 2331",
// 		City:       "Charlotte",
// 		PostalCode: "28202",
// 		State:      "NC",
// 		Country:    "US",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Joshua Lee Marchant & Jesse
// 	u = user.New(nsdb)
// 	u.FirstName = "Joshua Lee"
// 	u.LastName = "Marchant"
// 	u.Email = "joshua@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName + " & Jesse",
// 		Line1:      "8 The Hermitage",
// 		Line2:      "Les Croutes",
// 		City:       "St. Peter Port Guernsey",
// 		PostalCode: "GY1 1QH",
// 		Country:    "GB",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    2,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Jesper Eriksson
// 	u = user.New(nsdb)
// 	u.FirstName = "Jesper"
// 	u.LastName = "Eriksson"
// 	u.Email = "freakeh@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "Krusbärsvägen 6",
// 		City:       "Ljung",
// 		State:      "Västra Götaland",
// 		PostalCode: "52442",
// 		Country:    "SE",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	// Celia Chen
// 	u = user.New(nsdb)
// 	u.FirstName = "Celia"
// 	u.LastName = "Chen"
// 	u.Email = "ceecee@fade2karma.com"
// 	u.GetOrCreate("Email=", u.Email)

// 	o = order.New(nsdb)
// 	o.Parent = u.Key()
// 	o.UserId = u.Id()
// 	o.PaymentStatus = payment.Paid
// 	o.ShippingAddress = Address{
// 		Name:       u.FirstName + " " + u.LastName,
// 		Line1:      "2709W 37th AVE",
// 		City:       "Vancouver",
// 		State:      "BC",
// 		PostalCode: "V6N 2T5",
// 		Country:    "CA",
// 	}

// 	o.Items = []lineitem.LineItem{
// 		lineitem.LineItem{
// 			ProductId:   p.Id(),
// 			ProductName: p.Name,
// 			ProductSlug: p.Slug,
// 			Quantity:    1,
// 		},
// 	}
// 	o.Metadata = Map{
// 		"batch": "f2k",
// 	}
// 	o.Tally()
// 	o.MustPut()

// 	return org
// })
