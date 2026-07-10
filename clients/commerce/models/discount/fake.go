package discount

import (
	"math/rand"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/scope"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/target"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore) *Discount {
	d := New(db)
	d.Name = fake.Word()

	d.StartDate = time.Date(rand.Intn(25)+2000, time.Month(rand.Intn(12)+1), rand.Intn(25)+1, 0, 0, 0, 0, time.UTC)
	d.EndDate = d.StartDate.AddDate(0, 0, rand.Intn(30))
	d.Type = FreeShipping

	d.Scope = Scope{Type: scope.Organization}
	d.Target = Target{Type: target.Cart}

	return d
}
