package coupon

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore) *Redemption {
	r := New(db)
	r.Code = fake.Word()
	return r
}
