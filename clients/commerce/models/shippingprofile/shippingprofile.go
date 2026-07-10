package shippingprofile

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ShippingProfile]("shippingprofile") }

type ShippingProfile struct {
	mixin.Model[ShippingProfile]

	Name string `json:"name"`
	Type string `json:"type"` // "default", "gift_card", "custom"

	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s *ShippingProfile) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}

	return err
}

func (s *ShippingProfile) Save() ([]datastore.Property, error) {
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))

	return datastore.SaveStruct(s)
}

func New(db *datastore.Datastore) *ShippingProfile {
	s := new(ShippingProfile)
	s.Init(db)
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("shippingprofile")
}
