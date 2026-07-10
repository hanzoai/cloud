package parallel

import (
	"context"
	"reflect"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
)

type ParallelFn struct {
	Kind       string
	Name       string
	EntityType reflect.Type
	Value      reflect.Value
	DelayFn    *delay.Function
}

var parallelFns = make(map[string]*ParallelFn)

func New(name string, fn interface{}) *ParallelFn {
	// Check type of worker func to ensure it matches required signature.
	typ := reflect.TypeOf(fn)

	// Ensure that fn is actually a func
	if typ.Kind() != reflect.Func {
		log.Panic("Function is required for second parameter")
	}

	// fn should be a function that takes at least two arguments
	argNum := typ.NumIn()
	if argNum < 2 {
		log.Panic("Function requires at least two arguments")
	}

	// Check fn's first argument
	if typ.In(0) != datastoreType {
		log.Panic("First argument must be datastore.Datastore: %v", typ)
	}

	// Get entity type & kind
	entityType := typ.In(1).Elem()
	entity := reflect.New(entityType).Interface().(mixin.Kind)
	kind := entity.Kind()

	// Create a new ParallelFn
	p := &ParallelFn{
		Name:       name,
		Kind:       kind,
		EntityType: entityType,
		Value:      reflect.ValueOf(fn),
	}

	// Create delay function
	p.createDelayFn(p.Name)

	parallelFns[p.Name] = p

	return p
}

// Creates a new parallel datastore worker task, which will operate on a single
// entity of a given kind at a time (but all of them eventually, in parallel).
func (fn *ParallelFn) createDelayFn(name string) {
	fn.DelayFn = delay.Func("parallel-fn-"+name, func(ctx context.Context, namespace string, offset int, batchSize int, args ...interface{}) {
		// Explicitly switch namespace
		nsCtx := ctx
		if namespace != "" {
			// In the new architecture, namespace is handled through context values
			nsCtx = context.WithValue(ctx, "namespace", namespace)
		}

		// Set timeout
		nsCtx, cancel := context.WithTimeout(nsCtx, time.Second*30)
		defer cancel()

		// Run query to get results for this batch of entities
		db := datastore.New(nsCtx)
		if namespace != "" {
			db.SetNamespace(namespace)
		}

		// Construct query
		q := db.Query(fn.Kind).Offset(offset).Limit(batchSize)

		// Run query
		t := q.Run()

		// Loop over entities passing them into workerFunc one at a time
		for {
			entity := newEntity(db, fn.EntityType)
			key, err := t.Next(entity)

			// Done iterating
			if err == datastore.Done || key == nil {
				break
			}

			// Skip field mismatch errors
			if err := datastore.IgnoreFieldMismatch(err); err != nil {
				log.Error("Failed to fetch next entity: %v", err, ctx)
				break
			}

			if err := entity.SetKey(key); err != nil {
				log.Error("Failed to set key: %v", err, ctx)
				break
			}

			// Build arguments for workerFunc
			numArgs := len(args)
			in := make([]reflect.Value, numArgs+2, numArgs+2)
			in[0] = reflect.ValueOf(db)
			in[1] = reflect.ValueOf(entity)

			// Append variadic args
			for i := 0; i < numArgs; i++ {
				in[i+2] = reflect.ValueOf(args[i])
			}

			// Run our worker func with this entity
			fn.Value.Call(in)
		}
	})
}

// Call underlying delay function
func (fn *ParallelFn) Call(ctx context.Context, args ...interface{}) {
	fn.DelayFn.Call(ctx, args...)
}

// Run fn in parallel across all entities
func (fn *ParallelFn) Run(c *gin.Context, batchSize int, args ...interface{}) error {
	// Limit results in test mode
	if c.MustGet("test").(bool) {
		batchSize = 1
	}

	ctx := c.Request.Context()
	if reqCtx := c.Value("context"); reqCtx != nil {
		if ctxVal, ok := reqCtx.(context.Context); ok {
			ctx = ctxVal
		}
	}

	namespaces := make([]string, 0)

	// Check if namespace is set explicitly
	v, ok := c.Get("namespace")
	if ok {
		namespace, ok := v.(string)
		if ok {
			namespaces = append(namespaces, namespace)
		}
	}

	// Use all namespaces
	if len(namespaces) == 0 {
		namespaces = datastore.GetNamespaces(ctx)
	}

	log.Debug("executing across namespaces: %v", namespaces)
	if len(namespaces) == 0 {
		args := append([]interface{}{fn.Name, "", batchSize}, args...)
		initNamespace.Call(ctx, args...)
	} else {
		// Iterate through namespaces and initialize workers to run in each
		for _, ns := range namespaces {
			args := append([]interface{}{fn.Name, ns, batchSize}, args...)
			initNamespace.Call(ctx, args...)
		}
	}

	return nil
}

// Start individual runs in a given namespace
var initNamespace = delay.Func("parallel-init", func(ctx context.Context, fnName string, namespace string, batchSize int, args ...interface{}) {
	// Set namespace explicitly
	nsCtx := ctx
	if namespace != "" {
		nsCtx = context.WithValue(ctx, "namespace", namespace)
	}

	db := datastore.New(nsCtx)
	if namespace != "" {
		db.SetNamespace(namespace)
	}

	// Get relevant ParallelFn
	fn := parallelFns[fnName]

	total, _ := db.Query(fn.Kind).Count()

	// Start all workers
	for offset := 0; offset < total; offset += batchSize {
		// Append variadic arguments after required args
		args := append([]interface{}{namespace, offset, batchSize}, args...)

		// Call delay.Function
		fn.DelayFn.Call(ctx, args...)
	}
})
