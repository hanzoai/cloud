package wallet

// store.go is the Hanzo Base persistence for accounts + wallets — a schema on
// hanzo-sql, or the SQLite file the SAME single-connection + WAL pattern every
// clients/* store uses opens (copied from clients/treasury/ledger/sqlstore). Its SQL
// is the dialect both engines share (sdk/dialect). ONE wallets store holds every tenant's rows;
// tenant isolation is the `org` column present on EVERY row and enforced in
// EVERY query. A wallet fetched for a different org returns not-found HERE, in
// the store, not in the handler — the isolation boundary lives in one place.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags),
	// identical to every other clients/* store.
	_ "github.com/hanzoai/sqlite"
)

// The names an owner gave its accounts and wallets (accounts.name, wallets.name) are
// SEALED columns (internal/org, field.go) on both engines, bound to the row's (org, id):
// free text a person chose, and no WHERE or ORDER BY reads it. Left plain: ids, the scope
// (project, agent, account), custody, tier and chain, the wallet's address (a public
// on-chain value that is looked up and rotated) and its key reference (a pointer into
// KMS, not a key), and times.
type store struct {
	db     *sql.DB
	fields *tenure.Fields
}

// subsystem names the store: the system namespace's "wallets". The database and the sealer
// are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "wallets"

var (
	accountKey, accountSealed = []string{"org", "id"}, []string{"name"}
	walletKey, walletSealed   = []string{"org", "id"}, []string{"name"}
)

// openStore opens (creating + migrating) the wallets store under dir. master is
// cloud.Deps.MasterKey; with none the store does not open, so no name is ever written in
// the clear because a key was missing.
func openStore(dir string, master []byte) (*store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("wallets: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("wallets: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*store, error) {
		s := &store{db: db, fields: fields}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
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
  created_at BIGINT NOT NULL
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
  created_at      BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_wallets_org ON wallets(org);
`
	// One transaction through tx alone (Dialect.Migrate): two replicas opening the
	// store at once do not run its DDL against each other.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, base); err != nil {
			return fmt.Errorf("wallets migrate: %w", err)
		}
		// Forward-add every column introduced after the original wallets schema, so a
		// table created before they existed gains them before any index needs them.
		// Each is added only when the table lacks it: a failed ALTER would abort the
		// transaction on PostgreSQL, so none is tried and forgiven.
		for _, add := range []struct{ column, def string }{
			{"project", "TEXT NOT NULL DEFAULT ''"},
			{"agent", "TEXT NOT NULL DEFAULT ''"},
			{"chain", "TEXT NOT NULL DEFAULT ''"},
			{"finance_account", "TEXT"},
		} {
			has, err := d.HasColumn(ctx, tx, "wallets", add.column)
			if err != nil {
				return fmt.Errorf("wallets migrate scope column: %w", err)
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE wallets ADD COLUMN `+add.column+` `+add.def); err != nil {
				return fmt.Errorf("wallets migrate scope column: %w", err)
			}
		}
		// Indexes over the forward-added columns — safe only now that the ALTERs above
		// guarantee project/agent/finance_account exist on every wallets table.
		const scopeIdx = `
CREATE INDEX IF NOT EXISTS ix_wallets_scope   ON wallets(org, project, agent, account_id);
CREATE INDEX IF NOT EXISTS ix_wallets_finance ON wallets(org, finance_account);
`
		if _, err := tx.ExecContext(ctx, scopeIdx); err != nil {
			return fmt.Errorf("wallets migrate scope index: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	// A SQLite file from before the names were sealed holds them in plaintext. Seal them
	// in place, before any read, so the store never reads a value it did not write and
	// cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed, and Upgrade leaves
	// them alone. It is a walk in small transactions of its own, after the schema has
	// committed.
	for _, t := range []struct {
		table       string
		key, sealed []string
	}{{"accounts", accountKey, accountSealed}, {"wallets", walletKey, walletSealed}} {
		if _, err := s.fields.Upgrade(context.Background(), s.db, t.table, t.key, t.sealed); err != nil {
			return fmt.Errorf("wallets migrate: %w", err)
		}
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

// ── accounts ─────────────────────────────────────────────────────────────────

func (s *store) createAccount(ctx context.Context, a *WalletAccount) error {
	r := s.fields.Row("accounts", a.Org, a.ID)
	name := r.Seal("name", a.Name)
	if err := r.Err(); err != nil {
		return fmt.Errorf("insert account: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts (id, org, name, created_at) VALUES ($1,$2,$3,$4)`,
		a.ID, a.Org, name, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert account: %w", err)
	}
	return nil
}

// getAccount fetches an org's account by id. found=false for another org's id.
func (s *store) getAccount(ctx context.Context, org, id string) (*WalletAccount, bool, error) {
	var a WalletAccount
	err := s.db.QueryRowContext(ctx,
		`SELECT id, org, name, created_at FROM accounts WHERE id=$1 AND org=$2`, id, org).
		Scan(&a.ID, &a.Org, &a.Name, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get account: %w", err)
	}
	if err := s.openAccount(&a); err != nil {
		return nil, false, fmt.Errorf("get account: %w", err)
	}
	return &a, true, nil
}

// openAccount opens an account's sealed name. The row key is read off the row itself, so a
// ciphertext carried to another account or tenant fails here.
func (s *store) openAccount(a *WalletAccount) error {
	r := s.fields.Row("accounts", a.Org, a.ID)
	a.Name = r.Open("name", a.Name)
	return r.Err()
}

func (s *store) listAccounts(ctx context.Context, org string) ([]WalletAccount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, name, created_at FROM accounts WHERE org=$1 ORDER BY created_at DESC, id DESC`, org)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []WalletAccount{}
	for rows.Next() {
		var a WalletAccount
		if err := rows.Scan(&a.ID, &a.Org, &a.Name, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		if err := s.openAccount(&a); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── wallets ──────────────────────────────────────────────────────────────────

func (s *store) createWallet(ctx context.Context, w *Wallet) error {
	r := s.fields.Row("wallets", w.Org, w.ID)
	name := r.Seal("name", w.Name)
	if err := r.Err(); err != nil {
		return fmt.Errorf("insert wallet: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wallets (id, org, project, agent, account_id, name, custody, tier, chain, address, key_ref, finance_account, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		w.ID, w.Org, w.Project, w.Agent, w.AccountID, name, string(w.Custody), string(w.Tier), w.Chain, w.Address, w.KeyRef,
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
func (s *store) scanWallet(sc interface{ Scan(...any) error }) (*Wallet, error) {
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
	r := s.fields.Row("wallets", w.Org, w.ID)
	w.Name = r.Open("name", w.Name)
	if err := r.Err(); err != nil {
		return nil, err
	}
	return &w, nil
}

// getWallet fetches a wallet by id SCOPED to org. A wallet owned by a different
// org returns found=false — the tenant-isolation boundary lives here.
func (s *store) getWallet(ctx context.Context, org, id string) (*Wallet, bool, error) {
	w, err := s.scanWallet(s.db.QueryRowContext(ctx,
		`SELECT `+walletCols+` FROM wallets WHERE id=$1 AND org=$2`, id, org))
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
	where := []string{"org=$1"}
	args := []any{sc.Org}
	if sc.Project != "" {
		args = append(args, sc.Project)
		where = append(where, "project=$"+strconv.Itoa(len(args)))
	}
	if sc.Agent != "" {
		args = append(args, sc.Agent)
		where = append(where, "agent=$"+strconv.Itoa(len(args)))
	}
	if sc.AccountID != "" {
		args = append(args, sc.AccountID)
		where = append(where, "account_id=$"+strconv.Itoa(len(args)))
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
		w, err := s.scanWallet(rows)
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
		`UPDATE wallets SET address=$1, key_ref=$2 WHERE id=$3 AND org=$4`, address, keyRef, id, org)
	if err != nil {
		return fmt.Errorf("update wallet key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// nullable maps "" to a SQL NULL so the finance_account column stays honestly
// unbound rather than an empty string.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
