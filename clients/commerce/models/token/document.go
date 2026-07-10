package token

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/searchpartial"
)

type Document struct {
	mixin.DocumentSaveLoad `datastore:"-" json:"-"`

	// Special Kind Facet
	Kind string `search:",facet"`

	Id_           string
	Email         string
	EmailPartials string
	UserId        string
	// Expires       float64

	CreatedAt float64
	UpdatedAt float64
}

func (d Document) Id() string {
	return string(d.Id_)
}

func (d *Document) Init() {
	d.SetDocument(d)
}

func (t Token) Document() mixin.Document {
	emailUser := strings.Split(t.Email, "@")[0]

	doc := &Document{}
	doc.Init()
	doc.Kind = "token"
	doc.Id_ = t.Id()
	doc.Email = t.Email
	doc.EmailPartials = searchpartial.Partials(emailUser) + " " + emailUser
	doc.UserId = t.UserId

	doc.CreatedAt = float64(t.CreatedAt.Unix())
	doc.UpdatedAt = float64(t.UpdatedAt.Unix())

	// doc.Expires = float64(t.Expires.Unix())

	return doc
}
