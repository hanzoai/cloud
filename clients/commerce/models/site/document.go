package site

import (
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
)

type Document struct {
	// Special Kind Facet
	Kind string `search:",facet"`

	Id_    string
	Name   string
	Domain string
	Url    string
}

func (d Document) Id() string {
	return d.Id_
}

func (s Site) Document() mixin.Document {
	return &Document{
		Kind:   "site",
		Id_:    s.Id(),
		Name:   s.Name,
		Domain: s.Domain,
		Url:    s.Url,
	}
}
