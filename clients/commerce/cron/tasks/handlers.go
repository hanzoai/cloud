package tasks

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/cron/payout/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/cron/payout/partner"
	"github.com/hanzoai/cloud/clients/commerce/cron/payout/platform"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/task"
)

// Register tasks
func init() {
	task.New("payout-affiliate", func(c *gin.Context) {
		ctx := middleware.GetContext(c)
		affiliate.Payout(ctx)
	})

	task.New("payout-partner", func(c *gin.Context) {
		ctx := middleware.GetContext(c)
		partner.Payout(ctx)
	})

	task.New("payout-platform", func(c *gin.Context) {
		ctx := middleware.GetContext(c)
		platform.Payout(ctx)
	})
}
