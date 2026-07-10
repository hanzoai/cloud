package shippingrates

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/georate"
	"github.com/hanzoai/orm"
)

type GeoRate struct {
	georate.GeoRate

	// Shipping Name like 'Shipping'
	ShippingName string `json:"shippingName"`
}

func init() { orm.Register[ShippingRates]("shippingrates") }

type ShippingRates struct {
	mixin.Model[ShippingRates]

	StoreId string `json:"storeId"`

	GeoRates []GeoRate `json:"geoRates" orm:"default:[]"`
	// TODO: Support Mass / Dimension Based Rates
	// DimRates []DimRate `json:"dimRates"`
}

func (t ShippingRates) GetGeoRates() []georate.GeoRate {
	grs := make([]georate.GeoRate, 0)
	for i, _ := range t.GeoRates {
		grs = append(grs, t.GeoRates[i].GeoRate)
	}
	return grs
}

func (t ShippingRates) Match(ctr, st, ct, pc string, c currency.Cents) (*GeoRate, int, int) {
	gr, level, i := georate.Match(t.GetGeoRates(), ctr, st, ct, pc, c)
	if gr != nil {
		return &t.GeoRates[i], level, i
	}

	return nil, -1, i
}

// New creates a new ShippingRates wired to the given datastore.
func New(db *datastore.Datastore) *ShippingRates {
	t := new(ShippingRates)
	t.Init(db)
	return t
}

// Query returns a datastore query for shipping rates.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("shippingrates")
}
