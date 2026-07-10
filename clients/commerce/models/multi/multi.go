package multi

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
)

// Vals should be a slice of models
func multi(vals interface{}, fn func(mixin.Entity) error) error {
	// Vals must be a slice
	if reflect.TypeOf(vals).Kind() != reflect.Slice {
		return errors.New(fmt.Sprintf("Must be called with slice of entities, not: %v", vals))
	}

	var wg sync.WaitGroup
	slice := reflect.ValueOf(vals)
	n := slice.Len()
	errs := make(MultiError, n)
	errd := false

	// Loop over slice initializing entities
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			// Grab next entity off slice
			entity, ok := slice.Index(i).Interface().(mixin.Entity)
			if !ok {
				errd = true
				errs[i] = errors.New(fmt.Sprintf("Slice must contain entities, not %v", slice.Index(i).Interface()))
				return
			}

			// Run operation on entity
			if err := fn(entity); err != nil {
				errd = true
				errs[i] = err
			}
		}(i)
	}

	// Wait to finish
	wg.Wait()

	if errd {
		return errs
	} else {
		return nil
	}
}

func Get(db *datastore.Datastore, keys interface{}, vals interface{}) error {
	// Fetch underlying entities
	if err := db.GetMulti(keys, vals); err != nil {
		return err
	}

	keySlice := reflect.ValueOf(keys)
	valSlice := reflect.ValueOf(vals)
	nkeys := keySlice.Len()

	errs := make(MultiError, nkeys)
	errd := false

	var wg sync.WaitGroup

	// Loop over slice fetching entities
	for i := 0; i < nkeys; i++ {
		wg.Add(1)

		// Run method in gofunc
		go func(i int) {
			defer wg.Done()

			key := keySlice.Index(i).Interface()
			entity := valSlice.Index(i).Interface().(mixin.Entity)

			// Set key on model
			if err := entity.SetKey(key); err != nil {
				errd = true
				errs[i] = err
			}
			// Ensure model is initialized correctly
			entity.Init(db)
		}(i)
	}

	// Wait to finish
	wg.Wait()

	if errd {
		return errs
	} else {
		return nil
	}
}

func Put(vals interface{}) error {
	return multi(vals, func(entity mixin.Entity) error {
		return entity.Put()
	})
}

func Create(vals interface{}) error {
	return multi(vals, func(entity mixin.Entity) error {
		return entity.Create()
	})
}

func Delete(vals interface{}) error {
	return multi(vals, func(entity mixin.Entity) error {
		return entity.Delete()
	})
}

func Update(vals interface{}) error {
	return multi(vals, func(entity mixin.Entity) error {
		return entity.Update()
	})
}

func MustPut(vals interface{}) {
	if err := Put(vals); err != nil {
		panic(err)
	}
}

func MustCreate(vals interface{}) {
	if err := Create(vals); err != nil {
		panic(err)
	}
}

func MustUpdate(vals interface{}) {
	if err := Update(vals); err != nil {
		panic(err)
	}
}

func MustDelete(vals interface{}) {
	if err := Delete(vals); err != nil {
		panic(err)
	}
}
