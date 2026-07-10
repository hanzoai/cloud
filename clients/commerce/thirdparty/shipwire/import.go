package shipwire

import (
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

func Import(db *datastore.Datastore, filename string) {
	for record := range json.Iterator(filename) {
		if config.IsDevelopment && record.Index > 25 {
			break // Only import first 25 in development
		}

	}
}
