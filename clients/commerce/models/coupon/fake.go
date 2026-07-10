package coupon

import (
	"math/rand"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore) *Coupon {
	c := New(db)
	c.Name = fake.Word()
	c.Type = Flat
	c.Code_ = fake.Word() + fake.Word()
	c.StartDate = time.Date(rand.Intn(25)+2000, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC).UTC()
	c.EndDate = c.StartDate.AddDate(0, 0, rand.Intn(30))
	c.Limit = rand.Intn(500) + 1
	c.Enabled = true
	c.Amount = rand.Intn(5000)
	c.Used = c.Limit - rand.Intn(c.Limit)

	return c
}
