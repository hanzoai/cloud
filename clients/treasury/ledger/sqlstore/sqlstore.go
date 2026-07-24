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
//
// MONEY IS EXACT AND BIG. Amounts are 18-decimal USD (1e-18, the EVM/uint256 unit) held as
// big.Int money.Amount — a value exceeds SQLite's 64-bit INTEGER past ~$9.20, so amount
// columns are TEXT (the signed 18-decimal integer string) and an account's balance is a
// maintained running total (treasury_accounts.balance), NOT a SQL SUM (you cannot SUM a
// decimal-string column, and a busy wallet's million usage postings must not be re-summed
// on every gate read). The running balance is updated inside the same transaction as each
// posting, so it can never drift from the journal.
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

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/treasury/ledger"
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
	// Target 18-decimal schema — a fresh file gets this directly.
	const ddl = `
CREATE TABLE IF NOT EXISTS treasury_accounts (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  balance    TEXT NOT NULL DEFAULT '0',
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS treasury_entries (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  program    TEXT NOT NULL DEFAULT '',
  ref        TEXT NOT NULL,
  memo       TEXT NOT NULL DEFAULT '',
  amount     TEXT NOT NULL DEFAULT '0',
  created_at INTEGER NOT NULL,
  UNIQUE(kind, program, ref)
);
CREATE INDEX IF NOT EXISTS ix_treasury_entries_created ON treasury_entries(created_at);

CREATE TABLE IF NOT EXISTS treasury_postings (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id   TEXT NOT NULL REFERENCES treasury_entries(id),
  account    TEXT NOT NULL,
  amount     TEXT NOT NULL DEFAULT '0',
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
	if err := s.migrateCentsToUnits(); err != nil {
		return err
	}
	// Re-apply the DDL so any table REBUILT by the legacy migration regains its indexes
	// (every statement is IF NOT EXISTS, so this is a no-op for already-present objects).
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("treasury reindex: %w", err)
	}
	return nil
}

// migrateCentsToUnits rebuilds a pre-migration file (the legacy INTEGER-cents schema:
// treasury_entries.amount_cents + INTEGER posting amounts, no accounts.balance) into the
// 18-decimal schema, converting every stored cents value ×1e16 to the exact 18-decimal integer and materializing
// each account's running balance. Idempotent: on an already-migrated file it detects the
// absent amount_cents column and returns immediately. Runs once, inside one transaction.
func (s *Store) migrateCentsToUnits() error {
	legacy, err := s.hasColumn("treasury_entries", "amount_cents")
	if err != nil {
		return fmt.Errorf("detect legacy schema: %w", err)
	}
	if !legacy {
		return nil // already migrated (or fresh)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("migrate begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The 18-decimal tables were created above with different names? No — CREATE TABLE IF NOT
	// EXISTS was a no-op because the legacy tables already exist. Rename them aside, make
	// the 18-decimal tables, copy-convert, drop the legacy copies.
	for _, stmt := range []string{
		`ALTER TABLE treasury_entries  RENAME TO treasury_entries_legacy`,
		`ALTER TABLE treasury_postings RENAME TO treasury_postings_legacy`,
		`ALTER TABLE treasury_accounts RENAME TO treasury_accounts_legacy`,
		`CREATE TABLE treasury_accounts (
		   id TEXT PRIMARY KEY, kind TEXT NOT NULL, balance TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
		`CREATE TABLE treasury_entries (
		   id TEXT PRIMARY KEY, kind TEXT NOT NULL, program TEXT NOT NULL DEFAULT '', ref TEXT NOT NULL,
		   memo TEXT NOT NULL DEFAULT '', amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL,
		   UNIQUE(kind, program, ref))`,
		`CREATE TABLE treasury_postings (
		   id INTEGER PRIMARY KEY AUTOINCREMENT, entry_id TEXT NOT NULL REFERENCES treasury_entries(id),
		   account TEXT NOT NULL, amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrate ddl %q: %w", stmt, err)
		}
	}

	// entries: amount_cents (int) → amount (18-decimal integer text)
	if err := convertRows(tx,
		`SELECT id,kind,program,ref,memo,amount_cents,created_at FROM treasury_entries_legacy`,
		func(scan func(...any) error) error {
			var id, kind, program, ref, memo string
			var cents, created int64
			if err := scan(&id, &kind, &program, &ref, &memo, &cents, &created); err != nil {
				return err
			}
			_, err := tx.Exec(
				`INSERT INTO treasury_entries (id,kind,program,ref,memo,amount,created_at) VALUES (?,?,?,?,?,?,?)`,
				id, kind, program, ref, memo, money.FromCents(cents).AttoString(), created)
			return err
		}); err != nil {
		return err
	}

	// postings: amount (int cents) → amount (18-decimal integer text), accumulating each account's balance
	balances := map[string]money.Amount{}
	if err := convertRows(tx,
		`SELECT entry_id,account,amount,created_at FROM treasury_postings_legacy ORDER BY id`,
		func(scan func(...any) error) error {
			var entryID, account string
			var cents, created int64
			if err := scan(&entryID, &account, &cents, &created); err != nil {
				return err
			}
			amt := money.FromCents(cents)
			if _, err := tx.Exec(
				`INSERT INTO treasury_postings (entry_id,account,amount,created_at) VALUES (?,?,?,?)`,
				entryID, account, amt.AttoString(), created); err != nil {
				return err
			}
			balances[account] = balances[account].Add(amt)
			return nil
		}); err != nil {
		return err
	}

	// accounts: carry kind/created_at, set the materialized balance
	if err := convertRows(tx,
		`SELECT id,kind,created_at FROM treasury_accounts_legacy`,
		func(scan func(...any) error) error {
			var id, kind string
			var created int64
			if err := scan(&id, &kind, &created); err != nil {
				return err
			}
			_, err := tx.Exec(
				`INSERT INTO treasury_accounts (id,kind,balance,created_at) VALUES (?,?,?,?)`,
				id, kind, balances[id].AttoString(), created)
			return err
		}); err != nil {
		return err
	}

	for _, drop := range []string{
		`DROP TABLE treasury_postings_legacy`,
		`DROP TABLE treasury_entries_legacy`,
		`DROP TABLE treasury_accounts_legacy`,
	} {
		if _, err := tx.Exec(drop); err != nil {
			return fmt.Errorf("migrate drop %q: %w", drop, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate commit: %w", err)
	}
	return nil
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// convertRows runs q and calls fn for each row with its Scan; used only by the one-shot
// legacy migration so its per-row insert reads inside the same tx.
func convertRows(tx *sql.Tx, q string, fn func(scan func(...any) error) error) error {
	rows, err := tx.Query(q)
	if err != nil {
		return fmt.Errorf("migrate query %q: %w", q, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows.Scan); err != nil {
			return fmt.Errorf("migrate row: %w", err)
		}
	}
	return rows.Err()
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ── ledger.Store (read path, no tx) ──────────────────────────────────────────

func (s *Store) Balance(ctx context.Context, account string) (money.Amount, error) {
	return balanceOf(s.db.QueryRowContext(ctx,
		`SELECT balance FROM treasury_accounts WHERE id=?`, account), account)
}

func (s *Store) BalancesWithPrefix(ctx context.Context, prefix string) (map[string]money.Amount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, balance FROM treasury_accounts WHERE id LIKE ?`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("balances prefix %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]money.Amount)
	for rows.Next() {
		var acct, bal string
		if err := rows.Scan(&acct, &bal); err != nil {
			return nil, fmt.Errorf("scan balance: %w", err)
		}
		amt, err := money.ParseInt(bal)
		if err != nil {
			return nil, fmt.Errorf("parse balance %q: %w", acct, err)
		}
		out[acct] = amt
	}
	return out, rows.Err()
}

func (s *Store) Entries(ctx context.Context, limit int) ([]ledger.Entry, error) {
	// limit <= 0 means EVERY entry (the ledger.Root full scan); a positive limit
	// bounds the admin journal read.
	q := `SELECT id,kind,program,ref,memo,amount,created_at
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
		var amt string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &amt, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		if e.Amount, err = money.ParseInt(amt); err != nil {
			return nil, fmt.Errorf("parse entry amount: %w", err)
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

// SumByKindSince sums the entry amounts of ONE kind created at/after `since` (unix
// seconds) — a read-only aggregate over the indexed created_at, no schema change.
// Amounts are 18-decimal TEXT (SQLite INTEGER overflows past ~$9.20), so the fold is
// in Go. This is the usage-cap's period-spend source: sum kind "finance.usage" for
// the org store since the start of the UTC month, so a cap enforces on real ledger
// spend rather than a bounded, truncatable journal scan.
func (s *Store) SumByKindSince(ctx context.Context, kind string, since int64) (money.Amount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT amount FROM treasury_entries WHERE kind = ? AND created_at >= ?`, kind, since)
	if err != nil {
		return money.Zero(), fmt.Errorf("sum by kind: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sum := money.Zero()
	for rows.Next() {
		var amt string
		if err := rows.Scan(&amt); err != nil {
			return money.Zero(), fmt.Errorf("scan amount: %w", err)
		}
		a, perr := money.ParseInt(amt)
		if perr != nil {
			return money.Zero(), fmt.Errorf("parse amount: %w", perr)
		}
		sum = sum.Add(a)
	}
	return sum, rows.Err()
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
		var amt string
		if err := rows.Scan(&p.Account, &amt); err != nil {
			return nil, fmt.Errorf("scan posting: %w", err)
		}
		if p.Amount, err = money.ParseInt(amt); err != nil {
			return nil, fmt.Errorf("parse posting amount: %w", err)
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
		// Unset → the canonical 20% default (not 0%): a fresh treasury sweeps the same
		// creator/revenue share the rest of the platform uses. A SuperAdmin who wants a
		// different rate (including 0) writes an explicit policy row via SetPolicy.
		return ledger.Policy{RevenueShareBps: ledger.DefaultRevenueShareBps}, nil
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
	var amt string
	err := t.tx.QueryRowContext(t.ctx,
		`SELECT id,kind,program,ref,memo,amount,created_at
		   FROM treasury_entries WHERE kind=? AND program=? AND ref=?`, kind, program, ref).
		Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &amt, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.Entry{}, false, nil
	}
	if err != nil {
		return ledger.Entry{}, false, fmt.Errorf("entry by ref: %w", err)
	}
	if e.Amount, err = money.ParseInt(amt); err != nil {
		return ledger.Entry{}, false, fmt.Errorf("parse entry amount: %w", err)
	}
	return e, true, nil
}

func (t *txAdapter) Balance(account string) (money.Amount, error) {
	return balanceOf(t.tx.QueryRowContext(t.ctx,
		`SELECT balance FROM treasury_accounts WHERE id=?`, account), account)
}

func (t *txAdapter) Insert(e ledger.Entry, postings []ledger.Posting) error {
	for _, p := range postings {
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT OR IGNORE INTO treasury_accounts (id, kind, balance, created_at) VALUES (?,?, '0', ?)`,
			p.Account, accountKind(p.Account), e.CreatedAt); err != nil {
			return fmt.Errorf("upsert account %q: %w", p.Account, err)
		}
	}
	if _, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO treasury_entries (id,kind,program,ref,memo,amount,created_at) VALUES (?,?,?,?,?,?,?)`,
		e.ID, e.Kind, e.Program, e.Ref, e.Memo, e.Amount.AttoString(), e.CreatedAt); err != nil {
		return fmt.Errorf("insert entry: %w", err)
	}
	for _, p := range postings {
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT INTO treasury_postings (entry_id, account, amount, created_at) VALUES (?,?,?,?)`,
			e.ID, p.Account, p.Amount.AttoString(), e.CreatedAt); err != nil {
			return fmt.Errorf("insert posting: %w", err)
		}
		// Maintain the account's running balance in the SAME tx, so a read never re-sums
		// the journal and can never drift from it.
		cur, err := balanceOf(t.tx.QueryRowContext(t.ctx,
			`SELECT balance FROM treasury_accounts WHERE id=?`, p.Account), p.Account)
		if err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(t.ctx,
			`UPDATE treasury_accounts SET balance=? WHERE id=?`, cur.Add(p.Amount).AttoString(), p.Account); err != nil {
			return fmt.Errorf("update balance %q: %w", p.Account, err)
		}
	}
	return nil
}

// balanceOf scans a single balance TEXT column into a money.Amount (a missing row is a
// zero balance, not an error — an account exists only once it has been posted to).
func balanceOf(row *sql.Row, account string) (money.Amount, error) {
	var bal string
	err := row.Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return money.Zero(), nil
	}
	if err != nil {
		return money.Zero(), fmt.Errorf("balance %q: %w", account, err)
	}
	amt, perr := money.ParseInt(bal)
	if perr != nil {
		return money.Zero(), fmt.Errorf("parse balance %q: %w", account, perr)
	}
	return amt, nil
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
