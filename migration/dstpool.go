// Destination pool for per-(org, user) SQLite files. Opens lazily on
// first row routed to an (org, user), caches the *sql.DB handle,
// applies canonical pragmas, and creates the per-table DDL on first
// insert into each table.
//
// Concurrency note: the migrator is single-goroutine row streamer, so
// the pool itself is unsynchronised — adding the rows for one source
// table is serialised end-to-end. If the migrator ever becomes
// concurrent it needs a sync.Mutex around the map plus per-handle
// locking.

package migration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/internal/cek"
)

// dstPool owns the open destination SQLite handles, one per
// (org, user) tuple.
type dstPool struct {
	root    string
	handles map[string]*dstHandle
}

func newDstPool(root string) *dstPool {
	return &dstPool{
		root:    root,
		handles: map[string]*dstHandle{},
	}
}

// Close drains every open dst handle. Safe to call from defer.
func (p *dstPool) Close() {
	for _, h := range p.handles {
		_ = h.Close()
	}
}

// flushAll commits any open per-handle transactions. Called every
// BatchSize rows from copyTable so a multi-million-row table doesn't
// hold one giant transaction.
func (p *dstPool) flushAll(ctx context.Context) error {
	for _, h := range p.handles {
		if err := h.flush(ctx); err != nil {
			return err
		}
	}
	return nil
}

// get returns the handle for (org, user), opening + bootstrapping the
// per-table schema on first call.
func (p *dstPool) get(ctx context.Context, org, user string, t TableInfo) (*dstHandle, error) {
	key := org + "/" + user
	h, ok := p.handles[key]
	if !ok {
		path, err := destinationPath(p.root, org, user)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		db, err := cek.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		for _, p := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA synchronous=NORMAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := db.ExecContext(ctx, p); err != nil {
				db.Close()
				return nil, fmt.Errorf("pragma %q: %w", p, err)
			}
		}
		db.SetMaxOpenConns(1)
		h = &dstHandle{db: db, path: path, tables: map[string]bool{}}
		p.handles[key] = h
	}
	if !h.tables[t.Name] {
		if err := h.createTable(ctx, t); err != nil {
			return nil, err
		}
		h.tables[t.Name] = true
	}
	return h, nil
}

// destinationPath derives /data/<org>/<user>/cloud.sqlite. The user
// "_org" sentinel routes to /data/<org>/_org/cloud.sqlite, matching
// the convention in CLAUDE_PG_TO_SQLITE_MIGRATION.md.
func destinationPath(root, org, user string) (string, error) {
	if !validToken.MatchString(org) {
		return "", fmt.Errorf("invalid org token %q", org)
	}
	if !validToken.MatchString(user) {
		return "", fmt.Errorf("invalid user token %q", user)
	}
	return filepath.Join(root, org, user, "cloud.sqlite"), nil
}

// dstHandle wraps one (org, user) SQLite file. Keeps a per-call
// transaction open so batched inserts amortise fsync cost.
type dstHandle struct {
	db     *sql.DB
	path   string
	tables map[string]bool
	tx     *sql.Tx
}

// createTable applies the canonical-shape DDL for one source PG table.
// Each table gets the same column list (verbatim), with PG types
// mapped via mapPGType. Foreign keys are dropped — the legacy schema's
// FKs are not portable across the per-(org, user) split.
func (h *dstHandle) createTable(ctx context.Context, t TableInfo) error {
	parts := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		ddl := fmt.Sprintf("%q %s", c.Name, mapPGType(c.DataType))
		if !c.IsNullable {
			ddl += " NOT NULL"
		}
		parts = append(parts, ddl)
	}
	stmt := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %q (%s)`, t.Name, strings.Join(parts, ", "))
	_, err := h.db.ExecContext(ctx, stmt)
	return err
}

// insert appends one row to the destination table, opening a tx lazily.
func (h *dstHandle) insert(ctx context.Context, table string, row []any) error {
	if h.tx == nil {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		h.tx = tx
	}
	placeholders := strings.Repeat("?, ", len(row))
	placeholders = strings.TrimSuffix(placeholders, ", ")
	stmt := fmt.Sprintf(`INSERT INTO %q VALUES (%s)`, table, placeholders)
	_, err := h.tx.ExecContext(ctx, stmt, row...)
	return err
}

// flush commits the open transaction if any.
func (h *dstHandle) flush(ctx context.Context) error {
	if h.tx == nil {
		return nil
	}
	err := h.tx.Commit()
	h.tx = nil
	return err
}

// Close commits any open tx and closes the underlying *sql.DB.
func (h *dstHandle) Close() error {
	_ = h.flush(context.Background())
	return h.db.Close()
}

// mapPGType maps a PG data_type string to a SQLite affinity. The
// mapping is conservative — anything unrecognised becomes TEXT, which
// the SQLite type system accepts for any value and is the same
// fallback hanzoai/base uses.
func mapPGType(pgType string) string {
	switch strings.ToLower(pgType) {
	case "smallint", "integer", "int", "bigint",
		"serial", "bigserial", "smallserial":
		return "INTEGER"
	case "boolean", "bool":
		return "INTEGER"
	case "real", "double precision", "numeric", "decimal":
		return "REAL"
	case "bytea":
		return "BLOB"
	default:
		return "TEXT"
	}
}
