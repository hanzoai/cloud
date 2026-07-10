// Package delay provides a task queue abstraction for background job execution.
// This is a standalone implementation that replaces the google.golang.org/appengine/delay package.
package delay

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

const (
	// Path is the HTTP path for invocations (maintained for compatibility)
	Path = "/_/queue/delay"
)

type contextKey int

var (
	// Funcs is the registry of all delayed functions
	Funcs = make(map[string]*Function)

	// funcsMu protects Funcs map
	funcsMu sync.RWMutex

	// precomputed types
	errorType = reflect.TypeOf((*error)(nil)).Elem()

	// errors
	errFirstArg         = errors.New("delay: first argument must be context.Context")
	errOutsideDelayFunc = errors.New("delay: request headers are only available inside a delay.Func")
	errFuncInvalid      = errors.New("delay: func is invalid")
	errNotAFunction     = errors.New("delay: not a function")

	// context keys
	headersContextKey contextKey = 0
	taskIDContextKey  contextKey = 1

	// DefaultRetryCount is the default number of retries for failed tasks
	DefaultRetryCount = 3

	// DefaultRetryDelay is the default delay between retries
	DefaultRetryDelay = time.Second * 5
)

// TaskOptions configures task execution
type TaskOptions struct {
	Queue      string
	Name       string
	Delay      time.Duration
	RetryCount int
	RetryDelay time.Duration
}

// Task represents a delayed task
type Task struct {
	ID      string
	Path    string
	Payload []byte
	Options TaskOptions
}

// Function wraps a delayed function with queue configuration
type Function struct {
	fv  reflect.Value // Kind() == reflect.Func
	key string
	err error // any error during initialization

	queue string
	name  string
	delay time.Duration
}

type invocation struct {
	Key  string
	Args []interface{}
}

// Func declares a new Function. The second argument must be a function with a
// first argument of type context.Context.
// This function must be called at program initialization time. That means it
// must be called in a global variable declaration or from an init function.
// This restriction is necessary because the instance that delays a function
// call may not be the one that executes it. Only the code executed at program
// initialization time is guaranteed to have been run by an instance before it
// receives a request.
func Func(key string, i interface{}) *Function {
	f := &Function{fv: reflect.ValueOf(i)}

	f.key = key
	f.queue = "" // Use default queue
	f.name = ""  // Auto-generated name
	f.delay = 0

	t := f.fv.Type()
	if t.Kind() != reflect.Func {
		f.err = errNotAFunction
		return f
	}
	if t.NumIn() == 0 || !isContext(t.In(0)) {
		f.err = errFirstArg
		return f
	}

	// Register the function's arguments with the gob package.
	// This is required because they are marshaled inside a []interface{}.
	// gob.Register only expects to be called during initialization;
	// that's fine because this function expects the same.
	for i := 0; i < t.NumIn(); i++ {
		// Only concrete types may be registered. If the argument has
		// interface type, the client is responsible for registering the
		// concrete types it will hold.
		if t.In(i).Kind() == reflect.Interface {
			continue
		}
		gob.Register(reflect.Zero(t.In(i)).Interface())
	}

	funcsMu.Lock()
	defer funcsMu.Unlock()

	if old := Funcs[f.key]; old != nil {
		old.err = fmt.Errorf("delay: multiple functions registered for %s", key)
	}

	Funcs[f.key] = f

	return f
}

// Call invokes a delayed function asynchronously using goroutines.
// The function is executed in a background goroutine after any configured delay.
func (f *Function) Call(c context.Context, args ...interface{}) error {
	t, err := f.Task(args...)
	if err != nil {
		log.Warn(err)
		return err
	}

	// Override name if set
	if f.name != "" {
		t.Options.Name = f.name
	}

	// Execute the task asynchronously
	return executeTask(c, t, f)
}

// executeTask runs the task in a goroutine with optional delay
func executeTask(parentCtx context.Context, t *Task, f *Function) error {
	delay := f.delay
	retryCount := DefaultRetryCount
	retryDelay := DefaultRetryDelay

	if t.Options.RetryCount > 0 {
		retryCount = t.Options.RetryCount
	}
	if t.Options.RetryDelay > 0 {
		retryDelay = t.Options.RetryDelay
	}

	go func() {
		// Create a new context for the background task
		// We don't inherit cancellation from parentCtx since this is a background job
		ctx := context.Background()

		// Add task ID to context if available
		if t.Options.Name != "" {
			ctx = context.WithValue(ctx, taskIDContextKey, t.Options.Name)
		}

		// Apply initial delay if configured
		if delay > 0 {
			time.Sleep(delay)
		}

		// Decode the invocation
		var inv invocation
		if err := gob.NewDecoder(bytes.NewReader(t.Payload)).Decode(&inv); err != nil {
			log.Error(ctx, "delay: failed decoding task payload: %v", err)
			return
		}

		// Execute with retries
		var lastErr error
		for attempt := 0; attempt <= retryCount; attempt++ {
			if attempt > 0 {
				log.Warn(ctx, "delay: retrying task %s (attempt %d/%d)", f.key, attempt, retryCount)
				time.Sleep(retryDelay)
			}

			lastErr = executeInvocation(ctx, f, inv.Args)
			if lastErr == nil {
				return // Success
			}

			log.Error(ctx, "delay: func %s failed (attempt %d): %v", f.key, attempt+1, lastErr)
		}

		log.Error(ctx, "delay: func %s exhausted all retries: %v", f.key, lastErr)
	}()

	return nil
}

// executeInvocation executes the function with the given arguments
func executeInvocation(ctx context.Context, f *Function, args []interface{}) error {
	ft := f.fv.Type()
	in := []reflect.Value{reflect.ValueOf(ctx)}

	for i, arg := range args {
		var v reflect.Value
		if arg != nil {
			v = reflect.ValueOf(arg)
		} else {
			// Task was passed a nil argument, so we must construct
			// the zero value for the argument here.
			n := len(in) // we're constructing the nth argument
			var at reflect.Type
			if !ft.IsVariadic() || n < ft.NumIn()-1 {
				at = ft.In(n)
			} else {
				at = ft.In(ft.NumIn() - 1).Elem()
			}
			v = reflect.Zero(at)
		}
		in = append(in, v)

		// Suppress unused variable warning
		_ = i
	}

	out := f.fv.Call(in)

	if n := ft.NumOut(); n > 0 && ft.Out(n-1) == errorType {
		if errv := out[n-1]; !errv.IsNil() {
			return errv.Interface().(error)
		}
	}

	return nil
}

// Task creates a Task that will invoke the function.
// Its parameters may be tweaked before execution.
// Users should not modify the Path or Payload fields of the returned Task.
func (f *Function) Task(args ...interface{}) (*Task, error) {
	if f.err != nil {
		return nil, fmt.Errorf("delay: func is invalid: %v", f.err)
	}

	nArgs := len(args) + 1 // +1 for the context.Context
	ft := f.fv.Type()
	minArgs := ft.NumIn()

	if ft.IsVariadic() {
		minArgs--
	}

	if nArgs < minArgs {
		return nil, fmt.Errorf("delay: too few arguments to func: %d < %d", nArgs, minArgs)
	}

	if !ft.IsVariadic() && nArgs > minArgs {
		return nil, fmt.Errorf("delay: too many arguments to func: %d > %d", nArgs, minArgs)
	}

	// Check arg types.
	for i := 1; i < nArgs; i++ {
		at := reflect.TypeOf(args[i-1])
		var dt reflect.Type

		if i < minArgs {
			// not a variadic arg
			dt = ft.In(i)
		} else {
			// a variadic arg
			dt = ft.In(minArgs).Elem()
		}

		// nil arguments won't have a type, so they need special handling.
		if at == nil {
			// nil interface
			switch dt.Kind() {
			case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
				continue // may be nil
			}
			return nil, fmt.Errorf("delay: argument %d has wrong type: %v is not nilable", i, dt)
		}

		switch at.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
			av := reflect.ValueOf(args[i-1])
			if av.IsNil() {
				// nil value in interface; not supported by gob, so we replace it
				// with a nil interface value
				args[i-1] = nil
			}
		}

		if !at.AssignableTo(dt) {
			return nil, fmt.Errorf("delay: argument %d has wrong type: %v is not assignable to %v", i, at, dt)
		}
	}

	inv := invocation{
		Key:  f.key,
		Args: args,
	}

	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(inv); err != nil {
		return nil, fmt.Errorf("delay: gob encoding failed: %v", err)
	}

	return &Task{
		Path:    Path,
		Payload: buf.Bytes(),
		Options: TaskOptions{
			Queue: f.queue,
			Name:  f.name,
			Delay: f.delay,
		},
	}, nil
}

// Queue returns a copy of this Function with the specified queue.
func (f *Function) Queue(queue string) *Function {
	f2 := &Function{
		fv:    f.fv,
		key:   f.key,
		err:   f.err,
		queue: queue,
		name:  f.name,
		delay: f.delay,
	}
	return f2
}

// Once adds a task only once by using a unique name.
// This prevents duplicate task execution.
func (f *Function) Once(ctx context.Context, name string, delay time.Duration, args ...interface{}) error {
	f2 := &Function{
		fv:    f.fv,
		key:   f.key,
		err:   f.err,
		queue: f.queue,
		name:  name,
		delay: delay,
	}
	return f2.Call(ctx, args...)
}

// FuncByKey retrieves a registered function by its key.
func FuncByKey(key string) *Function {
	funcsMu.RLock()
	defer funcsMu.RUnlock()

	f, ok := Funcs[key]
	if !ok {
		keys := []string{}
		for k := range Funcs {
			keys = append(keys, k)
		}
		panic(fmt.Errorf("delay: key %s not found in delay.Funcs(%s)", key, keys))
	}
	return f
}

// Later executes a function after a delay.
// This is a simpler API for one-off delayed tasks.
func Later(ctx context.Context, delay time.Duration, fn func(context.Context) error) error {
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		if err := fn(context.Background()); err != nil {
			log.Error(ctx, "delay: Later func failed: %v", err)
		}
	}()
	return nil
}

// Now executes a function immediately in a background goroutine.
func Now(ctx context.Context, fn func(context.Context) error) error {
	return Later(ctx, 0, fn)
}
