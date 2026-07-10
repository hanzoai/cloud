package movie

import (
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
)

type Document struct {
	mixin.DocumentSaveLoad `datastore:"-" json:"-"`

	// Special Kind Option
	Kind string `search:",facet"`

	Id_  string
	Slug string
	EIDR string
	IMDB string

	Name        string
	Headline    string
	Description string

	AvailableOption string `search:"available,facet"`
	HiddenOption    string `search:"hidden,facet"`
}

func (d *Document) Id() string {
	return d.Id_
}

func (d *Document) Init() {
	d.SetDocument(d)
}

func (m Movie) Document() mixin.Document {
	doc := &Document{}
	doc.Init()
	doc.Kind = kind
	doc.Id_ = m.Id()
	doc.Slug = m.Slug
	doc.IMDB = m.IMDB
	doc.EIDR = m.EIDR

	if m.Available {
		doc.AvailableOption = "available"
	}

	if m.Hidden {
		doc.HiddenOption = "hidden"
	}

	return doc
}
