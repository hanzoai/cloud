// Package sqlstore is the Hanzo Base (HIP-0105 per-tenant SQLite) adapter for the
// ledger core: it implements ledger.Store + ledger.Tx over one SQLite file, and
// nothing more. It carries the storage concern the core deliberately does not — the
// SAME single-connection + WAL pattern every clients/* store uses (referrals, crm,
// prompts), so it is Base-compatible and drops into the unified binary unchanged.
//
// It imports the core (ledger) and the one Hanzo SQLite driver — never cloud, zip,
// or IAM. When the core is lifted to hanzoai/finance this adapter travels with it
// as the default backend; the driver import is the only thing a different Base
// backend would swap.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags) — the
	// Base storage substrate, identical to every other clients/* store.
	_ "github.com/hanzoai/sqlite"

	"github.com/hanzoai/cloud/clients/treasury/ledger"
	"github.com/hanzoai/cloud/internal/cek"
)

// Store is the SQLite-backed ledger persistence. ONE file holds the whole chart of
// accounts, journal and policy for a deployment (the platform's own books — not
// per-org, unlike a tenant product store). Serialized on a single connection so the
// engine's balance-guard read-then-write is atomic under load.
type Store struct {
	db *sql.DB
}

// Open opens (creating + migrating) the ledger database at path.
func Open(path string) (*Store, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1) // serialize: the balance guard depends on it
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS treasury_accounts (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS treasury_entries (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,
  program      TEXT NOT NULL DEFAULT '',
  ref          TEXT NOT NULL,
  memo         TEXT NOT NULL DEFAULT '',
  amount_cents INTEGER NOT NULL,
  created_at   INTEGER NOT NULL,
  UNIQUE(kind, program, ref)
);
CREATE INDEX IF NOT EXISTS ix_treasury_entries_created ON treasury_entries(created_at);

CREATE TABLE IF NOT EXISTS treasury_postings (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id   TEXT NOT NULL REFERENCES treasury_entries(id),
  account    TEXT NOT NULL,
  amount     INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_treasury_postings_account ON treasury_postings(account);
CREATE INDEX IF NOT EXISTS ix_treasury_postings_entry   ON treasury_postings(entry_id);

CREATE TABLE IF NOT EXISTS treasury_policy (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  revenue_share_bps INTEGER NOT NULL DEFAULT 0,
  updated_at        INTEGER NOT NULL DEFAULT 0
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("treasury migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ── ledger.Store (read path, no tx) ──────────────────────────────────────────

func (s *Store) Balance(ctx context.Context, account string) (int64, error) {
	var bal int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM treasury_postings WHERE account=?`, account).Scan(&bal)
	if err != nil {
		return 0, fmt.Errorf("balance %q: %w", account, err)
	}
	return bal, nil
}

func (s *Store) BalancesWithPrefix(ctx context.Context, prefix string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account, COALESCE(SUM(amount),0) FROM treasury_postings WHERE account LIKE ? GROUP BY account`,
		prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("balances prefix %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int64)
	for rows.Next() {
		var acct string
		var bal int64
		if err := rows.Scan(&acct, &bal); err != nil {
			return nil, fmt.Errorf("scan balance: %w", err)
		}
		out[acct] = bal
	}
	return out, rows.Err()
}

func (s *Store) Entries(ctx context.Context, limit int) ([]ledger.Entry, error) {
	// limit <= 0 means EVERY entry (the ledger.Root full scan); a positive limit
	// bounds the admin journal read.
	q := `SELECT id,kind,program,ref,memo,amount_cents,created_at
		   FROM treasury_entries ORDER BY created_at DESC, id DESC`
	args := []any{}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ledger.Entry
	for rows.Next() {
		var e ledger.Entry
		if err := rows.Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &e.AmountCents, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ps, err := s.postingsOf(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Postings = ps
	}
	return out, nil
}

func (s *Store) postingsOf(ctx context.Context, entryID string) ([]ledger.Posting, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account, amount FROM treasury_postings WHERE entry_id=? ORDER BY id`, entryID)
	if err != nil {
		return nil, fmt.Errorf("list postings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ledger.Posting
	for rows.Next() {
		var p ledger.Posting
		if err := rows.Scan(&p.Account, &p.Amount); err != nil {
			return nil, fmt.Errorf("scan posting: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Policy(ctx context.Context) (ledger.Policy, error) {
	var p ledger.Policy
	err := s.db.QueryRowContext(ctx,
		`SELECT revenue_share_bps, updated_at FROM treasury_policy WHERE id=1`).Scan(&p.RevenueShareBps, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Policy{}, nil // unset → 0%, an honest default
	}
	if err != nil {
		return ledger.Policy{}, fmt.Errorf("read policy: %w", err)
	}
	return p, nil
}

func (s *Store) SetPolicy(ctx context.Context, p ledger.Policy) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO treasury_policy (id, revenue_share_bps, updated_at) VALUES (1,?,?)
		 ON CONFLICT(id) DO UPDATE SET revenue_share_bps=excluded.revenue_share_bps, updated_at=excluded.updated_at`,
		p.RevenueShareBps, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("set policy: %w", err)
	}
	return nil
}

// ── ledger.Store.Tx + ledger.Tx (write path) ─────────────────────────────────

func (s *Store) Tx(ctx context.Context, fn func(ledger.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(&txAdapter{ctx: ctx, tx: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// txAdapter binds ledger.Tx to a live *sql.Tx.
type txAdapter struct {
	ctx context.Context
	tx  *sql.Tx
}

func (t *txAdapter) EntryByRef(kind, program, ref string) (ledger.Entry, bool, error) {
	var e ledger.Entry
	err := t.tx.QueryRowContext(t.ctx,
		`SELECT id,kind,program,ref,memo,amount_cents,created_at
		   FROM treasury_entries WHERE kind=? AND program=? AND ref=?`, kind, program, ref).
		Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &e.AmountCents, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, nil
	}
	if err != nil {
		return ledger.Entry{}, false, fmt.Errorf("entry by ref: %w", err)
	}
	return e, true, nil
}

func (t *txAdapter) Balance(account string) (int64, error) {
	var bal int64
	err := t.tx.QueryRowContext(t.ctx,
		`SELECT COALESCE(SUM(amount),0) FROM treasury_postings WHERE account=?`, account).Scan(&bal)
	if err != nil {
		return 0, fmt.Errorf("tx balance %q: %w", account, err)
	}
	return bal, nil
}

func (t *txAdapter) Insert(e ledger.Entry, postings []ledger.Posting) error {
	for _, p := range postings {
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT OR IGNORE INTO treasury_accounts (id, kind, created_at) VALUES (?,?,?)`,
			p.Account, accountKind(p.Account), e.CreatedAt); err != nil {
			return fmt.Errorf("upsert account %q: %w", p.Account, err)
		}
	}
	if _, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO treasury_entries (id,kind,program,ref,memo,amount_cents,created_at) VALUES (?,?,?,?,?,?,?)`,
		e.ID, e.Kind, e.Program, e.Ref, e.Memo, e.AmountCents, e.CreatedAt); err != nil {
		return fmt.Errorf("insert entry: %w", err)
	}
	for _, p := range postings {
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT INTO treasury_postings (entry_id, account, amount, created_at) VALUES (?,?,?,?)`,
			e.ID, p.Account, p.Amount, e.CreatedAt); err != nil {
			return fmt.Errorf("insert posting: %w", err)
		}
	}
	return nil
}

// accountKind derives an account's kind from its id namespace (the segment before
// the first ':'), so the accounts table stays queryable by kind without the engine
// having to pass it.
func accountKind(account string) string {
	if i := strings.IndexByte(account, ':'); i > 0 {
		return account[:i]
	}
	return "other"
}
