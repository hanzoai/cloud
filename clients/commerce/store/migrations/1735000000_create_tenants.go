// Package migrations — commerce-owned base migrations.
//
// Each file registers an up/down migration via core.AppMigrations. They run
// after base's SystemMigrations and before the app starts serving, on every
// store.New() call. Naming convention is `<unix_ts>_<snake_name>.go` to give
// a deterministic apply order.
package migrations

import (
	"fmt"

	"github.com/hanzoai/base/core"
)

// 1735000000 == 2024-12-24 UTC — picked as the epoch for the commerce/store
// seam. Later commerce migrations append with later timestamps.
func init() {
	core.AppMigrations.Register(func(app core.App) error {
		collection := core.NewBaseCollection("commerce_tenants")

		// Tenant identity. `name` is the stable tenant key (also the Hanzo
		// IAM org owner claim — JWT `owner` matches this column).
		collection.Fields.Add(&core.TextField{
			Name:     "name",
			Required: true,
			Max:      100,
			Pattern:  `^[a-z0-9][a-z0-9-]{1,63}$`,
		})

		// Hostnames is a JSON array; handler-side code validates each entry
		// is a bare hostname (no scheme, no path, no port). Exact-match
		// lookup runs over this array — suffix-match spoofing is rejected.
		collection.Fields.Add(&core.JSONField{
			Name:     "hostnames",
			Required: true,
			MaxSize:  64 * 1024, // 64 KB — ~1000 hostnames worth of headroom
		})

		// Branding — SPA-visible. 64 KB is overkill but a JSON blob budget
		// is cheap and blocks a tenant admin from uploading an embedded
		// image.
		collection.Fields.Add(&core.JSONField{
			Name:    "brand",
			MaxSize: 64 * 1024,
		})

		// IAM — OIDC issuer + public client id only. No secret fields in
		// this JSON; handler validates shape on write.
		collection.Fields.Add(&core.JSONField{
			Name:    "iam",
			MaxSize: 16 * 1024,
		})

		// IDV — opaque to commerce.
		collection.Fields.Add(&core.JSONField{
			Name:    "idv",
			MaxSize: 16 * 1024,
		})

		// Providers — list of {name, enabled, kms_path}. NO credentials in
		// this column. Secrets flow to KMS out-of-band (commerce/{tenant}/
		// {provider}/{field}) and this table only stores the reference.
		collection.Fields.Add(&core.JSONField{
			Name:    "providers",
			MaxSize: 64 * 1024,
		})

		// BD endpoint — where checkout API proxies deposit intents. Tenant
		// supplies its own per-environment back-end URL.
		collection.Fields.Add(&core.URLField{
			Name: "bd_endpoint",
		})

		// Return URL allowlist bounds the ?return= param the SPA may bounce
		// to. Prevents open-redirect phishing pivots.
		collection.Fields.Add(&core.JSONField{
			Name:    "return_url_allowlist",
			MaxSize: 16 * 1024,
		})

		// Autodates — base enforces these as system fields.
		collection.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		collection.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnCreate: true,
			OnUpdate: true,
		})

		// Unique-by-name index: prevents two tenant rows with the same
		// `name` (IAM owner collision). Hostnames are NOT uniquely indexed
		// at the SQL level because JSON array indexing in SQLite/Postgres
		// differs — uniqueness is enforced in TenantRepo.FindByHostname
		// (returns the first match; handler writes check for duplicates).
		collection.AddIndex("idx_commerce_tenants_name", true, "name", "")

		// Admin API rules: no public read/write. Every path into this table
		// goes through TenantRepo with an already-authenticated + scoped
		// caller. Leaving rules = nil on a base collection blocks all REST
		// exposure from base's auto-generated endpoints.

		if err := app.Save(collection); err != nil {
			return fmt.Errorf("create commerce_tenants: %w", err)
		}
		return nil
	}, func(app core.App) error {
		c, err := app.FindCollectionByNameOrId("commerce_tenants")
		if err != nil {
			return nil // already absent — idempotent down
		}
		return app.Delete(c)
	})
}
