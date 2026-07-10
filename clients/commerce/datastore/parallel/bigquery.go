package parallel

import (
	"context"
	"reflect"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bigquery"
)

func NewBigQuery(name string, fn interface{}) *ParallelFn {
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
	p.createBigQueryDelayFn(p.Name)

	parallelFns[p.Name] = p

	return p
}

type BigQueryRow struct {
	Row       bigquery.Row
	ProjectId string
	DataSetId string
	TableId   string
}

// Creates a new parallel datastore worker task, which will operate on a single
// entity of a given kind at a time (but all of them eventually, in parallel).
func (fn *ParallelFn) createBigQueryDelayFn(name string) {
	fn.DelayFn = delay.Func("parallel-bigquery-fn-"+name, func(ctx context.Context, namespace string, offset int, batchSize int, args ...interface{}) {
		// Explicitly switch namespace
		nsCtx := ctx
		if namespace != "" {
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

		client, err := bigquery.NewClient(ctx)
		if err != nil {
			log.Error("Could not create big query client: %v", err, ctx)
			return
		}

		rows := make([]BigQueryRow, 0, 0)

		// Loop over entities passing them into workerFunc one at a time
		for {
			entity := newEntity(db, fn.EntityType)
			key, err := t.Next(entity)

			// Done iterating
			if err == datastore.Done || key == nil {
				break
			}

			// Ignore field mismatch errors
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
			in := make([]reflect.Value, numArgs+3, numArgs+3)
			in[0] = reflect.ValueOf(db)
			in[1] = reflect.ValueOf(entity)
			in[2] = reflect.ValueOf(&rows)

			// Append variadic args
			for i := 0; i < numArgs; i++ {
				in[i+3] = reflect.ValueOf(args[i])
			}

			// Run our worker func with this entity
			fn.Value.Call(in)
		}

		binnedRows := make(map[string][]BigQueryRow)

		for _, row := range rows {
			key := row.ProjectId + "_" + row.DataSetId + "_" + row.TableId
			if _, ok := binnedRows[key]; !ok {
				binnedRows[key] = []BigQueryRow{row}
			} else {
				binnedRows[key] = append(binnedRows[key], row)
			}
		}

		for _, rows := range binnedRows {
			insertRows := make([]bigquery.Row, len(rows))
			for i, row := range rows {
				insertRows[i] = row.Row
			}
			row := rows[0]
			err = client.InsertRows(row.ProjectId, row.DataSetId, row.TableId, insertRows)
			if err != nil {
				log.Panic("Could not insert into bigquery, attempting to retry: %v", err, ctx)
			}
		}
	})
}
