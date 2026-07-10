package test

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
)

type User struct {
	mixin.BaseModel
	mixin.AccessTokens

	Name    string
	BCreate string
}

func (u User) Kind() string {
	return "user"
}

func (u *User) Init(db *datastore.Datastore) {
	u.BaseModel = mixin.BaseModel{Db: db, Entity: u}
	u.AccessTokens = mixin.AccessTokens{Entity: u}
}

func (u *User) Document() mixin.Document {
	return nil
}

func newUser(db *datastore.Datastore) *User {
	u := new(User)
	u.Init(db)
	return u
}

func (u *User) BeforeCreate() error {
	u.BCreate = "BC"
	return nil
}
