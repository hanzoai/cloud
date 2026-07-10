package iface

// Iterator is the interface for datastore query iterators
type Iterator interface {
	Next(dst interface{}) (Key, error)
	Cursor() (Cursor, error)
}

// Cursor represents a position in a query result set
type Cursor interface {
	String() string
}

// Query is the interface for datastore queries.
// This is compatible with both the legacy appengine datastore
// and the new db.Query interface.
type Query interface {
	Ancestor(ancestor Key) Query
	Count() (int, error)
	Distinct() Query
	EventualConsistency() Query
	Filter(filterStr string, value interface{}) Query
	KeysOnly() Query
	Limit(limit int) Query
	Offset(offset int) Query
	Order(fieldName string) Query
	Project(fieldNames ...string) Query
	Run() Iterator
	Start(c Cursor) Query
	End(c Cursor) Query
	ByKey(key Key, dst interface{}) (Key, bool, error)
	ById(id string, dst interface{}) (Key, bool, error)
	IdExists(id string) (Key, bool, error)
	KeyExists(key Key) (bool, error)
	First(dst interface{}) (Key, bool, error)
	FirstKey() (Key, bool, error)
	GetAll(dst interface{}) ([]Key, error)
	GetModels(dst interface{}) error
	GetKeys() ([]Key, error)
}
