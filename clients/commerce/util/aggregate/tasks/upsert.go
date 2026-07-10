package tasks

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/delay"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/aggregate"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

var upsertAggregate = delay.Func("UpsertAggregate", func(ctx context.Context, namespace, name, typ string, t time.Time, f string, deltaValue int, deltaVectorValue []int64) {
	freq := aggregate.Frequency(f)

	// Create namespaced context using our nscontext helper
	nsctx := nscontext.WithNamespace(ctx, namespace)

	db := datastore.New(nsctx)
	err := db.RunInTransaction(func(db *datastore.Datastore) error {
		agg := aggregate.New(db)
		aggregate.Init(agg, name, t, freq)

		if err := agg.GetById(agg.Instance); err != nil {
			// insert aggregate
			agg.Value = int64(deltaValue)
			agg.VectorValue = deltaVectorValue
		} else {
			// update aggregate
			agg.Value += agg.Value

			if deltaVectorValue != nil {
				if agg.VectorValue == nil {
					agg.VectorValue = deltaVectorValue
				} else {
					for len(deltaVectorValue) > len(agg.VectorValue) {
						agg.VectorValue = append(agg.VectorValue, 0)
					}

					for i, v := range deltaVectorValue {
						agg.VectorValue[i] += v
					}
				}
			}
		}

		agg.Type = typ
		if err := agg.Put(); err != nil {
			return err
		}

		return nil
	}, nil)

	if err != nil {
		// Poor man's retry
		log.Error("UpsertAggregate error: %v", err, ctx)
		panic(err)
	}
})

func UpsertAggregate(ctx context.Context, namespace, name, typ string, t time.Time, f aggregate.Frequency, deltaValue int, deltaVectorValue []int64) {
	upsertAggregate.Call(ctx, namespace, name, typ, t, string(f), deltaValue, deltaVectorValue)
}
