package validator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// The ONE Hanzo "sqlite" driver; blank import registers it.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes:
//
//	errNotFound → 404, errConflict → 409, errChallenge → 400/401.
var (
	errNotFound  = errors.New("validators: not found")
	errConflict  = errors.New("validators: slot already claimed by another org")
	errChallenge = errors.New("validators: challenge missing, expired, or already used")
)

// Store is the validators database. ONE database — the deployment's own
// "validators" subsystem, a SQLite file or a schema on hanzo-sql —
// holds every org's entitlements, the owner-gated registration queue, and the
// short-lived wallet-signature challenges. Tenant isolation is the `org`
// column, enforced on EVERY org-scoped query. On SQLite, MaxOpenConns(1)
// serializes writes against the single-writer file (mirrors clients/ads). Its SQL
// is the dialect both engines share (sdk/dialect).
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "validators", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

// migrate creates the three tables. Idempotent (IF NOT EXISTS).
//
//   - validator_slots: the entitlement. token_id is the PRIMARY KEY because the
//     tokenId IS the validator slot and an ERC-721 token has ONE owner, so a
//     slot is claimed once globally; the ownerOf proof in nft.go is what admits
//     a claim, and this key makes a second org's claim of the same slot a 409.
//   - validator_registrations: the OWNER-GATED queue. A row is enqueued
//     `pending_owner_approval` and is NEVER auto-submitted to any P-Chain — the
//     owner co-signs out of band (Phase 2). No submit path exists in this code.
//   - validator_challenges: single-use, TTL-bounded nonces binding a wallet
//     signature to an org (replay defense).
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS validator_slots (
  token_id     BIGINT PRIMARY KEY,
  org          TEXT NOT NULL,
  wallet       TEXT NOT NULL,
  node_id      TEXT NOT NULL,
  kms_ref      TEXT NOT NULL,
  cr_name      TEXT NOT NULL,
  namespace    TEXT NOT NULL,
  bls_pubkey   TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'provisioning',
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_validator_slots_org ON validator_slots(org, updated_at);

CREATE TABLE IF NOT EXISTS validator_registrations (
  id           TEXT PRIMARY KEY,
  token_id     BIGINT NOT NULL,
  org          TEXT NOT NULL,
  node_id      TEXT NOT NULL,
  bls_pubkey   TEXT NOT NULL DEFAULT '',
  weight       BIGINT NOT NULL DEFAULT 0,
  status       TEXT NOT NULL DEFAULT 'pending_owner_approval',
  created_at   BIGINT NOT NULL,
  updated_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_validator_reg_org    ON validator_registrations(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_validator_reg_status ON validator_registrations(status, updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS ux_validator_reg_token ON validator_registrations(token_id);

CREATE TABLE IF NOT EXISTS validator_challenges (
  nonce        TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  expires_at   BIGINT NOT NULL,
  consumed     BIGINT NOT NULL DEFAULT 0,
  created_at   BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_validator_challenges_org ON validator_challenges(org);
`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := dialect.Of(s.db).Migrate(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}); err != nil {
		return fmt.Errorf("validators migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// ── slots (entitlements) ────────────────────────────────────────────────────

// Slot is an org's claimed validator slot: the on-chain NFT tokenId (== slot),
// the wallet that proved ownership, the generated node identity, and where its
// node lives. Status walks provisioning → node_created → registration_queued.
type Slot struct {
	TokenID   uint64 `json:"tokenId"`
	Org       string `json:"-"`
	Wallet    string `json:"wallet"`
	NodeID    string `json:"nodeID"`
	KMSRef    string `json:"-"`
	CRName    string `json:"crName"`
	Namespace string `json:"namespace"`
	BLSPubkey string `json:"blsPubkey"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

const slotCols = `token_id,org,wallet,node_id,kms_ref,cr_name,namespace,bls_pubkey,status,created_at,updated_at`

func scanSlot(sc interface{ Scan(...any) error }) (Slot, error) {
	var s Slot
	err := sc.Scan(&s.TokenID, &s.Org, &s.Wallet, &s.NodeID, &s.KMSRef, &s.CRName,
		&s.Namespace, &s.BLSPubkey, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// GetSlot returns a slot by tokenId REGARDLESS of org (the token is global). The
// caller compares .Org to enforce tenant ownership. errNotFound when absent.
func (s *Store) GetSlot(ctx context.Context, tokenID uint64) (Slot, error) {
	return getSlot(ctx, s.db, tokenID)
}

func getSlot(ctx context.Context, q dialect.Querier, tokenID uint64) (Slot, error) {
	row := q.QueryRowContext(ctx, `SELECT `+slotCols+` FROM validator_slots WHERE token_id=$1`, tokenID)
	sl, err := scanSlot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Slot{}, errNotFound
	}
	if err != nil {
		return Slot{}, fmt.Errorf("get slot: %w", err)
	}
	return sl, nil
}

// ClaimSlot inserts a new slot entitlement. It is idempotent for the SAME org
// (a re-claim of a slot this org already holds returns the existing row) and
// fails with errConflict if a DIFFERENT org already holds the slot — a second
// org can only reach here by owning the NFT, which an ERC-721 forbids, so this
// is the defense-in-depth backstop.
func (s *Store) ClaimSlot(ctx context.Context, sl Slot) (Slot, error) {
	if err := dialect.Text(sl.Org, sl.Wallet, sl.NodeID, sl.KMSRef, sl.CRName, sl.Namespace, sl.BLSPubkey, sl.Status); err != nil {
		return Slot{}, fmt.Errorf("insert slot: %w", err)
	}
	// Read, then write, under the store's one write lock: two claims of one slot
	// cannot both find it free.
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Slot{}, fmt.Errorf("claim slot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := getSlot(ctx, tx, sl.TokenID)
	if err == nil {
		if existing.Org != sl.Org {
			return Slot{}, errConflict
		}
		return existing, nil // idempotent re-claim by the same org
	}
	if !errors.Is(err, errNotFound) {
		return Slot{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO validator_slots (`+slotCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		sl.TokenID, sl.Org, sl.Wallet, sl.NodeID, sl.KMSRef, sl.CRName, sl.Namespace,
		sl.BLSPubkey, sl.Status, sl.CreatedAt, sl.UpdatedAt); err != nil {
		return Slot{}, fmt.Errorf("insert slot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Slot{}, fmt.Errorf("claim slot: %w", err)
	}
	return sl, nil
}

// SetSlotStatus advances a slot's lifecycle status (provisioning → node_created
// → registration_queued). Scoped by tokenId; the caller has already verified org.
func (s *Store) SetSlotStatus(ctx context.Context, tokenID uint64, status string, updatedAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE validator_slots SET status=$1, updated_at=$2 WHERE token_id=$3`, status, updatedAt, tokenID)
	if err != nil {
		return fmt.Errorf("set slot status: %w", err)
	}
	return nil
}

// ListSlots returns an org's claimed slots, most-recently-updated first.
func (s *Store) ListSlots(ctx context.Context, org string, limit int) ([]Slot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+slotCols+` FROM validator_slots WHERE org=$1 ORDER BY updated_at DESC LIMIT $2`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list slots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Slot, 0, 16)
	for rows.Next() {
		sl, err := scanSlot(rows)
		if err != nil {
			return nil, fmt.Errorf("scan slot: %w", err)
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// ── registration queue (owner-gated) ────────────────────────────────────────

// Registration is a queued, OWNER-GATED request to add the node to a validator
// set via a classic AddPermissionlessValidatorTx. It is enqueued
// `pending_owner_approval` and NEVER auto-submitted — the owner co-signs out of
// band (Phase 2). This package has NO P-Chain submit path.
type Registration struct {
	ID        string `json:"id"`
	TokenID   uint64 `json:"tokenId"`
	Org       string `json:"-"`
	NodeID    string `json:"nodeID"`
	BLSPubkey string `json:"blsPubkey"`
	Weight    uint64 `json:"weight"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

const regCols = `id,token_id,org,node_id,bls_pubkey,weight,status,created_at,updated_at`

func scanReg(sc interface{ Scan(...any) error }) (Registration, error) {
	var r Registration
	err := sc.Scan(&r.ID, &r.TokenID, &r.Org, &r.NodeID, &r.BLSPubkey, &r.Weight,
		&r.Status, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// EnqueueRegistration inserts an owner-gated registration. Idempotent per
// tokenId (the unique index): a re-enqueue returns the existing row so a retried
// provision never queues a duplicate.
func (s *Store) EnqueueRegistration(ctx context.Context, r Registration) (Registration, error) {
	if err := dialect.Text(r.ID, r.Org, r.NodeID, r.BLSPubkey, r.Status); err != nil {
		return Registration{}, fmt.Errorf("insert registration: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Registration{}, fmt.Errorf("enqueue registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := getRegByToken(ctx, tx, r.TokenID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, errNotFound) {
		return Registration{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO validator_registrations (`+regCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		r.ID, r.TokenID, r.Org, r.NodeID, r.BLSPubkey, r.Weight, r.Status, r.CreatedAt, r.UpdatedAt); err != nil {
		return Registration{}, fmt.Errorf("insert registration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Registration{}, fmt.Errorf("enqueue registration: %w", err)
	}
	return r, nil
}

func (s *Store) getRegByToken(ctx context.Context, tokenID uint64) (Registration, error) {
	return getRegByToken(ctx, s.db, tokenID)
}

func getRegByToken(ctx context.Context, q dialect.Querier, tokenID uint64) (Registration, error) {
	row := q.QueryRowContext(ctx, `SELECT `+regCols+` FROM validator_registrations WHERE token_id=$1`, tokenID)
	r, err := scanReg(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Registration{}, errNotFound
	}
	if err != nil {
		return Registration{}, fmt.Errorf("get registration: %w", err)
	}
	return r, nil
}

// ListRegistrations returns an org's registration queue, newest first.
func (s *Store) ListRegistrations(ctx context.Context, org string, limit int) ([]Registration, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+regCols+` FROM validator_registrations WHERE org=$1 ORDER BY updated_at DESC LIMIT $2`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list registrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Registration, 0, 16)
	for rows.Next() {
		r, err := scanReg(rows)
		if err != nil {
			return nil, fmt.Errorf("scan registration: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── challenges (single-use wallet-signature nonces) ─────────────────────────

// PutChallenge stores a fresh nonce for an org with an absolute expiry.
func (s *Store) PutChallenge(ctx context.Context, nonce, org string, expiresAt, createdAt int64) error {
	if err := dialect.Text(nonce, org); err != nil {
		return fmt.Errorf("insert challenge: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO validator_challenges (nonce,org,expires_at,consumed,created_at) VALUES ($1,$2,$3,0,$4)`,
		nonce, org, expiresAt, createdAt); err != nil {
		return fmt.Errorf("insert challenge: %w", err)
	}
	return nil
}

// ConsumeChallenge atomically validates + burns a nonce: it must exist, belong
// to org, be unconsumed, and be unexpired at `now`. It returns errChallenge on
// any of those failing. The single UPDATE...WHERE is the atomic compare-and-set
// that makes a nonce strictly single-use even under concurrent claims.
func (s *Store) ConsumeChallenge(ctx context.Context, nonce, org string, now int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE validator_challenges SET consumed=1
		 WHERE nonce=$1 AND org=$2 AND consumed=0 AND expires_at>=$3`, nonce, org, now)
	if err != nil {
		return fmt.Errorf("consume challenge: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errChallenge
	}
	return nil
}

// PurgeExpiredChallenges deletes consumed/expired nonces (housekeeping; called
// opportunistically on issue so the table never grows unbounded).
func (s *Store) PurgeExpiredChallenges(ctx context.Context, now int64) {
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM validator_challenges WHERE consumed=1 OR expires_at<$1`, now)
}
