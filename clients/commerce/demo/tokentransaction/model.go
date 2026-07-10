package tokentransaction

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

var kind = "tokentransaction"

func (t Transaction) Kind() string {
	return kind
}

func (t *Transaction) Init(db *datastore.Datastore) {
	t.BaseModel.Init(db, t)
}

func (t *Transaction) Defaults() {
}

func New(db *datastore.Datastore) *Transaction {
	t := new(Transaction)
	t.Init(db)
	t.Defaults()
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
