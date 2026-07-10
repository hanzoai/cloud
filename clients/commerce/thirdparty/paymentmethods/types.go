package paymentmethods

import "encoding/json"

type Type string

const (
	PlaidType Type = "plaid"
)

// Union object for all payment method parameters
type PaymentMethodParams struct {
	// Verifier refers to the entity which is verifying the user
	// Short lived public token reference
	VerifierToken string `json:"-"`

	// Reference to the verifier id
	VerifierId string `json:"-"`

	// Reference to external user (if any)
	ExternalUserId string `json:"-"`

	// Metadata to save with payment method
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Returned Pay Token
type PaymentMethodOutput struct {
	Inputs PaymentMethodParams

	// Long lived payment token
	PayToken string `json:"-"`

	// Reference to external token
	PayTokenId string `json:"-"`

	// Reference to external user (if any)
	ExternalUserId string `json:"-"`

	// Type of payment method
	Type Type `json:"type"`
}
