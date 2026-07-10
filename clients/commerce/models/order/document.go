package order

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/searchpartial"
)

type Document struct {
	mixin.DocumentSaveLoad `datastore:"-" json:"-"`

	// Special Kind Option
	Kind string `search:",facet"`

	Id_           string
	Number        float64
	Email         string
	EmailPartials string
	UserId        string
	StoreId       string
	CampaignId    string
	CartId        string
	ReferrerId    string

	ProductNames string
	ProductIds   string
	ProductSlugs string

	BillingAddressName        string
	BillingAddressLine1       string
	BillingAddressLine2       string
	BillingAddressCity        string
	BillingAddressStateCode   string
	BillingAddressState       string
	BillingAddressCountryCode string
	BillingAddressCountry     string
	BillingAddressPostalCode  string

	ShippingAddressName        string
	ShippingAddressLine1       string
	ShippingAddressLine2       string
	ShippingAddressCity        string
	ShippingAddressStateCode   string
	ShippingAddressState       string
	ShippingAddressCountryCode string
	ShippingAddressCountry     string
	ShippingAddressPostalCode  string

	Discount   float64
	Subtotal   float64
	Shipping   float64
	Tax        float64
	Adjustment float64
	Total      float64
	Paid       float64
	Refunded   float64

	Type string

	CouponCodes string

	Status            string
	PaymentStatus     string
	FulfillmentStatus string

	FulfillmentTracking   string
	FulfillmentExternalId string
	FulfillmentService    string
	FulfillmentCarrier    string

	CreatedAt float64
	UpdatedAt float64

	Test string

	// Facets
	ProductNameOption0 string `search:"productName,facet"`
	ProductNameOption1 string `search:"productName,facet"`
	ProductNameOption2 string `search:"productName,facet"`
	ProductNameOption3 string `search:"productName,facet"`
	ProductNameOption4 string `search:"productName,facet"`
	ProductNameOption5 string `search:"productName,facet"`
	ProductNameOption6 string `search:"productName,facet"`
	ProductNameOption7 string `search:"productName,facet"`
	ProductNameOption8 string `search:"productName,facet"`
	ProductNameOption9 string `search:"productName,facet"`

	BillingAddressCityOption       string `search:"billingAddressCity,facet"`
	BillingAddressStateOption      string `search:"billingAddressState,facet"`
	BillingAddressPostalCodeOption string `search:"billingAddressPostalCode,facet"`
	BillingAddressCountryOption    string `search:"billingAddressCountry,facet"`

	ShippingAddressCityOption       string `search:"shippingAddressCity,facet"`
	ShippingAddressStateOption      string `search:"shippingAddressState,facet"`
	ShippingAddressPostalCodeOption string `search:"shippingAddressPostalCode,facet"`
	ShippingAddressCountryOption    string `search:"shippingAddressCountry,facet"`

	DiscountOption   float64 `search:"discount,facet"`
	SubtotalOption   float64 `search:"subtotal,facet"`
	ShippingOption   float64 `search:"shipping,facet"`
	TaxOption        float64 `search:"tax,facet"`
	AdjustmentOption float64 `search:"adjustment,facet"`
	TotalOption      float64 `search:"total,facet"`
	PaidOption       float64 `search:"paid,facet"`
	RefundedOption   float64 `search:"refunded,facet"`

	TypeOption        string `search:"type,facet"`
	CouponCodeOption0 string `search:"couponCode,facet"`

	StatusOption            string `search:"status,facet"`
	PaymentStatusOption     string `search:"paymentStatus,facet"`
	FulfillmentStatusOption string `search:"fulfillmentStatus,facet"`
	PreorderOption          string `search:"preorder,facet"`
	ConfirmedOption         string `search:"confirmed,facet"`

	FulfillmentTrackingOption0 string `search:"fulfillmentTracking,facet"`
	FulfillmentTrackingOption1 string `search:"fulfillmentTracking,facet"`
	FulfillmentTrackingOption2 string `search:"fulfillmentTracking,facet"`
}

func (d *Document) Id() string {
	return d.Id_
}

func (d *Document) Init() {
	d.SetDocument(d)
}

func (o Order) Document() mixin.Document {
	emailUser := strings.Split(o.Email, "@")[0]

	productIds := make([]string, 0)
	productSlugs := make([]string, 0)
	productNames := make([]string, 0)
	for _, item := range o.Items {
		productIds = append(productIds, item.ProductId)
		productSlugs = append(productIds, item.ProductSlug)
		productNames = append(productIds, item.ProductName)
	}
	trackings := make([]string, 0)
	for _, tracking := range o.Fulfillment.Trackings {
		trackings = append(trackings, tracking.Number)
	}

	doc := &Document{}
	doc.Init()
	doc.Kind = kind
	doc.Id_ = o.Id()
	doc.Number = float64(o.NumberFromId())
	doc.Email = o.Email
	doc.EmailPartials = searchpartial.Partials(emailUser) + " " + emailUser
	doc.UserId = o.UserId
	doc.StoreId = o.StoreId
	doc.CampaignId = o.CampaignId
	doc.CartId = o.CartId
	doc.ReferrerId = o.ReferrerId

	doc.ProductNames = strings.Join(productNames, " ")
	doc.ProductIds = strings.Join(productIds, " ")
	doc.ProductSlugs = strings.Join(productSlugs, " ")

	doc.BillingAddressName = o.BillingAddress.Name
	doc.BillingAddressLine1 = o.BillingAddress.Line1
	doc.BillingAddressLine2 = o.BillingAddress.Line2
	doc.BillingAddressCity = o.BillingAddress.City
	doc.BillingAddressStateCode = o.BillingAddress.State
	doc.BillingAddressCountryCode = o.BillingAddress.Country
	if o.BillingAddress.Country != "" {
		if c, err := country.FindByISO3166_2(o.BillingAddress.Country); err == nil {
			doc.BillingAddressCountry = c.Name.Common

			if o.BillingAddress.State != "" {
				if sd, err := c.FindSubDivision(o.BillingAddress.State); err == nil {
					doc.BillingAddressState = sd.Name
				} else {
					log.Error("BillingAddress State Code '%s' caused an error: %s ", o.BillingAddress.State, err, o.Context())
				}
			}
		} else {
			log.Error("BillingAddress Country Code '%s' caused an error: %s", o.BillingAddress.Country, err, o.Context())
		}
	}
	doc.BillingAddressPostalCode = o.BillingAddress.PostalCode

	doc.ShippingAddressName = o.ShippingAddress.Name
	doc.ShippingAddressLine1 = o.ShippingAddress.Line1
	doc.ShippingAddressLine2 = o.ShippingAddress.Line2
	doc.ShippingAddressCity = o.ShippingAddress.City
	doc.ShippingAddressStateCode = o.ShippingAddress.State
	doc.ShippingAddressCountryCode = o.ShippingAddress.Country
	if o.ShippingAddress.Country != "" {
		if c, err := country.FindByISO3166_2(o.ShippingAddress.Country); err == nil {
			doc.ShippingAddressCountry = c.Name.Common

			if o.ShippingAddress.State != "" {
				if sd, err := c.FindSubDivision(o.ShippingAddress.State); err == nil {
					doc.ShippingAddressState = sd.Name
				} else {
					log.Error("ShippingAddress State Code '%s' caused an error: %s ", o.ShippingAddress.State, err, o.Context())
				}
			}
		} else {
			log.Error("ShippingAddress Country Code '%s' caused an error: %s", o.ShippingAddress.Country, err, o.Context())
		}
	}
	doc.ShippingAddressPostalCode = o.ShippingAddress.PostalCode

	doc.Discount = o.Currency.ToFloat(o.Discount)
	doc.Subtotal = o.Currency.ToFloat(o.Subtotal)
	doc.Shipping = o.Currency.ToFloat(o.Shipping)
	doc.Tax = o.Currency.ToFloat(o.Tax)
	doc.Adjustment = o.Currency.ToFloat(o.Adjustment)
	doc.Total = o.Currency.ToFloat(o.Total)
	doc.Paid = o.Currency.ToFloat(o.Paid)
	doc.Refunded = o.Currency.ToFloat(o.Refunded)

	doc.Type = string(o.Type)

	doc.CreatedAt = float64(o.CreatedAt.Unix())
	doc.UpdatedAt = float64(o.UpdatedAt.Unix())

	doc.CouponCodes = strings.Join(o.CouponCodes, " ")
	doc.ReferrerId = o.ReferrerId

	doc.Status = string(o.Status)
	doc.PaymentStatus = string(o.PaymentStatus)
	doc.FulfillmentStatus = string(o.Fulfillment.Status)
	doc.FulfillmentTracking = strings.Join(trackings, " ")
	doc.FulfillmentExternalId = o.Fulfillment.ExternalId
	doc.FulfillmentService = o.Fulfillment.Service
	doc.FulfillmentCarrier = o.Fulfillment.Carrier

	nItems := len(o.Items)
	if nItems > 0 {
		doc.ProductNameOption0 = o.Items[0].ProductName
	}
	if nItems > 1 {
		doc.ProductNameOption1 = o.Items[1].ProductName
	}
	if nItems > 2 {
		doc.ProductNameOption2 = o.Items[2].ProductName
	}
	if nItems > 3 {
		doc.ProductNameOption3 = o.Items[3].ProductName
	}
	if nItems > 4 {
		doc.ProductNameOption4 = o.Items[4].ProductName
	}
	if nItems > 5 {
		doc.ProductNameOption5 = o.Items[5].ProductName
	}
	if nItems > 6 {
		doc.ProductNameOption6 = o.Items[6].ProductName
	}
	if nItems > 7 {
		doc.ProductNameOption7 = o.Items[7].ProductName
	}
	if nItems > 8 {
		doc.ProductNameOption8 = o.Items[8].ProductName
	}
	if nItems > 9 {
		doc.ProductNameOption9 = o.Items[9].ProductName
	}

	doc.BillingAddressCityOption = doc.BillingAddressCity
	doc.BillingAddressStateOption = doc.BillingAddressState
	doc.BillingAddressPostalCodeOption = doc.BillingAddressPostalCode
	doc.BillingAddressCountryOption = doc.BillingAddressCountry

	doc.ShippingAddressCityOption = doc.ShippingAddressCity
	doc.ShippingAddressStateOption = doc.ShippingAddressState
	doc.ShippingAddressPostalCodeOption = doc.ShippingAddressPostalCode
	doc.ShippingAddressCountryOption = doc.ShippingAddressCountry

	switch o.Currency {
	case currency.ETH, currency.BTC, currency.XBT:
		doc.DiscountOption = float64(o.Discount) / 1e9
		doc.SubtotalOption = float64(o.Subtotal) / 1e9
		doc.ShippingOption = float64(o.Shipping) / 1e9
		doc.TaxOption = float64(o.Tax) / 1e9
		doc.AdjustmentOption = float64(o.Adjustment) / 1e9
		doc.TotalOption = float64(o.Total) / 1e9
		doc.PaidOption = float64(o.Paid) / 1e9
		doc.RefundedOption = float64(o.Refunded) / 1e9
	default:
		doc.DiscountOption = float64(o.Discount)
		doc.SubtotalOption = float64(o.Subtotal)
		doc.ShippingOption = float64(o.Shipping)
		doc.TaxOption = float64(o.Tax)
		doc.AdjustmentOption = float64(o.Adjustment)
		doc.TotalOption = float64(o.Total)
		doc.PaidOption = float64(o.Paid)
		doc.RefundedOption = float64(o.Refunded)
	}

	doc.TypeOption = string(o.Type)

	nCoupons := len(o.CouponCodes)
	if nCoupons > 0 {
		doc.CouponCodeOption0 = o.CouponCodes[0]
	}

	doc.StatusOption = doc.Status
	doc.PaymentStatusOption = doc.PaymentStatus
	doc.FulfillmentStatusOption = doc.FulfillmentStatus
	if o.Preorder {
		doc.PreorderOption = "preorder"
	}
	if !o.Unconfirmed {
		doc.ConfirmedOption = "confirmed"
	}

	nTrackings := len(o.Fulfillment.Trackings)
	if nTrackings > 0 {
		doc.FulfillmentTrackingOption0 = o.Fulfillment.Trackings[0].Number
	}
	if nTrackings > 1 {
		doc.FulfillmentTrackingOption1 = o.Fulfillment.Trackings[1].Number
	}
	if nTrackings > 2 {
		doc.FulfillmentTrackingOption2 = o.Fulfillment.Trackings[2].Number
	}

	if o.Test {
		doc.Test = "true"
	} else {
		doc.Test = "false"
	}

	return doc
}
