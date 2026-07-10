package migrations

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore/parallel"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/util/task"
)

type Row parallel.BigQueryRow

type SetupFn func(*gin.Context) []interface{}

var NoArgs = []interface{}{}

func NoSetup(c *gin.Context) []interface{} {
	return NoArgs
}

func New(name string, setupFn SetupFn, fns ...interface{}) *delay.Function {
	name = "migration-" + name

	tasks := make([]*parallel.ParallelFn, len(fns))
	for i, fn := range fns {
		tasks[i] = parallel.New(name+"-task-"+strconv.Itoa(i), fn)
	}

	return task.Func(name, func(c *gin.Context) {
		// Call setup fn
		args := setupFn(c)

		for i, _ := range fns {
			// Run task fn
			tasks[i].Run(c, 10, args...)
		}
	})
}

func NewBigQuery(name string, setupFn SetupFn, fns ...interface{}) *delay.Function {
	name = "migration-" + name

	tasks := make([]*parallel.ParallelFn, len(fns))
	for i, fn := range fns {
		tasks[i] = parallel.NewBigQuery(name+"-task-"+strconv.Itoa(i), fn)
	}

	return task.Func(name, func(c *gin.Context) {
		// Call setup fn
		args := setupFn(c)

		for i, _ := range fns {
			// Run task fn
			tasks[i].Run(c, 10, args...)
		}
	})
}
