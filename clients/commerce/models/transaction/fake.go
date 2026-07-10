package transaction

import (
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"math/rand"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore) *Transaction {
	t := New(db)
	t.DestinationId = fake.Id()
	t.DestinationKind = "user"
	t.SourceId = fake.Id()
	t.Test = true
	t.Type = Deposit
	t.Amount = currency.Cents(rand.Intn(10000))
	t.Currency = currency.Fake()
	return t
}
