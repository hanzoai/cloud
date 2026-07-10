package fulfillmentset

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[FulfillmentSet]("fulfillmentset") }

type FulfillmentSet struct {
	mixin.Model[FulfillmentSet]

	Name string `json:"name"`
	Type string `json:"type"` // "shipping", "pickup", "digital"

	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (f *FulfillmentSet) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(f, ps); err != nil {
		return err
	}

	if len(f.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(f.Metadata_), &f.Metadata)
	}

	return err
}

func (f *FulfillmentSet) Save() ([]datastore.Property, error) {
	f.Metadata_ = string(json.EncodeBytes(&f.Metadata))

	return datastore.SaveStruct(f)
}

func New(db *datastore.Datastore) *FulfillmentSet {
	f := new(FulfillmentSet)
	f.Init(db)
	return f
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("fulfillmentset")
}
