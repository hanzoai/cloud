package disclosure

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

	Publication string
	Hash        string
	Type        string
	Receiver    string
}

func (d Document) Id() string {
	return string(d.Id_)
}

func (d *Document) Init() {
	d.SetDocument(d)
}

func (d Disclosure) Document() mixin.Document {

	doc := &Document{}
	doc.Init()
	doc.Kind = search.Atom(kind)
	doc.Id_ = d.Id()

	doc.CreatedAt = float64(d.CreatedAt.Unix())
	doc.UpdatedAt = float64(d.UpdatedAt.Unix())

	doc.Publication = d.Publication
	doc.Type = d.Type
	doc.Receiver = d.Receiver

	return doc
}
