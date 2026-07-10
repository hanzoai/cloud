package datastore

import (
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
)

// Key is the interface for datastore keys
type Key = iface.Key

// Export key functions
var EncodeKey = key.Encode
var DecodeKey = key.Decode

// convertKey converts a Key to a *key.DatastoreKey
func convertKey(k Key) *key.DatastoreKey {
	if k == nil {
		return nil
	}
	return key.ToDatastoreKey(k)
}

// convertKeys converts a slice of keys
func convertKeys(keys interface{}) []*key.DatastoreKey {
	switch v := keys.(type) {
	case []Key:
		n := len(v)
		dskeys := make([]*key.DatastoreKey, n)
		for i := 0; i < n; i++ {
			dskeys[i] = key.ToDatastoreKey(v[i])
		}
		return dskeys
	case []*key.DatastoreKey:
		return v
	default:
		panic(fmt.Errorf("Invalid slice of keys: %v", keys))
	}
}

// Encode/decode hashid keys
func (d *Datastore) DecodeKey(encoded string) (*key.DatastoreKey, error) {
	return DecodeKey(d.Context, encoded)
}

func (d *Datastore) EncodeKey(k Key) string {
	return EncodeKey(d.Context, k)
}

// Wrap key creation funcs
func (d *Datastore) NewKey(kind, stringID string, intID int64, parent Key) *key.DatastoreKey {
	return key.NewKey(d.Context, kind, stringID, intID, convertKey(parent))
}

func (d *Datastore) NewIncompleteKey(kind string, parent Key) *key.DatastoreKey {
	return key.NewIncompleteKey(d.Context, kind, convertKey(parent))
}

// Create helpers
func (d *Datastore) NewKeyFromId(id string) *key.DatastoreKey {
	return key.NewFromId(d.Context, id)
}

func (d *Datastore) NewKeyFromInt(kind string, id interface{}, parent Key) (*key.DatastoreKey, error) {
	return key.NewFromInt(d.Context, kind, id, parent)
}

func (d *Datastore) NewKeyFromString(kind string, id string, parent Key) *key.DatastoreKey {
	return d.NewKey(kind, id, 0, parent)
}

func (d *Datastore) AllocateID(kind string, parent Key) int64 {
	id, _ := d.AllocateIDs(kind, parent, 1)
	return id
}

func (d *Datastore) AllocateIDs(kind string, parent Key, n int) (int64, int64) {
	// In the new db package, we generate IDs client-side
	// This simulates the old behavior of allocating sequential IDs
	low := d.allocateCounter
	d.allocateCounter += int64(n)
	high := d.allocateCounter
	return low, high
}

func (d *Datastore) AllocateKey(kind string, parent Key) *key.DatastoreKey {
	id := d.AllocateID(kind, parent)
	return d.NewKey(kind, "", id, parent)
}

// Datastore uses a key's ancestry to allocate unique integer IDs. If you
// allocate an ID with a nil parent you get an "orphaned" ID, i.e., an ID which
// does not use ancestry to determine uniqueness.  We have historically
// depended on this behavior for cheap, monotonically increasing order numbers
// (which are calculated from the key's integer id component).
func (d *Datastore) AllocateOrphanID(kind string) int64 {
	id, _ := d.AllocateIDs(kind, nil, 1)
	return id
}

func (d *Datastore) AllocateOrphanKey(kind string, parent Key) *key.DatastoreKey {
	id := d.AllocateOrphanID(kind)
	return d.NewKey(kind, "", id, parent)
}
