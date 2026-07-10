package commission

import (
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake() Commission {
	var c Commission
	c.Flat = currency.Cents(0).Fake()
	c.Minimum = currency.Cents(0).Fake()
	c.Percent = fake.Percent
	return c
}
