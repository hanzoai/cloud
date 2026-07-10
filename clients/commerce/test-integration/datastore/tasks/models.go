package tasks

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
)

// Model 1
type Model struct {
	mixin.BaseModel
	Count int
}

func (m Model) Kind() string {
	return "user"
}

func (m *Model) Init(db *datastore.Datastore) {
	m.BaseModel = mixin.BaseModel{Db: db, Entity: m}
}

func (m Model) Document() mixin.Document {
	return nil
}

func (m *Model) Validator() *val.Validator {
	return val.New()
}

func NewModel(db *datastore.Datastore) *Model {
	m := new(Model)
	m.Init(db)
	return m
}

// Model 2
type Model2 struct {
	mixin.BaseModel
	Count int
}

func (m Model2) Kind() string {
	return "order"
}

func (m *Model2) Init(db *datastore.Datastore) {
	m.BaseModel = mixin.BaseModel{Db: db, Entity: m}
}

func (m Model2) Document() mixin.Document {
	return nil
}

func (m *Model2) Validator() *val.Validator {
	return val.New()
}

func NewModel2(db *datastore.Datastore) *Model2 {
	m := new(Model2)
	m.Init(db)
	return m
}
