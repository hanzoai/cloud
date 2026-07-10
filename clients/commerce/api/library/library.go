package library

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingrates"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrates"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// Countries Loading
type SubDivision struct {
	Name    string `json:"name"`
	IsoCode string `json:"code"`
}

type Country struct {
	Name         string        `json:"name"`
	IsoCode      string        `json:"code"`
	SubDivisions []SubDivision `json:"subdivisions"`
}

var Countries []Country
var CountryLastUpdated time.Time

func init() {
	// Populate Countries list if it doesn't exist
	if CountryLastUpdated.IsZero() {
		CountryLastUpdated = time.Now()

		Countries = make([]Country, 0)

		for _, c := range country.Countries {
			sdvs := make([]SubDivision, 0)

			for _, sd := range c.SubDivisions() {
				sdvs = append(sdvs, SubDivision{
					sd.Name,
					sd.Code,
				})
			}

			co := Country{
				c.Name.Common,
				c.Codes.Alpha2,
				sdvs,
			}

			Countries = append(Countries, co)
		}
	}
}

// ShopJS request and response
type LoadShopJSReq struct {
	HasCountries     bool `json:"hasCountries"`
	HasTaxRates      bool `json:"hasTaxRates"`
	HasShippingRates bool `json:"hasShippingRates"`

	LastChecked time.Time `json:"lastChecked"`
	StoreId     string    `json:"storeId"`
}

type LoadShopJSRes struct {
	Countries     []Country                    `json:"countries,omitempty"`
	TaxRates      *taxrates.TaxRates           `json:"taxRates,omitempty"`
	ShippingRates *shippingrates.ShippingRates `json:"shippingRates,omitempty"`
	Currency      currency.Type                `json:"currency,omitempty"`
	StoreId       string                       `json:"storeId,omitempty"`

	Live bool `json:"live"`
}

func LoadShopJS(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	// Decode response body to get ShopJS Params
	req := &LoadShopJSReq{}

	if err := json.Decode(c.Request.Body, req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Default store if StoreId is left blank
	if req.StoreId == "" {
		req.StoreId = org.DefaultStore
	}

	stor := store.New(db)
	if err := stor.GetById(req.StoreId); err != nil {
		http.Fail(c, 404, "Store `"+req.StoreId+"` not found", err)
		return
	}

	// Build response
	res := LoadShopJSRes{StoreId: req.StoreId}

	// Determine Test Mode
	res.Live = org.Live

	if !req.HasCountries ||
		req.LastChecked.Before(CountryLastUpdated) {
		res.Countries = Countries
	}

	if res.Currency == "" {
		res.Currency = stor.Currency
	}

	if res.Currency == "" {
		res.Currency = org.Currency
	}

	if res.Currency == "" {
		res.Currency = currency.USD
	}

	if req.HasTaxRates {
		tr := taxrates.New(db)
		if ok, err := tr.Query().Filter("StoreId=", req.StoreId).Get(); ok {
			if req.LastChecked.Before(tr.UpdatedAt) {
				res.TaxRates = tr
			}
		} else if err != nil {
			http.Fail(c, 500, err.Error(), err)
			return
		}
	} else {
		tr := taxrates.New(db)
		if ok, err := tr.Query().Filter("StoreId=", req.StoreId).Get(); ok {
			res.TaxRates = tr
		} else if err != nil {
			http.Fail(c, 500, err.Error(), err)
			return
		}
	}

	if req.HasShippingRates {
		sr := shippingrates.New(db)
		if ok, err := sr.Query().Filter("StoreId=", req.StoreId).Get(); ok {
			if req.LastChecked.Before(sr.UpdatedAt) {
				res.ShippingRates = sr
			}
		} else if err != nil {
			http.Fail(c, 500, err.Error(), err)
			return
		}
	} else {
		sr := shippingrates.New(db)
		if ok, err := sr.Query().Filter("StoreId=", req.StoreId).Get(); ok {
			res.ShippingRates = sr
		} else if err != nil {
			http.Fail(c, 500, err.Error(), err)
			return
		}
	}

	http.Render(c, 200, res)
}

type LoadDaishoReq struct {
	HasCountries bool `json:"hasCountries"`

	LastChecked time.Time `json:"lastChecked"`
}

type LoadDaishoRes struct {
	Countries []Country `json:"countries,omitempty"`
}

func LoadDaisho(c *gin.Context) {
	// Decode response body to get ShopJS Params
	req := &LoadDaishoReq{}

	if err := json.Decode(c.Request.Body, req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Build response
	res := LoadDaishoRes{}

	if !req.HasCountries ||
		req.LastChecked.Before(CountryLastUpdated) {
		res.Countries = Countries
	}

	http.Render(c, 200, res)
}
