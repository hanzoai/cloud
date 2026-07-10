// Package returnreason is the return-reason lookup (Medusa v2 parity:
// return-reason) — the categorized reason a customer returns an item.
package returnreason

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ReturnReason]("return-reason") }

// ReturnReason categorizes why an item was returned (e.g. "wrong_size").
type ReturnReason struct {
	mixin.Model[ReturnReason]

	Value       string `json:"value"` // code, e.g. "wrong_size"
	Label       string `json:"label"`
	Description string `json:"description" datastore:",noindex"`

	// ParentReturnReasonId nests reasons (e.g. "damaged" > "damaged_in_transit").
	ParentReturnReasonId string `json:"parentReturnReasonId,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (r *ReturnReason) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(r, ps); err != nil {
		return err
	}
	if len(r.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(r.Metadata_), &r.Metadata)
	}
	return err
}

func (r *ReturnReason) Save() ([]datastore.Property, error) {
	r.Metadata_ = string(json.EncodeBytes(&r.Metadata))
	return datastore.SaveStruct(r)
}

func New(db *datastore.Datastore) *ReturnReason {
	r := new(ReturnReason)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("return-reason")
}
