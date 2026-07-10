package site

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/netlify"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Site]("site") }

type Site struct {
	mixin.Model[Site]

	Domain string `json:"domain"`
	Name   string `json:"name"`
	Url    string `json:"url"`

	Netlify_ netlify.Site `json:"-"`
}

// Return netlify overriden with our local properties
func (s Site) Netlify() *netlify.Site {
	s.Netlify_.Name = s.Name
	s.Netlify_.Domain = s.Domain
	return &s.Netlify_
}

func (s *Site) SetNetlify(nsite *netlify.Site) {
	s.Netlify_ = *nsite
}

// New creates a new Site wired to the given datastore.
func New(db *datastore.Datastore) *Site {
	s := new(Site)
	s.Init(db)
	return s
}

// Query returns a datastore query for sites.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("site")
}
