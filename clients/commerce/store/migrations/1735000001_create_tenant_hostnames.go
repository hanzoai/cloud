// Package migrations — commerce-owned base migrations.
//
// 1735000001_create_tenant_hostnames pulls hostnames out of the
// commerce_tenants.hostnames JSON column into a separate row-per-hostname
// collection so a UNIQUE index can enforce global claim ownership.
//
// Why a join table rather than a JSON-column-level index:
//   - SQLite's generated-column + UNIQUE INDEX over JSON arrays is expressible
//     (via `json_each`) but the syntax does not translate 1:1 to Postgres's
//     expression index, so keeping parity across backends via a first-class
//     relation table is simpler and race-safer across replicas.
//   - A row-per-hostname lets the unique constraint land at the SQL engine,
//     which is the only place that can survive concurrent transactions from
//     distinct commerce replicas. Application-level mutex checks race.
//
// This migration runs AFTER 1735000000_create_tenants so the relation field
// can reference the commerce_tenants collection by its canonical id.
package migrations

import (
	"fmt"

	"github.com/hanzoai/base/core"
)

func init() {
	core.AppMigrations.Register(func(app core.App) error {
		parent, err := app.FindCollectionByNameOrId("commerce_tenants")
		if err != nil {
			return fmt.Errorf("tenant_hostnames: find commerce_tenants: %w", err)
		}

		collection := core.NewBaseCollection("commerce_tenant_hostnames")

		// The canonical, normalized hostname. Normalization (lowercase,
		// trailing-dot stripped, port stripped, RFC 1123 validated) runs in
		// the store layer before Insert — this field trusts its caller and
		// stores bytes verbatim.
		collection.Fields.Add(&core.TextField{
			Name:     "hostname",
			Required: true,
			Max:      253, // DNS cap
			Pattern:  `^[a-z0-9][a-z0-9.\-]*[a-z0-9]$`,
		})

		// Owning tenant. CascadeDelete ensures that when a tenant row is
		// deleted the hostnames go with it; a dangling hostname could
		// otherwise be resolved by FindByHostname after the owner is gone.
		collection.Fields.Add(&core.RelationField{
			Name:          "tenant",
			Required:      true,
			CollectionId:  parent.Id,
			CascadeDelete: true,
			MaxSelect:     1,
		})

		collection.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})

		// UNIQUE on hostname is the merge-blocker fix for P8-C1. At the SQL
		// engine level, two inserts with the same hostname collide regardless
		// of which commerce replica issued them.
		collection.AddIndex("idx_commerce_tenant_hostnames_unique", true, "hostname", "")

		// Secondary non-unique index on tenant for fast cascade + admin list.
		collection.AddIndex("idx_commerce_tenant_hostnames_by_tenant", false, "tenant", "")

		if err := app.Save(collection); err != nil {
			return fmt.Errorf("tenant_hostnames: create collection: %w", err)
		}
		return nil
	}, func(app core.App) error {
		c, err := app.FindCollectionByNameOrId("commerce_tenant_hostnames")
		if err != nil {
			return nil // already absent — idempotent down
		}
		return app.Delete(c)
	})
}
