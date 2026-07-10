package webhook

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore) *Webhook {
	s := New(db)
	s.Url = fake.Url()
	s.Live = false
	s.All = true
	s.Enabled = true
	return s
}
