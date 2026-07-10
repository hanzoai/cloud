package parallel

import (
	"reflect"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
)

// Precompute a few common types
var (
	datastoreType = reflect.TypeOf((*datastore.Datastore)(nil))
	keyType       = reflect.TypeOf((*datastore.Key)(nil)).Elem()
)

// Create a new entity instance of a given type
func newEntity(db *datastore.Datastore, entityType reflect.Type) mixin.Entity {
	entity := reflect.New(entityType).Interface().(mixin.Entity)
	model := mixin.BaseModel{Db: db, Entity: entity}

	// Set model on entity
	field := reflect.Indirect(reflect.ValueOf(entity)).FieldByName("Model")
	field.Set(reflect.ValueOf(model))
	return entity
}
