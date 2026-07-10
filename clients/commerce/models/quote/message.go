package quote

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[QuoteMessage]("quote-message") }

// Author types on a quote thread.
const (
	AuthorCustomer = "customer"
	AuthorMerchant = "merchant"
)

// QuoteMessage is a single message on a quote's negotiation thread. ItemId
// optionally scopes the message to a specific line of the underlying order.
type QuoteMessage struct {
	mixin.Model[QuoteMessage]

	QuoteId    string `json:"quoteId"`
	AuthorId   string `json:"authorId"`
	AuthorType string `json:"authorType"`
	Text       string `json:"text"`

	// ItemId optionally references the order line this message is about.
	ItemId string `json:"itemId,omitempty"`
}

func (m *QuoteMessage) Load(ps []datastore.Property) error {
	return datastore.LoadStruct(m, ps)
}

func (m *QuoteMessage) Save() ([]datastore.Property, error) {
	return datastore.SaveStruct(m)
}

func NewMessage(db *datastore.Datastore) *QuoteMessage {
	m := new(QuoteMessage)
	m.Init(db)
	return m
}

func QueryMessages(db *datastore.Datastore) datastore.Query {
	return db.Query("quote-message")
}
