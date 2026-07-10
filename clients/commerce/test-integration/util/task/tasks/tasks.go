package tasks

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/task"
)

// SOON!
// task.Group("fixtures", func(){
// 	task.Register("products")
// 	task.Register("orders")
// })

var Foo = task.Func("foo", func(c *gin.Context) {
	// Use context.Background() for test context
	ctx := context.Background()
	_ = ctx // Context available if needed for future use

	// Test task - no actual memcache operations
	log.Info("Task foo executed", c)
})

var Baz = task.Func("baz", func(c *gin.Context) {
	// Use context.Background() for test context
	ctx := context.Background()
	_ = ctx // Context available if needed for future use

	// Test task - no actual memcache operations
	log.Info("Task baz executed", c)
})

var NestedBaz = task.Func("nested-baz", func(c *gin.Context) {
	ctx := context.Background()
	Baz.Call(ctx)
})
