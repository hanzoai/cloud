// Package approval is the B2B spend-approval domain: before a company's
// (models/company) cart converts to an order, it can require sign-off from an
// admin or sales manager. The pure state machine governing the pending →
// approved/rejected transition lives in service.go.
package approval

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Approval]("approval") }

// Approval types.
const (
	TypeAdmin        = "admin"
	TypeSalesManager = "sales_manager"
)

// Approval lifecycle states.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// Approval is a spend-approval gate on a B2B cart/order. It is resolved once
// its status leaves pending; NextStatus (service.go) enforces the transitions.
type Approval struct {
	mixin.Model[Approval]

	CartId    string `json:"cartId"`
	OrderId   string `json:"orderId,omitempty"`
	CompanyId string `json:"companyId"`

	// Type is the approver role required: admin or sales_manager.
	Type string `json:"type"`

	Status string `json:"status" orm:"default:pending"`

	// HandledBy records the user who approved or rejected.
	HandledBy string `json:"handledBy,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (a *Approval) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(a, ps); err != nil {
		return err
	}
	if len(a.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(a.Metadata_), &a.Metadata)
	}
	return err
}

func (a *Approval) Save() ([]datastore.Property, error) {
	a.Metadata_ = string(json.EncodeBytes(&a.Metadata))
	return datastore.SaveStruct(a)
}

// IsResolved reports whether the approval has been decided (no longer pending).
func (a *Approval) IsResolved() bool {
	return a.Status != StatusPending
}

func New(db *datastore.Datastore) *Approval {
	a := new(Approval)
	a.Init(db)
	return a
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("approval")
}
