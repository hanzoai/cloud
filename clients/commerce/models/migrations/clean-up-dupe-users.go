package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

var _ = New("clean-up-dupe-users", func(c *gin.Context) []interface{} {
	db := datastore.New(c)
	db.SetNamespace("kanoa")

	keys, err := user.Query(db).Filter("Deleted=", true).KeysOnly().Limit(500).GetAll(nil)
	if err != nil {
		log.Panic("Failed to get keys for deleted users: %v", err, c)
	}

	for len(keys) > 0 {
		log.Debug("Deleting %s keys", len(keys), c)

		if err := db.DeleteMulti(keys); err != nil {
			log.Warn("Failed to delete keys: %v", err, c)
		}

		keys, err = user.Query(db).Filter("Deleted=", true).KeysOnly().Limit(500).GetAll(nil)
		if err != nil {
			log.Panic("Failed to get keys for deleted users: %v", err, c)
		}
	}

	return NoArgs
})
