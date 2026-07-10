package migrations

import (
	"fmt"

	"github.com/hanzoai/base/core"
)

// 1735000002 — the commerce_kv collection. This replaces the former
// Redis/Valkey KV: a per-store (per-org/user) embedded cache backed by base's
// SQLite (or Postgres when DataDSN is set). One row per key, with a lazy-TTL
// expires_at (unix millis; 0 == no expiry).
//
// The KVStore (store/kv.go) is the only writer. No base REST exposure: rules
// are left nil so base's auto-generated endpoints reject every request — the
// cache is process-internal, never a public surface.
func init() {
	core.AppMigrations.Register(func(app core.App) error {
		collection := core.NewBaseCollection("commerce_kv")

		// key — the cache key. Unique so each key maps to exactly one row;
		// the unique index also backstops the SetNX absence check under any
		// isolation surprise. Max 1024 covers the longest commerce key
		// ("iam:org_by_name:<host>", "lock:<key>") with generous headroom.
		collection.Fields.Add(&core.TextField{
			Name:     "key",
			Required: true,
			Max:      1024,
		})

		// value — opaque bytes stored as text. Commerce values are small
		// JSON / id strings; 1 MB caps a pathological writer without
		// constraining real use.
		collection.Fields.Add(&core.TextField{
			Name: "value",
			Max:  1024 * 1024,
		})

		// expires_at — absolute expiry in unix millis. 0 is the no-expiry
		// sentinel. Reads compare against now and lazily delete expired rows.
		collection.Fields.Add(&core.NumberField{
			Name: "expires_at",
		})

		// Autodates for observability/debugging; not used by the cache logic.
		collection.Fields.Add(&core.AutodateField{
			Name:     "created",
			OnCreate: true,
		})
		collection.Fields.Add(&core.AutodateField{
			Name:     "updated",
			OnCreate: true,
			OnUpdate: true,
		})

		// Unique-by-key: one row per cache key.
		collection.AddIndex("idx_commerce_kv_key", true, "key", "")
		// Non-unique expiry index for any future sweep job.
		collection.AddIndex("idx_commerce_kv_expires_at", false, "expires_at", "")

		if err := app.Save(collection); err != nil {
			return fmt.Errorf("create commerce_kv: %w", err)
		}
		return nil
	}, func(app core.App) error {
		c, err := app.FindCollectionByNameOrId("commerce_kv")
		if err != nil {
			return nil // already absent — idempotent down
		}
		return app.Delete(c)
	})
}
