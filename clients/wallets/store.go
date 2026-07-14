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
	"strings"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags),
	// identical to every other clients/* store.
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

type store struct {
	db *sql.DB
}

// openStore opens (creating + migrating) wallets.db at path.
func openStore(path string) (*store, error) {
	db, err := cek.Open(path)
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
	// Migrate in dependency order: base tables, then forward-add columns, then the
	// indexes that reference those columns. A CREATE INDEX on a column that a
	// pre-existing table lacks fails ("no such column"), so any index over a
	// forward-added column MUST come after the ALTERs — never in the base DDL.
	const base = `
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
  project         TEXT NOT NULL DEFAULT '',
  agent           TEXT NOT NULL DEFAULT '',
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
CREATE INDEX IF NOT EXISTS ix_wallets_org ON wallets(org);
`
	if _, err := s.db.Exec(base); err != nil {
		return fmt.Errorf("wallets migrate: %w", err)
	}
	// Forward-add every column introduced after the original wallets schema, so a
	// table created before they existed gains them before any index needs them. On
	// a fresh DB the CREATE above already has them, so the "duplicate column" error
	// is the expected no-op and is tolerated.
	for _, alter := range []string{
		`ALTER TABLE wallets ADD COLUMN project         TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wallets ADD COLUMN agent           TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wallets ADD COLUMN chain           TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE wallets ADD COLUMN finance_account TEXT`,
	} {
		if _, err := s.db.Exec(alter); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("wallets migrate scope column: %w", err)
		}
	}
	// Indexes over the forward-added columns — safe only now that the ALTERs above
	// guarantee project/agent/finance_account exist on every wallets table.
	const scopeIdx = `
CREATE INDEX IF NOT EXISTS ix_wallets_scope   ON wallets(org, project, agent, account_id);
CREATE INDEX IF NOT EXISTS ix_wallets_finance ON wallets(org, finance_account);
`
	if _, err := s.db.Exec(scopeIdx); err != nil {
		return fmt.Errorf("wallets migrate scope index: %w", err)
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
		`INSERT INTO wallets (id, org, project, agent, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.ID, w.Org, w.Project, w.Agent, w.AccountID, w.Name, string(w.Custody), string(w.Tier), w.Chain, w.Address, w.KeyRef,
		nullable(w.FinanceAccount), w.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert wallet: %w", err)
	}
	return nil
}

// walletCols is the ONE column list every wallet row read scans, in the order
// scanWallet reads them — so the select projection and the scan can never drift.
const walletCols = `id, org, project, agent, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at`

// scanWallet reads one wallets row (walletCols order) into a Wallet.
func scanWallet(sc interface{ Scan(...any) error }) (*Wallet, error) {
	var w Wallet
	var custody, tier string
	var finance sql.NullString
	if err := sc.Scan(&w.ID, &w.Org, &w.Project, &w.Agent, &w.AccountID, &w.Name, &custody, &tier,
		&w.Chain, &w.Address, &w.KeyRef, &finance, &w.CreatedAt); err != nil {
		return nil, err
	}
	w.Custody = Kind(custody)
	w.Tier = Tier(tier)
	w.FinanceAccount = finance.String
	return &w, nil
}

// getWallet fetches a wallet by id SCOPED to org. A wallet owned by a different
// org returns found=false — the tenant-isolation boundary lives here.
func (s *store) getWallet(ctx context.Context, org, id string) (*Wallet, bool, error) {
	w, err := scanWallet(s.db.QueryRowContext(ctx,
		`SELECT `+walletCols+` FROM wallets WHERE id=? AND org=?`, id, org))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get wallet: %w", err)
	}
	return w, true, nil
}

// listWallets lists an org's wallets. It is the org-only case of the ONE scope
// lookup path (listWalletsByScope with just the org bound).
func (s *store) listWallets(ctx context.Context, org string) ([]Wallet, error) {
	return s.listWalletsByScope(ctx, Scope{Org: org})
}

// listWalletsByScope is the ONE scope-filtered lookup path: org is ALWAYS a bound
// predicate (the isolation boundary), and each non-empty narrowing (project,
// agent, account) adds a bound equality that narrows WITHIN the org. An empty
// narrowing is a wildcard, so Scope{Org: x} lists the whole org and a fuller scope
// lists exactly that sub-scope — the mirror of keyRef's derivation, for reads.
func (s *store) listWalletsByScope(ctx context.Context, sc Scope) ([]Wallet, error) {
	where := []string{"org=?"}
	args := []any{sc.Org}
	if sc.Project != "" {
		where = append(where, "project=?")
		args = append(args, sc.Project)
	}
	if sc.Agent != "" {
		where = append(where, "agent=?")
		args = append(args, sc.Agent)
	}
	if sc.AccountID != "" {
		where = append(where, "account_id=?")
		args = append(args, sc.AccountID)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+walletCols+` FROM wallets WHERE `+strings.Join(where, " AND ")+
			` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Wallet{}
	for rows.Next() {
		w, err := scanWallet(rows)
		if err != nil {
			return nil, fmt.Errorf("scan wallet: %w", err)
		}
		out = append(out, *w)
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
	w, err := scanWallet(s.db.QueryRowContext(ctx,
		`SELECT `+walletCols+` FROM wallets WHERE org=? AND finance_account=?`, org, financeAccount))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("wallet for finance account: %w", err)
	}
	return w, true, nil
}

// nullable maps "" to a SQL NULL so the finance_account column stays honestly
// unbound rather than an empty string.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
