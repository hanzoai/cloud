package types

import "github.com/hanzoai/cloud/clients/commerce/models/types/currency"

type Adjustment struct {
	// Possible values: flat.
	Type string `json:"type"`
	// Reasoning for price adjustment.
	Reason string `json:"reason"`
	// Authorizer of price adjustment.
	Issuer string `json:"issuer"`
	// Amount of price adjustment.
	Amount currency.Cents `json:"amount"`
}
