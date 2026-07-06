// Package sqlstore backs the treasury ledger.Store/ledger.Tx port with
// ledgercore — the ONE Hanzo double-entry engine (github.com/hanzo-fi/ledger) —
// over a single per-deployment SQLite file. It carries the storage concern the
// core deliberately does not, and NOTHING else.
//
// THE CONVERGENCE: the treasury no longer hand-rolls its own double-entry SQL.
// The accounting truth — every account balance and the reserve overdraw guard —
// is computed by ledgercore (postings -> moves -> balances + a hash-chained log),
// the SAME engine the ledger's own store uses, so there is exactly one
// double-entry implementation across the stack. This adapter only translates the
// treasury's vocabulary (int64 cents, a Kind/Program/Ref idempotency key, a
// signed-Posting Entry) onto ledgercore, and holds the revenue-share policy —
// which is Hanzo config, NOT accounting — in a small side table.
//
// The treasury's engine (clients/treasury/ledger) and its on-chain Root
// (a commitment over Entries) are UNCHANGED: this adapter round-trips each Entry
// verbatim (stored as ledgercore transaction metadata), so the Root is identical
// to the previous hand-rolled store's, independent of how ledgercore derives its
// own postings and balances.
//
// It imports the core (ledger), the ONE engine (ledgercore), and the one Hanzo
// SQLite driver — never cloud, zip, or IAM — so it stays Base-compatible and
// travels with the core when it is lifted to hanzoai/finance.
package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags) — the
	// Base storage substrate, identical to every other clients/* store.
	_ "github.com/hanzoai/sqlite"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"

	"github.com/hanzo-fi/ledger/pkg/ledgercore"

	"github.com/hanzoai/cloud/clients/treasury/ledger"
)

const (
	// asset is the single unit the treasury books in (USD minor units). Every
	// treasury account trades this one asset, so a fixed code keeps the mapping
	// onto ledgercore's (account, asset) balances trivial.
	asset = "cents"
	// ledgerName scopes every treasury row inside ledgercore's per-ledger keyspace.
	ledgerName = "treasury"
	// entryMetaKey is the ledgercore-transaction metadata slot holding the whole
	// treasury Entry (with its signed postings) verbatim, so Entries/EntryByRef
	// return the caller's domain object EXACTLY and the treasury Root stays stable.
	entryMetaKey = "hanzo.treasury.entry"
)

// Store is the SQLite-backed treasury ledger persistence. ONE file holds the
// whole chart of accounts, journal and policy for a deployment. It is serialized
// on a single connection so ledgercore's read-then-write balance guard is atomic
// under load.
type Store struct {
	sqldb *sql.DB
	core  *ledgercore.Store
}

// Open opens (creating + migrating) the treasury database at path.
func Open(path string) (*Store, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	sqldb.SetMaxOpenConns(1) // serialize: the reserve balance guard depends on it
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := sqldb.Exec(pragma); err != nil {
			_ = sqldb.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	db := bun.NewDB(sqldb, sqlitedialect.New())
	ctx := context.Background()
	if err := ledgercore.Migrate(ctx, db); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("treasury migrate: %w", err)
	}
	s := &Store{sqldb: sqldb, core: ledgercore.New(db, ledgerName)}
	if err := s.migratePolicy(ctx); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migratePolicy(ctx context.Context) error {
	// Policy is Hanzo config, not double-entry accounting, so it lives beside the
	// ledgercore tables rather than inside them.
	_, err := s.sqldb.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS treasury_policy (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  revenue_share_bps INTEGER NOT NULL DEFAULT 0,
  updated_at        INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		return fmt.Errorf("treasury migrate policy: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.sqldb.Close() }

// ── ledger.Store (read path, no tx) ──────────────────────────────────────────

func (s *Store) Balance(ctx context.Context, account string) (int64, error) {
	bal, err := s.core.GetBalance(ctx, account, asset)
	if err != nil {
		return 0, fmt.Errorf("balance %q: %w", account, err)
	}
	return bal.Int64(), nil
}

func (s *Store) BalancesWithPrefix(ctx context.Context, prefix string) (map[string]int64, error) {
	m, err := s.core.PrefixBalances(ctx, prefix, asset)
	if err != nil {
		return nil, fmt.Errorf("balances prefix %q: %w", prefix, err)
	}
	out := make(map[string]int64, len(m))
	for acct, bal := range m {
		out[acct] = bal.Int64()
	}
	return out, nil
}

func (s *Store) Entries(ctx context.Context, limit int) ([]ledger.Entry, error) {
	// limit <= 0 means EVERY entry (the ledger.Root full scan); a positive limit
	// bounds the admin journal read — ledgercore.ListPosts honours the same rule.
	recs, err := s.core.ListPosts(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	out := make([]ledger.Entry, 0, len(recs))
	for _, r := range recs {
		e, err := entryFromRecord(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) Policy(ctx context.Context) (ledger.Policy, error) {
	var p ledger.Policy
	err := s.sqldb.QueryRowContext(ctx,
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
	_, err := s.sqldb.ExecContext(ctx,
		`INSERT INTO treasury_policy (id, revenue_share_bps, updated_at) VALUES (1,?,?)
		 ON CONFLICT(id) DO UPDATE SET revenue_share_bps=excluded.revenue_share_bps, updated_at=excluded.updated_at`,
		p.RevenueShareBps, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("set policy: %w", err)
	}
	return nil
}

// ── ledger.Store.Tx + ledger.Tx (write path) ─────────────────────────────────

// Tx runs fn inside one ledgercore transaction (a single SQLite transaction), so
// the engine's balance read and the resulting post commit or roll back together —
// two concurrent debits can never both pass the reserve guard.
func (s *Store) Tx(ctx context.Context, fn func(ledger.Tx) error) error {
	return s.core.WithTx(ctx, func(cs *ledgercore.Store) error {
		return fn(&txAdapter{ctx: ctx, core: cs})
	})
}

// txAdapter binds ledger.Tx to a tx-scoped ledgercore.Store.
type txAdapter struct {
	ctx  context.Context
	core *ledgercore.Store
}

func (t *txAdapter) EntryByRef(kind, program, ref string) (ledger.Entry, bool, error) {
	rec, ok, err := t.core.PostByIdempotencyKey(t.ctx, refKey(kind, program, ref))
	if err != nil {
		return ledger.Entry{}, false, fmt.Errorf("entry by ref: %w", err)
	}
	if !ok {
		return ledger.Entry{}, false, nil
	}
	e, err := entryFromRecord(rec)
	if err != nil {
		return ledger.Entry{}, false, err
	}
	return e, true, nil
}

func (t *txAdapter) Balance(account string) (int64, error) {
	bal, err := t.core.GetBalance(t.ctx, account, asset)
	if err != nil {
		return 0, fmt.Errorf("tx balance %q: %w", account, err)
	}
	return bal.Int64(), nil
}

func (t *txAdapter) Insert(e ledger.Entry, postings []ledger.Posting) error {
	e.Postings = postings // store the entry verbatim, carrying its signed postings
	src, dst, amount, err := sourceDest(postings)
	if err != nil {
		return err
	}
	entryJSON, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode entry %q: %w", e.ID, err)
	}
	_, err = t.core.Post(t.ctx,
		[]ledgercore.Posting{{Source: src, Destination: dst, Asset: asset, Amount: big.NewInt(amount)}},
		ledgercore.PostParams{
			IdempotencyKey: refKey(e.Kind, e.Program, e.Ref),
			Reference:      e.Ref,
			Metadata:       map[string]string{entryMetaKey: string(entryJSON)},
			Timestamp:      time.Unix(e.CreatedAt, 0).UTC(),
		})
	if err != nil {
		return fmt.Errorf("insert entry %q: %w", e.ID, err)
	}
	return nil
}

// ── mapping helpers ──────────────────────────────────────────────────────────

// refKey is the idempotency key for an entry — the SAME (kind|program|ref)
// encoding the engine uses internally, so a replay collides on the ledgercore
// idempotency index exactly as the domain intends.
func refKey(kind, program, ref string) string { return kind + "|" + program + "|" + ref }

// sourceDest reduces a balanced treasury entry (a set of signed legs) to the one
// ledgercore posting that moves value from the debit account to the credit
// account. Every entry the engine posts (accrual, seed, payout) is a two-leg
// debit/credit, so this is exact; any other shape is refused at the boundary.
func sourceDest(postings []ledger.Posting) (source, destination string, amount int64, err error) {
	var src, dst *ledger.Posting
	for i := range postings {
		switch {
		case postings[i].Amount < 0:
			if src != nil {
				return "", "", 0, fmt.Errorf("treasury: entry has more than one debit leg")
			}
			src = &postings[i]
		case postings[i].Amount > 0:
			if dst != nil {
				return "", "", 0, fmt.Errorf("treasury: entry has more than one credit leg")
			}
			dst = &postings[i]
		}
	}
	if src == nil || dst == nil {
		return "", "", 0, fmt.Errorf("treasury: entry is not a two-leg debit/credit")
	}
	if src.Amount+dst.Amount != 0 {
		return "", "", 0, fmt.Errorf("treasury: entry postings do not net to zero")
	}
	return src.Account, dst.Account, dst.Amount, nil
}

// entryFromRecord reconstructs the treasury Entry stored verbatim in a ledgercore
// record's metadata — the faithful round-trip that keeps Entries, EntryByRef and
// the treasury Root byte-identical to the hand-rolled store's.
func entryFromRecord(r ledgercore.Record) (ledger.Entry, error) {
	raw, ok := r.Metadata[entryMetaKey]
	if !ok {
		return ledger.Entry{}, fmt.Errorf("treasury: transaction %d is missing entry metadata", r.TransactionID)
	}
	var e ledger.Entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return ledger.Entry{}, fmt.Errorf("treasury: decode entry for transaction %d: %w", r.TransactionID, err)
	}
	return e, nil
}
