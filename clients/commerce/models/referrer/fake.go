package referrer

import (
	"math/rand"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore, userId string) *Referrer {
	ref := New(db)
	ref.Code = fake.Word()
	ref.UserId = userId
	ref.FirstReferredAt = time.Date(rand.Intn(15)+2000, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC)
	return ref
}
