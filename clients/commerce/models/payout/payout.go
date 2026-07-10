package payout

import (
	"github.com/hanzoai/orm"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Status represents the payout lifecycle state.
type Status string

const (
	Pending   Status = "pending"
	InTransit Status = "in_transit"
	Paid      Status = "paid"
	Failed    Status = "failed"
	Canceled  Status = "canceled"
)

var kind = "billing-payout"

// Payout represents an outbound transfer to a bank account or card.

func init() { orm.Register[Payout]("billing-payout") }

type Payout struct {
	mixin.Model[Payout]

	Amount          int64         `json:"amount"` // cents
	Currency        currency.Type `json:"currency"`
	Status          Status        `json:"status"`
	DestinationType string        `json:"destinationType"` // "bank_account" | "card"
	DestinationId   string        `json:"destinationId"`   // payment method ID
	Description     string        `json:"description,omitempty"`
	ArrivalDate     time.Time     `json:"arrivalDate,omitempty"`
	ProviderRef     string        `json:"providerRef,omitempty"`
	FailureCode     string        `json:"failureCode,omitempty"`
	FailureMessage  string        `json:"failureMessage,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}



func (p *Payout) Defaults() {
	p.Parent = p.Datastore().NewKey("synckey", "", 1, nil)
	if p.Status == "" {
		p.Status = Pending
	}
	if p.Currency == "" {
		p.Currency = "usd"
	}
}

func (p *Payout) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	if len(p.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(p.Metadata_), &p.Metadata)
	}

	return err
}

func (p *Payout) Save() (ps []datastore.Property, err error) {
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))
	return datastore.SaveStruct(p)
}

func (p *Payout) Validator() *val.Validator {
	return nil
}

// MarkInTransit transitions payout to in-transit.
func (p *Payout) MarkInTransit() error {
	if p.Status != Pending {
		return fmt.Errorf("can only transit pending payouts, current: %s", p.Status)
	}
	p.Status = InTransit
	return nil
}

// MarkPaid transitions payout to paid.
func (p *Payout) MarkPaid() error {
	if p.Status != Pending && p.Status != InTransit {
		return fmt.Errorf("can only pay pending/in-transit payouts, current: %s", p.Status)
	}
	p.Status = Paid
	p.ArrivalDate = time.Now()
	return nil
}

// MarkFailed transitions payout to failed.
func (p *Payout) MarkFailed(code, message string) error {
	p.Status = Failed
	p.FailureCode = code
	p.FailureMessage = message
	return nil
}

// Cancel transitions payout to canceled.
func (p *Payout) Cancel() error {
	if p.Status != Pending {
		return fmt.Errorf("can only cancel pending payouts, current: %s", p.Status)
	}
	p.Status = Canceled
	return nil
}

func New(db *datastore.Datastore) *Payout {
	p := new(Payout)
	p.Init(db)
	p.Defaults()
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
