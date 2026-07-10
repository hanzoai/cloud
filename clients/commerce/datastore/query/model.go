package query

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	. "github.com/hanzoai/cloud/clients/commerce/datastore/utils"
)

type Kind interface {
	Kind() string
}

// Subset of model API needed to initialize a model correctly.
type Model interface {
	SetContext(ctx context.Context)
	SetEntity(entity interface{})
	SetKey(key interface{}) error
}

// Initialize model
func initModel(ctx context.Context, key iface.Key, value reflect.Value) {
	entity := value.Interface().(Kind)
	model := entity.(Model)
	model.SetContext(ctx)
	model.SetEntity(entity)
	model.SetKey(key)
}

// Fetches models and initializes them automatically. Dst must have type *[]*M,
// for some model type M.
func (q *Query) GetModels(dst interface{}) error {
	if q.dbQuery == nil {
		return fmt.Errorf("query: database not initialized")
	}

	keys, err := q.dbQuery.GetAll(q.ctx, dst)
	err = IgnoreFieldMismatch(err)

	if err != nil {
		return err
	}

	nkeys := len(keys)

	// Stop now if we found no models
	if nkeys == 0 {
		return nil
	}

	// Get slice
	slice := reflect.ValueOf(dst)
	if !isPtrSlice(slice) {
		return fmt.Errorf("Expected dst to be a pointer to a slice of models, got: %v", slice.Kind())
	}

	// De-pointer
	for slice.Kind() == reflect.Ptr {
		slice = reflect.Indirect(slice)
	}

	// Only a pointer to an entity will match the Entity interface
	if !isSliceOfPtr(slice) {
		return fmt.Errorf("Expected dst to be a pointer to a slice of models, got: %v", slice.Kind())
	}

	// Initialize all models in parallel
	var wg sync.WaitGroup

	for i := 0; i < nkeys; i++ {
		wg.Add(1)
		// Convert db.Key to iface.Key
		ifaceKey := &ifaceKeyWrapper{keys[i]}
		go func(ctx context.Context, key iface.Key, value reflect.Value) {
			initModel(ctx, key, value)
			wg.Done()
		}(q.ctx, ifaceKey, slice.Index(i))
	}

	// Wait to finish
	wg.Wait()

	return nil
}

// ifaceKeyWrapper wraps db.Key to implement iface.Key
type ifaceKeyWrapper struct {
	key interface{}
}

func (w *ifaceKeyWrapper) AppID() string {
	return "hanzo"
}

func (w *ifaceKeyWrapper) Encode() string {
	if k, ok := w.key.(interface{ Encode() string }); ok {
		return k.Encode()
	}
	return ""
}

func (w *ifaceKeyWrapper) Equal(o iface.Key) bool {
	if o == nil {
		return false
	}
	return w.Kind() == o.Kind() && w.Encode() == o.Encode()
}

func (w *ifaceKeyWrapper) GobDecode(buf []byte) error {
	return nil
}

func (w *ifaceKeyWrapper) GobEncode() ([]byte, error) {
	return nil, nil
}

func (w *ifaceKeyWrapper) Incomplete() bool {
	if k, ok := w.key.(interface{ Incomplete() bool }); ok {
		return k.Incomplete()
	}
	return false
}

func (w *ifaceKeyWrapper) IntID() int64 {
	if k, ok := w.key.(interface{ IntID() int64 }); ok {
		return k.IntID()
	}
	return 0
}

func (w *ifaceKeyWrapper) Kind() string {
	if k, ok := w.key.(interface{ Kind() string }); ok {
		return k.Kind()
	}
	return ""
}

func (w *ifaceKeyWrapper) MarshalJSON() ([]byte, error) {
	return []byte(`"` + w.Encode() + `"`), nil
}

func (w *ifaceKeyWrapper) Namespace() string {
	if k, ok := w.key.(interface{ Namespace() string }); ok {
		return k.Namespace()
	}
	return ""
}

func (w *ifaceKeyWrapper) Parent() iface.Key {
	if k, ok := w.key.(interface{ Parent() interface{} }); ok {
		p := k.Parent()
		if p == nil {
			return nil
		}
		return &ifaceKeyWrapper{p}
	}
	return nil
}

func (w *ifaceKeyWrapper) String() string {
	return w.Encode()
}

func (w *ifaceKeyWrapper) StringID() string {
	if k, ok := w.key.(interface{ StringID() string }); ok {
		return k.StringID()
	}
	return ""
}

func (w *ifaceKeyWrapper) UnmarshalJSON(buf []byte) error {
	return nil
}
