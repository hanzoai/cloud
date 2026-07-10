package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/form"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("form-enabled",
	func(c *gin.Context) []interface{} {
		return NoArgs
	},
	func(db *ds.Datastore, f *form.Form) {
		f.EmailList.Enabled = true
		if err := f.Put(); err != nil {
			log.Error(err, db.Context)
		}
	},
)
