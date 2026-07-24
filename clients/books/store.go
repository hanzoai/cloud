package books

// store.go — one per-org (HIP-0302 physical-file isolated) SQLite holding the org's
// books: the seeded chart of accounts, immutable GL Entry rows, the Payment Ledger Entry
// subledger for AR/AP legs, and the ingestion cursor. A distinct org resolves to a
// distinct {DataDir}/orgs/{slug}/books.db (sandbox → books-sandbox.db), so one org's
// ledger can never reach another's rows.
//
// IMMUTABILITY. GL Entry rows are append-only: the store has no UPDATE or DELETE of a
// gl_entry, and every write goes through post() inside one transaction gated by the
// voucher's idempotency key. A correction is a NEW reversing voucher, never an edit —
// the ledger of record is the sum of what was posted.

import (
	"context"
	"database/sql"
	"fmt"
)

type store struct {
	db *sql.DB
}

// openStore wraps an org DB (already opened + pragma'd by cloud.OrgDB) into the books
// store, running its migration and seeding the fixed chart. It is the open func
// cloud.OrgStore calls once per org file.
func openStore(db *sql.DB) (*store, error) {
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.seed(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) Close() error { return s.db.Close() }

func (s *store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS account (
  number TEXT PRIMARY KEY,
  name   TEXT NOT NULL,
  type   TEXT NOT NULL,
  party  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS voucher (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  source_kind TEXT NOT NULL,
  source_id   TEXT NOT NULL,
  posting_at  TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  UNIQUE(source_kind, source_id)
);
CREATE TABLE IF NOT EXISTS gl_entry (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  voucher_id  INTEGER NOT NULL REFERENCES voucher(id),
  posting_at  TEXT NOT NULL,
  account     TEXT NOT NULL REFERENCES account(number),
  debit       INTEGER NOT NULL DEFAULT 0,
  credit      INTEGER NOT NULL DEFAULT 0,
  against     TEXT NOT NULL DEFAULT '',
  source_kind TEXT NOT NULL,
  source_id   TEXT NOT NULL,
  remarks     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS gl_entry_account ON gl_entry(account);
CREATE TABLE IF NOT EXISTS payment_ledger_entry (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  voucher_id  INTEGER NOT NULL REFERENCES voucher(id),
  posting_at  TEXT NOT NULL,
  account     TEXT NOT NULL,
  party_type  TEXT NOT NULL,
  amount      INTEGER NOT NULL,
  source_kind TEXT NOT NULL,
  source_id   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cursor (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  last_at TEXT NOT NULL DEFAULT '',
  last_id TEXT NOT NULL DEFAULT ''
);
-- bank_txn is the normalized bank transaction the connectors (OFX/CSV import,
-- Plaid, Teller) land here. It is UNIQUE per (connector, external_id): re-importing
-- a statement or re-syncing a connector can never create a second row, which is the
-- bank-layer half of the idempotency guarantee (the GL voucher UNIQUE key is the
-- other). raw holds the connector's original JSON for audit; matched_voucher points
-- at the GL voucher source_id this txn posted/cleared through (empty when none).
CREATE TABLE IF NOT EXISTS bank_txn (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  connector       TEXT NOT NULL,
  external_id     TEXT NOT NULL,
  posted_at       TEXT NOT NULL,
  amount_cents    INTEGER NOT NULL,
  currency        TEXT NOT NULL DEFAULT 'usd',
  direction       TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  merchant        TEXT NOT NULL DEFAULT '',
  raw             TEXT NOT NULL DEFAULT '',
  matched_voucher TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL,
  UNIQUE(connector, external_id)
);
CREATE INDEX IF NOT EXISTS bank_txn_status ON bank_txn(status);
-- bank_cursor is one connector's per-org pull high-water mark (Plaid/Teller). The
-- per-org store is already org-scoped, so the connector name alone is the key.
CREATE TABLE IF NOT EXISTS bank_cursor (
  connector TEXT PRIMARY KEY,
  cursor    TEXT NOT NULL DEFAULT ''
);
-- bank_question is the clarifying-question ledger: an UNMATCHED bank inflow raises
-- one here instead of guessing revenue. UNIQUE per (connector, external_id) so a
-- re-sync never asks the same question twice.
CREATE TABLE IF NOT EXISTS bank_question (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  connector   TEXT NOT NULL,
  external_id TEXT NOT NULL,
  prompt      TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'open',
  created_at  TEXT NOT NULL,
  UNIQUE(connector, external_id)
);
-- bank_settlement records which processor CAPTURE (a commerce DEBIT on 1010) a bank inflow
-- has cleared. It is the per-capture consumed-state that keeps reconciliation honest: each
-- capture settles to the bank at most ONCE, so a coincidental same-amount inflow can never
-- pre-consume the capture the real settlement needs, and a second identical settlement finds
-- no open capture. capture_source_id is the consumed capture's voucher source_id (PK ⇒
-- consumed once); (connector, external_id) is the inflow that consumed it (UNIQUE ⇒ one
-- inflow clears one capture, and a re-sync of that inflow is an idempotent no-op).
CREATE TABLE IF NOT EXISTS bank_settlement (
  capture_source_id TEXT PRIMARY KEY,
  connector         TEXT NOT NULL,
  external_id       TEXT NOT NULL,
  settled_at        TEXT NOT NULL,
  UNIQUE(connector, external_id)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("books migrate: %w", err)
	}
	return nil
}

// seed inserts the fixed chart of accounts, idempotently — a re-open of an existing
// books.db is a no-op. The chart is a constant, so a row is never updated (an account's
// meaning is fixed by its number).
func (s *store) seed(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range chartOfAccounts {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO account (number, name, type, party) VALUES (?,?,?,?)`,
			a.Number, a.Name, string(a.Type), string(a.Party)); err != nil {
			return fmt.Errorf("books seed %s: %w", a.Number, err)
		}
	}
	return tx.Commit()
}

// post is the SINGLE ledger-write choke point. It runs the voucher through processGLMap
// (merge → toggle → round-off → the Σdebit==Σcredit invariant) and, only if it balances,
// writes the immutable GL Entry rows + a Payment Ledger Entry per AR/AP leg in ONE
// transaction. It is idempotent by (source_kind, source_id): a voucher already posted
// returns posted=false and writes nothing (the UNIQUE(voucher) row is the guard, so a
// re-run of ingestion never double-books).
func (s *store) post(ctx context.Context, v Voucher, allowance int64) (posted bool, err error) {
	legs, err := processGLMap(v.Legs, allowance, RoundOff)
	if err != nil {
		return false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO voucher (source_kind, source_id, posting_at, description)
		 VALUES (?,?,?,?) ON CONFLICT(source_kind, source_id) DO NOTHING`,
		v.SourceKind, v.SourceID, v.PostingAt, v.Description)
	if err != nil {
		return false, fmt.Errorf("books post voucher: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil // already posted — idempotent skip
	}
	vid, err := res.LastInsertId()
	if err != nil {
		return false, err
	}

	against := counterAccounts(legs)
	for _, l := range legs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gl_entry (voucher_id, posting_at, account, debit, credit, against, source_kind, source_id, remarks)
			 VALUES (?,?,?,?,?,?,?,?,?)`,
			vid, v.PostingAt, l.Account, l.Debit, l.Credit, against[l.Account], v.SourceKind, v.SourceID, v.Description); err != nil {
			return false, fmt.Errorf("books post gl_entry: %w", err)
		}
		// AR/AP legs also land in the Payment Ledger Entry subledger (party outstanding).
		if a, ok := accountByNumber[l.Account]; ok && a.Party != NoParty {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO payment_ledger_entry (voucher_id, posting_at, account, party_type, amount, source_kind, source_id)
				 VALUES (?,?,?,?,?,?,?)`,
				vid, v.PostingAt, l.Account, string(a.Party), l.Debit-l.Credit, v.SourceKind, v.SourceID); err != nil {
				return false, fmt.Errorf("books post payment_ledger_entry: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// counterAccounts returns, per account, a summary of the OTHER accounts in the voucher —
// the "against" column ERPNext keeps so a single GL row shows what it moved against.
func counterAccounts(legs []Leg) map[string]string {
	out := make(map[string]string, len(legs))
	for _, a := range legs {
		var s string
		for _, b := range legs {
			if b.Account == a.Account {
				continue
			}
			if s != "" {
				s += ","
			}
			s += b.Account
		}
		out[a.Account] = s
	}
	return out
}

// GLRow is one persisted GL Entry, as the read API surfaces it.
type GLRow struct {
	ID         int64  `json:"id"`
	PostingAt  string `json:"postingAt"`
	Account    string `json:"account"`
	Debit      int64  `json:"debit"`
	Credit     int64  `json:"credit"`
	Against    string `json:"against,omitempty"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
	Remarks    string `json:"remarks,omitempty"`
}

// listGL returns the most recent GL Entry rows (newest first), capped at limit.
func (s *store) listGL(ctx context.Context, limit int) ([]GLRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, posting_at, account, debit, credit, against, source_kind, source_id, remarks
		 FROM gl_entry ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("books listGL: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []GLRow{}
	for rows.Next() {
		var r GLRow
		if err := rows.Scan(&r.ID, &r.PostingAt, &r.Account, &r.Debit, &r.Credit, &r.Against, &r.SourceKind, &r.SourceID, &r.Remarks); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// listAccounts returns the seeded chart of accounts (number-ascending within class,
// exactly the seed order via id ordering is not guaranteed, so order by number).
func (s *store) listAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT number, name, type, party FROM account ORDER BY number`)
	if err != nil {
		return nil, fmt.Errorf("books listAccounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Account{}
	for rows.Next() {
		var a Account
		var typ, party string
		if err := rows.Scan(&a.Number, &a.Name, &typ, &party); err != nil {
			return nil, err
		}
		a.Type = AccountType(typ)
		a.Party = PartyType(party)
		out = append(out, a)
	}
	return out, rows.Err()
}

// sums is the debit/credit total per account over an optional posting-time window:
// startExclusive (>) and endInclusive (<=); either "" drops that bound. It is the ONE
// aggregation the trial balance composes into opening/period/closing.
type sums map[string][2]int64 // account → {debit, credit}

func (s *store) sums(ctx context.Context, startExclusive, endInclusive string) (sums, error) {
	q := `SELECT account, COALESCE(SUM(debit),0), COALESCE(SUM(credit),0) FROM gl_entry`
	var args []any
	var where []string
	if startExclusive != "" {
		where = append(where, "posting_at > ?")
		args = append(args, startExclusive)
	}
	if endInclusive != "" {
		where = append(where, "posting_at <= ?")
		args = append(args, endInclusive)
	}
	for i, w := range where {
		if i == 0 {
			q += " WHERE " + w
		} else {
			q += " AND " + w
		}
	}
	q += " GROUP BY account"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("books sums: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := sums{}
	for rows.Next() {
		var acct string
		var d, c int64
		if err := rows.Scan(&acct, &d, &c); err != nil {
			return nil, err
		}
		out[acct] = [2]int64{d, c}
	}
	return out, rows.Err()
}

// cursorState is the ingestion high-water mark for this org's books.
type cursorState struct {
	LastAt string
	LastID string
}

func (s *store) cursor(ctx context.Context) (cursorState, error) {
	row := s.db.QueryRowContext(ctx, `SELECT last_at, last_id FROM cursor WHERE id = 1`)
	var cs cursorState
	err := row.Scan(&cs.LastAt, &cs.LastID)
	if err == sql.ErrNoRows {
		return cursorState{}, nil
	}
	if err != nil {
		return cursorState{}, fmt.Errorf("books cursor: %w", err)
	}
	return cs, nil
}

func (s *store) setCursor(ctx context.Context, cs cursorState) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cursor (id, last_at, last_id) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET last_at = excluded.last_at, last_id = excluded.last_id`,
		cs.LastAt, cs.LastID)
	if err != nil {
		return fmt.Errorf("books setCursor: %w", err)
	}
	return nil
}
