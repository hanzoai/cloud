package region

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[Region]("region") }

type Country struct {
	ISO2        string `json:"iso2"`
	ISO3        string `json:"iso3"`
	NumCode     int    `json:"numCode"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	RegionId    string `json:"regionId"`
}

type Region struct {
	mixin.Model[Region]

	// Name of region
	Name string `json:"name"`

	// ISO 4217 currency code
	CurrencyCode string `json:"currencyCode"`

	// Whether automatic taxes are enabled
	AutomaticTaxes bool `json:"automaticTaxes"`

	// Whether tax-inclusive pricing is enabled
	TaxInclusiveEnabled bool `json:"taxInclusiveEnabled"`

	// Countries in this region (serialized to datastore)
	Countries  []Country `json:"countries" datastore:"-" orm:"default:[]"`
	Countries_ string    `json:"-" datastore:",noindex"`

	// Arbitrary metadata
	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (r *Region) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(r, ps); err != nil {
		return err
	}

	if len(r.Countries_) > 0 {
		if err = json.DecodeBytes([]byte(r.Countries_), &r.Countries); err != nil {
			return err
		}
	}

	if len(r.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(r.Metadata_), &r.Metadata)
	}

	return err
}

func (r *Region) Save() ([]datastore.Property, error) {
	r.Countries_ = string(json.EncodeBytes(&r.Countries))
	r.Metadata_ = string(json.EncodeBytes(&r.Metadata))

	return datastore.SaveStruct(r)
}

func New(db *datastore.Datastore) *Region {
	r := new(Region)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("region")
}
