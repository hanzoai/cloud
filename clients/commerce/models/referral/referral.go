package referral

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Referral]("referral") }

type Event string

const (
	NewOrder Event = "new-order"
	NewUser  Event = "new-user"
)

type Referrer struct {
	Id            string `json:"id"`
	UserId        string `json:"userId"`
	AffiliateId   string `json:"affiliateId"`
	WoopraDomains string `json:"woopraDomains"`
}

type Fee struct {
	Currency currency.Type  `json:"currency,omitempty"`
	Id       string         `json:"id,omitempty"`
	Amount   currency.Cents `json:"amount,omitempty"`
}

type Referral struct {
	mixin.Model[Referral]

	Type Event `json:"event"`

	// User created by referral
	UserId string `json:"userId"`

	// Order created by referral
	OrderId string `json:"orderId"`

	// Referred by
	Referrer Referrer `json:"referrer,omitempty"`

	Fee Fee `json:"fee,omitempty"`

	Client      client.Client `json:"-"`
	Blacklisted bool          `json:"blacklisted,omitempty"`
	Duplicate   bool          `json:"duplicate,omitempty"`
	Revoked     bool          `json:"revoked"`
}

func New(db *datastore.Datastore) *Referral {
	r := new(Referral)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("referral")
}
