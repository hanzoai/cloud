package organization

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/app"
	"github.com/hanzoai/cloud/clients/commerce/models/namespace"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

const (
	DefaultAppName   = "Hanzo App"
	DefaultStoreName = "Default"
)

// Hooks
func (o *Organization) BeforeCreate() error {
	o.Fees.Id = o.Id()
	if o.SecretKey == nil {
		o.SecretKey = []byte(rand.SecretKey())
	}
	// Generate Tokens
	o.AddDefaultTokens()

	return nil
}

func (o *Organization) AfterCreate() error {
	// Save namespace so we can decode keys for this organization later
	db := datastore.New(o.Context())
	ns := namespace.New(db)
	err := ns.GetOrCreate("Name=", o.Name)

	if err != nil {
		log.Warn("Failed to put namespace: %v", err)
	}

	ns.Name = o.Name
	ns.IntId = o.Key().IntID()
	ns.MustUpdate()

	nsCtx := o.Namespaced(o.Context())
	nsDb := datastore.New(nsCtx)

	ap := app.New(nsDb)
	ap.Name = DefaultAppName
	ap.MustCreate()

	stor := store.New(nsDb)
	stor.Name = DefaultStoreName
	stor.Currency = o.Currency
	stor.MustCreate()

	o.DefaultApp = ap.Id()
	o.DefaultStore = stor.Id()

	o.MustUpdate()

	return nil
}
