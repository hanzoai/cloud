// Package store — hanzo/base seam entry point.
//
// Construction:
//
//	s, err := store.New(store.Config{DataDir: "/var/lib/commerce"})
//
// Postgres override (multi-instance deployments):
//
//	s, err := store.New(store.Config{
//	    DataDir: "/var/lib/commerce",   // still used for base's aux
//	    DataDSN: "postgres://...",      // takes precedence for the main db
//	})
//
// The Store is the single construction point for collection-backed
// repositories. New() runs Bootstrap (DB connections, settings load, system
// migrations) and then RunAllMigrations (commerce-defined collections).
// Callers hold the resulting *Store for the process lifetime.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hanzoai/base/core"

	// Register base's system migrations (collection tables, superusers,
	// etc.). These run during Bootstrap() — if they are not imported the
	// BaseApp starts with no `_collections` table and every subsequent
	// query fails. The main base package imports this transitively via
	// its own `base.go`, but we invoke core.NewBaseApp directly so we
	// must import explicitly.
	_ "github.com/hanzoai/base/migrations"

	// Register commerce-owned base migrations. The init() in each file under
	// commerce/store/migrations appends to core.AppMigrations. Importing here
	// ensures they run before the store is handed to the handlers.
	_ "github.com/hanzoai/cloud/clients/commerce/store/migrations"
)

// Config controls how the store connects to its backing database. Zero value
// is valid and selects the file-path SQLite default under DataDir.
type Config struct {
	// DataDir is the base filesystem path for SQLite DB files. If empty,
	// defaults to ./commerce_data (matches commerced's default).
	DataDir string

	// DataDSN is an optional PostgreSQL DSN ("postgres://user:pass@host/db").
	// When set, overrides the main data DB; aux still lives under DataDir.
	DataDSN string

	// AuxDSN is an optional PostgreSQL DSN for the auxiliary DB. When empty,
	// aux falls back to DataDir/auxiliary.db.
	AuxDSN string

	// QueryTimeout is applied to all store-issued queries. Defaults to 30s
	// when zero; never unlimited — a hung query is a DoS vector.
	QueryTimeout time.Duration
}

// FromEnv builds a Config from conventional environment variables.
// Precedence matches hanzo/base: DSN set → Postgres; otherwise SQLite.
//
//	COMMERCE_DATA_DIR   data dir (default "./commerce_data")
//	COMMERCE_BASE_URL   main data DSN (Postgres) — takes precedence
//	COMMERCE_BASE_AUX   aux DSN (Postgres)
func FromEnv() Config {
	cfg := Config{
		DataDir: os.Getenv("COMMERCE_DATA_DIR"),
		DataDSN: os.Getenv("COMMERCE_BASE_URL"),
		AuxDSN:  os.Getenv("COMMERCE_BASE_AUX"),
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./commerce_data"
	}
	return cfg
}

// Store is the seam between commerce's HTTP layer and hanzo/base. Add a new
// collection by (a) writing a migration under store/migrations, (b) adding a
// typed repo, (c) wiring it onto this struct.
type Store struct {
	App     core.App
	Tenants *TenantRepo
	KV      *KVStore
}

// New bootstraps a base app from cfg and returns a store ready to serve.
// The caller owns lifecycle: Close() releases DB handles and stops the
// base cron ticker.
func New(cfg Config) (*Store, error) {
	if cfg.DataDir == "" {
		cfg.DataDir = "./commerce_data"
	}
	if cfg.QueryTimeout == 0 {
		cfg.QueryTimeout = 30 * time.Second
	}

	appCfg := core.BaseAppConfig{
		DataDir:      cfg.DataDir,
		QueryTimeout: cfg.QueryTimeout,
		DataDSN:      cfg.DataDSN,
		AuxDSN:       cfg.AuxDSN,
	}
	app := core.NewBaseApp(appCfg)

	if err := app.Bootstrap(); err != nil {
		return nil, fmt.Errorf("store: bootstrap: %w", err)
	}

	// Run the commerce-registered migrations (and any base system migrations
	// that are still pending). RunAllMigrations executes SystemMigrations
	// first, then AppMigrations — the commerce/store/migrations package
	// registers into AppMigrations via init().
	if err := app.RunAllMigrations(); err != nil {
		return nil, fmt.Errorf("store: migrate up: %w", err)
	}

	return &Store{
		App:     app,
		Tenants: NewTenantRepo(app),
		KV:      NewKVStore(app),
	}, nil
}

// Close releases DB handles. Safe to call once per Store.
func (s *Store) Close(_ context.Context) error {
	if s == nil || s.App == nil {
		return errors.New("store: nil app")
	}
	return s.App.ResetBootstrapState()
}
