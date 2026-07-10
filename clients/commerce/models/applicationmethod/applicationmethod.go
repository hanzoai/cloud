package applicationmethod

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ApplicationMethod]("applicationmethod") }

type ApplicationMethod struct {
	mixin.Model[ApplicationMethod]

	PromotionId  string `json:"promotionId"`
	Value        int    `json:"value"`
	CurrencyCode string `json:"currencyCode"`
	MaxQuantity  int    `json:"maxQuantity"`
	Type         string `json:"type"`
	TargetType   string `json:"targetType"`
	Allocation   string `json:"allocation"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (a *ApplicationMethod) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(a, ps); err != nil {
		return err
	}

	if len(a.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(a.Metadata_), &a.Metadata)
	}

	return err
}

func (a *ApplicationMethod) Save() ([]datastore.Property, error) {
	a.Metadata_ = string(json.EncodeBytes(&a.Metadata))

	return datastore.SaveStruct(a)
}

func New(db *datastore.Datastore) *ApplicationMethod {
	a := new(ApplicationMethod)
	a.Init(db)
	return a
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("applicationmethod")
}
