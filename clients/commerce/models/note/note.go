package note

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Note]("note") }

type Note struct {
	mixin.Model[Note]

	Enabled bool `json:"enabled" orm:"default:true"`

	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

// New creates a new Note wired to the given datastore.
func New(db *datastore.Datastore) *Note {
	n := new(Note)
	n.Init(db)
	return n
}

// Query returns a datastore query for notes.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("note")
}
