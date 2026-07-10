package taxrate

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[TaxRate]("taxrate") }

type TaxRate struct {
	mixin.Model[TaxRate]

	Rate         float64 `json:"rate"`
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	IsDefault    bool    `json:"isDefault"`
	IsCombinable bool    `json:"isCombinable"`
	TaxRegionId  string  `json:"taxRegionId"`

	// Arbitrary key/value pairs associated with this tax rate
	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (t *TaxRate) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(t, ps); err != nil {
		return err
	}

	if len(t.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(t.Metadata_), &t.Metadata)
	}

	return err
}

func (t *TaxRate) Save() ([]datastore.Property, error) {
	t.Metadata_ = string(json.EncodeBytes(&t.Metadata))

	return datastore.SaveStruct(t)
}

func New(db *datastore.Datastore) *TaxRate {
	t := new(TaxRate)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("taxrate")
}
