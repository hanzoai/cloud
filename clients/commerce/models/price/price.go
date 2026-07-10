package price

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Price]("price") }

type Price struct {
	mixin.Model[Price]

	PriceSetId   string         `json:"priceSetId"`
	CurrencyCode string         `json:"currencyCode"`
	Amount       currency.Cents `json:"amount"`
	MinQuantity  int            `json:"minQuantity"`
	MaxQuantity  int            `json:"maxQuantity"`
	PriceListId  string         `json:"priceListId"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (p *Price) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	if len(p.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(p.Metadata_), &p.Metadata)
	}

	return err
}

func (p *Price) Save() ([]datastore.Property, error) {
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))

	return datastore.SaveStruct(p)
}

func New(db *datastore.Datastore) *Price {
	t := new(Price)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("price")
}
