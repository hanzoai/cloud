package migrations

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hanzoai/cloud/clients/commerce/models/order"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("add-batch-data-for-kanoa",
	func(c *gin.Context) []interface{} {
		c.Set("namespace", "kanoa")
		return NoArgs
	},
	func(db *ds.Datastore, ord *order.Order) {
		loc, _ := time.LoadLocation("America/Los_Angeles")
		batch1Time := time.Date(2016, 2, 28, 0, 0, 0, 0, loc)

		if _, ok := ord.Metadata["batch"]; ok {
			return
		}

		if ord.CreatedAt.Before(batch1Time) {
			ord.Metadata["batch"] = "1"
		} else {
			ord.Metadata["batch"] = "2"
		}
		ord.Put()
	},
)
