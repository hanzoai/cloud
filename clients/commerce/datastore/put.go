package datastore

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
)

// Helper to create new incomplete key from string kind
func convertKeyOrKind(ds *Datastore, keyOrKind interface{}) *key.DatastoreKey {
	switch v := keyOrKind.(type) {
	case string:
		return ds.NewIncompleteKey(v, nil)
	case Key:
		return convertKey(v)
	default:
		panic(fmt.Errorf("Invalid key or kind: %v", keyOrKind))
	}
}

// Puts entity using provided key or kind
func (d *Datastore) Put(keyOrKind interface{}, val interface{}) (*key.DatastoreKey, error) {
	if d.database == nil {
		return nil, errors.New("datastore: database not initialized")
	}

	// Structural money-mint gate (mintauth.Enforce): a write that mints spendable
	// balance is refused when its context is HTTP-gated but not authorized. This
	// is the ONE place the invariant lives — every ledger write, on any route or
	// none, funnels through Put, so no handler can mint without authorization.
	// No-op for non-ledger entities, non-mint writes, and ungated (cron/test) ctx.
	if err := mintauth.Enforce(d.Context, val); err != nil {
		d.warn("Refused unauthorized mint (%v, %#v): %v", convertKeyOrKind(d, keyOrKind), val, err, d.Context)
		return nil, err
	}

	dskey := convertKeyOrKind(d, keyOrKind)
	dbKey := dskey.ToDBKey(d.database)

	resultKey, err := d.database.Put(d.Context, dbKey, val)
	if err != nil {
		d.warn("Unable to put (%v, %#v): %v", dskey, val, err, d.Context)
		return nil, err
	}

	// Update the key if it was incomplete
	if dskey.Incomplete() && resultKey != nil {
		dskey = key.FromDBKey(resultKey)
	}

	return dskey, nil
}

// Keys may be either either []datastore.Key or []*key.DatastoreKey, vals expected in typical format
func (d *Datastore) PutMulti(keys interface{}, vals interface{}) ([]*key.DatastoreKey, error) {
	if d.database == nil {
		return nil, errors.New("datastore: database not initialized")
	}

	// Same money-mint gate as Put, applied per-element for batch writes.
	if rv := reflect.ValueOf(vals); rv.Kind() == reflect.Slice {
		for i := 0; i < rv.Len(); i++ {
			if err := mintauth.Enforce(d.Context, rv.Index(i).Interface()); err != nil {
				d.warn("Refused unauthorized mint in PutMulti[%d]: %v", i, err, d.Context)
				return nil, err
			}
		}
	}

	dskeys := convertKeys(keys)

	// Convert to db.Key slice
	dbKeys := make([]db.Key, len(dskeys))
	for i, k := range dskeys {
		dbKeys[i] = k.ToDBKey(d.database)
	}

	resultKeys, err := d.database.PutMulti(d.Context, dbKeys, vals)
	if err != nil {
		return nil, err
	}

	// Convert result keys back to DatastoreKeys
	result := make([]*key.DatastoreKey, len(resultKeys))
	for i, k := range resultKeys {
		result[i] = key.FromDBKey(k)
	}

	return result, nil
}

// Gets an entity using datastore.Key or encoded Key
func (d *Datastore) MustPut(keyOrKind interface{}, value interface{}) *key.DatastoreKey {
	if k, err := d.Put(keyOrKind, value); err != nil {
		panic(err)
	} else {
		return k
	}
}

// Gets an entity using datastore.Key or encoded Key
func (d *Datastore) MustPutMulti(keys interface{}, vals interface{}) []*key.DatastoreKey {
	if keys, err := d.PutMulti(keys, vals); err != nil {
		panic(err)
	} else {
		return keys
	}
}
