package token

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Token]("token") }

type Token struct {
	mixin.Model[Token]

	Email   string    `json:"email"`
	UserId  string    `json:"userId"`
	Used    bool      `json:"used"`
	Expires time.Time `json:"expires"`
}

func (t Token) Expired() bool {
	if t.Used || time.Now().After(t.Expires) {
		return true
	}
	return false
}

func New(db *datastore.Datastore) *Token {
	t := new(Token)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("token")
}
