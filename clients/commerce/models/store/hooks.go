package store

import (
	"github.com/hanzoai/cloud/clients/commerce/models/shippingrates"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrates"
)

// Hooks
func (s *Store) AfterCreate() error {
	trs := taxrates.New(s.Datastore())
	trs.StoreId = s.Id()
	trs.MustCreate()

	srs := shippingrates.New(s.Datastore())
	srs.StoreId = s.Id()
	srs.MustCreate()
	return nil
}
