package cart

import "time"

type Document struct {
	Id_    string
	UserId string

	ProductIds string

	BillingAddressLine1       string
	BillingAddressLine2       string
	BillingAddressCity        string
	BillingAddressState       string
	BillingAddressCountryCode string
	BillingAddressCountry     string
	BillingAddressPostalCode  string

	ShippingAddressLine1       string
	ShippingAddressLine2       string
	ShippingAddressCity        string
	ShippingAddressState       string
	ShippingAddressCountryCode string
	ShippingAddressCountry     string
	ShippingAddressPostalCode  string

	Type string

	CreatedAt time.Time
	UpdatedAt time.Time

	Currency    string
	Total       float64
	CouponCodes string
	ReferrerId  string

	Status            string
	PaymentStatus     string
	FulfillmentStatus string
	Preorder          string
	Confirmed         string
}

func (d Document) Id() string {
	return d.Id_
}
