// Package refundreason is the refund-reason lookup (Medusa v2 parity:
// refund-reason) — the categorized reason a payment was refunded.
package refundreason

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[RefundReason]("refund-reason") }

// RefundReason categorizes why a payment was refunded.
type RefundReason struct {
	mixin.Model[RefundReason]

	Label       string `json:"label"`
	Description string `json:"description" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (r *RefundReason) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(r, ps); err != nil {
		return err
	}
	if len(r.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(r.Metadata_), &r.Metadata)
	}
	return err
}

func (r *RefundReason) Save() ([]datastore.Property, error) {
	r.Metadata_ = string(json.EncodeBytes(&r.Metadata))
	return datastore.SaveStruct(r)
}

func New(db *datastore.Datastore) *RefundReason {
	r := new(RefundReason)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("refund-reason")
}
