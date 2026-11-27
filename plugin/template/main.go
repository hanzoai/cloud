package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	luxlog "github.com/luxfi/log"

	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/apps/template"
	"github.com/hanzoai/namespace"
)

// use mounts the gallery with this host's answers to what it asks: who the caller
// is comes from the identity boundary (principal), and the private kits live in the
// deployment's "templates" store — a SQLite file or a schema on hanzo-sql, wherever
// locus places it — migrated under dialect's lock so replicas starting together
// do not race the schema.
//
// The adapter lives HERE, on cloud's side: the gallery is built from hanzoai/cloud
// and must never import this host, so the host's plugin contract bends to the app
// rather than the app learning about Deps.
func use(app cloud.Router, _ cloud.Deps) error {
	return template.Mount(app, template.Deps{
		Log:    luxlog.Default().New("subsystem", "template"),
		Org:    principal.OrgFrom,
		Acting: principal.Acting,
		Open:   open,
	})
}

// open places the store, applies the gallery's schema to it, and hands the database
// over. The gallery closes it in Shutdown.
func open(ctx context.Context, migrate func(context.Context, *sql.Tx) error) (*sql.DB, error) {
	return locus.Use(ctx, nil, namespace.System(), "templates", cloud.DataDir(), func(db *sql.DB) (*sql.DB, error) {
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error { return migrate(ctx, tx) }); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
		return db, nil
	})
}

// Standalone entry for the templates app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `templates openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "template",
		Price:    cloud.Free,
		Use:      use,
		Shutdown: template.Shutdown,
	}}, []string{"template"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
