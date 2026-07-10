package plan

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func Fake(db *datastore.Datastore) *Plan {
	p := New(db)
	p.Slug = fake.Slug()
	p.SKU = fake.SKU()
	p.Name = fake.Word()
	p.Id_ = fake.Id()
	p.Description = fake.Word()
	p.Price = currency.Cents(0).Fake()
	p.Currency = currency.USD
	p.Interval = Monthly
	return p
}
