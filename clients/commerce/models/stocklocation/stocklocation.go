package stocklocation

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[StockLocation]("stocklocation") }

type StockLocation struct {
	mixin.Model[StockLocation]

	// Name of stock location
	Name string `json:"name"`

	// Address fields
	AddressLine1 string `json:"addressLine1"`
	AddressLine2 string `json:"addressLine2"`
	City         string `json:"city"`
	Country      string `json:"country"`
	Province     string `json:"province"`
	PostalCode   string `json:"postalCode"`
	Phone        string `json:"phone"`

	// Arbitrary metadata
	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s *StockLocation) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}

	return err
}

func (s *StockLocation) Save() ([]datastore.Property, error) {
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))

	return datastore.SaveStruct(s)
}

func New(db *datastore.Datastore) *StockLocation {
	s := new(StockLocation)
	s.Init(db)
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("stocklocation")
}
