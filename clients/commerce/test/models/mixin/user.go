package test

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
)

type User struct {
	mixin.BaseModel
	mixin.AccessTokens

	Email string
	Name  string

	Friends []string
}

// We shadow the real user's kind because kinds must be defined in our hashid
// map or none of this works.
func (u User) Kind() string {
	return "user"
}

func (u *User) Defaults() {
	u.Friends = make([]string, 0)
}

func (u *User) Init(db *datastore.Datastore) {
	u.BaseModel.Init(db, u)
	u.AccessTokens = mixin.AccessTokens{Entity: u}
}

func (u *User) Document() mixin.Document {
	return nil
}

func newUser(db *datastore.Datastore) *User {
	u := new(User)
	u.Init(db)
	u.Defaults()
	return u
}

func (u *User) Validator() *val.Validator {
	return val.New()
}
