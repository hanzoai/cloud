package oauthtoken

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
)

func (t *Token) Load(properties []datastore.Property) error {
	// Ensure we're initialized
	t.Defaults()

	// Load supported properties
	err := datastore.LoadStruct(t, properties)
	if err != nil {
		return err
	}

	return err
}

func (t *Token) Save() ([]datastore.Property, error) {
	// Save properties
	return datastore.SaveStruct(t)
}
