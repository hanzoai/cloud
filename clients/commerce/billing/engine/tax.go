package engine

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrate"
	"github.com/hanzoai/cloud/clients/commerce/models/taxregion"
	"github.com/hanzoai/cloud/clients/commerce/types"
)

// TaxLine represents a single tax computation on an invoice.
type TaxLine struct {
	TaxRateId    string  `json:"taxRateId"`
	Description  string  `json:"description"`
	Amount       int64   `json:"amount"`       // tax amount in cents
	Rate         float64 `json:"rate"`          // e.g., 0.0875 for 8.75%
	Inclusive    bool    `json:"inclusive"`
	Jurisdiction string  `json:"jurisdiction"`
}

// CalculateInvoiceTax computes tax for an invoice based on the customer address
// using the existing TaxRate and TaxRegion models. Returns tax lines and total.
func CalculateInvoiceTax(db *datastore.Datastore, inv *billinginvoice.BillingInvoice, customerAddress *types.Address) ([]TaxLine, int64, error) {
	if customerAddress == nil {
		return nil, 0, nil
	}

	rootKey := db.NewKey("synckey", "", 1, nil)

	// Find matching tax regions by country and state/province
	regions := make([]*taxregion.TaxRegion, 0)
	rq := taxregion.Query(db).Ancestor(rootKey)
	if customerAddress.Country != "" {
		rq = rq.Filter("CountryCode=", customerAddress.Country)
	}
	if _, err := rq.GetAll(&regions); err != nil {
		return nil, 0, nil // no regions = no tax
	}

	// Filter by state/province if available
	matchedRegions := make([]*taxregion.TaxRegion, 0)
	for _, r := range regions {
		if r.ProvinceCode == "" || r.ProvinceCode == customerAddress.State {
			matchedRegions = append(matchedRegions, r)
		}
	}

	if len(matchedRegions) == 0 {
		return nil, 0, nil
	}

	// Get tax rates for matched regions
	var taxLines []TaxLine
	var totalTax int64

	for _, region := range matchedRegions {
		rates := make([]*taxrate.TaxRate, 0)
		tq := taxrate.Query(db).Ancestor(rootKey).
			Filter("TaxRegionId=", region.Id())
		if _, err := tq.GetAll(&rates); err != nil {
			continue
		}

		for _, rate := range rates {
			taxAmount := int64(float64(inv.Subtotal) * rate.Rate)

			jurisdiction := region.CountryCode
			if region.ProvinceCode != "" {
				jurisdiction += "-" + region.ProvinceCode
			}

			taxLines = append(taxLines, TaxLine{
				TaxRateId:    rate.Id(),
				Description:  rate.Name,
				Amount:       taxAmount,
				Rate:         rate.Rate,
				Jurisdiction: jurisdiction,
			})

			totalTax += taxAmount
		}
	}

	return taxLines, totalTax, nil
}
