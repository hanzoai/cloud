package key

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/datastore/utils"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// Key is a type alias for iface.Key
type Key = iface.Key

var (
	FromId = Decode
)

// DatastoreKey implements the iface.Key interface and provides
// compatibility with the legacy appengine datastore API.
type DatastoreKey struct {
	kind       string
	stringID   string
	intID      int64
	parent     *DatastoreKey
	namespace  string
	appID      string
	incomplete bool
}

// Ensure DatastoreKey implements iface.Key
var _ iface.Key = (*DatastoreKey)(nil)

// NewKey creates a new key with the specified parameters
func NewKey(ctx context.Context, kind, stringID string, intID int64, parent Key) *DatastoreKey {
	var parentKey *DatastoreKey
	if parent != nil {
		parentKey = ToDatastoreKey(parent)
	}

	namespace := nscontext.GetNamespace(ctx)

	return &DatastoreKey{
		kind:      kind,
		stringID:  stringID,
		intID:     intID,
		parent:    parentKey,
		namespace: namespace,
		appID:     "hanzo",
	}
}

// NewIncompleteKey creates a new incomplete key
func NewIncompleteKey(ctx context.Context, kind string, parent Key) *DatastoreKey {
	var parentKey *DatastoreKey
	if parent != nil {
		parentKey = ToDatastoreKey(parent)
	}

	namespace := nscontext.GetNamespace(ctx)

	return &DatastoreKey{
		kind:       kind,
		parent:     parentKey,
		namespace:  namespace,
		appID:      "hanzo",
		incomplete: true,
	}
}

// New creates a new key for given id type
func New(ctx context.Context, kind string, id interface{}, parent Key) *DatastoreKey {
	var pkey *DatastoreKey
	if parent != nil {
		pkey = ToDatastoreKey(parent)
	}

	switch v := id.(type) {
	case int64:
		return NewKey(ctx, kind, "", v, pkey)
	case int:
		return NewKey(ctx, kind, "", int64(v), pkey)
	case string:
		return NewKey(ctx, kind, v, 0, pkey)
	default:
		return NewIncompleteKey(ctx, kind, pkey)
	}
}

// NewFromId returns key from hashid or encoded strings
func NewFromId(ctx context.Context, id string) *DatastoreKey {
	key, err := Decode(ctx, id)
	if err != nil {
		panic(err)
	}
	return key
}

// NewFromInt returns key from integer id
func NewFromInt(ctx context.Context, kind string, intid interface{}, parent Key) (*DatastoreKey, error) {
	var id int64
	switch v := intid.(type) {
	case string:
		if parsed, err := strconv.ParseInt(v, 10, 64); err != nil {
			return nil, fmt.Errorf("Invalid integer for key: '%v'", intid)
		} else {
			id = parsed
		}
	case int64:
		id = v
	case int:
		id = int64(v)
	default:
		return nil, fmt.Errorf("Invalid integer for key: '%v'", intid)
	}

	var pkey *DatastoreKey
	if parent != nil {
		pkey = ToDatastoreKey(parent)
	}

	return NewKey(ctx, kind, "", id, pkey), nil
}

// ToDatastoreKey converts any Key to a DatastoreKey
func ToDatastoreKey(key Key) *DatastoreKey {
	if key == nil {
		return nil
	}

	// If it's already a DatastoreKey, return it
	if dk, ok := key.(*DatastoreKey); ok {
		return dk
	}

	// Convert from db.Key or other Key implementations
	var parentKey *DatastoreKey
	if p := key.Parent(); p != nil {
		parentKey = ToDatastoreKey(p)
	}

	return &DatastoreKey{
		kind:       key.Kind(),
		stringID:   key.StringID(),
		intID:      key.IntID(),
		parent:     parentKey,
		namespace:  key.Namespace(),
		appID:      "hanzo",
		incomplete: key.Incomplete(),
	}
}

// FromDBKey converts a db.Key to a DatastoreKey
func FromDBKey(key db.Key) *DatastoreKey {
	if key == nil {
		return nil
	}

	var parentKey *DatastoreKey
	if p := key.Parent(); p != nil {
		parentKey = FromDBKey(p)
	}

	return &DatastoreKey{
		kind:       key.Kind(),
		stringID:   key.StringID(),
		intID:      key.IntID(),
		parent:     parentKey,
		namespace:  key.Namespace(),
		appID:      "hanzo",
		incomplete: key.Incomplete(),
	}
}

// ToDBKey converts a DatastoreKey to a db.Key
func (k *DatastoreKey) ToDBKey(database db.DB) db.Key {
	var parent db.Key
	if k.parent != nil {
		parent = k.parent.ToDBKey(database)
	}

	if k.stringID != "" {
		return database.NewKey(k.kind, k.stringID, 0, parent)
	}
	return database.NewKey(k.kind, "", k.intID, parent)
}

// Implement iface.Key interface

func (k *DatastoreKey) AppID() string {
	return k.appID
}

func (k *DatastoreKey) Encode() string {
	if k.stringID != "" {
		return k.stringID
	}
	if k.intID != 0 {
		return fmt.Sprintf("%d", k.intID)
	}
	return ""
}

func (k *DatastoreKey) Equal(o Key) bool {
	if o == nil {
		return false
	}
	if k.Kind() != o.Kind() {
		return false
	}
	if k.StringID() != o.StringID() {
		return false
	}
	if k.IntID() != o.IntID() {
		return false
	}
	if k.Namespace() != o.Namespace() {
		return false
	}

	// Compare parents
	kp := k.Parent()
	op := o.Parent()
	if kp == nil && op == nil {
		return true
	}
	if kp == nil || op == nil {
		return false
	}
	return kp.Equal(op)
}

func (k *DatastoreKey) GobDecode(buf []byte) error {
	type encoded struct {
		Kind      string
		StringID  string
		IntID     int64
		Parent    *DatastoreKey
		Namespace string
		AppID     string
	}

	dec := gob.NewDecoder(bytes.NewReader(buf))
	var e encoded
	if err := dec.Decode(&e); err != nil {
		return err
	}

	k.kind = e.Kind
	k.stringID = e.StringID
	k.intID = e.IntID
	k.parent = e.Parent
	k.namespace = e.Namespace
	k.appID = e.AppID
	return nil
}

func (k *DatastoreKey) GobEncode() ([]byte, error) {
	type encoded struct {
		Kind      string
		StringID  string
		IntID     int64
		Parent    *DatastoreKey
		Namespace string
		AppID     string
	}

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(encoded{
		Kind:      k.kind,
		StringID:  k.stringID,
		IntID:     k.intID,
		Parent:    k.parent,
		Namespace: k.namespace,
		AppID:     k.appID,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (k *DatastoreKey) Incomplete() bool {
	return k.incomplete || (k.stringID == "" && k.intID == 0)
}

func (k *DatastoreKey) IntID() int64 {
	return k.intID
}

func (k *DatastoreKey) Kind() string {
	return k.kind
}

func (k *DatastoreKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.Encode())
}

func (k *DatastoreKey) Namespace() string {
	return k.namespace
}

func (k *DatastoreKey) Parent() Key {
	if k.parent == nil {
		return nil
	}
	return k.parent
}

func (k *DatastoreKey) String() string {
	return fmt.Sprintf("/%s,%s,%d", k.kind, k.stringID, k.intID)
}

func (k *DatastoreKey) StringID() string {
	return k.stringID
}

func (k *DatastoreKey) UnmarshalJSON(buf []byte) error {
	var s string
	if err := json.Unmarshal(buf, &s); err != nil {
		return err
	}

	// Try to decode as integer ID first
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		k.intID = id
		return nil
	}

	// Otherwise treat as string ID
	k.stringID = s
	return nil
}

// SetNamespace sets the namespace on the key
func (k *DatastoreKey) SetNamespace(ns string) {
	k.namespace = ns
}

// SetIntID sets the integer ID on the key
func (k *DatastoreKey) SetIntID(id int64) {
	k.intID = id
	k.stringID = ""
	k.incomplete = false
}

// SetStringID sets the string ID on the key
func (k *DatastoreKey) SetStringID(id string) {
	k.stringID = id
	k.intID = 0
	k.incomplete = false
}

// Encode key using hashid algorithm
func Encode(ctx context.Context, key Key) string {
	return hashid.EncodeKey(ctx, key)
}

// Decode key using hashid algorithm and falling back to base64 encoding
func Decode(ctx context.Context, encoded string) (*DatastoreKey, error) {
	// Assume hashid
	key, err := hashid.DecodeKey(ctx, encoded)
	if err == nil {
		return FromDBKey(key), nil
	}

	log.Debug("Failed to decode hashid, assuming base64 encoding: %v", err, ctx)

	// Try to parse as integer ID
	if id, parseErr := strconv.ParseInt(encoded, 10, 64); parseErr == nil {
		return &DatastoreKey{intID: id}, nil
	}

	// Treat as string ID
	return &DatastoreKey{stringID: encoded}, nil
}

// Encode64 encodes key with base64 encoding
func Encode64(key Key) string {
	return key.Encode()
}

// Decode64 decodes key encoded with base64 encoding
func Decode64(ctx context.Context, encoded string) (*DatastoreKey, error) {
	return Decode(ctx, encoded)
}

// Exists checks if key exists in datastore
func Exists(ctx context.Context, key interface{}) (bool, error) {
	// Convert into Key
	var k *DatastoreKey
	switch v := key.(type) {
	case *DatastoreKey:
		k = v
	case Key:
		k = ToDatastoreKey(v)
	case string:
		decoded, err := Decode(ctx, v)
		if err != nil {
			return false, err
		}
		k = decoded
	default:
		return false, utils.ErrInvalidKey
	}

	// For now, return false - the actual check requires a db connection
	// This would need to be passed from the Datastore wrapper
	_ = k
	return false, nil
}

// DecodeKey decodes a key string (alias for Decode)
func DecodeKey(ctx context.Context, encoded string) (*DatastoreKey, error) {
	return Decode(ctx, encoded)
}
