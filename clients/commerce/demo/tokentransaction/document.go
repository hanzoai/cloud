package tokentransaction

import (
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/search"
)

type Document struct {
	mixin.DocumentSaveLoad `datastore:"-" json:"-"`

	// Special Kind Facet
	Kind search.Atom `search:",facet"`

	Id_ string

	CreatedAt float64
	UpdatedAt float64

	Timestamp float64

	SendingAddress   string
	SendingUserId    string
	SendingName      string
	SendingState     string
	SendingCountry   string
	ReceivingAddress string
	ReceivingUserId  string
	ReceivingName    string
	ReceivingState   string
	ReceivingCountry string
	Protocol         string
	TransactionHash  string
}

func (d Document) Id() string {
	return string(d.Id_)
}

func (d *Document) Init() {
	d.SetDocument(d)
}

func (t Transaction) Document() mixin.Document {

	doc := &Document{}
	doc.Init()
	doc.Kind = search.Atom(kind)
	doc.Id_ = t.Id()

	doc.CreatedAt = float64(t.CreatedAt.Unix())
	doc.UpdatedAt = float64(t.UpdatedAt.Unix())
	doc.Timestamp = float64(t.Timestamp.Unix())

	doc.SendingAddress = t.SendingAddress
	doc.SendingUserId = t.SendingUserId
	doc.SendingName = t.SendingName
	doc.SendingState = t.SendingState
	doc.SendingCountry = t.SendingCountry
	doc.ReceivingAddress = t.ReceivingAddress
	doc.ReceivingUserId = t.ReceivingUserId
	doc.ReceivingName = t.ReceivingName
	doc.ReceivingState = t.ReceivingState
	doc.ReceivingCountry = t.ReceivingCountry
	doc.Protocol = t.Protocol
	doc.TransactionHash = t.TransactionHash

	return doc
}
