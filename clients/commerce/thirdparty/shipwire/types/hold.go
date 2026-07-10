package types

type Hold struct {
	// Hold ID
	ID int `json:"id"`

	// Shipwire Order ID
	OrderID int `json:"orderId"`

	// Hanzo Order ID
	ExternalOrderID string `json:"externalOrderId,omitempty"`

	Type    string `json:"type"`
	SubType string `json:"subType"`

	Description string `json:"description"`

	// Since when is this hold applied
	AppliedDate Date `json:"appliedDate"`

	// When was this order cleared, or null if it's still active.
	ClearedDate Date `json:"clearedDate"`
}
