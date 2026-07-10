package priceset

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[PriceSet]("priceset") }

type PriceSet struct {
	mixin.Model[PriceSet]

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (p *PriceSet) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	if len(p.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(p.Metadata_), &p.Metadata)
	}

	return err
}

func (p *PriceSet) Save() ([]datastore.Property, error) {
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))

	return datastore.SaveStruct(p)
}

func New(db *datastore.Datastore) *PriceSet {
	t := new(PriceSet)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("priceset")
}
