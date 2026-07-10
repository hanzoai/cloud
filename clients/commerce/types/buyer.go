package types

import "errors"

var BuyerEmailOrPhoneRequired = errors.New("Buyer's Email or Phone is required.")

type Buyer struct {
	Email     string `json:"email,omitempty"`
	UserId    string `json:"userId,omitempty"`
	FirstName string `json:"firstName,omitempty"`
	LastName  string `json:"lastName,omitempty"`
	Company   string `json:"company,omitempty"`
	Phone     string `json:"phone,omitempty"`
	// Address   Address `json:"address,omitempty"`
	ShippingAddress Address `json:"shippingAddress,omitempty"`
	BillingAddress  Address `json:"billingAddress,omitempty"`
}

func (b Buyer) Name() string {
	return b.FirstName + " " + b.LastName
}

func (b Buyer) Validate() (bool, []error) {
	if b.Email != "" && b.Phone != "" {
		return false, []error{BuyerEmailOrPhoneRequired}
	}

	return true, nil
}
