package wallets

// store.go is the Hanzo Base (SQLite) persistence for accounts + wallets — the
// SAME single-connection + WAL pattern every clients/* store uses (copied from
// clients/treasury/ledger/sqlstore). ONE wallets.db holds every tenant's rows;
// tenant isolation is the `org` column present on EVERY row and enforced in
// EVERY query. A wallet fetched for a different org returns not-found HERE, in
// the store, not in the handler — the isolation boundary lives in one place.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags),
	// identical to every other clients/* store.
	_ "github.com/hanzoai/sqlite"
)

type store struct {
	db *sql.DB
}

// openStore opens (creating + migrating) wallets.db at path.
func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1) // serialize writes — one file, one writer
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
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS accounts (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_accounts_org ON accounts(org);

CREATE TABLE IF NOT EXISTS wallets (
  id              TEXT PRIMARY KEY,
  org             TEXT NOT NULL,
  account_id      TEXT NOT NULL,
  name            TEXT NOT NULL,
  custody         TEXT NOT NULL,
  tier            TEXT NOT NULL,
  chain           TEXT NOT NULL DEFAULT '',
  address         TEXT NOT NULL,
  key_ref         TEXT NOT NULL,
  finance_account TEXT,
  created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_wallets_org     ON wallets(org);
CREATE INDEX IF NOT EXISTS ix_wallets_finance ON wallets(org, finance_account);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("wallets migrate: %w", err)
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

// ── accounts ─────────────────────────────────────────────────────────────────

func (s *store) createAccount(ctx context.Context, a *Account) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts (id, org, name, created_at) VALUES (?,?,?,?)`,
		a.ID, a.Org, a.Name, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert account: %w", err)
	}
	return nil
}

// getAccount fetches an org's account by id. found=false for another org's id.
func (s *store) getAccount(ctx context.Context, org, id string) (*Account, bool, error) {
	var a Account
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org, name, created_at FROM accounts WHERE id=? AND org=?`, id, org).
		Scan(&a.ID, &a.Org, &a.Name, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get account: %w", err)
	}
	return &a, true, nil
}

func (s *store) listAccounts(ctx context.Context, org string) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, name, created_at FROM accounts WHERE org=? ORDER BY created_at DESC, id DESC`, org)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Org, &a.Name, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── wallets ──────────────────────────────────────────────────────────────────

func (s *store) createWallet(ctx context.Context, w *Wallet) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wallets (id, org, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		w.ID, w.Org, w.AccountID, w.Name, string(w.Custody), string(w.Tier), w.Chain, w.Address, w.KeyRef,
		nullable(w.FinanceAccount), w.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert wallet: %w", err)
	}
	return nil
}

// getWallet fetches a wallet by id SCOPED to org. A wallet owned by a different
// org returns found=false — the tenant-isolation boundary lives here.
func (s *store) getWallet(ctx context.Context, org, id string) (*Wallet, bool, error) {
	var w Wallet
	var custody, tier string
	var finance sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at
		   FROM wallets WHERE id=? AND org=?`, id, org).
		Scan(&w.ID, &w.Org, &w.AccountID, &w.Name, &custody, &tier, &w.Chain, &w.Address, &w.KeyRef, &finance, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get wallet: %w", err)
	}
	w.Custody = Kind(custody)
	w.Tier = Tier(tier)
	w.FinanceAccount = finance.String
	return &w, true, nil
}

func (s *store) listWallets(ctx context.Context, org string) ([]Wallet, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at
		   FROM wallets WHERE org=? ORDER BY created_at DESC, id DESC`, org)
	if err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Wallet{}
	for rows.Next() {
		var w Wallet
		var custody, tier string
		var finance sql.NullString
		if err := rows.Scan(&w.ID, &w.Org, &w.AccountID, &w.Name, &custody, &tier, &w.Chain, &w.Address, &w.KeyRef, &finance, &w.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan wallet: %w", err)
		}
		w.Custody = Kind(custody)
		w.Tier = Tier(tier)
		w.FinanceAccount = finance.String
		out = append(out, w)
	}
	return out, rows.Err()
}

// updateWalletKey persists a rotated wallet's new address + key ref, scoped to org.
func (s *store) updateWalletKey(ctx context.Context, org, id, address, keyRef string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE wallets SET address=?, key_ref=? WHERE id=? AND org=?`, address, keyRef, id, org)
	if err != nil {
		return fmt.Errorf("update wallet key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// walletForFinanceAccount resolves the wallet bound to a finance ledger account,
// scoped to org. The seam by which a treasury reserve signer later BECOMES an MPC
// treasury wallet. found=false when unbound.
func (s *store) walletForFinanceAccount(ctx context.Context, org, financeAccount string) (*Wallet, bool, error) {
	var w Wallet
	var custody, tier string
	var finance sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at
		   FROM wallets WHERE org=? AND finance_account=?`, org, financeAccount).
		Scan(&w.ID, &w.Org, &w.AccountID, &w.Name, &custody, &tier, &w.Chain, &w.Address, &w.KeyRef, &finance, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("wallet for finance account: %w", err)
	}
	w.Custody = Kind(custody)
	w.Tier = Tier(tier)
	w.FinanceAccount = finance.String
	return &w, true, nil
}

// nullable maps "" to a SQL NULL so the finance_account column stays honestly
// unbound rather than an empty string.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
