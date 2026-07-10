package mixin

import (
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/reflect"
)

// Create a new zero'd entity of this type
func (m *BaseModel) Zero() Entity {
	// Get type of entity
	entity := reflect.ValueOf(m.Entity)

	// De-pointer if necessary
	for entity.Kind() == reflect.Ptr {
		entity = reflect.Indirect(entity)
	}

	// Actual type
	typ := entity.Type()

	// Create new entity
	entity = reflect.New(typ)
	return entity.Interface().(Entity)
}

// Create a clone of current entity
func (m *BaseModel) Clone() Entity {
	entity := m.Zero()
	if err := reflect.Copy(m.Entity, entity); err != nil {
		log.Warn("Unable to copy of model: %v", err, m.Db.Context)
	}
	return entity
}

// Create a clone of currenty entity using only JSON-serializable data
func (m *BaseModel) CloneFromJSON() Entity {
	buf := json.EncodeBuffer(m.Entity)
	entity := m.Zero()
	json.DecodeBuffer(buf, entity)
	return entity
}

// Create a slice of entity type suitable for use with datastore.GetAll, etc.
func (m *BaseModel) Slice() interface{} {
	typ := reflect.TypeOf(m.Entity)
	slice := reflect.MakeSlice(reflect.SliceOf(typ), 0, 0)
	ptr := reflect.New(slice.Type())
	ptr.Elem().Set(slice)
	return ptr.Interface()
}

// Serialize entity to JSON
func (m *BaseModel) JSON() []byte {
	return json.EncodeBytes(m.Entity)
}

func (m *BaseModel) JSONString() string {
	return string(m.JSON())
}
