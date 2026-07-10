package apipermission

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[ApiPermission]("apipermission") }

type ApiPermission struct {
	mixin.Model[ApiPermission]

	Name     string `json:"name"`
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

// New creates a new ApiPermission wired to the given datastore.
func New(db *datastore.Datastore) *ApiPermission {
	p := new(ApiPermission)
	p.Init(db)
	return p
}

// Query returns a datastore query for api permissions.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("apipermission")
}
