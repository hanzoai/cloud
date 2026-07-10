package hashid

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace/consts"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

var (
	idToNamespace = make(map[int64]string)
	namespaceToId = make(map[string]int64)
)

// IgnoreFieldMismatch is a helper for field mismatch errors
func IgnoreFieldMismatch(err error) error {
	if err == nil {
		return nil
	}
	// For now, just return the error as-is
	// The actual implementation would need to check error types
	return err
}

func cache(namespace string, id int64) {
	idToNamespace[id] = namespace
	namespaceToId[namespace] = id
}

type Model struct {
	Id_       string
	CreatedAt time.Time
	UpdatedAt time.Time
	Deleted   bool
}

type Namespace struct {
	// Included for compatibility with namespace models
	Model

	IntId int64
	Name  string
}

func fmtNs(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// Query for namespace by Name - simplified version without appengine
func queryNamespace(ctx context.Context, filter string, value interface{}) (*Namespace, bool, error) {
	// In the new architecture, namespace lookups would go through the db package
	// For now, we'll use cached values or return defaults
	ns := new(Namespace)

	// Check cache first for Name lookups
	if filter == "Name=" {
		name, ok := value.(string)
		if ok {
			if id, cached := namespaceToId[name]; cached {
				ns.Name = name
				ns.IntId = id
				return ns, true, nil
			}
		}
	}

	// Check cache for IntId lookups
	if filter == "IntId=" {
		id, ok := value.(int64)
		if !ok {
			if idInt, intOk := value.(int); intOk {
				id = int64(idInt)
			}
		}
		if name, cached := idToNamespace[id]; cached {
			ns.Name = name
			ns.IntId = id
			return ns, true, nil
		}
	}

	// For default namespace, always return success
	if filter == "Name=" && value == "" {
		return &Namespace{Name: "", IntId: 0}, true, nil
	}

	// Not found in cache
	return nil, false, nil
}

// Get IntID for namespace
func getId(ctx context.Context, name string) int64 {
	if name == consts.Namespace {
		return 0
	}

	// Check cache first
	if id, ok := namespaceToId[name]; ok {
		return id
	}

	ns, ok, err := queryNamespace(ctx, "Name=", name)

	// Blow up if we can't complete query or find namespace
	if err != nil {
		log.Warn("Error querying namespace: %v", err)
		return 0
	}

	if !ok {
		log.Warn("Namespace '%s' not found, using default", name)
		return 0
	}

	// Cache for future use
	cache(ns.Name, ns.IntId)

	return ns.IntId
}

// Get namespace from organization using its IntID
func getName(ctx context.Context, id int64) (string, error) {
	if id == 0 {
		return consts.Namespace, nil
	}

	// Check cache first
	if name, ok := idToNamespace[id]; ok {
		return name, nil
	}

	ns, ok, err := queryNamespace(ctx, "IntId=", id)

	// Query failed for some inexplicable reason
	if err != nil {
		return "", err
	}

	// Failed to find matching namespace
	if !ok {
		return "", fmt.Errorf("Namespace with id %d does not exist", id)
	}

	// Cache for future use
	cache(ns.Name, ns.IntId)

	return ns.Name, nil
}

// Get namespaced context
func getContext(ctx context.Context, namespace string) context.Context {
	if namespace == "" {
		return ctx
	}

	return nscontext.WithNamespace(ctx, namespace)
}

// Get namespaced context for namespaces
func getNamespaceContext(ctx context.Context) context.Context {
	return getContext(ctx, consts.Namespace)
}

// Encodes organization namespace into its IntID
func encodeNamespace(ctx context.Context, namespace string) int {
	// Default namespace
	if namespace == "" {
		return 0
	}

	id, ok := namespaceToId[namespace]
	if !ok {
		id = getId(ctx, namespace)

		// Cache result
		cache(namespace, id)
	}

	return int(id)
}

func decodeNamespace(ctx context.Context, encoded int) (ns string, err error) {
	// Default namespace
	if encoded == 0 {
		return "", nil
	}

	id := int64(encoded)
	ns, ok := idToNamespace[id]
	if !ok {
		if ns, err = getName(ctx, id); err != nil {
			return "", err
		}

		// Cache result
		cache(ns, id)
	}

	return ns, nil
}

// Key interface for encoding/decoding
// Note: Due to Go's strict interface matching, this interface defines
// Parent() as returning interface{} to allow any key type to be passed
type Key interface {
	Kind() string
	IntID() int64
	StringID() string
	Namespace() string
}

// KeyWithParent is for keys that have a parent
type KeyWithParent interface {
	Key
	Parent() interface{}
}

// getParent extracts the parent key from a key using type assertion
func getParent(key interface{}) interface{} {
	if k, ok := key.(KeyWithParent); ok {
		return k.Parent()
	}
	// Try reflection for types with Parent() that return their own type
	if k, ok := key.(interface{ Parent() interface{} }); ok {
		return k.Parent()
	}
	return nil
}

// EncodeKey encodes a key to a string using hashid encoding
// The key parameter accepts any type that implements the Key interface
func EncodeKey(ctx context.Context, key interface{}) string {
	if key == nil {
		return ""
	}

	// Extract key properties
	k, ok := key.(Key)
	if !ok {
		return ""
	}

	id := int(k.IntID())

	// Return empty if incomplete key
	if id == 0 && k.StringID() == "" {
		return ""
	}

	// If we have a string ID, just return it
	if k.StringID() != "" {
		return k.StringID()
	}

	ids := make([]int, 2)
	ids[0] = encodeKind(k.Kind())
	ids[1] = id

	// Add ancestor keys
	parent := getParent(key)
	for parent != nil {
		if pk, ok := parent.(Key); ok {
			ids = append(ids, encodeKind(pk.Kind()), int(pk.IntID()))
			parent = getParent(parent)
		} else {
			break
		}
	}

	// Default to default namespace
	namespace := 0

	// Get namespace from key
	namespace = encodeNamespace(ctx, k.Namespace())

	// Append namespace
	ids = append(ids, namespace)

	encoded := Encode(ids...)

	log.Debug("%s%v encoded to '%s'", fmtNs(k.Namespace()), key, encoded)

	return encoded
}

func DecodeKey(ctx context.Context, encoded string) (db.Key, error) {
	ids, err := Decode(encoded)
	if err != nil {
		return nil, err
	}

	n := len(ids)

	// A valid key without parents will have exactly 3 segments: namespace,
	// kind and intid. For each parent we expect two more segments.
	if n < 3 || (n-3)%2 == 1 {
		return nil, fmt.Errorf("Invalid number of segments: %v", ids)
	}

	// Set namespace
	ns, err := decodeNamespace(ctx, ids[n-1])
	if err != nil {
		return nil, err
	}

	ctx = getContext(ctx, ns)

	// root key
	kind, err := decodeKind(ids[n-3])
	if err != nil {
		return nil, err
	}

	// Build the key chain from root to leaf
	key := &decodedKey{
		kind:      kind,
		intID:     int64(ids[n-2]),
		namespace: ns,
	}

	// root key is always last key, so reverse through list to recreate key
	for i := n - 4; i >= 0; i = i - 2 {
		kind, err := decodeKind(ids[i-1])
		if err != nil {
			return nil, err
		}
		key = &decodedKey{
			kind:      kind,
			intID:     int64(ids[i]),
			parent:    key,
			namespace: ns,
		}
	}

	log.Debug("'%s' decoded to %s%v", encoded, fmtNs(ns), key)

	return key, nil
}

// decodedKey implements db.Key for decoded keys
type decodedKey struct {
	kind      string
	stringID  string
	intID     int64
	parent    *decodedKey
	namespace string
}

func (k *decodedKey) Kind() string      { return k.kind }
func (k *decodedKey) StringID() string  { return k.stringID }
func (k *decodedKey) IntID() int64      { return k.intID }
func (k *decodedKey) Namespace() string { return k.namespace }
func (k *decodedKey) Incomplete() bool  { return k.stringID == "" && k.intID == 0 }

func (k *decodedKey) Parent() db.Key {
	if k.parent == nil {
		return nil
	}
	return k.parent
}

func (k *decodedKey) Encode() string {
	if k.stringID != "" {
		return k.stringID
	}
	return fmt.Sprintf("%d", k.intID)
}

func (k *decodedKey) Equal(other db.Key) bool {
	if other == nil {
		return false
	}
	return k.Kind() == other.Kind() && k.Encode() == other.Encode()
}

func MustDecodeKey(ctx context.Context, encoded string) db.Key {
	key, err := DecodeKey(ctx, encoded)
	if err != nil {
		panic(err)
	}

	return key
}

func KeyExists(ctx context.Context, encoded string) (bool, error) {
	_, err := DecodeKey(ctx, encoded)
	if err != nil {
		return false, err
	}

	// Cannot check existence without a database connection
	// The actual check would need to be done through the db package
	return false, nil
}

// RegisterNamespace caches a namespace mapping
func RegisterNamespace(name string, id int64) {
	cache(name, id)
}
