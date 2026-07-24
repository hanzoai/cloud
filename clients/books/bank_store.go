package books

// bank_store.go — the per-org store reads/writes for the bank engine's tables (bank_txn,
// bank_cursor, bank_question), added to the SAME org books.db as the GL. Keeping them
// here (not in store.go) leaves the P0 spine's store methods untouched while the schema
// itself lives in the one migration choke point. Every method is org-implicit: the store
// is already resolved to one org's file, so org is never a parameter.

import (
	"context"
	"database/sql"
	"fmt"
)

// bank txn statuses — the lifecycle of a normalized bank row.
const (
	statusPosted     = "posted"     // outflow booked to an expense
	statusReconciled = "reconciled" // inflow matched a Square-clearing settlement → cleared
	statusUnmatched  = "unmatched"  // inflow with no match → clarifying question raised
	statusTransfer   = "transfer"   // own-account move → recorded, no GL effect
)

// BankTxnRow is a persisted bank_txn as the read API surfaces it.
type BankTxnRow struct {
	Connector      string    `json:"connector"`
	ExternalID     string    `json:"externalId"`
	PostedAt       string    `json:"postedAt"`
	AmountCents    int64     `json:"amountCents"`
	Currency       string    `json:"currency"`
	Direction      Direction `json:"direction"`
	Description    string    `json:"description,omitempty"`
	Merchant       string    `json:"merchant,omitempty"`
	MatchedVoucher string    `json:"matchedVoucher,omitempty"`
	Status         string    `json:"status"`
}

// bankTxnStatus returns the recorded status of a bank_txn (found=false if never seen). It
// is the fast idempotency check: a row already present has already been mapped/posted, so
// the engine skips it. Correctness does not RELY on this — post() and the question UNIQUE
// key are the real guards — but it avoids re-classifying a settled row.
func (s *store) bankTxnStatus(ctx context.Context, connector, externalID string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT status FROM bank_txn WHERE connector = ? AND external_id = ?`, connector, externalID)
	var status string
	switch err := row.Scan(&status); err {
	case nil:
		return status, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("books bankTxnStatus: %w", err)
	}
}

// insertBankTxn records the normalized row with its final status. INSERT ... ON CONFLICT
// DO NOTHING makes it idempotent under (connector, external_id), so a concurrent or
// repeated map never writes a second row.
func (s *store) insertBankTxn(ctx context.Context, bt BankTxn, raw, matchedVoucher, status string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bank_txn (connector, external_id, posted_at, amount_cents, currency, direction, description, merchant, raw, matched_voucher, status)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(connector, external_id) DO NOTHING`,
		bt.Connector, bt.ExternalID, bt.PostedAt, bt.AmountCents, bt.Currency, string(bt.Direction),
		bt.Description, bt.Merchant, raw, matchedVoucher, status)
	if err != nil {
		return fmt.Errorf("books insertBankTxn: %w", err)
	}
	return nil
}

// listBankTxns returns the most recent bank rows (newest first), capped at limit.
func (s *store) listBankTxns(ctx context.Context, limit int) ([]BankTxnRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT connector, external_id, posted_at, amount_cents, currency, direction, description, merchant, matched_voucher, status
		 FROM bank_txn ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("books listBankTxns: %w", err)
	}
	return scanBankTxns(rows)
}

// listUnreconciled returns the bank inflows still awaiting a human answer (status
// 'unmatched'), newest first — the rows GET /v1/books/bank/unreconciled surfaces.
func (s *store) listUnreconciled(ctx context.Context) ([]BankTxnRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT connector, external_id, posted_at, amount_cents, currency, direction, description, merchant, matched_voucher, status
		 FROM bank_txn WHERE status = ? ORDER BY id DESC`, statusUnmatched)
	if err != nil {
		return nil, fmt.Errorf("books listUnreconciled: %w", err)
	}
	return scanBankTxns(rows)
}

func scanBankTxns(rows *sql.Rows) ([]BankTxnRow, error) {
	defer func() { _ = rows.Close() }()
	out := []BankTxnRow{}
	for rows.Next() {
		var r BankTxnRow
		var dir string
		if err := rows.Scan(&r.Connector, &r.ExternalID, &r.PostedAt, &r.AmountCents, &r.Currency,
			&dir, &r.Description, &r.Merchant, &r.MatchedVoucher, &r.Status); err != nil {
			return nil, err
		}
		r.Direction = Direction(dir)
		out = append(out, r)
	}
	return out, rows.Err()
}

// BankQuestion is one open clarifying question about an unmatched bank inflow —
// the persistence row. It adapts to the founder-facing canonical [Question]
// (anomalies.go) via [BankQuestion.toQuestion] so the one /v1/books/questions
// surface can present anomaly and bank questions through a single type.
type BankQuestion struct {
	Connector  string `json:"connector"`
	ExternalID string `json:"externalId"`
	Prompt     string `json:"prompt"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
}

// toQuestion maps a bank clarifying question onto the canonical founder-facing
// Question shape so both anomaly and bank questions render through one contract.
func (q BankQuestion) toQuestion() Question {
	return Question{
		ID:       q.Connector + ":" + q.ExternalID,
		Kind:     "unmatched-deposit",
		Text:     q.Prompt,
		PostedAt: q.CreatedAt,
	}
}

// raiseQuestion records a clarifying question for an unmatched inflow, idempotently under
// (connector, external_id) — a re-sync of the same unmatched deposit never asks twice.
func (s *store) raiseQuestion(ctx context.Context, connector, externalID, prompt, createdAt string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bank_question (connector, external_id, prompt, status, created_at)
		 VALUES (?,?,?,'open',?)
		 ON CONFLICT(connector, external_id) DO NOTHING`,
		connector, externalID, prompt, createdAt)
	if err != nil {
		return fmt.Errorf("books raiseQuestion: %w", err)
	}
	return nil
}

// listQuestions returns the open clarifying questions, newest first.
func (s *store) listQuestions(ctx context.Context) ([]BankQuestion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT connector, external_id, prompt, status, created_at
		 FROM bank_question WHERE status = 'open' ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("books listQuestions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []BankQuestion{}
	for rows.Next() {
		var q BankQuestion
		if err := rows.Scan(&q.Connector, &q.ExternalID, &q.Prompt, &q.Status, &q.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// bankCursor reads one connector's pull high-water mark ("" if never synced).
func (s *store) bankCursor(ctx context.Context, connector string) (string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT cursor FROM bank_cursor WHERE connector = ?`, connector)
	var cur string
	switch err := row.Scan(&cur); err {
	case nil, sql.ErrNoRows:
		return cur, nil
	default:
		return "", fmt.Errorf("books bankCursor: %w", err)
	}
}

// setBankCursor advances one connector's pull high-water mark.
func (s *store) setBankCursor(ctx context.Context, connector, cursor string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO bank_cursor (connector, cursor) VALUES (?, ?)
		 ON CONFLICT(connector) DO UPDATE SET cursor = excluded.cursor`, connector, cursor)
	if err != nil {
		return fmt.Errorf("books setBankCursor: %w", err)
	}
	return nil
}

// claimCapture atomically reserves ONE specific unconsumed processor capture for a bank
// inflow to clear, and reports whether it succeeded. A capture is a commerce DEBIT on 1010
// (booked when the processor captured the charge); each may settle to the bank exactly once.
// In a single transaction it either (a) returns the capture THIS same inflow already claimed
// — crash-safe idempotency, so a replay after a mid-reconcile crash re-clears the same
// capture rather than a different one — or (b) claims the OLDEST commerce capture of exactly
// `amount` posted in [fromInclusive, toInclusive] that no prior settlement has consumed,
// recording the consumption so no other inflow can re-claim it. ok=false means no unconsumed
// capture of that amount is in-window: the inflow is not a settlement we can place, so the
// caller must raise a question rather than clear (never over-clear 1010, never guess).
func (s *store) claimCapture(ctx context.Context, amount int64, fromInclusive, toInclusive, connector, externalID, settledAt string) (string, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()

	// (a) idempotent replay: this inflow already claimed a capture.
	var existing string
	switch err := tx.QueryRowContext(ctx,
		`SELECT capture_source_id FROM bank_settlement WHERE connector = ? AND external_id = ?`,
		connector, externalID).Scan(&existing); err {
	case nil:
		return existing, true, nil
	case sql.ErrNoRows:
		// fall through to claim a fresh capture
	default:
		return "", false, fmt.Errorf("books claimCapture lookup: %w", err)
	}

	// (b) claim the oldest unconsumed commerce capture of exactly this amount in-window. Only
	// a commerce_txn DEBIT on 1010 is a capture — a clearing move is a CREDIT, and a non-
	// commerce debit is not a processor capture — so source_kind is part of the key.
	var captureID string
	switch err := tx.QueryRowContext(ctx,
		`SELECT source_id FROM gl_entry
		 WHERE account = ? AND source_kind = 'commerce_txn' AND debit = ?
		   AND posting_at >= ? AND posting_at <= ?
		   AND source_id NOT IN (SELECT capture_source_id FROM bank_settlement)
		 ORDER BY posting_at ASC, id ASC LIMIT 1`,
		SquareClearing, amount, fromInclusive, toInclusive).Scan(&captureID); err {
	case nil:
		// claimed below
	case sql.ErrNoRows:
		return "", false, nil // no open capture of this amount in-window → not a settlement
	default:
		return "", false, fmt.Errorf("books claimCapture select: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO bank_settlement (capture_source_id, connector, external_id, settled_at)
		 VALUES (?,?,?,?)`,
		captureID, connector, externalID, settledAt); err != nil {
		return "", false, fmt.Errorf("books claimCapture insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return captureID, true, nil
}
