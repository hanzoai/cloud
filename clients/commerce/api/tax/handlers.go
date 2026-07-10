package tax

import (
	"context"
	"math"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/taxprovider"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrate"
	"github.com/hanzoai/cloud/clients/commerce/models/taxraterule"
	"github.com/hanzoai/cloud/clients/commerce/models/taxregion"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	jsonhttp "github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

// Ensure imports are used.
var _ = taxprovider.TaxProvider{}
var _ = taxraterule.TaxRateRule{}

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	// CRUD for tax models
	rest.New(taxregion.TaxRegion{}).Route(router, args...)
	rest.New(taxrate.TaxRate{}).Route(router, args...)
	rest.New(taxraterule.TaxRateRule{}).Route(router, args...)
	rest.New(taxprovider.TaxProvider{}).Route(router, args...)

	// Tax calculation endpoint
	calcApi := rest.New("/tax")
	calcApi.POST("/calculate", append(args, namespaced, Calculate)...)
	calcApi.Route(router, args...)
}

// Request/response types for tax calculation.

type calcItem struct {
	Amount   float64 `json:"amount"`
	Quantity int     `json:"quantity"`
}

type calcAddress struct {
	CountryCode  string `json:"countryCode"`
	ProvinceCode string `json:"provinceCode"`
}

type calcRequest struct {
	Items           []calcItem  `json:"items"`
	ShippingAddress calcAddress `json:"shippingAddress"`
}

type calcItemResult struct {
	Amount   float64 `json:"amount"`
	Quantity int     `json:"quantity"`
	TaxRate  float64 `json:"taxRate"`
	Tax      float64 `json:"tax"`
}

type calcResponse struct {
	Items    []calcItemResult `json:"items"`
	TotalTax float64          `json:"totalTax"`
}

// Calculate computes tax for a list of items given a shipping address.
//
// It looks up the TaxRegion matching the provided countryCode and
// provinceCode, queries all TaxRates for that region, sums combinable
// rates (or uses the default rate), then applies the effective rate to
// each item.
func Calculate(c *gin.Context) {
	var req calcRequest
	if err := json.Decode(c.Request.Body, &req); err != nil {
		jsonhttp.Fail(c, 400, "Invalid request body", err)
		return
	}

	if len(req.Items) == 0 {
		jsonhttp.Fail(c, 400, "No items provided", nil)
		return
	}

	ctx := middleware.GetContext(c)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	db := datastore.New(ctx)

	// Find matching tax region by country + province.
	region := taxregion.New(db)
	ok, err := region.Query().
		Filter("CountryCode=", req.ShippingAddress.CountryCode).
		Filter("ProvinceCode=", req.ShippingAddress.ProvinceCode).
		Get()
	if err != nil {
		jsonhttp.Fail(c, 500, "Failed to query tax region", err)
		return
	}

	// If no province-level region, try country-level (empty province).
	if !ok {
		region = taxregion.New(db)
		ok, err = region.Query().
			Filter("CountryCode=", req.ShippingAddress.CountryCode).
			Filter("ProvinceCode=", "").
			Get()
		if err != nil {
			jsonhttp.Fail(c, 500, "Failed to query tax region", err)
			return
		}
	}

	// No region found -- zero tax.
	if !ok {
		items := make([]calcItemResult, len(req.Items))
		for i, item := range req.Items {
			items[i] = calcItemResult{
				Amount:   item.Amount,
				Quantity: item.Quantity,
				TaxRate:  0,
				Tax:      0,
			}
		}
		jsonhttp.Render(c, 200, calcResponse{Items: items, TotalTax: 0})
		return
	}

	// Fetch tax rates for this region.
	regionId := region.Id()
	var rates []*taxrate.TaxRate
	q := taxrate.Query(db).Filter("TaxRegionId=", regionId)
	if _, err := q.GetAll(&rates); err != nil {
		jsonhttp.Fail(c, 500, "Failed to query tax rates", err)
		return
	}

	// Compute effective rate: sum all combinable rates, or fall back to
	// the first default rate.
	effectiveRate := 0.0
	hasCombined := false

	for _, r := range rates {
		if r.IsCombinable {
			effectiveRate += r.Rate
			hasCombined = true
		}
	}

	if !hasCombined {
		for _, r := range rates {
			if r.IsDefault {
				effectiveRate = r.Rate
				break
			}
		}
		// If still zero and rates exist, use the first one.
		if effectiveRate == 0 && len(rates) > 0 {
			effectiveRate = rates[0].Rate
		}
	}

	// Calculate per-item tax.
	totalTax := 0.0
	items := make([]calcItemResult, len(req.Items))

	for i, item := range req.Items {
		lineTotal := item.Amount * float64(item.Quantity)
		tax := roundCents(lineTotal * effectiveRate)
		totalTax += tax

		items[i] = calcItemResult{
			Amount:   item.Amount,
			Quantity: item.Quantity,
			TaxRate:  effectiveRate,
			Tax:      tax,
		}
	}

	totalTax = roundCents(totalTax)

	jsonhttp.Render(c, 200, calcResponse{
		Items:    items,
		TotalTax: totalTax,
	})
}

// roundCents rounds to 2 decimal places (cents).
func roundCents(v float64) float64 {
	return math.Round(v*100) / 100
}
