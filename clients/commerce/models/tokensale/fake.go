package tokensale

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore) *TokenSale {
	ts := New(db)
	ts.Name = fake.ProductName()
	ts.TotalTokens = fake.Number(100000)

	return ts
}
