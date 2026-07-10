package shipping

import (
	"math"
	"sort"

	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/weight"
)

type RateType string

const (
	Flat     RateType = "flat"
	Variable          = "variable"
)

// This represents the minimum value of a shipping formula
//
//	for example: Shipping = $10 Flat Rate if weight > 10 lbs
type Formula struct {
	MinWeight weight.Mass    `json:"minWeight"`
	RateType  RateType       `json:"type"`
	Price     currency.Cents `json:"price"`
}

// A collection of shipping rate formulas, all must have a common weight and currency unit
type Rates struct {
	Formulas   []Formula     `json:"formulas"`
	WeightUnit weight.Unit   `json:"weightUnit"`
	Currency   currency.Type `json:"currency"`

	// Rate used by default
	BaseRateType RateType       `json:"type"`
	BasePrice    currency.Cents `json:"price"`
}

func (r Rates) Len() int {
	return len(r.Formulas)
}

func (r *Rates) Swap(i, j int) {
	r.Formulas[i], r.Formulas[j] = r.Formulas[j], r.Formulas[i]
}

func (r Rates) Less(i, j int) bool {
	return r.Formulas[i].MinWeight < r.Formulas[j].MinWeight
}

// To calculate shipping rate, we sort an array of formulas by the MinWeight ascending
// When we find the first element with a weight greater than a min, we calculate using the previous one
func (r Rates) GetPrice(p *product.Product) (currency.Cents, currency.Type) {
	// Convert to f units
	w := weight.Convert(p.Weight, p.WeightUnit, r.WeightUnit)

	sort.Sort(&r)

	// i is index of last Formula compared
	i := -1
	for j, f := range r.Formulas {

		// Break if MinWeight is less than Product Weight
		if w < f.MinWeight {
			break
		}

		// Set index to current formula
		i = j
	}

	if i == -1 {
		// Use the base rate if weight is less than the first MinWeight
		return calculateShippingPrice(w, r.BaseRateType, r.BasePrice), r.Currency
	}
	// Otherwise look up the corresponding formula and use the rates on it
	f := r.Formulas[i]
	return calculateShippingPrice(w, f.RateType, f.Price), r.Currency
}

// helpers
func calculateShippingPrice(w weight.Mass, rateType RateType, price currency.Cents) currency.Cents {
	switch rateType {
	case Variable:
		// Do the math and round up for variable rates
		return currency.Cents(math.Ceil(float64(w) * float64(price)))
	default:
		// Flat/other cases
		return price
	}
}
