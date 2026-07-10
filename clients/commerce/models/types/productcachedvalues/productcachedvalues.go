package productcachedvalues

import (
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/dimensions"
	"github.com/hanzoai/cloud/clients/commerce/models/types/weight"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Values on product that need to be cached on the lineitem
type ProductCachedValues struct {
	// 3-letter ISO currency code (lowercase).
	Currency      currency.Type  `json:"currency"`
	Price         currency.Cents `json:"price"`
	MSRP          currency.Cents `json:"msrp,omitempty"`
	InventoryCost currency.Cents `json:"-"`

	// Subscription
	IsSubscribeable bool     `json:"isSubscribeable"`
	Interval        Interval `json:"interval"`
	IntervalCount   int      `json:"intervalCount"`
	// Kinda stripe specific, refactor later
	TrialPeriodDays int `json:"trialPeriodDays"`

	Inventory int `json:"inventory"`

	Weight         weight.Mass     `json:"weight"`
	WeightUnit     weight.Unit     `json:"weightUnit"`
	Dimensions     dimensions.Size `json:"dimensions"`
	DimensionsUnit dimensions.Unit `json:"dimensionsUnit"`

	Taxable bool `json:"taxable"`

	// Optional Estimated Delivery line
	EstimatedDelivery string `json:"estimatedDelivery"`

	// DEPRECATED

	ListPrice      currency.Cents `json:"listPrice,omitempty"`
	ProjectedPrice currency.Cents `json:"projectedPrice,omitempty"`
}
