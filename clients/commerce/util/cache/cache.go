package cache

import (
	"reflect"
	"time"
)

type Memoized func(args ...interface{}) interface{}

func call(fn interface{}, args ...interface{}) interface{} {
	// Construct args for call
	reflectArgs := make([]reflect.Value, len(args))
	for i, arg := range args {
		reflectArgs[i] = reflect.ValueOf(arg)
	}

	// Get reflected func
	fnv := reflect.ValueOf(fn)

	// Call function with reflected args
	ret := fnv.Call(reflectArgs)
	if len(ret) > 0 {
		return ret[0].Interface()
	} else {
		return nil
	}
}

func Once(fn interface{}) Memoized {
	var cached interface{}

	return func(args ...interface{}) interface{} {
		if cached == nil {
			cached = call(fn, args...)
		}

		return cached
	}
}

// Cache result of fn, optionally expiring result.
func Memoize(fn interface{}, args ...interface{}) Memoized {
	seconds := int64(0)

	// Takes one extra optional argument, a timeout in seconds
	if len(args) > 0 {
		seconds = int64(args[0].(int))
	}

	// Get next expiration time
	duration := time.Duration(seconds * 1000 * 1000 * 1000) // time.Duration expects nanoseconds
	expires := time.Now()
	expires.Add(duration)

	var cached interface{}

	return func(args ...interface{}) interface{} {
		now := time.Now()

		// If this is the first time being called or expiration hit, cache result
		if cached == nil || (seconds > 0 && now.After(expires)) {
			cached = call(fn, args...)

			// Reset expiration timeout
			expires = now.Add(duration)
		}

		return cached
	}
}
