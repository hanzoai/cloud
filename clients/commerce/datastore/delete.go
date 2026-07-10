package datastore

import (
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/cloud/clients/commerce/db"
)

// Delete an entity
func (d *Datastore) Delete(k Key) error {
	if d.database == nil {
		return errors.New("datastore: database not initialized")
	}

	dskey := key.ToDatastoreKey(k)
	dbKey := dskey.ToDBKey(d.database)
	return d.database.Delete(d.Context, dbKey)
}

func (d *Datastore) DeleteMulti(keys interface{}) error {
	if d.database == nil {
		return errors.New("datastore: database not initialized")
	}

	dskeys := convertKeys(keys)

	// Convert to db.Key slice
	dbKeys := make([]db.Key, len(dskeys))
	for i, k := range dskeys {
		dbKeys[i] = k.ToDBKey(d.database)
	}

	return d.database.DeleteMulti(d.Context, dbKeys)
}

// Gets an entity using datastore.Key or encoded Key
func (d *Datastore) MustDelete(k Key) {
	if err := d.Delete(k); err != nil {
		panic(err)
	}
}

// Gets an entity using datastore.Key or encoded Key
func (d *Datastore) MustDeleteMulti(keys interface{}) {
	if err := d.DeleteMulti(keys); err != nil {
		panic(err)
	}
}
