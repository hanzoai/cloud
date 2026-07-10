package subscriber

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore, userId string) *Subscriber {
	s := New(db)
	s.UserId = userId
	s.FormId = fake.RandSeq(10, []rune("abcdefghijklmnopqrstuvwxyz"))
	s.Email = strings.ToLower(fake.EmailAddress())
	s.Unsubscribed = false
	return s
}
