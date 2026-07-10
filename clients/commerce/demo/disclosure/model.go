package disclosure

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

var kind = "disclosure"

func (d Disclosure) Kind() string {
	return kind
}

func (d *Disclosure) Init(db *datastore.Datastore) {
	d.BaseModel.Init(db, d)
}

func (d *Disclosure) Defaults() {
}

func New(db *datastore.Datastore) *Disclosure {
	d := new(Disclosure)
	d.Init(db)
	d.Defaults()
	return d
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
