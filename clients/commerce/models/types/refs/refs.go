package refs

// For your third party integration ref objects here

type EcommerceRefType string

const (
	AffirmEcommerceRefType EcommerceRefType = "affirm"
	AuthorizeNetRefType    EcommerceRefType = "authorize"
	// Balance  Type = "balance"
	// Ethereum Type = "ethereum"
	// Bitcoin  Type = "bitcoin"
	// Null     Type = "null"
	// PayPal   Type = "paypal"
	StripeEcommerceRefType EcommerceRefType = "stripe"
)

type StripeRef struct {
	Id string `json:"id,omitempty"`
}

type AffirmRef struct {
	Id string `json:"id,omitempty"`
}

type AuthorizeNetRef struct {
	SubscriptionId           string `json:"subscriptionId,omitempty"`
	CustomerProfileId        string `json:"customerProfileId,omitempty"`
	CustomerPaymentProfileId string `json:"customerPaymentProfileId,omitempty"`
}

type EcommerceRef struct {
	Type EcommerceRefType `json:"type,omitempty"`

	Stripe       StripeRef       `json:"stripe,omitempty"`
	Affirm       AffirmRef       `json:"affirm,omitempty"`
	AuthorizeNet AuthorizeNetRef `json:"authorizeNet,omitempty"`
}
