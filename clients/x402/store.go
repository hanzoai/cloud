package x402

// store.go is the settlement ledger's index: ONE row per settled authorization,
// keyed by a DETERMINISTIC id derived from (payer address, nonce). That id is
// both the replay-dedup key (a PRIMARY KEY, so a second insert of the same
// authorization is atomically refused) AND the metering/finance idempotency ref,
// so the fast in-pod dedup and the ledger's own idempotency agree on ONE key —
// settle-once is enforced in two independent places. Same single-writer WAL Base
// (SQLite) pattern every clients/* store uses.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// Settlement is the recorded outcome of one settled x402 authorization.
type Settlement struct {
	ID         string // deterministic: settlementID(from, nonce)
	PayerOrg   string
	From       string
	Nonce      string
	Resource   string
	Payee      string
	PayeeOrg   string
	Amount     string // exact 18-dp USD (money.Amount.IntString)
	SettledVia string
	TxHash     string
	CreatedAt  int64
}

type store struct{ db *sql.DB }

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
	const ddl = `
CREATE TABLE IF NOT EXISTS settlements (
  id          TEXT PRIMARY KEY,
  payer_org   TEXT NOT NULL,
  from_addr   TEXT NOT NULL,
  nonce       TEXT NOT NULL,
  resource    TEXT NOT NULL,
  payee       TEXT NOT NULL,
  payee_org   TEXT NOT NULL,
  amount      TEXT NOT NULL,
  settled_via TEXT NOT NULL,
  tx_hash     TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_settlements_payer ON settlements(payer_org, created_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("x402 migrate: %w", err)
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

// record atomically claims a settlement id. It returns recorded=true when THIS
// call won the row (the caller must settle), or recorded=false with the existing
// row when the id was already taken (a retry or a replay — the caller decides
// which by comparing terms). The PRIMARY KEY makes the claim atomic across
// concurrent writers: exactly one INSERT succeeds.
func (s *store) record(ctx context.Context, st *Settlement) (recorded bool, existing *Settlement, err error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO settlements
		 (id, payer_org, from_addr, nonce, resource, payee, payee_org, amount, settled_via, tx_hash, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		st.ID, st.PayerOrg, st.From, st.Nonce, st.Resource, st.Payee, st.PayeeOrg, st.Amount,
		st.SettledVia, st.TxHash, st.CreatedAt)
	if err != nil {
		return false, nil, fmt.Errorf("record settlement: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil, nil
	}
	ex, _, err := s.get(ctx, st.ID)
	if err != nil {
		return false, nil, err
	}
	return false, ex, nil
}

// get fetches a settlement by id (the receipt lookup + the dedup read).
func (s *store) get(ctx context.Context, id string) (*Settlement, bool, error) {
	var st Settlement
	err := s.db.QueryRowContext(ctx,
		`SELECT id, payer_org, from_addr, nonce, resource, payee, payee_org, amount, settled_via, tx_hash, created_at
		   FROM settlements WHERE id=?`, id).
		Scan(&st.ID, &st.PayerOrg, &st.From, &st.Nonce, &st.Resource, &st.Payee, &st.PayeeOrg,
			&st.Amount, &st.SettledVia, &st.TxHash, &st.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get settlement: %w", err)
	}
	return &st, true, nil
}

// getScoped fetches a settlement by id but ONLY within payerOrg — the tenant
// isolation boundary for the receipt-lookup API, so one org can never read
// another's settlement.
func (s *store) getScoped(ctx context.Context, payerOrg, id string) (*Settlement, bool, error) {
	st, found, err := s.get(ctx, id)
	if err != nil || !found || st.PayerOrg != payerOrg {
		return nil, false, err
	}
	return st, true, nil
}
