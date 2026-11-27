package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// The house book is anchored on Hanzo L1 from whichever replica an operator reaches, so
// what the anchor remembers is the book's, not a replica's: the last root committed
// (which the next status compares the journal to), the org whose treasury wallet signs
// (the signer is bound by name and any replica binds it again), and the lock that keeps
// two replicas from sending from one address at one nonce.

// Anchor is one root committed to the chain.
type Anchor struct {
	Root    string // 0x… ledger root committed
	TxHash  string // the transaction that carried it
	Block   uint64 // the block it landed in
	At      int64  // unix seconds
	Entries int    // journal entries the root covers
}

// Anchor reads the last root committed, and whether one was.
func (s *Store) Anchor(ctx context.Context) (Anchor, bool, error) {
	var a Anchor
	var block int64
	err := s.db.QueryRowContext(ctx,
		`SELECT root, tx_hash, block, anchored_at, entries FROM treasury_anchor WHERE id=1 AND tx_hash<>''`).
		Scan(&a.Root, &a.TxHash, &block, &a.At, &a.Entries)
	if errors.Is(err, sql.ErrNoRows) {
		return Anchor{}, false, nil
	}
	if err != nil {
		return Anchor{}, false, fmt.Errorf("read anchor: %w", err)
	}
	a.Block = uint64(block)
	return a, true, nil
}

// SetAnchor records the root committed, replacing the one before. The signer it was
// signed by is left as it is.
func (s *Store) SetAnchor(ctx context.Context, a Anchor) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO treasury_anchor (id, root, tx_hash, block, anchored_at, entries) VALUES (1,$1,$2,$3,$4,$5)
		 ON CONFLICT(id) DO UPDATE SET root=excluded.root, tx_hash=excluded.tx_hash, block=excluded.block,
		   anchored_at=excluded.anchored_at, entries=excluded.entries`,
		a.Root, a.TxHash, int64(a.Block), a.At, a.Entries); err != nil {
		return fmt.Errorf("record anchor: %w", err)
	}
	return nil
}

// SignerOrg is the org whose treasury wallet signs anchors, or "" when none was bound.
func (s *Store) SignerOrg(ctx context.Context) (string, error) {
	var org string
	err := s.db.QueryRowContext(ctx, `SELECT signer_org FROM treasury_anchor WHERE id=1`).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read anchor signer: %w", err)
	}
	return org, nil
}

// SetSignerOrg records the org whose treasury wallet signs anchors from now on. The last
// anchor is left as it is.
func (s *Store) SetSignerOrg(ctx context.Context, org string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO treasury_anchor (id, signer_org) VALUES (1,$1)
		 ON CONFLICT(id) DO UPDATE SET signer_org=excluded.signer_org`, org); err != nil {
		return fmt.Errorf("record anchor signer: %w", err)
	}
	return nil
}

// Anchoring takes the right to send an anchor transaction and answers how to give it
// back. A transaction names the sender's nonce, so two sent from one address at once
// carry the same one and the chain keeps only one of them; whoever holds this sends.
//
// On hanzo-sql it is an advisory lock of the book's schema, in a transaction of its own
// that outlives the request (dialect.Held), so replicas take it in turn and one that
// dies holding it frees it with its connection. On a file there is one process, and the
// lock is a mutex of the store's: the file's one connection is not held through a call
// to the chain.
func (s *Store) Anchoring(ctx context.Context) (release func(), err error) {
	d := dialect.Of(s.db)
	if !d.Postgres() {
		s.anchoring.Lock()
		return s.anchoring.Unlock, nil
	}
	_, held, err := dialect.Held(ctx, dialect.LockWait, func(ctx context.Context) (*sql.Tx, error) {
		return d.Serialize(ctx, s.db, "anchor")
	})
	if err != nil {
		return nil, fmt.Errorf("treasury: lock the anchor: %w", err)
	}
	return func() { _ = held() }, nil
}
