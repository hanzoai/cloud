package return_

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore, userId string) *Return {
	r := New(db)
	r.Fulfillment.Type = "shipwire"
	r.Fulfillment.Status = fulfillment.Pending
	r.Fulfillment.ExternalId = fake.Id()
	return r
}
