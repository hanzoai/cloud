package mixin

// DatastoreAdapter bridges commerce's *datastore.Datastore to orm.DB,
// enabling orm.Model[T] entities to operate on top of commerce's existing
// datastore infrastructure.
//
// Key adapters convert between iface.Key (commerce) and orm.Key (ORM).
// Query adapters convert between iface.Query and orm.Query.

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/orm"
)

// DatastoreAdapter wraps *datastore.Datastore to implement orm.DB.
type DatastoreAdapter struct {
	ds *datastore.Datastore
}

// NewDatastoreAdapter creates a new adapter wrapping a commerce datastore.
func NewDatastoreAdapter(ds *datastore.Datastore) *DatastoreAdapter {
	return &DatastoreAdapter{ds: ds}
}

// Datastore returns the underlying commerce datastore.
func (a *DatastoreAdapter) Datastore() *datastore.Datastore {
	return a.ds
}

// --- orm.DB implementation ---

func (a *DatastoreAdapter) Get(ctx context.Context, k orm.Key, dst interface{}) error {
	dsKey := ormKeyToDS(k)
	return a.ds.Get(dsKey, dst)
}

// GetForUpdate is required by orm.DB. DatastoreAdapter is a legacy path
// backed by hanzoai/datastore-go and does not participate in orm-managed
// transactions — GetForUpdate outside a tx is an error per the interface
// contract. The legacy commerce code never takes out row locks, so this
// sentinel is the correct forward posture; new code uses commerce/store.
func (a *DatastoreAdapter) GetForUpdate(_ context.Context, _ orm.Key, _ interface{}) error {
	return errors.New("commerce: GetForUpdate is not supported on the legacy datastore adapter — use commerce/store for row-locked reads")
}

// RunInTransactionWith is required by orm.DB. The legacy datastore backs a
// single-writer MongoDB-style store where transaction retry semantics are
// degenerate — opts.MaxAttempts is ignored and the callback runs once
// against the non-tx adapter. Callers that need real retry must use
// commerce/store (base-backed, SQLite/Postgres-aware).
func (a *DatastoreAdapter) RunInTransactionWith(ctx context.Context, _ *orm.TxOptions, fn func(tx orm.DB) error) error {
	return a.RunInTransaction(ctx, fn)
}

func (a *DatastoreAdapter) Put(ctx context.Context, k orm.Key, src interface{}) (orm.Key, error) {
	dsKey := ormKeyToDS(k)
	resultKey, err := a.ds.Put(dsKey, src)
	if err != nil {
		return nil, err
	}
	return dsKeyToOrm(resultKey), nil
}

func (a *DatastoreAdapter) Delete(ctx context.Context, k orm.Key) error {
	dsKey := ormKeyToDS(k)
	return a.ds.Delete(dsKey)
}

func (a *DatastoreAdapter) Query(kind string) orm.Query {
	dsQuery := a.ds.Query(kind)
	return &queryAdapter{q: dsQuery}
}

func (a *DatastoreAdapter) NewKey(kind, stringID string, intID int64, parent orm.Key) orm.Key {
	var dsParent iface.Key
	if parent != nil {
		dsParent = ormKeyToDS(parent)
	}
	dsKey := a.ds.NewKey(kind, stringID, intID, dsParent)
	return dsKeyToOrm(dsKey)
}

func (a *DatastoreAdapter) NewIncompleteKey(kind string, parent orm.Key) orm.Key {
	var dsParent iface.Key
	if parent != nil {
		dsParent = ormKeyToDS(parent)
	}
	dsKey := a.ds.NewIncompleteKey(kind, dsParent)
	return dsKeyToOrm(dsKey)
}

func (a *DatastoreAdapter) AllocateIDs(kind string, parent orm.Key, n int) ([]orm.Key, error) {
	var dsParent iface.Key
	if parent != nil {
		dsParent = ormKeyToDS(parent)
	}
	low, _ := a.ds.AllocateIDs(kind, dsParent, n)
	keys := make([]orm.Key, n)
	for i := 0; i < n; i++ {
		dsKey := a.ds.NewKey(kind, "", low+int64(i), dsParent)
		keys[i] = dsKeyToOrm(dsKey)
	}
	return keys, nil
}

func (a *DatastoreAdapter) RunInTransaction(ctx context.Context, fn func(tx orm.DB) error) error {
	return a.ds.RunInTransaction(func(txDS *datastore.Datastore) error {
		txAdapter := &DatastoreAdapter{ds: txDS}
		return fn(txAdapter)
	}, nil)
}

func (a *DatastoreAdapter) Close() error {
	return nil
}

// --- Key adapters ---

// ormKeyWrapper wraps iface.Key to implement orm.Key.
type ormKeyWrapper struct {
	dsKey iface.Key
}

func (w *ormKeyWrapper) Kind() string      { return w.dsKey.Kind() }
func (w *ormKeyWrapper) StringID() string  { return w.dsKey.StringID() }
func (w *ormKeyWrapper) IntID() int64      { return w.dsKey.IntID() }
func (w *ormKeyWrapper) Namespace() string { return w.dsKey.Namespace() }
func (w *ormKeyWrapper) Encode() string    { return w.dsKey.Encode() }

func (w *ormKeyWrapper) Parent() orm.Key {
	p := w.dsKey.Parent()
	if p == nil {
		return nil
	}
	return &ormKeyWrapper{dsKey: p}
}

// Unwrap returns the underlying iface.Key.
func (w *ormKeyWrapper) Unwrap() iface.Key {
	return w.dsKey
}

// dsKeyToOrm converts an iface.Key to orm.Key.
func dsKeyToOrm(k iface.Key) orm.Key {
	if k == nil {
		return nil
	}
	return &ormKeyWrapper{dsKey: k}
}

// ormKeyToDS converts an orm.Key back to iface.Key.
func ormKeyToDS(k orm.Key) iface.Key {
	if k == nil {
		return nil
	}
	// Fast path: unwrap if it's our wrapper
	if w, ok := k.(*ormKeyWrapper); ok {
		return w.dsKey
	}
	// Slow path: create a new DatastoreKey from the orm.Key
	var parent *key.DatastoreKey
	if p := k.Parent(); p != nil {
		parent = key.ToDatastoreKey(ormKeyToDS(p))
	}
	return key.NewKey(context.Background(), k.Kind(), k.StringID(), k.IntID(), parent)
}

// OrmKeyToDS is the exported version for use in bridge code.
func OrmKeyToDS(k orm.Key) iface.Key {
	return ormKeyToDS(k)
}

// DSKeyToOrm is the exported version for use in bridge code.
func DSKeyToOrm(k iface.Key) orm.Key {
	return dsKeyToOrm(k)
}

// --- Query adapter ---

// queryAdapter wraps iface.Query to implement orm.Query.
type queryAdapter struct {
	q iface.Query
}

func (qa *queryAdapter) Filter(filterStr string, value interface{}) orm.Query {
	return &queryAdapter{q: qa.q.Filter(filterStr, value)}
}

func (qa *queryAdapter) Order(fieldPath string) orm.Query {
	return &queryAdapter{q: qa.q.Order(fieldPath)}
}

func (qa *queryAdapter) Limit(limit int) orm.Query {
	return &queryAdapter{q: qa.q.Limit(limit)}
}

func (qa *queryAdapter) Offset(offset int) orm.Query {
	return &queryAdapter{q: qa.q.Offset(offset)}
}

func (qa *queryAdapter) Ancestor(ancestor orm.Key) orm.Query {
	return &queryAdapter{q: qa.q.Ancestor(ormKeyToDS(ancestor))}
}

func (qa *queryAdapter) KeysOnly() orm.Query {
	return &queryAdapter{q: qa.q.KeysOnly()}
}

func (qa *queryAdapter) GetAll(ctx context.Context, dst interface{}) ([]orm.Key, error) {
	dsKeys, err := qa.q.GetAll(dst)
	if err != nil {
		return nil, err
	}
	ormKeys := make([]orm.Key, len(dsKeys))
	for i, k := range dsKeys {
		ormKeys[i] = dsKeyToOrm(k)
	}
	return ormKeys, nil
}

func (qa *queryAdapter) First(dst interface{}) (orm.Key, bool, error) {
	dsKey, ok, err := qa.q.First(dst)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return dsKeyToOrm(dsKey), true, nil
}

func (qa *queryAdapter) Count(ctx context.Context) (int, error) {
	return qa.q.Count()
}

func (qa *queryAdapter) ById(id string, dst interface{}) (orm.Key, bool, error) {
	dsKey, ok, err := qa.q.ById(id, dst)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return dsKeyToOrm(dsKey), true, nil
}

func (qa *queryAdapter) IdExists(id string) (orm.Key, bool, error) {
	dsKey, ok, err := qa.q.IdExists(id)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return dsKeyToOrm(dsKey), true, nil
}

func (qa *queryAdapter) KeyExists(k orm.Key) (bool, error) {
	return qa.q.KeyExists(ormKeyToDS(k))
}

// Ensure DatastoreAdapter implements orm.DB at compile time.
var _ orm.DB = (*DatastoreAdapter)(nil)

// Ensure queryAdapter implements orm.Query at compile time.
var _ orm.Query = (*queryAdapter)(nil)

// Ensure ormKeyWrapper implements orm.Key at compile time.
var _ orm.Key = (*ormKeyWrapper)(nil)

// String representation for debugging.
func (a *DatastoreAdapter) String() string {
	return fmt.Sprintf("DatastoreAdapter{ns: %s}", a.ds.GetNamespace())
}
