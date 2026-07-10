package shippingoption

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type PriceType string

const (
	FlatRate   PriceType = "flat"
	Calculated PriceType = "calculated"
)

func init() { orm.Register[ShippingOption]("shippingoption") }

type ShippingOption struct {
	mixin.Model[ShippingOption]

	Name          string         `json:"name"`
	PriceType     PriceType      `json:"priceType"`
	Amount        currency.Cents `json:"amount"`
	ServiceZoneId string         `json:"serviceZoneId"`
	ProviderId    string         `json:"providerId"`
	ProfileId     string         `json:"profileId"`
	DataJSON      string         `json:"data" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s *ShippingOption) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}

	return err
}

func (s *ShippingOption) Save() ([]datastore.Property, error) {
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))

	return datastore.SaveStruct(s)
}

func New(db *datastore.Datastore) *ShippingOption {
	s := new(ShippingOption)
	s.Init(db)
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("shippingoption")
}
