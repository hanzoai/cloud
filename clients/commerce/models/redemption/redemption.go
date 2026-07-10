package coupon

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Redemption]("redemption") }

type Redemption struct {
	mixin.Model[Redemption]

	// Coupon code (need not be unique).
	Code string `json:"code"`
}

func (r *Redemption) Load(props []datastore.Property) (err error) {
	// Load supported properties
	return datastore.LoadStruct(r, props)
}

func (r *Redemption) Save() (props []datastore.Property, err error) {
	r.Code = strings.ToUpper(r.Code)

	// Save properties
	return datastore.SaveStruct(r)
}

func New(db *datastore.Datastore) *Redemption {
	r := new(Redemption)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("redemption")
}
