// Package order provides the Order model.
// This file contains SQL schema definitions for orders when using
// the structured table approach (vs JSON entity storage).
package order

import (
	"context"
	"database/sql"
)

// Schema contains SQL schema for orders supporting both SQLite and PostgreSQL
var Schema = struct {
	SQLite     string
	PostgreSQL string
}{
	SQLite: `
		-- Orders table for SQLite
		CREATE TABLE IF NOT EXISTS orders (
			id TEXT PRIMARY KEY,
			number INTEGER,

			-- References
			store_id TEXT,
			campaign_id TEXT,
			user_id TEXT,
			email TEXT,
			cart_id TEXT,
			referrer_id TEXT,
			referral_id TEXT,

			-- Status
			status TEXT NOT NULL DEFAULT 'open',
			payment_status TEXT NOT NULL DEFAULT 'unpaid',
			preorder INTEGER DEFAULT 0,
			unconfirmed INTEGER DEFAULT 0,

			-- Payment
			currency TEXT NOT NULL DEFAULT 'usd',
			payment_type TEXT,
			payment_method_id TEXT,
			mode TEXT,
			shipping_method TEXT,

			-- Amounts (in cents)
			line_total INTEGER DEFAULT 0,
			taxable_line_total INTEGER DEFAULT 0,
			discount INTEGER DEFAULT 0,
			subtotal INTEGER DEFAULT 0,
			shipping INTEGER DEFAULT 0,
			tax INTEGER DEFAULT 0,
			adjustment INTEGER DEFAULT 0,
			total INTEGER DEFAULT 0,
			balance INTEGER DEFAULT 0,
			paid INTEGER DEFAULT 0,
			refunded INTEGER DEFAULT 0,

			-- Addresses (JSON)
			company TEXT,
			billing_address JSON,
			shipping_address JSON,

			-- Items and Discounts (JSON arrays)
			items JSON,
			adjustments JSON,
			discounts JSON,
			coupons JSON,
			coupon_codes JSON,
			payment_ids JSON,

			-- Dates
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			cancelled_at DATETIME,

			-- Fulfillment (JSON)
			fulfillment JSON,
			return_ids JSON,

			-- Gift options
			gift INTEGER DEFAULT 0,
			gift_message TEXT,
			gift_email TEXT,

			-- Token sales
			token_sale_id TEXT,

			-- Integrations (JSON)
			mailchimp JSON,
			notifications JSON,
			metadata JSON,
			history JSON,

			-- Flags
			test INTEGER DEFAULT 0,
			deleted INTEGER DEFAULT 0,
			version INTEGER DEFAULT 1,

			-- Subscriptions
			subscriptions JSON,

			-- Form/Template
			form_id TEXT,
			template_id TEXT
		);

		-- Indexes for common queries
		CREATE INDEX IF NOT EXISTS idx_orders_user_id ON orders(user_id);
		CREATE INDEX IF NOT EXISTS idx_orders_email ON orders(email);
		CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
		CREATE INDEX IF NOT EXISTS idx_orders_payment_status ON orders(payment_status);
		CREATE INDEX IF NOT EXISTS idx_orders_store_id ON orders(store_id);
		CREATE INDEX IF NOT EXISTS idx_orders_campaign_id ON orders(campaign_id);
		CREATE INDEX IF NOT EXISTS idx_orders_referrer_id ON orders(referrer_id);
		CREATE INDEX IF NOT EXISTS idx_orders_created_at ON orders(created_at);
		CREATE INDEX IF NOT EXISTS idx_orders_deleted ON orders(deleted);
		CREATE INDEX IF NOT EXISTS idx_orders_test ON orders(test);
		CREATE INDEX IF NOT EXISTS idx_orders_number ON orders(number);
	`,

	PostgreSQL: `
		-- Orders table for PostgreSQL
		CREATE TABLE IF NOT EXISTS orders (
			id TEXT PRIMARY KEY,
			number INTEGER,

			-- References
			store_id TEXT,
			campaign_id TEXT,
			user_id TEXT,
			email TEXT,
			cart_id TEXT,
			referrer_id TEXT,
			referral_id TEXT,

			-- Status
			status TEXT NOT NULL DEFAULT 'open',
			payment_status TEXT NOT NULL DEFAULT 'unpaid',
			preorder BOOLEAN DEFAULT FALSE,
			unconfirmed BOOLEAN DEFAULT FALSE,

			-- Payment
			currency TEXT NOT NULL DEFAULT 'usd',
			payment_type TEXT,
			payment_method_id TEXT,
			mode TEXT,
			shipping_method TEXT,

			-- Amounts (in cents)
			line_total BIGINT DEFAULT 0,
			taxable_line_total BIGINT DEFAULT 0,
			discount BIGINT DEFAULT 0,
			subtotal BIGINT DEFAULT 0,
			shipping BIGINT DEFAULT 0,
			tax BIGINT DEFAULT 0,
			adjustment BIGINT DEFAULT 0,
			total BIGINT DEFAULT 0,
			balance BIGINT DEFAULT 0,
			paid BIGINT DEFAULT 0,
			refunded BIGINT DEFAULT 0,

			-- Addresses
			company TEXT,
			billing_address JSONB,
			shipping_address JSONB,

			-- Items and Discounts
			items JSONB DEFAULT '[]'::JSONB,
			adjustments JSONB DEFAULT '[]'::JSONB,
			discounts JSONB DEFAULT '[]'::JSONB,
			coupons JSONB DEFAULT '[]'::JSONB,
			coupon_codes JSONB DEFAULT '[]'::JSONB,
			payment_ids JSONB DEFAULT '[]'::JSONB,

			-- Dates
			created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
			cancelled_at TIMESTAMPTZ,

			-- Fulfillment
			fulfillment JSONB,
			return_ids JSONB DEFAULT '[]'::JSONB,

			-- Gift options
			gift BOOLEAN DEFAULT FALSE,
			gift_message TEXT,
			gift_email TEXT,

			-- Token sales
			token_sale_id TEXT,

			-- Integrations
			mailchimp JSONB,
			notifications JSONB,
			metadata JSONB DEFAULT '{}'::JSONB,
			history JSONB DEFAULT '[]'::JSONB,

			-- Flags
			test BOOLEAN DEFAULT FALSE,
			deleted BOOLEAN DEFAULT FALSE,
			version INTEGER DEFAULT 1,

			-- Subscriptions
			subscriptions JSONB DEFAULT '[]'::JSONB,

			-- Form/Template
			form_id TEXT,
			template_id TEXT
		);

		-- Indexes for common queries
		CREATE INDEX IF NOT EXISTS idx_orders_user_id ON orders(user_id);
		CREATE INDEX IF NOT EXISTS idx_orders_email ON orders(email);
		CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
		CREATE INDEX IF NOT EXISTS idx_orders_payment_status ON orders(payment_status);
		CREATE INDEX IF NOT EXISTS idx_orders_store_id ON orders(store_id);
		CREATE INDEX IF NOT EXISTS idx_orders_campaign_id ON orders(campaign_id);
		CREATE INDEX IF NOT EXISTS idx_orders_referrer_id ON orders(referrer_id);
		CREATE INDEX IF NOT EXISTS idx_orders_created_at ON orders(created_at);
		CREATE INDEX IF NOT EXISTS idx_orders_deleted ON orders(deleted);
		CREATE INDEX IF NOT EXISTS idx_orders_test ON orders(test);
		CREATE INDEX IF NOT EXISTS idx_orders_number ON orders(number);

		-- GIN indexes for JSONB columns
		CREATE INDEX IF NOT EXISTS idx_orders_metadata ON orders USING GIN (metadata);
		CREATE INDEX IF NOT EXISTS idx_orders_items ON orders USING GIN (items);
	`,
}

// SchemaManager provides methods for managing order schema
type SchemaManager struct {
	db *sql.DB
}

// NewSchemaManager creates a new schema manager
func NewSchemaManager(database *sql.DB) *SchemaManager {
	return &SchemaManager{db: database}
}

// CreateSchema creates the orders table and indexes
func (sm *SchemaManager) CreateSchema(ctx context.Context, dialect string) error {
	var schema string
	switch dialect {
	case "sqlite", "sqlite3":
		schema = Schema.SQLite
	case "postgres", "postgresql":
		schema = Schema.PostgreSQL
	default:
		schema = Schema.SQLite
	}

	_, err := sm.db.ExecContext(ctx, schema)
	return err
}

// DropSchema drops the orders table
func (sm *SchemaManager) DropSchema(ctx context.Context) error {
	_, err := sm.db.ExecContext(ctx, "DROP TABLE IF EXISTS orders")
	return err
}

// Migrations contains SQL migrations for the orders table
var Migrations = []Migration{
	{
		Version:     1,
		Description: "Create orders table",
		Up:          Schema.SQLite,
		Down:        "DROP TABLE IF EXISTS orders",
	},
	{
		Version:     2,
		Description: "Add wallet fields",
		Up: `
			ALTER TABLE orders ADD COLUMN wallet_id TEXT;
			ALTER TABLE orders ADD COLUMN wallet_passphrase TEXT;
		`,
		Down: `
			ALTER TABLE orders DROP COLUMN wallet_id;
			ALTER TABLE orders DROP COLUMN wallet_passphrase;
		`,
	},
	{
		Version:     3,
		Description: "Add analytics fields",
		Up: `
			ALTER TABLE orders ADD COLUMN utm_source TEXT;
			ALTER TABLE orders ADD COLUMN utm_medium TEXT;
			ALTER TABLE orders ADD COLUMN utm_campaign TEXT;
			ALTER TABLE orders ADD COLUMN utm_term TEXT;
			ALTER TABLE orders ADD COLUMN utm_content TEXT;
		`,
		Down: `
			ALTER TABLE orders DROP COLUMN utm_source;
			ALTER TABLE orders DROP COLUMN utm_medium;
			ALTER TABLE orders DROP COLUMN utm_campaign;
			ALTER TABLE orders DROP COLUMN utm_term;
			ALTER TABLE orders DROP COLUMN utm_content;
		`,
	},
}

// Migration represents a database migration
type Migration struct {
	Version     int
	Description string
	Up          string
	Down        string
}

// MigrationManager handles database migrations
type MigrationManager struct {
	db         *sql.DB
	migrations []Migration
}

// NewMigrationManager creates a new migration manager
func NewMigrationManager(database *sql.DB) *MigrationManager {
	return &MigrationManager{
		db:         database,
		migrations: Migrations,
	}
}

// EnsureMigrationTable creates the migration tracking table
func (mm *MigrationManager) EnsureMigrationTable(ctx context.Context) error {
	_, err := mm.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS _order_migrations (
			version INTEGER PRIMARY KEY,
			description TEXT,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

// GetAppliedVersions returns all applied migration versions
func (mm *MigrationManager) GetAppliedVersions(ctx context.Context) ([]int, error) {
	rows, err := mm.db.QueryContext(ctx, "SELECT version FROM _order_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// Migrate runs all pending migrations
func (mm *MigrationManager) Migrate(ctx context.Context) error {
	if err := mm.EnsureMigrationTable(ctx); err != nil {
		return err
	}

	applied, err := mm.GetAppliedVersions(ctx)
	if err != nil {
		return err
	}

	appliedMap := make(map[int]bool)
	for _, v := range applied {
		appliedMap[v] = true
	}

	for _, m := range mm.migrations {
		if appliedMap[m.Version] {
			continue
		}

		tx, err := mm.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, m.Up); err != nil {
			tx.Rollback()
			return err
		}

		if _, err := tx.ExecContext(ctx,
			"INSERT INTO _order_migrations (version, description) VALUES (?, ?)",
			m.Version, m.Description); err != nil {
			tx.Rollback()
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}

// Rollback rolls back the last migration
func (mm *MigrationManager) Rollback(ctx context.Context) error {
	applied, err := mm.GetAppliedVersions(ctx)
	if err != nil {
		return err
	}

	if len(applied) == 0 {
		return nil
	}

	lastVersion := applied[len(applied)-1]

	var migration *Migration
	for _, m := range mm.migrations {
		if m.Version == lastVersion {
			migration = &m
			break
		}
	}

	if migration == nil {
		return nil
	}

	tx, err := mm.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, migration.Down); err != nil {
		tx.Rollback()
		return err
	}

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM _order_migrations WHERE version = ?",
		lastVersion); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
