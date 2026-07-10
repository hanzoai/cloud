package mixin

import "reflect"

type BeforeCreate interface {
	BeforeCreate() error
}

type BeforeDelete interface {
	BeforeDelete() error
}

type AfterCreate interface {
	AfterCreate() error
}

type AfterDelete interface {
	AfterDelete() error
}

// These last two interfaces are largely ignored -- we use helper below to have
// nicely typed update hooks in models.
type BeforeUpdate interface {
	BeforeUpdate(Entity) error
}

type AfterUpdate interface {
	AfterUpdate(Entity) error
}

// Try to get method off a model
func getHook(name string, entity Kind) (reflect.Method, bool) {
	typ := reflect.TypeOf(entity)
	return typ.MethodByName(name)
}

// Call method returned by getMethod
func callHook(entity Kind, method reflect.Method, previous interface{}) error {
	args := []reflect.Value{
		reflect.ValueOf(entity),
		reflect.ValueOf(previous),
	}

	ret := method.Func.Call(args)
	err, ok := ret[0].Interface().(error)
	if ok {
		return err
	}
	return nil
}
