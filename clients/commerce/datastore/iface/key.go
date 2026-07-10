package iface

// Key is the interface for datastore keys.
// This is compatible with both the legacy appengine datastore
// and the new db.Key interface.
type Key interface {
	AppID() string
	Encode() string
	Equal(o Key) bool
	GobDecode(buf []byte) error
	GobEncode() ([]byte, error)
	Incomplete() bool
	IntID() int64
	Kind() string
	MarshalJSON() ([]byte, error)
	Namespace() string
	Parent() Key
	String() string
	StringID() string
	UnmarshalJSON(buf []byte) error
}
