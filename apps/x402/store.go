package x402

// store.go is the settlement ledger's index: ONE row per CLAIMED authorization,
// keyed by a DETERMINISTIC id derived from (payer address, nonce). That id is
// both the replay-dedup key (a PRIMARY KEY, so a second insert of the same
// authorization is atomically refused) AND the metering/finance idempotency ref,
// so the fast in-pod dedup and the ledger's own idempotency agree on ONE key —
// settle-once is enforced in two independent places. Same single-writer WAL Base
// (SQLite) pattern every clients/* store uses.
//
// A row is written when the settlement is CLAIMED and flipped when it is SETTLED,
// so the two states an in-flight payment can be in are a column rather than the
// presence or absence of a row. That is what makes an interrupted settlement
// recoverable: the claim names the payer, the payee and the amount, and both money
// writes are idempotent on the id it is keyed by (see settle + Reconcile).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// Settlement is one x402 authorization this rail has accepted: claimed when the
// row is written, Settled once both halves of the money have moved.
type Settlement struct {
	ID       string // deterministic: settlementID(from, nonce)
	PayerOrg string
	From     string
	Nonce    string
	Resource string
	Payee    string
	PayeeOrg string
	// PayeeSubject is the recipient WALLET's ledger subject — where the credit
	// actually lands. It is on the row, and not re-resolved from wallets when a
	// settlement is completed later, because a claim has to be sufficient on its
	// own: re-resolving would make the sweep depend on the wallets subsystem being
	// reachable, and on the wallet still resolving to the same subject it did when
	// the payer was charged. Neither is guaranteed, and both would silently pay the
	// wrong account.
	PayeeSubject string
	Amount       string // exact 18-dp USD (money.Amount.IntString)
	Network      string // CAIP-2
	SettledVia   string
	TxHash       string
	Settled      bool
	CreatedAt    int64
}

type store struct{ db *sql.DB }

func openStore(dir string) (*store, error) {
	db, err := cek.Open(namespace.System(), "x402", dir)
	if err != nil {
		return nil, fmt.Errorf("open x402 store: %w", err)
	}
	sqlpool.Single(db)
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
  payee_subj  TEXT NOT NULL DEFAULT '',
  amount      TEXT NOT NULL,
  network     TEXT NOT NULL DEFAULT '',
  settled_via TEXT NOT NULL,
  tx_hash     TEXT NOT NULL DEFAULT '',
  settled     INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_settlements_payer ON settlements(payer_org, created_at);
-- The sweep's index: the unsettled rows are the ones Reconcile has to find, and
-- they are a vanishing fraction of the table, so a partial index is the whole scan.
CREATE INDEX IF NOT EXISTS ix_settlements_pending ON settlements(created_at) WHERE settled = 0;
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("x402 migrate: %w", err)
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

// claim atomically claims a settlement id, UNSETTLED. It returns claimed=true when
// THIS call won the row (the caller must move the money), or claimed=false with the
// existing row when the id was already taken (the same settlement, or a replay —
// the caller decides which by comparing terms). The PRIMARY KEY makes the claim
// atomic across concurrent writers: exactly one INSERT succeeds.
func (s *store) claim(ctx context.Context, st *Settlement) (claimed bool, existing *Settlement, err error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO settlements
		 (id, payer_org, from_addr, nonce, resource, payee, payee_org, payee_subj, amount, network, settled_via, tx_hash, settled, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,0,?)`,
		st.ID, st.PayerOrg, st.From, st.Nonce, st.Resource, st.Payee, st.PayeeOrg, st.PayeeSubject,
		st.Amount, st.Network, st.SettledVia, st.TxHash, st.CreatedAt)
	if err != nil {
		return false, nil, fmt.Errorf("claim settlement: %w", err)
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

// markSettled flips a claim to settled once BOTH halves of the money have moved.
// It is the last write of the flow, so until it lands the row stays sweepable.
func (s *store) markSettled(ctx context.Context, id, txHash string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE settlements SET settled=1, tx_hash=? WHERE id=?`, txHash, id); err != nil {
		return fmt.Errorf("mark settled: %w", err)
	}
	return nil
}

const settlementCols = `id, payer_org, from_addr, nonce, resource, payee, payee_org,
	payee_subj, amount, network, settled_via, tx_hash, settled, created_at`

func scanSettlement(sc interface{ Scan(...any) error }) (*Settlement, error) {
	var st Settlement
	err := sc.Scan(&st.ID, &st.PayerOrg, &st.From, &st.Nonce, &st.Resource, &st.Payee,
		&st.PayeeOrg, &st.PayeeSubject, &st.Amount, &st.Network, &st.SettledVia, &st.TxHash,
		&st.Settled, &st.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// get fetches a settlement by id (the receipt lookup + the dedup read).
func (s *store) get(ctx context.Context, id string) (*Settlement, bool, error) {
	st, err := scanSettlement(s.db.QueryRowContext(ctx,
		`SELECT `+settlementCols+` FROM settlements WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get settlement: %w", err)
	}
	return st, true, nil
}

// pending lists claims that were never completed, oldest first — the sweep's whole
// input. `until` excludes rows still on a live request's path, so Reconcile never
// races the flow that owns them.
func (s *store) pending(ctx context.Context, until int64) ([]*Settlement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+settlementCols+` FROM settlements
		   WHERE settled = 0 AND created_at <= ? ORDER BY created_at`, until)
	if err != nil {
		return nil, fmt.Errorf("list pending settlements: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Settlement
	for rows.Next() {
		st, err := scanSettlement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending settlement: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
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
